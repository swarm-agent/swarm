import { requestJson } from '../../../app/api'
import type { DesktopPermissionRecord } from '../types/realtime'

export interface AutomationV2Settings {
  schema_version: 2
  schedule: { kind: 'interval' | 'cron' | 'trigger'; interval_seconds?: number; cron?: string; timezone?: string }
  expiration: { kind: 'indefinite' | 'at'; expires_at?: number }
  missed: 'skip' | 'coalesce'
  overlap: 'serialize' | 'independent'
  activate_on_accept: true
}
export interface AutomationV2Document extends Record<string, unknown> {
  title: string
  info: { goal: string; [key: string]: unknown }
  checkpoints: Array<{
    id: string
    title: string
    tasks?: string[]
    objective?: string
    acceptance_criteria: string[]
    closing_state?: string
    alert_conditions?: string
    [key: string]: unknown
  }>
  automation_v2: AutomationV2Settings
}
export interface AutomationV2Review { proposal_id: string; revision: number; digest: string }
export interface AutomationV2Proposal extends AutomationV2Review {
  account_id: string; workspace_id: string; session_id: string; document: AutomationV2Document; base_generation?: number
}
export interface AutomationV2Record extends AutomationV2Proposal {
  automation_id: string; generation: number; enabled: boolean; cancelled: boolean; accepted_at: number
  authorization: AutomationV2Settings['expiration']; next_due_at?: number
  archived?: boolean
  archived_at?: number
}
export interface AutomationV2OccurrenceDeliverable {
  label?: string
  path?: string
  media_type?: string
  filename?: string
  artifact_id?: string
  revision_ref?: string
  session_id?: string
  collection_id?: string
  variant_id?: string
  source_ref?: string
  url?: string
}

export interface AutomationV2Occurrence {
  id: string
  state: string
  detail?: string
  due_at: number
  session_id: string
  accepted: AutomationV2Record
  run_id?: string
  admitted_at?: number
  observed_at?: number
  closing_state?: string
  deliverables?: AutomationV2OccurrenceDeliverable[]
  artifacts?: AutomationV2OccurrenceDeliverable[]
  summary?: string
  result?: string
  report?: string
}

export interface AutomationV2Progress {
  record: AutomationV2Record; observed_at: number; timezone: string; forecast: number[]; forecast_is_admission: false
  no_next_reason?: string; complete: boolean; next_cursor?: string
  occurrences: AutomationV2Occurrence[]
}
export interface AutomationV2Read { workspace_id: string; action: 'list' | 'review' | 'progress'; session_id?: string; timezone?: string; cursor?: string; archived_mode?: 'exclude' | 'include' | 'only' }
export interface AutomationV2Response {
  records?: AutomationV2Record[];
  next_cursor?: string;
  proposal?: AutomationV2Proposal;
  record?: AutomationV2Record;
  progress?: AutomationV2Progress;
  token_minted?: boolean;
  token_path?: string;
  message?: string;
}
export type AutomationV2Mutation = { workspace_id: string; session_id: string } & (
  | { action: 'propose_automation'; document: AutomationV2Document; review: AutomationV2Review }
  | { action: 'accept_automation'; review: AutomationV2Review }
  | { action: 'decline_automation'; review: AutomationV2Review }
  | { action: 'pause' | 'resume' | 'cancel_future' | 'cancel_all'; generation: number }
)
export function automationV2Review(value: AutomationV2Review): AutomationV2Review {
  if (!value?.proposal_id || !Number.isSafeInteger(value.revision) || value.revision < 1 || !/^[a-f0-9]{64}$/.test(value.digest)) throw new Error('Exact automation review unavailable. Refresh the proposal.')
  return { proposal_id: value.proposal_id, revision: value.revision, digest: value.digest }
}

export function automationV2PermissionProposal(permission: DesktopPermissionRecord): AutomationV2Proposal | null {
  const req = String(permission.requirement || '').toLowerCase()
  if (req !== 'automation_v2_acceptance') return null
  try {
    const rawArgs = permission.toolArguments || (permission as any).tool_arguments || ''
    const payload = typeof rawArgs === 'string' ? JSON.parse(rawArgs) : rawArgs
    const review = automationV2Review(payload.worker_review || payload.automation_review)
    const workspaceId = payload.scope?.workspace_id || payload.workspace_id || (permission as any).workspaceId || (permission as any).workspace_id
    const accountId = payload.scope?.account_id || payload.account_id || (permission as any).accountScopeId || (permission as any).account_scope_id || 'default'
    const sessionId = permission.sessionId || (permission as any).session_id
    const doc = payload.document
    const settings = doc?.worker_v2 || doc?.automation_v2
    const isAutomationV2 = payload.review_kind === 'worker_v2' || payload.review_kind === 'automation_v2' || Boolean(settings)
    if (!isAutomationV2 || !workspaceId || !settings || !doc?.checkpoints?.length) return null
    if (!doc.automation_v2) doc.automation_v2 = settings
    if (!doc.worker_v2) doc.worker_v2 = settings
    return { ...review, workspace_id: workspaceId, account_id: accountId, session_id: sessionId, document: doc }
  } catch { return null }
}
export function validateAutomationV2(settings: AutomationV2Settings, now = Date.now()): void {
  if (settings.schema_version !== 2 || settings.activate_on_accept !== true || !['skip', 'coalesce'].includes(settings.missed) || !['serialize', 'independent'].includes(settings.overlap)) throw new Error('Unsupported automation policy.')
  const expiration = settings.expiration ?? { kind: 'indefinite' }
  if (expiration.kind === 'at' ? !Number.isSafeInteger(expiration.expires_at) || expiration.expires_at! <= now : expiration.kind !== 'indefinite' || !!expiration.expires_at) throw new Error('Choose Indefinite or a future expiration.')
  const schedule = settings.schedule
  if (schedule.kind === 'interval') {
    if (!Number.isSafeInteger(schedule.interval_seconds) || schedule.interval_seconds! < 60 || schedule.interval_seconds! > 31622400 || schedule.cron || schedule.timezone) throw new Error('Elapsed timer must be 60–31622400 whole seconds; it has no timezone.')
  } else if (schedule.kind === 'cron') {
    const fields = (schedule.cron ?? '').trim().split(/\s+/)
    const bounds = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 6]]
    if (fields.length !== 5 || fields.some((field, i) => {
      if (field === '*') return false
      if (!/^(\d+|\*\/\d+)$/.test(field)) return true
      const step = field.startsWith('*/'), n = Number(step ? field.slice(2) : field)
      return n < (step ? 1 : bounds[i][0]) || n > (step ? bounds[i][1] - bounds[i][0] + 1 : bounds[i][1])
    }) || (fields[2] !== '*' && fields[4] !== '*') || schedule.interval_seconds) throw new Error('Use five cron fields: numbers, * or */n only; do not restrict both day fields.')
    if (!schedule.timezone || schedule.timezone === 'Local') throw new Error('An explicit IANA timezone is required.')
    try { new Intl.DateTimeFormat('en', { timeZone: schedule.timezone }).format(now) } catch { throw new Error('Enter a valid IANA timezone.') }
  } else if (schedule.kind === 'trigger') {
    if (schedule.cron || schedule.interval_seconds) throw new Error('Trigger schedule must not declare cron or interval.')
  } else throw new Error('Choose an elapsed timer, wall-clock schedule, or on-demand trigger.')
}
export async function readAutomationV2(input: AutomationV2Read): Promise<AutomationV2Response> {
  const query = new URLSearchParams({ workspace_id: input.workspace_id })
  if (input.session_id) query.set('session_id', input.session_id)
  if (input.timezone) query.set('timezone', input.timezone)
  if (input.cursor) query.set('cursor', input.cursor)
  if (input.archived_mode) query.set('archived_mode', input.archived_mode)
  if (input.action === 'list') query.set('limit', '20')
  const value = await requestJson<AutomationV2Response & AutomationV2Progress>(`/v3/automations/v2${input.action === 'list' ? '' : '/' + input.action}?${query}`)
  return input.action === 'progress' ? { progress: value, record: value.record } : value
}
export function mutateAutomationV2(input: AutomationV2Mutation): Promise<AutomationV2Response> {
  if (input.action === 'propose_automation') validateAutomationV2(input.document.automation_v2)
  if ('review' in input) automationV2Review(input.review)
  const path = input.action === 'propose_automation' ? 'proposal' : input.action === 'accept_automation' ? 'accept' : input.action === 'decline_automation' ? 'decline' : 'control'
  return requestJson(`/v3/automations/v2/${path}`, { method: 'POST', body: JSON.stringify(input) })
}
