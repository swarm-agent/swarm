import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useMatchRoute, useNavigate } from "@tanstack/react-router";
import {
  ArrowDown,
  CheckCircle2,
  CircleAlert,
  Loader2,
  LoaderCircle,
  XCircle,
} from "lucide-react";
import { Button } from "../../../../components/ui/button";
import { cn } from "../../../../lib/cn";
import { ChatMarkdown } from "./chat-markdown";
import {
  buildStructuredToolMessage,
  parseStructuredToolMessage,
} from "../services/tool-message";
import {
  selectDesktopPlanExecutionView,
  type DesktopPlanExecutionView,
  type RenderedSessionMessages,
} from "../../state/desktop-v3-cache-selectors";
import type {
  DesktopV3CacheState,
  LiveRunOverlay,
  MessageSnapshot,
  PendingUserMessage,
} from "../../state/desktop-v3-cache-types";
import {
  dispatchDesktopV3Cache,
  useDesktopV3CacheSelector,
} from "../../state/desktop-v3-cache-store";
import type {
  DesktopPermissionRecord,
  DesktopSessionRecord,
} from "../../types/realtime";
import type {
  StructuredToolMessage,
  ToolMessageState,
  AgentStateRecord,
  SessionPreferenceRecord,
  ChatMessageRecord,
} from "../types/chat";
import {
  getDesktopSessionStopTarget,
  resolveDesktopChatRouteFromSession,
  type DesktopChatRoute,
} from "../services/chat-routing";
import {
  agentStateQueryOptions,
  modelOptionsQueryOptions,
  uiSettingsQueryKey,
  uiSettingsQueryOptions,
} from "../../../queries/query-options";
import {
  normalizeSessionMode,
  normalizeThinkingTagsEnabled,
  type DesktopSessionMode,
} from "../../settings/swarm/types/swarm-settings";
import { saveThinkingTagsSetting } from "../../settings/swarm/mutations/save-thinking-tags-setting";
import {
  formatContextWindow,
  effectiveContextWindow,
  normalizeModelID,
  normalizeProviderID,
} from "../services/model-options";
import {
  preferenceFromAgentModelLock,
  resolveDesktopV3AgentModelLock,
} from "../services/agent-model-preferences";
import { DesktopV3AgenticComposer } from "./desktop-v3-agentic-composer";
import {
  DesktopV3ChatHeader,
  type DesktopV3ChatHeaderSessionActions,
} from "./desktop-v3-chat-header";
import { listAuthCredentials } from "../../settings/queries/list-auth-credentials";
import { desktopProviderNeedsAuth } from "../services/auth-needs";
import { formatConversationMarkdown, loadCompleteConversationMessages, sanitizeTranscriptFilename } from "../services/transcript-export";
import {
  buildDesktopV3RunStatusModel,
  type DesktopV3RunStatusModel,
} from "./desktop-v3-run-status";
import type { DesktopSlashCommand } from "../services/slash-commands";
import {
  sessionV3AgentSettingsMutationResponse,
  sessionV3ModeSettingsMutationResponse,
  sessionV3PreferenceSettingsMutationResponse,
  updateSessionV3Agent,
  updateSessionV3Mode,
  updateSessionV3Preference,
  stopSessionV3Run,
} from "../../session-v3/api";
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  type DesktopV3ExistingMessageOperation,
} from "../../session-v3/existing-session-flow";
import { compactDesktopV3Session } from "../../session-v3/compact-session-flow";
import {
  acceptAndContinueDesktopPlanCheckpoint,
  archiveDesktopV3Sessions,
  resolveDesktopPlanBlockedCheckpoint,
  resumeDesktopPlanAutomatic,
  resumeDesktopPlanCheckpointed,
} from "../../session-v3/plan-execution-api";
import { fetchAndApplyDesktopV3PlanSnapshot } from "../../state/desktop-v3-session-api";
import {
  fetchSessionMessages,
  resolveSessionPermission,
} from "../queries/chat-queries";
import {
  refreshAgentModelMutationCaches,
  updateAgentProfile,
} from "../queries/agent-preference-mutations";
import type { AgentModelControlConfirmInput } from "./agent-model-control";
import { DesktopPermissionModal } from "../../permissions/components/desktop-permission-modal";
import {
  isPlanProposalPermission,
  permissionRequiresApproval,
} from "../../permissions/services/permission-payload";
import {
  DesktopInlinePlanReviewCard,
  structuredPlanDocumentFromPermission,
} from "./desktop-inline-plan-review-card";
import { DesktopPlanAgentSidecar } from "./desktop-plan-agent-sidecar";
import { DesktopPlanExecutionSidebar, type DesktopPlanExecutionSidebarActionInput } from "./desktop-plan-execution-sidebar";

const EMPTY_AGENT_STATE: AgentStateRecord = {
  profiles: [],
  activePrimary: "",
  activeSubagent: {},
  version: 0,
  providerDefaultsPreview: null,
  toolInventory: null,
};

function desktopPlanLifecycleComplete(response: {
  execution_summary?: unknown;
}): boolean {
  const summary = response.execution_summary;
  if (!summary || typeof summary !== "object" || Array.isArray(summary))
    return false;
  const planComplete = (summary as Record<string, unknown>).plan_complete;
  return planComplete === true || planComplete === "true";
}

function optionKey(provider: string, model: string, contextMode = ""): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`;
}

function metadataString(
  metadata: Record<string, unknown> | null | undefined,
  key: string,
): string {
  const value = metadata?.[key];
  return typeof value === "string" ? value.trim() : "";
}

function recordObject(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function policyString(
  policy: unknown,
  camelKey: string,
  snakeKey: string,
): string {
  const record = recordObject(policy);
  if (!record) return "";
  const value = record[camelKey] ?? record[snakeKey];
  return typeof value === "string" ? value.trim() : "";
}

function policyLockedPreference(
  policy: unknown,
): SessionPreferenceRecord | null {
  const record = recordObject(policy);
  if (!record || record.locked !== true) return null;
  const preference = normalizePreference(record.preference);
  return preference.provider && preference.model ? preference : null;
}

function normalizePreference(value: unknown): SessionPreferenceRecord {
  const record =
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : {};
  const nested =
    record.preference &&
    typeof record.preference === "object" &&
    !Array.isArray(record.preference)
      ? (record.preference as Record<string, unknown>)
      : record;
  return {
    provider: String(nested.provider ?? "").trim(),
    model: String(nested.model ?? "").trim(),
    thinking: String(nested.thinking ?? "").trim(),
    serviceTier: String(nested.serviceTier ?? nested.service_tier ?? "").trim(),
    contextMode: String(nested.contextMode ?? nested.context_mode ?? "").trim(),
    updatedAt:
      typeof nested.updatedAt === "number"
        ? nested.updatedAt
        : typeof nested.updated_at === "number"
          ? nested.updated_at
          : 0,
  };
}

type NormalizedUsageSummary = {
  provider: string;
  model: string;
  source: string;
  contextWindow: number;
  turnCount: number;
  inputTokens: number;
  outputTokens: number;
  thinkingTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  remainingTokens: number;
  totalTokens: number;
  serviceTier: string;
  estimatedCostUSD: number;
  updatedAt: number;
};

function normalizeUsageSummary(value: unknown): NormalizedUsageSummary | null {
  const record =
    value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  if (!record) return null;
  const contextWindow = finiteNumber(
    record.context_window ?? record.contextWindow,
  );
  const totalTokens = finiteNumber(record.total_tokens ?? record.totalTokens);
  const remainingTokens = finiteNumber(
    record.remaining_tokens ?? record.remainingTokens,
  );
  const updatedAt = finiteNumber(record.updated_at ?? record.updatedAt);
  if (
    contextWindow <= 0 &&
    totalTokens <= 0 &&
    remainingTokens <= 0 &&
    updatedAt <= 0
  )
    return null;
  return {
    provider: String(record.provider ?? "").trim(),
    model: String(record.model ?? "").trim(),
    source: String(record.source ?? "").trim(),
    contextWindow,
    turnCount: finiteNumber(record.turn_count ?? record.turnCount),
    inputTokens: finiteNumber(record.input_tokens ?? record.inputTokens),
    outputTokens: finiteNumber(record.output_tokens ?? record.outputTokens),
    thinkingTokens: finiteNumber(
      record.thinking_tokens ?? record.thinkingTokens,
    ),
    cacheReadTokens: finiteNumber(
      record.cache_read_tokens ?? record.cacheReadTokens,
    ),
    cacheWriteTokens: finiteNumber(
      record.cache_write_tokens ?? record.cacheWriteTokens,
    ),
    remainingTokens,
    totalTokens,
    serviceTier: String(record.service_tier ?? record.serviceTier ?? "").trim(),
    estimatedCostUSD: finiteNumber(
      record.estimated_cost_usd ?? record.estimatedCostUSD,
    ),
    updatedAt,
  };
}

function finiteNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.max(0, value)
    : 0;
}

function formatDesktopV3ContextLabel(
  contextWindow: number,
  remainingTokens?: number,
): string {
  if (contextWindow <= 0) return "ctx";
  if (typeof remainingTokens === "number") {
    return `${formatContextWindow(remainingTokens)} / ${formatContextWindow(contextWindow)} ctx`;
  }
  return `${formatContextWindow(contextWindow)} ctx`;
}

function formatDesktopV3ContextTooltip(
  contextWindow: number,
  usage: NormalizedUsageSummary | null,
): string {
  if (usage && contextWindow > 0) {
    const parts = [
      `Remaining context ${formatContextWindow(usage.remainingTokens)} of ${formatContextWindow(contextWindow)}.`,
      `Current session context ${usage.totalTokens.toLocaleString()} tokens (${usage.inputTokens.toLocaleString()} input + ${usage.outputTokens.toLocaleString()} output).`,
    ];
    if (usage.cacheReadTokens > 0 || usage.cacheWriteTokens > 0) {
      parts.push(
        `Cache: ${usage.cacheReadTokens.toLocaleString()} read, ${usage.cacheWriteTokens.toLocaleString()} write.`,
      );
    }
    if (usage.turnCount > 0)
      parts.push(
        `Provider usage snapshots: ${usage.turnCount.toLocaleString()}.`,
      );
    if (usage.serviceTier) parts.push(`Service tier: ${usage.serviceTier}.`);
    return parts.join(" ");
  }
  if (contextWindow > 0)
    return `Context window ${formatContextWindow(contextWindow)}`;
  return "Context window unavailable";
}

function desktopV3ContextUsagePercent(
  contextWindow: number,
  usage: NormalizedUsageSummary | null,
): number {
  if (!usage || contextWindow <= 0) return 0;
  const usedTokens = Math.max(0, contextWindow - usage.remainingTokens);
  return (usedTokens / contextWindow) * 100;
}

function serviceTierFromPreference(
  preference: SessionPreferenceRecord,
): string {
  return preference.serviceTier.trim().toLowerCase() || "standard";
}

function preferencesEqual(
  left: SessionPreferenceRecord,
  right: SessionPreferenceRecord,
): boolean {
  return (
    left.provider === right.provider &&
    left.model === right.model &&
    left.thinking === right.thinking &&
    left.serviceTier === right.serviceTier &&
    left.contextMode === right.contextMode
  );
}

function firstNonEmpty(...values: string[]): string {
  for (const value of values) {
    const normalized = value.trim();
    if (normalized) return normalized;
  }
  return "";
}

function comparePendingPermissions(
  left: DesktopPermissionRecord,
  right: DesktopPermissionRecord,
): number {
  return (
    (left.permissionRequestedAt || left.createdAt || left.updatedAt || 0) -
      (right.permissionRequestedAt ||
        right.createdAt ||
        right.updatedAt ||
        0) || left.id.localeCompare(right.id)
  );
}

type DesktopV3InputSettingsSnapshot = {
  sessionId: string;
  mode: DesktopSessionMode;
  agent: string;
  preference: SessionPreferenceRecord;
};

function buildDesktopV3ExistingSettingsSnapshot(input: {
  sessionId: string;
  metadata?: Record<string, unknown>;
  session?: DesktopSessionRecord | null;
  cacheSession?: { mode?: string; metadata?: Record<string, unknown> } | null;
  cachedPreference: SessionPreferenceRecord;
  agentModelPolicy?: unknown;
}): DesktopV3InputSettingsSnapshot {
  return {
    sessionId: input.sessionId,
    mode: normalizeSessionMode(input.session?.mode || input.cacheSession?.mode),
    agent: firstNonEmpty(
      metadataString(input.metadata, "agent_name"),
      metadataString(input.metadata, "resolved_agent_name"),
      policyString(input.agentModelPolicy, "agentName", "agent_name"),
      policyString(
        input.agentModelPolicy,
        "resolvedAgentName",
        "resolved_agent_name",
      ),
    ),
    preference: input.cachedPreference,
  };
}

function planExecutionViewsEqual(
  left: DesktopPlanExecutionView | null,
  right: DesktopPlanExecutionView | null,
): boolean {
  if (left === right) return true;
  if (!left || !right) return false;
  return (
    left.plan === right.plan &&
    left.activeCheckpoint === right.activeCheckpoint &&
    left.activeCheckpointId === right.activeCheckpointId &&
    left.status === right.status &&
    left.policyMode === right.policyMode &&
    left.policyShape === right.policyShape &&
    left.currentRunId === right.currentRunId &&
    left.currentSessionId === right.currentSessionId &&
    left.freshContext === right.freshContext &&
    left.reviewRequired === right.reviewRequired &&
    left.blocked === right.blocked &&
    left.failed === right.failed &&
    left.completed === right.completed &&
    left.attemptCount === right.attemptCount
  );
}

function pendingPermissionsEqual(
  left: DesktopPermissionRecord[],
  right: DesktopPermissionRecord[],
): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  for (let index = 0; index < left.length; index += 1) {
    const a = left[index];
    const b = right[index];
    if (!a || !b) return false;
    if (
      a.id !== b.id ||
      a.sessionId !== b.sessionId ||
      a.runId !== b.runId ||
      a.callId !== b.callId ||
      a.toolName !== b.toolName ||
      a.toolArguments !== b.toolArguments ||
      a.approvedArguments !== b.approvedArguments ||
      !permissionSavedRuleEqual(a.savedRule, b.savedRule) ||
      a.status !== b.status ||
      a.decision !== b.decision ||
      a.reason !== b.reason ||
      a.requirement !== b.requirement ||
      a.mode !== b.mode ||
      a.createdAt !== b.createdAt ||
      a.updatedAt !== b.updatedAt ||
      a.resolvedAt !== b.resolvedAt ||
      a.permissionRequestedAt !== b.permissionRequestedAt
    )
      return false;
  }
  return true;
}

function permissionSavedRuleEqual(
  left: DesktopPermissionRecord["savedRule"],
  right: DesktopPermissionRecord["savedRule"],
): boolean {
  if (left === right) return true;
  if (!left || !right) return false;
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.decision === right.decision &&
    left.tool === right.tool &&
    left.pattern === right.pattern &&
    left.createdAt === right.createdAt &&
    left.updatedAt === right.updatedAt
  );
}

function modelControlDetail(input: {
  locked: boolean;
  customized: boolean;
  modelLabel: string;
  thinking: string;
  serviceTier: string;
}): string {
  return `${input.modelLabel || "Model"} · thinking ${input.thinking || "off"} · tier ${input.serviceTier}`;
}

function scrollFollowKeyPart(value: unknown): string {
  if (value === null || value === undefined) return "";
  switch (typeof value) {
    case "string":
    case "number":
    case "boolean":
      return String(value);
    default:
      return "";
  }
}

export type DesktopV3RenderItem =
  | {
      type: "plan-break";
      message: MessageSnapshot;
      headline: string;
      details: string[];
      timelineSeq?: number;
    }
  | {
      type: "plan-final-handoff";
      message: MessageSnapshot;
      headline: string;
      body: string;
      timelineSeq?: number;
    }
  | {
      type: "plan-blocked-handoff";
      message: MessageSnapshot;
      headline: string;
      body: string;
      timelineSeq?: number;
    }
  | {
      type: "message";
      message: MessageSnapshot;
      timelineSeq?: number;
      renderKey?: string;
    }
  | { type: "pending-user"; message: PendingUserMessage; timelineSeq?: number }
  | {
      type: "live-assistant";
      id: string;
      content: string;
      timelineSeq?: number;
    }
  | {
      type: "live-reasoning";
      id: string;
      text: string;
      summary: string;
      state: NonNullable<LiveRunOverlay["reasoning"]>["state"];
      startedAt: number | null;
      completedAt?: number | null;
      timelineSeq?: number;
    }
  | {
      type: "live-tool";
      id: string;
      tool: LiveRunOverlay["toolCallsByCallId"][string];
      timelineSeq?: number;
    }
  | { type: "live-working"; id: string; timelineSeq?: number };

type DesktopV3ScrollBehavior = "auto" | "smooth";

const DESKTOP_V3_BOTTOM_BUFFER_PX = 140;
const DESKTOP_V3_HISTORY_AUTOLOAD_TOP_PX = 320;
const DESKTOP_V3_ACTIVITY_FOLLOW_MS = 30_000;
const DESKTOP_V3_RESIZE_FOLLOW_MS = 750;
const DESKTOP_V3_USER_SCROLL_INTENT_MS = 600;

function desktopV3BottomDistance(element: HTMLElement): number {
  return Math.max(
    0,
    element.scrollHeight - element.scrollTop - element.clientHeight,
  );
}

export function useDesktopV3StickyBottomScroll(options: {
  resetKey: string;
  itemCount: number;
  followKey?: string;
  bottomBufferPx?: number;
}) {
  const bottomBufferPx = options.bottomBufferPx ?? DESKTOP_V3_BOTTOM_BUFFER_PX;
  const scrollContainerRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const autoFollowRef = useRef(true);
  const suppressAutoFollowOnceRef = useRef(false);
  const smoothFollowUntilRef = useRef(0);
  const forceFollowUntilRef = useRef(0);
  const lastScrollEventAtRef = useRef(0);
  const frameRef = useRef<number | null>(null);
  const lastScrollHeightRef = useRef(0);
  const preserveTopAnchorRef = useRef<{
    scrollHeight: number;
    scrollTop: number;
  } | null>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);
  const [hasUnseenLatest, setHasUnseenLatest] = useState(false);

  const cancelScheduledScroll = useCallback(() => {
    if (frameRef.current === null) return;
    window.cancelAnimationFrame(frameRef.current);
    frameRef.current = null;
  }, []);

  const setPinnedStateFromElement = useCallback(
    (element: HTMLElement) => {
      const pinned = desktopV3BottomDistance(element) <= bottomBufferPx;
      const now = Date.now();
      const keepFollowingSmoothJump =
        !pinned && smoothFollowUntilRef.current > now;
      const keepFollowingActiveContent =
        !pinned && forceFollowUntilRef.current > now;
      autoFollowRef.current =
        pinned || keepFollowingSmoothJump || keepFollowingActiveContent;
      setIsAtBottom(
        pinned || keepFollowingSmoothJump || keepFollowingActiveContent,
      );
      if (pinned || keepFollowingActiveContent) setHasUnseenLatest(false);
      return pinned;
    },
    [bottomBufferPx],
  );

  const pinToLatest = useCallback(
    (
      options: { behavior?: DesktopV3ScrollBehavior; followMs?: number } = {},
    ) => {
      const element = scrollContainerRef.current;
      const now = Date.now();
      autoFollowRef.current = true;
      forceFollowUntilRef.current = Math.max(
        forceFollowUntilRef.current,
        now + (options.followMs ?? DESKTOP_V3_ACTIVITY_FOLLOW_MS),
      );
      setIsAtBottom(true);
      setHasUnseenLatest(false);
      if (!element) return;
      if (options.behavior === "smooth") {
        smoothFollowUntilRef.current = Math.max(
          smoothFollowUntilRef.current,
          now + 1200,
        );
        element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
        return;
      }
      smoothFollowUntilRef.current = 0;
      element.scrollTop = element.scrollHeight;
    },
    [],
  );

  const scrollToBottom = useCallback(
    (behavior: DesktopV3ScrollBehavior = "auto") => {
      pinToLatest({ behavior, followMs: DESKTOP_V3_ACTIVITY_FOLLOW_MS });
    },
    [pinToLatest],
  );

  const preserveScrollPositionForPrepend = useCallback(() => {
    const element = scrollContainerRef.current;
    if (!element) return;
    preserveTopAnchorRef.current = {
      scrollHeight: element.scrollHeight,
      scrollTop: element.scrollTop,
    };
    suppressAutoFollowOnceRef.current = true;
    autoFollowRef.current = false;
    forceFollowUntilRef.current = 0;
  }, []);

  const scheduleAutoFollow = useCallback(
    (
      scheduleOptions: {
        forceUnseen?: boolean;
        forceFollow?: boolean;
        followMs?: number;
      } = {},
    ) => {
      const element = scrollContainerRef.current;
      const nextScrollHeight = element?.scrollHeight ?? 0;
      const contentAdvanced =
        scheduleOptions.forceUnseen ||
        nextScrollHeight > lastScrollHeightRef.current + 1;
      lastScrollHeightRef.current = nextScrollHeight;
      if (suppressAutoFollowOnceRef.current) {
        suppressAutoFollowOnceRef.current = false;
        return;
      }
      if (scheduleOptions.forceFollow) {
        pinToLatest({
          followMs: scheduleOptions.followMs ?? DESKTOP_V3_ACTIVITY_FOLLOW_MS,
        });
        return;
      }
      if (!autoFollowRef.current) {
        if (contentAdvanced && !preserveTopAnchorRef.current)
          setHasUnseenLatest(true);
        return;
      }
      if (frameRef.current !== null) return;
      frameRef.current = window.requestAnimationFrame(() => {
        frameRef.current = null;
        if (!autoFollowRef.current) return;
        pinToLatest({
          followMs: scheduleOptions.followMs ?? DESKTOP_V3_RESIZE_FOLLOW_MS,
        });
      });
    },
    [pinToLatest],
  );

  useEffect(() => {
    const element = scrollContainerRef.current;
    if (!element) return;
    const handleScroll = () => {
      const now = Date.now();
      const recentProgrammaticFollow =
        forceFollowUntilRef.current > now &&
        now - lastScrollEventAtRef.current <= DESKTOP_V3_USER_SCROLL_INTENT_MS;
      lastScrollEventAtRef.current = now;
      const pinned = desktopV3BottomDistance(element) <= bottomBufferPx;
      if (!pinned && !recentProgrammaticFollow) {
        forceFollowUntilRef.current = 0;
      }
      setPinnedStateFromElement(element);
    };
    handleScroll();
    element.addEventListener("scroll", handleScroll, { passive: true });
    return () => element.removeEventListener("scroll", handleScroll);
  }, [bottomBufferPx, setPinnedStateFromElement]);

  useEffect(() => {
    autoFollowRef.current = true;
    preserveTopAnchorRef.current = null;
    suppressAutoFollowOnceRef.current = false;
    forceFollowUntilRef.current = 0;
    lastScrollHeightRef.current = scrollContainerRef.current?.scrollHeight ?? 0;
    setIsAtBottom(true);
    setHasUnseenLatest(false);
    scrollToBottom("auto");
  }, [options.resetKey, scrollToBottom]);

  useLayoutEffect(() => {
    const anchor = preserveTopAnchorRef.current;
    const element = scrollContainerRef.current;
    if (!anchor || !element) return;
    const nextScrollTop =
      element.scrollTop +
      Math.max(0, element.scrollHeight - anchor.scrollHeight);
    element.scrollTop = Math.max(0, nextScrollTop);
    lastScrollHeightRef.current = element.scrollHeight;
    preserveTopAnchorRef.current = null;
    setPinnedStateFromElement(element);
  }, [options.itemCount, setPinnedStateFromElement]);

  useEffect(() => {
    scheduleAutoFollow({ forceUnseen: true });
  }, [options.itemCount, scheduleAutoFollow]);

  useEffect(() => {
    if (!options.followKey) return;
    scheduleAutoFollow({
      forceFollow: true,
      forceUnseen: true,
      followMs: DESKTOP_V3_ACTIVITY_FOLLOW_MS,
    });
  }, [options.followKey, scheduleAutoFollow]);

  useEffect(() => {
    const scrollElement = scrollContainerRef.current;
    const contentElement = contentRef.current;
    if (!scrollElement || !contentElement) return;
    const handleObservedResize = () => scheduleAutoFollow();
    const handleObservedMutation = () =>
      scheduleAutoFollow({ forceUnseen: true });
    const resizeObserver =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(handleObservedResize);
    resizeObserver?.observe(scrollElement);
    resizeObserver?.observe(contentElement);
    const mutationObserver =
      typeof MutationObserver === "undefined"
        ? null
        : new MutationObserver(handleObservedMutation);
    mutationObserver?.observe(contentElement, {
      childList: true,
      subtree: true,
    });
    handleObservedResize();
    return () => {
      resizeObserver?.disconnect();
      mutationObserver?.disconnect();
      cancelScheduledScroll();
    };
  }, [cancelScheduledScroll, scheduleAutoFollow]);

  return {
    scrollContainerRef,
    contentRef,
    isAtBottom,
    hasUnseenLatest,
    scrollToBottom,
    preserveScrollPositionForPrepend,
  };
}

function numericTimelineSeq(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : 0;
}

function renderItemTimelineSeq(item: DesktopV3RenderItem): number {
  switch (item.type) {
    case "message":
      return numericTimelineSeq(item.timelineSeq ?? item.message.global_seq);
    case "pending-user":
      return numericTimelineSeq(item.timelineSeq ?? item.message.timelineSeq);
    default:
      return numericTimelineSeq(item.timelineSeq);
  }
}

function committedToolRenderKey(message: MessageSnapshot): string {
  const toolMessage = parseStructuredToolMessage(message.content);
  const identity = toolMessage?.toolInstanceId || toolMessage?.callId || "";
  return identity ? `live-tool:${identity}` : "";
}

export function desktopV3RenderItemKey(item: DesktopV3RenderItem): string {
  switch (item.type) {
    case "plan-break":
    case "plan-final-handoff":
    case "plan-blocked-handoff":
      return item.message.id;
    case "message":
      return item.renderKey || item.message.id;
    case "pending-user":
      return item.message.messageId;
    default:
      return item.id;
  }
}

export function orderDesktopV3LiveRenderItems(
  items: DesktopV3RenderItem[],
): DesktopV3RenderItem[] {
  return items
    .map((item, index) => ({ item, index, seq: renderItemTimelineSeq(item) }))
    .sort((left, right) => {
      const leftSequenced = left.seq > 0;
      const rightSequenced = right.seq > 0;
      if (leftSequenced && rightSequenced && left.seq !== right.seq) {
        return left.seq - right.seq;
      }
      if (leftSequenced !== rightSequenced) {
        return leftSequenced ? -1 : 1;
      }
      return left.index - right.index;
    })
    .map((entry) => entry.item);
}

function reasoningElapsedLabel(
  startedAt: number | null,
  completedAt: number | null | undefined,
  now: number,
): string {
  if (typeof startedAt !== "number" || startedAt <= 0) return "";
  const endAt =
    typeof completedAt === "number" && completedAt > startedAt
      ? completedAt
      : now;
  const elapsed = Math.max(0, endAt - startedAt);
  if (elapsed < 1000) return `${elapsed}ms`;
  if (elapsed < 60_000) return `${(elapsed / 1000).toFixed(1)}s`;
  return `${(elapsed / 60_000).toFixed(1)}m`;
}

function reasoningHeadline(
  state: NonNullable<LiveRunOverlay["reasoning"]>["state"],
  startedAt: number | null,
  completedAt: number | null | undefined,
  now: number,
): string {
  const label = state === "error" ? "Thinking failed" : "Thinking";
  const elapsed = reasoningElapsedLabel(
    startedAt,
    state === "running" ? null : completedAt,
    now,
  );
  return elapsed ? `${label} · ${elapsed}` : label;
}

function reasoningBody(
  text: string,
  summary: string,
  thinkingTagsEnabled: boolean,
): string {
  if (!thinkingTagsEnabled) return "";
  return text.trim() || summary.trim() || "Thinking…";
}

function normalizeReplayContent(content: string): string {
  return content.trim().replace(/\s+/g, " ");
}

function canonicalContentSet(
  messages: MessageSnapshot[],
  role: string,
): Set<string> {
  const contents = new Set<string>();
  for (const message of messages) {
    if (message.role !== role) continue;
    const normalized = normalizeReplayContent(message.content);
    if (normalized) contents.add(normalized);
  }
  return contents;
}

export function isDesktopV3ManualCompactionAckMessage(
  message: MessageSnapshot,
): boolean {
  if ((message.role || "").trim().toLowerCase() !== "assistant") return false;
  const metadataSource =
    typeof message.metadata?.source === "string"
      ? message.metadata.source.trim().toLowerCase()
      : "";
  if (metadataSource === "manual_context_compaction_ack") return true;
  return message.content
    .trim()
    .startsWith("Manual context compact complete (Compact #");
}

export function isDesktopV3PlanExecutionBreakMessage(
  message: MessageSnapshot,
): boolean {
  if ((message.role || "").trim().toLowerCase() !== "system") return false;
  const metadataSource =
    typeof message.metadata?.source === "string"
      ? message.metadata.source.trim().toLowerCase()
      : "";
  const metadataKind =
    typeof message.metadata?.kind === "string"
      ? message.metadata.kind.trim().toLowerCase()
      : "";
  return (
    metadataSource === "plan_execution_lifecycle" ||
    metadataKind === "plan_execution_break"
  );
}

export function isDesktopV3PlanFinalHandoffMessage(
  message: MessageSnapshot,
): boolean {
  if ((message.role || "").trim().toLowerCase() !== "system") return false;
  const metadataSource =
    typeof message.metadata?.source === "string"
      ? message.metadata.source.trim().toLowerCase()
      : "";
  const metadataKind =
    typeof message.metadata?.kind === "string"
      ? message.metadata.kind.trim().toLowerCase()
      : "";
  return (
    metadataSource === "plan_execution_final_handoff" ||
    metadataKind === "plan_final_checkpoint_handoff"
  );
}

export function isDesktopV3PlanBlockedHandoffMessage(
  message: MessageSnapshot,
): boolean {
  if ((message.role || "").trim().toLowerCase() !== "system") return false;
  const metadataSource =
    typeof message.metadata?.source === "string"
      ? message.metadata.source.trim().toLowerCase()
      : "";
  const metadataKind =
    typeof message.metadata?.kind === "string"
      ? message.metadata.kind.trim().toLowerCase()
      : "";
  return (
    metadataSource === "plan_execution_blocked_handoff" ||
    metadataKind === "plan_blocked_checkpoint_handoff"
  );
}

function buildDesktopV3PlanExecutionBreakItem(
  message: MessageSnapshot,
): Extract<DesktopV3RenderItem, { type: "plan-break" }> {
  const lines = message.content
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
  const headline = lines[0] || "Plan execution updated";
  return {
    type: "plan-break",
    message,
    headline,
    details: lines.slice(1),
    timelineSeq: message.global_seq,
  };
}

function buildDesktopV3PlanFinalHandoffItem(
  message: MessageSnapshot,
): Extract<DesktopV3RenderItem, { type: "plan-final-handoff" }> {
  const lines = message.content.split(/\r?\n/);
  const headline =
    lines.find((line) => line.trim())?.trim() || "Final checkpoint handoff";
  const headlineIndex = lines.findIndex((line) => line.trim());
  const bodyLines = headlineIndex >= 0 ? lines.slice(headlineIndex + 1) : [];
  const body = bodyLines.join("\n").trim() || message.content.trim();
  return {
    type: "plan-final-handoff",
    message,
    headline,
    body,
    timelineSeq: message.global_seq,
  };
}

function buildDesktopV3PlanBlockedHandoffItem(
  message: MessageSnapshot,
): Extract<DesktopV3RenderItem, { type: "plan-blocked-handoff" }> {
  const item = buildDesktopV3PlanFinalHandoffItem(message);
  return {
    ...item,
    type: "plan-blocked-handoff",
  };
}

export function buildDesktopV3LiveRunRenderItems(
  run: LiveRunOverlay,
  options: {
    assistantMessages?: Set<string>;
    reasoningMessages?: Set<string>;
  } = {},
): DesktopV3RenderItem[] {
  const items: DesktopV3RenderItem[] = [];
  for (const segment of run.assistantSegments ?? []) {
    const content = segment.content;
    if (!content.trim()) continue;
    if (options.assistantMessages?.has(normalizeReplayContent(content)))
      continue;
    items.push({
      type: "live-assistant",
      id: segment.id,
      content,
      timelineSeq: segment.timelineSeq,
    });
  }
  const reasoningRecords = Object.values(
    run.reasoningByKey ?? (run.reasoning ? { active: run.reasoning } : {}),
  );
  for (const reasoning of reasoningRecords) {
    const text = reasoning.text.trim();
    const summary = reasoning.summary.trim();
    if (
      reasoning.state === "completed" &&
      (options.reasoningMessages?.has(normalizeReplayContent(text)) ||
        options.reasoningMessages?.has(normalizeReplayContent(summary)))
    )
      continue;
    if (!text && !summary && reasoning.state !== "running") continue;
    items.push({
      type: "live-reasoning",
      id: `live-reasoning:${reasoning.key || reasoning.reasoningId || reasoning.reasoningKey || run.runId}`,
      text,
      summary,
      state: reasoning.state,
      startedAt: reasoning.startedAt,
      completedAt: reasoning.completedAt,
      timelineSeq: reasoning.timelineSeq,
    });
  }
  for (const tool of Object.values(run.toolCallsByCallId)) {
    items.push({
      type: "live-tool",
      id: `live-tool:${tool.toolInstanceId || tool.callId}`,
      tool,
      timelineSeq: tool.timelineSeq,
    });
  }
  if (run.assistantDraft?.content) {
    items.push({
      type: "live-assistant",
      id: `live-assistant:${run.runId}:draft`,
      content: run.assistantDraft.content,
      timelineSeq: run.assistantDraft.timelineSeq,
    });
  } else if (run.status === "running" || run.status === "pending_executor") {
    items.push({
      type: "live-working",
      id: `live-working:${run.runId}`,
      timelineSeq: (run.lastEventSeqSeen ?? 0) + 1,
    });
  }
  return orderDesktopV3LiveRenderItems(items);
}

export function buildDesktopV3ConversationRenderItems(
  renderedMessages: RenderedSessionMessages,
): DesktopV3RenderItem[] {
  const pendingMessageIds = new Set(
    renderedMessages.pendingUser.map((message) => message.messageId),
  );
  const committedMessages = renderedMessages.committed.filter(
    (message) =>
      !isDesktopV3ManualCompactionAckMessage(message) &&
      !pendingMessageIds.has(message.id),
  );
  const assistantMessages = canonicalContentSet(committedMessages, "assistant");
  const reasoningMessages = canonicalContentSet(committedMessages, "reasoning");
  const items: DesktopV3RenderItem[] = [
    ...committedMessages.map((message) =>
      isDesktopV3PlanExecutionBreakMessage(message)
        ? buildDesktopV3PlanExecutionBreakItem(message)
        : isDesktopV3PlanFinalHandoffMessage(message)
          ? buildDesktopV3PlanFinalHandoffItem(message)
          : isDesktopV3PlanBlockedHandoffMessage(message)
            ? buildDesktopV3PlanBlockedHandoffItem(message)
            : {
                type: "message" as const,
                message,
                timelineSeq: message.global_seq,
                renderKey: committedToolRenderKey(message) || undefined,
              },
    ),
    ...renderedMessages.pendingUser.map((message) => ({
      type: "pending-user" as const,
      message,
      timelineSeq: message.timelineSeq,
    })),
  ];
  for (const run of renderedMessages.liveRuns) {
    items.push(
      ...buildDesktopV3LiveRunRenderItems(run, {
        assistantMessages,
        reasoningMessages,
      }),
    );
  }
  return orderDesktopV3LiveRenderItems(items);
}

export function resolveDesktopV3StopRunRequest(input: {
  route: DesktopChatRoute | null | undefined;
  runId: string | null | undefined;
}): { runId: string; targetSwarmId: string } {
  const runId = input.runId?.trim() ?? "";
  if (!runId) {
    throw new Error("Desktop V3 stop requires run_id");
  }
  const target = getDesktopSessionStopTarget(input.route);
  if (target.sessionApi !== "v3") {
    throw new Error(target.unsupportedReason);
  }
  return { runId, targetSwarmId: target.targetSwarmId };
}

export interface DesktopV3ExistingConversationPaneProps {
  modeCommand?: "toggle-plan-auto" | null;
  onModeCommandHandled?: () => void;
  onModeChange?: (mode: DesktopSessionMode) => void;
  sessionId: string;
  initialHydrateStatus: "idle" | "loading" | "cached" | "ready" | "error";
  renderedMessages: RenderedSessionMessages;
  messagesLoaded: boolean;
  metadata?: Record<string, unknown>;
  session?: DesktopSessionRecord | null;
  loadedMessageCount?: number;
  routeOptions?: DesktopChatRoute[];
  onOpenChats?: () => void;
  onNewSession?: () => void;
  sessionActions?: DesktopV3ChatHeaderSessionActions | null;
  onSlashCommand?: (
    command: DesktopSlashCommand,
    draft: string,
  ) => void | Promise<void>;
  onCompactingChange?: (sessionId: string, startedAt: number | null) => void;
  onArchivePlanSession?: (sessionId: string) => void;
  onOpenPlan?: () => void;
  planSidebarBelowActions?: ReactNode;
}

export function completeDesktopV3ExistingMessage(input: {
  sessionId: string;
  operation: DesktopV3ExistingMessageOperation;
  mountedRef: { current: boolean };
  setOperation: (operation: DesktopV3ExistingMessageOperation | null) => void;
  setDraft: (draft: string) => void;
}): void {
  clearDesktopV3ExistingMessageOperation(
    input.sessionId,
    input.operation.operationId,
  );
  if (!input.mountedRef.current) return;
  input.setOperation(null);
  input.setDraft("");
}

export function DesktopV3ExistingConversationPane({
  modeCommand = null,
  onModeCommandHandled,
  onModeChange,
  sessionId,
  initialHydrateStatus,
  renderedMessages,
  messagesLoaded,
  metadata,
  session,
  loadedMessageCount,
  routeOptions = [],
  onOpenChats,
  onNewSession,
  sessionActions = null,
  onSlashCommand,
  onCompactingChange,
  onArchivePlanSession,
  onOpenPlan,
  planSidebarBelowActions,
}: DesktopV3ExistingConversationPaneProps) {
  const normalizedSessionId = sessionId.trim();
  const navigate = useNavigate();
  const matchRoute = useMatchRoute();
  const queryClient = useQueryClient();
  const mountedRef = useRef(true);
  const operationRef = useRef<DesktopV3ExistingMessageOperation | null>(
    loadDesktopV3ExistingMessageOperation(normalizedSessionId),
  );
  const agentStateQuery = useQuery(agentStateQueryOptions());
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions());
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions());
  const authCredentialsQuery = useQuery({
    queryKey: ["auth-credentials", "desktop-composer"],
    queryFn: () => listAuthCredentials("", "", 200),
    staleTime: 30_000,
  });
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE;
  const modelOptions = modelOptionsQuery.data ?? [];
  const thinkingTagsEnabled = normalizeThinkingTagsEnabled(
    uiSettingsQuery.data,
  );
  const selectPlanExecutionViewForSession = useCallback(
    (state: DesktopV3CacheState) =>
      selectDesktopPlanExecutionView(state, normalizedSessionId),
    [normalizedSessionId],
  );
  const rawCachedPreference = useDesktopV3CacheSelector(
    (state) => state.preferencesBySession[normalizedSessionId],
  );
  const rawCachedUsage = useDesktopV3CacheSelector(
    (state) => state.usageBySession[normalizedSessionId],
  );
  const cachedAgentModelPolicy = useDesktopV3CacheSelector(
    (state) => state.agentModelPolicyBySession[normalizedSessionId],
  );
  const planExecutionView = useDesktopV3CacheSelector(
    selectPlanExecutionViewForSession,
    planExecutionViewsEqual,
  );
  const cachedPreference = useMemo(
    () => normalizePreference(rawCachedPreference),
    [rawCachedPreference],
  );
  const cacheSession = useDesktopV3CacheSelector((state) => {
    const record = state.sessionsById[normalizedSessionId];
    return record?.kind === "full" ? record.session : null;
  });
  const storedOperation = operationRef.current;
  const sessionMode = session?.mode || cacheSession?.mode || "auto";
  const selectPendingPermissionsForSession = useCallback(
    (state: DesktopV3CacheState) =>
      [...(state.permissionsBySession[normalizedSessionId] ?? [])]
        .filter((permission) =>
          permissionRequiresApproval(permission, sessionMode),
        )
        .sort(comparePendingPermissions),
    [normalizedSessionId, sessionMode],
  );
  const pendingPermissions = useDesktopV3CacheSelector(
    selectPendingPermissionsForSession,
    pendingPermissionsEqual,
  );
  const pendingPlanPermissions = pendingPermissions.filter(
    isPlanProposalPermission,
  );
  const pendingModalPermissions = pendingPermissions.filter(
    (permission) => !isPlanProposalPermission(permission),
  );
  const selectedPermission = pendingModalPermissions[0] ?? null;
  const pendingPlanPermission = pendingPlanPermissions[0] ?? null;
  const pendingPlanDocument = useMemo(
    () => pendingPlanPermission
      ? structuredPlanDocumentFromPermission(pendingPlanPermission)
      : null,
    [pendingPlanPermission],
  );
  const [planAgentMobileOpen, setPlanAgentMobileOpen] = useState(true);
  useEffect(() => {
    setPlanAgentMobileOpen(Boolean(pendingPlanPermission));
  }, [pendingPlanPermission?.id]);
  const currentRun =
    renderedMessages.liveRuns.find(
      (run) => run.status === "running" || run.status === "pending_executor",
    ) ?? null;
  const canonicalRunStatusModel = buildDesktopV3RunStatusModel({
    currentRunIntent: renderedMessages.currentRunIntent,
    latestRunIntent: renderedMessages.latestRunIntent,
    liveRuns: renderedMessages.liveRuns,
  });
  const sessionMetadata =
    session?.metadata ?? cacheSession?.metadata ?? metadata;
  const headerBranchLabel =
    metadataString(sessionMetadata, "swarm_v3_branch_label") ||
    session?.worktreeBranch?.trim() ||
    session?.gitBranch?.trim() ||
    cacheSession?.worktree_branch?.trim() ||
    metadataString(sessionMetadata, "git_branch") ||
    metadataString(sessionMetadata, "branch");
  const settingsBaseline = useMemo(
    () =>
      buildDesktopV3ExistingSettingsSnapshot({
        sessionId: normalizedSessionId,
        metadata: sessionMetadata,
        session,
        cacheSession,
        cachedPreference,
        agentModelPolicy: cachedAgentModelPolicy,
      }),
    [
      cacheSession,
      cachedAgentModelPolicy,
      cachedPreference,
      normalizedSessionId,
      session,
      sessionMetadata,
    ],
  );
  const [draft, setDraft] = useState(storedOperation?.request.content ?? "");
  const [sendError, setSendError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [compactStartedAt, setCompactStartedAt] = useState<number | null>(null);
  const [thinkingTagsSaving, setThinkingTagsSaving] = useState(false);
  const [agentModelSaving, setAgentModelSaving] = useState(false);
  const [planExecutionBusyAction, setPlanExecutionBusyAction] = useState<
    string | null
  >(null);
  const [loadingOlderHistory, setLoadingOlderHistory] = useState(false);
  const [olderHistoryError, setOlderHistoryError] = useState<string | null>(
    null,
  );
  const [olderHistoryAutoActive, setOlderHistoryAutoActive] = useState(false);
  const [transcriptAction, setTranscriptAction] = useState<'copy' | 'download' | null>(null);
  const loadingOlderHistoryRef = useRef(false);
  const previousHistoryScrollTopRef = useRef<number | null>(null);
  const [mode, setMode] = useState<DesktopSessionMode>(settingsBaseline.mode);
  const [selectedAgent, setSelectedAgent] = useState(settingsBaseline.agent);
  const [preference, setPreference] = useState<SessionPreferenceRecord>(
    settingsBaseline.preference,
  );
  const initializedSettingsSessionRef = useRef("");
  const localSettingsDirtyRef = useRef({
    agent: false,
    mode: false,
    preference: false,
  });
  const unlockedPreferenceRef = useRef<SessionPreferenceRecord>(
    settingsBaseline.preference,
  );

  const hasStoredOperation = Boolean(storedOperation);
  const hasMessages =
    renderedMessages.committed.length > 0 ||
    renderedMessages.pendingUser.length > 0 ||
    renderedMessages.liveRuns.length > 0;
  const selectedAgentModelLock = useMemo(
    () =>
      resolveDesktopV3AgentModelLock(agentState.profiles, selectedAgent, mode),
    [agentState.profiles, mode, selectedAgent],
  );
  const lockedPolicyPreference = useMemo(
    () => policyLockedPreference(cachedAgentModelPolicy),
    [cachedAgentModelPolicy],
  );
  const cachedPolicyMatchesSelectedMode = mode === settingsBaseline.mode;
  const selectedModelKey = optionKey(
    preference.provider,
    preference.model,
    preference.contextMode,
  );
  const selectedModelOption =
    modelOptions.find((option) => option.key === selectedModelKey) ?? null;
  const hasResolvedPreference = Boolean(
    preference.provider.trim() &&
    preference.model.trim() &&
    preference.thinking.trim(),
  );
  const selectedModelAvailable = Boolean(
    selectedModelOption && hasResolvedPreference,
  );
  const needsAuth = desktopProviderNeedsAuth(
    preference.provider,
    authCredentialsQuery.data,
  );
  const cachedUsage = useMemo(
    () => normalizeUsageSummary(rawCachedUsage),
    [rawCachedUsage],
  );
  const selectedContextWindow = selectedModelOption
    ? effectiveContextWindow(
        selectedModelOption.provider,
        selectedModelOption.model,
        selectedModelOption.contextMode,
        selectedModelOption.contextWindow,
      )
    : 0;
  const cachedUsageMatchesSelectedModel = Boolean(
    cachedUsage &&
    selectedModelOption &&
    normalizeProviderID(cachedUsage.provider) ===
      normalizeProviderID(selectedModelOption.provider) &&
    normalizeModelID(cachedUsage.provider, cachedUsage.model) ===
      normalizeModelID(
        selectedModelOption.provider,
        selectedModelOption.model,
      ) &&
    (selectedContextWindow <= 0 ||
      cachedUsage.contextWindow <= 0 ||
      cachedUsage.contextWindow === selectedContextWindow),
  );
  const cachedUsageIsProviderSnapshot = Boolean(
    cachedUsage?.source && cachedUsage.source !== "settings_mutation",
  );
  const displayedUsage =
    cachedUsageMatchesSelectedModel && cachedUsageIsProviderSnapshot
      ? cachedUsage
      : null;
  const effectiveContextWindowValue =
    selectedContextWindow > 0
      ? selectedContextWindow
      : cachedUsage?.contextWindow && cachedUsage.contextWindow > 0
        ? cachedUsage.contextWindow
        : 0;
  const contextLabel = formatDesktopV3ContextLabel(
    effectiveContextWindowValue,
    displayedUsage?.remainingTokens,
  );
  const contextTooltip = formatDesktopV3ContextTooltip(
    effectiveContextWindowValue,
    displayedUsage,
  );
  const contextUsagePercent = desktopV3ContextUsagePercent(
    effectiveContextWindowValue,
    displayedUsage,
  );
  const workspaceSettingsMatch =
    matchRoute({ to: "/$workspaceSlug/settings", fuzzy: false }) ??
    matchRoute({ to: "/$workspaceSlug/$sessionId", fuzzy: false }) ??
    matchRoute({ to: "/$workspaceSlug", fuzzy: false });
  const routeWorkspaceSlug =
    workspaceSettingsMatch && "workspaceSlug" in workspaceSettingsMatch
      ? String(workspaceSettingsMatch.workspaceSlug ?? "").trim()
      : "";
  const route = useMemo(
    () =>
      resolveDesktopChatRouteFromSession(
        session ?? null,
        routeOptions,
        routeOptions[0] ?? null,
      ),
    [routeOptions, session],
  );
  const compacting = compactStartedAt !== null;
  const canSend = Boolean(
    normalizedSessionId &&
    !sending &&
    !compacting &&
    selectedAgent.trim() &&
    selectedModelAvailable &&
    (hasStoredOperation || draft.trim()),
  );
  const renderItems = useMemo(
    () => buildDesktopV3ConversationRenderItems(renderedMessages),
    [renderedMessages],
  );
  const scrollFollowKey = useMemo(
    () =>
      [
        renderedMessages.pendingUser
          .map(
            (message) =>
              `${message.clientRequestId}:${message.createdAt}:${message.status}`,
          )
          .join("|"),
        renderedMessages.liveRuns
          .map((run) => {
            const toolsKey = Object.values(run.toolCallsByCallId)
              .map((tool) =>
                [
                  tool.toolInstanceId || tool.callId,
                  tool.toolName,
                  tool.status,
                  tool.updatedAt,
                  tool.timelineSeq,
                ]
                  .map(scrollFollowKeyPart)
                  .join(":"),
              )
              .join("|");
            return [
              run.runId,
              run.status,
              run.assistantDraft?.updatedAt,
              run.assistantDraft?.offsetEnd,
              run.reasoning?.updatedAt,
              run.reasoning?.updatedSeq,
              toolsKey,
              run.lastEventSeqSeen,
            ]
              .map(scrollFollowKeyPart)
              .join(":");
          })
          .join("||"),
      ].join("::"),
    [renderedMessages.liveRuns, renderedMessages.pendingUser],
  );
  const loadedCommittedCount =
    loadedMessageCount ?? renderedMessages.committed.length;
  const totalMessageCount = Math.max(
    session?.messageCount ??
      cacheSession?.message_count ??
      loadedCommittedCount,
    loadedCommittedCount,
  );
  const oldestLoadedSeq = renderedMessages.committed.reduce(
    (min, message) =>
      min === 0 ? message.global_seq : Math.min(min, message.global_seq),
    0,
  );
  const hasPartialHistory =
    messagesLoaded &&
    loadedCommittedCount > 0 &&
    totalMessageCount > loadedCommittedCount;
  const showConversationLoading =
    initialHydrateStatus === "loading" &&
    !messagesLoaded &&
    !hasMessages &&
    !hasStoredOperation;
  const showPlanExecutionSidebar = Boolean(planExecutionView?.plan.document);
  const showPlanSidebar = showPlanExecutionSidebar || Boolean(pendingPlanDocument);
  const {
    scrollContainerRef,
    contentRef,
    isAtBottom,
    hasUnseenLatest,
    scrollToBottom,
    preserveScrollPositionForPrepend,
  } = useDesktopV3StickyBottomScroll({
    resetKey: normalizedSessionId,
    itemCount: renderItems.length,
    followKey: scrollFollowKey,
  });
  const hasRunningReasoning = renderedMessages.liveRuns.some((run) => {
    if (run.reasoning?.state === "running") return true;
    return Object.values(run.reasoningByKey ?? {}).some(
      (reasoning) => reasoning.state === "running",
    );
  });
  const runStatusModel: DesktopV3RunStatusModel | null =
    compactStartedAt !== null
      ? {
          kind: "active",
          label: "Compacting",
          startedAt: compactStartedAt,
          active: true,
        }
      : canonicalRunStatusModel;
  const statusTimerActive =
    Boolean(runStatusModel?.active) || hasRunningReasoning || compacting;
  const [timerNow, setTimerNow] = useState(() => Date.now());

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    const operation =
      loadDesktopV3ExistingMessageOperation(normalizedSessionId);
    operationRef.current = operation;
    setDraft(operation?.request.content ?? "");
    setSendError(null);
  }, [normalizedSessionId]);

  useEffect(() => {
    if (!statusTimerActive) return;
    const timer = window.setInterval(() => setTimerNow(Date.now()), 250);
    return () => window.clearInterval(timer);
  }, [statusTimerActive]);

  useEffect(() => {
    if (!normalizedSessionId) return;
    const sessionChanged =
      initializedSettingsSessionRef.current !== normalizedSessionId;
    if (sessionChanged) {
      initializedSettingsSessionRef.current = normalizedSessionId;
      localSettingsDirtyRef.current = {
        agent: false,
        mode: false,
        preference: false,
      };
      setMode(settingsBaseline.mode);
      setSelectedAgent(settingsBaseline.agent);
      setPreference(settingsBaseline.preference);
      unlockedPreferenceRef.current = settingsBaseline.preference;
      return;
    }
    if (!localSettingsDirtyRef.current.mode) {
      setMode((current) =>
        current === settingsBaseline.mode ? current : settingsBaseline.mode,
      );
    }
    if (!localSettingsDirtyRef.current.agent && settingsBaseline.agent) {
      setSelectedAgent((current) =>
        current === settingsBaseline.agent ? current : settingsBaseline.agent,
      );
    }
    if (
      !localSettingsDirtyRef.current.preference &&
      (settingsBaseline.preference.provider ||
        settingsBaseline.preference.model)
    ) {
      unlockedPreferenceRef.current = settingsBaseline.preference;
      setPreference((current) =>
        preferencesEqual(current, settingsBaseline.preference)
          ? current
          : settingsBaseline.preference,
      );
    }
  }, [normalizedSessionId, settingsBaseline]);

  useEffect(() => {
    if (cachedPolicyMatchesSelectedMode && lockedPolicyPreference) {
      setPreference((current) =>
        preferencesEqual(current, lockedPolicyPreference)
          ? current
          : lockedPolicyPreference,
      );
      return;
    }
    if (!selectedAgentModelLock.locked) return;
    setPreference((current) =>
      preferenceFromAgentModelLock(
        selectedAgentModelLock,
        current,
        modelOptions,
      ),
    );
  }, [
    cachedPolicyMatchesSelectedMode,
    lockedPolicyPreference,
    modelOptions,
    selectedAgentModelLock,
  ]);

  useEffect(() => {
    if (modeCommand !== "toggle-plan-auto") return;
    localSettingsDirtyRef.current.mode = true;
    const nextMode = mode === "plan" ? "auto" : "plan";
    setMode(nextMode);
    onModeChange?.(nextMode);
    onModeCommandHandled?.();
  }, [mode, modeCommand, onModeChange, onModeCommandHandled]);

  function handleOpenAgentSettings() {
    const search = { tab: "agents", ...(normalizedSessionId ? { returnSessionId: normalizedSessionId } : {}) };
    if (routeWorkspaceSlug) {
      void navigate({
        to: "/$workspaceSlug/settings",
        params: { workspaceSlug: routeWorkspaceSlug },
        search,
      });
      return;
    }
    void navigate({ to: "/settings", search });
  }

  function handleOpenAuthSettings() {
    if (routeWorkspaceSlug) {
      void navigate({
        to: "/$workspaceSlug/settings",
        params: { workspaceSlug: routeWorkspaceSlug },
        search: { tab: "auth" },
      });
      return;
    }
    void navigate({ to: "/settings", search: { tab: "auth" } });
  }

  async function handleAgentSelect(nextAgentName: string) {
    const normalizedAgentName = nextAgentName.trim();
    if (!normalizedSessionId || !normalizedAgentName || normalizedAgentName === selectedAgent.trim()) return;
    setSendError(null);
    try {
      const agentResponse = await updateSessionV3Agent(normalizedSessionId, normalizedAgentName);
      dispatchDesktopV3Cache({
        type: "mutation.sessionSettingsResult",
        raw: sessionV3AgentSettingsMutationResponse(agentResponse, normalizedSessionId),
      });
      setSelectedAgent(normalizedAgentName);
      localSettingsDirtyRef.current.agent = false;
      const nextLock = resolveDesktopV3AgentModelLock(agentState.profiles, normalizedAgentName, mode);
      if (nextLock.locked) {
        setPreference((current) => preferenceFromAgentModelLock(nextLock, current, modelOptions));
      }
    } catch (error) {
      if (mountedRef.current) setSendError(error instanceof Error ? error.message : "Failed to switch agent");
      throw error;
    }
  }

  function handleModeSelect(nextMode: DesktopSessionMode) {
    if (!normalizedSessionId || nextMode === mode) return;
    localSettingsDirtyRef.current.mode = true;
    setMode(nextMode);
    onModeChange?.(nextMode);
    const nextLock = resolveDesktopV3AgentModelLock(
      agentState.profiles,
      selectedAgent,
      nextMode,
    );
    if (!nextLock.locked) return;
    setPreference((current) =>
      preferenceFromAgentModelLock(nextLock, current, modelOptions),
    );
  }

  async function handleConfirmAgentSettings(
    input: AgentModelControlConfirmInput,
  ) {
    if (!normalizedSessionId || agentModelSaving) return;
    setAgentModelSaving(true);
    setSendError(null);
    try {
      const action = input.action;
      const basePreference = preference;
      await updateAgentProfile(input.profile, action.agentPatch);
      const agentStateResult =
        await refreshAgentModelMutationCaches(queryClient);
      const nextAgentName = input.agentName.trim();
      if (nextAgentName) {
        const agentResponse = await updateSessionV3Agent(
          normalizedSessionId,
          nextAgentName,
        );
        dispatchDesktopV3Cache({
          type: "mutation.sessionSettingsResult",
          raw: sessionV3AgentSettingsMutationResponse(
            agentResponse,
            normalizedSessionId,
          ),
        });
        setSelectedAgent(nextAgentName);
      }
      const refreshedLock = resolveDesktopV3AgentModelLock(
        agentStateResult.profiles,
        input.agentName,
        mode,
      );
      const nextPreference = refreshedLock.locked
        ? preferenceFromAgentModelLock(
            refreshedLock,
            basePreference,
            modelOptions,
          )
        : basePreference;
      if (
        !refreshedLock.locked &&
        !preferencesEqual(nextPreference, preference)
      ) {
        const preferenceResponse = await updateSessionV3Preference(
          normalizedSessionId,
          {
            provider: nextPreference.provider,
            model: nextPreference.model,
            thinking: nextPreference.thinking,
            serviceTier: nextPreference.serviceTier,
            contextMode: nextPreference.contextMode,
          },
        );
        const settingsResponse = sessionV3PreferenceSettingsMutationResponse(
          preferenceResponse,
          normalizedSessionId,
        );
        dispatchDesktopV3Cache({
          type: "mutation.sessionSettingsResult",
          raw: settingsResponse,
        });
        const updatedPreference = normalizePreference(
          settingsResponse.preference ?? nextPreference,
        );
        setPreference(updatedPreference);
        unlockedPreferenceRef.current = updatedPreference;
      } else {
        setPreference(nextPreference);
        if (!refreshedLock.locked)
          unlockedPreferenceRef.current = nextPreference;
      }
      localSettingsDirtyRef.current = {
        agent: false,
        mode: false,
        preference: false,
      };
    } catch (error) {
      if (mountedRef.current)
        setSendError(
          error instanceof Error
            ? error.message
            : "Failed to update agent settings",
        );
      throw error;
    } finally {
      if (mountedRef.current) setAgentModelSaving(false);
    }
  }

  async function persistVisibleSettings() {
    if (!normalizedSessionId) return;
    const currentAgent = settingsBaseline.agent.trim();
    const nextAgent = selectedAgent.trim();
    if (nextAgent && nextAgent !== currentAgent) {
      const agentResponse = await updateSessionV3Agent(
        normalizedSessionId,
        nextAgent,
      );
      dispatchDesktopV3Cache({
        type: "mutation.sessionSettingsResult",
        raw: sessionV3AgentSettingsMutationResponse(
          agentResponse,
          normalizedSessionId,
        ),
      });
    }
    if (mode !== settingsBaseline.mode) {
      const modeResponse = await updateSessionV3Mode(normalizedSessionId, mode);
      const settingsResponse = sessionV3ModeSettingsMutationResponse(
        modeResponse,
        normalizedSessionId,
        mode,
      );
      dispatchDesktopV3Cache({
        type: "mutation.sessionSettingsResult",
        raw: settingsResponse,
      });
      if (settingsResponse.preference) {
        setPreference(normalizePreference(settingsResponse.preference));
      }
    }
  }

  const handleLoadOlderHistory = useCallback(async () => {
    if (
      !normalizedSessionId ||
      loadingOlderHistoryRef.current ||
      !hasPartialHistory ||
      oldestLoadedSeq <= 0
    )
      return false;
    loadingOlderHistoryRef.current = true;
    setLoadingOlderHistory(true);
    setOlderHistoryError(null);
    try {
      const result = await fetchSessionMessages(
        normalizedSessionId,
        undefined,
        0,
        {
          sessionApi: "v3",
          beforeSeq: oldestLoadedSeq,
          limit: 200,
        },
      );
      const incomingMessages = result.messages.map(
        chatMessageToMessageSnapshot,
      );
      if (incomingMessages.length === 0 && result.hasMoreOlder) {
        throw new Error(
          "Older history page returned no messages. Try refreshing the conversation.",
        );
      }
      preserveScrollPositionForPrepend();
      dispatchDesktopV3Cache({
        type: "messages.prependHistoryResult",
        sessionId: normalizedSessionId,
        messages: incomingMessages,
        sourceMessageCount: totalMessageCount,
        knownFull: !result.hasMoreOlder,
      });
      if (!result.hasMoreOlder) setOlderHistoryAutoActive(false);
      return result.hasMoreOlder;
    } catch (error) {
      if (mountedRef.current) {
        setOlderHistoryError(
          error instanceof Error ? error.message : String(error),
        );
        setOlderHistoryAutoActive(false);
      }
      return false;
    } finally {
      loadingOlderHistoryRef.current = false;
      if (mountedRef.current) setLoadingOlderHistory(false);
    }
  }, [
    hasPartialHistory,
    normalizedSessionId,
    oldestLoadedSeq,
    preserveScrollPositionForPrepend,
    totalMessageCount,
  ]);

  useEffect(() => {
    setOlderHistoryAutoActive(false);
    setOlderHistoryError(null);
    loadingOlderHistoryRef.current = false;
    previousHistoryScrollTopRef.current = null;
  }, [normalizedSessionId]);

  useEffect(() => {
    if (!hasPartialHistory) {
      setOlderHistoryAutoActive(false);
    }
  }, [hasPartialHistory]);

  useEffect(() => {
    const element = scrollContainerRef.current;
    if (!element) return;
    previousHistoryScrollTopRef.current = element.scrollTop;
    const handleHistoryAutoloadScroll = () => {
      const currentScrollTop = element.scrollTop;
      const previousScrollTop =
        previousHistoryScrollTopRef.current ?? currentScrollTop;
      previousHistoryScrollTopRef.current = currentScrollTop;
      const scrollingUp = currentScrollTop < previousScrollTop - 1;
      if (!scrollingUp || currentScrollTop > DESKTOP_V3_HISTORY_AUTOLOAD_TOP_PX)
        return;
      if (!hasPartialHistory || oldestLoadedSeq <= 0) return;
      setOlderHistoryError(null);
      setOlderHistoryAutoActive(true);
    };
    element.addEventListener("scroll", handleHistoryAutoloadScroll, {
      passive: true,
    });
    return () =>
      element.removeEventListener("scroll", handleHistoryAutoloadScroll);
  }, [hasPartialHistory, oldestLoadedSeq, scrollContainerRef]);

  useEffect(() => {
    if (
      !olderHistoryAutoActive ||
      !hasPartialHistory ||
      olderHistoryError ||
      loadingOlderHistory ||
      oldestLoadedSeq <= 0
    )
      return;
    void handleLoadOlderHistory();
  }, [
    handleLoadOlderHistory,
    hasPartialHistory,
    loadingOlderHistory,
    olderHistoryAutoActive,
    olderHistoryError,
    oldestLoadedSeq,
  ]);

  async function handleSubmit(submittedDraft = draft) {
    if (!normalizedSessionId || sending || compacting) return;

    setSending(true);
    setSendError(null);
    scrollToBottom("smooth");
    try {
      if (!selectedModelAvailable) {
        throw new Error("Select a model and thinking level before sending");
      }
      await persistVisibleSettings();
      const operation =
        operationRef.current ??
        createDesktopV3ExistingMessageOperation({
          sessionId: normalizedSessionId,
          prompt: submittedDraft,
          metadata,
        });
      operationRef.current = operation;
      setDraft("");
      persistDesktopV3ExistingMessageOperation(operation);

      await continueDesktopV3Conversation(operation);
      completeDesktopV3ExistingMessage({
        sessionId: normalizedSessionId,
        operation,
        mountedRef,
        setOperation: (nextOperation) => {
          operationRef.current = nextOperation;
        },
        setDraft,
      });
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      if (mountedRef.current) {
        setSending(false);
      }
    }
  }

  async function handleCompact(note: string) {
    if (!normalizedSessionId || compacting || sending || currentRun) return;

    const startedAt = Date.now();
    setCompactStartedAt(startedAt);
    onCompactingChange?.(normalizedSessionId, startedAt);
    setSendError(null);
    scrollToBottom("smooth");
    try {
      await persistVisibleSettings();
      await compactDesktopV3Session({
        sessionId: normalizedSessionId,
        note,
        agentName: selectedAgent || "",
      });
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      if (mountedRef.current) setCompactStartedAt(null);
      onCompactingChange?.(normalizedSessionId, null);
    }
  }

  async function handleThinkingTagsToggle(enabled: boolean) {
    if (thinkingTagsSaving) return;
    setThinkingTagsSaving(true);
    setSendError(null);
    try {
      const updated = await saveThinkingTagsSetting(enabled);
      queryClient.setQueryData(uiSettingsQueryKey(), updated);
    } catch (error) {
      if (mountedRef.current) {
        setSendError(
          error instanceof Error
            ? error.message
            : "Failed to update thinking tags setting",
        );
      }
    } finally {
      if (mountedRef.current) setThinkingTagsSaving(false);
    }
  }

  async function handlePlanExecutionAction(
    input: DesktopPlanExecutionSidebarActionInput,
  ) {
    const policySwitch =
      input.action === "resume_automatic" ||
      input.action === "resume_checkpointed";
    if (
      !normalizedSessionId ||
      planExecutionBusyAction ||
      (currentRun && !policySwitch)
    )
      return;
    const busyKey = `${input.action}:${input.checkpointId ?? ""}`;
    setPlanExecutionBusyAction(busyKey);
    setSendError(null);
    if (!policySwitch) scrollToBottom("smooth");
    try {
      await persistVisibleSettings();
      switch (input.action) {
        case "accept_checkpoint": {
          if (!input.checkpointId)
            throw new Error("Accept checkpoint requires checkpoint_id");
          const response = await acceptAndContinueDesktopPlanCheckpoint(
            normalizedSessionId,
            input.checkpointId,
          );
          if (desktopPlanLifecycleComplete(response)) {
            await archiveDesktopV3Sessions([normalizedSessionId]);
            onArchivePlanSession?.(normalizedSessionId);
          }
          break;
        }
        case "archive_plan":
          await archiveDesktopV3Sessions([normalizedSessionId]);
          onArchivePlanSession?.(normalizedSessionId);
          break;
        case "resolve_blocked_checkpoint":
          if (!input.checkpointId)
            throw new Error(
              "Resolve blocked checkpoint requires checkpoint_id",
            );
          await resolveDesktopPlanBlockedCheckpoint(
            normalizedSessionId,
            input.checkpointId,
            { startNext: true },
          );
          break;
        case "resolve_blocked_only":
          if (!input.checkpointId)
            throw new Error(
              "Resolve blocked checkpoint requires checkpoint_id",
            );
          await resolveDesktopPlanBlockedCheckpoint(
            normalizedSessionId,
            input.checkpointId,
          );
          break;
        case "resume_automatic":
          await resumeDesktopPlanAutomatic(normalizedSessionId);
          break;
        case "resume_checkpointed":
          await resumeDesktopPlanCheckpointed(normalizedSessionId);
          break;
      }
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error));
      }
    } finally {
      if (mountedRef.current) setPlanExecutionBusyAction(null);
    }
  }

  async function handleStop() {
    if (!normalizedSessionId || !currentRun?.runId) return;
    try {
      const stopRequest = resolveDesktopV3StopRunRequest({
        route,
        runId: currentRun.runId,
      });
      await stopSessionV3Run(normalizedSessionId, stopRequest);
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error));
      }
    }
  }

  async function resolvePermission(
    permission: DesktopPermissionRecord,
    action:
      "approve" | "deny" | "approve_always" | "always_allow" | "always_deny",
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) {
    const resolved = await resolveSessionPermission(
      permission.sessionId,
      permission.id,
      action,
      reason,
      approvedArguments,
      { sessionApi: "v3" },
    );
    dispatchDesktopV3Cache({
      type: "permission.resolveResult",
      sessionId: permission.sessionId,
      permissionId: permission.id,
      permission: resolved,
    });
    const approved =
      action === "approve" ||
      action === "approve_always" ||
      action === "always_allow";
    const toolName = permission.toolName
      .trim()
      .toLowerCase()
      .replace(/-/g, "_");
    if (approved && toolName === "exit_plan_mode") {
      try {
        await fetchAndApplyDesktopV3PlanSnapshot(permission.sessionId, {
          includeHistory: false,
        });
      } catch (error) {
        if (mountedRef.current) {
          setSendError(
            error instanceof Error
              ? `Plan activated, but sidebar refresh failed: ${error.message}`
              : "Plan activated, but sidebar refresh failed",
          );
        }
      }
    }
  }

  async function handleResolvePermission(
    action:
      "approve" | "deny" | "approve_always" | "always_allow" | "always_deny",
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) {
    if (!selectedPermission) return;
    await resolvePermission(
      selectedPermission,
      action,
      reason,
      approvedArguments,
    );
  }

  const planExecutionActionRef = useRef(handlePlanExecutionAction);
  planExecutionActionRef.current = handlePlanExecutionAction;
  const stablePlanExecutionAction = useCallback(
    (input: DesktopPlanExecutionSidebarActionInput) =>
      planExecutionActionRef.current(input),
    [],
  );

  const stopRef = useRef(handleStop);
  stopRef.current = handleStop;
  const stableStop = useCallback(() => stopRef.current(), []);

  const openPlanRef = useRef(onOpenPlan);
  openPlanRef.current = onOpenPlan;
  const handleTranscriptExport = useCallback(async (kind: 'copy' | 'download') => {
    if (transcriptAction) return;
    setTranscriptAction(kind);
    try {
      const initial = renderedMessages.committed.map((item) => ({
        id: item.id,
        sessionId: item.session_id,
        globalSeq: item.global_seq,
        role: item.role,
        content: item.content,
        createdAt: item.created_at,
        metadata: item.metadata,
      }));
      const complete = hasPartialHistory
        ? await loadCompleteConversationMessages(initial, async (beforeSeq) => fetchSessionMessages(normalizedSessionId, undefined, 0, { sessionApi: 'v3', beforeSeq, limit: 200 }))
        : initial;
      const title = session?.title || cacheSession?.title || 'Conversation';
      const markdown = formatConversationMarkdown({
        title,
        workspaceName: session?.workspaceName || cacheSession?.workspace_name,
        sessionId: normalizedSessionId,
        exportedAt: new Date(),
      }, complete);
      if (kind === 'copy') {
        await navigator.clipboard.writeText(markdown);
      } else {
        const url = URL.createObjectURL(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }));
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = sanitizeTranscriptFilename(title);
        anchor.click();
        URL.revokeObjectURL(url);
      }
    } catch (error) {
      window.alert(`Conversation export failed: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      if (mountedRef.current) setTranscriptAction(null);
    }
  }, [cacheSession?.title, cacheSession?.workspace_name, hasPartialHistory, normalizedSessionId, renderedMessages.committed, session?.title, session?.workspaceName, transcriptAction]);
  const headerSessionActions = useMemo(() => sessionActions ? {
    ...sessionActions,
    pendingAction: transcriptAction ?? sessionActions.pendingAction,
    onCopyConversation: () => { void handleTranscriptExport('copy'); },
    onDownloadConversation: () => { void handleTranscriptExport('download'); },
  } : null, [handleTranscriptExport, sessionActions, transcriptAction]);

  const hasOpenPlan = Boolean(onOpenPlan);
  const stableOpenPlan = useMemo(
    () => (hasOpenPlan ? () => openPlanRef.current?.() : undefined),
    [hasOpenPlan],
  );

  if (!normalizedSessionId) {
    return (
      <DesktopV3ChatStateCard
        title="Select a session"
        description="Choose a session from the sidebar to view its conversation."
      />
    );
  }

  return (
    <div
      className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-[var(--app-bg)]"
      data-testid="desktop-v3-existing-conversation-pane"
    >
      <DesktopV3ChatHeader
        title={session?.title || cacheSession?.title || "Conversation"}
        workspaceName={
          session?.workspaceName || cacheSession?.workspace_name || "Workspace"
        }
        branchName={headerBranchLabel}
        mode={mode}
        runStatus={runStatusModel}
        runStatusNow={timerNow}
        onOpenChats={onOpenChats}
        onNewSession={onNewSession}
        sessionActions={headerSessionActions}
      />
      <div
        className={cn(
          "grid min-h-0 min-w-0 flex-1 grid-cols-[minmax(0,1fr)] overflow-hidden",
          showPlanSidebar ? "xl:grid-cols-[minmax(0,1fr)_360px]" : "",
        )}
      >
        <div className="flex min-h-0 min-w-0 flex-col overflow-hidden">
          <div className="relative min-h-0 min-w-0 flex-1 overflow-hidden">
            <div
              ref={scrollContainerRef}
              className="h-full min-h-0 overflow-x-hidden overflow-y-auto py-6 [scrollbar-gutter:stable]"
              data-testid="desktop-chat-scroller"
            >
              <div
                ref={contentRef}
                className="mx-auto flex min-h-full w-full min-w-0 max-w-[70rem] flex-col gap-5 px-8 sm:px-12"
              >
                {showConversationLoading ? (
                  <DesktopV3ConversationLoadingSpinner />
                ) : null}
                {initialHydrateStatus === "error" &&
                !messagesLoaded &&
                !hasMessages ? (
                  <DesktopV3ChatInlineState
                    title="Conversation unavailable"
                    description="Initial message hydrate failed. You can still send from this session while cached state recovers."
                    tone="error"
                  />
                ) : null}
                {compacting ? <DesktopV3CompactPendingState /> : null}
                {messagesLoaded && !hasMessages ? (
                  <DesktopV3ChatInlineState
                    title="Empty conversation"
                    description="Send a message to continue this session."
                  />
                ) : null}
                {renderItems.map((item, index) => {
                  const itemKey = desktopV3RenderItemKey(item);
                  return (
                    <div
                      key={itemKey}
                      data-testid="desktop-chat-row"
                      data-render-item-type={item.type}
                      data-render-item-key={itemKey}
                    >
                      <DesktopV3RenderItemView
                        item={item}
                        thinkingTagsEnabled={thinkingTagsEnabled}
                        timerNow={timerNow}
                        index={index}
                      />
                    </div>
                  );
                })}
                {pendingPlanPermissions.map((permission, index) => (
                  <DesktopInlinePlanReviewCard
                    key={permission.id}
                    permission={permission}
                    parentSessionId={normalizedSessionId}
                    pendingPosition={index + 1}
                    pendingCount={pendingPlanPermissions.length}
                    onOpenPlanAgent={() => setPlanAgentMobileOpen(true)}
                    onResolve={resolvePermission}
                  />
                ))}
                <div aria-hidden="true" />
              </div>
            </div>
            {pendingPlanDocument && pendingPlanPermission && !planAgentMobileOpen ? (
              <Button
                type="button"
                variant="outline"
                className="absolute right-5 top-5 z-10 xl:hidden"
                onClick={() => setPlanAgentMobileOpen(true)}
              >
                Plan
              </Button>
            ) : null}
            {!isAtBottom && hasUnseenLatest ? (
              <button
                type="button"
                aria-label="Jump to latest message"
                title="Jump to latest message"
                onClick={() => scrollToBottom("smooth")}
                className="absolute bottom-5 right-5 z-10 inline-flex h-10 w-10 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-surface-elevated)] text-[var(--app-text)] shadow-lg transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
              >
                <ArrowDown size={18} aria-hidden="true" />
              </button>
            ) : null}
          </div>

          <DesktopV3AgenticComposer
            draft={draft}
            onDraftChange={setDraft}
            placeholder="Message Swarm…"
            inputLabel="Continue Desktop V3 conversation"
            disabled={sending || compacting}
            busy={sending || compacting}
            canSubmit={canSend}
            canStop={Boolean(currentRun)}
            error={sendError}
            onSubmit={handleSubmit}
            onStop={handleStop}
            onCompact={handleCompact}
            mode={mode}
            onModeSelect={handleModeSelect}
            currentAgent={selectedAgent || "Agent"}
            selectedPrimaryAgent={selectedAgent || ""}
            agents={agentState.profiles}
            modelOptions={modelOptions}
            selectedModelKey={selectedModelKey}
            selectedServiceTier={preference.serviceTier}
            modelPickerDisabled={selectedAgentModelLock.locked}
            modelPickerDisabledReason={selectedAgentModelLock.disabledReason}
            modelLockNotice={
              selectedAgentModelLock.locked
                ? selectedAgentModelLock.disabledReason
                : ""
            }
            modelControlDetail={modelControlDetail({
              locked: selectedAgentModelLock.locked,
              customized: selectedAgentModelLock.customized,
              modelLabel: selectedModelOption?.label || preference.model,
              thinking: preference.thinking,
              serviceTier: serviceTierFromPreference(preference),
            })}
            onOpenAgentSettings={handleOpenAgentSettings}
            onAgentSelect={handleAgentSelect}
            needsAuth={needsAuth}
            onOpenAuthSettings={handleOpenAuthSettings}
            onConfirmAgentSettings={handleConfirmAgentSettings}
            agentModelControlBusy={agentModelSaving}
            thinking={preference.thinking}
            thinkingTagsEnabled={thinkingTagsEnabled}
            onThinkingTagsToggle={(enabled) => {
              void handleThinkingTagsToggle(enabled);
            }}
            thinkingTagsBusy={thinkingTagsSaving}
            contextLabel={contextLabel}
            contextTooltip={contextTooltip}
            contextUsagePercent={contextUsagePercent}
            compactDisabled={compacting || sending || Boolean(currentRun)}
            onSlashCommand={onSlashCommand}
          />
        </div>

        {pendingPlanDocument && pendingPlanPermission ? (
          <DesktopPlanAgentSidecar
            parentSessionId={normalizedSessionId}
            permission={pendingPlanPermission}
            document={pendingPlanDocument}
            embedded
            mobileOpen={planAgentMobileOpen}
            modelLabel={preference.model}
            onClose={() => setPlanAgentMobileOpen(false)}

          />
        ) : showPlanExecutionSidebar && planExecutionView ? (
          <DesktopPlanExecutionSidebar
            view={planExecutionView}
            busyAction={planExecutionBusyAction}
            canStop={Boolean(currentRun)}
            onAction={stablePlanExecutionAction}
            onStop={stableStop}
            onEditPlan={stableOpenPlan}
            belowActions={planSidebarBelowActions}
          />
        ) : null}
      </div>

      <DesktopPermissionModal
        key={`permission:${normalizedSessionId}`}
        open={Boolean(selectedPermission)}
        permission={selectedPermission}
        pendingCount={pendingModalPermissions.length}
        sessionMode={sessionMode}
        onOpenChange={() => undefined}
        onResolve={handleResolvePermission}
      />
    </div>
  );
}

export const DesktopV3RenderItemView = memo(function DesktopV3RenderItemView({
  item,
  thinkingTagsEnabled,
  timerNow,
}: {
  item: DesktopV3RenderItem;
  thinkingTagsEnabled: boolean;
  timerNow: number;
  index: number;
}) {
  switch (item.type) {
    case "plan-break":
      return <DesktopV3PlanExecutionBreak item={item} />;
    case "plan-final-handoff":
      return <DesktopV3PlanFinalHandoff item={item} />;
    case "plan-blocked-handoff":
      return <DesktopV3PlanBlockedHandoff item={item} />;
    case "message":
      return (
        <DesktopV3CommittedMessage
          message={item.message}
          thinkingTagsEnabled={thinkingTagsEnabled}
          timerNow={timerNow}
        />
      );
    case "pending-user":
      return <DesktopV3PendingUserMessage message={item.message} />;
    case "live-assistant":
      return (
        <DesktopV3AssistantMessage content={item.content} role="assistant" />
      );
    case "live-reasoning":
      return (
        <DesktopV3ReasoningMessage
          item={item}
          thinkingTagsEnabled={thinkingTagsEnabled}
          timerNow={timerNow}
        />
      );
    case "live-tool":
      return <DesktopV3LiveToolCall tool={item.tool} />;
    case "live-working":
      return null;
    default:
      return null;
  }
});

function DesktopV3PlanExecutionBreak({
  item,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-break" }>;
}) {
  return (
    <div
      className="flex justify-center py-1"
      data-testid="desktop-v3-plan-execution-break"
    >
      <div className="max-w-[min(100%,42rem)] rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-center shadow-sm">
        <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-primary)]">
          {item.headline}
        </div>
        {item.details.length > 0 ? (
          <div className="mt-1.5 grid gap-0.5 text-xs leading-5 text-[var(--app-text-muted)]">
            {item.details.map((detail, index) => (
              <div key={`${item.message.id}:detail:${index}`}>{detail}</div>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function DesktopV3PlanFinalHandoff({
  item,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-final-handoff" }>;
}) {
  return (
    <DesktopV3PlanHandoff
      item={item}
      icon={<CheckCircle2 size={12} className="text-[var(--app-primary)]" />}
      testId="desktop-v3-plan-final-handoff"
    />
  );
}

function DesktopV3PlanBlockedHandoff({
  item,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-blocked-handoff" }>;
}) {
  return (
    <DesktopV3PlanHandoff
      item={item}
      icon={<CircleAlert size={12} className="text-[var(--app-warning)]" />}
      testId="desktop-v3-plan-blocked-handoff"
    />
  );
}

function DesktopV3PlanHandoff({
  item,
  icon,
  testId,
}: {
  item: Extract<
    DesktopV3RenderItem,
    { type: "plan-final-handoff" | "plan-blocked-handoff" }
  >;
  icon: ReactNode;
  testId: string;
}) {
  return (
    <div className="flex justify-start py-1" data-testid={testId}>
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-90">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          {icon}
          {item.headline}
        </div>
        {item.body ? <ChatMarkdown content={item.body} /> : null}
      </div>
    </div>
  );
}

function DesktopV3CommittedMessage({
  message,
  thinkingTagsEnabled,
  timerNow,
}: {
  message: MessageSnapshot;
  thinkingTagsEnabled: boolean;
  timerNow: number;
}) {
  const role = message.role || "message";
  const toolMessage = parseStructuredToolMessage(message.content);
  if (toolMessage || role === "tool") {
    return (
      <DesktopV3ToolMessage
        content={message.content}
        toolMessage={toolMessage}
        thinkingTagsEnabled={thinkingTagsEnabled}
      />
    );
  }
  if (role === "user") {
    return <DesktopV3UserMessage content={message.content} />;
  }
  if (role === "reasoning") {
    return (
      <DesktopV3ReasoningMessage
        item={{
          type: "live-reasoning",
          id: message.id,
          text: message.content,
          summary: message.content,
          state: "completed",
          startedAt: null,
          completedAt: null,
          timelineSeq: message.global_seq,
        }}
        thinkingTagsEnabled={thinkingTagsEnabled}
        timerNow={timerNow}
      />
    );
  }
  if (role === "assistant") {
    return <DesktopV3AssistantMessage content={message.content} role={role} />;
  }
  return (
    <div className="flex justify-start">
      <div className="max-w-[calc(100%-2rem)] rounded-2xl border border-[var(--app-border)] px-4 py-3 text-sm text-[var(--app-text)]">
        <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          {role}
        </div>
        <ChatMarkdown content={message.content} />
      </div>
    </div>
  );
}

function DesktopV3UserMessage({
  content,
  pendingLabel,
}: {
  content: string;
  pendingLabel?: string;
}) {
  return (
    <div className="flex justify-end pr-0">
      <div className="max-w-[70%] rounded-xl bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)] shadow-sm">
        <div className="whitespace-pre-wrap break-words">{content}</div>
        {pendingLabel ? (
          <div className="mt-1 text-right text-[10px] uppercase tracking-[0.12em] opacity-70">
            {pendingLabel}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function DesktopV3PendingUserMessage({
  message,
}: {
  message: PendingUserMessage;
}) {
  return (
    <DesktopV3UserMessage
      content={message.content}
      pendingLabel={
        message.status === "failed" ? message.error || "failed" : undefined
      }
    />
  );
}

function DesktopV3CompactPendingState() {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          <LoaderCircle
            size={12}
            className="animate-spin text-[var(--app-primary)]"
          />
          Compacting context
        </div>
        <div className="text-[var(--app-text-muted)]">
          Waiting for the backend compact cursor…
        </div>
      </div>
    </div>
  );
}

function DesktopV3AssistantMessage({
  content,
  role,
}: {
  content: string;
  role: string;
}) {
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)]">
        {role === "reasoning" ? (
          <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
            reasoning
          </div>
        ) : null}
        <ChatMarkdown content={content} />
      </div>
    </div>
  );
}

function DesktopV3ToolMessage({
  content,
  toolMessage,
  thinkingTagsEnabled = true,
}: {
  content: string;
  toolMessage: StructuredToolMessage | null;
  thinkingTagsEnabled?: boolean;
}) {
  const isBash = toolMessage?.tool.trim().toLowerCase() === "bash";
  return (
    <div className="flex justify-start">
      <div
        className={cn(
          "min-w-0",
          isBash ? "w-full max-w-full" : "max-w-[calc(100%-2rem)]",
        )}
      >
        <ChatMarkdown
          content={content}
          toolMessage={toolMessage ?? undefined}
          thinkingTagsEnabled={thinkingTagsEnabled}
        />
      </div>
    </div>
  );
}

function DesktopV3ReasoningMessage({
  item,
  thinkingTagsEnabled,
  timerNow,
}: {
  item: Extract<DesktopV3RenderItem, { type: "live-reasoning" }>;
  thinkingTagsEnabled: boolean;
  timerNow: number;
}) {
  const body = reasoningBody(item.text, item.summary, thinkingTagsEnabled);
  const label = reasoningHeadline(
    item.state,
    item.startedAt,
    item.completedAt ?? null,
    timerNow,
  );
  const StateIcon =
    item.state === "running"
      ? LoaderCircle
      : item.state === "error"
        ? XCircle
        : CheckCircle2;
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          <StateIcon
            size={12}
            className={
              item.state === "running"
                ? "animate-spin text-[var(--app-primary)]"
                : item.state === "error"
                  ? "text-[var(--app-danger)]"
                  : "text-[var(--app-text-subtle)]"
            }
          />
          {label}
        </div>
        {body ? <ChatMarkdown content={body} /> : null}
      </div>
    </div>
  );
}

function DesktopV3LiveToolCall({
  tool,
}: {
  tool: LiveRunOverlay["toolCallsByCallId"][string];
}) {
  const state: ToolMessageState =
    tool.status === "failed" || tool.status === "error"
      ? "error"
      : tool.status === "completed" ||
          tool.status === "done" ||
          tool.status === "cancelled" ||
          tool.status === "canceled"
        ? "done"
        : "running";
  const output = tool.outputText ?? "";
  const args = tool.argumentsText ?? "";
  const error = tool.errorText?.trim() || (state === "error" ? output : "");
  const parsed = buildStructuredToolMessage({
    pathId: "run.v3.provider-tool-result.v1",
    tool: tool.toolName || "tool",
    callId: tool.callId,
    toolInstanceId: tool.toolInstanceId,
    argumentsText: args,
    outputText: output,
    error,
    durationMs: tool.durationMs,
    state,
    taskStream: tool.taskStream,
  });
  if (parsed && tool.timelineSeq) parsed.timelineSeq = tool.timelineSeq;
  return <DesktopV3ToolMessage content="" toolMessage={parsed} />;
}

export function chatMessageToMessageSnapshot(
  message: ChatMessageRecord,
): MessageSnapshot {
  return {
    id: message.id,
    session_id: message.sessionId,
    global_seq: message.globalSeq,
    role: message.role,
    content: message.content,
    metadata: message.metadata,
    created_at: message.createdAt,
  };
}

function DesktopV3ConversationLoadingSpinner() {
  return (
    <div
      className="flex min-h-full items-center justify-center py-16"
      role="status"
      aria-label="Loading conversation"
    >
      <Loader2
        size={28}
        strokeWidth={2.2}
        className="animate-spin text-[var(--app-primary)]"
      />
    </div>
  );
}

function DesktopV3ChatInlineState({
  title,
  description,
  tone = "muted",
}: {
  title: string;
  description: string;
  tone?: "muted" | "error";
}) {
  return (
    <div
      className={cn(
        "py-16 text-center",
        tone === "error"
          ? "text-[var(--app-danger)]"
          : "text-[var(--app-text-muted)]",
      )}
    >
      <div className="text-sm font-semibold text-[var(--app-text)]">
        {title}
      </div>
      <p className="mt-2 text-sm">{description}</p>
    </div>
  );
}

function DesktopV3ChatStateCard({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="flex h-full flex-1 items-center justify-center px-6 text-center">
      <div className="max-w-lg">
        <div className="text-lg font-semibold">{title}</div>
        <p className="mt-2 text-sm text-[var(--app-text-muted)]">
          {description}
        </p>
      </div>
    </div>
  );
}
