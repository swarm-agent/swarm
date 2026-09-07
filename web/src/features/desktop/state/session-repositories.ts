import type { SessionRepository, SessionRepositoriesResponse } from '../git/types'

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
  private pageCount = 1
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
    if (append && this.pageCount >= 10) { this.update({ error: 'Loaded the 10-page inventory budget (at most 200 rows); open a child session for narrower history.' }); return }
    this.controller?.abort()
    const controller = new AbortController()
    this.controller = controller
    const generation = ++this.generation
    const cursor = append ? this.state.nextCursor : ''
    const invalidation = this.invalidation
    this.update({ loading: true, error: '', ...(append ? {} : { stale: true }) })
    try {
      let page = await this.fetchPage(cursor, controller.signal)
      let incoming = page.items
      let pages = 1
      const seen = new Set<string>([cursor])
      // Rebuild loaded pages from the first fresh cursor; never reuse old cursors.
      while (!append && pages < this.pageCount && page.next_cursor) {
        if (generation !== this.generation || controller.signal.aborted) return
        if (seen.has(page.next_cursor)) throw new Error('Repository pagination did not advance; refresh required')
        seen.add(page.next_cursor)
        page = await this.fetchPage(page.next_cursor, controller.signal)
        incoming = mergeRepositoryRows(incoming, page.items)
        pages++
      }
      if (generation !== this.generation || controller.signal.aborted) return
      if (page.next_cursor && page.next_cursor === cursor) throw new Error('Repository pagination did not advance; refresh required')
      const items = mergeRepositoryRows(append ? this.state.items : [], incoming)
      this.pageCount = append ? this.pageCount + 1 : pages
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
  return type.startsWith('realtime.') || type.startsWith('mutation.') || type === 'reconnect.applySnapshot'
    || type === 'snapshot.apply' || type === 'hydrate.apply' || type === 'syncStream.applyBatch'
}
