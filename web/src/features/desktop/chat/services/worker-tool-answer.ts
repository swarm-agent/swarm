// A manage_workers (or manage_automation) tool result as the chat reads it.
// The Orchestrator's manage_workers answers in the catalog envelope
// ({tool, action, machine, state, data, ...}); the action's own answer is
// data. Messages stored before the envelope hold the answer at the top level
// and are still read, so older conversations keep hiding their proposals.

import { isManageProjectsEnvelope } from './manage-projects-answer'

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

/** The action's own answer: an envelope's data, or an older message itself. */
export function workerToolAnswer(payload: unknown): Record<string, unknown> | null {
  const out = record(payload)
  if (!out) return null
  return isManageProjectsEnvelope(out) ? record(out.data) : out
}

/** Whether the result is a Worker V2 proposal waiting for the person's acceptance (shown in the Workers sidebar, not as a chat card). */
export function isPendingWorkerProposalResult(payload: unknown): boolean {
  const result = workerToolAnswer(payload)
  if (!result) return false
  return result.status === 'pending_review'
    && result.next_action === 'await_worker_acceptance'
    && result.review_kind === 'worker_v2'
}
