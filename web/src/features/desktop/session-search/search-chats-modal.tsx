import { useCallback, useEffect, useMemo, useState } from 'react'
import type { FormEvent } from 'react'
import { LoaderCircle, MessageSquare, Pencil, Search, Trash2 } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { ModalCloseButton } from '../../../components/ui/modal-close-button'
import { updateSessionV3Title } from '../session-v3/api'
import {
  deleteDesktopSessions,
  searchDesktopSessions,
  type DesktopSessionDeletePreview,
  type DesktopSessionLibrarySummary,
  type DesktopSessionSearchItem,
} from './session-search-api'

const PAGE_SIZE = 50
const RECENT_SEARCHES_KEY = 'swarm.web.desktop.session_search.recents'
const MAX_RECENT_SEARCHES = 8
const EMPTY_SUMMARY: DesktopSessionLibrarySummary = { active_conversation_count: 0, archived_conversation_count: 0, raw_session_count: 0, agent_child_count: 0, logical_content_bytes: 0 }
type ArchivedFilter = 'exclude' | 'include' | 'only'
type CleanupPreset = '14' | '30' | '90' | 'custom'

function loadRecentSearches(): string[] {
  if (typeof window === 'undefined') return []
  try {
    const parsed = JSON.parse(window.localStorage.getItem(RECENT_SEARCHES_KEY) || '[]')
    return Array.isArray(parsed) ? parsed.map((value) => typeof value === 'string' ? value.trim() : '').filter(Boolean).slice(0, MAX_RECENT_SEARCHES) : []
  } catch { return [] }
}
function saveRecentSearches(values: string[]) { if (typeof window !== 'undefined') window.localStorage.setItem(RECENT_SEARCHES_KEY, JSON.stringify(values.slice(0, MAX_RECENT_SEARCHES))) }
function rememberSearchTerm(term: string, current: string[]): string[] {
  const normalized = term.trim()
  if (!normalized) return current
  const next = [normalized, ...current.filter((value) => value.toLowerCase() !== normalized.toLowerCase())].slice(0, MAX_RECENT_SEARCHES)
  saveRecentSearches(next)
  return next
}
function timestampFromDateInput(value: string, endOfDay = false): number | undefined {
  if (!value) return undefined
  const timestamp = new Date(`${value}T${endOfDay ? '23:59:59.999' : '00:00:00.000'}`).getTime()
  return Number.isFinite(timestamp) ? timestamp : undefined
}
function formatSessionTime(timestamp: number): string { return timestamp ? new Date(timestamp).toLocaleString() : 'No activity yet' }
function sessionTitle(item: DesktopSessionSearchItem): string { return item.title?.trim() || item.id }
function formatBytes(value: number): string {
  if (value < 1024) return `${value.toLocaleString()} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let size = value / 1024
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++ }
  return `${size.toFixed(size >= 10 ? 1 : 2)} ${units[unit]}`
}
function lineageTag(item: DesktopSessionSearchItem): string {
  if (item.library_metric?.unlinked_child) return 'Unlinked child'
  const kind = (item.library_metric?.lineage_kind || '').toLowerCase()
  if (kind.includes('flow')) return 'Flow'
  if (kind.includes('background')) return 'Background'
  return item.library_metric?.parent_session_id ? 'Subagent' : ''
}
function cleanupTimestamp(preset: CleanupPreset, customDate: string): number | undefined {
  if (preset === 'custom') return timestampFromDateInput(customDate)
  return Date.now() - Number(preset) * 24 * 60 * 60 * 1000
}

interface SearchChatsModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onOpenSession: (item: DesktopSessionSearchItem) => void
}

export function SearchChatsModal({ open, onOpenChange, onOpenSession }: SearchChatsModalProps) {
  const [queryDraft, setQueryDraft] = useState('')
  const [submittedQuery, setSubmittedQuery] = useState('')
  const [archivedMode, setArchivedMode] = useState<ArchivedFilter>('exclude')
  const [fromDate, setFromDate] = useState('')
  const [toDate, setToDate] = useState('')
  const [items, setItems] = useState<DesktopSessionSearchItem[]>([])
  const [summary, setSummary] = useState(EMPTY_SUMMARY)
  const [nextCursor, setNextCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [recentSearches, setRecentSearches] = useState<string[]>(loadRecentSearches)
  const [cleanupPreset, setCleanupPreset] = useState<CleanupPreset>('30')
  const [cleanupDate, setCleanupDate] = useState('')
  const [deletePreview, setDeletePreview] = useState<DesktopSessionDeletePreview | null>(null)
  const [cleanupPending, setCleanupPending] = useState(false)
  const [confirmRecent, setConfirmRecent] = useState(false)

  const runSearch = useCallback(async (cursor = '') => {
    const append = cursor.trim() !== ''
    setError(null)
    append ? setLoadingMore(true) : setLoading(true)
    try {
      const result = await searchDesktopSessions({ query: submittedQuery, archived_mode: archivedMode, global: true, from_updated_at: timestampFromDateInput(fromDate), to_updated_at: timestampFromDateInput(toDate, true), cursor: cursor || undefined, limit: PAGE_SIZE })
      setItems((current) => append ? [...current, ...result.items] : result.items)
      setSummary(result.summary)
      setNextCursor(result.pagination.next_cursor ?? '')
      setHasMore(Boolean(result.pagination.has_more && result.pagination.next_cursor))
      if (!append) setRecentSearches((current) => rememberSearchTerm(submittedQuery, current))
    } catch (searchError) { setError(searchError instanceof Error ? searchError.message : 'Search failed') }
    finally { setLoading(false); setLoadingMore(false) }
  }, [archivedMode, fromDate, submittedQuery, toDate])

  useEffect(() => { if (open) void runSearch() }, [open, runSearch])
  useEffect(() => { setDeletePreview(null); setConfirmRecent(false) }, [cleanupPreset, cleanupDate, archivedMode])

  const groups = useMemo(() => {
    const byRoot = new Map<string, { root?: DesktopSessionSearchItem; children: DesktopSessionSearchItem[] }>()
    for (const item of items) {
      const metric = item.library_metric
      const rootID = metric?.unlinked_child ? `unlinked:${item.id}` : metric?.root_session_id || item.id
      const group = byRoot.get(rootID) ?? { children: [] }
      if (item.id === rootID || (!metric?.parent_session_id && !metric?.unlinked_child)) group.root = item
      else group.children.push(item)
      byRoot.set(rootID, group)
    }
    return Array.from(byRoot.entries())
  }, [items])

  const renameItem = async (item: DesktopSessionSearchItem) => {
    const title = window.prompt('Rename conversation', sessionTitle(item))?.trim()
    if (!title || title === sessionTitle(item)) return
    try { await updateSessionV3Title(item.id, title, crypto.randomUUID()); await runSearch() }
    catch (renameError) { setError(renameError instanceof Error ? renameError.message : 'Rename failed') }
  }
  const deleteItem = async (item: DesktopSessionSearchItem) => {
    try {
      const preview = await deleteDesktopSessions({ session_ids: [item.id], archived_mode: 'include', global: true, dry_run: true })
      const warning = preview.recent_75_overlap_count > 0 ? '\nThis includes one of your newest 75 conversations.' : ''
      if (!window.confirm(`Delete ${preview.conversation_count} conversation(s), ${preview.session_count} session(s), and approximately ${formatBytes(preview.logical_bytes)} of logical content?${warning}`)) return
      await deleteDesktopSessions({ session_ids: [item.id], archived_mode: 'include', global: true, confirmation_token: preview.confirmation_token, confirm_recent: preview.recent_75_overlap_count > 0 })
      await runSearch()
    } catch (deleteError) { setError(deleteError instanceof Error ? deleteError.message : 'Delete failed') }
  }
  const previewCleanup = async () => {
    const updatedBefore = cleanupTimestamp(cleanupPreset, cleanupDate)
    if (!updatedBefore) { setError('Choose a cleanup date'); return }
    setCleanupPending(true); setError(null)
    try { setDeletePreview(await deleteDesktopSessions({ updated_before: updatedBefore, archived_mode: archivedMode, global: true, dry_run: true })); setConfirmRecent(false) }
    catch (previewError) { setError(previewError instanceof Error ? previewError.message : 'Cleanup preview failed') }
    finally { setCleanupPending(false) }
  }
  const executeCleanup = async () => {
    const updatedBefore = cleanupTimestamp(cleanupPreset, cleanupDate)
    if (!updatedBefore || !deletePreview) return
    setCleanupPending(true); setError(null)
    try {
      await deleteDesktopSessions({ updated_before: updatedBefore, archived_mode: archivedMode, global: true, confirmation_token: deletePreview.confirmation_token, confirm_recent: confirmRecent })
      setDeletePreview(null); setConfirmRecent(false); await runSearch()
    } catch (cleanupError) { setError(cleanupError instanceof Error ? cleanupError.message : 'Cleanup failed') }
    finally { setCleanupPending(false) }
  }

  if (!open) return null
  const handleSubmit = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); setSubmittedQuery(queryDraft.trim()) }
  const renderResult = (item: DesktopSessionSearchItem, child = false) => {
    const snippet = item.snippets?.[0]
    const tag = child ? lineageTag(item) : ''
    return <div key={item.id} className={child ? 'ml-6 border-l border-[var(--app-border)] pl-3' : ''}>
      <div className="grid gap-2 px-4 py-3 hover:bg-[var(--app-surface-hover)] sm:grid-cols-[minmax(0,1fr)_auto] sm:px-5">
        <button type="button" onClick={() => onOpenSession(item)} className="min-w-0 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]">
          <div className="flex min-w-0 items-center gap-2"><MessageSquare size={14} className="shrink-0 text-[var(--app-text-subtle)]" /><span className="truncate text-sm font-semibold">{sessionTitle(item)}</span>{tag ? <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--app-text-subtle)]">{tag}</span> : null}{item.archived ? <span className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] uppercase text-[var(--app-text-subtle)]">Archived</span> : null}</div>
          <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{item.workspace_name || item.workspace_path || 'Unknown workspace'}</div>
          {snippet?.text ? <div className="mt-2 line-clamp-2 text-sm leading-5 text-[var(--app-text-muted)]">{snippet.text}</div> : null}
        </button>
        <div className="flex items-center justify-between gap-2 sm:justify-end">
          <div className="text-xs text-[var(--app-text-subtle)] sm:text-right"><div>{item.message_count.toLocaleString()} messages</div><div className="mt-1">{formatSessionTime(item.updated_at || item.last_message_at)}</div></div>
          <button type="button" title="Rename" aria-label={`Rename ${sessionTitle(item)}`} onClick={() => void renameItem(item)} className="rounded-lg p-2 text-[var(--app-text-muted)] hover:bg-[var(--app-bg-inset)] hover:text-[var(--app-text)]"><Pencil size={14} /></button>
          <button type="button" title="Delete" aria-label={`Delete ${sessionTitle(item)}`} onClick={() => void deleteItem(item)} className="rounded-lg p-2 text-[var(--app-text-muted)] hover:bg-[var(--app-bg-inset)] hover:text-[var(--app-error)]"><Trash2 size={14} /></button>
        </div>
      </div>
    </div>
  }

  return <Dialog role="dialog" aria-modal="true" aria-label="Search Chats" className="z-[80] p-3 sm:p-6">
    <DialogBackdrop onClick={() => onOpenChange(false)} />
    <DialogPanel className="h-[min(820px,calc(100vh-24px))] w-[min(920px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
      <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-4 sm:px-5">
        <div><div className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Desktop</div><h2 className="mt-1 text-xl font-semibold">Search Chats</h2><p className="mt-1 text-sm text-[var(--app-text-muted)]">Search and manage conversations without loading chat bodies.</p></div>
        <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close Search Chats" />
      </div>
      <div className="shrink-0 border-b border-[var(--app-border)] px-4 py-3 sm:px-5">
        <div className="grid grid-cols-2 gap-2 text-xs sm:grid-cols-5">
          <div><b>{summary.active_conversation_count.toLocaleString()}</b> active conversations</div><div><b>{summary.archived_conversation_count.toLocaleString()}</b> archived</div><div><b>{summary.raw_session_count.toLocaleString()}</b> raw sessions</div><div><b>{summary.agent_child_count.toLocaleString()}</b> agent runs</div><div><b>{formatBytes(summary.logical_content_bytes)}</b> approx. logical</div>
        </div>
      </div>
      <div className="flex shrink-0 flex-col gap-3 border-b border-[var(--app-border)] px-4 py-3 sm:px-5">
        <form className="grid gap-2" onSubmit={handleSubmit}>
          <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]"><label className="relative"><Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)]" /><input value={queryDraft} onChange={(event) => setQueryDraft(event.target.value)} placeholder="Search titles and messages" className="h-10 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] pl-9 pr-3 text-sm" /></label><Button type="submit" className="h-10 rounded-xl px-5">Search</Button></div>
          <div className="grid gap-2 sm:grid-cols-3"><label className="grid gap-1 text-xs text-[var(--app-text-muted)]">Archived<select value={archivedMode} onChange={(event) => setArchivedMode(event.target.value as ArchivedFilter)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm"><option value="exclude">Hide archived</option><option value="include">Include archived</option><option value="only">Archived only</option></select></label><label className="grid gap-1 text-xs text-[var(--app-text-muted)]">Active after<input type="date" value={fromDate} onChange={(event) => setFromDate(event.target.value)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm" /></label><label className="grid gap-1 text-xs text-[var(--app-text-muted)]">Active before<input type="date" value={toDate} onChange={(event) => setToDate(event.target.value)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm" /></label></div>
        </form>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
          <div className="flex flex-wrap items-end gap-2"><label className="grid gap-1 text-xs text-[var(--app-text-muted)]">Cleanup conversations inactive for<select value={cleanupPreset} onChange={(event) => setCleanupPreset(event.target.value as CleanupPreset)} className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-sm"><option value="14">2 weeks</option><option value="30">30 days</option><option value="90">90 days</option><option value="custom">Before date…</option></select></label>{cleanupPreset === 'custom' ? <input aria-label="Cleanup before date" type="date" value={cleanupDate} onChange={(event) => setCleanupDate(event.target.value)} className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-sm" /> : null}<Button variant="outline" className="h-8" disabled={cleanupPending} onClick={() => void previewCleanup()}>Preview</Button></div>
          {deletePreview ? <div className="mt-3 text-xs"><p><b>{deletePreview.conversation_count}</b> conversations / <b>{deletePreview.session_count}</b> sessions ({deletePreview.child_count} descendants), approximately <b>{formatBytes(deletePreview.logical_bytes)}</b>.</p>{deletePreview.active_run_count || deletePreview.pending_approval_count ? <p className="mt-1 font-semibold text-[var(--app-error)]">Blocked: {deletePreview.active_run_count} running and {deletePreview.pending_approval_count} awaiting approval.</p> : null}{deletePreview.recent_75_overlap_count > 0 ? <label className="mt-2 flex gap-2 font-semibold text-[var(--app-warning)]"><input type="checkbox" checked={confirmRecent} onChange={(event) => setConfirmRecent(event.target.checked)} />I understand this includes {deletePreview.recent_75_overlap_count} of my newest 75 conversations.</label> : null}<Button className="mt-2 h-8" disabled={cleanupPending || deletePreview.session_count === 0 || deletePreview.active_run_count > 0 || deletePreview.pending_approval_count > 0 || (deletePreview.recent_75_overlap_count > 0 && !confirmRecent)} onClick={() => void executeCleanup()}><Trash2 size={14} />Delete previewed conversations</Button></div> : null}
        </div>
        {recentSearches.length ? <div className="flex flex-wrap gap-2"><span className="text-xs text-[var(--app-text-subtle)]">Recent:</span>{recentSearches.map((term) => <button key={term} type="button" onClick={() => { setQueryDraft(term); setSubmittedQuery(term) }} className="rounded-full border border-[var(--app-border)] px-2 py-0.5 text-xs">{term}</button>)}<button type="button" onClick={() => { setRecentSearches([]); saveRecentSearches([]) }} className="text-xs text-[var(--app-text-muted)]">Clear</button></div> : null}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">{error ? <div className="m-4 rounded-xl border border-[var(--app-error)] p-3 text-sm text-[var(--app-error)]">{error}</div> : null}{loading ? <div className="grid min-h-[200px] place-items-center"><LoaderCircle className="animate-spin" /></div> : groups.length === 0 ? <div className="grid min-h-[200px] place-items-center text-sm text-[var(--app-text-muted)]">No chats found</div> : <div className="divide-y divide-[var(--app-border)]">{groups.map(([rootID, group]) => <div key={rootID}>{group.root ? renderResult(group.root) : <div className="px-5 py-2 text-xs font-semibold text-[var(--app-text-muted)]">Parent conversation · {rootID.replace(/^unlinked:/, '')}</div>}{group.children.map((child) => renderResult(child, true))}</div>)}</div>}</div>
      <div className="flex shrink-0 items-center justify-between border-t border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 text-xs text-[var(--app-text-subtle)] sm:px-5"><span>Loaded {items.length.toLocaleString()} raw result{items.length === 1 ? '' : 's'} in {groups.length.toLocaleString()} conversation group{groups.length === 1 ? '' : 's'}</span>{hasMore ? <Button variant="outline" className="h-9" disabled={loadingMore} onClick={() => void runSearch(nextCursor)}>{loadingMore ? <LoaderCircle size={15} className="animate-spin" /> : null}Load 50 more</Button> : null}</div>
    </DialogPanel>
  </Dialog>
}
