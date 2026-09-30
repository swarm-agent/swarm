import { useState } from 'react'
import { Bell } from 'lucide-react'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { selectNotificationSummary, selectOrderedNotifications } from '../../state/desktop-v3-cache-selectors'
import { DesktopNotificationsModal } from './desktop-notifications-modal'
import { useWorkerApprovalAttention } from './worker-approval-attention'
import { clearNotifications, updateNotification } from '../api'

export function OrchestratorNotifications({ accountScopeId, workspaceSlug }: { accountScopeId: string; workspaceSlug?: string }) {
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')
  const notifications = useDesktopV3CacheSelector(selectOrderedNotifications)
  const summary = useDesktopV3CacheSelector(selectNotificationSummary)
  const attention = useWorkerApprovalAttention(accountScopeId)
  const count = summary.unreadCount + attention.workers.length
  const act = async (action: () => Promise<unknown>) => {
    setError('')
    try { await action() } catch (cause) { setError(cause instanceof Error ? cause.message : 'Notification update failed') }
  }
  return <>
    <button type="button" className="rounded-lg p-1.5 text-slate-300 hover:bg-slate-800" aria-label={`Open notifications${count ? ` · ${count}${attention.partial ? '+' : ''} need attention` : ''}`} title="Notifications and worker approvals" onClick={() => setOpen(true)}><Bell size={14} /><span className="text-[10px]">{attention.page?.error ? '!' : count || attention.partial ? `${count}${attention.partial ? '+' : ''}${attention.page?.stale ? ' ·' : ''}` : ''}</span></button>
    <DesktopNotificationsModal open={open} onOpenChange={setOpen} workspaceSlug={workspaceSlug} notifications={notifications} summary={summary} loading={false} connectionState="idle" onMarkRead={record => act(() => updateNotification(record.id, { read: true }))} onAcknowledge={record => act(() => updateNotification(record.id, { acked: true }))} onMute={record => act(() => updateNotification(record.id, { muted: true }))} onClearAll={() => act(clearNotifications)} />
    {error && <p role="alert">{error}</p>}
  </>
}
