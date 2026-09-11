import { useRef, useState } from 'react'
import { desktopAutomations } from '../../runtime/desktop-automations'
import { parseAutomationProposal } from './automation-proposal'
import { AutomationSessionPanel } from './automation-session'

export function AutomationProposalCard({ payload }: { payload: unknown }) {
  const proposal = parseAutomationProposal(payload)
  const lock = useRef(false)
  const [pending, setPending] = useState(false)
  const [accepted, setAccepted] = useState(false)
  const [message, setMessage] = useState('')
  const [enable, setEnable] = useState<unknown>(null)
  if (!proposal) return <p>Automation result contains no actionable proposal.</p>
  async function accept() {
    if (!proposal || lock.current || accepted) return
    lock.current = true; setPending(true)
    try {
      const response = await desktopAutomations.mutate(proposal)
      if (response.enable_proposal) setEnable({ result: { status: 'requires_user_approval', applied: false, proposal: response.enable_proposal } })
      setAccepted(true)
      setMessage(proposal.action === 'run' ? 'Run admitted, not proof of execution or success.' : 'Request recorded. Execution approval and enabling are separate steps.')
    } catch (error) { setMessage(error instanceof Error ? error.message : 'Request failed; refresh before retrying.') }
    finally { lock.current = false; setPending(false) }
  }
  return <section aria-label="Automation proposal" className="my-3 rounded border border-[var(--app-border)] p-3">
    <h3>Review automation {proposal.action}</h3>
    <p>The AI cannot authorize this request. Review the complete request before accepting; saved historical proposals may already be applied or stale.</p>
    <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(proposal, null, 2)}</pre>
    <button disabled={pending || accepted} onClick={() => void accept()}>{accepted ? 'Request recorded' : pending ? 'Submitting…' : 'Accept reviewed request'}</button>
    <p role="status">{message}</p>
    {enable != null && <AutomationProposalCard payload={enable} />}
    {accepted && !enable && proposal.action !== 'cancel' && <AutomationSessionPanel workspaceId={proposal.workspace_id} id={proposal.id} />}
  </section>
}
