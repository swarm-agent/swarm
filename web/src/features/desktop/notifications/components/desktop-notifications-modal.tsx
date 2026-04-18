import { useMemo } from 'react'
import { Bell, BellOff, CheckCheck, Clock3 } from 'lucide-react'
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
      return 'Live updates connected'
    case 'connecting':
      return 'Realtime reconnecting'
    case 'error':
      return 'Realtime error'
    case 'closed':
      return 'Realtime disconnected'
    default:
      return 'Realtime idle'
  }
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
}) {
  const activeNotifications = useMemo(
    () => notifications.filter((record) => record.status === 'active'),
    [notifications],
  )

  if (!open) {
    return null
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Notifications" className="z-[80] p-4 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(980px,calc(100vw-24px))] gap-4 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:w-[min(1040px,calc(100vw-48px))]">
        <div className="flex items-start justify-between border-b border-[var(--app-border)] px-6 py-5">
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <Bell size={18} className="text-[var(--app-text)]" />
              <h2 className="text-lg font-semibold text-[var(--app-text)]">Notifications</h2>
            </div>
            <p className="text-sm text-[var(--app-text-muted)]">
              {summary.unreadCount} unread · {summary.activeCount} active · {connectionLabel(connectionState)}
            </p>
          </div>
          <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)} aria-label="Close notifications">
            Close
          </Button>
        </div>

        <div className="grid gap-4 px-6 pb-6 lg:grid-cols-[240px,minmax(0,1fr)]">
          <Card className="flex flex-col gap-3 p-4">
            <Badge tone={summary.unreadCount > 0 ? 'warning' : 'neutral'}>
              Unread {summary.unreadCount}
            </Badge>
            <Badge tone={summary.activeCount > 0 ? 'live' : 'neutral'}>
              Active {summary.activeCount}
            </Badge>
            <Badge tone={connectionState === 'open' ? 'live' : connectionState === 'error' ? 'danger' : 'neutral'}>
              {connectionLabel(connectionState)}
            </Badge>
            <div className="text-xs text-[var(--app-text-muted)]">
              Updated {formatRelativeTime(summary.updatedAt) || 'not yet'}
            </div>
          </Card>

          <div className="min-h-[360px] space-y-3 overflow-y-auto pr-1">
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

                  <div className="flex flex-wrap gap-2">
                    {!record.readAt ? (
                      <Button variant="secondary" size="sm" onClick={() => void onMarkRead(record)}>
                        <CheckCheck size={14} /> Mark read
                      </Button>
                    ) : null}
                    {!record.ackedAt ? (
                      <Button variant="secondary" size="sm" onClick={() => void onAcknowledge(record)}>
                        Acknowledge
                      </Button>
                    ) : null}
                    {!record.mutedAt ? (
                      <Button variant="ghost" size="sm" onClick={() => void onMute(record)}>
                        Mute
                      </Button>
                    ) : null}
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
