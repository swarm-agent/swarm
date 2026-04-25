import { useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Bell, BellOff, CheckCheck, Clock3, Trash2 } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { cn } from '../../../../lib/cn'
import type {
  DesktopConnectionState,
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
} from '../types'

function formatRelativeTime(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) {
    return ''
  }
  const deltaMs = Math.max(0, Date.now() - timestamp)
  if (deltaMs < 60_000) {
    return 'just now'
  }
  const minutes = Math.floor(deltaMs / 60_000)
  if (minutes < 60) {
    return `${minutes} min${minutes === 1 ? '' : 's'} ago`
  }
  const hours = Math.floor(minutes / 60)
  if (hours < 24) {
    return `${hours} hr${hours === 1 ? '' : 's'} ago`
  }
  const days = Math.floor(hours / 24)
  return `${days} day${days === 1 ? '' : 's'} ago`
}

function statusTone(record: DesktopNotificationCenterRecord): 'warning' | 'live' | 'neutral' | 'danger' {
  if (record.severity === 'error') {
    return 'danger'
  }
  if (record.status === 'active' && !record.readAt) {
    return 'warning'
  }
  if (record.status === 'active') {
    return 'live'
  }
  return 'neutral'
}

function statusLabel(record: DesktopNotificationCenterRecord): string {
  if (record.mutedAt) {
    return 'Muted'
  }
  if (record.ackedAt) {
    return 'Acknowledged'
  }
  if (record.readAt) {
    return 'Read'
  }
  return record.status === 'resolved' ? 'Resolved' : 'Unread'
}

function connectionLabel(connectionState: DesktopConnectionState): string {
  switch (connectionState) {
    case 'open':
      return 'Connected'
    case 'connecting':
      return 'Reconnecting'
    case 'error':
      return 'Connection error'
    case 'closed':
      return 'Disconnected'
    default:
      return 'Idle'
  }
}

function NotificationActionSlot({ show, children }: { show: boolean; children: ReactNode }) {
  return (
    <span className="inline-flex min-h-9 min-w-[112px] justify-start">
      {show ? children : <span aria-hidden="true" className="h-9 w-full" />}
    </span>
  )
}

export function DesktopNotificationsModal({
  open,
  onOpenChange,
  notifications,
  summary,
  loading,
  connectionState,
  onMarkRead,
  onAcknowledge,
  onMute,
  onClearAll,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  notifications: DesktopNotificationCenterRecord[]
  summary: DesktopNotificationSummary
  loading: boolean
  connectionState: DesktopConnectionState
  onMarkRead: (record: DesktopNotificationCenterRecord) => Promise<void>
  onAcknowledge: (record: DesktopNotificationCenterRecord) => Promise<void>
  onMute: (record: DesktopNotificationCenterRecord) => Promise<void>
  onClearAll: () => Promise<void>
}) {
  const [clearing, setClearing] = useState(false)
  const activeNotifications = useMemo(
    () => notifications.filter((record) => record.status === 'active'),
    [notifications],
  )
  const hasNotifications = notifications.length > 0
  const summaryText = summary.unreadCount > 0
    ? `${summary.unreadCount} unread notification${summary.unreadCount === 1 ? '' : 's'}`
    : hasNotifications
      ? 'No unread notifications'
      : 'No notifications'
  const detailText = hasNotifications
    ? `${summary.activeCount} active · ${summary.totalCount} total · ${connectionLabel(connectionState).toLowerCase()}${summary.updatedAt > 0 ? ` · updated ${formatRelativeTime(summary.updatedAt)}` : ''}`
    : `${connectionLabel(connectionState)}${summary.updatedAt > 0 ? ` · updated ${formatRelativeTime(summary.updatedAt)}` : ''}`

  const handleClearAll = async () => {
    if (!hasNotifications || clearing) {
      return
    }
    setClearing(true)
    try {
      await onClearAll()
    } finally {
      setClearing(false)
    }
  }

  if (!open) {
    return null
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Notifications" className="z-[80] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(980px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
        <div className="flex flex-wrap items-start justify-between gap-3 border-b border-[var(--app-border)] px-6 py-5">
          <div className="min-w-0 space-y-2">
            <div className="flex items-center gap-2">
              <Bell size={18} className="text-[var(--app-text)]" />
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Notifications</h2>
            </div>
            <p className="text-sm text-[var(--app-text-muted)]">
              {summaryText}
            </p>
            <p className="text-xs text-[var(--app-text-subtle)]">
              {detailText}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="secondary" size="sm" onClick={() => void handleClearAll()} disabled={!hasNotifications || clearing}>
              <Trash2 size={14} /> {clearing ? 'Clearing…' : 'Clear all'}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)} aria-label="Close notifications">
              Close
            </Button>
          </div>
        </div>

        <div className="flex min-h-0 flex-1 flex-col px-6 pb-6 pt-4">
          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto pr-1">
            {loading ? (
              <Card className="p-4 text-sm text-[var(--app-text-muted)]">Loading notifications…</Card>
            ) : notifications.length === 0 ? (
              <Card className="flex flex-col items-center gap-3 p-8 text-center text-sm text-[var(--app-text-muted)]">
                <BellOff size={24} className="text-[var(--app-text-subtle)]" />
                <div>No notifications yet.</div>
              </Card>
            ) : (
              notifications.map((record) => (
                <Card key={record.id} className={cn('space-y-3 p-4', !record.readAt && 'border-[var(--app-primary)]/40')}>
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0 flex-1 space-y-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <div className="font-medium text-[var(--app-text)]">{record.title}</div>
                        <Badge tone={statusTone(record)}>{statusLabel(record)}</Badge>
                        {record.status === 'active' ? <Badge tone="live">Active</Badge> : <Badge tone="neutral">Resolved</Badge>}
                      </div>
                      <div className="text-sm text-[var(--app-text-muted)]">{record.body || 'No details provided.'}</div>
                    </div>
                    <div className="flex items-center gap-2 text-xs text-[var(--app-text-subtle)]">
                      <Clock3 size={14} />
                      {formatRelativeTime(record.updatedAt || record.createdAt) || 'just now'}
                    </div>
                  </div>

                  <div className="flex flex-wrap gap-2 text-xs text-[var(--app-text-muted)]">
                    <span>Category: {record.category}</span>
                    {record.toolName ? <span>Tool: {record.toolName}</span> : null}
                    {record.requirement ? <span>Requirement: {record.requirement}</span> : null}
                    {record.originSwarmID ? <span>Origin: {record.originSwarmID}</span> : null}
                    {record.sessionId ? <span>Session: {record.sessionId}</span> : null}
                  </div>

                  <div className="grid gap-2 text-xs text-[var(--app-text-muted)] sm:grid-cols-[112px_112px_112px]">
                    <NotificationActionSlot show={!record.readAt}>
                      <Button variant="secondary" size="sm" className="w-full justify-center" onClick={() => void onMarkRead(record)}>
                        <CheckCheck size={14} /> Mark read
                      </Button>
                    </NotificationActionSlot>
                    <NotificationActionSlot show={!record.ackedAt}>
                      <Button variant="secondary" size="sm" className="w-full justify-center" onClick={() => void onAcknowledge(record)}>
                        Acknowledge
                      </Button>
                    </NotificationActionSlot>
                    <NotificationActionSlot show={!record.mutedAt}>
                      <Button variant="ghost" size="sm" className="w-full justify-center" onClick={() => void onMute(record)}>
                        Mute
                      </Button>
                    </NotificationActionSlot>
                  </div>
                </Card>
              ))
            )}

            {!loading && notifications.length > 0 && activeNotifications.length === 0 ? (
              <Card className="p-3 text-xs text-[var(--app-text-muted)]">All current notifications are resolved.</Card>
            ) : null}
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
