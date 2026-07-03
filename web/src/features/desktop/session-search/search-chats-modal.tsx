import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { LoaderCircle, MessageSquare, Search } from 'lucide-react'
import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { ModalCloseButton } from '../../../components/ui/modal-close-button'
import { searchDesktopSessions, type DesktopSessionSearchItem } from './session-search-api'

const PAGE_SIZE = 50
const RECENT_SEARCHES_KEY = 'swarm.web.desktop.session_search.recents'
const MAX_RECENT_SEARCHES = 8

type ArchivedFilter = 'exclude' | 'include' | 'only'

function loadRecentSearches(): string[] {
  if (typeof window === 'undefined') return []
  try {
    const raw = window.localStorage.getItem(RECENT_SEARCHES_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed)
      ? parsed.map((value) => typeof value === 'string' ? value.trim() : '').filter(Boolean).slice(0, MAX_RECENT_SEARCHES)
      : []
  } catch {
    return []
  }
}

function saveRecentSearches(values: string[]) {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(RECENT_SEARCHES_KEY, JSON.stringify(values.slice(0, MAX_RECENT_SEARCHES)))
}

function rememberSearchTerm(term: string, current: string[]): string[] {
  const normalized = term.trim()
  if (!normalized) return current
  const next = [normalized, ...current.filter((value) => value.toLowerCase() !== normalized.toLowerCase())].slice(0, MAX_RECENT_SEARCHES)
  saveRecentSearches(next)
  return next
}

function timestampFromDateInput(value: string, endOfDay = false): number | undefined {
  if (!value) return undefined
  const timestamp = endOfDay
    ? new Date(`${value}T23:59:59.999`).getTime()
    : new Date(`${value}T00:00:00.000`).getTime()
  return Number.isFinite(timestamp) ? timestamp : undefined
}

function formatSessionTime(timestamp: number): string {
  if (!timestamp) return 'No activity yet'
  return new Date(timestamp).toLocaleString()
}

function sessionTitle(item: DesktopSessionSearchItem): string {
  return item.title?.trim() || item.id
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
  const [nextCursor, setNextCursor] = useState('')
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [recentSearches, setRecentSearches] = useState<string[]>(loadRecentSearches)

  const runSearch = useCallback(async (cursor = '') => {
    const append = cursor.trim() !== ''
    setError(null)
    if (append) setLoadingMore(true)
    else setLoading(true)
    try {
      const result = await searchDesktopSessions({
        query: submittedQuery,
        archived_mode: archivedMode,
        global: true,
        from_updated_at: timestampFromDateInput(fromDate),
        to_updated_at: timestampFromDateInput(toDate, true),
        cursor: cursor || undefined,
        limit: PAGE_SIZE,
      })
      setItems((current) => append ? [...current, ...result.items] : result.items)
      setNextCursor(result.pagination.next_cursor ?? '')
      setHasMore(Boolean(result.pagination.has_more && result.pagination.next_cursor))
      if (!append) {
        setRecentSearches((current) => rememberSearchTerm(submittedQuery, current))
      }
    } catch (searchError) {
      setError(searchError instanceof Error ? searchError.message : 'Search failed')
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [archivedMode, fromDate, submittedQuery, toDate])

  useEffect(() => {
    if (!open) return
    void runSearch()
  }, [open, runSearch])

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setSubmittedQuery(queryDraft.trim())
  }

  const clearRecentSearches = () => {
    setRecentSearches([])
    saveRecentSearches([])
  }

  if (!open) return null

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Search Chats" className="z-[80] p-3 sm:p-6">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="h-[min(760px,calc(100vh-24px))] w-[min(860px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:h-[min(760px,calc(100vh-48px))] sm:w-[min(900px,calc(100vw-48px))]">
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-4 sm:px-5">
          <div className="flex min-w-0 items-start gap-3">
            <div className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)]">
              <Search size={20} />
            </div>
            <div className="min-w-0">
              <div className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Desktop</div>
              <h2 className="mt-1 text-xl font-semibold text-[var(--app-text)]">Search Chats</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Search metadata and snippets; full chats load only after opening a result.</p>
            </div>
          </div>
          <ModalCloseButton onClick={() => onOpenChange(false)} aria-label="Close Search Chats" />
        </div>

        <div className="flex shrink-0 flex-col gap-3 border-b border-[var(--app-border)] px-4 py-4 sm:px-5">
          <form className="grid gap-3" onSubmit={handleSubmit}>
            <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto]">
              <label className="relative min-w-0">
                <span className="sr-only">Search chats</span>
                <Search size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--app-text-subtle)]" />
                <input
                  value={queryDraft}
                  onChange={(event) => setQueryDraft(event.target.value)}
                  placeholder="Search titles and messages"
                  className="h-10 w-full rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] pl-9 pr-3 text-sm outline-none focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
                />
              </label>
              <Button type="submit" className="h-10 rounded-xl px-5">Search</Button>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              <label className="grid gap-1 text-xs text-[var(--app-text-muted)]">
                Archived
                <select value={archivedMode} onChange={(event) => setArchivedMode(event.target.value as ArchivedFilter)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm text-[var(--app-text)]">
                  <option value="exclude">Hide archived</option>
                  <option value="include">Include archived</option>
                  <option value="only">Archived only</option>
                </select>
              </label>
              <label className="grid gap-1 text-xs text-[var(--app-text-muted)]">
                Active after
                <input type="date" value={fromDate} onChange={(event) => setFromDate(event.target.value)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm text-[var(--app-text)]" />
              </label>
              <label className="grid gap-1 text-xs text-[var(--app-text-muted)]">
                Active before
                <input type="date" value={toDate} onChange={(event) => setToDate(event.target.value)} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm text-[var(--app-text)]" />
              </label>
            </div>
          </form>

          <div className="grid gap-2">
            <div className="flex items-center justify-between gap-3">
              <p className="text-[11px] font-medium uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Recent searches</p>
              {recentSearches.length > 0 ? <button type="button" onClick={clearRecentSearches} className="text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]">Clear</button> : null}
            </div>
            {recentSearches.length > 0 ? (
              <div className="flex flex-wrap gap-2">
                {recentSearches.map((term) => (
                  <button key={term} type="button" onClick={() => { setQueryDraft(term); setSubmittedQuery(term) }} className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                    {term}
                  </button>
                ))}
              </div>
            ) : <p className="text-xs text-[var(--app-text-subtle)]">No recent searches yet.</p>}
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {error ? (
            <div className="m-4 rounded-xl border border-[var(--app-error)] bg-[color-mix(in_srgb,var(--app-error)_10%,var(--app-surface))] p-3 text-sm text-[var(--app-error)]">{error}</div>
          ) : null}
          {loading ? (
            <div className="grid min-h-[260px] place-items-center text-[var(--app-text-muted)]">
              <div className="grid justify-items-center gap-3 text-sm">
                <LoaderCircle size={22} className="animate-spin" />
                Loading chats…
              </div>
            </div>
          ) : items.length === 0 ? (
            <div className="grid min-h-[260px] place-items-center px-6 text-center">
              <div>
                <div className="text-base font-semibold text-[var(--app-text)]">No chats found</div>
                <p className="mt-2 text-sm text-[var(--app-text-muted)]">Try a different search term, archive filter, or date range.</p>
              </div>
            </div>
          ) : (
            <div className="divide-y divide-[var(--app-border)]">
              {items.map((item) => {
                const snippet = item.snippets?.[0]
                return (
                  <button key={item.id} type="button" onClick={() => onOpenSession(item)} className="grid w-full gap-2 px-4 py-3 text-left transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--app-focus-ring)] sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-4 sm:px-5">
                    <div className="min-w-0">
                      <div className="flex min-w-0 items-center gap-2">
                        <MessageSquare size={14} className="shrink-0 text-[var(--app-text-subtle)]" />
                        <div className="truncate text-sm font-semibold text-[var(--app-text)]">{sessionTitle(item)}</div>
                        {item.archived ? <span className="shrink-0 rounded-full border border-[var(--app-border)] px-2 py-0.5 text-[10px] uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">Archived</span> : null}
                      </div>
                      <div className="mt-1 truncate text-xs text-[var(--app-text-subtle)]">{item.workspace_name || item.workspace_path || 'Unknown workspace'}</div>
                      {snippet?.text ? <div className="mt-2 line-clamp-2 text-sm leading-5 text-[var(--app-text-muted)]">{snippet.text}</div> : null}
                    </div>
                    <div className="shrink-0 text-xs text-[var(--app-text-subtle)] sm:text-right">
                      <div>{item.message_count.toLocaleString()} messages</div>
                      <div className="mt-1">{formatSessionTime(item.updated_at || item.last_message_at)}</div>
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>

        <div className="flex shrink-0 items-center justify-between gap-3 border-t border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 sm:px-5">
          <div className="text-xs text-[var(--app-text-subtle)]">Showing {items.length.toLocaleString()} chat{items.length === 1 ? '' : 's'}</div>
          {hasMore ? (
            <Button variant="outline" className="h-9 rounded-xl px-4" disabled={loadingMore} onClick={() => void runSearch(nextCursor)}>
              {loadingMore ? <LoaderCircle size={15} className="animate-spin" /> : null}
              Load 50 more
            </Button>
          ) : null}
        </div>
      </DialogPanel>
    </Dialog>
  )
}
