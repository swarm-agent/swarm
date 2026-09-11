import type { AutomationDefinition, AutomationMutation } from '../../state/desktop-automation-api'

function dateLabel(value?: number) {
  if (!value) return 'Not specified'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? 'Invalid date — review required' : date.toLocaleString()
}
export function AutomationDefinitionSummary({ definition }: { definition: AutomationDefinition }) {
  const schedule = definition.schedule
  const trigger = schedule.kind === 'interval' ? `Every ${schedule.interval_seconds ?? '?'} seconds` : schedule.kind === 'cron' ? `Cron: ${schedule.expression ?? 'not specified'}` : schedule.kind === 'event' ? `Event source: ${schedule.trigger_source ?? 'not configured'}` : 'Manual — explicit run request'
  return <section aria-label="Automation plan summary" className="space-y-3 rounded-lg border border-[var(--app-border)] p-4">
    <h3 className="font-semibold">{definition.name}</h3><dl className="grid gap-3 text-sm sm:grid-cols-2">
      <div><dt className="font-semibold">Trigger</dt><dd>{trigger} · {schedule.timezone || 'Timezone not specified'}</dd></div>
      <div><dt className="font-semibold">Scheduling rules</dt><dd>Missed: {schedule.missed_policy}. Overlap: {schedule.overlap_policy}.</dd></div>
      <div><dt className="font-semibold">Requested state</dt><dd>{definition.enabled ? 'Enabled requested — server authorization still required' : 'Paused'}</dd></div>
      <div><dt className="font-semibold">Policy expires</dt><dd>{dateLabel(definition.authorization.expires_at)}</dd></div>
      <div><dt className="font-semibold">Tool constraints</dt><dd className="break-words">{definition.authorization.allowed_tools?.join(', ') || 'No additional tool allowlist specified; normal permissions still apply.'}</dd></div>
      <div><dt className="font-semibold">Target constraints</dt><dd className="break-words">{definition.authorization.target_ids?.join(', ') || 'No additional target list specified; workspace grants still apply.'}</dd></div>
    </dl><h4 className="font-semibold">Pinned instructions</h4><ol className="list-inside list-decimal space-y-2 text-sm">{definition.plans.map(binding => <li key={binding.id} className="break-words">{binding.id} · plan {binding.plan.plan_id} · revision {binding.plan.revision}{binding.depends_on?.length ? ` · after ${binding.depends_on.join(', ')}` : ''}</li>)}</ol>
    {!definition.plans.length && <p>No pinned plan yet. Ask the AI to draft one for review.</p>}
    <p className="text-xs text-[var(--app-text-muted)]">Review plan content in its conversation before approving. Saving is not approval; approval is not enabling; a run request is not proof of execution.</p>
  </section>
}
const actionDescriptions: Record<AutomationMutation['action'], string> = {
  save: 'Save this exact configuration revision. This does not grant execution permission.',
  context: 'Replace the user instruction map with the reviewed text below.',
  approve: 'Approve the exact saved execution policy digest. Enabling is a separate action.',
  enable: 'Enable the approved automation. This may admit scheduled work under its current policy.',
  pause: 'Pause future scheduling. This does not cancel already admitted occurrences.',
  run: 'Admit one run under the current policy. Admission does not prove execution or success.',
  cancel: 'Request cancellation of the specified occurrence.',
  revoke: 'Revoke the specified execution approval.',
}
export function AutomationProposalSummary({ proposal }: { proposal: AutomationMutation }) {
  return <div className="space-y-3 text-sm"><p>{actionDescriptions[proposal.action]}</p><p className="break-words">Automation: {proposal.id} · expected revision {proposal.expected_revision}</p>
    {proposal.action === 'save' && <AutomationDefinitionSummary definition={proposal.definition} />}
    {proposal.action === 'context' && <dl>{Object.entries(proposal.user_instructions).map(([key, value]) => <div key={key}><dt className="font-semibold break-words">{key}</dt><dd className="whitespace-pre-wrap break-words">{value}</dd></div>)}</dl>}
    {proposal.action === 'run' && <p>Scheduled for {dateLabel(proposal.scheduled_at)}</p>}
    {proposal.action === 'cancel' && <p className="break-all">Occurrence: {proposal.occurrence_id}</p>}
    {proposal.action === 'approve' && <p className="break-all">Policy digest: {proposal.policy_sha256}</p>}
    {proposal.action === 'revoke' && <p className="break-all">Approval: {proposal.approval_reference}</p>}
  </div>
}
