import type { DesignCatalog, DesignHistory } from '../session-v3/design-api'

export interface DesignResourceSnapshot<T> { data?: T; loading: boolean; error?: string }
/** One request at a time; invalidations during reads coalesce at completion, not on a timer. */
export class DesignResource<T> {
  private snapshot: DesignResourceSnapshot<T> = { loading: false }
  private listeners = new Set<() => void>()
  private pending = false
  private flight?: Promise<void>
  private generation = 0
  private disposed = false
  constructor(private readonly load: () => Promise<T>) {}
  getSnapshot = () => this.snapshot
  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
      if (!this.active) this.reset()
    }
  }
  /** Fence every outstanding response before dropping private metadata. */
  reset = () => {
    this.generation++
    this.pending = false
    this.flight = undefined
    this.publish({ loading: false })
  }
  dispose = () => { this.disposed = true; this.reset() }
  get active() { return this.listeners.size > 0 }
  private publish(snapshot: DesignResourceSnapshot<T>) { this.snapshot = snapshot; this.listeners.forEach(listener => listener()) }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve()
    this.pending = true
    if (this.flight) return this.flight
    const generation = this.generation
    this.flight = Promise.resolve().then(async () => {
      while (generation === this.generation && this.pending) {
        this.pending = false
        this.publish({ ...this.snapshot, loading: true, error: undefined })
        try {
          const data = await this.load()
          if (generation === this.generation) this.publish({ data, loading: false })
        } catch (error) {
          if (generation === this.generation) this.publish({ loading: false, error: error instanceof Error ? error.message : String(error) })
        }
      }
    }).finally(() => { if (generation === this.generation) this.flight = undefined })
    return this.flight
  }
}

export interface DesignDataAPI {
  catalog: (session: string, after: string) => Promise<DesignCatalog>
  history: (session: string, artifact: string, after: number) => Promise<DesignHistory>
}
/** Canonical design read models. Viewed revisions remain local UI navigation, never selection. */
export class DesktopDesignState {
  private catalogs = new Map<string, { resource: DesignResource<DesignCatalog>; pages: number }>()
  private histories = new Map<string, { session: string; resource: DesignResource<DesignHistory>; pages: number }>()
  constructor(private readonly api: DesignDataAPI) {}
  /** Auth reset preserves subscribed resource identities, but never their private bytes. */
  reset() {
    for (const entry of this.catalogs.values()) { entry.pages = 1; entry.resource.reset() }
    for (const entry of this.histories.values()) { entry.pages = 1; entry.resource.reset() }
  }
  private prune<T>(entries: Map<string, { resource: DesignResource<T> }>) {
    // Active views are never evicted; inactive metadata is bounded independently.
    const inactive = [...entries].filter(([, entry]) => !entry.resource.active)
    for (const [key, entry] of inactive.slice(0, Math.max(0, inactive.length - 32))) {
      entry.resource.dispose()
      entries.delete(key)
    }
  }
  catalog(session: string) {
    let entry = this.catalogs.get(session)
    if (!entry) {
      const created: { pages: number; resource: DesignResource<DesignCatalog> } = { pages: 1, resource: new DesignResource<DesignCatalog>(async () => {
        const requests: DesignCatalog['requests'] = []
        let cursor = ''
        for (let page = 0; page < created.pages; page++) {
          const result = await this.api.catalog(session, cursor)
          requests.push(...result.requests)
          if (result.next_cursor && result.next_cursor === cursor) throw new Error('Design catalog cursor did not advance')
          cursor = result.next_cursor
          if (!cursor) break
        }
        return { requests, next_cursor: cursor }
      }) }
      entry = created
      this.catalogs.set(session, entry)
      this.prune(this.catalogs)
    }
    return entry.resource
  }
  history(session: string, artifact: string) {
    const key = JSON.stringify([session, artifact])
    let entry = this.histories.get(key)
    if (!entry) {
      const created: { session: string; pages: number; resource: DesignResource<DesignHistory> } = { session, pages: 1, resource: new DesignResource<DesignHistory>(async () => {
        const first = await this.api.history(session, artifact, 0)
        const revisions = [...first.revisions]
        let last = first.revisions
        for (let page = 1; page < created.pages && last.length === 50; page++) {
          const after = last[last.length - 1].ref.revision
          const next = await this.api.history(session, artifact, after)
          if (next.revisions.some(row => row.ref.revision <= after)) throw new Error('Design history did not advance')
          last = next.revisions
          revisions.push(...last)
        }
        return { artifact: first.artifact, revisions }
      }) }
      entry = created
      this.histories.set(key, entry)
      this.prune(this.histories)
    }
    return entry.resource
  }
  moreRequests(session: string) { this.catalog(session); this.catalogs.get(session)!.pages++; return this.catalog(session).refresh() }
  moreHistory(session: string, artifact: string) { const resource = this.history(session, artifact); this.histories.get(JSON.stringify([session, artifact]))!.pages++; return resource.refresh() }
  invalidate(session?: string) {
    for (const [id, entry] of this.catalogs) if ((!session || id === session) && entry.resource.active) void entry.resource.refresh()
    for (const entry of this.histories.values()) if ((!session || entry.session === session) && entry.resource.active) void entry.resource.refresh()
  }
}

/** Closed event allowlist: usage/token chatter and unrelated session events do not invalidate. */
export function designEventSession(frame: { kind?: string; session_id?: string; event_type?: string; event?: unknown }): string | undefined {
  if (frame.kind !== 'event' || !frame.event || typeof frame.event !== 'object') return
  const event = frame.event as { event_type?: string; session_id?: string }
  const type = event.event_type || frame.event_type
  if (type !== 'design.accepted' && type !== 'design.updated') return
  return event.session_id || frame.session_id || undefined
}
