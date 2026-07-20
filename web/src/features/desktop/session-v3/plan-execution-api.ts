import { requestJson } from "../../../app/api";
import {
  normalizeDesktopSessionPlan,
  type DesktopSessionPlanWire,
} from "../chat/services/session-plan-record";
import type { DesktopSessionPlanRecord } from "../chat/types/chat";
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from "../state/desktop-v3-cache-store";
import { commandPlanRuntime } from "../v3-runtime/plan-runtime-api";
import type { SessionArchiveMutationResponse } from "../state/desktop-v3-cache-types";

export type DesktopPlanExecutionGranularity = "checkpointed";
export type DesktopPlanContinuationPolicy =
  "automatic" | "review_each_checkpoint";

export interface DesktopPlanLifecycleResponse {
  ok?: boolean;
  session_id?: string;
  plan_id?: string;
  transition?: string;
  change_type?: string;
  policy_effective?: string;
  approval_required?: boolean;
  status?: string;
  summary?: string;
  checkpoint_id?: string;
  attempt_id?: string;
  run_queued?: boolean;
  run_intent?: unknown;
  plan?: DesktopSessionPlanWire | null;
  execution_summary?: unknown;
  session?: unknown;
}

export type DesktopSessionArchiveResponse = SessionArchiveMutationResponse;

export interface DesktopPlanEnterInput {
  reason?: string;
}

export interface DesktopPlanSubmitInput {
  title?: string;
  plan?: string;
  document?: Record<string, unknown>;
  executionGranularity?: DesktopPlanExecutionGranularity;
  continuationPolicy?: DesktopPlanContinuationPolicy;
  continueAutomatically?: boolean;
}

export interface DesktopPlanApprovalInput {
  executionGranularity?: DesktopPlanExecutionGranularity;
  continuationPolicy?: DesktopPlanContinuationPolicy;
  continueAutomatically?: boolean;
}

export interface DesktopPlanStartAutomaticInput {
  checkpointId?: string;
  executionGranularity?: DesktopPlanExecutionGranularity;
  continuationPolicy?: DesktopPlanContinuationPolicy;
  continueAutomatically?: boolean;
}

export interface DesktopPlanStartCheckpointedInput {
  checkpointId?: string;
  executionGranularity?: DesktopPlanExecutionGranularity;
  continuationPolicy?: DesktopPlanContinuationPolicy;
  continueAutomatically?: boolean;
}

export interface DesktopPlanCurrentRunInput {
  planId?: string;
}

export interface DesktopPlanCheckpointInput {
  planId?: string;
  definitionRevision?: number;
  expectedExecutionSeq?: number;
  clientRequestId?: string;
  attemptId?: string;
  runId?: string;
  epochId?: string;
  runSessionId?: string;
  parentSessionId?: string;
  suppressLifecycleMessage?: boolean;
}

export interface DesktopPlanCheckpointAcceptInput {
  planId?: string;
  result?: string;
  notes?: string;
  reviewedAt?: number;
}

export interface DesktopPlanCheckpointResolveBlockInput {
  planId?: string;
  result?: string;
  notes?: string;
  reviewedAt?: number;
  startNext?: boolean;
}

export interface DesktopPlanRestoreRevisionInput {
  planId?: string;
  version: number;
  checkpointId?: string;
  executionGranularity?: DesktopPlanExecutionGranularity;
  continuationPolicy?: DesktopPlanContinuationPolicy;
  continueAutomatically?: boolean;
  restart?: boolean;
  start?: boolean;
  skipPrior?: boolean;
  suppressLifecycleMessage?: boolean;
}

export async function enterDesktopPlanMode(
  sessionId: string,
  input: DesktopPlanEnterInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, "enter", {
    reason: trimmed(input.reason),
  });
}

export async function submitDesktopPlanForApproval(
  sessionId: string,
  planId: string,
  input: DesktopPlanSubmitInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `plans/${encodePathSegment(planId)}/submit`,
    {
      title: trimmed(input.title),
      plan: input.plan,
      document: input.document,
      execution_granularity: input.executionGranularity,
      continuation_policy: input.continuationPolicy,
      continue_automatically: input.continueAutomatically,
    },
  );
}

export async function approveDesktopPlan(
  sessionId: string,
  planId: string,
  input: DesktopPlanApprovalInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `plans/${encodePathSegment(planId)}/approve`,
    {
      execution_granularity: input.executionGranularity,
      continuation_policy: input.continuationPolicy,
      continue_automatically: input.continueAutomatically,
    },
  );
}

export async function startDesktopPlanAutomatic(
  sessionId: string,
  planId: string,
  input: DesktopPlanStartAutomaticInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `plans/${encodePathSegment(planId)}/start-automatic`,
    {
      checkpoint_id: trimmed(input.checkpointId),
      execution_granularity: input.executionGranularity,
      continuation_policy: input.continuationPolicy,
      continue_automatically: input.continueAutomatically,
    },
  );
}

export async function startDesktopPlanCheckpointed(
  sessionId: string,
  planId: string,
  input: DesktopPlanStartCheckpointedInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `plans/${encodePathSegment(planId)}/start-checkpointed`,
    {
      checkpoint_id: trimmed(input.checkpointId),
      execution_granularity: input.executionGranularity,
      continuation_policy: input.continuationPolicy,
      continue_automatically: input.continueAutomatically,
    },
  );
}

export async function pauseDesktopPlanRun(
  sessionId: string,
  input: DesktopPlanCurrentRunInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, "runs/current/pause", {
    plan_id: trimmed(input.planId),
  });
}

export async function stopDesktopPlanRun(
  sessionId: string,
  input: DesktopPlanCurrentRunInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, "runs/current/stop", {
    plan_id: trimmed(input.planId),
  });
}

export async function resumeDesktopPlanAutomatic(
  sessionId: string,
  input: DesktopPlanCurrentRunInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(sessionId, "runs/current/resume-automatic", {
    plan_id: trimmed(input.planId),
  });
}

export async function resumeDesktopPlanCheckpointed(
  sessionId: string,
  input: DesktopPlanCurrentRunInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    "runs/current/resume-checkpointed",
    {
      plan_id: trimmed(input.planId),
    },
  );
}

export async function startDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanRuntimeCommand(sessionId, checkpointId, "start_checkpoint", input);
}

export async function continueDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanRuntimeCommand(sessionId, checkpointId, "start_checkpoint", input);
}

export async function acceptDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointAcceptInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `checkpoints/${encodePathSegment(checkpointId)}/accept`,
    {
      plan_id: trimmed(input.planId),
      result: input.result,
      notes: input.notes,
      reviewed_at: input.reviewedAt,
    },
  );
}

export async function acceptAndContinueDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointAcceptInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  const acceptResponse = await acceptDesktopPlanCheckpoint(
    sessionId,
    checkpointId,
    input,
  );
  const nextCheckpointId = nextRunnableCheckpointIdAfterAccept(
    acceptResponse,
    checkpointId,
  );
  if (!nextCheckpointId) return acceptResponse;
  return continueDesktopPlanCheckpoint(sessionId, nextCheckpointId, {
    planId: trimmed(input.planId) ?? trimmed(acceptResponse.plan_id),
    suppressLifecycleMessage: true,
  });
}

export async function resolveDesktopPlanBlockedCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointResolveBlockInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `checkpoints/${encodePathSegment(checkpointId)}/resolve-block`,
    {
      plan_id: trimmed(input.planId),
      result: input.result,
      notes: input.notes,
      reviewed_at: input.reviewedAt,
      start_next: input.startNext || undefined,
    },
  );
}

export async function resumeDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `checkpoints/${encodePathSegment(checkpointId)}/resume`,
    {
      plan_id: trimmed(input.planId),
    },
  );
}

export async function restartDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `checkpoints/${encodePathSegment(checkpointId)}/restart`,
    {
      plan_id: trimmed(input.planId),
    },
  );
}

export async function rewindDesktopPlanCheckpoint(
  sessionId: string,
  checkpointId: string,
  input: DesktopPlanCheckpointInput = {},
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    `checkpoints/${encodePathSegment(checkpointId)}/rewind`,
    {
      plan_id: trimmed(input.planId),
    },
  );
}

export async function restoreDesktopPlanRevision(
  sessionId: string,
  input: DesktopPlanRestoreRevisionInput,
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    "lifecycle/restore-revision",
    restoreRevisionPayload(input),
  );
}

export async function restartDesktopPlanFromRevision(
  sessionId: string,
  input: DesktopPlanRestoreRevisionInput,
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    "lifecycle/restart-from-revision",
    restoreRevisionPayload(input, { restart: true, start: true }),
  );
}

export async function jumpDesktopPlanToRevisionCheckpoint(
  sessionId: string,
  input: DesktopPlanRestoreRevisionInput,
): Promise<DesktopPlanLifecycleResponse> {
  return postDesktopPlanLifecycle(
    sessionId,
    "lifecycle/jump-to-checkpoint",
    restoreRevisionPayload(input, {
      restart: true,
      start: true,
      skipPrior: true,
    }),
  );
}

export async function archiveDesktopV3Sessions(
  sessionIds: string[],
): Promise<DesktopSessionArchiveResponse> {
  const normalizedIds = sessionIds
    .map((sessionId) => sessionId.trim())
    .filter(Boolean);
  if (normalizedIds.length === 0)
    throw new Error("Archive request requires at least one session_id");
  const response = await requestJson<DesktopSessionArchiveResponse>(
    "/v3/sessions:archive",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ session_ids: normalizedIds }),
    },
  );
  dispatchDesktopV3Cache({
    type: "mutation.sessionArchiveResult",
    raw: response,
  });
  return response;
}

function restoreRevisionPayload(
  input: DesktopPlanRestoreRevisionInput,
  defaults: { restart?: boolean; start?: boolean; skipPrior?: boolean } = {},
): Record<string, unknown> {
  return {
    plan_id: trimmed(input.planId),
    version: input.version,
    checkpoint_id: trimmed(input.checkpointId),
    execution_granularity: input.executionGranularity,
    continuation_policy: input.continuationPolicy,
    continue_automatically: input.continueAutomatically,
    restart: input.restart ?? defaults.restart,
    start: input.start ?? defaults.start,
    skip_prior: input.skipPrior ?? defaults.skipPrior,
    suppress_lifecycle_message: input.suppressLifecycleMessage || undefined,
  };
}

async function postDesktopPlanRuntimeCommand(
  sessionId: string,
  checkpointId: string,
  action: string,
  input: DesktopPlanCheckpointInput,
): Promise<DesktopPlanLifecycleResponse> {
  const runtime = getDesktopV3CacheSnapshot().planRuntimeBySession[sessionId];
  const planId = trimmed(input.planId) ?? runtime?.definition?.plan_id;
  const definitionRevision = input.definitionRevision ?? runtime?.definition?.definition_revision;
  const expectedExecutionSeq = input.expectedExecutionSeq ?? runtime?.appliedExecutionSeq;
  if (!planId || !definitionRevision || expectedExecutionSeq === undefined) {
    throw new Error("V3 plan runtime hydration is required before an execution command");
  }
  if (!input.clientRequestId) {
    throw new Error("V3 plan runtime command requires client_request_id; stale clients must refresh");
  }
  const response = await commandPlanRuntime({
    session_id: sessionId,
    plan_id: planId,
    action,
    definition_revision: definitionRevision,
    expected_execution_seq: expectedExecutionSeq,
    client_request_id: input.clientRequestId,
    checkpoint_id: checkpointId,
    attempt_id: input.attemptId,
    run_id: input.runId,
    epoch_id: input.epochId,
    run_session_id: input.runSessionId,
    parent_session_id: input.parentSessionId,
  });
  return { ok: response.ok, session_id: sessionId, plan_id: planId, transition: action, execution_summary: response.receipt };
}

async function postDesktopPlanLifecycle(
  sessionId: string,
  path: string,
  body: Record<string, unknown> = {},
): Promise<DesktopPlanLifecycleResponse> {
  const normalizedSessionId = sessionId.trim();
  if (!normalizedSessionId)
    throw new Error("Desktop plan lifecycle request requires session_id");
  const response = await requestJson<DesktopPlanLifecycleResponse>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plan-mode/${path}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
  );
  const plan = response.plan
    ? normalizeDesktopSessionPlan(response.plan)
    : null;
  if (plan) applyDesktopPlanLifecycleResult(normalizedSessionId, plan);
  return response;
}

function encodePathSegment(value: string): string {
  const trimmedValue = value.trim();
  if (!trimmedValue)
    throw new Error("Desktop plan lifecycle request requires a path id");
  return encodeURIComponent(trimmedValue);
}

function trimmed(value: string | undefined): string | undefined {
  const normalized = value?.trim() ?? "";
  return normalized || undefined;
}

function nextRunnableCheckpointIdAfterAccept(
  response: DesktopPlanLifecycleResponse,
  acceptedCheckpointId: string,
): string {
  const summary = objectRecord(response.execution_summary);
  if (
    booleanValue(summary?.plan_complete) ||
    booleanValue(summary?.review_required) ||
    booleanValue(summary?.blocked) ||
    booleanValue(summary?.failed)
  ) {
    return "";
  }
  const status = String(summary?.next_checkpoint_status ?? "")
    .trim()
    .toLowerCase();
  if (status && status !== "pending" && status !== "in_progress") {
    return "";
  }
  const nextCheckpointId = String(
    summary?.next_checkpoint_id ?? fallbackActiveCheckpointId(response.plan),
  ).trim();
  if (!nextCheckpointId || nextCheckpointId === acceptedCheckpointId.trim())
    return "";
  return nextCheckpointId;
}

function fallbackActiveCheckpointId(
  plan: DesktopSessionPlanWire | null | undefined,
): string {
  const planRecord = objectRecord(plan);
  const document = objectRecord(planRecord?.document);
  return String(
    document?.active_checkpoint_id ?? document?.activeCheckpointId ?? "",
  ).trim();
}

function objectRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function booleanValue(value: unknown): boolean {
  return value === true || value === "true";
}

function applyDesktopPlanLifecycleResult(
  sessionId: string,
  plan: DesktopSessionPlanRecord,
): void {
  dispatchDesktopV3Cache({
    type: "planSnapshot.apply",
    sessionId,
    hasActivePlan: true,
    activePlan: plan,
    planRevisions: [],
  });
}
