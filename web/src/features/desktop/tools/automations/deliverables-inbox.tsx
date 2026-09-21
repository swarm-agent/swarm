import { useEffect, useMemo, useState } from 'react'
import {
  AlertTriangle,
  Bot,
  CheckCircle2,
  Clock,
  ExternalLink,
  FileText,
  Filter,
  Inbox,
  LoaderCircle,
  RefreshCcw,
  Send,
  Share2,
  Sparkles,
  Trash2,
  XCircle,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import {
  approveDeliverable,
  dismissDeliverable,
  deleteDeliverable,
  fetchDeliverables,
  type DeliverableRecord,
} from '../../state/desktop-deliverables-api'

export interface DeliverablesInboxProps {
  workspaceId: string
  workspacePath?: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  selectedWorkerId?: string
}

export function DeliverablesInbox({
  workspaceId,
  workspaceSlug,
  onOpenSession,
  selectedWorkerId,
}: DeliverablesInboxProps) {
  const [deliverables, setDeliverables] = useState<DeliverableRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionLoadingId, setActionLoadingId] = useState<string | null>(null)

  // Filters
  const [statusFilter, setStatusFilter] = useState<'pending_review' | 'approved' | 'dismissed' | 'all'>('pending_review')
  const [kindFilter, setKindFilter] = useState<'all' | 'social_post' | 'alert' | 'report'>('all')
  const [workerFilter, setWorkerFilter] = useState<string>(selectedWorkerId || 'all')

  const loadItems = async () => {
    setLoading(true)
    setError(null)
    try {
      const items = await fetchDeliverables({
        workspace_id: workspaceId || undefined,
        limit: 100,
      })
      setDeliverables(items)
    } catch (err: any) {
      setError(err?.message || 'Failed to load deliverables')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void loadItems()
  }, [workspaceId])

  // Aggregate worker IDs for the filter dropdown
  const uniqueWorkers = useMemo(() => {
    const set = new Set<string>()
    for (const d of deliverables) {
      if (d.worker_id) set.add(d.worker_id)
    }
    return Array.from(set)
  }, [deliverables])

  // Count pending review items
  const pendingCount = useMemo(() => {
    return deliverables.filter((d) => d.status === 'pending_review').length
  }, [deliverables])

  const filteredDeliverables = useMemo(() => {
    return deliverables.filter((d) => {
      if (statusFilter !== 'all' && d.status !== statusFilter) return false
      if (kindFilter !== 'all' && d.kind !== kindFilter) return false
      if (workerFilter !== 'all' && d.worker_id !== workerFilter) return false
      return true
    })
  }, [deliverables, statusFilter, kindFilter, workerFilter])

  const handleApprove = async (id: string) => {
    setActionLoadingId(id)
    try {
      const res = await approveDeliverable(id)
      setDeliverables((prev) =>
        prev.map((d) => (d.id === id ? res.deliverable : d))
      )
    } catch (err: any) {
      alert(`Approval failed: ${err?.message || 'unknown error'}`)
    } finally {
      setActionLoadingId(null)
    }
  }

  const handleDismiss = async (id: string) => {
    setActionLoadingId(id)
    try {
      const updated = await dismissDeliverable(id)
      setDeliverables((prev) =>
        prev.map((d) => (d.id === id ? updated : d))
      )
    } catch (err: any) {
      alert(`Dismiss failed: ${err?.message || 'unknown error'}`)
    } finally {
      setActionLoadingId(null)
    }
  }

  const handleDelete = async (id: string) => {
    if (!confirm('Permanently remove this deliverable?')) return
    setActionLoadingId(id)
    try {
      await deleteDeliverable(id)
      setDeliverables((prev) => prev.filter((d) => d.id !== id))
    } catch (err: any) {
      alert(`Delete failed: ${err?.message || 'unknown error'}`)
    } finally {
      setActionLoadingId(null)
    }
  }

  return (
    <div className="space-y-6" data-testid="deliverables-inbox">
      {/* Header Banner */}
      <div className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5 shadow-xs">
        <div className="flex items-center gap-3.5">
          <div className="flex h-11 w-11 items-center justify-center rounded-xl bg-[var(--app-primary)] text-white shadow-xs">
            <Inbox className="h-6 w-6" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="text-base font-semibold text-[var(--app-text)]">
                Agent Mailbox & Review Inbox
              </h2>
              {pendingCount > 0 && (
                <span
                  data-testid="pending-badge"
                  className="rounded-full bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-xs font-semibold text-[var(--app-primary)]"
                >
                  {pendingCount} Pending Review
                </span>
              )}
            </div>
            <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
              Review and approve deliverables, social post batches, and alerts sent by background workers.
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void loadItems()}
            disabled={loading}
            className="h-8 gap-1.5 text-xs"
          >
            <RefreshCcw className={cn('h-3.5 w-3.5', loading && 'animate-spin')} />
            Refresh
          </Button>
        </div>
      </div>

      {/* Filter Controls Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)]/60 pb-3">
        {/* Status Tabs */}
        <div className="flex items-center gap-1.5">
          {(
            [
              { id: 'pending_review', label: 'Pending Review' },
              { id: 'approved', label: 'Approved & Published' },
              { id: 'dismissed', label: 'Dismissed' },
              { id: 'all', label: 'All Items' },
            ] as const
          ).map((tab) => (
            <button
              key={tab.id}
              onClick={() => setStatusFilter(tab.id)}
              className={cn(
                'rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
                statusFilter === tab.id
                  ? 'bg-[var(--app-primary)] text-white'
                  : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'
              )}
            >
              {tab.label}
              {tab.id === 'pending_review' && pendingCount > 0 && (
                <span className="ml-1.5 rounded-full bg-white/20 px-1.5 py-0.2 text-[10px] font-bold">
                  {pendingCount}
                </span>
              )}
            </button>
          ))}
        </div>

        {/* Kind and Worker Dropdowns */}
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1.5 text-xs text-[var(--app-text-muted)]">
            <Filter className="h-3.5 w-3.5" />
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as any)}
              className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] outline-hidden"
            >
              <option value="all">All Types</option>
              <option value="social_post">Social Posts / Tweets</option>
              <option value="alert">Alerts</option>
              <option value="report">Reports</option>
            </select>
          </div>

          {uniqueWorkers.length > 0 && (
            <select
              value={workerFilter}
              onChange={(e) => setWorkerFilter(e.target.value)}
              className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] outline-hidden"
            >
              <option value="all">All Workers</option>
              {uniqueWorkers.map((w) => (
                <option key={w} value={w}>
                  {w}
                </option>
              ))}
            </select>
          )}
        </div>
      </div>

      {/* Deliverables List */}
      {loading ? (
        <div className="flex flex-col items-center justify-center py-16 text-center text-xs text-[var(--app-text-muted)]">
          <LoaderCircle className="h-6 w-6 animate-spin text-[var(--app-primary)] mb-2" />
          Loading mailbox deliverables…
        </div>
      ) : error ? (
        <div className="rounded-xl border border-red-500/20 bg-red-500/10 p-4 text-xs text-red-600 dark:text-red-400">
          {error}
        </div>
      ) : filteredDeliverables.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-2xl border border-dashed border-[var(--app-border)] py-16 text-center">
          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-[var(--app-surface)] text-[var(--app-text-muted)] mb-3">
            <CheckCircle2 className="h-6 w-6 text-emerald-500" />
          </div>
          <h3 className="text-sm font-semibold text-[var(--app-text)]">Mailbox Inbox Clear</h3>
          <p className="text-xs text-[var(--app-text-muted)] mt-1 max-w-sm">
            {statusFilter === 'pending_review'
              ? 'There are no pending deliverables awaiting your review right now.'
              : 'No deliverables match the selected filters.'}
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          {filteredDeliverables.map((item) => (
            <DeliverableCard
              key={item.id}
              deliverable={item}
              onApprove={() => handleApprove(item.id)}
              onDismiss={() => handleDismiss(item.id)}
              onDelete={() => handleDelete(item.id)}
              onOpenSession={onOpenSession}
              isLoading={actionLoadingId === item.id}
            />
          ))}
        </div>
      )}
    </div>
  )
}

interface DeliverableCardProps {
  deliverable: DeliverableRecord
  onApprove: () => void
  onDismiss: () => void
  onDelete: () => void
  onOpenSession?: (id: string) => void
  isLoading?: boolean
}

export function DeliverableCard({
  deliverable,
  onApprove,
  onDismiss,
  onDelete,
  onOpenSession,
  isLoading,
}: DeliverableCardProps) {
  const isSocial = deliverable.kind === 'social_post'
  const isAlert = deliverable.kind === 'alert'
  const isPending = deliverable.status === 'pending_review'
  const isApproved = deliverable.status === 'approved' || deliverable.status === 'published'
  const isDismissed = deliverable.status === 'dismissed'

  // Extract posts from payload if social post
  const posts: Array<{ text: string; media_urls?: string[] }> = useMemo(() => {
    if (!deliverable.payload) return []
    if (Array.isArray(deliverable.payload.posts)) {
      return deliverable.payload.posts.map((p: any) =>
        typeof p === 'string' ? { text: p } : { text: p.text || '', media_urls: p.media_urls }
      )
    }
    if (Array.isArray(deliverable.payload.tweets)) {
      return deliverable.payload.tweets.map((t: any) =>
        typeof t === 'string' ? { text: t } : { text: t.text || '' }
      )
    }
    return []
  }, [deliverable.payload])

  const actionContract = deliverable.action_contract
  const isXPostAction = actionContract?.action === 'publish_x_post'

  return (
    <div
      data-testid="deliverable-card"
      className={cn(
        'rounded-2xl border bg-[var(--app-surface)] p-5 shadow-xs transition-all space-y-4',
        isPending
          ? 'border-[var(--app-primary-border)]/60 bg-[var(--app-surface)]'
          : isAlert
          ? 'border-amber-500/30'
          : 'border-[var(--app-border)]/60 opacity-80'
      )}
    >
      {/* Card Header */}
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-[var(--app-border)]/50 pb-3">
        <div className="flex items-center gap-3">
          <div
            className={cn(
              'flex h-9 w-9 items-center justify-center rounded-xl text-white shadow-xs',
              isSocial
                ? 'bg-blue-500'
                : isAlert
                ? 'bg-amber-500'
                : 'bg-[var(--app-primary)]'
            )}
          >
            {isSocial ? (
              <Share2 className="h-4 w-4" />
            ) : isAlert ? (
              <AlertTriangle className="h-4 w-4" />
            ) : (
              <FileText className="h-4 w-4" />
            )}
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold text-[var(--app-text)]">{deliverable.title}</h3>
              <span
                className={cn(
                  'rounded-md px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider',
                  isPending
                    ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)]'
                    : isApproved
                    ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'
                    : 'bg-[var(--app-surface-hover)] text-[var(--app-text-muted)]'
                )}
              >
                {deliverable.status.replace('_', ' ')}
              </span>
            </div>
            <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)] mt-0.5">
              {deliverable.worker_id && (
                <span className="flex items-center gap-1 font-medium text-[var(--app-text)]">
                  <Bot className="h-3 w-3" />
                  {deliverable.worker_id}
                </span>
              )}
              <span>•</span>
              <span className="flex items-center gap-1">
                <Clock className="h-3 w-3" />
                {new Date(deliverable.created_at).toLocaleTimeString([], {
                  hour: '2-digit',
                  minute: '2-digit',
                })}
              </span>
            </div>
          </div>
        </div>

        {/* Top Right Quick Actions */}
        <div className="flex items-center gap-1.5">
          {deliverable.session_id && onOpenSession && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => onOpenSession(deliverable.session_id!)}
              className="h-7 gap-1 text-[11px] text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
            >
              <ExternalLink className="h-3 w-3" />
              Session
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={onDelete}
            disabled={isLoading}
            className="h-7 w-7 p-0 text-[var(--app-text-muted)] hover:text-red-500"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>

      {/* Summary (if present) */}
      {deliverable.summary && (
        <p className="text-xs text-[var(--app-text)] leading-relaxed">
          {deliverable.summary}
        </p>
      )}

      {/* Specific Kind Views */}
      {/* 1. Social Post Format: Premade Tweets Form */}
      {isSocial && posts.length > 0 && (
        <div className="space-y-2.5 rounded-xl border border-[var(--app-border)]/80 bg-[var(--app-bg-alt)]/50 p-3.5">
          <div className="flex items-center justify-between text-[11px] font-semibold text-[var(--app-text-muted)]">
            <span>PREMADE THREAD ({posts.length} {posts.length === 1 ? 'POST' : 'POSTS'})</span>
            {isXPostAction && (
              <span className="text-blue-500 flex items-center gap-1">
                Target: X (Twitter)
              </span>
            )}
          </div>

          <div className="space-y-3">
            {posts.map((post, idx) => (
              <div
                key={idx}
                className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 shadow-2xs space-y-2"
              >
                <div className="flex items-center justify-between text-[10px] text-[var(--app-text-muted)]">
                  <span className="font-bold text-[var(--app-primary)]">
                    Tweet {idx + 1} of {posts.length}
                  </span>
                  <span>{post.text.length} / 280 chars</span>
                </div>
                <p className="text-xs text-[var(--app-text)] whitespace-pre-wrap leading-relaxed">
                  {post.text}
                </p>
                {post.media_urls && post.media_urls.length > 0 && (
                  <div className="flex flex-wrap gap-2 pt-1">
                    {post.media_urls.map((url, i) => (
                      <span
                        key={i}
                        className="rounded-md bg-[var(--app-surface-hover)] px-2 py-0.5 text-[10px] text-[var(--app-text-muted)] font-mono"
                      >
                        Media: {url}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* 2. Media Artifact References (if attached) */}
      {deliverable.media_refs && deliverable.media_refs.length > 0 && (
        <div className="space-y-1.5">
          <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
            Attached Deliverable Artifacts ({deliverable.media_refs.length})
          </div>
          <div className="flex flex-wrap gap-2">
            {deliverable.media_refs.map((m, i) => (
              <div
                key={i}
                className="flex items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1 text-xs text-[var(--app-text)]"
              >
                <FileText className="h-3.5 w-3.5 text-[var(--app-primary)]" />
                <span>{m.label || m.filename || m.path || 'Artifact'}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Publication / Result Receipt (if approved/published) */}
      {isApproved && deliverable.action_result && (
        <div className="rounded-xl border border-emerald-500/20 bg-emerald-500/5 p-3 text-xs space-y-1">
          <div className="flex items-center gap-1.5 font-semibold text-emerald-600 dark:text-emerald-400">
            <CheckCircle2 className="h-4 w-4" />
            <span>Publication Receipt</span>
          </div>
          <p className="text-[11px] text-[var(--app-text-muted)]">
            Target: {String(deliverable.action_result.published_to || 'Executed Action')} • Status:{' '}
            {String(deliverable.action_result.status || 'Success')}
          </p>
        </div>
      )}

      {/* Action Footer */}
      <div className="flex flex-wrap items-center justify-between gap-3 pt-2">
        <div className="text-[11px] text-[var(--app-text-muted)]">
          {actionContract && (
            <span>
              Action Contract:{' '}
              <code className="rounded-sm bg-[var(--app-surface-hover)] px-1 font-mono text-[10px]">
                {actionContract.action}
              </code>
            </span>
          )}
        </div>

        {isPending && (
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={onDismiss}
              disabled={isLoading}
              className="h-8 text-xs gap-1"
              data-testid="dismiss-btn"
            >
              <XCircle className="h-3.5 w-3.5 text-[var(--app-text-muted)]" />
              Dismiss
            </Button>

            <Button
              variant="default"
              size="sm"
              onClick={onApprove}
              disabled={isLoading}
              className="h-8 text-xs gap-1.5 bg-blue-600 hover:bg-blue-700 text-white shadow-xs"
              data-testid="approve-publish-btn"
            >
              {isLoading ? (
                <LoaderCircle className="h-3.5 w-3.5 animate-spin" />
              ) : isXPostAction ? (
                <Send className="h-3.5 w-3.5" />
              ) : (
                <Sparkles className="h-3.5 w-3.5" />
              )}
              {isXPostAction ? 'Approve & Publish to X' : 'Approve & Execute Action'}
            </Button>
          </div>
        )}

        {isDismissed && (
          <span className="text-xs text-[var(--app-text-muted)] italic">
            Dismissed without action
          </span>
        )}
      </div>
    </div>
  )
}
