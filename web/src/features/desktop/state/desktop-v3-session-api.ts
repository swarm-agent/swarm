import { requestJson } from '../../../app/api'
import { normalizeDesktopSessionPlan, normalizeDesktopSessionPlanRevisions, type DesktopSessionPlanWire } from '../chat/services/session-plan-record'
import type { DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord } from '../chat/types/chat'
import { dispatchDesktopV3Cache } from './desktop-v3-cache-store'

export interface DesktopV3PlanSnapshot {
  hasActivePlan: boolean
  activePlan: DesktopSessionPlanRecord | null
  planRevisions: DesktopSessionPlanRevisionRecord[]
}

interface ActivePlanResponseWire {
  has_active?: boolean
  active_plan?: DesktopSessionPlanWire | null
}

interface PlanSaveResponseWire {
  plan?: DesktopSessionPlanWire | null
}

interface PlanHistoryResponseWire {
  revisions?: unknown
}

export function assertRawCanonicalDesktopV3SessionId(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 requires a raw canonical session id.')
  }
  return normalizedSessionId
}

function applyDesktopV3PlanSnapshot(sessionId: string, snapshot: DesktopV3PlanSnapshot): void {
  dispatchDesktopV3Cache({
    type: 'planSnapshot.apply',
    sessionId,
    hasActivePlan: snapshot.hasActivePlan,
    activePlan: snapshot.activePlan,
    planRevisions: snapshot.planRevisions,
  })
}

export async function fetchAndApplyDesktopV3PlanSnapshot(
  sessionId: string,
  options: { signal?: AbortSignal; includeHistory?: boolean } = {},
): Promise<DesktopV3PlanSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const active = await requestJson<ActivePlanResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/active`,
    { signal: options.signal },
  )
  const hasActivePlan = active.has_active === true
  const activePlan = hasActivePlan && active.active_plan ? normalizeDesktopSessionPlan(active.active_plan) : null
  let planRevisions: DesktopSessionPlanRevisionRecord[] = []
  if (options.includeHistory !== false && activePlan?.id) {
    const history = await requestJson<PlanHistoryResponseWire>(
      `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans/${encodeURIComponent(activePlan.id)}/history?limit=100&revision_kind=definition`,
      { signal: options.signal },
    )
    planRevisions = normalizeDesktopSessionPlanRevisions(history.revisions)
  }
  const snapshot: DesktopV3PlanSnapshot = {
    hasActivePlan,
    activePlan,
    planRevisions,
  }
  applyDesktopV3PlanSnapshot(normalizedSessionId, snapshot)
  return snapshot
}

export async function saveDesktopV3SessionPlan(
  sessionId: string,
  input: {
    id?: string
    title?: string
    plan?: string
    document?: unknown
    documentPatch?: unknown
    status?: string
    approvalState?: string
  },
): Promise<DesktopV3PlanSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<PlanSaveResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: input.id?.trim() || undefined,
        plan_id: input.id?.trim() || undefined,
        title: input.title?.trim() || undefined,
        plan: input.plan,
        document: input.document ?? undefined,
        document_patch: input.documentPatch ?? undefined,
        status: input.status?.trim() || undefined,
        approval_state: input.approvalState?.trim() || undefined,
      }),
    },
  )
  const savedPlan = response.plan ? normalizeDesktopSessionPlan(response.plan) : null
  if (savedPlan) {
    applyDesktopV3PlanSnapshot(normalizedSessionId, {
      hasActivePlan: true,
      activePlan: savedPlan,
      planRevisions: [],
    })
  }
  return fetchAndApplyDesktopV3PlanSnapshot(normalizedSessionId)
}
