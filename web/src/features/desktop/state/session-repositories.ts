import type { SessionRepository, SessionRepositoriesResponse } from '../git/types'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'

export const repositoryKey = (row: SessionRepository) => JSON.stringify([row.id, row.session_id, row.workspace_path])
export const repositoryGroupKey = (row: SessionRepository) => JSON.stringify([row.workspace_id, row.source_path])
export function mergeRepositoryRows(previous: SessionRepository[], incoming: SessionRepository[]) {
  const rows = new Map(previous.map(row => [repositoryKey(row), row]))
  for (const row of incoming) rows.set(repositoryKey(row), row)
  return [...rows.values()]
}

export interface RepositoryInventoryState {
  items: SessionRepository[]
  selectedKey: string
  loading: boolean
  stale: boolean
  error: string
  nextCursor: string
  historyCoverage: string
}

// One transport at a time; generations reject even transports that ignore abort.
// Refresh never merges an old opaque cursor into a new inventory revision.
export class SessionRepositoryInventory {
  state: RepositoryInventoryState = { items: [], selectedKey: '', loading: false, stale: true, error: '', nextCursor: '', historyCoverage: '' }
  private generation = 0
  private invalidation = 0
  private controller?: AbortController
  private listeners = new Set<() => void>()
  constructor(private fetchPage: (cursor: string, signal: AbortSignal) => Promise<SessionRepositoriesResponse>) {}
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  snapshot = () => this.state
  private update(patch: Partial<RepositoryInventoryState>) {
    this.state = { ...this.state, ...patch }
    this.listeners.forEach(listener => listener())
  }
  select = (key: string) => { if (this.state.items.some(row => repositoryKey(row) === key)) this.update({ selectedKey: key }) }
  invalidate = () => { this.invalidation++; this.update({ stale: true }) }
  dispose = () => { this.generation++; this.controller?.abort(); this.update({ loading: false, stale: true }) }
  refresh = () => this.load(false)
  loadMore = () => this.load(true)
  private async load(append: boolean) {
    if (append && (this.state.loading || this.state.stale || !this.state.nextCursor)) return
    this.controller?.abort()
    const controller = new AbortController()
    this.controller = controller
    const generation = ++this.generation
    const cursor = append ? this.state.nextCursor : ''
    const invalidation = this.invalidation
    this.update({ loading: true, error: '', ...(append ? {} : { stale: true }) })
    try {
      const page = await this.fetchPage(cursor, controller.signal)
      if (generation !== this.generation || controller.signal.aborted) return
      if (page.next_cursor && page.next_cursor === cursor) throw new Error('Repository pagination did not advance; refresh required')
      // Continue indefinitely by replacing the full window, not accumulating history.
      // Refresh starts at the first page; the exact selection can remain unloaded.
      const previous = append && this.state.items.length + page.items.length <= 200 ? this.state.items : []
      const items = mergeRepositoryRows(previous, page.items).slice(0, 200)
      // A missing selection remains unresolved, never silently replaced by a default.
      const selectedKey = this.state.selectedKey || (items[0] ? repositoryKey(items[0]) : '')
      this.update({ items, selectedKey, nextCursor: page.next_cursor || '', historyCoverage: page.history_coverage, stale: invalidation !== this.invalidation, loading: false })
    } catch (error) {
      if (generation !== this.generation || controller.signal.aborted) return
      this.update({ loading: false, stale: true, error: error instanceof Error ? error.message : String(error) })
    }
  }
}

export function repositoryMutationSupported(row: SessionRepository | undefined, owner: { id: string; path: string; worktree: boolean; branch?: string }, stale: boolean) {
  return Boolean(row && !stale && row.availability === 'available' && row.status?.has_git && !row.files_truncated
    && row.session_id === owner.id && row.workspace_path === owner.path
    && (!owner.branch || row.branch === owner.branch)
    && row.kind === (owner.worktree ? 'parent' : 'source'))
}

export function repositoryEventInvalidates(type: string) {
  return repositoryInvalidationActions.has(type)
}

// Exact discriminants from DesktopV3CacheAction; live token patches and resume
// bookkeeping do not change repository ownership or disk status.
const repositoryInvalidationActions: ReadonlySet<string> = new Set([
  'snapshot.apply', 'hydrate.apply', 'syncStream.applyBatch', 'reconnect.applySnapshot',
  'realtime.applyEvent', 'realtime.worksetSessionDiscovered', 'realtime.worksetSessionUpdated',
  'realtime.worksetSessionRemoved', 'realtime.cursorError', 'realtime.statusChanged',
  'mutation.sessionCreateResult', 'mutation.sessionSettingsResult', 'mutation.sessionArchiveResult',
  'liveRun.mergeRepairEvents',
] satisfies DesktopV3CacheAction['type'][])

// Fixed coalescing window rather than trailing debounce: sustained changes cannot
// starve reads. An invalidation during a request always gets a subsequent read.
export function scheduleRepositoryRefresh(target: { state: { loading: boolean }; invalidate(): void; refresh(): Promise<void>; dispose(): void }, visible: () => boolean, delay = 1_000) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let disposed = false
  const schedule = () => {
    if (disposed || timer || !visible()) return
    timer = setTimeout(() => {
      timer = undefined
      if (!visible()) return
      if (target.state.loading) { schedule(); return }
      void target.refresh()
    }, delay)
  }
  return {
    invalidate() { target.invalidate(); schedule() },
    visibilityChanged() { if (!visible()) { if (timer) clearTimeout(timer); timer = undefined; target.dispose() } else { target.invalidate(); schedule() } },
    dispose() { disposed = true; if (timer) clearTimeout(timer); target.dispose() },
  }
}
