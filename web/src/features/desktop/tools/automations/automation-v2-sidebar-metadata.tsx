import { useEffect, useMemo, useState } from 'react'
import { Clock3 } from 'lucide-react'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Record, AutomationV2Settings } from '../../state/desktop-automation-v2-api'
import { automationV2PermissionProposal } from './automation-v2-plan-review'
import { scheduleFrequency, scheduleLabel } from './automation-v2-schedule'

export function AutomationSidebarMetadataRow({ schedule, status, nextDueAt }: {
  schedule?: AutomationV2Settings['schedule']; status: string; nextDueAt?: number
}) {
  const cadence = schedule ? scheduleLabel(schedule) : 'Automation'
  const details = [cadence, schedule?.timezone, schedule && scheduleFrequency(schedule), status,
    nextDueAt ? `Next scheduled: ${new Date(nextDueAt).toLocaleString()} (local time; not a guaranteed start)` : undefined,
  ].filter(Boolean).join(' · ')
  return <div aria-label="Automation metadata" title={details} className="mt-0.5 flex min-w-0 items-center justify-between gap-2 text-[10px] leading-4 text-[var(--app-text-subtle)]">
    <span className="flex min-w-0 items-center gap-1.5"><Clock3 size={11} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" /><span className="min-w-0 truncate">{cadence}{schedule?.timezone ? ` · ${schedule.timezone}` : ''}</span></span>
    <span className="shrink-0 text-[var(--app-text-muted)]">{status}</span>
  </div>
}

export function AutomationV2SidebarMetadata({ sessionId, identity, now, needsApproval }: {
  sessionId: string; identity: 'pending' | 'accepted'; now: number; needsApproval: boolean
}) {
  const permission = useDesktopV3CacheSelector(state => state.permissionsBySession[sessionId]?.find(p => p.status === 'pending' && p.requirement === 'automation_v2_acceptance'))
  const proposal = useMemo(() => permission ? automationV2PermissionProposal(permission) : null, [permission])
  const workspaceId = useDesktopV3CacheSelector(state => {
    const session = state.sessionsById[sessionId]
    if (session?.kind === 'full' && session.session.automation_v2) return session.session.automation_v2.workspace_id
    for (const page of Object.values(state.automationV2Pages)) {
      const record = page.data?.record ?? page.data?.progress?.record
      if (record?.session_id === sessionId) return record.workspace_id
      const listed = page.data?.records?.find(record => record.session_id === sessionId)
      if (listed) return listed.workspace_id
    }
    return undefined
  })
  if (identity === 'pending') return <AutomationSidebarMetadataRow schedule={proposal?.document.automation_v2.schedule} status="Awaiting acceptance" />
  if (!workspaceId) return <AutomationSidebarMetadataRow status="Schedule unavailable" />
  return <AcceptedAutomationMetadata workspaceId={workspaceId} sessionId={sessionId} now={now} needsApproval={needsApproval} />
}

function AcceptedAutomationMetadata({ workspaceId, sessionId, now, needsApproval }: {
  workspaceId: string; sessionId: string; now: number; needsApproval: boolean
}) {
  const input = useMemo(() => ({ action: 'progress' as const, workspace_id: workspaceId, session_id: sessionId, timezone: 'UTC' }), [workspaceId, sessionId])
  const key = automationV2PageKey(input)
  const page = useDesktopV3CacheSelector(state => state.automationV2Pages[key])
  const [capacityError, setCapacityError] = useState(false)
  useEffect(() => {
    try { const lease = desktopAutomationV2.acquire(input); setCapacityError(false); return lease.release }
    catch { setCapacityError(true) }
  }, [input])
  const record = page?.data?.progress?.record
  const unavailable = capacityError || !!page?.error
  const refreshing = !page || page.loading || page.stale
  const status = unavailable ? 'Schedule unavailable' : refreshing ? 'Updating schedule…' : record ? automationSidebarStatus(record, now, needsApproval) : 'Schedule unavailable'
  return <AutomationSidebarMetadataRow schedule={record?.document.automation_v2.schedule} status={status} nextDueAt={!unavailable && !refreshing && record && status === 'Scheduled' ? record.next_due_at : undefined} />
}

export function automationSidebarStatus(record: AutomationV2Record, now: number, needsApproval: boolean): string {
  if (record.cancelled) return 'Cancelled'
  if (record.authorization.kind === 'at' && record.authorization.expires_at !== undefined && record.authorization.expires_at <= now) return 'Expired'
  if (!record.enabled) return 'Paused'
  if (needsApproval) return 'Needs approval'
  return 'Scheduled'
}
