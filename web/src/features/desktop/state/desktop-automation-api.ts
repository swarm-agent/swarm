import { apiFetch } from '../../../app/api'
import { STARTUP_REQUEST_TIMEOUT_MS, withRequestDeadline } from '../../../app/request-lifecycle'

export type AutomationKind = 'definition' | 'occurrence' | 'context' | 'audit'
export interface AutomationDefinition {
  name: string
  enabled: boolean
  plans: { id: string; plan: { session_id: string; plan_id: string; revision: number; document_sha256?: string }; depends_on?: string[] }[]
  schedule: { kind: 'manual' | 'interval' | 'cron' | 'event'; expression?: string; timezone?: string; interval_seconds?: number; trigger_source?: string; missed_policy: 'skip' | 'coalesce'; overlap_policy: 'independent' | 'serialize' }
  authorization: { mode: 'approval_required' | 'approved_policy'; approval_reference?: string; allowed_tools?: string[]; target_ids?: string[]; expires_at?: number }
}
export interface AutomationRecord {
  scope: { account_id: string; workspace_id: string }
  automation_id: string
  kind: AutomationKind
  id: string
  revision: number
  subject_id: string
  actor: string
  written_at: number
  definition?: AutomationDefinition
  occurrence?: { definition_revision: number; trigger_identity: string; scheduled_at: number; state: string; session_id?: string }
  context?: { user_locked?: Record<string, string>; agent_owned?: Record<string, string> }
  outcome?: { occurrence_id?: string; kind: string; summary: string; facts?: Record<string, string> }
}
export interface AutomationContextBundle {
  Trust: string
  Revision: number
  UserInstructions: Record<string, string> | null
  Summaries: Record<string, string> | null
}
export interface AutomationApproval {
  scope: AutomationRecord['scope']; id: string; automation_id: string; definition_revision: number
  policy_sha256: string; subject_id: string; expires_at: number; written_at: number; revoked_at?: number; revision: number
}
export interface AutomationRead {
  workspace_id: string
  action: 'list' | 'search' | 'history' | 'get' | 'context' | 'policy'
  id?: string
  kind?: AutomationKind
  record_id?: string
  query?: string
  cursor?: string
  before?: number
  revision?: number
  limit?: number
}
export interface AutomationResponse {
  records?: AutomationRecord[] | null
  next_cursor?: string
  next_before?: number
  context?: AutomationContextBundle
  record?: AutomationRecord
  policy_sha256?: string
  approval?: AutomationApproval | null
  fresh?: boolean
}
type MutationBase = { workspace_id: string; id: string; mutation_id: string; expected_revision: number }
export type AutomationMutation = MutationBase & (
  | { action: 'save'; definition: AutomationDefinition }
  | { action: 'context'; user_instructions: Record<string, string> }
  | { action: 'enable' | 'pause' }
  | { action: 'run'; scheduled_at: number }
  | { action: 'cancel'; occurrence_id: string }
  | { action: 'approve'; policy_sha256: string }
  | { action: 'revoke'; approval_reference: string }
)
export class AutomationAPIError extends Error {
  constructor(public readonly status: number) { super(status === 409 ? 'Automation changed; refresh before retrying.' : status === 403 ? 'Automation permission denied.' : `Automation request failed (${status}).`) }
}
export function automationReadURL(input: AutomationRead): string {
  if (!input.workspace_id.trim()) throw new Error('workspace_id required')
  const limit = input.limit ?? 20
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw new Error('limit must be 1–50')
  for (const value of [input.before, input.revision]) {
    if (value !== undefined && (!Number.isSafeInteger(value) || value < 0)) throw new Error('Invalid revision')
  }
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries({ ...input, limit })) {
    if (value !== undefined) query.set(key, String(value))
  }
  return `/v3/automations?${query}`
}
export function validateAutomationMutation(input: AutomationMutation): void {
  if (input.action === 'run' && (!Number.isSafeInteger(input.scheduled_at) || input.scheduled_at <= 0)) throw new Error('Fixed scheduled_at required')
  if (!input.workspace_id.trim() || !input.id.trim() || !input.mutation_id.trim() || input.mutation_id.length > 256) throw new Error('Mutation identity required')
  if (!Number.isSafeInteger(input.expected_revision) || input.expected_revision < 0 || (input.expected_revision === 0 && input.action !== 'save' && input.action !== 'context')) throw new Error('Exact revision required')
}
async function request(url: string, init: RequestInit, signal?: AbortSignal): Promise<AutomationResponse> {
  return withRequestDeadline(async (requestSignal) => {
    const response = await apiFetch(url, { ...init, signal: requestSignal })
    if (!response.ok) throw new AutomationAPIError(response.status)
    return await response.json() as AutomationResponse
  }, STARTUP_REQUEST_TIMEOUT_MS, signal)
}
export function readAutomations(input: AutomationRead, signal?: AbortSignal): Promise<AutomationResponse> {
  return request(automationReadURL(input), {}, signal)
}
// Call only in response to the user's explicit gesture. Stored policy references
// are not client-side grants; enable/run always cross backend live authorization.
export function mutateAutomation(input: AutomationMutation, signal?: AbortSignal): Promise<AutomationResponse> {
  validateAutomationMutation(input)
  const suffix = input.action === 'approve' || input.action === 'revoke' ? `/${input.action}` : ''
  return request(`/v3/automations${suffix}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }, signal)
}
