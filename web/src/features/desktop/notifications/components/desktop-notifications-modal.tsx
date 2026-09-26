import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import {
  AlertTriangle,
  Bell,
  BellOff,
  Bot,
  CheckCheck,
  ChevronDown,
  ChevronUp,
  Clock3,
  Cloud,
  ExternalLink,
  Loader2,
  RefreshCw,
  Sparkles,
  Trash2,
} from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { cn } from '../../../../lib/cn'
import { requestJson } from '../../../../app/api'
import { DesktopStorageBucketsModal } from '../../storage/components/desktop-storage-buckets-modal'
import { listStorageBuckets, scanStorageBucket } from '../../storage/api'
import type { StorageBucket } from '../../storage/types'
import type {
  DesktopConnectionState,
  DesktopNotificationAction,
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

function shortID(value: string | null): string {
  if (!value) {
    return ''
  }
  return value.length > 8 ? value.slice(0, 8) : value
}

function notificationSessionLabel(record: DesktopNotificationCenterRecord): string {
  return record.sessionLabel || record.sessionTitle || (record.sessionId ? `Session ${shortID(record.sessionId)}` : '')
}

function notificationMeta(record: DesktopNotificationCenterRecord): string[] {
  const meta: string[] = []
  const origin = record.originLabel || (record.workerId ? `Worker: ${record.workerId}` : record.originSwarmID ? shortID(record.originSwarmID) : '')
  if (origin) {
    meta.push(`Origin: ${origin}`)
  }
  if (record.workspaceName || record.workspacePath) {
    meta.push(`Workspace: ${record.workspaceName || record.workspacePath}`)
  }
  if (record.toolName) {
    meta.push(`Tool: ${record.toolName}`)
  }
  if (record.requirement) {
    meta.push(`Requirement: ${record.requirement}`)
  }
  return meta
}

function isInboxNotification(record: DesktopNotificationCenterRecord): boolean {
  return (
    record.kind === 'ai_deliverable' ||
    record.kind === 'ai_request' ||
    record.category === 'inbox' ||
    record.category === 'deliverable'
  )
}

export function isSafeNotificationActionEndpoint(endpoint: string | null | undefined): boolean {
  if (!endpoint || typeof endpoint !== 'string') return false
  const trimmed = endpoint.trim()
  if (!trimmed.startsWith('/') || trimmed.startsWith('//') || trimmed.includes('://')) {
    return false
  }
  if (trimmed.includes('..') || trimmed.includes('\\')) {
    return false
  }
  const [basePath] = trimmed.split('?')
  const allowedPrefixes = [
    '/v3/deliverables/',
    '/v1/notifications/',
    '/v1/alerts/',
    '/v3/automations/v2/',
    '/v3/sessions/',
    '/v1/storage/',
    '/settings',
  ]
  return allowedPrefixes.some((prefix) => basePath.startsWith(prefix))
}

export function isSafeActionURL(url: string | null | undefined): boolean {
  if (!url || typeof url !== 'string') return false
  const trimmed = url.trim()
  if (trimmed.startsWith('/') && !trimmed.startsWith('//')) {
    return true
  }
  try {
    const parsed = new URL(trimmed)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

export function isSafeMediaURL(url: string | null | undefined): boolean {
  if (!url || typeof url !== 'string') return false
  const trimmed = url.trim()
  const lower = trimmed.toLowerCase()
  if (lower.startsWith('javascript:') || lower.startsWith('data:') || lower.startsWith('vbscript:')) {
    return false
  }
  if (trimmed.startsWith('/') && !trimmed.startsWith('//')) {
    return true
  }
  try {
    const parsed = new URL(trimmed)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' || parsed.protocol === 'blob:'
  } catch {
    return false
  }
}

function NotificationMediaPreview({ url }: { url: string }) {
  if (!isSafeMediaURL(url)) {
    return null
  }
  const isVideo = url.endsWith('.mp4') || url.endsWith('.webm') || url.includes('video')
  if (isVideo) {
    return (
      <div className="mt-3 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-black shadow-inner">
        <video controls playsInline preload="metadata" className="max-h-[340px] w-full" src={url}>
          Your browser does not support the video tag.
        </video>
      </div>
    )
  }
  return (
    <div className="mt-3 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-neutral-900">
      <img className="max-h-[340px] w-full object-contain" src={url} alt="Deliverable preview" />
    </div>
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
  const navigate = useNavigate()
  const [activeTab, setActiveTab] = useState<'inbox' | 'activity'>('inbox')
  const [clearing, setClearing] = useState(false)
  const [expandedCardId, setExpandedCardId] = useState<string | null>(null)
  const [executingActionId, setExecutingActionId] = useState<string | null>(null)
  const [storageModalOpen, setStorageModalOpen] = useState(false)
  const [buckets, setBuckets] = useState<StorageBucket[]>([])
  const [loadingBuckets, setLoadingBuckets] = useState(false)
  const [scanningBuckets, setScanningBuckets] = useState(false)

  const loadBuckets = async () => {
    setLoadingBuckets(true)
    try {
      const list = await listStorageBuckets()
      setBuckets(list)
    } finally {
      setLoadingBuckets(false)
    }
  }

  useEffect(() => {
    if (open) {
      void loadBuckets()
    }
  }, [open])

  const handleScanAllBuckets = async () => {
    if (scanningBuckets || buckets.length === 0) return
    setScanningBuckets(true)
    try {
      for (const b of buckets) {
        if (b.enabled) {
          try {
            await scanStorageBucket(b.id)
          } catch (err) {
            console.warn('[storage] scan error for bucket', b.id, err)
          }
        }
      }
    } finally {
      setScanningBuckets(false)
    }
  }

  const inboxNotifications = useMemo(
    () => notifications.filter(isInboxNotification),
    [notifications]
  )

  const activityNotifications = useMemo(
    () => notifications.filter((record) => !isInboxNotification(record)),
    [notifications]
  )

  const displayedNotifications = activeTab === 'inbox' ? inboxNotifications : activityNotifications

  const unreadInboxCount = useMemo(
    () => inboxNotifications.filter((record) => record.status === 'active' && !record.readAt).length,
    [inboxNotifications]
  )

  const unreadActivityCount = useMemo(
    () => activityNotifications.filter((record) => record.status === 'active' && !record.readAt).length,
    [activityNotifications]
  )

  const activeNotifications = useMemo(
    () => displayedNotifications.filter((record) => record.status === 'active'),
    [displayedNotifications]
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

  const handleExecuteAction = async (
    record: DesktopNotificationCenterRecord,
    action: DesktopNotificationAction
  ) => {
    const executionKey = `${record.id}:${action.id}`
    setExecutingActionId(executionKey)
    try {
      if (
        action.actionType === 'route' ||
        action.id === 'review_cloud' ||
        action.endpoint?.startsWith('/settings')
      ) {
        onOpenChange(false)
        const tab = action.endpoint && action.endpoint.includes('tab=')
          ? new URL(action.endpoint, 'http://localhost').searchParams.get('tab') || 'cloud'
          : 'cloud'
        void navigate({ to: '/settings', search: { tab } })
        return
      }

      if (action.endpoint) {
        if (!isSafeNotificationActionEndpoint(action.endpoint)) {
          console.warn('[notifications] rejected unsafe action endpoint:', action.endpoint)
          return
        }
        await requestJson(action.endpoint, { method: 'POST' })
      }
      await onAcknowledge(record)

      if (action.id === 'accept_canonical' || (record.payload?.target_tab === 'cloud' && action.id.includes('accept'))) {
        onOpenChange(false)
        void navigate({ to: '/settings', search: { tab: 'cloud' } })
      }
    } catch (error) {
      console.error('[notifications] failed to execute action', error)
    } finally {
      setExecutingActionId(null)
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

        {/* Tab Switcher: AI Inbox vs Activity */}
        <div className="flex border-b border-[var(--app-border)] px-6 pt-2">
          <button
            type="button"
            className={cn(
              'flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm font-medium transition',
              activeTab === 'inbox'
                ? 'border-[var(--app-primary)] text-[var(--app-text)] font-semibold'
                : 'border-transparent text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            )}
            onClick={() => setActiveTab('inbox')}
          >
            <Sparkles size={16} className={activeTab === 'inbox' ? 'text-[var(--app-primary)]' : ''} />
            AI Inbox
            {unreadInboxCount > 0 ? (
              <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-[var(--app-primary)] px-1.5 text-[11px] font-bold text-white">
                {unreadInboxCount}
              </span>
            ) : inboxNotifications.length > 0 ? (
              <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-[var(--app-border)] px-1.5 text-[11px] font-medium text-[var(--app-text-muted)]">
                {inboxNotifications.length}
              </span>
            ) : null}
          </button>
          <button
            type="button"
            className={cn(
              'flex items-center gap-2 border-b-2 px-4 py-2.5 text-sm font-medium transition',
              activeTab === 'activity'
                ? 'border-[var(--app-primary)] text-[var(--app-text)] font-semibold'
                : 'border-transparent text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            )}
            onClick={() => setActiveTab('activity')}
          >
            <Bell size={16} />
            Activity
            {unreadActivityCount > 0 ? (
              <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-[var(--app-border-strong)] px-1.5 text-[11px] font-semibold text-[var(--app-text)]">
                {unreadActivityCount}
              </span>
            ) : null}
          </button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col px-6 pb-6 pt-4">
          {/* Storage Bucket Hooked Warning or Status Banner for AI Inbox */}
          {activeTab === 'inbox' && (
            <>
              {!loadingBuckets && buckets.length === 0 ? (
                <div className="mb-3 flex items-center justify-between rounded-2xl border border-amber-500/30 bg-amber-500/10 p-3.5 text-xs text-amber-200">
                  <div className="flex items-center gap-2.5">
                    <AlertTriangle size={18} className="shrink-0 text-amber-400" />
                    <div>
                      <div className="font-semibold text-amber-100">No Cloud Storage Bucket Connected</div>
                      <div className="text-[11px] text-amber-300/80">
                        Remote workers writing deliverables to S3/GCP cannot be discovered until a bucket is hooked up.
                      </div>
                    </div>
                  </div>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => {
                      onOpenChange(false)
                      void navigate({ to: '/settings', search: { tab: 'cloud' } })
                    }}
                    className="shrink-0 font-medium"
                  >
                    Configure in Cloud
                  </Button>
                </div>
              ) : buckets.length > 0 ? (
                <div className="mb-3 flex items-center justify-between rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-muted)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
                  <div className="flex items-center gap-2">
                    <Cloud size={14} className="text-[var(--app-primary)]" />
                    <span>
                      Cloud Storage Hub:{' '}
                      <span className="font-medium text-[var(--app-text)]">{buckets[0].name}</span>{' '}
                      ({buckets[0].provider.toUpperCase()})
                    </span>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={handleScanAllBuckets}
                      disabled={scanningBuckets}
                    >
                      <RefreshCw size={12} className={cn('mr-1', scanningBuckets && 'animate-spin')} />
                      {scanningBuckets ? 'Scanning…' : 'Scan for Deliverables'}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        onOpenChange(false)
                        void navigate({ to: '/settings', search: { tab: 'cloud' } })
                      }}
                    >
                      Cloud Settings
                    </Button>
                  </div>
                </div>
              ) : null}
            </>
          )}

          <div className="min-h-0 flex-1 space-y-3 overflow-y-auto pr-1">
            {loading ? (
              <Card className="p-4 text-sm text-[var(--app-text-muted)]">Loading notifications…</Card>
            ) : displayedNotifications.length === 0 ? (
              <Card className="flex flex-col items-center gap-3 p-8 text-center text-sm text-[var(--app-text-muted)]">
                {activeTab === 'inbox' ? (
                  <>
                    <Sparkles size={24} className="text-[var(--app-primary)]/60" />
                    <div>No AI inbox deliverables or requests yet.</div>
                    <div className="text-xs text-[var(--app-text-subtle)]">
                      When background agents or cloud workers produce content batches, storyboards, or request pairing, they appear here.
                    </div>
                  </>
                ) : (
                  <>
                    <BellOff size={24} className="text-[var(--app-text-subtle)]" />
                    <div>No system activity yet.</div>
                  </>
                )}
              </Card>
            ) : (
              displayedNotifications.map((record) => {
                const sessionLabel = notificationSessionLabel(record)
                const meta = notificationMeta(record)
                const isInbox = isInboxNotification(record)
                const isExpanded = expandedCardId === record.id
                const payload = record.payload || {}
                const mediaUrl = typeof payload.media_url === 'string' ? payload.media_url : null
                const threadPosts = Array.isArray(payload.thread)
                  ? payload.thread
                  : Array.isArray(payload.posts)
                    ? payload.posts
                    : null
                const actions = record.actions || []
                const mediaSha256 = typeof payload.media_sha256 === 'string'
                  ? payload.media_sha256
                  : typeof payload.sha256 === 'string'
                    ? payload.sha256
                    : null
                const telemetry = (payload.telemetry && typeof payload.telemetry === 'object') ? (payload.telemetry as Record<string, any>) : null
                const costUsd = typeof telemetry?.cost_usd === 'number' ? telemetry.cost_usd : undefined
                const thinkingTokens = typeof telemetry?.thinking_tokens === 'number' ? telemetry.thinking_tokens : undefined
                const totalTokens = typeof telemetry?.total_tokens === 'number' ? telemetry.total_tokens : undefined
                const computeDurationMs = typeof telemetry?.compute_duration_ms === 'number' ? telemetry.compute_duration_ms : undefined
                const modelName = typeof telemetry?.model === 'string' ? telemetry.model : undefined

                return (
                  <Card
                    key={record.id}
                    className={cn(
                      'space-y-3 p-4 transition-all',
                      !record.readAt && 'border-[var(--app-primary)]/40',
                      isInbox && !record.readAt && 'bg-[var(--app-surface-subtle)]'
                    )}
                  >
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div className="min-w-0 flex-1 space-y-1">
                        <div className="flex flex-wrap items-center gap-2">
                          {isInbox ? (
                            record.kind === 'ai_request' ? (
                              <Bot size={16} className="text-[var(--app-primary)]" />
                            ) : (
                              <Sparkles size={16} className="text-[var(--app-primary)]" />
                            )
                          ) : null}
                          <div className="font-medium text-[var(--app-text)]">{record.title}</div>
                          {isInbox ? (
                            <Badge tone={record.kind === 'ai_request' ? 'live' : 'warning'}>
                              {record.kind === 'ai_request'
                                ? record.payload?.target_tab === 'cloud' || record.payload?.bucket_id || (typeof record.title === 'string' && record.title.toLowerCase().includes('cloud'))
                                  ? 'Cloud Connection'
                                  : 'Pairing Request'
                                : 'AI Deliverable'}
                            </Badge>
                          ) : null}
                          {record.verified ? (
                            <Badge tone="live" title="Origin authenticated via scoped credentials">
                              Verified
                            </Badge>
                          ) : null}
                          <Badge tone={statusTone(record)}>{statusLabel(record)}</Badge>
                          {record.status === 'active' ? <Badge tone="live">Active</Badge> : <Badge tone="neutral">Resolved</Badge>}
                        </div>
                        {sessionLabel ? <div className="truncate text-sm font-medium text-[var(--app-text)]">{sessionLabel}</div> : null}
                        <div className="text-sm text-[var(--app-text-muted)]">{record.body || 'No details provided.'}</div>
                      </div>
                      <div className="flex items-center gap-2 text-xs text-[var(--app-text-subtle)]">
                        <Clock3 size={14} />
                        {formatRelativeTime(record.updatedAt || record.createdAt) || 'just now'}
                      </div>
                    </div>

                    {/* Granular Cost & Telemetry Badges */}
                    {telemetry ? (
                      <div className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs">
                        {costUsd !== undefined ? (
                          <span className="flex items-center gap-1 font-semibold text-emerald-400">
                            💳 {costUsd < 0.01 ? `$${costUsd.toFixed(4)}` : `$${costUsd.toFixed(2)}`}
                          </span>
                        ) : null}
                        {thinkingTokens ? (
                          <span className="flex items-center gap-1 font-medium text-[var(--app-primary)]">
                            🧠 {thinkingTokens.toLocaleString()} Thinking
                          </span>
                        ) : null}
                        {totalTokens ? (
                          <span className="text-[var(--app-text-muted)]">
                            📊 {totalTokens.toLocaleString()} Tokens
                          </span>
                        ) : null}
                        {computeDurationMs ? (
                          <span className="text-[var(--app-text-subtle)]">
                            ⏱ {(computeDurationMs / 1000).toFixed(1)}s Compute
                          </span>
                        ) : null}
                        {modelName ? (
                          <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 font-mono text-[10px] text-[var(--app-text-subtle)]">
                            {modelName}
                          </span>
                        ) : null}
                      </div>
                    ) : null}

                    {/* Rich Deliverable Media Preview */}
                    {mediaUrl ? <NotificationMediaPreview url={mediaUrl} /> : null}

                    {/* Cloud Storage Connection Proposal Preview */}
                    {payload.bucket_id || payload.target_tab === 'cloud' || (typeof record.title === 'string' && record.title.toLowerCase().includes('cloud connection')) ? (
                      <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-[var(--app-primary)]/40 bg-[var(--app-surface)] p-3 shadow-inner">
                        <div className="flex items-center gap-3">
                          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary)]/15 text-[var(--app-primary)]">
                            <Cloud size={20} />
                          </div>
                          <div className="min-w-0">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="font-mono text-sm font-semibold text-[var(--app-text)]">
                                {String(payload.bucket_name || 'Cloud Storage Bucket')}
                              </span>
                              {payload.provider ? (
                                <span className="rounded-md bg-[var(--app-primary)]/20 px-2 py-0.5 text-[11px] font-bold uppercase text-[var(--app-primary)]">
                                  {String(payload.provider)}
                                </span>
                              ) : null}
                              {payload.region ? (
                                <span className="font-mono text-xs text-[var(--app-text-subtle)]">
                                  ({String(payload.region)})
                                </span>
                              ) : null}
                            </div>
                            <div className="mt-0.5 text-xs text-[var(--app-text-muted)]">
                              {payload.proposal_reason ? String(payload.proposal_reason) : 'Proposed canonical cloud storage connection for autonomous workers'}
                            </div>
                          </div>
                        </div>
                        <Button
                          variant="secondary"
                          size="sm"
                          className="gap-1.5 shrink-0"
                          onClick={() => {
                            onOpenChange(false)
                            void navigate({ to: '/settings', search: { tab: 'cloud' } })
                          }}
                        >
                          <ExternalLink size={14} /> Open Cloud Settings
                        </Button>
                      </div>
                    ) : null}

                    {/* Draft Thread / Posts Preview */}
                    {threadPosts && threadPosts.length > 0 ? (
                      <div className="space-y-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                        <div className="flex items-center justify-between">
                          <span className="text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
                            Drafted Content ({threadPosts.length} items)
                          </span>
                          <button
                            type="button"
                            className="flex items-center gap-1 text-xs text-[var(--app-primary)] hover:underline"
                            onClick={() => setExpandedCardId(isExpanded ? null : record.id)}
                          >
                            {isExpanded ? <>Collapse <ChevronUp size={12} /></> : <>Expand <ChevronDown size={12} /></>}
                          </button>
                        </div>
                        {isExpanded ? (
                          <div className="space-y-2 pt-1">
                            {threadPosts.map((post, idx) => (
                              <div
                                key={idx}
                                className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2.5 text-xs text-[var(--app-text)]"
                              >
                                {typeof post === 'string'
                                  ? post
                                  : typeof post === 'object' && post !== null
                                    ? String((post as any).text || (post as any).content || JSON.stringify(post))
                                    : String(post)}
                              </div>
                            ))}
                          </div>
                        ) : (
                          <div className="truncate text-xs text-[var(--app-text-subtle)]">
                            {typeof threadPosts[0] === 'string'
                              ? threadPosts[0]
                              : typeof threadPosts[0] === 'object' && threadPosts[0] !== null
                                ? String((threadPosts[0] as any).text || (threadPosts[0] as any).content || '')
                                : ''}
                          </div>
                        )}
                      </div>
                    ) : null}

                    <div className="flex flex-wrap gap-2 text-xs text-[var(--app-text-muted)]">
                      <span>Category: {record.category}</span>
                      {meta.map((item) => <span key={item}>{item}</span>)}
                      {record.sessionId ? <span title={record.sessionId}>Session: {shortID(record.sessionId)}</span> : null}
                      {mediaSha256 ? (
                        <span className="font-mono text-xs text-[var(--app-text-subtle)]" title={`Content Hash: ${mediaSha256}`}>
                          SHA256: {mediaSha256.slice(0, 8)}…
                        </span>
                      ) : null}
                    </div>

                    {/* Actionable Buttons Bar */}
                    {record.status === 'active' || record.actionURL || actions.length > 0 ? (
                      <div className="flex flex-wrap items-center gap-2 pt-1 text-xs text-[var(--app-text-muted)]">
                        {/* Custom Injected Actions (Approve, Request Changes, Accept Pair) */}
                        {actions.map((act) => {
                          const isExecuting = executingActionId === `${record.id}:${act.id}`
                          const variant = act.variant === 'primary' ? 'default' : act.variant === 'danger' ? 'destructive' : 'secondary'
                          return (
                            <Button
                              key={act.id}
                              variant={variant as any}
                              size="sm"
                              disabled={isExecuting}
                              onClick={() => void handleExecuteAction(record, act)}
                            >
                              {isExecuting ? <Loader2 size={14} className="animate-spin" /> : null}
                              {act.label}
                            </Button>
                          )
                        })}

                        {record.actionURL && isSafeActionURL(record.actionURL) ? (
                          record.actionURL.startsWith('/settings') ? (
                            <Button
                              variant="outline"
                              size="sm"
                              className="gap-1.5"
                              onClick={() => {
                                onOpenChange(false)
                                const url = new URL(record.actionURL!, 'http://localhost')
                                const tab = url.searchParams.get('tab') || 'cloud'
                                void navigate({ to: '/settings', search: { tab } })
                              }}
                            >
                              <ExternalLink size={14} /> Open Cloud Settings
                            </Button>
                          ) : (
                            <a
                              className="inline-flex min-h-9 min-w-[140px] items-center justify-center gap-2 rounded-xl border border-[var(--app-primary)] px-3 text-sm font-medium text-[var(--app-primary)] transition hover:bg-[color-mix(in_oklab,var(--app-primary)_10%,transparent)] hover:text-[var(--app-primary-hover)]"
                              href={record.actionURL}
                              target="_blank"
                              rel="noreferrer"
                              title={record.actionURL.includes('tab=deliverables') ? 'Open deliverable in review inbox' : 'Open session in a new tab'}
                            >
                              <ExternalLink size={14} /> {record.actionURL.includes('tab=deliverables') || record.actionURL.includes('/workers') ? 'Review deliverable' : 'Go to session'}
                            </a>
                          )
                        ) : null}

                        {record.status === 'active' && !record.readAt ? (
                          <Button variant="secondary" size="sm" className="min-w-[100px] justify-center" onClick={() => void onMarkRead(record)}>
                            <CheckCheck size={14} /> Mark read
                          </Button>
                        ) : null}
                        {record.status === 'active' && !record.ackedAt ? (
                          <Button variant="secondary" size="sm" className="min-w-[100px] justify-center" onClick={() => void onAcknowledge(record)}>
                            Acknowledge
                          </Button>
                        ) : null}
                        {record.status === 'active' && !record.mutedAt ? (
                          <Button variant="ghost" size="sm" className="min-w-[80px] justify-center" onClick={() => void onMute(record)}>
                            Mute
                          </Button>
                        ) : null}
                      </div>
                    ) : null}
                  </Card>
                )
              })
            )}

            {!loading && displayedNotifications.length > 0 && activeNotifications.length === 0 ? (
              <Card className="p-3 text-xs text-[var(--app-text-muted)]">All notifications in this view are resolved.</Card>
            ) : null}
          </div>
        </div>
      </DialogPanel>
      <DesktopStorageBucketsModal
        open={storageModalOpen}
        onOpenChange={setStorageModalOpen}
        onBucketsUpdated={loadBuckets}
      />
    </Dialog>
  )
}
