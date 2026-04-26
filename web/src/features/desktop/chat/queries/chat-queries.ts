import {
  requestJson,
  apiFetch,
  readErrorMessage,
  ensureDesktopSession,
} from "../../../../app/api";
import type {
  DesktopPermissionRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from "../../types/realtime";
import type {
  AgentStateRecord,
  ChatMessageRecord,
  ModelOptionRecord,
  ProviderDefaultsPreviewRecord,
  ResolvedSessionPreference,
  DesktopSessionPlanRecord,
} from "../types/chat";
import {
  applyDesktopChatRouteToSession,
  loadDesktopChatRouteForSession,
  saveDesktopChatRouteForSession,
  type DesktopChatRoute,
  withDesktopChatRoute,
} from "../services/chat-routing";
import {
  canonicalSessionWorkspaceName,
  canonicalSessionWorkspacePath,
} from "../../services/session-workspace";
import {
  modelAllowedByProviderPreset,
  sortModelOptions,
  supportsCodex1MMode,
} from "../services/model-options";
import { parseStructuredToolMessage } from "../services/tool-message";

interface SessionWire {
  id?: string;
  title?: string;
  workspace_path?: string;
  workspace_name?: string;
  mode?: string;
  metadata?: Record<string, unknown>;
  message_count?: number;
  updated_at?: number;
  created_at?: number;
  worktree_enabled?: boolean;
  worktree_root_path?: string;
  worktree_base_branch?: string;
  worktree_branch?: string;
  git_branch?: string;
  git_has_git?: boolean;
  git_clean?: boolean;
  git_dirty_count?: number;
  git_staged_count?: number;
  git_modified_count?: number;
  git_untracked_count?: number;
  git_conflict_count?: number;
  git_ahead_count?: number;
  git_behind_count?: number;
  git_commit_detected?: boolean;
  git_commit_count?: number;
  git_committed_file_count?: number;
  git_committed_additions?: number;
  git_committed_deletions?: number;
  lifecycle?: {
    session_id?: string;
    run_id?: string;
    active?: boolean;
    phase?: string;
    started_at?: number;
    ended_at?: number;
    updated_at?: number;
    generation?: number;
    stop_reason?: string;
    error?: string;
    owner_transport?: string;
  } | null;
}

interface ResolvePermissionResponseWire {
  permission?: {
    id?: string;
    session_id?: string;
    run_id?: string;
    call_id?: string;
    tool_name?: string;
    tool_arguments?: string;
    approved_arguments?: string;
    status?: string;
    decision?: string;
    reason?: string;
    requirement?: string;
    mode?: string;
    created_at?: number;
    updated_at?: number;
    resolved_at?: number;
    permission_requested_at?: number;
  };
  saved_rule?: {
    id?: string;
    kind?: string;
    decision?: string;
    tool?: string;
    pattern?: string;
    created_at?: number;
    updated_at?: number;
  };
}

interface PendingPermissionsResponseWire {
  permissions?: ResolvePermissionResponseWire["permission"][];
}

interface MessagesResponseWire {
  messages?: Array<{
    id?: string;
    session_id?: string;
    global_seq?: number;
    role?: string;
    content?: string;
    created_at?: number;
    metadata?: Record<string, unknown>;
  }>;
}

interface SessionPreferenceWire {
  preference?: {
    provider?: string;
    model?: string;
    thinking?: string;
    service_tier?: string;
    context_mode?: string;
    updated_at?: number;
  };
  context_window?: number;
  max_output_tokens?: number;
}

interface DraftModelWire {
  preference?: {
    provider?: string;
    model?: string;
    thinking?: string;
    service_tier?: string;
    context_mode?: string;
    updated_at?: number;
  };
  context_window?: number;
  max_output_tokens?: number;
}

interface SessionUsageSummaryWire {
  session_id?: string;
  provider?: string;
  model?: string;
  source?: string;
  context_window?: number;
  total_tokens?: number;
  remaining_tokens?: number;
  updated_at?: number;
}

interface SessionUsageResponseWire {
  usage_summary?: SessionUsageSummaryWire | null;
}

interface SessionPlanWire {
  id?: string;
  title?: string;
  plan?: string;
  status?: string;
  approval_state?: string;
  updated_at?: number;
}

interface ActiveSessionPlanResponseWire {
  has_active?: boolean;
  active_plan?: SessionPlanWire | null;
}

interface SaveSessionPlanResponseWire {
  plan?: SessionPlanWire | null;
}

type ProviderDefaultsPreviewWire = {
  provider?: string;
  primary_agent?: string;
  primary_model?: string;
  primary_thinking?: string;
  utility_provider?: string;
  utility_model?: string;
  utility_thinking?: string;
  utility_agents?: string[];
  affected_agents?: string[];
  out_of_sync_agents?: string[];
  inheriting_agents?: string[];
  stale_inherited_agents?: string[];
  custom_utility_agents?: string[];
  utility_baseline_agents?: string[];
  overwrite_explicit?: boolean;
};

type AgentStateWire = {
  state?: {
    profiles?: Array<{
      name?: string;
      mode?: string;
      description?: string;
      provider?: string;
      model?: string;
      thinking?: string;
      prompt?: string;
      execution_setting?: string;
      exit_plan_mode_enabled?: boolean;
      tool_scope?: {
        preset?: string;
        allow_tools?: string[];
        deny_tools?: string[];
        bash_prefixes?: string[];
        inherit_policy?: boolean;
      } | null;
      tool_contract?: {
        preset?: string;
        inherit_policy?: boolean;
        tools?: Record<
          string,
          {
            enabled?: boolean;
            bash_prefixes?: string[];
          }
        >;
      } | null;
      enabled?: boolean;
      protected?: boolean;
      updated_at?: number;
    }>;
    active_primary?: string;
    active_subagent?: Record<string, string>;
    version?: number;
  };
  provider_defaults_preview?: ProviderDefaultsPreviewWire | null;
};

type RestoreAgentDefaultsWire = {
  ok?: boolean;
  provider_defaults_preview?: ProviderDefaultsPreviewWire | null;
  profiles?: Array<{
    name?: string;
    mode?: string;
    description?: string;
    provider?: string;
    model?: string;
    thinking?: string;
    prompt?: string;
    execution_setting?: string;
    exit_plan_mode_enabled?: boolean;
    enabled?: boolean;
    protected?: boolean;
    updated_at?: number;
  }>;
  active_primary?: string;
  active_subagent?: Record<string, string>;
  version?: number;
};

interface ProviderStatusWire {
  id?: string;
  ready?: boolean;
  runnable?: boolean;
}

interface ProvidersResponseWire {
  providers?: ProviderStatusWire[];
}

interface FavoriteRecordWire {
  provider?: string;
  model?: string;
  label?: string;
  thinking?: string;
}

interface FavoritesResponseWire {
  records?: FavoriteRecordWire[];
}

interface ModelCatalogRecordWire {
  provider?: string;
  model?: string;
  context_window?: number;
}

interface CatalogResponseWire {
  records?: ModelCatalogRecordWire[];
}

function emptyLiveState(): DesktopSessionRecord["live"] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: "idle",
    step: 0,
    toolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: "",
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: "",
    retainedToolState: null,
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: "",
    reasoningSummary: "",
    reasoningText: "",
    reasoningState: "idle",
    reasoningSegment: 0,
    reasoningStartedAt: null,
    awaitingAck: false,
  };
}

function mapSessionUsageSummary(
  summary: SessionUsageSummaryWire | null | undefined,
): DesktopSessionUsageRecord | null {
  if (!summary || typeof summary !== "object") {
    return null;
  }
  const sessionId = String(summary.session_id ?? "").trim();
  const contextWindow =
    typeof summary.context_window === "number" ? summary.context_window : 0;
  const totalTokens =
    typeof summary.total_tokens === "number" ? summary.total_tokens : 0;
  const remainingTokens =
    typeof summary.remaining_tokens === "number" ? summary.remaining_tokens : 0;
  const updatedAt =
    typeof summary.updated_at === "number" ? summary.updated_at : 0;
  if (
    !sessionId &&
    contextWindow <= 0 &&
    totalTokens <= 0 &&
    remainingTokens <= 0 &&
    updatedAt <= 0
  ) {
    return null;
  }
  return {
    sessionId,
    provider: String(summary.provider ?? "").trim(),
    model: String(summary.model ?? "").trim(),
    source: String(summary.source ?? "").trim(),
    contextWindow,
    totalTokens,
    remainingTokens,
    updatedAt,
  };
}

function mapSessionPlan(
  plan: SessionPlanWire | null | undefined,
): DesktopSessionPlanRecord {
  return {
    id: String(plan?.id ?? "").trim(),
    title: String(plan?.title ?? "").trim(),
    plan: String(plan?.plan ?? ""),
    status: String(plan?.status ?? "").trim(),
    approvalState: String(plan?.approval_state ?? "").trim(),
    updatedAt: typeof plan?.updated_at === "number" ? plan.updated_at : 0,
  };
}

function mapSession(session: SessionWire): DesktopSessionRecord {
  const lifecycle =
    session.lifecycle && typeof session.lifecycle === "object"
      ? {
          sessionId: String(
            session.lifecycle.session_id ?? session.id ?? "",
          ).trim(),
          runId: String(session.lifecycle.run_id ?? "").trim() || null,
          active: Boolean(session.lifecycle.active),
          phase: String(session.lifecycle.phase ?? "").trim(),
          startedAt:
            typeof session.lifecycle.started_at === "number"
              ? session.lifecycle.started_at
              : 0,
          endedAt:
            typeof session.lifecycle.ended_at === "number"
              ? session.lifecycle.ended_at
              : 0,
          updatedAt:
            typeof session.lifecycle.updated_at === "number"
              ? session.lifecycle.updated_at
              : 0,
          generation:
            typeof session.lifecycle.generation === "number"
              ? session.lifecycle.generation
              : 0,
          stopReason:
            String(session.lifecycle.stop_reason ?? "").trim() || null,
          error: String(session.lifecycle.error ?? "").trim() || null,
          ownerTransport:
            String(session.lifecycle.owner_transport ?? "").trim() || null,
        }
      : null;
  const normalizedLifecyclePhase = lifecycle?.phase.trim().toLowerCase() ?? "";
  const liveStatus = lifecycle?.active
    ? ((["starting", "running", "blocked"].includes(normalizedLifecyclePhase)
        ? normalizedLifecyclePhase
        : "running") as DesktopSessionRecord["live"]["status"])
    : normalizedLifecyclePhase === "errored"
      ? "error"
      : "idle";
  const metadata =
    session.metadata && typeof session.metadata === "object"
      ? (session.metadata as Record<string, unknown>)
      : undefined;
  const workspacePath = String(session.workspace_path ?? "").trim();
  const hostedHostWorkspacePath =
    typeof metadata?.swarm_routed_host_workspace_path === "string"
      ? metadata.swarm_routed_host_workspace_path.trim()
      : "";
  const hostedRuntimeWorkspacePath =
    typeof metadata?.swarm_routed_runtime_workspace_path === "string"
      ? metadata.swarm_routed_runtime_workspace_path.trim()
      : "";
  const worktreeEnabled = Boolean(session.worktree_enabled);
  const worktreeRootPath = String(session.worktree_root_path ?? "").trim();
  const canonicalWorkspacePath = canonicalSessionWorkspacePath({
    workspacePath,
    hostedHostWorkspacePath,
    worktreeEnabled,
    worktreeRootPath,
  });
  return {
    id: String(session.id ?? "").trim(),
    title: String(session.title ?? "").trim(),
    workspacePath: canonicalWorkspacePath,
    workspaceName: canonicalSessionWorkspaceName(
      String(session.workspace_name ?? ""),
      workspacePath,
      canonicalWorkspacePath,
    ),
    mode: String(session.mode ?? "auto").trim() || "auto",
    metadata,
    messageCount:
      typeof session.message_count === "number" ? session.message_count : 0,
    updatedAt: typeof session.updated_at === "number" ? session.updated_at : 0,
    createdAt: typeof session.created_at === "number" ? session.created_at : 0,
    permissionsHydrated: true,
    runtimeWorkspacePath: hostedRuntimeWorkspacePath || workspacePath,
    worktreeEnabled,
    worktreeRootPath,
    worktreeBaseBranch: String(session.worktree_base_branch ?? "").trim(),
    worktreeBranch: String(session.worktree_branch ?? "").trim(),
    gitBranch: String(session.git_branch ?? "").trim(),
    gitHasGit: Boolean(session.git_has_git),
    gitClean: Boolean(session.git_clean),
    gitDirtyCount:
      typeof session.git_dirty_count === "number" ? session.git_dirty_count : 0,
    gitStagedCount:
      typeof session.git_staged_count === "number"
        ? session.git_staged_count
        : 0,
    gitModifiedCount:
      typeof session.git_modified_count === "number"
        ? session.git_modified_count
        : 0,
    gitUntrackedCount:
      typeof session.git_untracked_count === "number"
        ? session.git_untracked_count
        : 0,
    gitConflictCount:
      typeof session.git_conflict_count === "number"
        ? session.git_conflict_count
        : 0,
    gitAheadCount:
      typeof session.git_ahead_count === "number" ? session.git_ahead_count : 0,
    gitBehindCount:
      typeof session.git_behind_count === "number"
        ? session.git_behind_count
        : 0,
    gitCommitDetected: Boolean(session.git_commit_detected),
    gitCommitCount:
      typeof session.git_commit_count === "number"
        ? session.git_commit_count
        : 0,
    gitCommittedFileCount:
      typeof session.git_committed_file_count === "number"
        ? session.git_committed_file_count
        : 0,
    gitCommittedAdditions:
      typeof session.git_committed_additions === "number"
        ? session.git_committed_additions
        : 0,
    gitCommittedDeletions:
      typeof session.git_committed_deletions === "number"
        ? session.git_committed_deletions
        : 0,
    lifecycle,
    live: {
      ...emptyLiveState(),
      runId: lifecycle?.active ? lifecycle.runId : null,
      startedAt:
        lifecycle?.active && lifecycle.startedAt > 0
          ? lifecycle.startedAt
          : null,
      status: liveStatus,
      lastEventAt: lifecycle?.updatedAt ? lifecycle.updatedAt : null,
      summary: lifecycle?.active
        ? null
        : normalizedLifecyclePhase === "errored"
          ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
          : (lifecycle?.stopReason ?? null),
      error:
        normalizedLifecyclePhase === "errored"
          ? (lifecycle?.error ?? lifecycle?.stopReason ?? null)
          : null,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  };
}

export async function fetchSession(
  sessionId: string,
): Promise<DesktopSessionRecord | null> {
  const normalizedSessionId = sessionId.trim();
  if (!normalizedSessionId) {
    return null;
  }

  const route = loadDesktopChatRouteForSession(normalizedSessionId);
  const response = await requestJson<{ session?: SessionWire }>(
    `/v1/sessions/${encodeURIComponent(normalizedSessionId)}`,
  );
  const mapped = applyDesktopChatRouteToSession(
    mapSession(response.session ?? {}),
    route,
  );
  mapped.permissionsHydrated = false;
  return mapped.id ? mapped : null;
}

function mapResolvedPermission(
  permission: ResolvePermissionResponseWire["permission"],
  savedRule?: ResolvePermissionResponseWire["saved_rule"],
): DesktopPermissionRecord {
  return {
    id: String(permission?.id ?? "").trim(),
    sessionId: String(permission?.session_id ?? "").trim(),
    runId: String(permission?.run_id ?? "").trim(),
    callId: String(permission?.call_id ?? "").trim(),
    toolName: String(permission?.tool_name ?? "").trim(),
    toolArguments: String(permission?.tool_arguments ?? "").trim(),
    approvedArguments:
      String(
        (permission as { approved_arguments?: unknown } | undefined)
          ?.approved_arguments ?? "",
      ).trim() || undefined,
    savedRule: savedRule
      ? {
          id: String(savedRule.id ?? "").trim(),
          kind: String(savedRule.kind ?? "").trim(),
          decision: String(savedRule.decision ?? "").trim(),
          tool:
            typeof savedRule.tool === "string"
              ? savedRule.tool.trim()
              : undefined,
          pattern:
            typeof savedRule.pattern === "string"
              ? savedRule.pattern.trim()
              : undefined,
          createdAt:
            typeof savedRule.created_at === "number"
              ? savedRule.created_at
              : undefined,
          updatedAt:
            typeof savedRule.updated_at === "number"
              ? savedRule.updated_at
              : undefined,
        }
      : undefined,
    status: String(permission?.status ?? "").trim(),
    decision: String(permission?.decision ?? "").trim(),
    reason: String(permission?.reason ?? "").trim(),
    requirement: String(permission?.requirement ?? "").trim(),
    mode: String(permission?.mode ?? "").trim(),
    createdAt:
      typeof permission?.created_at === "number" ? permission.created_at : 0,
    updatedAt:
      typeof permission?.updated_at === "number" ? permission.updated_at : 0,
    resolvedAt:
      typeof permission?.resolved_at === "number" ? permission.resolved_at : 0,
    permissionRequestedAt:
      typeof permission?.permission_requested_at === "number"
        ? permission.permission_requested_at
        : 0,
  };
}

export async function fetchSessionMessages(
  sessionId: string,
  signal?: AbortSignal,
  afterSeq = 0,
): Promise<ChatMessageRecord[]> {
  const search = new URLSearchParams({ limit: "100" });
  if (afterSeq > 0) {
    search.set("after_seq", String(afterSeq));
  }
  const response = await requestJson<MessagesResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/messages?${search.toString()}`,
    { signal },
  );
  return Array.isArray(response.messages)
    ? response.messages.map((message) => {
        const content = String(message.content ?? "");
        return {
          id: String(message.id ?? "").trim(),
          sessionId: String(message.session_id ?? "").trim(),
          globalSeq:
            typeof message.global_seq === "number" ? message.global_seq : 0,
          role: String(message.role ?? "").trim(),
          content,
          createdAt:
            typeof message.created_at === "number" ? message.created_at : 0,
          metadata: message.metadata,
          toolMessage: parseStructuredToolMessage(content),
        };
      })
    : [];
}

export async function fetchSessionPreference(
  sessionId: string,
  signal?: AbortSignal,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<SessionPreferenceWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/preference`,
    { signal },
  );
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : 0,
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function updateSessionMode(
  sessionId: string,
  mode: string,
): Promise<string> {
  const response = await requestJson<{ mode?: string }>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/mode`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ mode }),
    },
  );
  return String(response.mode ?? "").trim() || "auto";
}

export async function updateSessionMetadata(
  sessionId: string,
  metadata: Record<string, unknown>,
): Promise<DesktopSessionRecord> {
  const route = loadDesktopChatRouteForSession(sessionId);
  const response = await requestJson<{ session?: SessionWire }>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/metadata`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        metadata,
      }),
    },
  );
  return applyDesktopChatRouteToSession(
    mapSession(response.session ?? {}),
    route,
  );
}

export async function updateSessionPreference(
  sessionId: string,
  input: Partial<ResolvedSessionPreference["preference"]>,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<SessionPreferenceWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/preference`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        provider: input.provider,
        model: input.model,
        thinking: input.thinking,
        service_tier: input.serviceTier,
        context_mode: input.contextMode,
      }),
    },
  );
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : Date.now(),
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function fetchDraftModelPreference(
  signal?: AbortSignal,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<DraftModelWire>("/v1/model", { signal });
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : 0,
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function updateDraftModelPreference(
  input: Partial<ResolvedSessionPreference["preference"]>,
): Promise<ResolvedSessionPreference> {
  const response = await requestJson<DraftModelWire>("/v1/model", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      provider: input.provider,
      model: input.model,
      thinking: input.thinking,
      service_tier: input.serviceTier,
      context_mode: input.contextMode,
    }),
  });
  return {
    preference: {
      provider: String(response.preference?.provider ?? "").trim(),
      model: String(response.preference?.model ?? "").trim(),
      thinking: String(response.preference?.thinking ?? "").trim(),
      serviceTier: String(response.preference?.service_tier ?? "").trim(),
      contextMode: String(response.preference?.context_mode ?? "").trim(),
      updatedAt:
        typeof response.preference?.updated_at === "number"
          ? response.preference.updated_at
          : Date.now(),
    },
    contextWindow:
      typeof response.context_window === "number" ? response.context_window : 0,
    maxOutputTokens:
      typeof response.max_output_tokens === "number"
        ? response.max_output_tokens
        : 0,
  };
}

export async function fetchAgentState(
  signal?: AbortSignal,
): Promise<AgentStateRecord> {
  const response = await requestJson<AgentStateWire>("/v2/agents?limit=200", {
    signal,
  });
  return {
    profiles: Array.isArray(response.state?.profiles)
      ? response.state.profiles.map((profile) => ({
          name: String(profile.name ?? "").trim(),
          mode: String(profile.mode ?? "").trim(),
          description: String(profile.description ?? "").trim(),
          provider: String(profile.provider ?? "").trim(),
          model: String(profile.model ?? "").trim(),
          thinking: String(profile.thinking ?? "").trim(),
          prompt: String(profile.prompt ?? ""),
          executionSetting: (() => {
            const raw = String(profile.execution_setting ?? "")
              .trim()
              .toLowerCase();
            return raw === "read" || raw === "readwrite" ? raw : "";
          })() as "read" | "readwrite" | "",
          exitPlanModeEnabled: Boolean(profile.exit_plan_mode_enabled),
          toolScope: (() => {
            if (
              profile.tool_contract &&
              typeof profile.tool_contract === "object"
            ) {
              const tools =
                profile.tool_contract.tools &&
                typeof profile.tool_contract.tools === "object"
                  ? profile.tool_contract.tools
                  : {};
              const allowTools: string[] = [];
              const denyTools: string[] = [];
              let bashPrefixes: string[] = [];
              for (const [name, config] of Object.entries(tools)) {
                if (config && typeof config === "object") {
                  if (Array.isArray(config.bash_prefixes) && name === "bash") {
                    bashPrefixes = config.bash_prefixes
                      .map((value) => String(value).trim())
                      .filter(Boolean);
                  }
                  if (config.enabled === true && name !== "bash") {
                    allowTools.push(name);
                  }
                  if (config.enabled === false) {
                    denyTools.push(name);
                  }
                }
              }
              return {
                preset: String(profile.tool_contract.preset ?? "").trim(),
                allowTools,
                denyTools,
                bashPrefixes,
                inheritPolicy: Boolean(profile.tool_contract.inherit_policy),
              };
            }
            if (profile.tool_scope && typeof profile.tool_scope === "object") {
              return {
                preset: String(profile.tool_scope.preset ?? "").trim(),
                allowTools: Array.isArray(profile.tool_scope.allow_tools)
                  ? profile.tool_scope.allow_tools
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                denyTools: Array.isArray(profile.tool_scope.deny_tools)
                  ? profile.tool_scope.deny_tools
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                bashPrefixes: Array.isArray(profile.tool_scope.bash_prefixes)
                  ? profile.tool_scope.bash_prefixes
                      .map((value) => String(value).trim())
                      .filter(Boolean)
                  : [],
                inheritPolicy: Boolean(profile.tool_scope.inherit_policy),
              };
            }
            return null;
          })(),
          enabled: Boolean(profile.enabled),
          protected: Boolean((profile as { protected?: boolean }).protected),
          updatedAt:
            typeof profile.updated_at === "number" ? profile.updated_at : 0,
        }))
      : [],
    activePrimary: String(response.state?.active_primary ?? "").trim(),
    activeSubagent: response.state?.active_subagent ?? {},
    version:
      typeof response.state?.version === "number" ? response.state.version : 0,
    providerDefaultsPreview: mapProviderDefaultsPreview(
      response.provider_defaults_preview,
    ),
  };
}

function mapProviderDefaultsPreview(
  preview?: ProviderDefaultsPreviewWire | null,
): ProviderDefaultsPreviewRecord | null {
  if (!preview || typeof preview !== "object") {
    return null;
  }
  return {
    provider: String(preview.provider ?? "").trim(),
    primaryAgent: String(preview.primary_agent ?? "").trim(),
    primaryModel: String(preview.primary_model ?? "").trim(),
    primaryThinking: String(preview.primary_thinking ?? "").trim(),
    utilityProvider: String(preview.utility_provider ?? preview.provider ?? "").trim(),
    utilityModel: String(preview.utility_model ?? "").trim(),
    utilityThinking: String(preview.utility_thinking ?? "").trim(),
    utilityAgents: Array.isArray(preview.utility_agents)
      ? preview.utility_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    affectedAgents: Array.isArray(preview.affected_agents)
      ? preview.affected_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    outOfSyncAgents: Array.isArray(preview.out_of_sync_agents)
      ? preview.out_of_sync_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    inheritingAgents: Array.isArray(preview.inheriting_agents)
      ? preview.inheriting_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    staleInheritedAgents: Array.isArray(preview.stale_inherited_agents)
      ? preview.stale_inherited_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    customUtilityAgents: Array.isArray(preview.custom_utility_agents)
      ? preview.custom_utility_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    utilityBaselineAgents: Array.isArray(preview.utility_baseline_agents)
      ? preview.utility_baseline_agents
          .map((value) => String(value).trim())
          .filter(Boolean)
      : [],
    overwriteExplicit: Boolean(preview.overwrite_explicit),
  };
}

function mapAgentDefaultsState(
  response: RestoreAgentDefaultsWire,
): AgentStateRecord {
  const state = {
    profiles: response.profiles,
    active_primary: response.active_primary,
    active_subagent: response.active_subagent,
    version: response.version,
  };
  return {
    profiles: Array.isArray(state?.profiles)
      ? state.profiles.map((profile) => ({
          name: String(profile.name ?? "").trim(),
          mode: String(profile.mode ?? "").trim(),
          description: String(profile.description ?? "").trim(),
          provider: String(profile.provider ?? "").trim(),
          model: String(profile.model ?? "").trim(),
          thinking: String(profile.thinking ?? "").trim(),
          prompt: String(profile.prompt ?? ""),
          executionSetting: (() => {
            const raw = String(profile.execution_setting ?? "")
              .trim()
              .toLowerCase();
            return raw === "read" || raw === "readwrite" ? raw : "";
          })() as "read" | "readwrite" | "",
          exitPlanModeEnabled: Boolean(profile.exit_plan_mode_enabled),
          toolScope: null,
          enabled: Boolean(profile.enabled),
          protected: Boolean((profile as { protected?: boolean }).protected),
          updatedAt:
            typeof profile.updated_at === "number" ? profile.updated_at : 0,
        }))
      : [],
    activePrimary: String(state?.active_primary ?? "").trim(),
    activeSubagent: state?.active_subagent ?? {},
    version: typeof state?.version === "number" ? state.version : 0,
    providerDefaultsPreview: mapProviderDefaultsPreview(
      response.provider_defaults_preview,
    ),
  };
}

export async function restoreAgentDefaults(input?: {
  utilityProvider?: string;
  utilityModel?: string;
  utilityThinking?: string;
  overwriteExplicit?: boolean;
}): Promise<AgentStateRecord> {
  const body: Record<string, string | boolean> = {};
  if (input?.utilityProvider !== undefined) {
    body.utility_provider = input.utilityProvider;
  }
  if (input?.utilityModel !== undefined) {
    body.utility_model = input.utilityModel;
  }
  if (input?.utilityThinking !== undefined) {
    body.utility_thinking = input.utilityThinking;
  }
  if (input?.overwriteExplicit !== undefined) {
    body.overwrite_explicit = input.overwriteExplicit;
  }
  const response = await requestJson<RestoreAgentDefaultsWire>(
    "/v2/agents/defaults/restore",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    },
  );
  return mapAgentDefaultsState(response);
}

export async function resetAgentDefaults(): Promise<AgentStateRecord> {
  const response = await requestJson<RestoreAgentDefaultsWire>(
    "/v2/agents/defaults/reset",
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({}),
    },
  );
  return mapAgentDefaultsState(response);
}

export async function activatePrimaryAgent(name: string): Promise<void> {
  await requestJson("/v2/agents/active/primary", {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ name: name.trim() }),
  });
}

export async function createSession(input: {
  title?: string;
  workspacePath: string;
  workspaceName: string;
  mode: string;
  agentName?: string;
  metadata?: Record<string, unknown>;
  preference: ResolvedSessionPreference["preference"];
  route?: DesktopChatRoute | null;
  worktreeMode?: string;
}): Promise<DesktopSessionRecord> {
  const response = await requestJson<{ session?: SessionWire }>(
    withDesktopChatRoute("/v1/sessions", input.route),
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title: input.title ?? "",
        workspace_path: input.workspacePath,
        host_workspace_path:
          input.route?.hostWorkspacePath ?? input.workspacePath,
        runtime_workspace_path:
          input.route?.runtimeWorkspacePath ?? input.workspacePath,
        workspace_name: input.workspaceName,
        mode: input.mode,
        agent_name: input.agentName?.trim() ?? "",
        metadata: input.metadata ?? undefined,
        worktree_mode: input.worktreeMode?.trim() || undefined,
        preference: {
          provider: input.preference.provider,
          model: input.preference.model,
          thinking: input.preference.thinking,
          service_tier: input.preference.serviceTier,
          context_mode: input.preference.contextMode,
        },
      }),
    },
  );
  const mapped = applyDesktopChatRouteToSession(
    mapSession(response.session ?? {}),
    input.route,
  );
  if (mapped.id) {
    saveDesktopChatRouteForSession(mapped.id, input.route);
  }
  return mapped;
}

export async function sendSessionMessage(
  sessionId: string,
  role: "user" | "assistant" | "system" | "tool" | "reasoning",
  content: string,
) {
  return requestJson(`/v1/sessions/${encodeURIComponent(sessionId)}/messages`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ role, content }),
  });
}

export interface DesktopBackgroundRunStartOptions {
  sessionId: string;
  prompt: string;
  agentName?: string;
  instructions?: string;
  compact?: boolean;
  background?: boolean;
  targetKind?: string;
  targetName?: string;
  toolScope?: {
    preset?: string;
    allow_tools?: string[];
    deny_tools?: string[];
    bash_prefixes?: string[];
    inherit_policy?: boolean;
  };
  executionContext?: {
    workspace_path?: string;
    cwd?: string;
    worktree_mode?: string;
    worktree_root_path?: string;
    worktree_branch?: string;
    worktree_base_branch?: string;
  };
}

export interface DesktopRunAccepted {
  ok?: boolean;
  session_id?: string;
  run_id?: string;
  status?: string;
  background?: boolean;
  target_kind?: string;
  target_name?: string;
  owner_transport?: string;
}

export async function startSessionRun(
  options: DesktopBackgroundRunStartOptions,
): Promise<DesktopRunAccepted> {
  const sessionId = options.sessionId.trim();
  if (!sessionId) {
    throw new Error("session id is required");
  }
  const prompt = options.prompt.trim();
  if (!prompt && !options.compact) {
    throw new Error("prompt is required");
  }

  return requestJson<DesktopRunAccepted>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/run/stream`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        type: "run.start",
        prompt,
        agent_name: options.agentName?.trim() ?? "",
        instructions: options.instructions?.trim() ?? "",
        compact: Boolean(options.compact),
        background: Boolean(options.background),
        target_kind: options.targetKind?.trim() ?? "",
        target_name: options.targetName?.trim() ?? "",
        tool_scope: options.toolScope,
        execution_context: options.executionContext,
      }),
    },
  );
}

export async function openRunStream(sessionId: string): Promise<WebSocket> {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  await ensureDesktopSession(true);
  const url = new URL(
    `/v1/sessions/${encodeURIComponent(sessionId)}/run/stream`,
    `${protocol}//${window.location.host}`,
  );
  return new WebSocket(url);
}

export async function stopSessionRun(
  sessionId: string,
  runId: string,
): Promise<void> {
  const response = await apiFetch(
    `/v1/sessions/${encodeURIComponent(sessionId)}/run/stream`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ type: "run.stop", run_id: runId }),
    },
  );
  if (!response.ok) {
    throw new Error(await readErrorMessage(response));
  }
}

export async function resolveSessionPermission(
  sessionId: string,
  permissionId: string,
  action:
    | "approve"
    | "deny"
    | "approve_always"
    | "always_allow"
    | "always_deny",
  reason: string,
  approvedArguments?: Record<string, unknown>,
): Promise<DesktopPermissionRecord> {
  const response = await requestJson<ResolvePermissionResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/permissions/${encodeURIComponent(permissionId)}/resolve`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        action,
        reason,
        approved_arguments: approvedArguments,
      }),
    },
  );
  return mapResolvedPermission(response.permission, response.saved_rule);
}

export async function fetchSessionUsageSummary(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopSessionUsageRecord | null> {
  const response = await requestJson<SessionUsageResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/usage`,
    { signal },
  );
  return mapSessionUsageSummary(response.usage_summary);
}

export async function fetchActiveSessionPlan(
  sessionId: string,
  signal?: AbortSignal,
): Promise<{ hasActive: boolean; plan: DesktopSessionPlanRecord }> {
  const response = await requestJson<ActiveSessionPlanResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/plans/active`,
    { signal },
  );
  return {
    hasActive: Boolean(response.has_active),
    plan: mapSessionPlan(response.active_plan),
  };
}

export async function saveSessionPlan(
  sessionId: string,
  input: {
    id?: string;
    title?: string;
    plan: string;
    status?: string;
    approvalState?: string;
  },
): Promise<DesktopSessionPlanRecord> {
  const response = await requestJson<SaveSessionPlanResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/plans`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        id: input.id?.trim() || undefined,
        plan_id: input.id?.trim() || undefined,
        title: input.title?.trim() || undefined,
        plan: input.plan,
        status: input.status?.trim() || undefined,
        approval_state: input.approvalState?.trim() || undefined,
      }),
    },
  );
  return mapSessionPlan(response.plan);
}

export async function fetchSessionPendingPermissions(
  sessionId: string,
  signal?: AbortSignal,
): Promise<DesktopPermissionRecord[]> {
  const response = await requestJson<PendingPermissionsResponseWire>(
    `/v1/sessions/${encodeURIComponent(sessionId)}/permissions?limit=200`,
    { signal },
  );
  return Array.isArray(response.permissions)
    ? response.permissions
        .map((permission) => mapResolvedPermission(permission))
        .filter(
          (permission) =>
            permission.id !== "" &&
            permission.sessionId !== "" &&
            permission.status === "pending",
        )
    : [];
}

function modelOptionKey(
  provider: string,
  model: string,
  contextMode = "",
): string {
  return `${provider}:${model}:${contextMode.trim().toLowerCase()}`;
}

export async function fetchModelOptions(
  signal?: AbortSignal,
): Promise<ModelOptionRecord[]> {
  const providersResponse = await requestJson<ProvidersResponseWire>(
    "/v1/providers",
    { signal },
  );
  const providers = Array.isArray(providersResponse.providers)
    ? providersResponse.providers
        .filter(
          (provider) => Boolean(provider.ready) && Boolean(provider.runnable),
        )
        .map((provider) => String(provider.id ?? "").trim())
        .filter(Boolean)
    : [];

  const [favoritesByProvider, catalogByProvider] = await Promise.all([
    Promise.all(
      providers.map(
        async (provider) =>
          [
            provider,
            await requestJson<FavoritesResponseWire>(
              `/v1/models/favorites?provider=${encodeURIComponent(provider)}&limit=200`,
              { signal },
            ),
          ] as const,
      ),
    ),
    Promise.all(
      providers.map(
        async (provider) =>
          [
            provider,
            await requestJson<CatalogResponseWire>(
              `/v1/model/catalog?provider=${encodeURIComponent(provider)}&limit=200`,
              { signal },
            ),
          ] as const,
      ),
    ),
  ]);

  const options = new Map<string, ModelOptionRecord>();

  for (const [provider, response] of favoritesByProvider) {
    for (const record of response.records ?? []) {
      const model = String(record.model ?? "").trim();
      if (!model || !modelAllowedByProviderPreset(provider, model)) {
        continue;
      }
      const key = modelOptionKey(provider, model);
      options.set(key, {
        key,
        provider,
        model,
        contextMode: "",
        label: String(record.label ?? `${provider}/${model}`).trim(),
        thinking: String(record.thinking ?? "").trim(),
        favorite: true,
        contextWindow: 0,
      });
    }
  }

  for (const [provider, response] of catalogByProvider) {
    for (const record of response.records ?? []) {
      const model = String(record.model ?? "").trim();
      if (!model || !modelAllowedByProviderPreset(provider, model)) {
        continue;
      }
      const key = modelOptionKey(provider, model);
      const current = options.get(key);
      if (!current) {
        options.set(key, {
          key,
          provider,
          model,
          contextMode: "",
          label: `${provider}/${model}`,
          thinking: "",
          favorite: false,
          contextWindow:
            typeof record.context_window === "number"
              ? record.context_window
              : 0,
        });
        continue;
      }
      options.set(key, {
        ...current,
        contextWindow:
          typeof record.context_window === "number"
            ? record.context_window
            : current.contextWindow,
      });
    }
  }

  for (const option of Array.from(options.values())) {
    if (!supportsCodex1MMode(option.provider, option.model)) {
      continue;
    }
    const contextMode = "1m";
    const key = modelOptionKey(option.provider, option.model, contextMode);
    if (options.has(key)) {
      continue;
    }
    options.set(key, {
      ...option,
      key,
      contextMode,
    });
  }

  return sortModelOptions(Array.from(options.values()));
}
