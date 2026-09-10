import type { SessionRepository, SessionRepositoriesResponse } from '../git/types'
import type { CacheEvent, DesktopV3CacheAction, DesktopV3CacheState } from './desktop-v3-cache-types'

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
      const initial = items.find(row => row.kind === 'parent' && row.default) || items.find(row => row.kind === 'parent') || items[0]
      const selectedKey = this.state.selectedKey || (initial ? repositoryKey(initial) : '')
      this.update({ items, selectedKey, nextCursor: page.next_cursor || '', historyCoverage: page.history_coverage, stale: invalidation !== this.invalidation, loading: false })
    } catch (error) {
      if (generation !== this.generation || controller.signal.aborted) return
      this.update({ loading: false, stale: true, error: error instanceof Error ? error.message : String(error) })
    }
  }
}

export function repositoryDialogTargetMatches(target: { sessionId: string; workspacePath: string }, selected: { sessionId: string; workspacePath: string }, enabled: boolean) {
  return enabled && target.sessionId === selected.sessionId && target.workspacePath === selected.workspacePath
}

export function repositoryMutationSupported(row: SessionRepository | undefined, owner: { id: string; path: string; worktree: boolean; branch?: string }, stale: boolean) {
  return Boolean(row && !stale && row.availability === 'available' && row.status?.has_git && !row.files_truncated
    && row.active !== false && row.status.workspace_path === row.workspace_path
    && row.session_id === owner.id && row.workspace_path === owner.path
    && (!owner.branch || row.branch === owner.branch)
    && row.kind === (owner.worktree ? 'parent' : 'source'))
}

// Resolve newly hydrated children from the canonical cache, not the paged inventory.
// These IDs trigger reads only; the repository API remains authorization authority.
export function repositoryOwnerIds(parent: string, rows: SessionRepository[], state: DesktopV3CacheState) {
  const ids = new Set([parent, ...rows.map(row => row.session_id)])
  for (const [id, record] of Object.entries(state.sessionsById)) {
    if (record.kind === 'full' && record.session.metadata?.parent_session_id === parent) ids.add(id)
  }
  return ids
}

export function repositoryEventInvalidates(action: DesktopV3CacheAction, sessionIds: ReadonlySet<string>, attachmentsOnly = false) {
  const allocated = (event: CacheEvent) => {
    if (attachmentsOnly || event.eventType !== 'session.tool.delta') return false
    const raw = event.payload.output || event.payload.output_delta || event.payload.delta
    if (typeof raw !== 'string' || raw.length > 262144) return false
    try {
      const progress = JSON.parse(raw)
      return progress?.tool === 'task' && progress.phase === 'repository.allocated'
    } catch { return false }
  }
  const relevant = (event: CacheEvent) => sessionIds.has(event.sessionId)
    && (allocated(event) || event.eventType === 'session.settings.updated' || event.eventType === 'session.created' || event.eventType === 'session.metadata.updated'
      || (!attachmentsOnly && /^(session\.(tool|run)\.(completed|failed|cancelled)|session\.worktree\.)/.test(event.eventType)))
  switch (action.type) {
    case 'realtime.applyEvent': return relevant(action.event)
    case 'syncStream.applyBatch':
    case 'liveRun.mergeRepairEvents': return action.events.some(relevant)
    case 'mutation.sessionSettingsResult': return sessionIds.has(action.raw.session_id || '')
    case 'hydrate.apply': return action.requestedSessionIds.some(id => sessionIds.has(id))
    case 'snapshot.apply':
    case 'reconnect.applySnapshot':
    case 'realtime.cursorError': return true
    default: return false
  }
}

// Event-driven single-flight reads. A change during a read queues one follow-up;
// completion notifications wake it without polling or repeatedly cancelling reads.
export function scheduleRepositoryRefresh(target: { state: { loading: boolean; stale: boolean }; subscribe(listener: () => void): () => void; invalidate(): void; refresh(): Promise<void>; dispose(): void }, visible: () => boolean) {
  let pending = false
  let queued = false
  let disposed = false
  const schedule = () => {
    if (disposed || queued || !pending || !visible() || target.state.loading) return
    queued = true
    queueMicrotask(() => {
      queued = false
      if (disposed || !pending || !visible() || target.state.loading) return
      pending = false
      void target.refresh()
    })
  }
  const unsubscribe = target.subscribe(schedule)
  return {
    invalidate() { pending = true; target.invalidate(); schedule() },
    visibilityChanged() { if (visible() && target.state.stale) { pending = true; schedule() } },
    dispose() { disposed = true; unsubscribe(); target.dispose() },
  }
}
