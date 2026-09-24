import { useEffect, useMemo, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  BarChart2,
  Bookmark,
  Bot,
  Calendar,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Clock,
  Code2,
  Copy,
  Download,
  ExternalLink,
  FileCode,
  FileText,
  Film,
  Filter,
  GitPullRequest,
  Globe,
  Heart,
  Image as ImageIcon,
  Inbox,
  Layers,
  LoaderCircle,
  MessageCircle,
  MessageSquare,
  MessageSquareReply,
  MoreHorizontal,
  RefreshCcw,
  Repeat2,
  Send,
  Share2,
  Tag,
  ThumbsUp,
  Trash2,
  Volume2,
  XCircle,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import { fetchDesktopV3Artifact } from '../../session-v3/artifact-api'
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

export function XLogo({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-label="X (Twitter) logo">
      <path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" />
    </svg>
  )
}

export function XVerifiedBadge({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <svg className={cn('shrink-0 text-sky-500 fill-current', className)} viewBox="0 0 24 24" aria-label="Verified account">
      <path d="M22.5 12.5c0-1.58-.875-2.95-2.148-3.6.154-.435.238-.905.238-1.4 0-2.21-1.79-4-4-4-.495 0-.965.084-1.4.238C14.55 2.475 13.18 1.6 11.6 1.6c-1.58 0-2.95.875-3.6 2.148-.435-.154-.905-.238-1.4-.238-2.21 0-4 1.79-4 4 0 .495.084.965.238 1.4C1.575 9.55.7 10.92.7 12.5c0 1.58.875 2.95 2.148 3.6-.154.435-.238.905-.238 1.4 0 2.21 1.79 4 4 4 .495 0 .965-.084 1.4-.238 1.25 1.273 2.62 2.148 4.2 2.148 1.58 0 2.95-.875 3.6-2.148.435.154.905.238 1.4.238 2.21 0 4-1.79 4-4 0-.495-.084-.965-.238-1.4 1.273-.65 2.148-2.02 2.148-3.6zm-12.71 4.29l-4.58-4.58 1.41-1.41 3.17 3.17 6.59-6.59 1.41 1.41-8 8z" />
    </svg>
  )
}

export function LinkedInLogo({ className = 'h-4 w-4' }: { className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-label="LinkedIn logo">
      <path d="M19 3a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h14m-.5 15.5v-5.3a3.26 3.26 0 0 0-3.26-3.26c-.85 0-1.84.52-2.28 1.3v-1.11h-2.79v8.37h2.79v-4.93c0-.77.62-1.4 1.39-1.4a1.4 1.4 0 0 1 1.4 1.4v4.93h2.75M6.46 10.9h2.79v8.37H6.46v-8.37M7.86 6.5a1.63 1.63 0 1 0 0 3.26 1.63 1.63 0 0 0 0-3.26z" />
    </svg>
  )
}

export function formatSocialTokens(text: string, platform: 'x' | 'linkedin') {
  if (!text) return null
  const tokenRegex = /(https?:\/\/[^\s]+|#[A-Za-z0-9_]+|@[A-Za-z0-9_]+|\$[A-Za-z0-9]+)/g
  const parts = text.split(tokenRegex)

  return parts.map((part, i) => {
    if (!part) return null
    if (part.startsWith('http://') || part.startsWith('https://')) {
      return (
        <a
          key={i}
          href={part}
          target="_blank"
          rel="noreferrer noopener"
          className={cn(
            'hover:underline break-all',
            platform === 'x'
              ? 'text-sky-500 dark:text-sky-400 font-normal'
              : 'text-[#0a66c2] dark:text-sky-400 font-medium'
          )}
        >
          {part}
        </a>
      )
    }
    if (part.startsWith('#')) {
      return (
        <span
          key={i}
          className={cn(
            'cursor-pointer hover:underline',
            platform === 'x'
              ? 'text-sky-500 dark:text-sky-400 font-normal'
              : 'text-[#0a66c2] dark:text-sky-400 font-semibold'
          )}
        >
          {part}
        </span>
      )
    }
    if (part.startsWith('@')) {
      return (
        <span
          key={i}
          className={cn(
            'cursor-pointer hover:underline',
            platform === 'x'
              ? 'text-sky-500 dark:text-sky-400 font-normal'
              : 'text-[#0a66c2] dark:text-sky-400 font-semibold'
          )}
        >
          {part}
        </span>
      )
    }
    if (part.startsWith('$')) {
      return (
        <span
          key={i}
          className={cn(
            'cursor-pointer hover:underline',
            platform === 'x'
              ? 'text-sky-500 dark:text-sky-400 font-normal'
              : 'text-[#0a66c2] dark:text-sky-400 font-semibold'
          )}
        >
          {part}
        </span>
      )
    }
    return <span key={i}>{part}</span>
  })
}

export interface SocialMediaAttachmentGalleryProps {
  mediaUrls?: string[]
  videoUrl?: string
  linkPreview?: {
    url: string
    title?: string
    description?: string
    image_url?: string
    domain?: string
  }
  platform?: 'x' | 'linkedin'
  onSelectImage?: (url: string) => void
}

export function SocialMediaAttachmentGallery({
  mediaUrls = [],
  videoUrl,
  linkPreview,
  platform = 'x',
  onSelectImage,
}: SocialMediaAttachmentGalleryProps) {
  if (videoUrl) {
    return (
      <div className="overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-black shadow-xs">
        <video
          controls
          playsInline
          src={videoUrl}
          className="max-h-80 w-full object-contain mx-auto"
        />
      </div>
    )
  }

  if (mediaUrls.length === 1) {
    const url = mediaUrls[0]
    return (
      <div
        className={cn(
          'overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-black/5 dark:bg-black/30 shadow-xs group relative',
          platform === 'linkedin' ? 'rounded-xl' : 'rounded-2xl'
        )}
      >
        <img
          src={url}
          alt="Social attachment"
          onClick={() => onSelectImage?.(url)}
          className="max-h-96 w-full object-cover transition-opacity hover:opacity-95 cursor-pointer"
        />
      </div>
    )
  }

  if (mediaUrls.length === 2) {
    return (
      <div
        className={cn(
          'grid grid-cols-2 gap-1.5 overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-black/5 dark:bg-black/30 shadow-xs',
          platform === 'linkedin' ? 'rounded-xl' : 'rounded-2xl'
        )}
      >
        {mediaUrls.map((url, i) => (
          <img
            key={i}
            src={url}
            alt={`Attachment ${i + 1}`}
            onClick={() => onSelectImage?.(url)}
            className="h-56 w-full object-cover cursor-pointer hover:opacity-95 transition-opacity"
          />
        ))}
      </div>
    )
  }

  if (mediaUrls.length === 3) {
    return (
      <div
        className={cn(
          'grid grid-cols-2 gap-1.5 overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-black/5 dark:bg-black/30 shadow-xs',
          platform === 'linkedin' ? 'rounded-xl' : 'rounded-2xl'
        )}
      >
        <img
          src={mediaUrls[0]}
          alt="Attachment 1"
          onClick={() => onSelectImage?.(mediaUrls[0])}
          className="row-span-2 h-full w-full object-cover cursor-pointer hover:opacity-95 transition-opacity"
        />
        <div className="flex flex-col gap-1.5">
          <img
            src={mediaUrls[1]}
            alt="Attachment 2"
            onClick={() => onSelectImage?.(mediaUrls[1])}
            className="h-[110px] w-full object-cover cursor-pointer hover:opacity-95 transition-opacity"
          />
          <img
            src={mediaUrls[2]}
            alt="Attachment 3"
            onClick={() => onSelectImage?.(mediaUrls[2])}
            className="h-[110px] w-full object-cover cursor-pointer hover:opacity-95 transition-opacity"
          />
        </div>
      </div>
    )
  }

  if (mediaUrls.length >= 4) {
    return (
      <div
        className={cn(
          'grid grid-cols-2 gap-1.5 overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-black/5 dark:bg-black/30 shadow-xs',
          platform === 'linkedin' ? 'rounded-xl' : 'rounded-2xl'
        )}
      >
        {mediaUrls.slice(0, 4).map((url, i) => (
          <img
            key={i}
            src={url}
            alt={`Attachment ${i + 1}`}
            onClick={() => onSelectImage?.(url)}
            className="h-36 w-full object-cover cursor-pointer hover:opacity-95 transition-opacity"
          />
        ))}
      </div>
    )
  }

  if (linkPreview) {
    return (
      <a
        href={linkPreview.url}
        target="_blank"
        rel="noreferrer noopener"
        className={cn(
          'block overflow-hidden rounded-2xl border border-[var(--app-border)]/80 bg-[var(--app-surface-subtle)] hover:bg-[var(--app-surface-hover)] transition-colors shadow-xs',
          platform === 'linkedin' ? 'rounded-xl' : 'rounded-2xl'
        )}
      >
        {linkPreview.image_url && (
          <img
            src={linkPreview.image_url}
            alt={linkPreview.title || 'Link preview'}
            className="h-44 w-full object-cover"
          />
        )}
        <div className="p-3 space-y-1">
          <span className="text-[11px] font-mono text-[var(--app-text-muted)] uppercase tracking-wider block">
            {linkPreview.domain || (linkPreview.url ? new URL(linkPreview.url).hostname : 'link')}
          </span>
          {linkPreview.title && (
            <h4 className="text-xs font-semibold text-[var(--app-text)] line-clamp-1">
              {linkPreview.title}
            </h4>
          )}
          {linkPreview.description && (
            <p className="text-[11px] text-[var(--app-text-muted)] line-clamp-2">
              {linkPreview.description}
            </p>
          )}
        </div>
      </a>
    )
  }

  return null
}

export interface TwitterReplicaCardProps {
  post: {
    text: string
    media_urls?: string[]
    video_url?: string
    author_name?: string
    author_handle?: string
    author_avatar?: string
    link_preview?: any
  }
  workerId?: string
  created_at?: number
  postIndex?: number
  totalPosts?: number
  onCopyText?: () => void
}

export function TwitterReplicaCard({
  post,
  workerId,
  created_at,
  postIndex,
  totalPosts,
  onCopyText,
}: TwitterReplicaCardProps) {
  const [liked, setLiked] = useState(false)
  const [reposted, setReposted] = useState(false)
  const [bookmarked, setBookmarked] = useState(false)

  const displayName = post.author_name || (workerId ? workerId.replace(/^worker_/, '').replace(/_/g, ' ') : 'Swarm AI')
  const handle = post.author_handle || (workerId ? workerId.replace(/^worker_/, '') : 'swarm_agent')

  return (
    <div
      data-testid="twitter-replica-card"
      className="rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-950 p-4 shadow-xs space-y-3 font-sans transition-all"
    >
      {/* Header */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-3 min-w-0">
          {post.author_avatar ? (
            <img
              src={post.author_avatar}
              alt={displayName}
              className="h-10 w-10 rounded-full object-cover shrink-0"
            />
          ) : (
            <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-gradient-to-tr from-sky-500 to-blue-600 text-white font-bold text-sm shadow-xs">
              {displayName.charAt(0).toUpperCase()}
            </div>
          )}
          <div className="min-w-0">
            <div className="flex items-center gap-1.5 flex-wrap">
              <span className="font-bold text-sm text-zinc-900 dark:text-zinc-100 truncate">
                {displayName}
              </span>
              <XVerifiedBadge className="h-4 w-4" />
              <span className="text-zinc-500 dark:text-zinc-400 text-xs">
                @{handle}
              </span>
              <span className="text-zinc-400 dark:text-zinc-600 text-xs">·</span>
              <span className="text-zinc-500 dark:text-zinc-400 text-xs">
                {created_at ? new Date(created_at).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : 'Just now'}
              </span>
            </div>
            {typeof postIndex === 'number' && typeof totalPosts === 'number' && totalPosts > 1 && (
              <div className="mt-0.5">
                <span className="rounded-md bg-sky-500/10 px-1.5 py-0.2 text-[10px] font-bold text-sky-600 dark:text-sky-400 font-mono">
                  Tweet {postIndex + 1} of {totalPosts}
                </span>
              </div>
            )}
          </div>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          {onCopyText && (
            <button
              onClick={onCopyText}
              title="Copy Tweet Text"
              className="rounded-lg p-1 text-zinc-400 hover:bg-zinc-100 dark:hover:bg-zinc-800 hover:text-zinc-700 dark:hover:text-zinc-200 transition-colors"
            >
              <Copy className="h-3.5 w-3.5" />
            </button>
          )}
          <XLogo className="h-4 w-4 text-zinc-800 dark:text-zinc-200" />
        </div>
      </div>

      {/* Tweet Body */}
      <div className="text-[14px] leading-relaxed text-zinc-900 dark:text-zinc-100 whitespace-pre-wrap">
        {formatSocialTokens(post.text, 'x')}
      </div>

      {/* Media Attachments */}
      {(post.media_urls?.length || post.video_url || post.link_preview) && (
        <div className="pt-1">
          <SocialMediaAttachmentGallery
            mediaUrls={post.media_urls}
            videoUrl={post.video_url}
            linkPreview={post.link_preview}
            platform="x"
          />
        </div>
      )}

      {/* Engagement Action Bar */}
      <div className="flex items-center justify-between border-t border-zinc-100 dark:border-zinc-800/80 pt-2.5 text-zinc-500 dark:text-zinc-400 text-xs">
        <button
          className="flex items-center gap-1.5 hover:text-sky-500 transition-colors group"
          title="Reply"
        >
          <div className="rounded-full p-1.5 group-hover:bg-sky-500/10 transition-colors">
            <MessageCircle className="h-4 w-4" />
          </div>
          <span className="text-[11px]">18</span>
        </button>

        <button
          onClick={() => setReposted((p) => !p)}
          className={cn(
            'flex items-center gap-1.5 transition-colors group',
            reposted ? 'text-emerald-500 font-semibold' : 'hover:text-emerald-500'
          )}
          title="Repost"
        >
          <div className="rounded-full p-1.5 group-hover:bg-emerald-500/10 transition-colors">
            <Repeat2 className="h-4 w-4" />
          </div>
          <span className="text-[11px]">{reposted ? 43 : 42}</span>
        </button>

        <button
          onClick={() => setLiked((p) => !p)}
          className={cn(
            'flex items-center gap-1.5 transition-colors group',
            liked ? 'text-rose-500 font-semibold' : 'hover:text-rose-500'
          )}
          title="Like"
        >
          <div className="rounded-full p-1.5 group-hover:bg-rose-500/10 transition-colors">
            <Heart className={cn('h-4 w-4', liked && 'fill-rose-500')} />
          </div>
          <span className="text-[11px]">{liked ? 193 : 192}</span>
        </button>

        <button
          className="flex items-center gap-1.5 hover:text-sky-500 transition-colors group"
          title="Views"
        >
          <div className="rounded-full p-1.5 group-hover:bg-sky-500/10 transition-colors">
            <BarChart2 className="h-4 w-4" />
          </div>
          <span className="text-[11px]">4.1K</span>
        </button>

        <div className="flex items-center gap-1">
          <button
            onClick={() => setBookmarked((p) => !p)}
            className={cn(
              'rounded-full p-1.5 hover:text-sky-500 hover:bg-sky-500/10 transition-colors',
              bookmarked && 'text-sky-500'
            )}
            title="Bookmark"
          >
            <Bookmark className={cn('h-4 w-4', bookmarked && 'fill-sky-500')} />
          </button>
          <button
            className="rounded-full p-1.5 hover:text-sky-500 hover:bg-sky-500/10 transition-colors"
            title="Share"
          >
            <Share2 className="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>
  )
}

export interface LinkedInReplicaCardProps {
  post: {
    text: string
    media_urls?: string[]
    video_url?: string
    author_name?: string
    author_handle?: string
    author_title?: string
    author_avatar?: string
    link_preview?: any
  }
  workerId?: string
  created_at?: number
  onCopyText?: () => void
}

export function LinkedInReplicaCard({
  post,
  workerId,
  created_at,
  onCopyText,
}: LinkedInReplicaCardProps) {
  const [liked, setLiked] = useState(false)

  const displayName = post.author_name || (workerId ? workerId.replace(/^worker_/, '').replace(/_/g, ' ') : 'Swarm Specialist')
  const authorTitle = post.author_title || 'Autonomous AI Specialist • 1st • Follow'

  return (
    <div
      data-testid="linkedin-replica-card"
      className="rounded-2xl border border-zinc-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 p-4 shadow-xs space-y-3 font-sans transition-all"
    >
      {/* Header */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-3 min-w-0">
          {post.author_avatar ? (
            <img
              src={post.author_avatar}
              alt={displayName}
              className="h-12 w-12 rounded-full object-cover shrink-0"
            />
          ) : (
            <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-full bg-gradient-to-tr from-blue-700 to-indigo-600 text-white font-bold text-base shadow-xs">
              {displayName.charAt(0).toUpperCase()}
            </div>
          )}
          <div className="min-w-0">
            <h4 className="font-semibold text-sm text-zinc-900 dark:text-zinc-100 truncate">
              {displayName}
            </h4>
            <p className="text-xs text-zinc-500 dark:text-zinc-400 truncate">
              {authorTitle}
            </p>
            <div className="flex items-center gap-1 text-[11px] text-zinc-400 dark:text-zinc-500 mt-0.5">
              <span>{created_at ? new Date(created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '1h'}</span>
              <span>•</span>
              <span className="flex items-center gap-0.5">
                <Globe className="h-3 w-3" />
                <span>Public</span>
              </span>
            </div>
          </div>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          {onCopyText && (
            <button
              onClick={onCopyText}
              title="Copy LinkedIn Post"
              className="rounded-lg p-1 text-zinc-400 hover:bg-zinc-100 dark:hover:bg-zinc-800 hover:text-zinc-700 dark:hover:text-zinc-200 transition-colors"
            >
              <Copy className="h-3.5 w-3.5" />
            </button>
          )}
          <span className="rounded bg-[#0a66c2] text-white px-1.5 py-0.5 text-[10px] font-bold tracking-wider">
            in
          </span>
          <button className="text-zinc-400 hover:text-zinc-600">
            <MoreHorizontal className="h-4 w-4" />
          </button>
        </div>
      </div>

      {/* Post Text */}
      <div className="text-[13px] leading-relaxed text-zinc-800 dark:text-zinc-200 whitespace-pre-wrap">
        {formatSocialTokens(post.text, 'linkedin')}
      </div>

      {/* Media Attachments */}
      {(post.media_urls?.length || post.video_url || post.link_preview) && (
        <div className="pt-1">
          <SocialMediaAttachmentGallery
            mediaUrls={post.media_urls}
            videoUrl={post.video_url}
            linkPreview={post.link_preview}
            platform="linkedin"
          />
        </div>
      )}

      {/* Reactions Count Stats */}
      <div className="flex items-center justify-between border-b border-zinc-100 dark:border-zinc-800/80 pb-2 text-[11px] text-zinc-500 dark:text-zinc-400">
        <div className="flex items-center gap-1.5">
          <div className="flex -space-x-1">
            <span className="flex h-4 w-4 items-center justify-center rounded-full bg-blue-600 text-white text-[9px]">👍</span>
            <span className="flex h-4 w-4 items-center justify-center rounded-full bg-red-500 text-white text-[9px]">❤️</span>
            <span className="flex h-4 w-4 items-center justify-center rounded-full bg-emerald-600 text-white text-[9px]">👏</span>
          </div>
          <span className="font-medium text-zinc-600 dark:text-zinc-300">{liked ? 89 : 88}</span>
        </div>
        <div>
          <span>16 comments • 5 reposts</span>
        </div>
      </div>

      {/* Action Buttons Bar */}
      <div className="grid grid-cols-4 gap-1 pt-1 text-xs text-zinc-600 dark:text-zinc-400">
        <button
          onClick={() => setLiked((p) => !p)}
          className={cn(
            'flex items-center justify-center gap-1.5 rounded-lg py-2 hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-colors font-medium',
            liked && 'text-[#0a66c2] font-semibold'
          )}
        >
          <ThumbsUp className={cn('h-4 w-4', liked && 'fill-[#0a66c2]')} />
          <span>Like</span>
        </button>

        <button className="flex items-center justify-center gap-1.5 rounded-lg py-2 hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-colors font-medium">
          <MessageSquare className="h-4 w-4" />
          <span>Comment</span>
        </button>

        <button className="flex items-center justify-center gap-1.5 rounded-lg py-2 hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-colors font-medium">
          <Repeat2 className="h-4 w-4" />
          <span>Repost</span>
        </button>

        <button className="flex items-center justify-center gap-1.5 rounded-lg py-2 hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-colors font-medium">
          <Send className="h-4 w-4" />
          <span>Send</span>
        </button>
      </div>
    </div>
  )
}

export interface HtmlSandboxPreviewProps {
  html: string
  title?: string
  className?: string
}

export function HtmlSandboxPreview({ html, title, className }: HtmlSandboxPreviewProps) {
  const [viewMode, setViewMode] = useState<'preview' | 'code'>('preview')
  const [copied, setCopied] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)

  return (
    <div
      data-testid="html-sandbox-preview"
      className={cn(
        'overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xs space-y-0',
        className
      )}
    >
      <div className="flex items-center justify-between border-b border-[var(--app-border)]/60 bg-[var(--app-surface-subtle)] px-3 py-2 text-xs">
        <div className="flex items-center gap-2">
          <FileCode className="h-4 w-4 text-[var(--app-primary)]" />
          <span className="font-semibold text-[var(--app-text)]">{title || 'HTML Interactive Sandbox'}</span>
        </div>

        <div className="flex items-center gap-2">
          <div className="flex items-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5 text-[11px]">
            <button
              onClick={() => setViewMode('preview')}
              className={cn(
                'rounded-md px-2 py-0.5 font-medium transition-colors',
                viewMode === 'preview'
                  ? 'bg-[var(--app-primary)] text-white font-bold'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              )}
            >
              Interactive Preview
            </button>
            <button
              onClick={() => setViewMode('code')}
              className={cn(
                'rounded-md px-2 py-0.5 font-medium transition-colors',
                viewMode === 'code'
                  ? 'bg-[var(--app-primary)] text-white font-bold'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
              )}
            >
              HTML Source
            </button>
          </div>

          {viewMode === 'preview' && (
            <button
              onClick={() => setReloadKey((k) => k + 1)}
              title="Reload sandbox"
              className="rounded-md p-1 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
            >
              <RefreshCcw className="h-3.5 w-3.5" />
            </button>
          )}

          <button
            onClick={() => {
              navigator.clipboard.writeText(html)
              setCopied(true)
              setTimeout(() => setCopied(false), 2000)
            }}
            className="flex items-center gap-1 rounded-md px-2 py-1 text-[11px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] font-medium"
          >
            {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
            <span>{copied ? 'Copied' : 'Copy'}</span>
          </button>
        </div>
      </div>

      {viewMode === 'preview' ? (
        <div className="p-2 bg-white">
          <iframe
            key={reloadKey}
            sandbox="allow-scripts allow-same-origin"
            srcDoc={html}
            title={title || 'HTML Preview Sandbox'}
            className="h-80 w-full rounded-lg border border-zinc-200 bg-white"
          />
        </div>
      ) : (
        <div className="max-h-80 overflow-y-auto p-3 font-mono text-[11px] leading-relaxed text-[var(--app-text)] bg-[var(--app-bg-alt)] whitespace-pre">
          {html}
        </div>
      )}
    </div>
  )
}

export interface AudioPlayerCardProps {
  audioUrl: string
  title?: string
  label?: string
  className?: string
}

export function AudioPlayerCard({ audioUrl, title, label, className }: AudioPlayerCardProps) {
  return (
    <div
      data-testid="audio-player-card"
      className={cn(
        'rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3.5 shadow-xs space-y-2.5',
        className
      )}
    >
      <div className="flex items-center justify-between text-xs">
        <div className="flex items-center gap-2">
          <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-purple-500/10 text-purple-600 dark:text-purple-400">
            <Volume2 className="h-4 w-4" />
          </div>
          <div>
            <h4 className="font-semibold text-[var(--app-text)]">{title || label || 'Audio Deliverable'}</h4>
            <span className="text-[10px] text-[var(--app-text-muted)]">HTML5 Audio Player</span>
          </div>
        </div>

        <a
          href={audioUrl}
          download={label || 'audio.mp3'}
          className="flex items-center gap-1 text-[11px] text-[var(--app-primary)] hover:underline font-medium"
        >
          <Download className="h-3 w-3" />
          <span>Download</span>
        </a>
      </div>

      <div className="flex items-center gap-0.5 h-6 px-1 bg-[var(--app-surface-subtle)] rounded-md opacity-80">
        {[12, 24, 16, 8, 20, 28, 14, 18, 22, 10, 16, 26, 18, 12, 20, 15, 25, 30, 20, 16, 22, 18, 10, 14].map((h, i) => (
          <div
            key={i}
            className="flex-1 bg-[var(--app-primary)] rounded-full transition-all"
            style={{ height: `${h}px` }}
          />
        ))}
      </div>

      <audio controls src={audioUrl} className="w-full h-8" />
    </div>
  )
}

export interface VideoPlayerCardProps {
  videoUrl: string
  title?: string
  label?: string
  className?: string
}

export function VideoPlayerCard({ videoUrl, title, label, className }: VideoPlayerCardProps) {
  return (
    <div
      data-testid="video-player-card"
      className={cn(
        'overflow-hidden rounded-xl border border-[var(--app-border)] bg-black shadow-xs space-y-0',
        className
      )}
    >
      <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900/90 px-3 py-1.5 text-xs text-zinc-200">
        <div className="flex items-center gap-1.5">
          <Film className="h-3.5 w-3.5 text-purple-400" />
          <span className="font-medium truncate">{title || label || 'Video Deliverable'}</span>
        </div>
        <a
          href={videoUrl}
          download={label || 'video.mp4'}
          className="flex items-center gap-1 text-[11px] text-purple-400 hover:text-purple-300 font-medium"
        >
          <Download className="h-3 w-3" />
          <span>Download</span>
        </a>
      </div>

      <video
        controls
        playsInline
        src={videoUrl}
        className="max-h-80 w-full object-contain mx-auto bg-black"
      />
    </div>
  )
}

export interface JsonInspectorCardProps {
  data: unknown
  title?: string
  className?: string
}

export function JsonInspectorCard({ data, title, className }: JsonInspectorCardProps) {
  const [copied, setCopied] = useState(false)
  const [isExpanded, setIsExpanded] = useState(true)

  const jsonString = useMemo(() => {
    try {
      return JSON.stringify(data, null, 2)
    } catch {
      return String(data)
    }
  }, [data])

  return (
    <div
      data-testid="json-inspector-card"
      className={cn(
        'rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xs space-y-0 overflow-hidden',
        className
      )}
    >
      <div className="flex items-center justify-between border-b border-[var(--app-border)]/60 bg-[var(--app-surface-subtle)] px-3 py-2 text-xs">
        <div className="flex items-center gap-2">
          <Code2 className="h-4 w-4 text-[var(--app-primary)]" />
          <span className="font-semibold text-[var(--app-text)]">{title || 'JSON Data Deliverable'}</span>
        </div>

        <div className="flex items-center gap-2">
          <button
            onClick={() => setIsExpanded((e) => !e)}
            className="flex items-center gap-1 text-[11px] text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
          >
            {isExpanded ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
            <span>{isExpanded ? 'Collapse' : 'Expand'}</span>
          </button>

          <button
            onClick={() => {
              navigator.clipboard.writeText(jsonString)
              setCopied(true)
              setTimeout(() => setCopied(false), 2000)
            }}
            className="flex items-center gap-1 rounded-md px-2 py-0.5 text-[11px] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] font-medium"
          >
            {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
            <span>{copied ? 'Copied JSON' : 'Copy JSON'}</span>
          </button>
        </div>
      </div>

      {isExpanded && (
        <pre className="max-h-72 overflow-y-auto p-3 font-mono text-[11px] leading-relaxed text-[var(--app-text)] bg-[var(--app-bg-alt)] whitespace-pre">
          {jsonString}
        </pre>
      )}
    </div>
  )
}

export function ArtifactImagePreview({
  sessionId,
  artifactId,
  label,
  className,
}: {
  sessionId: string
  artifactId: string
  label?: string
  className?: string
}) {
  const [imageUrl, setImageUrl] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)

  useEffect(() => {
    let active = true
    let objectUrl: string | null = null
    setLoading(true)
    setError(false)

    fetchDesktopV3Artifact(sessionId, artifactId)
      .then((blob) => {
        if (!active) return
        objectUrl = URL.createObjectURL(blob)
        setImageUrl(objectUrl)
        setLoading(false)
      })
      .catch((err) => {
        if (!active) return
        console.error('Failed to load artifact image:', err)
        setError(true)
        setLoading(false)
      })

    return () => {
      active = false
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [sessionId, artifactId])

  if (loading) {
    return (
      <div className={cn("flex h-36 w-full items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-xs text-[var(--app-text-muted)]", className)}>
        <LoaderCircle className="h-4 w-4 animate-spin text-[var(--app-primary)]" />
        <span className="ml-2">Loading image deliverable preview…</span>
      </div>
    )
  }

  if (error || !imageUrl) {
    return (
      <div className={cn("flex h-20 w-full items-center justify-center rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-xs text-[var(--app-text-muted)]", className)}>
        <ImageIcon className="h-4 w-4 mr-1.5 opacity-60" />
        <span>Image artifact ({label || artifactId})</span>
      </div>
    )
  }

  return (
    <div className={cn("overflow-hidden rounded-xl border border-[var(--app-border)] bg-black/5 dark:bg-black/30 shadow-xs", className)}>
      <img
        src={imageUrl}
        alt={label || 'Deliverable image preview'}
        className="max-h-80 w-full object-contain mx-auto"
      />
      <div className="border-t border-[var(--app-border)]/50 bg-[var(--app-surface)] px-3 py-1.5 text-[11px] text-[var(--app-text-muted)] flex items-center justify-between">
        <span className="truncate font-medium">{label || artifactId}</span>
        <a
          href={imageUrl}
          download={label || `${artifactId}.jpg`}
          className="text-[var(--app-primary)] hover:underline ml-2 shrink-0 font-medium"
        >
          Download
        </a>
      </div>
    </div>
  )
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

  const PRESET_TAGS = ['Tone', 'Length', 'Media', 'Formatting', 'Accuracy', 'Hook', 'Call to Action', 'Hashtags', 'Code']

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
  const posts: Array<{
    text: string
    media_urls?: string[]
    video_url?: string
    author_name?: string
    author_handle?: string
    author_title?: string
    author_avatar?: string
    link_preview?: any
  }> = useMemo(() => {
    if (!deliverable.payload) return []
    if (Array.isArray(deliverable.payload.posts)) {
      return deliverable.payload.posts.map((p: any) =>
        typeof p === 'string'
          ? { text: p }
          : {
              text: p.text || '',
              media_urls: p.media_urls || (p.image_url ? [p.image_url] : undefined),
              video_url: p.video_url,
              author_name: p.author_name || (deliverable.payload?.author_name as string),
              author_handle: p.author_handle || (deliverable.payload?.author_handle as string),
              author_title: p.author_title || (deliverable.payload?.author_title as string),
              author_avatar: p.author_avatar || (deliverable.payload?.author_avatar as string),
              link_preview: p.link_preview,
            }
      )
    }
    if (Array.isArray(deliverable.payload.tweets)) {
      return deliverable.payload.tweets.map((t: any) =>
        typeof t === 'string'
          ? { text: t }
          : {
              text: t.text || '',
              media_urls: t.media_urls || (t.image_url ? [t.image_url] : undefined),
              video_url: t.video_url,
              author_name: t.author_name || (deliverable.payload?.author_name as string),
              author_handle: t.author_handle || (deliverable.payload?.author_handle as string),
              author_avatar: t.author_avatar || (deliverable.payload?.author_avatar as string),
              link_preview: t.link_preview,
            }
      )
    }
    if (deliverable.payload.tweet) {
      const t = deliverable.payload.tweet as any
      return [
        typeof t === 'string'
          ? { text: t }
          : {
              text: t.text || '',
              media_urls: t.media_urls || (t.image_url ? [t.image_url] : undefined),
              video_url: t.video_url,
              author_name: t.author_name,
              author_handle: t.author_handle,
            },
      ]
    }
    if (deliverable.payload.post) {
      const p = deliverable.payload.post as any
      return [
        typeof p === 'string'
          ? { text: p }
          : {
              text: p.text || '',
              media_urls: p.media_urls || (p.image_url ? [p.image_url] : undefined),
              video_url: p.video_url,
              author_name: p.author_name,
              author_handle: p.author_handle,
              author_title: p.author_title,
            },
      ]
    }
    if (isSocial && typeof deliverable.payload.text === 'string') {
      return [{ text: deliverable.payload.text }]
    }
    return []
  }, [deliverable.payload, isSocial])

  // Platform selection for social post replicas
  const defaultPlatform = useMemo<'x' | 'linkedin'>(() => {
    if (deliverable.action_contract?.action === 'publish_linkedin_post') return 'linkedin'
    if (deliverable.title.toLowerCase().includes('linkedin')) return 'linkedin'
    if (deliverable.payload?.platform === 'linkedin') return 'linkedin'
    return 'x'
  }, [deliverable])

  const [activePlatform, setActivePlatform] = useState<'x' | 'linkedin'>(defaultPlatform)

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
            <div className="flex items-center gap-2">
              <span>
                {new Date(deliverable.revision_feedback.requested_at).toLocaleTimeString([], {
                  hour: '2-digit',
                  minute: '2-digit',
                })}
              </span>
              <button
                onClick={() => setShowReviseForm(true)}
                className="text-[10px] text-amber-700 dark:text-amber-400 underline hover:opacity-80 font-medium"
              >
                Edit Feedback
              </button>
            </div>
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

      {/* 1. Social Post Format: Authentic Replicas for Twitter/X and LinkedIn */}
      {isSocial && posts.length > 0 && (
        <div className="space-y-3 rounded-xl border border-[var(--app-border)]/80 bg-[var(--app-bg-alt)]/50 p-3.5">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)]/50 pb-2.5">
            <div className="flex items-center gap-2">
              <span className="text-[11px] font-semibold text-[var(--app-text-muted)]">
                PREMADE THREAD ({posts.length} {posts.length === 1 ? 'POST' : 'POSTS'})
              </span>
              {isXPostAction && (
                <span className="text-blue-500 text-[11px] flex items-center gap-1 font-medium">
                  Target: X (Twitter)
                </span>
              )}
            </div>

            {/* Platform Replica Switcher */}
            <div className="flex items-center gap-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5 text-xs">
              <button
                onClick={() => setActivePlatform('x')}
                className={cn(
                  'flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[11px] font-semibold transition-colors',
                  activePlatform === 'x'
                    ? 'bg-black text-white dark:bg-zinc-100 dark:text-zinc-900 shadow-2xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                )}
              >
                <XLogo className="h-3 w-3" />
                <span>Twitter / X</span>
              </button>
              <button
                onClick={() => setActivePlatform('linkedin')}
                className={cn(
                  'flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[11px] font-semibold transition-colors',
                  activePlatform === 'linkedin'
                    ? 'bg-[#0a66c2] text-white shadow-2xs'
                    : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                )}
              >
                <LinkedInLogo className="h-3 w-3" />
                <span>LinkedIn</span>
              </button>
            </div>
          </div>

          <div className="space-y-4">
            {activePlatform === 'x' ? (
              posts.map((post, idx) => (
                <TwitterReplicaCard
                  key={idx}
                  post={post}
                  workerId={deliverable.worker_id}
                  created_at={deliverable.created_at}
                  postIndex={idx}
                  totalPosts={posts.length}
                  onCopyText={() => {
                    navigator.clipboard.writeText(post.text)
                    setCopied(true)
                    setTimeout(() => setCopied(false), 2000)
                  }}
                />
              ))
            ) : (
              posts.map((post, idx) => (
                <LinkedInReplicaCard
                  key={idx}
                  post={post}
                  workerId={deliverable.worker_id}
                  created_at={deliverable.created_at}
                  onCopyText={() => {
                    navigator.clipboard.writeText(post.text)
                    setCopied(true)
                    setTimeout(() => setCopied(false), 2000)
                  }}
                />
              ))
            )}
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
            <VideoPlayerCard videoUrl={deliverable.payload.video_url} title={deliverable.title} />
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
            <AudioPlayerCard audioUrl={deliverable.payload.audio_url} title={deliverable.title} />
          )}
        </div>
      )}

      {/* 3. HTML Interactive Sandbox Preview (if html present in payload) */}
      {deliverable.payload && typeof deliverable.payload.html === 'string' && (
        <HtmlSandboxPreview
          html={deliverable.payload.html}
          title={deliverable.title}
        />
      )}

      {/* 4. JSON / Data Inspector (if json/data present in payload) */}
      {deliverable.payload && Boolean(deliverable.payload.json || deliverable.payload.data) && (
        <JsonInspectorCard
          data={deliverable.payload.json || deliverable.payload.data}
          title={deliverable.title}
        />
      )}

      {/* 5. Report Format: Rich Document View */}
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
              className="flex items-center gap-1 text-[10px] text-[var(--app-text-muted)] hover:text-[var(--app-text)] font-medium"
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

      {/* 6. Code Patch / Diff Format */}
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

      {/* 7. Alert Format: Severity Details */}
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

      {/* Attached Media Artifact References & Visual Previews */}
      {deliverable.media_refs && deliverable.media_refs.length > 0 && (
        <div className="space-y-2.5 pt-1">
          <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)] flex items-center gap-1.5">
            <Layers className="h-3.5 w-3.5 text-purple-500" />
            <span>Attached Deliverable Artifacts ({deliverable.media_refs.length})</span>
          </div>

          {/* Render Rich Previews for Media Artifacts */}
          <div className="space-y-3">
            {deliverable.media_refs.map((m, i) => {
              const isImg =
                m.media_type?.startsWith('image/') ||
                /\.(png|jpe?g|webp|gif|svg)$/i.test(m.filename || m.path || m.label || '') ||
                deliverable.kind === 'media' ||
                deliverable.kind === 'media_bundle'

              const sessionId = (m.session_id as string) || deliverable.session_id
              const artifactId = (m.artifact_id as string) || (m.variant_id as string)

              if (isImg && sessionId && artifactId) {
                return (
                  <ArtifactImagePreview
                    key={`img-${i}`}
                    sessionId={sessionId}
                    artifactId={artifactId}
                    label={m.label || m.filename || m.path || 'Image deliverable'}
                  />
                )
              }
              return null
            })}
          </div>

          {/* Artifact Reference Chips */}
          <div className="flex flex-wrap gap-2">
            {deliverable.media_refs.map((m, i) => (
              <div
                key={i}
                className="flex items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1 text-xs text-[var(--app-text)] shadow-2xs"
              >
                <FileText className="h-3.5 w-3.5 text-[var(--app-primary)]" />
                <span>{m.label || m.filename || m.path || 'Artifact'}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Accepted & Ready Banner (when approved/published) */}
      {isApproved && (
        <div className="rounded-xl border border-emerald-500/20 bg-emerald-500/5 p-3.5 text-xs space-y-2.5">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2 font-semibold text-emerald-600 dark:text-emerald-400">
              <CheckCircle2 className="h-4 w-4" />
              <span>Accepted & Ready for Use</span>
            </div>
            <span className="rounded-full bg-emerald-500/20 px-2 py-0.5 text-[10px] font-bold text-emerald-700 dark:text-emerald-300">
              Copy-Ready
            </span>
          </div>

          <div className="flex items-center gap-2 pt-1">
            {posts.length > 0 && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  const allText = posts
                    .map((p, i) => `${posts.length > 1 ? `${i + 1}/${posts.length} ` : ''}${p.text}`)
                    .join('\n\n')
                  navigator.clipboard.writeText(allText)
                  setCopied(true)
                  setTimeout(() => setCopied(false), 2000)
                }}
                className="h-7 text-xs gap-1.5"
              >
                {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
                <span>{copied ? 'Copied Thread!' : (posts.length > 1 ? 'Copy Full Thread' : 'Copy Post Content')}</span>
              </Button>
            )}
          </div>

          {deliverable.action_result && (
            <div className="border-t border-emerald-500/10 pt-2 text-[11px] text-[var(--app-text-muted)]">
              <span className="font-semibold text-emerald-600 dark:text-emerald-400">Publication Receipt: </span>
              Target: {String(deliverable.action_result.published_to || 'Executed Action')} • Status:{' '}
              {String(deliverable.action_result.status || 'Success')}
            </div>
          )}
        </div>
      )}

      {/* Inline Request Changes / Revision Form */}
      {showReviseForm && (
        <div className="rounded-xl border border-amber-500/30 bg-[var(--app-surface)] p-4 space-y-3 shadow-xs">
          <div className="flex items-center justify-between text-xs font-semibold text-[var(--app-text)]">
            <span className="flex items-center gap-1.5 text-amber-600 dark:text-amber-400">
              <MessageSquareReply className="h-4 w-4" />
              Send Back for Worker Revision
            </span>
            <button
              onClick={() => setShowReviseForm(false)}
              className="text-[10px] text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
            >
              Cancel
            </button>
          </div>

          <p className="text-[11px] text-[var(--app-text-muted)]">
            Provide feedback and tags to guide the worker. The worker will be prompted to revise this deliverable.
          </p>

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
                        ? 'bg-amber-600 text-white font-bold'
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
            className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] p-2.5 text-xs text-[var(--app-text)] placeholder:text-[var(--app-text-muted)] outline-hidden focus:border-amber-500"
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
              Send Back / Request Revision
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
                <CheckCircle2 className="h-3.5 w-3.5" />
              )}
              {isXPostAction ? 'Approve & Publish to X' : 'Accept / Approve'}
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
