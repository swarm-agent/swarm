import type { AutomationMutation } from '../../state/desktop-automation-api'
import { validateAutomationMutation } from '../../state/desktop-automation-api'

// Tool output is untrusted data, never an arbitrary URL or request capability.
export function parseAutomationProposal(payload: unknown): AutomationMutation | null {
  if (!payload || typeof payload !== 'object') return null
  const result = (payload as Record<string, unknown>).result
  if (!result || typeof result !== 'object') return null
  const value = result as Record<string, unknown>
  if (value.status !== 'requires_user_approval' || value.applied !== false) return null
  const proposal = value.proposal as Record<string, unknown> | undefined
  if (!proposal || proposal.method !== 'POST' || !proposal.body || typeof proposal.body !== 'object') return null
  const body = proposal.body as Record<string, unknown>
  if (!['save', 'enable', 'pause', 'run', 'cancel', 'approve'].includes(String(body.action))) return null
  if (proposal.path !== (body.action === 'approve' ? '/v3/automations/approve' : '/v3/automations')) return null
  const allowed = ['action', 'workspace_id', 'id', 'mutation_id', 'expected_revision', ...(body.action === 'save' ? ['definition'] : body.action === 'run' ? ['scheduled_at'] : body.action === 'cancel' ? ['occurrence_id'] : body.action === 'approve' ? ['policy_sha256'] : [])]
  if (Object.keys(body).some(key => !allowed.includes(key))) return null
  try {
    const mutation = body as AutomationMutation
    validateAutomationMutation(mutation)
    if (mutation.action === 'save' && (!mutation.definition || typeof mutation.definition.name !== 'string' || typeof mutation.definition.enabled !== 'boolean' || !Array.isArray(mutation.definition.plans) || !mutation.definition.schedule || !mutation.definition.authorization)) return null
    if (mutation.action === 'approve' && (typeof mutation.policy_sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(mutation.policy_sha256))) return null
    if (mutation.action === 'cancel' && (typeof mutation.occurrence_id !== 'string' || !mutation.occurrence_id.trim())) return null
    return mutation
  } catch { return null }
}
