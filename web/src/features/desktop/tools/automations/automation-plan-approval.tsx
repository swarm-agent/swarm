import { useState } from 'react'
import { requestJson } from '../../../../app/api'
import type { StructuredPlanDocument } from '../../chat/components/structured-plan-document'
import { readAutomations } from '../../state/desktop-automation-api'
import { desktopAutomations } from '../../runtime/desktop-automations'
import { AutomationProposalCard } from './automation-proposal-card'

// Only the server can hash its typed canonical document serialization.
export function requireAutomationDocumentDigest(value: unknown): string {
  if (typeof value !== 'string' || !/^[a-f0-9]{64}$/.test(value)) throw new Error('Canonical plan digest is unavailable; refresh before approving.')
  return value
}
export function AutomationPlanApproval({ document, parentSessionId }: { document: StructuredPlanDocument; parentSessionId: string }) {
  const [busy, setBusy] = useState(false)
  const [accepted, setAccepted] = useState(false)
  const [error, setError] = useState('')
  const [enable, setEnable] = useState<unknown>(null)
  async function accept() {
    const intent = document.automation
    if (!intent || busy || accepted) return
    setBusy(true); setError('')
    try {
      const response = await requestJson<{ document_sha256?: string; active_plan?: { id: string; version: number; document: Record<string, unknown> }; plan?: { id: string; version: number; document: Record<string, unknown> } }>(`/v3/sessions/${encodeURIComponent(parentSessionId)}/plans/active`)
      const plan = response.active_plan ?? response.plan
      if (!plan || plan.id !== document.id || JSON.stringify(plan.document.automation) !== JSON.stringify(intent) || String(plan.document.revision_id ?? '') !== document.revisionId) throw new Error('Automation proposal changed; refresh before approving.')
      const policy = await readAutomations({ action: 'policy', workspace_id: intent.scope.workspace_id, id: intent.automation_id })
      if (policy.record?.revision !== intent.definition_revision || policy.record.scope.workspace_id !== intent.scope.workspace_id || policy.record.scope.account_id !== intent.scope.account_id || policy.record.automation_id !== intent.automation_id || !policy.policy_sha256 || policy.record.definition?.session_id !== parentSessionId) throw new Error('Automation configuration changed; refresh before approving.')
      const result = await desktopAutomations.mutate({ action: 'approve', workspace_id: intent.scope.workspace_id, id: intent.automation_id, expected_revision: intent.definition_revision, mutation_id: crypto.randomUUID(), policy_sha256: policy.policy_sha256, proposal: { session_id: parentSessionId, plan_id: plan.id, revision: plan.version, document_sha256: requireAutomationDocumentDigest(response.document_sha256) } })
      setAccepted(true)
      if (result.enable_proposal) setEnable({ result: { status: 'requires_user_approval', applied: false, proposal: result.enable_proposal } })
    } catch (cause) { setError(cause instanceof Error ? cause.message : 'Approval failed') }
    finally { setBusy(false) }
  }
  return <section aria-label="Approve automation"><p>Approve recurring execution policy and convert this conversation to Automation. This does not start a one-time plan. Enabling requires a separate explicit acceptance. Historical plans and artifacts are retained.</p><button disabled={busy || accepted} onClick={() => void accept()}>{accepted ? 'Automation approved' : busy ? 'Approving…' : 'Approve automation conversion'}</button>{error && <p role="alert">{error}</p>}{enable != null && <AutomationProposalCard payload={enable} />}</section>
}
