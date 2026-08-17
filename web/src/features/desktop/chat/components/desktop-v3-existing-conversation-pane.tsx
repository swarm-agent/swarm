import {
  memo,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ComponentProps,
  type MutableRefObject,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import {
  ArrowDown,
  ArrowRight,
  CheckCircle2,
  Check,
  ChevronDown,
  CircleAlert,
  CircleDot,
  Copy,
  LoaderCircle,
  Loader2,
  Github,
  ExternalLink,
  GalleryHorizontal,
  ListChecks,
  MessageCircle,
  XCircle,
} from "lucide-react";
import { cn } from "../../../../lib/cn";
import { ChatMarkdown, SearchReadToolGroupView } from "./chat-markdown";
import {
  buildStructuredToolMessage,
} from "../services/tool-message";
import {
  selectDesktopPlanExecutionView,
  selectDesktopV3TaskChildViewModel,
  type DesktopPlanExecutionView,
  type RenderedSessionMessages,
} from "../../state/desktop-v3-cache-selectors";
import type {
  DesktopV3ArtifactSelectionReference,
  DesktopV3CacheState,
  DesktopV3MediaReference,
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
  ModelProfileRecord,
  ChatMessageRecord,
  DesktopPlanFinalHandoff,
  DesktopPlanFinalHandoffSuggestedPrompt,
  TaskChildCardActions,
  TaskToolRow,
} from "../types/chat";
import {
  getDesktopSessionStopTarget,
  resolveDesktopChatRouteFromSession,
  type DesktopChatRoute,
} from "../services/chat-routing";
import {
  agentStateQueryOptions,
  modelOptionsQueryOptions,
  modelProfilesQueryOptions,
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
  modelOptionKey,
  normalizeModelID,
  normalizeProviderID,
} from "../services/model-options";
import {
  activeModelProfileFromMetadata,
  preferenceFromModelProfile,
  preferenceFromModelProfileMetadata,
} from "../services/model-profiles";
import { createModelProfile, invalidateModelProfiles, setDefaultModelProfile, updateModelProfile } from "../queries/model-profile-queries";
import {
  preferenceFromAgentModelLock,
  resolveDesktopV3AgentModelLock,
  resolveDesktopV3SessionAgentModelLock,
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
  sessionV3ModelProfileSettingsMutationResponse,
  updateSessionV3Agent,
  updateSessionV3ModelProfile,
  stopSessionV3Run,
} from "../../session-v3/api";
import { getDesktopV3MediaCapability, uploadDesktopV3MediaAsset } from "../../session-v3/write-api";
import { admitComposerFile } from "../services/composer-attachments";
import type { DesktopVideoSourceAttachment } from "../services/video-source-attachments";
import {
  clearDesktopV3ExistingMessageOperation,
  continueDesktopV3Conversation,
  createDesktopV3ExistingMessageOperation,
  loadDesktopV3ExistingMessageOperation,
  persistDesktopV3ExistingMessageOperation,
  type DesktopV3ExistingMessageOperation,
} from "../../session-v3/existing-session-flow";
import { compactDesktopV3Session } from "../../session-v3/compact-session-flow";
import { selectAndHydrateDesktopV3Session } from "../../state/desktop-v3-session-hydrator";
import {
  acceptAndContinueDesktopPlanCheckpoint,
  archiveDesktopV3Sessions,
  restartDesktopPlanCheckpoint,
  resumeDesktopPlanCheckpoint,
} from "../../session-v3/plan-execution-api";
import {
  fetchSessionMessages,
  resolveSessionPermission,
} from "../queries/chat-queries";
import type { AgentModelControlConfirmInput } from "./agent-model-control";
import { DesktopPermissionModal } from "../../permissions/components/desktop-permission-modal";
import {
  isPlanProposalPermission,
  permissionDisplayToolName,
  permissionRequiresApproval,
} from "../../permissions/services/permission-payload";
import { DesktopInlineBashPermissionCard } from "./desktop-inline-bash-permission-card";
import {
  DesktopInlinePlanReviewCard,
  structuredPlanDocumentFromPermission,
} from "./desktop-inline-plan-review-card";
import { DesktopPlanAgentSidecar } from "./desktop-plan-agent-sidecar";
import {
  DesktopPlanExecutionSidebar,
  type DesktopPlanExecutionSidebarActionInput,
} from "./desktop-plan-execution-sidebar";
import { normalizeDesktopPlanFinalHandoff } from "../services/session-plan-record";
import { DesktopV3ArtifactGallery, type DesktopV3ArtifactGalleryEntry } from "./desktop-v3-artifact-gallery";
import { DesktopV3ArtifactSidebar, desktopV3ArtifactsForSession, desktopV3HasPendingVisualSwarm, desktopV3MobileVisualSwarmArtifactToOpen, desktopV3NextSessionSidebarView } from "./desktop-v3-artifact-sidebar";
import { appendDesktopV3ArtifactMessageSelections, desktopV3ArtifactCatalogEntryForViewerLocation, desktopV3ArtifactCatalogEntryKey, desktopV3ArtifactCollectionViewerHref, desktopV3ArtifactCollectionViewerSearch, desktopV3ArtifactMessageSelection, desktopV3ArtifactViewerHref, desktopV3ArtifactViewerLocation, desktopV3ArtifactViewerSearch, fetchDesktopV3ArtifactCatalog, type DesktopV3ArtifactCatalogEntry, type DesktopV3ArtifactMessageSelection } from "../../session-v3/artifact-api";
import { DesktopV3ArtifactPreviewThumbnail } from "./desktop-v3-artifact-preview-thumbnail";
import { useDesktopV3OpenArtifactCatalogRefresh } from "../../session-v3/use-artifact-catalog-refresh";
import {
  desktopV3ActiveSessionSidebarView,
  effectiveDesktopSidebarDisplayMode,
  loadDesktopSidebarDisplayMode,
  type DesktopSidebarDisplayMode,
  type DesktopV3SessionSidebarView,
} from "./desktop-sidebar-display";

const PLAN_SIDEBAR_MEDIA_QUERY = "(min-width: 1300px)";

function usePlanSidebarViewport(): boolean {
  const [matches, setMatches] = useState(() => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia(PLAN_SIDEBAR_MEDIA_QUERY).matches);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const query = window.matchMedia(PLAN_SIDEBAR_MEDIA_QUERY);
    const update = () => setMatches(query.matches);
    update();
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  return matches;
}

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
    mode: normalizeSessionMode(input.cacheSession?.mode || input.session?.mode),
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
    left.paused === right.paused &&
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

type DesktopV3PlanTransitionTone = "primary" | "success" | "warning" | "danger";

type DesktopV3PlanHandoffRenderFields = {
  message: MessageSnapshot;
  headline: string;
  body: string;
  summary: string;
  finalHandoff: DesktopPlanFinalHandoff | null;
  timelineSeq?: number;
};

export type DesktopV3RenderItem =
  | {
      type: "plan-break";
      message: MessageSnapshot;
      headline: string;
      details: string[];
      tone: DesktopV3PlanTransitionTone;
      timelineSeq?: number;
    }
  | ({
      type: "plan-checkpoint-handoff";
    } & DesktopV3PlanHandoffRenderFields)
  | ({
      type: "plan-final-handoff";
    } & DesktopV3PlanHandoffRenderFields)
  | ({
      type: "plan-blocked-handoff";
    } & DesktopV3PlanHandoffRenderFields)
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
  | {
      type: "search-read-group";
      id: string;
      toolMessages: StructuredToolMessage[];
      timelineSeq?: number;
    }
  | { type: "live-working"; id: string; timelineSeq?: number };

type DesktopV3ScrollBehavior = "auto" | "smooth";

const DESKTOP_V3_BOTTOM_BUFFER_PX = 140;
const DESKTOP_V3_HISTORY_AUTOLOAD_TOP_PX = 320;
const DESKTOP_V3_EXACT_BOTTOM_PX = 1;

function desktopV3BottomDistance(element: HTMLElement): number {
  return Math.max(
    0,
    element.scrollHeight - element.scrollTop - element.clientHeight,
  );
}

export function resolveDesktopV3StickyBottomAttachment(options: {
  bottomDistance: number;
  wasAttached: boolean;
  userEscapeIntent?: boolean;
  bottomBufferPx?: number;
}): boolean {
  if (options.bottomDistance <= DESKTOP_V3_EXACT_BOTTOM_PX) return true;
  if (!options.wasAttached) return false;
  return !(
    options.userEscapeIntent &&
    options.bottomDistance >
      (options.bottomBufferPx ?? DESKTOP_V3_BOTTOM_BUFFER_PX)
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
  const userEscapeIntentRef = useRef(false);
  const pointerScrollIntentRef = useRef(false);
  const lastScrollTopRef = useRef(0);
  const touchYRef = useRef<number | null>(null);
  const frameRef = useRef<number | null>(null);
  const preserveTopAnchorRef = useRef<{
    scrollHeight: number;
    scrollTop: number;
  } | null>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);

  const cancelScheduledScroll = useCallback(() => {
    if (frameRef.current === null) return;
    window.cancelAnimationFrame(frameRef.current);
    frameRef.current = null;
  }, []);

  const setPinnedStateFromElement = useCallback(
    (element: HTMLElement) => {
      const bottomDistance = desktopV3BottomDistance(element);
      const attached = resolveDesktopV3StickyBottomAttachment({
        bottomDistance,
        wasAttached: autoFollowRef.current,
        userEscapeIntent: userEscapeIntentRef.current,
        bottomBufferPx,
      });
      autoFollowRef.current = attached;
      if (!attached || bottomDistance <= DESKTOP_V3_EXACT_BOTTOM_PX) {
        userEscapeIntentRef.current = false;
      }
      setIsAtBottom(attached);
      return attached;
    },
    [bottomBufferPx],
  );

  const pinToLatest = useCallback(
    (options: { behavior?: DesktopV3ScrollBehavior } = {}) => {
      const element = scrollContainerRef.current;
      autoFollowRef.current = true;
      setIsAtBottom(true);
      if (!element) return;
      userEscapeIntentRef.current = false;
      pointerScrollIntentRef.current = false;
      if (options.behavior === "smooth") {
        element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
        return;
      }
      element.scrollTop = element.scrollHeight;
      lastScrollTopRef.current = element.scrollTop;
    },
    [],
  );

  const scrollToBottom = useCallback(
    (behavior: DesktopV3ScrollBehavior = "auto") => {
      pinToLatest({ behavior });
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
    userEscapeIntentRef.current = false;
    cancelScheduledScroll();
    setIsAtBottom(false);
  }, [cancelScheduledScroll]);

  const scheduleAutoFollow = useCallback(
    () => {
      if (suppressAutoFollowOnceRef.current) {
        suppressAutoFollowOnceRef.current = false;
        return;
      }
      if (!autoFollowRef.current) return;
      if (frameRef.current !== null) return;
      frameRef.current = window.requestAnimationFrame(() => {
        frameRef.current = null;
        if (!autoFollowRef.current) return;
        pinToLatest();
      });
    },
    [pinToLatest],
  );

  useEffect(() => {
    const element = scrollContainerRef.current;
    if (!element) return;
    lastScrollTopRef.current = element.scrollTop;
    const markUpwardIntent = () => {
      userEscapeIntentRef.current = true;
      cancelScheduledScroll();
      element.scrollTo({ top: element.scrollTop, behavior: "auto" });
    };
    const handleWheel = (event: WheelEvent) => {
      if (event.deltaY < 0) markUpwardIntent();
    };
    const handleTouchStart = (event: TouchEvent) => {
      touchYRef.current = event.touches[0]?.clientY ?? null;
    };
    const handleTouchMove = (event: TouchEvent) => {
      const nextY = event.touches[0]?.clientY ?? null;
      if (nextY !== null && touchYRef.current !== null && nextY > touchYRef.current) {
        markUpwardIntent();
      }
      touchYRef.current = nextY;
    };
    const handlePointerDown = (event: PointerEvent) => {
      pointerScrollIntentRef.current = event.pointerType === "mouse";
    };
    const handlePointerUp = () => {
      pointerScrollIntentRef.current = false;
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (["ArrowUp", "PageUp", "Home"].includes(event.key) || (event.key === " " && event.shiftKey)) {
        markUpwardIntent();
      }
    };
    const handleScroll = () => {
      if (pointerScrollIntentRef.current && element.scrollTop < lastScrollTopRef.current) {
        markUpwardIntent();
      }
      lastScrollTopRef.current = element.scrollTop;
      setPinnedStateFromElement(element);
    };
    handleScroll();
    element.addEventListener("wheel", handleWheel, { passive: true });
    element.addEventListener("touchstart", handleTouchStart, { passive: true });
    element.addEventListener("touchmove", handleTouchMove, { passive: true });
    element.addEventListener("pointerdown", handlePointerDown, { passive: true });
    window.addEventListener("pointerup", handlePointerUp, { passive: true });
    element.addEventListener("keydown", handleKeyDown);
    element.addEventListener("scroll", handleScroll, { passive: true });
    return () => {
      element.removeEventListener("wheel", handleWheel);
      element.removeEventListener("touchstart", handleTouchStart);
      element.removeEventListener("touchmove", handleTouchMove);
      element.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("pointerup", handlePointerUp);
      element.removeEventListener("keydown", handleKeyDown);
      element.removeEventListener("scroll", handleScroll);
    };
  }, [cancelScheduledScroll, setPinnedStateFromElement]);

  useEffect(() => {
    autoFollowRef.current = true;
    preserveTopAnchorRef.current = null;
    suppressAutoFollowOnceRef.current = false;
    userEscapeIntentRef.current = false;
    pointerScrollIntentRef.current = false;
    setIsAtBottom(true);
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
    preserveTopAnchorRef.current = null;
    setPinnedStateFromElement(element);
  }, [options.itemCount, setPinnedStateFromElement]);

  useEffect(() => {
    scheduleAutoFollow();
  }, [options.itemCount, scheduleAutoFollow]);

  useEffect(() => {
    if (!options.followKey) return;
    scheduleAutoFollow();
  }, [options.followKey, scheduleAutoFollow]);

  useEffect(() => {
    const scrollElement = scrollContainerRef.current;
    const contentElement = contentRef.current;
    if (!scrollElement || !contentElement) return;
    const handleObservedResize = () => scheduleAutoFollow();
    const handleObservedMutation = () =>
      scheduleAutoFollow();
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
  const identity = metadataString(message.metadata, "call_id")
    || message.toolMessage?.callId?.trim()
    || metadataString(message.metadata, "tool_instance_id")
    || message.toolMessage?.toolInstanceId?.trim();
  return identity ? `live-tool:${identity}` : "";
}

export function desktopV3RenderItemKey(item: DesktopV3RenderItem): string {
  switch (item.type) {
    case "plan-break":
    case "plan-checkpoint-handoff":
    case "plan-final-handoff":
    case "plan-blocked-handoff":
      return item.message.id;
    case "message":
      return item.renderKey || item.message.id;
    case "search-read-group":
      return item.id;
    case "pending-user":
      return item.message.messageId;
    default:
      return item.id;
  }
}

const TRANSCRIPT_ROW_ESTIMATE_PX = 120;
const TRANSCRIPT_ROW_GAP_PX = 20;

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

function structuredSearchReadMessage(item: DesktopV3RenderItem): StructuredToolMessage | null {
  const toolMessage = item.type === "message"
    ? item.message.toolMessage ?? null
    : item.type === "live-tool"
      ? structuredLiveToolMessage(item.tool)
      : null;
  const toolName = toolMessage?.tool.trim().toLowerCase();
  return toolName === "search" || toolName === "find" || toolName === "read" ? toolMessage : null;
}

export function groupDesktopV3SearchReadActivity(
  orderedItems: DesktopV3RenderItem[],
): DesktopV3RenderItem[] {
  const grouped: DesktopV3RenderItem[] = [];
  let pending: Array<{ item: DesktopV3RenderItem; toolMessage: StructuredToolMessage }> = [];

  const flush = () => {
    if (pending.length === 1) {
      grouped.push(pending[0].item);
    } else if (pending.length > 1) {
      const first = pending[0].item;
      grouped.push({
        type: "search-read-group",
        id: `search-read-group:${desktopV3RenderItemKey(first)}`,
        toolMessages: pending.map((entry) => entry.toolMessage),
        timelineSeq: renderItemTimelineSeq(first),
      });
    }
    pending = [];
  };

  for (const item of orderedItems) {
    const toolMessage = structuredSearchReadMessage(item);
    if (toolMessage) {
      pending.push({ item, toolMessage });
      continue;
    }
    flush();
    grouped.push(item);
  }
  flush();
  return grouped;
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

export function isDesktopV3PlanCheckpointHandoffMessage(
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
    metadataSource === "plan_execution_checkpoint_handoff" ||
    metadataKind === "plan_checkpoint_handoff"
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

function planTransitionTone(message: MessageSnapshot): DesktopV3PlanTransitionTone {
  const action = metadataString(message.metadata, "action").toLowerCase();
  const status = metadataString(message.metadata, "execution_status").toLowerCase();
  if (action === "mark_failed" || status === "failed") return "danger";
  if (action === "mark_blocked" || status === "blocked") return "warning";
  if (action === "complete_checkpoint" || action === "checkpoint_outcome" || action === "accept_checkpoint") return "success";
  return "primary";
}

function desktopV3PlanHandoffMatchKey(message: MessageSnapshot): string {
  const planId = metadataString(message.metadata, "plan_id");
  const checkpointId = metadataString(message.metadata, "checkpoint_id");
  if (!planId || !checkpointId) return "";
  return `${planId}\u0000${checkpointId}`;
}

function isDesktopV3RedundantFinalReviewMessage(
  message: MessageSnapshot,
  finalHandoffKeys: Set<string>,
): boolean {
  if (!isDesktopV3PlanExecutionBreakMessage(message)) return false;
  const action = metadataString(message.metadata, "action").toLowerCase();
  const nextAction = metadataString(message.metadata, "next_action").toLowerCase();
  if (
    nextAction !== "await_review" ||
    (action !== "complete_checkpoint" && action !== "checkpoint_outcome")
  ) {
    return false;
  }
  const matchKey = desktopV3PlanHandoffMatchKey(message);
  return matchKey !== "" && finalHandoffKeys.has(matchKey);
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
    tone: planTransitionTone(message),
    timelineSeq: message.global_seq,
  };
}

const HANDOFF_SUMMARY_OPEN_TAG = "<swarm-handoff-summary>";
const HANDOFF_SUMMARY_CLOSE_TAG = "</swarm-handoff-summary>";

export interface DesktopV3HandoffSummaryParts {
  body: string;
  summary: string;
}

function desktopV3MarkdownFenceRanges(content: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = [];
  const lines = content.match(/.*(?:\r?\n|$)/g) ?? [];
  let offset = 0;
  let open: { marker: "`" | "~"; length: number; start: number } | null = null;
  for (const line of lines) {
    const lineWithoutEnding = line.replace(/\r?\n$/, "");
    const fence = lineWithoutEnding.match(/^ {0,3}(`{3,}|~{3,})/);
    if (fence) {
      const marker = fence[1][0] as "`" | "~";
      if (!open) {
        open = { marker, length: fence[1].length, start: offset };
      } else if (marker === open.marker && fence[1].length >= open.length) {
        ranges.push([open.start, offset + line.length]);
        open = null;
      }
    }
    offset += line.length;
  }
  if (open) ranges.push([open.start, content.length]);
  return ranges;
}

function desktopV3TagOffsetsOutsideFences(
  content: string,
  tag: string,
  ranges: Array<[number, number]>,
): number[] {
  const offsets: number[] = [];
  let lineStart = 0;
  for (const line of content.match(/.*(?:\r?\n|$)/g) ?? []) {
    const lineWithoutEnding = line.replace(/\r?\n$/, "");
    const leadingWhitespace = lineWithoutEnding.length - lineWithoutEnding.trimStart().length;
    const offset = lineStart + leadingWhitespace;
    if (
      lineWithoutEnding.trim() === tag &&
      !ranges.some(([start, end]) => offset >= start && offset < end)
    ) {
      offsets.push(offset);
    }
    lineStart += line.length;
  }
  return offsets;
}

export function parseDesktopV3HandoffSummary(
  content: string,
): DesktopV3HandoffSummaryParts {
  const fallback = { body: content, summary: "" };
  const fenceRanges = desktopV3MarkdownFenceRanges(content);
  const opens = desktopV3TagOffsetsOutsideFences(
    content,
    HANDOFF_SUMMARY_OPEN_TAG,
    fenceRanges,
  );
  const closes = desktopV3TagOffsetsOutsideFences(
    content,
    HANDOFF_SUMMARY_CLOSE_TAG,
    fenceRanges,
  );
  if (opens.length !== 1 || closes.length !== 1) return fallback;
  const open = opens[0];
  const close = closes[0];
  const summaryStart = open + HANDOFF_SUMMARY_OPEN_TAG.length;
  if (close < summaryStart) return fallback;
  const summary = content.slice(summaryStart, close).trim();
  if (!summary) return fallback;
  const before = content.slice(0, open).trimEnd();
  const after = content
    .slice(close + HANDOFF_SUMMARY_CLOSE_TAG.length)
    .trimStart();
  return {
    body: [before, after].filter(Boolean).join("\n\n"),
    summary,
  };
}

type DesktopV3PlanHandoffType =
  | "plan-checkpoint-handoff"
  | "plan-final-handoff"
  | "plan-blocked-handoff";

function buildDesktopV3PlanHandoffItem(
  message: MessageSnapshot,
  type: DesktopV3PlanHandoffType,
): Extract<DesktopV3RenderItem, { type: DesktopV3PlanHandoffType }> {
  const lines = message.content.split(/\r?\n/);
  const headline =
    lines.find((line) => line.trim())?.trim() || "Checkpoint handoff";
  const headlineIndex = lines.findIndex((line) => line.trim());
  const bodyLines = headlineIndex >= 0 ? lines.slice(headlineIndex + 1) : [];
  const rawBody = bodyLines.join("\n").trim() || message.content.trim();
  const finalHandoff = type === "plan-final-handoff"
    ? normalizeDesktopPlanFinalHandoff(message.metadata?.final_handoff)
    : type === "plan-blocked-handoff"
      ? normalizeDesktopPlanFinalHandoff(message.metadata?.blocked_handoff)
      : null;
  const parsed = finalHandoff
    ? { body: "", summary: "" }
    : parseDesktopV3HandoffSummary(rawBody);
  return {
    type,
    message,
    headline: finalHandoff?.title || headline,
    body: parsed.body,
    summary: parsed.summary,
    finalHandoff,
    timelineSeq: message.global_seq,
  } as Extract<DesktopV3RenderItem, { type: DesktopV3PlanHandoffType }>;
}

export function buildDesktopV3LiveRunRenderItems(
  run: LiveRunOverlay,
  options: {
    assistantMessages?: Set<string>;
    reasoningMessages?: Set<string>;
    committedToolKeys?: Set<string>;
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
    const id = `live-tool:${tool.callId || tool.toolInstanceId}`;
    if (options.committedToolKeys?.has(id)) continue;
    items.push({
      type: "live-tool",
      id,
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
  const finalHandoffKeys = new Set(
    committedMessages
      .filter(isDesktopV3PlanFinalHandoffMessage)
      .map(desktopV3PlanHandoffMatchKey)
      .filter(Boolean),
  );
  const visibleCommittedMessages = committedMessages.filter(
    (message) => !isDesktopV3RedundantFinalReviewMessage(message, finalHandoffKeys),
  );
  const assistantMessages = canonicalContentSet(visibleCommittedMessages, "assistant");
  const reasoningMessages = canonicalContentSet(visibleCommittedMessages, "reasoning");
  const committedToolKeys = new Set<string>(
    visibleCommittedMessages.map(committedToolRenderKey).filter((key): key is string => Boolean(key)),
  );
  const items: DesktopV3RenderItem[] = [
    ...visibleCommittedMessages.map((message) =>
      isDesktopV3PlanExecutionBreakMessage(message)
        ? buildDesktopV3PlanExecutionBreakItem(message)
        : isDesktopV3PlanCheckpointHandoffMessage(message)
          ? buildDesktopV3PlanHandoffItem(message, "plan-checkpoint-handoff")
          : isDesktopV3PlanFinalHandoffMessage(message)
            ? buildDesktopV3PlanHandoffItem(message, "plan-final-handoff")
            : isDesktopV3PlanBlockedHandoffMessage(message)
              ? buildDesktopV3PlanHandoffItem(message, "plan-blocked-handoff")
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
        committedToolKeys,
      }),
    );
  }
  return groupDesktopV3SearchReadActivity(orderDesktopV3LiveRenderItems(items));
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
  /** Compatibility-only command seam; resolved routed mode is read-only here. */
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
  onOpenChildSession?: (sessionId: string, workspacePath: string) => void;
  sessionActions?: DesktopV3ChatHeaderSessionActions | null;
  onSlashCommand?: (
    command: DesktopSlashCommand,
    draft: string,
  ) => void | Promise<void>;
  agentSettingsOpenSignal?: number;
  agentSettingsInitialAgent?: string;
  composerFocusSignal?: number;
  onCompactingChange?: (sessionId: string, startedAt: number | null) => void;
  onArchivePlanSession?: (sessionId: string) => void;
  onOpenPlan?: () => void;
  onOpenActionSettings?: () => void;
  planSidebarBelowActions?: ReactNode;
  artifactSelectionRequest?: DesktopV3ArtifactMessageSelection | null;
  onArtifactSelectionRequestHandled?: () => void;
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

type DesktopV3ExistingComposerController = {
  setDraft: (draft: string) => void;
};

type DesktopV3ExistingConversationComposerProps = Omit<
  ComponentProps<typeof DesktopV3AgenticComposer>,
  "draft" | "onDraftChange" | "canSubmit" | "onSubmit"
> & {
  initialDraft: string;
  hasStoredOperation: boolean;
  canSubmitWithoutDraft: boolean;
  controllerRef: MutableRefObject<DesktopV3ExistingComposerController | null>;
  onSubmit: ComponentProps<typeof DesktopV3AgenticComposer>['onSubmit'];
};

export function DesktopV3ExistingConversationComposer({
  initialDraft,
  hasStoredOperation,
  canSubmitWithoutDraft,
  controllerRef,
  onSubmit,
  ...composerProps
}: DesktopV3ExistingConversationComposerProps) {
  const [draft, setDraft] = useState(initialDraft);

  useLayoutEffect(() => {
    const controller: DesktopV3ExistingComposerController = { setDraft };
    controllerRef.current = controller;
    return () => {
      if (controllerRef.current === controller) controllerRef.current = null;
    };
  }, [controllerRef]);

  return (
    <DesktopV3AgenticComposer
      {...composerProps}
      draft={draft}
      onDraftChange={setDraft}
      canSubmit={
        canSubmitWithoutDraft && (hasStoredOperation || Boolean(draft.trim()))
      }
      onSubmit={onSubmit}
    />
  );
}

export function DesktopV3ExistingConversationPane({
  modeCommand = null,
  onModeCommandHandled,
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
  onOpenChildSession,
  sessionActions = null,
  onSlashCommand,
  agentSettingsOpenSignal = 0,
  agentSettingsInitialAgent = "",
  composerFocusSignal = 0,
  onCompactingChange,
  onArchivePlanSession,
  onOpenPlan,
  onOpenActionSettings,
  planSidebarBelowActions,
  artifactSelectionRequest = null,
  onArtifactSelectionRequestHandled,
}: DesktopV3ExistingConversationPaneProps) {
  const normalizedSessionId = sessionId.trim();
  const navigate = useNavigate();
  const routeParams = useParams({ strict: false }) as { workspaceSlug?: unknown };
  const artifactRouteSearch = useSearch({ strict: false }) as { artifactSession?: unknown; artifact?: unknown; collection?: unknown };
  const queryClient = useQueryClient();
  const mountedRef = useRef(true);
  const operationRef = useRef<DesktopV3ExistingMessageOperation | null>(
    loadDesktopV3ExistingMessageOperation(normalizedSessionId),
  );
  const agentStateQuery = useQuery(agentStateQueryOptions());
  const modelOptionsQuery = useQuery(modelOptionsQueryOptions());
  const modelProfilesQuery = useQuery(modelProfilesQueryOptions());
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions());
  const authCredentialsQuery = useQuery({
    queryKey: ["auth-credentials", "desktop-composer"],
    queryFn: () => listAuthCredentials("", "", 200),
    staleTime: 30_000,
  });
  const agentState = agentStateQuery.data ?? EMPTY_AGENT_STATE;
  const modelOptions = modelOptionsQuery.data ?? [];
  const modelProfileState = modelProfilesQuery.data ?? { profiles: [], defaultProfileId: '' };
  const thinkingTagsEnabled = normalizeThinkingTagsEnabled(
    uiSettingsQuery.data,
  );
  const selectPlanExecutionViewForSession = useCallback(
    (state: DesktopV3CacheState) =>
      selectDesktopPlanExecutionView(state, normalizedSessionId),
    [normalizedSessionId],
  );
  const sessionMediaCapability = useDesktopV3CacheSelector(
    (state) => state.sessionViewsById[normalizedSessionId]?.media_capability ?? null,
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
  const pendingBashPermissions = pendingPermissions.filter(
    (permission) => permissionDisplayToolName(permission.toolName) === "bash",
  );
  const pendingModalPermissions = pendingPermissions.filter(
    (permission) =>
      !isPlanProposalPermission(permission) &&
      permissionDisplayToolName(permission.toolName) !== "bash",
  );
  const selectedPermission = pendingModalPermissions[0] ?? null;
  const pendingPlanPermission = pendingPlanPermissions[0] ?? null;
  const pendingPlanDocument = useMemo(
    () => pendingPlanPermission
      ? structuredPlanDocumentFromPermission(pendingPlanPermission)
      : null,
    [pendingPlanPermission],
  );
  const [planAgentMobileOpen, setPlanAgentMobileOpen] = useState(false);
  const planSidebarViewport = usePlanSidebarViewport();
  useEffect(() => {
    setPlanAgentMobileOpen(false);
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
    cacheSession?.metadata ?? session?.metadata ?? metadata;
  const headerBranchLabel =
    session?.worktreeBranch?.trim() ||
    cacheSession?.worktree_branch?.trim() ||
    session?.gitBranch?.trim() ||
    metadataString(sessionMetadata, "swarm_v3_branch_label") ||
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
  const composerControllerRef = useRef<DesktopV3ExistingComposerController | null>(null);
  const [galleryArtifactSelectionRequest, setGalleryArtifactSelectionRequest] = useState<DesktopV3ArtifactMessageSelection[] | null>(null);
  const pendingExternalArtifactSelectionRequestRef = useRef("");
  const externalArtifactSelectionRequestRef = useRef(artifactSelectionRequest);
  externalArtifactSelectionRequestRef.current = artifactSelectionRequest;
  const externalArtifactSelectionRequestHandledRef = useRef(onArtifactSelectionRequestHandled);
  externalArtifactSelectionRequestHandledRef.current = onArtifactSelectionRequestHandled;
  const galleryArtifactSelectionInFlightRef = useRef(false);
  const queuedGalleryArtifactSelectionsRef = useRef<DesktopV3ArtifactMessageSelection[]>([]);
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
  const sessionAgentModelLock = useMemo(
    () => resolveDesktopV3SessionAgentModelLock(sessionMetadata),
    [sessionMetadata],
  );
  const selectedAgentModelLock = useMemo(
    () =>
      sessionAgentModelLock
      ?? resolveDesktopV3AgentModelLock(agentState.profiles, selectedAgent),
    [agentState.profiles, selectedAgent, sessionAgentModelLock],
  );
  const lockedPolicyPreference = useMemo(
    () => policyLockedPreference(cachedAgentModelPolicy),
    [cachedAgentModelPolicy],
  );
  const cachedPolicyMatchesSelectedMode = mode === settingsBaseline.mode;
  const sessionActiveModelProfile = useMemo(() => activeModelProfileFromMetadata(sessionMetadata), [sessionMetadata]);
  const composerActiveModelProfile = selectedAgent.trim().toLowerCase() === 'swarm'
    ? { source: 'agent-default' as const, profileId: '', name: 'Swarm model' }
    : sessionActiveModelProfile;
  const sessionProfilePreference = useMemo(
    () => preferenceFromModelProfileMetadata(sessionMetadata, mode),
    [mode, sessionMetadata],
  );
  const sessionAgentPreference = useMemo(
    () => sessionAgentModelLock
      ? preferenceFromAgentModelLock(sessionAgentModelLock, preference, modelOptions)
      : null,
    [modelOptions, preference, sessionAgentModelLock],
  );
  const displayedPreference = cachedPolicyMatchesSelectedMode
    ? (lockedPolicyPreference ?? preference)
    : (sessionProfilePreference ?? sessionAgentPreference ?? preference);
  // Header identity is presentation-only and must come from the hydrated session
  // snapshot/view. Local profile-picker state must never appear before resolution.
  const canonicalHeaderPreference = sessionProfilePreference ?? cachedPreference;
  const canonicalHeaderModelKey = modelOptionKey(
    canonicalHeaderPreference.provider,
    canonicalHeaderPreference.model,
    canonicalHeaderPreference.contextMode,
  );
  const canonicalHeaderModelOption = modelOptions.find(
    (option) => option.key === canonicalHeaderModelKey,
  ) ?? null;
  const canonicalHeaderModelLabel = canonicalHeaderPreference.provider.trim()
    && canonicalHeaderPreference.model.trim()
      ? canonicalHeaderModelOption?.label || canonicalHeaderPreference.model
      : "";
  const selectedModelKey = modelOptionKey(
    displayedPreference.provider,
    displayedPreference.model,
    displayedPreference.contextMode,
  );
  const selectedModelOption =
    modelOptions.find((option) => option.key === selectedModelKey) ?? null;
  const hasResolvedPreference = Boolean(
    displayedPreference.provider.trim() &&
    displayedPreference.model.trim() &&
    displayedPreference.thinking.trim(),
  );
  const selectedModelAvailable = Boolean(
    selectedModelOption && hasResolvedPreference,
  );
  const needsAuth = desktopProviderNeedsAuth(
    displayedPreference.provider,
    authCredentialsQuery.data,
  );
  const mediaCapabilityQuery = useQuery({
    queryKey: ['desktop-v3-media-capability', normalizedSessionId, selectedAgent, displayedPreference.provider, displayedPreference.model, authCredentialsQuery.dataUpdatedAt],
    queryFn: () => getDesktopV3MediaCapability(normalizedSessionId),
    enabled: Boolean(normalizedSessionId),
    staleTime: 0,
  });
  const mediaCapability = mediaCapabilityQuery.data ?? sessionMediaCapability;
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
  const routeWorkspaceSlug = typeof routeParams.workspaceSlug === "string"
    ? routeParams.workspaceSlug.trim()
    : "";
  const openPermissionsSettings = useCallback(() => {
    const search = { tab: "permissions" as const, returnSessionId: normalizedSessionId };
    if (routeWorkspaceSlug) {
      void navigate({ to: "/$workspaceSlug/settings", params: { workspaceSlug: routeWorkspaceSlug }, search });
      return;
    }
    void navigate({ to: "/settings", search });
  }, [navigate, normalizedSessionId, routeWorkspaceSlug]);
  const [planSidebarAvailableWidth, setPlanSidebarAvailableWidth] = useState(0);
  const planSidebarGridRef = useRef<HTMLDivElement | null>(null);
  const [sessionArtifacts, setSessionArtifacts] = useState<DesktopV3ArtifactCatalogEntry[]>([]);
  const [sessionArtifactsLoading, setSessionArtifactsLoading] = useState(false);
  const [sessionArtifactsError, setSessionArtifactsError] = useState("");
  const [sidebarView, setSidebarView] = useState<DesktopV3SessionSidebarView>("plan");
  const [artifactGalleryOpen, setArtifactGalleryOpen] = useState(false);
  const [artifactGalleryInitialKey, setArtifactGalleryInitialKey] = useState("");
  const [artifactGalleryInitialCollectionId, setArtifactGalleryInitialCollectionId] = useState("");
  const [artifactComposerFocusSignal, setArtifactComposerFocusSignal] = useState(0);
  const dismissedArtifactViewerLocationKeyRef = useRef("");
  const openedMobileVisualSwarmKeysRef = useRef(new Set<string>());
  const artifactSidebarSessionRef = useRef("");
  const priorSessionArtifactCountRef = useRef(0);
  const priorSessionHasPlanRef = useRef(false);
  const preferredPlanSidebarMode = useMemo(loadDesktopSidebarDisplayMode, []);
  const planSidebarDisplayMode: DesktopSidebarDisplayMode =
    effectiveDesktopSidebarDisplayMode(
      preferredPlanSidebarMode,
      planSidebarAvailableWidth,
    );
  useEffect(() => {
    const element = planSidebarGridRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const update = () => setPlanSidebarAvailableWidth(element.clientWidth);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const refreshSessionArtifacts = useCallback(async () => {
    if (!normalizedSessionId) return;
    setSessionArtifactsLoading(true);
    setSessionArtifactsError("");
    try {
      const catalog = await fetchDesktopV3ArtifactCatalog();
      if (artifactSidebarSessionRef.current !== normalizedSessionId) return;
      setSessionArtifacts(desktopV3ArtifactsForSession(catalog, normalizedSessionId));
    } catch (error) {
      setSessionArtifactsError(error instanceof Error ? error.message : "Session artifacts failed to load");
    } finally {
      setSessionArtifactsLoading(false);
    }
  }, [normalizedSessionId]);
  useDesktopV3OpenArtifactCatalogRefresh(Boolean(normalizedSessionId), refreshSessionArtifacts);
  useEffect(() => {
    artifactSidebarSessionRef.current = normalizedSessionId;
    priorSessionArtifactCountRef.current = 0;
    priorSessionHasPlanRef.current = false;
    setSessionArtifacts([]);
    setSidebarView("plan");
    setArtifactGalleryOpen(false);
    setArtifactGalleryInitialKey("");
    setArtifactGalleryInitialCollectionId("");
    dismissedArtifactViewerLocationKeyRef.current = "";
    openedMobileVisualSwarmKeysRef.current.clear();
    void refreshSessionArtifacts();
  }, [normalizedSessionId, refreshSessionArtifacts]);

  const taskChildActions = useMemo<TaskChildCardActions>(() => ({
    workspaceSlug: routeWorkspaceSlug,
    parentSessionId: normalizedSessionId,
    onNavigate: (childSessionId, workspacePath) => {
      const normalizedChildSessionId = childSessionId.trim();
      if (!normalizedChildSessionId) return;
      if (onOpenChildSession) {
        onOpenChildSession(normalizedChildSessionId, workspacePath);
        return;
      }
      if (!routeWorkspaceSlug) return;
      void selectAndHydrateDesktopV3Session(normalizedChildSessionId);
      void navigate({
        to: "/$workspaceSlug/$sessionId",
        params: { workspaceSlug: routeWorkspaceSlug, sessionId: normalizedChildSessionId },
      });
    },
  }), [navigate, normalizedSessionId, onOpenChildSession, routeWorkspaceSlug]);
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
  const canSubmitWithoutDraft = Boolean(
    normalizedSessionId &&
    !sending &&
    !compacting &&
    selectedAgent.trim() &&
    selectedModelAvailable,
  );
  const renderItems = useMemo(
    () => buildDesktopV3ConversationRenderItems(renderedMessages),
    [renderedMessages],
  );

  const taskChildRows = useMemo<TaskToolRow[]>(() => {
    const rows: TaskToolRow[] = [];
    for (const item of renderItems) {
      if (item.type === "message") {
        if (item.message.toolMessage?.tool === "task") rows.push(...item.message.toolMessage.taskRows);
      } else if (item.type === "live-tool" && item.tool.toolName === "task") {
        const tool = item.tool;
        const state: ToolMessageState = tool.status === "failed" || tool.status === "error"
          ? "error"
          : ["completed", "done", "cancelled", "canceled"].includes(tool.status ?? "") ? "done" : "running";
        const parsed = buildStructuredToolMessage({
          pathId: "run.v3.provider-tool-result.v1",
          tool: tool.toolName || "task",
          callId: tool.callId,
          toolInstanceId: tool.toolInstanceId,
          argumentsText: tool.argumentsText ?? "",
          outputText: tool.outputText ?? "",
          error: tool.errorText ?? "",
          durationMs: tool.durationMs,
          state,
          taskStream: tool.taskStream,
        });
        if (parsed) rows.push(...parsed.taskRows);
      }
    }
    const bySession = new Map<string, TaskToolRow>();
    for (const row of rows) bySession.set(row.childSessionId || row.launchKey || String(row.launchIndex), row);
    return [...bySession.values()];
  }, [renderItems]);
  const taskChildren = useDesktopV3CacheSelector((state) => taskChildRows.map((row) => ({ row, view: selectDesktopV3TaskChildViewModel(state, row) })));
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
  const hasSessionArtifacts = sessionArtifacts.length > 0;
  const hasPendingVisualSwarm = desktopV3HasPendingVisualSwarm(sessionArtifacts);
  const showConversationSidebar = showPlanSidebar || hasSessionArtifacts;
  useEffect(() => {
    if (artifactSidebarSessionRef.current !== normalizedSessionId) return;
    const previousCount = priorSessionArtifactCountRef.current;
    const previousHasPlan = priorSessionHasPlanRef.current;
    setSidebarView((current) => desktopV3NextSessionSidebarView({
      current,
      previousArtifactCount: previousCount,
      artifactCount: sessionArtifacts.length,
      hasPlan: showPlanSidebar,
      prioritizePlan: Boolean(pendingPlanDocument) || (showPlanSidebar && !previousHasPlan),
      hasPendingVisualSwarm,
    }));
    priorSessionArtifactCountRef.current = sessionArtifacts.length;
    priorSessionHasPlanRef.current = showPlanSidebar;
  }, [hasPendingVisualSwarm, normalizedSessionId, pendingPlanDocument, sessionArtifacts.length, showPlanSidebar]);
  const activeSidebarView = desktopV3ActiveSessionSidebarView({
    selected: sidebarView,
    hasPlan: showPlanSidebar,
    hasArtifacts: hasSessionArtifacts,
  });
  const artifactViewerLocation = useMemo(
    () => desktopV3ArtifactViewerLocation(normalizedSessionId, artifactRouteSearch),
    [
      artifactRouteSearch.artifact,
      artifactRouteSearch.artifactSession,
      artifactRouteSearch.collection,
      normalizedSessionId,
    ],
  );
  const artifactViewerLocationKey = artifactViewerLocation
    ? `${artifactViewerLocation.sessionId}:${artifactViewerLocation.collectionId ?? ""}:${artifactViewerLocation.artifactId ?? ""}`
    : "";
  const artifactViewerEntry = artifactViewerLocation
    ? desktopV3ArtifactCatalogEntryForViewerLocation(sessionArtifacts, artifactViewerLocation)
    : undefined;
  const artifactViewerHref = useCallback((artifact: DesktopV3ArtifactCatalogEntry) => {
    if (!routeWorkspaceSlug) return '#';
    return desktopV3ArtifactViewerHref(routeWorkspaceSlug, artifact);
  }, [routeWorkspaceSlug]);
  const artifactCollectionViewerHref = useCallback((artifact: DesktopV3ArtifactCatalogEntry) => {
    if (!routeWorkspaceSlug || !artifact.collectionId) return '#';
    return desktopV3ArtifactCollectionViewerHref(routeWorkspaceSlug, {
      sessionId: artifact.lineage?.parentSessionId || artifact.sessionId,
      collectionId: artifact.collectionId,
    });
  }, [routeWorkspaceSlug]);
  const openArtifactFullView = useCallback((artifact: DesktopV3ArtifactCatalogEntry) => {
    const artifactKey = desktopV3ArtifactCatalogEntryKey(artifact);
    setArtifactGalleryInitialCollectionId("");
    setArtifactGalleryInitialKey(artifactKey);
    setArtifactGalleryOpen(true);
    if (!routeWorkspaceSlug) return;
    void navigate({
      to: "/$workspaceSlug/$sessionId",
      params: { workspaceSlug: routeWorkspaceSlug, sessionId: artifact.sessionId },
      search: (previous) => ({ ...previous, artifact: undefined, collection: undefined, ...desktopV3ArtifactViewerSearch(artifact) }),
    });
  }, [navigate, routeWorkspaceSlug]);
  const navigateArtifactViewer = useCallback((artifact: DesktopV3ArtifactCatalogEntry) => {
    if (!routeWorkspaceSlug) return;
    setArtifactGalleryInitialCollectionId("");
    setArtifactGalleryInitialKey(desktopV3ArtifactCatalogEntryKey(artifact));
    void navigate({
      to: "/$workspaceSlug/$sessionId",
      params: { workspaceSlug: routeWorkspaceSlug, sessionId: artifact.sessionId },
      search: (previous) => ({ ...previous, artifact: undefined, collection: undefined, ...desktopV3ArtifactViewerSearch(artifact) }),
      replace: true,
    });
  }, [navigate, routeWorkspaceSlug]);
  const navigateArtifactCollectionViewer = useCallback((artifact: DesktopV3ArtifactCatalogEntry) => {
    const collectionId = artifact.collectionId?.trim() ?? "";
    const sessionId = artifact.lineage?.parentSessionId || artifact.sessionId;
    if (!routeWorkspaceSlug || !collectionId || !sessionId) return;
    setArtifactGalleryInitialKey("");
    setArtifactGalleryInitialCollectionId(collectionId);
    void navigate({
      to: "/$workspaceSlug/$sessionId",
      params: { workspaceSlug: routeWorkspaceSlug, sessionId },
      search: (previous) => ({
        ...previous,
        artifact: undefined,
        collection: undefined,
        ...desktopV3ArtifactCollectionViewerSearch({ sessionId, collectionId }),
      }),
      replace: true,
    });
  }, [navigate, routeWorkspaceSlug]);
  const setArtifactGalleryOpenFromViewer = useCallback((nextOpen: boolean) => {
    setArtifactGalleryOpen(nextOpen);
    if (nextOpen) {
      dismissedArtifactViewerLocationKeyRef.current = "";
      return;
    }
    dismissedArtifactViewerLocationKeyRef.current = artifactViewerLocationKey;
    if (!routeWorkspaceSlug) return;
    void navigate({
      to: "/$workspaceSlug/$sessionId",
      params: { workspaceSlug: routeWorkspaceSlug, sessionId: normalizedSessionId },
      search: (previous) => ({
        ...previous,
        artifactSession: undefined,
        artifact: undefined,
        collection: undefined,
      }),
      replace: true,
    });
  }, [artifactViewerLocationKey, navigate, normalizedSessionId, routeWorkspaceSlug]);
  useEffect(() => {
    if (!artifactViewerLocation) {
      dismissedArtifactViewerLocationKeyRef.current = "";
      setArtifactGalleryOpen(false);
      return;
    }
    if (dismissedArtifactViewerLocationKeyRef.current === artifactViewerLocationKey) return;
    setArtifactGalleryOpen(true);
    if (artifactViewerEntry) {
      setArtifactGalleryInitialCollectionId("");
      setArtifactGalleryInitialKey(desktopV3ArtifactCatalogEntryKey(artifactViewerEntry));
    } else if (artifactViewerLocation.collectionId && !artifactViewerLocation.artifactId) {
      setArtifactGalleryInitialKey("");
      setArtifactGalleryInitialCollectionId(artifactViewerLocation.collectionId);
    }
  }, [artifactViewerEntry, artifactViewerLocation, artifactViewerLocationKey]);
  useEffect(() => {
    if (!hasPendingVisualSwarm) return;
    const unopenedArtifact = desktopV3MobileVisualSwarmArtifactToOpen({
      artifacts: sessionArtifacts,
      sessionId: normalizedSessionId,
      sidebarViewport: planSidebarViewport,
      openedGroupKeys: openedMobileVisualSwarmKeysRef.current,
    });
    if (!unopenedArtifact) return;
    const iterationGroupId = unopenedArtifact.lineage?.iterationGroupId.trim();
    if (!iterationGroupId) return;
    for (const artifact of desktopV3ArtifactsForSession(sessionArtifacts, normalizedSessionId)) {
      const pendingGroupId = artifact.status === "staging" ? artifact.lineage?.iterationGroupId.trim() : "";
      if (pendingGroupId) openedMobileVisualSwarmKeysRef.current.add(`${normalizedSessionId}:${pendingGroupId}`);
    }
    setArtifactGalleryInitialKey(desktopV3ArtifactCatalogEntryKey(unopenedArtifact));
    setArtifactGalleryOpen(true);
  }, [hasPendingVisualSwarm, normalizedSessionId, planSidebarViewport, sessionArtifacts]);
  const {
    scrollContainerRef,
    contentRef,
    isAtBottom,
    scrollToBottom,
    preserveScrollPositionForPrepend,
  } = useDesktopV3StickyBottomScroll({
    resetKey: normalizedSessionId,
    itemCount: renderItems.length,
    followKey: scrollFollowKey,
  });
  const transcriptVirtualizer = useVirtualizer({
    count: renderItems.length,
    getScrollElement: () => scrollContainerRef.current,
    estimateSize: () => TRANSCRIPT_ROW_ESTIMATE_PX,
    getItemKey: (index) => desktopV3RenderItemKey(renderItems[index]),
    measureElement: (element) => element.getBoundingClientRect().height,
    overscan: 8,
  });
  const virtualTranscriptRows = transcriptVirtualizer.getVirtualItems();
  const runStatusModel: DesktopV3RunStatusModel | null =
    compactStartedAt !== null
      ? {
          kind: "active",
          label: "Compacting",
          startedAt: compactStartedAt,
          active: true,
        }
      : canonicalRunStatusModel;
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
    composerControllerRef.current?.setDraft(operation?.request.content ?? "");
    galleryArtifactSelectionInFlightRef.current = false;
    queuedGalleryArtifactSelectionsRef.current = [];
    pendingExternalArtifactSelectionRequestRef.current = "";
    setGalleryArtifactSelectionRequest(null);
    setSendError(null);
  }, [normalizedSessionId]);

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
    if (sessionProfilePreference) {
      setPreference((current) =>
        preferencesEqual(current, sessionProfilePreference)
          ? current
          : sessionProfilePreference,
      );
      return;
    }
    if (sessionAgentPreference) {
      setPreference((current) =>
        preferencesEqual(current, sessionAgentPreference)
          ? current
          : sessionAgentPreference,
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
    sessionAgentPreference,
    sessionProfilePreference,
  ]);

  useEffect(() => {
    if (modeCommand === "toggle-plan-auto") onModeCommandHandled?.();
  }, [modeCommand, onModeCommandHandled]);

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
      const nextLock = resolveDesktopV3SessionAgentModelLock(agentResponse.metadata)
        ?? resolveDesktopV3AgentModelLock(agentState.profiles, normalizedAgentName);
      if (nextLock.locked) {
        setPreference((current) => preferenceFromAgentModelLock(nextLock, current, modelOptions));
      }
    } catch (error) {
      if (mountedRef.current) setSendError(error instanceof Error ? error.message : "Failed to switch agent");
      throw error;
    }
  }

  async function handleApplyModelFavorite(profile: ModelProfileRecord) {
    if (!normalizedSessionId) return;
    const nextPreference = preferenceFromModelProfile(profile, mode, Date.now());
    if (!nextPreference) throw new Error("Model favorite does not resolve for the current chat mode");
    const response = await updateSessionV3ModelProfile(normalizedSessionId, {
      kind: 'temporary',
      profile: {
        name: profile.name,
        provider: profile.provider,
        model: profile.model,
        thinking: profile.thinking,
        serviceTier: profile.serviceTier,
        contextMode: profile.contextMode,
      },
    });
    dispatchDesktopV3Cache({
      type: "mutation.sessionSettingsResult",
      raw: sessionV3ModelProfileSettingsMutationResponse(response, normalizedSessionId),
    });
    setPreference(nextPreference);
    unlockedPreferenceRef.current = nextPreference;
    localSettingsDirtyRef.current.preference = false;
  }

  async function handleConfirmAgentSettings(
    input: AgentModelControlConfirmInput,
  ) {
    if (!normalizedSessionId || agentModelSaving) return;
    setAgentModelSaving(true);
    setSendError(null);
    try {
      if (input.agentName.trim().toLowerCase() === 'swarm') {
        throw new Error('Configure Swarm Action and Plan models directly in agent setup.');
      }
      let appliedProfile = input.modelProfile;
      let profileChoice: { kind: 'temporary'; profile: typeof input.modelProfile } | { kind: 'saved'; profileId: string };
      if (input.persistence === 'create' || input.persistence === 'create-copy') {
        const saved = await createModelProfile(input.modelProfile);
        appliedProfile = saved;
        if (input.makeDefault) await setDefaultModelProfile(saved.profileId);
        await invalidateModelProfiles(queryClient);
        profileChoice = { kind: 'saved', profileId: saved.profileId };
      } else if (input.persistence === 'update') {
        const saved = await updateModelProfile(input.profileId, input.modelProfile);
        appliedProfile = saved;
        if (input.makeDefault) await setDefaultModelProfile(saved.profileId);
        await invalidateModelProfiles(queryClient);
        profileChoice = { kind: 'saved', profileId: saved.profileId };
      } else {
        profileChoice = { kind: 'temporary', profile: input.modelProfile };
      }
      const nextPreference = preferenceFromModelProfile(appliedProfile, mode, Date.now());
      if (!nextPreference) throw new Error("Model profile does not resolve for the selected mode");
      const profileResponse = await updateSessionV3ModelProfile(normalizedSessionId, profileChoice);
      dispatchDesktopV3Cache({
        type: "mutation.sessionSettingsResult",
        raw: sessionV3ModelProfileSettingsMutationResponse(profileResponse, normalizedSessionId),
      });
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
      setPreference(nextPreference);
      unlockedPreferenceRef.current = nextPreference;
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

  async function handleSubmit(submittedDraft: string, attachments: DesktopV3MediaReference[], artifactSelections: DesktopV3ArtifactMessageSelection[], videoAttachments: DesktopVideoSourceAttachment[]) {
    if (!normalizedSessionId || sending || compacting) return;

    setSending(true);
    setSendError(null);
    scrollToBottom("smooth");
    try {
      if (!selectedModelAvailable) {
        throw new Error("Select a model and thinking level before sending");
      }
      await persistVisibleSettings();
      const retainedOperation = operationRef.current;
      if (retainedOperation) {
        const sameDraft = retainedOperation.request.content === submittedDraft.trim();
        const sameMedia = JSON.stringify(retainedOperation.request.media ?? []) === JSON.stringify(attachments);
        const retainedArtifacts = retainedOperation.request.artifact_selections ?? [];
        const sameArtifacts = JSON.stringify(retainedArtifacts) === JSON.stringify(artifactSelections);
        const sameVideos = JSON.stringify(retainedOperation.request.video_attachments ?? []) === JSON.stringify(videoAttachments);
        if (!sameDraft || !sameMedia || !sameArtifacts || !sameVideos) {
          throw new Error("Retry the retained message without changing its text or attachments");
        }
      }
      const operation =
        retainedOperation ??
        createDesktopV3ExistingMessageOperation({
          sessionId: normalizedSessionId,
          prompt: submittedDraft,
          metadata,
          media: attachments,
          videoAttachments,
          artifactSelections,
        });
      operationRef.current = operation;
      persistDesktopV3ExistingMessageOperation(operation);

      await continueDesktopV3Conversation(operation);
      completeDesktopV3ExistingMessage({
        sessionId: normalizedSessionId,
        operation,
        mountedRef,
        setOperation: (nextOperation) => {
          operationRef.current = nextOperation;
        },
        setDraft: (nextDraft) => composerControllerRef.current?.setDraft(nextDraft),
      });
    } catch (error) {
      if (mountedRef.current) {
        setSendError(error instanceof Error ? error.message : String(error));
      }
      throw error;
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
    if (!normalizedSessionId || planExecutionBusyAction || currentRun) return;
    const busyKey = `${input.action}:${input.checkpointId ?? ""}`;
    setPlanExecutionBusyAction(busyKey);
    setSendError(null);
    scrollToBottom("smooth");
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
        case "resume_checkpoint":
          if (!input.checkpointId)
            throw new Error("Resume checkpoint requires checkpoint_id");
          await resumeDesktopPlanCheckpoint(
            normalizedSessionId,
            input.checkpointId,
          );
          break;
        case "restart_checkpoint":
          if (!input.checkpointId)
            throw new Error("Restart checkpoint requires checkpoint_id");
          await restartDesktopPlanCheckpoint(
            normalizedSessionId,
            input.checkpointId,
          );
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
  }

  async function handleResolveBashPermission(
    permission: DesktopPermissionRecord,
    action: "approve" | "deny" | "approve_always" | "always_deny",
    reason: string,
  ) {
    await resolvePermission(permission, action, reason);
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

  const submitRef = useRef(handleSubmit);
  submitRef.current = handleSubmit;
  const stableSubmit = useCallback(
    (submittedDraft: string, attachments: DesktopV3MediaReference[], artifactSelections: DesktopV3ArtifactMessageSelection[], videoAttachments: DesktopVideoSourceAttachment[]) => submitRef.current(submittedDraft, attachments, artifactSelections, videoAttachments),
    [],
  );

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

  const stableSuggestedPrompt = useCallback((prompt: string) => stableSubmit(prompt, [], [], []), [stableSubmit]);
  const prefillSuggestedPrompt = useCallback((prompt: string) => {
    const normalizedPrompt = prompt.trim();
    if (!normalizedPrompt) return;
    composerControllerRef.current?.setDraft(normalizedPrompt);
    setArtifactComposerFocusSignal((current) => current + 1);
  }, []);
  const queueGalleryArtifactSelections = useCallback((selections: DesktopV3ArtifactMessageSelection[]) => {
    if (selections.length === 0) return;
    try {
      const requestSelections = appendDesktopV3ArtifactMessageSelections([], selections);
      if (galleryArtifactSelectionInFlightRef.current) {
        queuedGalleryArtifactSelectionsRef.current = appendDesktopV3ArtifactMessageSelections(queuedGalleryArtifactSelectionsRef.current, requestSelections);
        setSendError(null);
        return;
      }
      galleryArtifactSelectionInFlightRef.current = true;
      setGalleryArtifactSelectionRequest([...requestSelections]);
      setSendError(null);
    } catch (error) {
      galleryArtifactSelectionInFlightRef.current = false;
      setGalleryArtifactSelectionRequest(null);
      setSendError(error instanceof Error ? error.message : "Artifact selection failed");
    }
  }, []);
  const handleGalleryArtifactSelectionRequest = useCallback(() => {
    galleryArtifactSelectionInFlightRef.current = false;
    setGalleryArtifactSelectionRequest(null);
    const queuedSelections = queuedGalleryArtifactSelectionsRef.current;
    queuedGalleryArtifactSelectionsRef.current = [];
    const externalRequest = externalArtifactSelectionRequestRef.current;
    if (queuedSelections.length === 0 && !externalRequest) return;
    queueMicrotask(() => {
      if (queuedSelections.length > 0) {
        queueGalleryArtifactSelections(queuedSelections);
        return;
      }
      if (!externalArtifactSelectionRequestRef.current || !externalRequest) return;
      queueGalleryArtifactSelections([externalRequest]);
      externalArtifactSelectionRequestHandledRef.current?.();
    });
  }, [queueGalleryArtifactSelections]);
  useEffect(() => {
    if (galleryArtifactSelectionRequest || !artifactSelectionRequest) return;
    const artifactSelectionRequestKey = JSON.stringify(artifactSelectionRequest);
    if (pendingExternalArtifactSelectionRequestRef.current === artifactSelectionRequestKey) return;
    const frame = requestAnimationFrame(() => {
      if (galleryArtifactSelectionInFlightRef.current) return;
      pendingExternalArtifactSelectionRequestRef.current = artifactSelectionRequestKey;
      queueGalleryArtifactSelections([artifactSelectionRequest]);
      onArtifactSelectionRequestHandled?.();
    });
    return () => cancelAnimationFrame(frame);
  }, [artifactSelectionRequest, galleryArtifactSelectionRequest, onArtifactSelectionRequestHandled, queueGalleryArtifactSelections]);

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
      className="relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-[var(--app-bg)]"
      data-desktop-chat-drop-zone
      data-testid="desktop-v3-existing-conversation-pane"
    >
      <DesktopV3ChatHeader
        title={session?.title || cacheSession?.title || "Conversation"}
        workspaceName={
          session?.workspaceName || cacheSession?.workspace_name || "Workspace"
        }
        branchName={headerBranchLabel}
        modelLabel={canonicalHeaderModelLabel}
        runStatus={runStatusModel}
        onOpenChats={onOpenChats}
        onNewSession={onNewSession}
        sessionActions={headerSessionActions}
      />
      <div
        ref={planSidebarGridRef}
        className={cn(
          "grid min-h-0 min-w-0 flex-1 grid-cols-[minmax(0,1fr)] overflow-hidden",
          showConversationSidebar && planSidebarDisplayMode === "full"
            ? "min-[1300px]:grid-cols-[minmax(0,1fr)_360px]"
            : "",
          showConversationSidebar && planSidebarDisplayMode === "compact"
            ? "min-[1300px]:grid-cols-[minmax(0,1fr)_280px]"
            : "",
          showConversationSidebar && planSidebarDisplayMode === "thin"
            ? "min-[1300px]:grid-cols-[minmax(0,1fr)_56px]"
            : "",
        )}
        data-plan-sidebar-mode={planSidebarDisplayMode}
        data-session-sidebar-view={activeSidebarView}
      >
        <div className="flex min-h-0 min-w-0 flex-col overflow-hidden">
          <div className="relative min-h-0 min-w-0 flex-1 overflow-hidden">
            <div
              ref={scrollContainerRef}
              className="h-full min-h-0 overflow-x-hidden overflow-y-auto py-6 [scrollbar-gutter:stable_both-edges]"
              data-testid="desktop-chat-scroller"
              tabIndex={0}
            >
              {/* Match the composer's 70rem frame, then double its 16/24px frame padding so both message edges sit exactly 16/24px inside the outlined composer. */}
              <div
                ref={contentRef}
                className="mx-auto flex min-h-full w-full min-w-0 max-w-[70rem] flex-col gap-5 px-8 [&>*:not(:last-child)]:[overflow-anchor:none] sm:px-12"
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
                {renderItems.length > 0 ? (
                  <div
                    className="relative min-w-0 shrink-0"
                    style={{ height: `${transcriptVirtualizer.getTotalSize()}px` }}
                    data-testid="desktop-chat-virtual-transcript"
                  >
                    {virtualTranscriptRows.map((virtualRow) => {
                      const item = renderItems[virtualRow.index];
                      const itemKey = desktopV3RenderItemKey(item);
                      return (
                        <div
                          key={virtualRow.key}
                          ref={transcriptVirtualizer.measureElement}
                          data-index={virtualRow.index}
                          className="absolute left-0 top-0 w-full min-w-0"
                          style={{ transform: `translateY(${virtualRow.start}px)`, paddingBottom: `${TRANSCRIPT_ROW_GAP_PX}px` }}
                          data-testid="desktop-chat-row"
                          data-render-item-type={item.type}
                          data-render-item-key={itemKey}
                        >
                          <DesktopV3RenderItemView
                            item={item}
                            thinkingTagsEnabled={thinkingTagsEnabled}
                            index={virtualRow.index}
                            taskChildActions={taskChildActions}
                            onSuggestedPrompt={stableSuggestedPrompt}
                            onPrefillPrompt={prefillSuggestedPrompt}
                            artifactCatalog={sessionArtifacts}
                            artifactHref={artifactViewerHref}
                            onArtifactNavigate={navigateArtifactViewer}
                            onArtifactSelections={queueGalleryArtifactSelections}
                          />
                        </div>
                      );
                    })}
                  </div>
                ) : null}
                {pendingBashPermissions.map((permission) => (
                  <DesktopInlineBashPermissionCard
                    key={`bash-permission:${permission.id}`}
                    permission={permission}
                    pendingCount={pendingBashPermissions.length}
                    sessionMode={sessionMode}
                    onResolve={handleResolveBashPermission}
                    onOpenPermissions={openPermissionsSettings}
                  />
                ))}
                {pendingPlanPermissions.map((permission, index) => (
                  <DesktopInlinePlanReviewCard
                    key={permission.id}
                    permission={permission}
                    parentSessionId={normalizedSessionId}
                    pendingPosition={index + 1}
                    pendingCount={pendingPlanPermissions.length}
                    onResolve={resolvePermission}
                  />
                ))}
                <div
                  aria-hidden="true"
                  data-testid="desktop-chat-tail-anchor"
                  className="h-px shrink-0 [overflow-anchor:auto]"
                />
              </div>
            </div>
            {!isAtBottom ? (
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

          {pendingPlanDocument && pendingPlanPermission && !planSidebarViewport ? (
            <div
              className="shrink-0 bg-[var(--app-surface)] min-[1300px]:hidden"
              id="desktop-plan-agent-composer-region"
              data-testid="desktop-plan-agent-composer-region"
            >
              {planAgentMobileOpen ? (
                <DesktopPlanAgentSidecar
                  parentSessionId={normalizedSessionId}
                  permission={pendingPlanPermission}
                  document={pendingPlanDocument}
                  embedded
                  mobileInline
                  mobileOpen
                  modelLabel={displayedPreference.model}
                  onClose={() => setPlanAgentMobileOpen(false)}
                />
              ) : (
                <div className="mx-auto w-full max-w-[70rem] px-4 py-2 sm:px-6">
                  <button
                    type="button"
                    className="flex min-h-12 w-full items-center gap-3 rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-4 py-2.5 text-left transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
                    aria-expanded="false"
                    aria-controls="mobile-plan-agent-panel"
                    onClick={() => setPlanAgentMobileOpen(true)}
                  >
                    <MessageCircle className="size-4 shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm font-semibold text-[var(--app-text)]">Ask Swarm Plan</span>
                      <span className="block truncate text-xs text-[var(--app-text-muted)]">Waiting for your review · Talk through changes before approval</span>
                    </span>
                    <ChevronDown className="size-4 shrink-0 -rotate-90 text-[var(--app-text-muted)]" aria-hidden="true" />
                  </button>
                </div>
              )}
            </div>
          ) : null}

          {!pendingPlanDocument && showPlanExecutionSidebar && planExecutionView ? (
            <div
              className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] min-[1300px]:hidden"
              data-testid="desktop-plan-execution-composer-region"
            >
              <details className="group mx-auto w-full max-w-[70rem] px-4 sm:px-6">
                <summary className="flex min-h-12 cursor-pointer list-none items-center gap-3 py-2.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--app-primary)] [&::-webkit-details-marker]:hidden">
                  <span className="flex min-w-0 flex-1 items-center gap-2.5">
                    <span className="shrink-0 text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]">
                      Plan
                    </span>
                    <span className="min-w-0 truncate text-xs font-medium text-[var(--app-text)]">
                      {planExecutionView.activeCheckpoint?.title || planExecutionView.plan.title || "Plan execution"}
                    </span>
                  </span>
                  {planExecutionView.policyMode !== "automatic" ? (
                    <span className="shrink-0 text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-primary)]">
                      Review each
                    </span>
                  ) : null}
                  <span className="hidden shrink-0 text-[10px] font-medium capitalize text-[var(--app-text-muted)] sm:inline">
                    {(planExecutionView.activeCheckpoint?.status || planExecutionView.status || "ready").replace(/_/g, " ")}
                  </span>
                  <ChevronDown
                    aria-hidden="true"
                    className="size-4 shrink-0 text-[var(--app-text-muted)] transition-transform group-open:rotate-180"
                  />
                </summary>
                <div className="max-h-[min(46vh,30rem)] overflow-y-auto border-t border-[var(--app-border)] py-4">
                  {hasSessionArtifacts ? (
                    <div className="mx-4 mb-3 grid grid-cols-2 gap-1 rounded-lg bg-[var(--app-bg-alt)] p-1 sm:mx-6" role="tablist" aria-label="Mobile session sidebar view" data-mobile-session-sidebar-toggle>
                      <button type="button" role="tab" aria-selected={activeSidebarView === "plan"} aria-label="Show plan" onClick={() => setSidebarView("plan")} className={cn("inline-flex min-h-9 items-center justify-center gap-1.5 rounded-md px-2 text-xs font-semibold transition", activeSidebarView === "plan" ? "bg-[var(--app-surface)] text-[var(--app-text)] shadow-sm" : "text-[var(--app-text-muted)] hover:text-[var(--app-text)]")}><ListChecks size={14} aria-hidden="true" />Plan</button>
                      <button type="button" role="tab" aria-selected={activeSidebarView === "artifacts"} aria-label={`Show ${sessionArtifacts.length} session artifacts`} onClick={() => setSidebarView("artifacts")} className={cn("inline-flex min-h-9 items-center justify-center gap-1.5 rounded-md px-2 text-xs font-semibold transition", activeSidebarView === "artifacts" ? "bg-[var(--app-surface)] text-[var(--app-text)] shadow-sm" : "text-[var(--app-text-muted)] hover:text-[var(--app-text)]")}><GalleryHorizontal size={14} aria-hidden="true" />Artifacts {sessionArtifacts.length}</button>
                    </div>
                  ) : null}
                  {activeSidebarView === "artifacts" ? (
                    <DesktopV3ArtifactSidebar artifacts={sessionArtifacts} displayMode="full" loading={sessionArtifactsLoading} error={sessionArtifactsError} embedded artifactHref={artifactViewerHref} onOpenArtifact={openArtifactFullView} onAddToChat={(artifacts) => queueGalleryArtifactSelections(artifacts.map((artifact) => desktopV3ArtifactMessageSelection(artifact, "select")))} />
                  ) : (
                    <DesktopPlanExecutionSidebar
                      view={planExecutionView}
                      embedded
                      busyAction={planExecutionBusyAction}
                      canStop={Boolean(currentRun)}
                      onAction={stablePlanExecutionAction}
                      onStop={stableStop}
                      onEditPlan={stableOpenPlan}
                      belowActions={planSidebarBelowActions}
                    />
                  )}
                </div>
              </details>
            </div>
          ) : null}

          <DesktopV3ExistingConversationComposer
            key={normalizedSessionId}
            workspacePath={session?.workspacePath?.trim() || cacheSession?.workspace_path?.trim() || metadataString(sessionMetadata, "workspace_path")}
            sessionId={normalizedSessionId}
            initialDraft={storedOperation?.request.content ?? ""}
            initialArtifactSelections={storedOperation?.request.artifact_selections ?? []}
            focusSignal={composerFocusSignal + artifactComposerFocusSignal}
            hasStoredOperation={hasStoredOperation}
            canSubmitWithoutDraft={canSubmitWithoutDraft}
            controllerRef={composerControllerRef}
            placeholder="Message Swarm…"
            inputLabel="Continue Desktop V3 conversation"
            disabled={sending || compacting}
            busy={sending || compacting}
            canStop={Boolean(currentRun)}
            error={sendError}
            artifactSelectionRequest={galleryArtifactSelectionRequest}
            onArtifactSelectionRequestHandled={handleGalleryArtifactSelectionRequest}
            mediaCapability={mediaCapability}
            onUploadAttachment={async (file, signal) => {
              const capability = await getDesktopV3MediaCapability(normalizedSessionId);
              const admission = admitComposerFile(file, capability);
              if (admission.kind !== 'media' || !capability.contract_token) throw new Error('This file type is not supported as media by the current model and credential.');
              const admitted = admission.capability;
              const fileType = admission.fileType;
              const mimeType = admission.mimeType;
              const declaredMIME = mimeType || (fileType ? (admitted.mime_types ?? []).find((value) => value.toLowerCase().endsWith(`/${fileType === 'jpg' ? 'jpeg' : fileType}`)) : undefined);
              if (!declaredMIME) throw new Error('The browser could not determine a supported media type for this attachment.');
              return uploadDesktopV3MediaAsset({ sessionId: normalizedSessionId, file, mimeType: declaredMIME, modality: admitted.modality, fileType, contractToken: capability.contract_token, signal });
            }}
            onSubmit={stableSubmit}
            onStop={handleStop}
            onCompact={handleCompact}
            mode={mode}
            showModePicker
            resolvedSessionControls
            currentAgent={selectedAgent || "Agent"}
            selectedPrimaryAgent={selectedAgent || ""}
            agents={agentState.profiles}
            modelProfiles={modelProfileState.profiles}
            activeModelProfile={composerActiveModelProfile}
            onUseAgentModelDefault={async () => {
              setAgentModelSaving(true);
              setSendError(null);
              try {
                const response = await updateSessionV3ModelProfile(normalizedSessionId, { kind: 'agent-default' });
                dispatchDesktopV3Cache({ type: 'mutation.sessionSettingsResult', raw: sessionV3ModelProfileSettingsMutationResponse(response, normalizedSessionId) });
              } catch (error) {
                setSendError(error instanceof Error ? error.message : 'Failed to use agent model default');
              } finally {
                setAgentModelSaving(false);
              }
            }}
            modelOptions={modelOptions}
            selectedModelKey={selectedModelKey}
            selectedServiceTier={displayedPreference.serviceTier}
            agentSettingsOpenSignal={agentSettingsOpenSignal}
            agentSettingsInitialAgent={agentSettingsInitialAgent}
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
              modelLabel: selectedModelOption?.label || displayedPreference.model,
              thinking: displayedPreference.thinking,
              serviceTier: serviceTierFromPreference(displayedPreference),
            })}
            onAgentSelect={handleAgentSelect}
            needsAuth={needsAuth}
            onOpenAuthSettings={handleOpenAuthSettings}
            onConfirmAgentSettings={handleConfirmAgentSettings}
            onApplyModelFavorite={handleApplyModelFavorite}
            agentModelControlBusy={agentModelSaving}
            thinking={displayedPreference.thinking}
            thinkingTagsEnabled={thinkingTagsEnabled}
            onThinkingTagsToggle={(enabled) => {
              void handleThinkingTagsToggle(enabled);
            }}
            thinkingTagsBusy={thinkingTagsSaving}
            contextLabel={contextLabel}
            contextTooltip={contextTooltip}
            compactDisabled={compacting || sending || Boolean(currentRun)}
            onSlashCommand={onSlashCommand}
            onOpenActionSettings={onOpenActionSettings}
          />
        </div>

        {showConversationSidebar ? (
          <div
            className={pendingPlanDocument
              ? "contents min-[1300px]:flex min-[1300px]:min-h-0 min-[1300px]:min-w-0 min-[1300px]:flex-col min-[1300px]:overflow-hidden"
              : "hidden min-h-0 min-w-0 overflow-hidden min-[1300px]:flex min-[1300px]:flex-col"}
            data-session-sidebar-column
            data-plan-sidebar-column={showPlanSidebar ? true : undefined}
          >
            {showPlanSidebar && hasSessionArtifacts ? (
              <div className={cn("shrink-0 border-b border-l border-[var(--app-border)]/60 bg-[var(--app-surface)]", planSidebarDisplayMode === "thin" ? "grid gap-1 p-1.5" : "grid grid-cols-2 gap-1 p-2")} role="tablist" aria-label="Session sidebar view" data-session-sidebar-toggle>
                <button type="button" role="tab" aria-selected={activeSidebarView === "plan"} aria-label="Show plan sidebar" title="Plan" onClick={() => setSidebarView("plan")} className={cn("inline-flex min-h-8 items-center justify-center gap-1.5 rounded-md px-2 text-xs font-semibold transition", activeSidebarView === "plan" ? "bg-[var(--app-surface-active)] text-[var(--app-text)]" : "text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]", planSidebarDisplayMode === "thin" && "px-0")}><ListChecks size={14} aria-hidden="true" />{planSidebarDisplayMode !== "thin" ? "Plan" : null}</button>
                <button type="button" role="tab" aria-selected={activeSidebarView === "artifacts"} aria-label={`Show ${sessionArtifacts.length} session artifacts`} title="Artifacts" onClick={() => setSidebarView("artifacts")} className={cn("inline-flex min-h-8 items-center justify-center gap-1.5 rounded-md px-2 text-xs font-semibold transition", activeSidebarView === "artifacts" ? "bg-[var(--app-surface-active)] text-[var(--app-text)]" : "text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]", planSidebarDisplayMode === "thin" && "px-0")}><GalleryHorizontal size={14} aria-hidden="true" />{planSidebarDisplayMode !== "thin" ? `Artifacts ${sessionArtifacts.length}` : null}</button>
              </div>
            ) : null}
            <div className={pendingPlanDocument
              ? "contents min-[1300px]:flex min-[1300px]:min-h-0 min-[1300px]:min-w-0 min-[1300px]:flex-1 min-[1300px]:flex-col min-[1300px]:overflow-hidden"
              : "flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"}>
              {activeSidebarView === "artifacts" ? (
                <DesktopV3ArtifactSidebar artifacts={sessionArtifacts} displayMode={planSidebarDisplayMode} loading={sessionArtifactsLoading} error={sessionArtifactsError} artifactHref={artifactViewerHref} onOpenArtifact={openArtifactFullView} onAddToChat={(artifacts) => queueGalleryArtifactSelections(artifacts.map((artifact) => desktopV3ArtifactMessageSelection(artifact, "select")))} />
              ) : pendingPlanDocument && pendingPlanPermission && planSidebarViewport ? (
                <DesktopPlanAgentSidecar
                  parentSessionId={normalizedSessionId}
                  permission={pendingPlanPermission}
                  document={pendingPlanDocument}
                  embedded
                  modelLabel={displayedPreference.model}
                  displayMode={planSidebarDisplayMode}
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
                  displayMode={planSidebarDisplayMode}
                  taskChildren={taskChildren}
                  taskChildActions={taskChildActions}
                />
              ) : null}
            </div>
          </div>
        ) : null}
      </div>

      <DesktopV3ArtifactGallery
        artifacts={sessionArtifacts}
        open={artifactGalleryOpen}
        onOpenChange={setArtifactGalleryOpenFromViewer}
        showTrigger={false}
        loading={sessionArtifactsLoading}
        error={sessionArtifactsError}
        title="Session artifacts"
        initialArtifactKey={artifactGalleryInitialKey}
        initialCollectionId={artifactGalleryInitialCollectionId}
        artifactHref={artifactViewerHref}
        collectionHref={artifactCollectionViewerHref}
        onArtifactNavigate={navigateArtifactViewer}
        onCollectionNavigate={navigateArtifactCollectionViewer}
        onAddToChat={(artifacts) => {
          queueGalleryArtifactSelections(artifacts.map(({ label, description, selection }) => ({ ...selection, label, description, action: "select" })));
          setArtifactGalleryOpenFromViewer(false);
          setArtifactComposerFocusSignal((current) => current + 1);
        }}
        onUseThisDesign={({ label, description, selection }) => {
          queueGalleryArtifactSelections([{ ...selection, label, description, action: "use" }]);
          setArtifactGalleryOpenFromViewer(false);
          setArtifactComposerFocusSignal((current) => current + 1);
        }}
        onSelectionPersisted={refreshSessionArtifacts}
      />

      <DesktopPermissionModal
        key={`permission:${normalizedSessionId}`}
        open={Boolean(selectedPermission)}
        permission={selectedPermission}
        pendingCount={pendingModalPermissions.length}
        sessionMode={sessionMode}
        onOpenChange={() => undefined}
        onOpenPermissions={openPermissionsSettings}
        onResolve={handleResolvePermission}
      />
    </div>
  );
}

export const DesktopV3RenderItemView = memo(function DesktopV3RenderItemView({
  item,
  thinkingTagsEnabled,
  taskChildActions,
  onSuggestedPrompt,
  onPrefillPrompt,
  artifactCatalog = [],
  artifactHref,
  onArtifactNavigate,
  onArtifactSelections,
}: {
  item: DesktopV3RenderItem;
  thinkingTagsEnabled: boolean;
  index: number;
  taskChildActions?: TaskChildCardActions;
  onSuggestedPrompt?: (prompt: string) => void | Promise<void>;
  onPrefillPrompt?: (prompt: string) => void;
  artifactCatalog?: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
  onArtifactSelections?: (selections: DesktopV3ArtifactMessageSelection[]) => void;
}) {
  switch (item.type) {
    case "plan-break":
      return <DesktopV3PlanExecutionBreak item={item} />;
    case "plan-checkpoint-handoff":
      return <DesktopV3PlanCheckpointHandoff item={item} />;
    case "plan-final-handoff":
      return <DesktopV3PlanFinalHandoff item={item} onSuggestedPrompt={onSuggestedPrompt} onPrefillPrompt={onPrefillPrompt} artifactCatalog={artifactCatalog} artifactHref={artifactHref} onArtifactNavigate={onArtifactNavigate} />;
    case "plan-blocked-handoff":
      return <DesktopV3PlanBlockedHandoff item={item} onSuggestedPrompt={onSuggestedPrompt} />;
    case "message":
      return (
        <DesktopV3CommittedMessage
          message={item.message}
          thinkingTagsEnabled={thinkingTagsEnabled}
          taskChildActions={taskChildActions}
          artifactCatalog={artifactCatalog}
          artifactHref={artifactHref}
          onArtifactNavigate={onArtifactNavigate}
          onArtifactSelections={onArtifactSelections}
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
        />
      );
    case "live-tool":
      return (
        <DesktopV3LiveToolCall
          tool={item.tool}
          taskChildActions={taskChildActions}
          artifactCatalog={artifactCatalog}
          artifactHref={artifactHref}
          onArtifactNavigate={onArtifactNavigate}
          onArtifactSelections={onArtifactSelections}
        />
      );
    case "search-read-group":
      return <SearchReadToolGroupView toolMessages={item.toolMessages} />;
    case "live-working":
      return null;
    default:
      return null;
  }
});

function planTransitionToneClass(tone: DesktopV3PlanTransitionTone): string {
  switch (tone) {
    case "success":
      return "bg-[var(--app-success-bg)] text-[var(--app-success)]";
    case "warning":
      return "bg-[var(--app-warning-bg)] text-[var(--app-warning)]";
    case "danger":
      return "bg-[var(--app-danger-bg)] text-[var(--app-danger)]";
    default:
      return "bg-[color-mix(in_srgb,var(--app-primary)_10%,transparent)] text-[var(--app-primary)]";
  }
}

type DesktopV3CheckpointReviewCard = {
  title: string;
  mode: string;
  checkpoint: string;
  next: string;
  plan: string;
  reportSummary: string;
  report: string;
  result: string;
  changedFiles: string[];
  validation: string[];
};

const CHECKPOINT_REVIEW_SECTION = /^(Report|Result|Changed files|Validation):(?:\s*(.*))?$/i;

function splitCheckpointReviewEntries(value: string, section: "changed files" | "validation"): string[] {
  if (!value.trim()) return [];
  const separator = section === "changed files"
    ? /;\s*/
    : /;\s+(?=(?:PASS|FAIL|WARN|SKIP|NOT RUN|Broad)\b)/i;
  return value
    .split(separator)
    .map((entry) => entry.replace(/^[-*]\s+/, "").trim())
    .filter(Boolean);
}

function parseDesktopV3CheckpointReviewCard(
  item: Extract<DesktopV3RenderItem, { type: "plan-break" }>,
): DesktopV3CheckpointReviewCard | null {
  const action = metadataString(item.message.metadata, "action").toLowerCase();
  const nextAction = metadataString(item.message.metadata, "next_action").toLowerCase();
  const reviewCard = action === "mark_needs_review"
    || nextAction === "await_review"
    || /(?:paused for review|review required)/i.test(item.headline);
  if (!reviewCard) return null;

  const headlineParts = item.headline.split(/\s+—\s+/);
  const title = headlineParts.shift()?.trim() || "Checkpoint review";
  const mode = headlineParts.join(" — ").trim();
  const checkpointLine = item.details.find((detail) => /^(Checkpoint|Completed|Resolved):/i.test(detail)) || "";
  const nextLine = item.details.find((detail) => /^Next:/i.test(detail)) || "";
  const planLine = item.details.find((detail) => /^Plan:/i.test(detail)) || "";
  const sectionLines = item.details.filter((detail) => detail !== checkpointLine && detail !== nextLine && detail !== planLine);
  const sections: Record<"report" | "result" | "changed files" | "validation", string[]> = {
    report: [],
    result: [],
    "changed files": [],
    validation: [],
  };
  let activeSection: keyof typeof sections | null = null;
  for (const line of sectionLines) {
    const match = line.match(CHECKPOINT_REVIEW_SECTION);
    if (match) {
      activeSection = match[1]?.toLowerCase() as keyof typeof sections;
      const firstLine = match[2]?.trim();
      if (firstLine) sections[activeSection].push(firstLine);
      continue;
    }
    if (activeSection) sections[activeSection].push(line);
  }

  const reportLines = sections.report;
  const reportSummary = reportLines.find((line) => !/^\s*(?:[#>*-]|\d+\.)\s*/.test(line)) || "";
  return {
    title,
    mode,
    checkpoint: checkpointLine.replace(/^(Checkpoint|Completed|Resolved):\s*/i, "").trim(),
    next: nextLine.replace(/^Next:\s*/i, "").trim(),
    plan: planLine.replace(/^Plan:\s*/i, "").trim(),
    reportSummary,
    report: reportLines.join("\n"),
    result: sections.result.join("\n"),
    changedFiles: splitCheckpointReviewEntries(sections["changed files"].join("\n"), "changed files"),
    validation: splitCheckpointReviewEntries(sections.validation.join("\n"), "validation"),
  };
}

function checkpointValidationSummary(validation: string[]): string {
  const passed = validation.filter((entry) => /^PASS\b/i.test(entry)).length;
  const attention = validation.filter((entry) => /^(?:FAIL|WARN)\b/i.test(entry)).length;
  return [
    passed > 0 ? `${passed} passed` : "",
    attention > 0 ? `${attention} need attention` : "",
  ].filter(Boolean).join(" · ") || `${validation.length} entries`;
}

function DesktopV3CheckpointReviewCardView({
  item,
  card,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-break" }>;
  card: DesktopV3CheckpointReviewCard;
}) {
  const checkpointId = metadataString(item.message.metadata, "checkpoint_id");
  return (
    <div
      className="flex w-full min-w-0 justify-start py-1"
      data-testid="desktop-v3-checkpoint-review-card"
      data-checkpoint-status="review"
      data-plan-transition-tone={item.tone}
    >
      <section
        aria-label="Checkpoint review"
        className="w-full min-w-0 overflow-hidden rounded-xl border border-[color-mix(in_srgb,var(--app-warning)_42%,var(--app-border))] bg-[var(--app-surface-subtle)] text-sm text-[var(--app-text)] shadow-sm"
      >
        <div className="flex min-w-0 items-start gap-3 border-b border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-warning-bg)_62%,var(--app-surface-subtle))] px-4 py-3">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-[var(--app-warning-bg)] text-[var(--app-warning)]">
            <CircleAlert size={16} aria-hidden="true" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <span className="text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-warning)]">Review required</span>
              {card.mode ? <span className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-1.5 py-0.5 text-[10px] font-medium text-[var(--app-text-muted)]">{card.mode}</span> : null}
            </div>
            <h3 className="mt-1 break-words text-[15px] font-semibold leading-6">{card.checkpoint || card.title}</h3>
            {card.reportSummary ? <p className="mt-1 break-words text-xs leading-5 text-[var(--app-text-muted)]">{card.reportSummary}</p> : null}
          </div>
        </div>

        <div className="grid min-w-0 gap-3 px-4 py-3">
          {card.next ? (
            <div className="flex min-w-0 items-start gap-2 rounded-lg border border-[color-mix(in_srgb,var(--app-warning)_28%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-warning-bg)_42%,transparent)] px-3 py-2">
              <ArrowRight size={14} className="mt-0.5 shrink-0 text-[var(--app-warning)]" aria-hidden="true" />
              <div className="min-w-0">
                <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Next</div>
                <div className="mt-0.5 break-words text-xs leading-5 text-[var(--app-text)]">{card.next}</div>
              </div>
            </div>
          ) : null}

          {card.result ? (
            <section aria-label="Review decision" className="min-w-0 border-l-2 border-[var(--app-warning)] pl-3">
              <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Decision needed</div>
              <ChatMarkdown content={card.result} className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]" />
            </section>
          ) : null}

          {(card.report || card.changedFiles.length > 0 || card.validation.length > 0) ? (
            <div className="grid min-w-0 gap-1 border-t border-[var(--app-border)] pt-2 text-xs" data-checkpoint-review-evidence>
              {card.report ? (
                <details>
                  <summary className="cursor-pointer py-1.5 font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)]">Full report</summary>
                  <div className="mt-1 min-w-0 border-l border-[var(--app-border)] pl-3 text-[var(--app-text-muted)]">
                    <ChatMarkdown content={card.report} />
                  </div>
                </details>
              ) : null}
              {card.changedFiles.length > 0 ? (
                <details>
                  <summary className="cursor-pointer py-1.5 font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)]">Files changed ({card.changedFiles.length})</summary>
                  <ul className="mt-1 grid gap-1 border-l border-[var(--app-border)] pl-3 font-mono text-[11px] text-[var(--app-text-muted)]">
                    {card.changedFiles.map((file, index) => <li key={`${item.message.id}:review-file:${index}`} className="break-all">{file}</li>)}
                  </ul>
                </details>
              ) : null}
              {card.validation.length > 0 ? (
                <details>
                  <summary className="cursor-pointer py-1.5 font-medium text-[var(--app-text-muted)] hover:text-[var(--app-text)]">Validation · {checkpointValidationSummary(card.validation)}</summary>
                  <ul className="mt-1 grid gap-1.5 border-l border-[var(--app-border)] pl-3 text-[var(--app-text-muted)]">
                    {card.validation.map((entry, index) => (
                      <li key={`${item.message.id}:review-validation:${index}`} className="flex min-w-0 items-start gap-2 break-words">
                        <span className={cn("mt-2 size-1.5 shrink-0 rounded-full", /^PASS\b/i.test(entry) ? "bg-[var(--app-success)]" : /^(?:FAIL|WARN)\b/i.test(entry) ? "bg-[var(--app-warning)]" : "bg-[var(--app-text-subtle)]")} aria-hidden="true" />
                        <span className="min-w-0">{entry}</span>
                      </li>
                    ))}
                  </ul>
                </details>
              ) : null}
            </div>
          ) : null}
        </div>

        {(card.plan || checkpointId) ? (
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-bg-alt)_42%,transparent)] px-4 py-2 text-[10px] text-[var(--app-text-subtle)]">
            {card.plan ? <span className="min-w-0 truncate">Plan: {card.plan}</span> : null}
            {checkpointId ? <code className="ml-auto shrink-0 font-mono">{checkpointId}</code> : null}
          </div>
        ) : null}
      </section>
    </div>
  );
}

function DesktopV3PlanExecutionBreak({
  item,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-break" }>;
}) {
  const reviewCard = parseDesktopV3CheckpointReviewCard(item);
  if (reviewCard) return <DesktopV3CheckpointReviewCardView item={item} card={reviewCard} />;

  const checkpoint = item.details.find((detail) => /^(Checkpoint|Completed|Resolved|Next):/i.test(detail));
  const context = item.details.find((detail) => /^(Context|Fresh context|Next):/i.test(detail) && detail !== checkpoint);
  const plan = item.details.find((detail) => /^Plan:/i.test(detail));
  const remainingDetails = item.details.filter((detail) => detail !== checkpoint && detail !== context && detail !== plan);

  return (
    <div
      className="flex w-full min-w-0 justify-start py-1"
      data-testid="desktop-v3-plan-execution-break"
      data-plan-transition-tone={item.tone}
    >
      <div className="w-full min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5">
        <div className="flex min-w-0 items-start gap-2.5">
          <span className={cn("mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-md", planTransitionToneClass(item.tone))}>
            <CircleDot size={13} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[13px] font-semibold leading-5 text-[var(--app-text)]">{item.headline}</div>
            {checkpoint ? <div className="break-words text-xs leading-5 text-[var(--app-text-muted)]">{checkpoint}</div> : null}
            {context ? <div className="break-words text-[11px] leading-4 text-[var(--app-text-subtle)]">{context}</div> : null}
            {remainingDetails.map((detail, index) => (
              <div key={`${item.message.id}:detail:${index}`} className="break-words text-[11px] leading-4 text-[var(--app-text-subtle)]">{detail}</div>
            ))}
            {plan ? <div className="mt-1 truncate text-[10px] text-[var(--app-text-subtle)]">{plan}</div> : null}
          </div>
        </div>
      </div>
    </div>
  );
}

function DesktopV3PlanCheckpointHandoff({
  item,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-checkpoint-handoff" }>;
}) {
  const title = handoffTitleDetail(item.headline, ["Checkpoint handoff"]);
  return (
    <div
      className="flex w-full min-w-0 justify-start py-1"
      data-testid="desktop-v3-plan-checkpoint-handoff"
    >
      <section
        aria-label="Checkpoint handoff"
        className="w-full min-w-0 rounded-xl border border-[color-mix(in_srgb,var(--app-primary)_35%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-primary)_6%,var(--app-surface-subtle))] px-4 py-3 text-sm leading-6 text-[var(--app-text)]"
      >
        <div className="flex min-w-0 items-start gap-3">
          <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-lg bg-[color-mix(in_srgb,var(--app-primary)_12%,transparent)] text-[var(--app-primary)]">
            <ArrowRight size={14} aria-hidden="true" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-primary)]">
              Checkpoint handoff
            </div>
            {title ? <h3 className="mt-1 break-words font-semibold text-[var(--app-text)]">{title}</h3> : null}
            <DesktopV3PlanHandoffContent item={item} />
          </div>
        </div>
      </section>
    </div>
  );
}

function DesktopV3PlanFinalHandoff({
  item,
  onSuggestedPrompt,
  onPrefillPrompt,
  artifactCatalog,
  artifactHref,
  onArtifactNavigate,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-final-handoff" }>;
  onSuggestedPrompt?: (prompt: string) => void | Promise<void>;
  onPrefillPrompt?: (prompt: string) => void;
  artifactCatalog: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
}) {
  if (item.finalHandoff) {
    return (
      <DesktopV3StructuredFinalHandoff
        item={item}
        handoff={item.finalHandoff}
        onSuggestedPrompt={onSuggestedPrompt}
        onPrefillPrompt={onPrefillPrompt}
        artifactCatalog={artifactCatalog}
        artifactHref={artifactHref}
        onArtifactNavigate={onArtifactNavigate}
      />
    );
  }
  return (
    <DesktopV3PlanHandoff
      item={item}
      icon={<CheckCircle2 size={12} className="text-[var(--app-primary)]" />}
      testId="desktop-v3-plan-final-handoff"
    />
  );
}

async function copyDesktopV3HandoffCode(code: string): Promise<void> {
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(code);
    return;
  }
  if (typeof document === "undefined") return;
  const textarea = document.createElement("textarea");
  textarea.value = code;
  textarea.setAttribute("readonly", "true");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  textarea.remove();
}

function DesktopV3HandoffCopyableCodeBlocks({ handoff, tone = "primary" }: { handoff: DesktopPlanFinalHandoff; tone?: "primary" | "warning" }) {
  const [copiedIndex, setCopiedIndex] = useState<number | null>(null);
  if (handoff.copyableCodeBlocks.length === 0) return null;
  return (
    <div className="mt-3 grid gap-2" data-handoff-copyable-code-blocks>
      {handoff.copyableCodeBlocks.map((block, index) => (
        <section key={`copyable-code:${index}`} className="overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-code-bg)]">
          <header className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-3 py-1.5">
            <span className="min-w-0 truncate text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{block.label || (block.language ? block.language : "Copy this")}</span>
            <button
              type="button"
              className="inline-flex shrink-0 items-center gap-1.5 rounded px-2 py-1 text-[10px] font-medium text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
              aria-label={`Copy ${block.label || "code"}`}
              onClick={() => { void copyDesktopV3HandoffCode(block.code).then(() => { setCopiedIndex(index); window.setTimeout(() => setCopiedIndex((current) => current === index ? null : current), 1600); }); }}
              data-handoff-copy-code
              data-handoff-code-tone={tone}
            >
              {copiedIndex === index ? <Check size={12} aria-hidden="true" /> : <Copy size={12} aria-hidden="true" />}
              {copiedIndex === index ? "Copied" : "Copy"}
            </button>
          </header>
          <pre className="max-h-64 overflow-auto whitespace-pre p-3 font-mono text-xs leading-5 text-[var(--app-text)]"><code className={block.language ? `language-${block.language}` : undefined}>{block.code}</code></pre>
        </section>
      ))}
    </div>
  );
}

export function selectDesktopV3SuggestedPrompt(
  prompt: string,
  onSuggestedPrompt?: (prompt: string) => void | Promise<void>,
): void {
  if (!prompt || !onSuggestedPrompt) return;
  void onSuggestedPrompt(prompt);
}

type DesktopV3FinalHandoffNextStep = DesktopPlanFinalHandoffSuggestedPrompt & {
  behavior: "send" | "prefill";
};

function finalHandoffActionLabel(value: string): string {
  const normalized = value.trim().replace(/[-_]+/g, " ");
  if (!normalized) return "Continue";
  return normalized.replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function buildDesktopV3FinalHandoffNextSteps(
  handoff: DesktopPlanFinalHandoff,
  includeRecommendationPrompt = true,
): DesktopV3FinalHandoffNextStep[] {
  if (handoff.suggestedPrompts.length > 0) {
    return handoff.suggestedPrompts.slice(0, 3).map((suggestion) => ({
      ...suggestion,
      behavior: /clar/i.test(suggestion.label) ? "prefill" : "send",
    }));
  }

  const steps: DesktopV3FinalHandoffNextStep[] = [];
  const recommendationAction = handoff.recommendation?.action.trim() ?? "";
  const normalizedAction = recommendationAction.toLowerCase().replace(/[-_]+/g, " ");
  const recommendationPrompt = handoff.recommendation?.prompt?.trim() ?? "";
  const isReviewOnly = /\breview\b/.test(normalizedAction);

  if (includeRecommendationPrompt && recommendationPrompt && !isReviewOnly) {
    steps.push({
      label: finalHandoffActionLabel(recommendationAction),
      prompt: recommendationPrompt,
      behavior: "send",
    });
  } else if (handoff.recommendation?.decision.trim().toLowerCase() === "change") {
    steps.push({
      label: "Request changes",
      prompt: "Please make the following changes: ",
      behavior: "prefill",
    });
  }

  if (handoff.details.changedFiles.length > 0 && steps.length < 3) {
    steps.push({
      label: "Commit changes",
      prompt: "Commit the completed changes with an appropriate commit message.",
      behavior: "send",
    });
  }

  const validationWasSkipped = handoff.details.validation.length === 0
    || handoff.details.validation.every((entry) => /not run|not requested|skipped/i.test(entry));
  if (validationWasSkipped && steps.length < 3) {
    steps.push({
      label: "Run focused tests",
      prompt: "Run the focused tests for these changes and report the results.",
      behavior: "send",
    });
  }

  const substantialHandoff = handoff.details.changedFiles.length >= 3 || handoff.impactBullets.length >= 2;
  if ((substantialHandoff || steps.length === 0) && steps.length < 3) {
    steps.push({
      label: "Ask for clarity",
      prompt: "I have a question about these changes: ",
      behavior: "prefill",
    });
  }

  return steps;
}

function DesktopV3StructuredFinalHandoff({
  item,
  handoff,
  onSuggestedPrompt,
  onPrefillPrompt,
  artifactCatalog,
  artifactHref,
  onArtifactNavigate,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-final-handoff" }>;
  handoff: DesktopPlanFinalHandoff;
  onSuggestedPrompt?: (prompt: string) => void | Promise<void>;
  onPrefillPrompt?: (prompt: string) => void;
  artifactCatalog: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
}) {
  const recommendation = handoff.recommendation;
  const details = handoff.details;
  const hasDetails = Boolean(details.report || details.result);
  const nextSteps = buildDesktopV3FinalHandoffNextSteps(handoff, handoff.artifacts.length === 0);
  const handoffArtifacts = handoff.artifacts.flatMap((artifact): DesktopV3ArtifactGalleryEntry[] => {
    const isVideoSource = Boolean(artifact.sourceRef);
    const isManagedArtifact = !isVideoSource && Boolean(artifact.sessionId || artifact.collectionId || artifact.eventSeq);
    const exactCatalogEntry = artifactCatalog.find((entry) => (
      entry.artifactId === artifact.artifactId
      && (!isManagedArtifact || (
        entry.sessionId === artifact.sessionId
        && entry.collectionId === artifact.collectionId
        && entry.eventSeq === artifact.eventSeq
      ))
    ));
    if (exactCatalogEntry) return [exactCatalogEntry];
    if (isManagedArtifact && (!artifact.sessionId || !artifact.collectionId || !artifact.eventSeq)) return [];
    const fallbackArtifact: DesktopV3ArtifactGalleryEntry = {
      ...artifact,
      sourceRef: artifact.sourceRef || "",
      sessionId: artifact.sessionId || item.message.session_id,
      collectionId: artifact.collectionId || "",
      eventSeq: artifact.eventSeq || 0,
      sessionTitle: "This session",
      workspacePath: "",
      workspaceName: "",
      planId: "",
      planTitle: "",
      checkpointId: "",
      checkpointTitle: "",
      collectionName: "",
      collectionDescription: "",
      filename: artifact.filename || artifact.label,
      kind: artifact.kind || artifact.mediaType,
      category: artifact.category || (artifact.mediaType === "text/html" || artifact.mediaType === "application/pdf" || artifact.mediaType.startsWith("image/") || artifact.mediaType.startsWith("video/") || artifact.kind === "video" ? "visual" : "document"),
      status: "ready",
      updatedAt: 0,
    };
    return [fallbackArtifact];
  });
  return (
    <div className="flex w-full min-w-0 justify-start py-1" data-testid="desktop-v3-plan-final-handoff">
      <section
        aria-label="Final handoff"
        className="w-full min-w-0 rounded-xl border border-[var(--app-border-active)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text)]"
        data-testid="desktop-v3-structured-final-handoff"
      >
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-primary)]">
              <CheckCircle2 size={13} aria-hidden="true" />
              Final handoff
            </div>
            <h3 className="mt-3 break-words text-base font-semibold leading-6">{handoff.title}</h3>
          </div>
        </div>

        <div className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]" data-final-handoff-overview>
          <ChatMarkdown content={handoff.overview} />
        </div>
        {handoff.impactBullets.length > 0 ? (
          <ul className="mt-2 grid gap-1.5 text-sm leading-5 text-[var(--app-text-muted)]" data-final-handoff-impact>
            {handoff.impactBullets.map((impact, index) => (
              <li key={`${item.message.id}:impact:${index}`} className="flex min-w-0 items-start gap-2">
                <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-[var(--app-primary)]" />
                <span className="min-w-0 break-words">{impact}</span>
              </li>
            ))}
          </ul>
        ) : null}

        <DesktopV3HandoffCopyableCodeBlocks handoff={handoff} />

        {recommendation ? (
          <div className="mt-3 border-l-2 border-[var(--app-primary)] pl-3" data-final-handoff-recommendation>
            <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Recommendation</div>
            {recommendation.reason ? (
              <p className="mt-1 text-sm leading-5 text-[var(--app-text-muted)]" data-final-handoff-recommendation-summary>
                {recommendation.reason}
              </p>
            ) : null}
          </div>
        ) : null}

        {handoffArtifacts.length > 0 ? (
          <div className="mt-3 flex flex-wrap gap-3" data-final-handoff-artifacts>
            {handoffArtifacts.map((artifact) => {
              const videoSourceHref = artifact.sourceRef
                ? `/v3/sessions/${encodeURIComponent(artifact.sessionId)}/video/sources/media?source_ref=${encodeURIComponent(artifact.sourceRef)}`
                : "";
              const href = videoSourceHref || artifactHref?.(artifact);
              if (!href) return null;
              const catalogedArtifact = artifactCatalog.some((entry) => desktopV3ArtifactCatalogEntryKey(entry) === desktopV3ArtifactCatalogEntryKey(artifact));
              const openArtifact = (event: ReactMouseEvent<HTMLAnchorElement>) => {
                if (videoSourceHref || !onArtifactNavigate || !catalogedArtifact || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
                event.preventDefault();
                onArtifactNavigate(artifact);
              };
              return (
                <a
                  key={`${artifact.sessionId}:${artifact.collectionId ?? ""}:${artifact.artifactId}`}
                  href={href}
                  onClick={openArtifact}
                  className="group w-full max-w-sm overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
                  aria-label={`Open ${artifact.label} artifact`}
                  data-final-handoff-artifact-link
                  data-artifact-id={artifact.artifactId}
                >
                  <DesktopV3ArtifactPreviewThumbnail artifact={artifact} className="rounded-none border-0 border-b border-[var(--app-border)]" />
                  <div className="flex min-w-0 items-center justify-between gap-3 p-3">
                    <div className="min-w-0">
                      <div className="truncate text-xs font-semibold text-[var(--app-text)]">{artifact.label}</div>
                      <div className="truncate text-[10px] text-[var(--app-text-subtle)]">{artifact.filename || artifact.mediaType}</div>
                    </div>
                    <ExternalLink size={14} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                  </div>
                </a>
              );
            })}
          </div>
        ) : null}

        {handoff.pullRequestUrl ? (
          <div className="mt-3" data-final-handoff-pull-request>
            <a
              href={handoff.pullRequestUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]"
            >
              <Github size={14} aria-hidden="true" />
              Open PR in new Tab
              <ExternalLink size={12} aria-hidden="true" />
            </a>
          </div>
        ) : null}

        {nextSteps.length > 0 ? (
          <div className="mt-3" data-final-handoff-suggestions>
            <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Suggested next steps</div>
            <div className="mt-1.5 flex flex-wrap gap-2">
              {nextSteps.map((suggestion, index) => {
                const available = suggestion.behavior === "prefill" ? Boolean(onPrefillPrompt) : Boolean(onSuggestedPrompt);
                return (
                  <button
                    key={`${item.message.id}:prompt:${index}`}
                    type="button"
                    className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-left text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50"
                    disabled={!available}
                    title={suggestion.prompt}
                    onClick={() => {
                      if (suggestion.behavior === "prefill") onPrefillPrompt?.(suggestion.prompt);
                      else selectDesktopV3SuggestedPrompt(suggestion.prompt, onSuggestedPrompt);
                    }}
                    data-final-handoff-prompt={suggestion.prompt}
                    data-final-handoff-prompt-behavior={suggestion.behavior}
                  >
                    {suggestion.label}
                  </button>
                );
              })}
            </div>
          </div>
        ) : null}

        <div className="mt-3 grid gap-1 border-t border-[var(--app-border)] pt-2 text-xs" data-final-handoff-evidence>
          {hasDetails ? (
            <details>
              <summary className="cursor-pointer py-1 font-medium text-[var(--app-text-muted)]">Details</summary>
              <div className="mt-1 border-l border-[var(--app-border)] pl-3">
                {details.report ? <ChatMarkdown content={details.report} /> : null}
                {details.result ? (
                  <div className="mt-2">
                    <div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Result</div>
                    <ChatMarkdown content={details.result} />
                  </div>
                ) : null}
              </div>
            </details>
          ) : null}
          {details.changedFiles.length > 0 ? (
            <details>
              <summary className="cursor-pointer py-1 font-medium text-[var(--app-text-muted)]">Files ({details.changedFiles.length})</summary>
              <ul className="mt-1 grid gap-1 border-l border-[var(--app-border)] pl-3 font-mono text-[11px] text-[var(--app-text-muted)]">
                {details.changedFiles.map((file, index) => <li key={`${item.message.id}:file:${index}`} className="break-all">{file}</li>)}
              </ul>
            </details>
          ) : null}
          {details.validation.length > 0 ? (
            <details>
              <summary className="cursor-pointer py-1 font-medium text-[var(--app-text-muted)]">Validation ({details.validation.length})</summary>
              <ul className="mt-1 grid gap-1 border-l border-[var(--app-border)] pl-3 text-[var(--app-text-muted)]">
                {details.validation.map((entry, index) => <li key={`${item.message.id}:validation:${index}`} className="break-words">{entry}</li>)}
              </ul>
            </details>
          ) : null}
        </div>
      </section>
    </div>
  );
}

function DesktopV3PlanBlockedHandoff({
  item,
  onSuggestedPrompt,
}: {
  item: Extract<DesktopV3RenderItem, { type: "plan-blocked-handoff" }>;
  onSuggestedPrompt?: (prompt: string) => void | Promise<void>;
}) {
  const title = handoffTitleDetail(item.headline, ["Checkpoint blocked", "Blocked checkpoint"]);
  const handoff = item.finalHandoff;
  const details = handoff?.details;
  const hasDetails = Boolean(details?.report || details?.result || details?.changedFiles.length || details?.validation.length);
  return (
    <div
      className="flex w-full min-w-0 justify-start py-1"
      data-testid="desktop-v3-plan-blocked-handoff"
    >
      <section
        aria-label="Blocked checkpoint handoff"
        className="w-full min-w-0 rounded-xl border border-[color-mix(in_srgb,var(--app-warning)_45%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-warning-bg)_55%,var(--app-surface-subtle))] px-4 py-3 text-sm leading-6 text-[var(--app-text)]"
        data-checkpoint-status="blocked"
      >
        <div className="flex min-w-0 items-start gap-3">
          <span className="mt-0.5 grid size-7 shrink-0 place-items-center rounded-lg bg-[var(--app-warning-bg)] text-[var(--app-warning)]">
            <CircleAlert size={14} aria-hidden="true" />
          </span>
          <div className="min-w-0 flex-1">
            <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-warning)]">
              Blocked checkpoint
            </div>
            {title ? <h3 className="mt-1 break-words text-base font-semibold text-[var(--app-text)]">{title}</h3> : null}
            {handoff ? (
              <>
                <div className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]" data-blocked-handoff-overview>
                  <ChatMarkdown content={handoff.overview} />
                </div>
                {handoff.impactBullets.length > 0 ? (
                  <ul className="mt-2 grid gap-1.5 text-sm leading-5 text-[var(--app-text-muted)]" data-blocked-handoff-impact>
                    {handoff.impactBullets.map((impact, index) => (
                      <li key={`${item.message.id}:blocked-impact:${index}`} className="flex min-w-0 items-start gap-2">
                        <span aria-hidden="true" className="mt-2 size-1 shrink-0 rounded-full bg-[var(--app-warning)]" />
                        <span className="min-w-0 break-words">{impact}</span>
                      </li>
                    ))}
                  </ul>
                ) : null}
                <DesktopV3HandoffCopyableCodeBlocks handoff={handoff} tone="warning" />
                {handoff.suggestedPrompts.length > 0 ? (
                  <div className="mt-3" data-blocked-handoff-suggestions>
                    <div className="text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">Next steps</div>
                    <div className="mt-1.5 flex flex-wrap gap-2">
                      {handoff.suggestedPrompts.map((suggestion, index) => (
                        <button
                          key={`${item.message.id}:blocked-prompt:${index}`}
                          type="button"
                          className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1.5 text-left text-xs font-medium text-[var(--app-text)] transition hover:border-[var(--app-border-active)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-warning)] disabled:cursor-not-allowed disabled:opacity-50"
                          disabled={!onSuggestedPrompt}
                          title={suggestion.prompt}
                          onClick={() => selectDesktopV3SuggestedPrompt(suggestion.prompt, onSuggestedPrompt)}
                          data-blocked-handoff-prompt={suggestion.prompt}
                        >
                          {suggestion.label}
                        </button>
                      ))}
                    </div>
                  </div>
                ) : null}
                {hasDetails ? (
                  <details className="mt-3 border-t border-[var(--app-border)] pt-2" data-blocked-handoff-evidence>
                    <summary className="cursor-pointer py-1 text-xs font-medium text-[var(--app-text-muted)]">Details</summary>
                    <div className="mt-1 border-l border-[var(--app-border)] pl-3 text-xs text-[var(--app-text-muted)]">
                      {details?.report ? <ChatMarkdown content={details.report} /> : null}
                      {details?.result ? <div className="mt-2"><div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Result</div><ChatMarkdown content={details.result} /></div> : null}
                      {details?.changedFiles.length ? <div className="mt-2">Files: {details.changedFiles.join(", ")}</div> : null}
                      {details?.validation.length ? <div className="mt-2"><div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Validation</div><ul className="grid gap-1">{details.validation.map((entry, index) => <li key={`${item.message.id}:blocked-validation:${index}`}>{entry}</li>)}</ul></div> : null}
                    </div>
                  </details>
                ) : null}
              </>
            ) : (
              <DesktopV3PlanHandoffContent item={item} />
            )}
          </div>
        </div>
      </section>
    </div>
  );
}

function handoffTitleDetail(headline: string, labels: string[]): string {
  const normalizedHeadline = headline.trim().toLowerCase();
  return labels.some((label) => label.toLowerCase() === normalizedHeadline)
    ? ""
    : headline.trim();
}

function DesktopV3PlanHandoffContent({
  item,
}: {
  item: Extract<
    DesktopV3RenderItem,
    { type: "plan-checkpoint-handoff" | "plan-blocked-handoff" }
  >;
}) {
  return (
    <>
      {item.summary ? (
        <section
          aria-label="At a glance"
          className="mt-3 w-full min-w-0 rounded-lg border border-[var(--app-border)] bg-[color-mix(in_srgb,var(--app-surface)_70%,transparent)] px-3 py-2.5"
        >
          <div className="mb-1 text-[10px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text-subtle)]">
            At a glance
          </div>
          <ChatMarkdown content={item.summary} />
        </section>
      ) : null}
      {item.body ? (
        <div className={item.summary || item.headline ? "mt-2 text-[var(--app-text-muted)]" : "text-[var(--app-text-muted)]"}>
          <ChatMarkdown content={item.body} />
        </div>
      ) : null}
    </>
  );
}

function DesktopV3PlanHandoff({
  item,
  icon,
  testId,
}: {
  item: Extract<
    DesktopV3RenderItem,
    {
      type:
        | "plan-checkpoint-handoff"
        | "plan-final-handoff"
        | "plan-blocked-handoff";
    }
  >;
  icon: ReactNode;
  testId: string;
}) {
  return (
    <div className="flex w-full min-w-0 justify-start py-1" data-testid={testId}>
      <div className="w-full min-w-0 text-sm leading-6 text-[var(--app-text)] opacity-90">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          {icon}
          {item.headline}
        </div>
        {item.summary ? (
          <section
            aria-label="At a glance"
            className="mb-3 w-full min-w-0 rounded-xl border border-[var(--app-border-active)] bg-[var(--app-surface-subtle)] px-3 py-2.5"
            data-testid={`${testId}-summary`}
          >
            <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-primary)]">
              At a glance
            </div>
            <ChatMarkdown content={item.summary} />
          </section>
        ) : null}
        {item.body ? <ChatMarkdown content={item.body} /> : null}
      </div>
    </div>
  );
}

function DesktopV3CommittedMessage({
  message,
  thinkingTagsEnabled,
  taskChildActions,
  artifactCatalog,
  artifactHref,
  onArtifactNavigate,
  onArtifactSelections,
}: {
  message: MessageSnapshot;
  thinkingTagsEnabled: boolean;
  taskChildActions?: TaskChildCardActions;
  artifactCatalog?: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
  onArtifactSelections?: (selections: DesktopV3ArtifactMessageSelection[]) => void;
}) {
  const role = message.role || "message";
  const toolMessage = message.toolMessage ?? null;
  if (toolMessage || role === "tool") {
    return (
      <DesktopV3ToolMessage
        content={message.content}
        toolMessage={toolMessage}
        thinkingTagsEnabled={thinkingTagsEnabled}
        taskChildActions={taskChildActions}
        artifactCatalog={artifactCatalog}
        artifactHref={artifactHref}
        onArtifactNavigate={onArtifactNavigate}
        onArtifactSelections={onArtifactSelections}
      />
    );
  }
  if (role === "user") {
    return <DesktopV3UserMessage content={message.content} media={message.media} artifactSelections={message.artifact_selections} />;
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
  media,
  artifactSelections,
  pendingLabel,
}: {
  content: string;
  media?: DesktopV3MediaReference[];
  artifactSelections?: DesktopV3ArtifactSelectionReference[];
  pendingLabel?: string;
}) {
  return (
    <div className="flex justify-end">
      <div className="max-w-[70%] rounded-xl bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)] shadow-sm">
        {content ? <div className="whitespace-pre-wrap break-words">{content}</div> : null}
        {media?.length ? <div className="mt-2 flex flex-wrap gap-1.5">{media.map((item, index) => <span key={`${item.asset_id}:${index}`} className="rounded-md border border-white/25 px-2 py-1 text-xs">{item.file_type?.toUpperCase() || item.mime_type} · {Math.ceil(item.size / 1024)} KB</span>)}</div> : null}
        {artifactSelections?.length ? <div className="mt-2 flex flex-wrap gap-1.5" data-testid="desktop-user-message-artifact-selections">{artifactSelections.map((selection) => <span key={`${selection.session_id}:${selection.collection_id}:${selection.variant_id}`} className="inline-flex min-w-0 max-w-full items-center gap-1.5 rounded-md border border-white/30 bg-white/10 px-2 py-1 text-xs"><GalleryHorizontal size={12} className="shrink-0" aria-hidden="true" /><span className="max-w-52 truncate" title={selection.description || selection.label}>{selection.label || 'Designer iteration'}</span>{selection.action === 'use' ? <span className="text-[9px] font-semibold uppercase tracking-wide opacity-75">Use design</span> : null}</span>)}</div> : null}
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
      media={message.media}
      artifactSelections={message.artifactSelections}
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
  taskChildActions,
  artifactCatalog,
  artifactHref,
  onArtifactNavigate,
  onArtifactSelections,
}: {
  content: string;
  toolMessage: StructuredToolMessage | null;
  thinkingTagsEnabled?: boolean;
  taskChildActions?: TaskChildCardActions;
  artifactCatalog?: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
  onArtifactSelections?: (selections: DesktopV3ArtifactMessageSelection[]) => void;
}) {
  const toolName = toolMessage?.tool.trim().toLowerCase();
  return (
    <div
      className={cn(
        "flex w-full min-w-0 justify-start",
        toolName === "bash" && "translate-x-[5px]",
      )}
    >
      <div className="w-full min-w-0 max-w-full">
        <ChatMarkdown
          content={content}
          toolMessage={toolMessage ?? undefined}
          thinkingTagsEnabled={thinkingTagsEnabled}
          taskChildActions={taskChildActions}
          artifactCatalog={artifactCatalog}
          artifactHref={artifactHref}
          onArtifactNavigate={onArtifactNavigate}
          onArtifactSelections={onArtifactSelections}
        />
      </div>
    </div>
  );
}

function DesktopV3ReasoningMessage({
  item,
  thinkingTagsEnabled,
}: {
  item: Extract<DesktopV3RenderItem, { type: "live-reasoning" }>;
  thinkingTagsEnabled: boolean;
}) {
  const [timerNow, setTimerNow] = useState(() => Date.now());
  useEffect(() => {
    if (item.state !== "running") return;
    const timer = window.setInterval(() => setTimerNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [item.state]);
  const body = reasoningBody(item.text, item.summary, thinkingTagsEnabled);
  const label = item.state === "error" ? "Thinking failed" : "Thinking";
  const elapsed = reasoningElapsedLabel(
    item.startedAt,
    item.state === "running" ? null : item.completedAt,
    timerNow,
  );
  const StateIcon = item.state === "error" ? XCircle : CheckCircle2;
  return (
    <div className="flex justify-start">
      <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)] opacity-80">
        <div className="mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">
          {item.state === "running" ? null : (
            <StateIcon
              size={12}
              className={
                item.state === "error"
                  ? "text-[var(--app-danger)]"
                  : "text-[var(--app-text-subtle)]"
              }
            />
          )}
          <span
            className={cn(
              item.state === "running"
                && "motion-safe:animate-[pulse_3s_ease-in-out_infinite] motion-reduce:animate-none",
            )}
          >
            {label}
          </span>
          {elapsed ? <span className="tabular-nums">· {elapsed}</span> : null}
        </div>
        {body ? <ChatMarkdown content={body} /> : null}
      </div>
    </div>
  );
}

function structuredLiveToolMessage(
  tool: LiveRunOverlay["toolCallsByCallId"][string],
): StructuredToolMessage | null {
  const providerReady = Boolean(
    tool.toolInstanceId?.startsWith("provider-tool:")
      && !tool.outputText?.trim()
      && !tool.errorText?.trim(),
  );
  const state: ToolMessageState =
    tool.status === "failed" || tool.status === "error"
      ? "error"
      : providerReady
        ? "running"
        : tool.status === "completed" ||
            tool.status === "done" ||
            tool.status === "cancelled" ||
            tool.status === "canceled"
          ? "done"
          : "running";
  const output = tool.outputText ?? "";
  const error = tool.errorText?.trim() || (state === "error" ? output : "");
  const parsed = buildStructuredToolMessage({
    pathId: "run.v3.provider-tool-result.v1",
    tool: tool.toolName || "tool",
    callId: tool.callId,
    toolInstanceId: tool.toolInstanceId,
    argumentsText: tool.argumentsText ?? "",
    outputText: output,
    error,
    durationMs: tool.durationMs,
    state,
    lifecycleStatus: tool.status,
    taskStream: tool.taskStream,
  });
  if (parsed && tool.timelineSeq) parsed.timelineSeq = tool.timelineSeq;
  return parsed;
}

function DesktopV3LiveToolCall({
  tool,
  taskChildActions,
  artifactCatalog,
  artifactHref,
  onArtifactNavigate,
  onArtifactSelections,
}: {
  tool: LiveRunOverlay["toolCallsByCallId"][string];
  taskChildActions?: TaskChildCardActions;
  artifactCatalog?: DesktopV3ArtifactCatalogEntry[];
  artifactHref?: (artifact: DesktopV3ArtifactCatalogEntry) => string;
  onArtifactNavigate?: (artifact: DesktopV3ArtifactCatalogEntry) => void;
  onArtifactSelections?: (selections: DesktopV3ArtifactMessageSelection[]) => void;
}) {
  return (
    <DesktopV3ToolMessage
      content=""
      toolMessage={structuredLiveToolMessage(tool)}
      taskChildActions={taskChildActions}
      artifactCatalog={artifactCatalog}
      artifactHref={artifactHref}
      onArtifactNavigate={onArtifactNavigate}
      onArtifactSelections={onArtifactSelections}
    />
  );
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
    toolMessage: message.toolMessage ?? null,
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
