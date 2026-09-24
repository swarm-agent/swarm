import { useEffect, useMemo, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  Bot,
  Calendar,
  Check,
  CheckCircle2,
  Clock,
  Code2,
  Copy,
  ExternalLink,
  FileText,
  Film,
  Filter,
  GitPullRequest,
  Image as ImageIcon,
  Inbox,
  LoaderCircle,
  MessageSquareReply,
  RefreshCcw,
  Send,
  Share2,
  Sparkles,
  Tag,
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
  requestDeliverableChanges,
  type DeliverableRecord,
} from '../../state/desktop-deliverables-api'

export interface DeliverablesInboxProps {
  workspaceId: string
  workspacePath?: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  selectedWorkerId?: string
  highlightId?: string
}

interface DateGroup {
  key: string
  label: string
  items: DeliverableRecord[]
  pendingCount: number
}

function formatDateGroupKey(timestamp: number): { key: string; label: string } {
  const d = new Date(timestamp)
  const now = new Date()

  const isToday =
    d.getDate() === now.getDate() &&
    d.getMonth() === now.getMonth() &&
    d.getFullYear() === now.getFullYear()

  if (isToday) {
    return { key: 'today', label: 'Today' }
  }

  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const isYesterday =
    d.getDate() === yesterday.getDate() &&
    d.getMonth() === yesterday.getMonth() &&
    d.getFullYear() === yesterday.getFullYear()

  if (isYesterday) {
    return { key: 'yesterday', label: 'Yesterday' }
  }

  const formatted = d.toLocaleDateString(undefined, {
    month: 'long',
    day: 'numeric',
    year: 'numeric',
  })
  return { key: `${d.getFullYear()}-${d.getMonth() + 1}-${d.getDate()}`, label: formatted }
}

export function DeliverablesInbox({
  workspaceId,
  workspaceSlug: _workspaceSlug,
  onOpenSession,
  selectedWorkerId,
  highlightId,
}: DeliverablesInboxProps) {
  const [deliverables, setDeliverables] = useState<DeliverableRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionLoadingId, setActionLoadingId] = useState<string | null>(null)

  // Filters
  const [workspaceFilter, setWorkspaceFilter] = useState<'all' | 'current'>('all')
  const [statusFilter, setStatusFilter] = useState<
    'pending_review' | 'needs_revision' | 'approved' | 'dismissed' | 'all'
  >('pending_review')
  const [kindFilter, setKindFilter] = useState<
    'all' | 'social_post' | 'media' | 'report' | 'code_patch' | 'alert'
  >('all')
  const [workerFilter, setWorkerFilter] = useState<string>(selectedWorkerId || 'all')

  const loadItems = async () => {
    setLoading(true)
    setError(null)
    try {
      const items = await fetchDeliverables({
        workspace_id: workspaceFilter === 'current' ? (workspaceId || undefined) : undefined,
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
  }, [workspaceId, workspaceFilter])

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
    return deliverables.filter((d) => d.status === 'pending_review' || d.status === 'needs_revision').length
  }, [deliverables])

  const filteredDeliverables = useMemo(() => {
    return deliverables.filter((d) => {
      if (statusFilter !== 'all' && d.status !== statusFilter) return false
      if (kindFilter !== 'all' && d.kind !== kindFilter) return false
      if (workerFilter !== 'all' && d.worker_id !== workerFilter) return false
      return true
    })
  }, [deliverables, statusFilter, kindFilter, workerFilter])

  // Group filtered deliverables by calendar day
  const dateGroups = useMemo<DateGroup[]>(() => {
    const groupsMap = new Map<string, { label: string; items: DeliverableRecord[]; order: number }>()

    for (const item of filteredDeliverables) {
      const { key, label } = formatDateGroupKey(item.created_at || Date.now())
      if (!groupsMap.has(key)) {
        groupsMap.set(key, { label, items: [], order: item.created_at || 0 })
      }
      groupsMap.get(key)!.items.push(item)
    }

    const sortedGroups = Array.from(groupsMap.entries()).map(([key, data]) => ({
      key,
      label: data.label,
      items: data.items,
      pendingCount: data.items.filter(
        (d) => d.status === 'pending_review' || d.status === 'needs_revision'
      ).length,
    }))

    return sortedGroups
  }, [filteredDeliverables])

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

  const handleRequestChanges = async (id: string, notes: string, tags: string[]) => {
    setActionLoadingId(id)
    try {
      const updated = await requestDeliverableChanges(id, { notes, tags })
      setDeliverables((prev) =>
        prev.map((d) => (d.id === id ? updated : d))
      )
    } catch (err: any) {
      alert(`Request changes failed: ${err?.message || 'unknown error'}`)
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
                  {pendingCount} Awaiting Review
                </span>
              )}
            </div>
            <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
              Review and approve deliverables, social posts, media renders, reports, and code diffs sent by background workers.
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void loadItems()}
            disabled={loading}
            className="h-8 gap-1.5 text-xs rounded-xl"
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
              { id: 'needs_revision', label: 'Needs Revision' },
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

        {/* Kind, Worker, and Workspace Dropdowns */}
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-1.5 text-xs text-[var(--app-text-muted)]">
            <select
              data-testid="deliverables-workspace-filter"
              value={workspaceFilter}
              onChange={(e) => setWorkspaceFilter(e.target.value as 'all' | 'current')}
              className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] outline-hidden font-medium"
            >
              <option value="all">All Workspaces</option>
              <option value="current">Current Workspace</option>
            </select>
          </div>

          <div className="flex items-center gap-1.5 text-xs text-[var(--app-text-muted)]">
            <Filter className="h-3.5 w-3.5" />
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as any)}
              className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] outline-hidden"
            >
              <option value="all">All Deliverable Types</option>
              <option value="social_post">Social Posts / Tweets</option>
              <option value="media">Media / Visuals / Video</option>
              <option value="report">Reports & Summaries</option>
              <option value="code_patch">Code Patches & Diffs</option>
              <option value="alert">Alerts</option>
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

      {/* Deliverables Calendar-Day Grouped Feed */}
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
        <div className="space-y-8">
          {dateGroups.map((group) => (
            <div key={group.key} className="space-y-4">
              {/* Calendar Day Header */}
              <div className="flex items-center justify-between border-b border-[var(--app-border)]/50 pb-2">
                <div className="flex items-center gap-2">
                  <Calendar className="h-4 w-4 text-[var(--app-primary)]" />
                  <span className="text-xs font-bold uppercase tracking-wider text-[var(--app-text)]">
                    {group.label}
                  </span>
                  <span className="rounded-full bg-[var(--app-surface-hover)] px-2 py-0.5 text-[10px] font-semibold text-[var(--app-text-muted)]">
                    {group.items.length} {group.items.length === 1 ? 'item' : 'items'}
                  </span>
                </div>
                {group.pendingCount > 0 && (
                  <span className="rounded-md bg-amber-500/10 px-2 py-0.5 text-[10px] font-semibold text-amber-600 dark:text-amber-400">
                    {group.pendingCount} ready for review
                  </span>
                )}
              </div>

              {/* Day's Deliverable Cards */}
              <div className="space-y-4">
                {group.items.map((item) => (
                  <DeliverableCard
                    key={item.id}
                    deliverable={item}
                    isHighlighted={highlightId === item.id}
                    onApprove={() => handleApprove(item.id)}
                    onDismiss={() => handleDismiss(item.id)}
                    onRequestChanges={(notes, tags) => handleRequestChanges(item.id, notes, tags)}
                    onDelete={() => handleDelete(item.id)}
                    onOpenSession={onOpenSession}
                    isLoading={actionLoadingId === item.id}
                  />
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

interface DeliverableCardProps {
  deliverable: DeliverableRecord
  isHighlighted?: boolean
  onApprove: () => void
  onDismiss: () => void
  onRequestChanges: (notes: string, tags: string[]) => void
  onDelete: () => void
  onOpenSession?: (id: string) => void
  isLoading?: boolean
}

export function DeliverableCard({
  deliverable,
  isHighlighted,
  onApprove,
  onDismiss,
  onRequestChanges,
  onDelete,
  onOpenSession,
  isLoading,
}: DeliverableCardProps) {
  const isSocial = deliverable.kind === 'social_post'
  const isMedia = deliverable.kind === 'media' || deliverable.kind === 'media_bundle'
  const isReport = deliverable.kind === 'report'
  const isCode = deliverable.kind === 'code_patch' || deliverable.kind === 'pr_patch'
  const isAlert = deliverable.kind === 'alert'

  const isPending = deliverable.status === 'pending_review'
  const isNeedsRevision = deliverable.status === 'needs_revision'
  const isApproved = deliverable.status === 'approved' || deliverable.status === 'published'
  const isDismissed = deliverable.status === 'dismissed'

  // Request Changes Dialog State
  const [showReviseForm, setShowReviseForm] = useState(false)
  const [revisionNotes, setRevisionNotes] = useState('')
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  const [copied, setCopied] = useState(false)

  const PRESET_TAGS = ['Tone', 'Length', 'Media', 'Formatting', 'Code', 'Accuracy']

  const toggleTag = (tag: string) => {
    setSelectedTags((prev) =>
      prev.includes(tag) ? prev.filter((t) => t !== tag) : [...prev, tag]
    )
  }

  const submitRevision = () => {
    if (!revisionNotes.trim() && selectedTags.length === 0) {
      alert('Please provide feedback notes or select tags to guide the worker.')
      return
    }
    onRequestChanges(revisionNotes, selectedTags)
    setShowReviseForm(false)
    setRevisionNotes('')
    setSelectedTags([])
  }

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
        isHighlighted && 'ring-2 ring-[var(--app-primary)] border-[var(--app-primary)]',
        isPending
          ? 'border-[var(--app-primary-border)]/60 bg-[var(--app-surface)]'
          : isNeedsRevision
          ? 'border-amber-500/40 bg-amber-500/5'
          : isAlert
          ? 'border-amber-500/30'
          : 'border-[var(--app-border)]/60 opacity-90'
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
                : isMedia
                ? 'bg-purple-500'
                : isReport
                ? 'bg-emerald-500'
                : isCode
                ? 'bg-indigo-500'
                : isAlert
                ? 'bg-amber-500'
                : 'bg-[var(--app-primary)]'
            )}
          >
            {isSocial ? (
              <Share2 className="h-4 w-4" />
            ) : isMedia ? (
              <ImageIcon className="h-4 w-4" />
            ) : isReport ? (
              <FileText className="h-4 w-4" />
            ) : isCode ? (
              <Code2 className="h-4 w-4" />
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
                    : isNeedsRevision
                    ? 'bg-amber-500/20 text-amber-700 dark:text-amber-400 font-bold'
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
              {deliverable.workspace_path && (
                <>
                  <span>•</span>
                  <span className="rounded-md bg-[var(--app-surface-hover)] px-1.5 py-0.5 text-[10px] font-medium text-[var(--app-text-muted)] border border-[var(--app-border)]/60">
                    {deliverable.workspace_path.split('/').filter(Boolean).pop()}
                  </span>
                </>
              )}
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

      {/* Revision Feedback Banner (if changes were requested) */}
      {deliverable.revision_feedback && (
        <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-3.5 text-xs space-y-2">
          <div className="flex items-center justify-between text-[11px] font-semibold text-amber-700 dark:text-amber-400">
            <span className="flex items-center gap-1.5">
              <MessageSquareReply className="h-3.5 w-3.5" />
              Awaiting Worker Revision
            </span>
            <span>
              {new Date(deliverable.revision_feedback.requested_at).toLocaleTimeString([], {
                hour: '2-digit',
                minute: '2-digit',
              })}
            </span>
          </div>
          {deliverable.revision_feedback.notes && (
            <p className="text-xs font-medium text-[var(--app-text)] bg-[var(--app-surface)] p-2.5 rounded-lg border border-amber-500/20">
              "{deliverable.revision_feedback.notes}"
            </p>
          )}
          {deliverable.revision_feedback.tags && deliverable.revision_feedback.tags.length > 0 && (
            <div className="flex flex-wrap gap-1.5 pt-1">
              {deliverable.revision_feedback.tags.map((tag) => (
                <span
                  key={tag}
                  className="rounded-md bg-amber-500/20 px-2 py-0.5 text-[10px] font-semibold text-amber-800 dark:text-amber-300"
                >
                  #{tag}
                </span>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Universal Kind Views */}

      {/* 1. Social Post Format: Premade Tweets Thread */}
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

      {/* 2. Media Format: Visual Gallery & Video Player */}
      {isMedia && deliverable.payload && (
        <div className="space-y-3 rounded-xl border border-[var(--app-border)]/80 bg-[var(--app-bg-alt)]/50 p-3.5">
          <div className="text-[11px] font-semibold text-[var(--app-text-muted)] flex items-center gap-1.5">
            <Film className="h-3.5 w-3.5 text-purple-500" />
            <span>MEDIA DELIVERABLE ASSETS</span>
          </div>

          {/* Video Preview if video URL present */}
          {typeof deliverable.payload.video_url === 'string' && (
            <div className="overflow-hidden rounded-xl bg-black border border-[var(--app-border)]">
              <video
                controls
                src={deliverable.payload.video_url}
                className="max-h-72 w-full object-contain"
              />
            </div>
          )}

          {/* Image Preview if image URL present */}
          {typeof deliverable.payload.image_url === 'string' && (
            <div className="overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
              <img
                src={deliverable.payload.image_url}
                alt={deliverable.title}
                className="max-h-80 w-full object-contain"
              />
            </div>
          )}

          {/* Audio Preview if audio URL present */}
          {typeof deliverable.payload.audio_url === 'string' && (
            <div className="p-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
              <audio controls src={deliverable.payload.audio_url} className="w-full" />
            </div>
          )}
        </div>
      )}

      {/* 3. Report Format: Rich Document View */}
      {isReport && deliverable.payload && typeof deliverable.payload.content === 'string' && (
        <div className="space-y-2 rounded-xl border border-[var(--app-border)]/80 bg-[var(--app-bg-alt)]/50 p-3.5">
          <div className="flex items-center justify-between text-[11px] font-semibold text-[var(--app-text-muted)]">
            <span className="flex items-center gap-1.5">
              <FileText className="h-3.5 w-3.5 text-emerald-500" />
              REPORT DOCUMENT
            </span>
            <button
              onClick={() => {
                navigator.clipboard.writeText(String(deliverable.payload?.content || ''))
                setCopied(true)
                setTimeout(() => setCopied(false), 2000)
              }}
              className="flex items-center gap-1 text-[10px] text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
            >
              {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          <div className="max-h-64 overflow-y-auto rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-xs leading-relaxed text-[var(--app-text)] whitespace-pre-wrap font-sans">
            {deliverable.payload.content}
          </div>
        </div>
      )}

      {/* 4. Code Patch / Diff Format */}
      {isCode && deliverable.payload && (
        <div className="space-y-2 rounded-xl border border-[var(--app-border)]/80 bg-[var(--app-bg-alt)]/50 p-3.5">
          <div className="flex items-center justify-between text-[11px] font-semibold text-[var(--app-text-muted)]">
            <span className="flex items-center gap-1.5">
              <GitPullRequest className="h-3.5 w-3.5 text-indigo-500" />
              CODE PATCH / DIFF
            </span>
            {typeof deliverable.payload.branch === 'string' && (
              <span className="font-mono text-[10px] text-indigo-400">
                {deliverable.payload.branch}
              </span>
            )}
          </div>
          {typeof deliverable.payload.diff === 'string' && (
            <div className="max-h-64 overflow-y-auto rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3 font-mono text-[11px] leading-tight text-[var(--app-text)] whitespace-pre">
              {deliverable.payload.diff.split('\n').map((line, idx) => {
                const isAdd = line.startsWith('+') && !line.startsWith('+++')
                const isDel = line.startsWith('-') && !line.startsWith('---')
                return (
                  <div
                    key={idx}
                    className={cn(
                      'px-1 py-0.5',
                      isAdd && 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
                      isDel && 'bg-red-500/15 text-red-600 dark:text-red-400'
                    )}
                  >
                    {line}
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {/* 5. Alert Format: Severity Details */}
      {isAlert && deliverable.payload && (
        <div className="space-y-2 rounded-xl border border-amber-500/20 bg-amber-500/5 p-3.5 text-xs">
          <div className="flex items-center gap-1.5 font-semibold text-amber-600 dark:text-amber-400">
            <AlertCircle className="h-4 w-4" />
            <span>Worker Attention Notice</span>
          </div>
          {typeof deliverable.payload.trace === 'string' && (
            <div className="max-h-32 overflow-y-auto rounded-lg bg-black/80 p-2.5 font-mono text-[10px] text-zinc-300">
              {deliverable.payload.trace}
            </div>
          )}
          {typeof deliverable.payload.recommended_action === 'string' && (
            <p className="text-[11px] text-[var(--app-text)] font-medium">
              Action Needed: {deliverable.payload.recommended_action}
            </p>
          )}
        </div>
      )}

      {/* Attached Media Artifact References */}
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

      {/* Inline Request Changes / Revision Form */}
      {showReviseForm && (
        <div className="rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 space-y-3 shadow-xs">
          <div className="flex items-center justify-between text-xs font-semibold text-[var(--app-text)]">
            <span className="flex items-center gap-1.5 text-[var(--app-primary)]">
              <MessageSquareReply className="h-4 w-4" />
              Request Changes from AI Worker
            </span>
            <button
              onClick={() => setShowReviseForm(false)}
              className="text-[10px] text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
            >
              Cancel
            </button>
          </div>

          {/* Quick Tag Chips */}
          <div className="space-y-1">
            <div className="text-[10px] text-[var(--app-text-muted)] font-medium flex items-center gap-1">
              <Tag className="h-3 w-3" />
              Quick Tags:
            </div>
            <div className="flex flex-wrap gap-1.5">
              {PRESET_TAGS.map((tag) => {
                const isSelected = selectedTags.includes(tag)
                return (
                  <button
                    key={tag}
                    onClick={() => toggleTag(tag)}
                    className={cn(
                      'rounded-md px-2 py-0.5 text-[11px] font-medium transition-colors',
                      isSelected
                        ? 'bg-[var(--app-primary)] text-white font-bold'
                        : 'bg-[var(--app-surface-hover)] text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                    )}
                  >
                    #{tag}
                  </button>
                )
              })}
            </div>
          </div>

          {/* Feedback Notes Textarea */}
          <textarea
            value={revisionNotes}
            onChange={(e) => setRevisionNotes(e.target.value)}
            placeholder="Tell the worker what to adjust (e.g. Make the hook punchier, use darker colors, trim length)..."
            rows={3}
            className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] p-2.5 text-xs text-[var(--app-text)] placeholder:text-[var(--app-text-muted)] outline-hidden focus:border-[var(--app-primary)]"
          />

          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => setShowReviseForm(false)}
              className="h-7 text-xs"
            >
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={submitRevision}
              disabled={isLoading}
              className="h-7 text-xs gap-1.5 bg-amber-600 hover:bg-amber-700 text-white"
            >
              <Send className="h-3 w-3" />
              Send Back to Worker
            </Button>
          </div>
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

        {(isPending || isNeedsRevision) && !showReviseForm && (
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
              variant="outline"
              size="sm"
              onClick={() => setShowReviseForm(true)}
              disabled={isLoading}
              className="h-8 text-xs gap-1.5 border-amber-500/30 text-amber-700 dark:text-amber-400 hover:bg-amber-500/10"
              data-testid="request-changes-btn"
            >
              <MessageSquareReply className="h-3.5 w-3.5" />
              Request Changes
            </Button>

            <Button
              variant="primary"
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
