import type { DesignCatalog, DesignHistory, ProjectDesignCatalog, DesignEditPage } from '../session-v3/design-api'

export interface DesignResourceSnapshot<T> { data?: T; loading: boolean; error?: string }
/** One request at a time; invalidations during reads coalesce at completion, not on a timer. */
export class DesignResource<T> {
  private snapshot: DesignResourceSnapshot<T> = { loading: false }
  private listeners = new Set<() => void>()
  private pending = false
  private flight?: Promise<void>
  private generation = 0
  private disposed = false
  private controller?: AbortController
  constructor(private readonly load: (signal: AbortSignal) => Promise<T>) {}
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
    this.controller?.abort()
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
          this.controller = new AbortController()
          const data = await this.load(this.controller.signal)
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
  edits?: (session: string, before: number, signal?: AbortSignal) => Promise<DesignEditPage>
  project?: (project: string, after: string, signal?: AbortSignal) => Promise<ProjectDesignCatalog>
  catalog: (session: string, after: string, signal?: AbortSignal) => Promise<DesignCatalog>
  history: (session: string, artifact: string, after: number, signal?: AbortSignal) => Promise<DesignHistory>
}
/** Canonical design read models. Viewed revisions remain local UI navigation, never selection. */
export class DesktopDesignState {
  private edits = new Map<string, { pages: number; resource: DesignResource<DesignEditPage> }>()
  private projects = new Map<string, { resource: DesignResource<ProjectDesignCatalog>; pages: number }>()
  private catalogs = new Map<string, { resource: DesignResource<DesignCatalog>; pages: number }>()
  private histories = new Map<string, { session: string; resource: DesignResource<DesignHistory>; pages: number }>()
  constructor(private readonly api: DesignDataAPI) {}
  /** Auth reset preserves subscribed resource identities, but never their private bytes. */
  reset() {
    for (const entry of this.edits.values()) { entry.pages = 1; entry.resource.reset() }
    for (const entry of this.projects.values()) { entry.pages = 1; entry.resource.reset() }
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
  editRequests(session: string) {
    let entry = this.edits.get(session)
    if (!entry) {
      const created: { pages: number; resource: DesignResource<DesignEditPage> } = { pages: 1, resource: new DesignResource(async signal => {
        if (!this.api.edits) throw new Error('Design edit history unavailable')
        const edits = new Map<string, DesignEditPage['edits'][number]>()
        let before = 0
        for (let page = 0; page < created.pages; page++) {
          const result = await this.api.edits(session, before, signal)
          for (const edit of result.edits) edits.set(edit.messageId, edit)
          if (result.nextBefore && before && result.nextBefore >= before) throw new Error('Design edit history did not advance')
          before = result.nextBefore
          if (!before) break
        }
        return { edits: [...edits.values()], nextBefore: before }
      }) }
      entry = created
      this.edits.set(session, entry)
      this.prune(this.edits)
    }
    return entry.resource
  }
  moreEdits(session: string) { const resource = this.editRequests(session); this.edits.get(session)!.pages++; return resource.refresh() }
  project(project: string) {
    let entry = this.projects.get(project)
    if (!entry) {
      const created: { pages: number; resource: DesignResource<ProjectDesignCatalog> } = { pages: 1, resource: new DesignResource<ProjectDesignCatalog>(async signal => {
        if (!this.api.project) throw new Error('Project design discovery is unavailable')
        const designs = new Map<string, ProjectDesignCatalog['designs'][number]>()
        const visited = new Set<string>()
        let cursor = ''
        for (let page = 0; page < created.pages; page++) {
          if (signal.aborted) throw new Error('Design discovery cancelled')
          visited.add(cursor)
          const result = await this.api.project(project, cursor, signal)
          for (const row of result.designs) {
            const key = JSON.stringify([row.request.parent_session_id, row.request.id])
            const previous = designs.get(key)
            if (!previous || (row.request.revision ?? 0) >= (previous.request.revision ?? 0)) designs.set(key, row)
          }
          cursor = result.next_cursor
          if (!cursor) break
          if (visited.has(cursor)) throw new Error('Project design cursor did not advance')
        }
        return { designs: [...designs.values()], next_cursor: cursor }
      }) }
      entry = created
      this.projects.set(project, entry)
      this.prune(this.projects)
    }
    return entry.resource
  }
  moreProject(project: string) { const resource = this.project(project); this.projects.get(project)!.pages++; return resource.refresh() }
  invalidateProject(project?: string) {
    for (const [id, entry] of this.projects) if ((!project || project === id) && entry.resource.active) {
      const sessions = new Set(entry.resource.getSnapshot().data?.designs.map(row => row.request.parent_session_id))
      for (const session of sessions) this.invalidate(session)
      void entry.resource.refresh()
    }
  }
  catalog(session: string) {
    let entry = this.catalogs.get(session)
    if (!entry) {
      const created: { pages: number; resource: DesignResource<DesignCatalog> } = { pages: 1, resource: new DesignResource<DesignCatalog>(async signal => {
        const requests: DesignCatalog['requests'] = []
        let cursor = ''
        for (let page = 0; page < created.pages; page++) {
          const result = await this.api.catalog(session, cursor, signal)
          requests.push(...result.requests)
          if (result.next_cursor && result.next_cursor === cursor) throw new Error('Design catalog cursor did not advance')
          cursor = result.next_cursor
          if (!cursor) break
        }
        return { requests: [...new Map(requests.map(row => [row.id, row])).values()], next_cursor: cursor }
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
      const created: { session: string; pages: number; resource: DesignResource<DesignHistory> } = { session, pages: 1, resource: new DesignResource<DesignHistory>(async signal => {
        const first = await this.api.history(session, artifact, 0, signal)
        const revisions = [...first.revisions]
        let last = first.revisions
        for (let page = 1; page < created.pages && last.length === 50; page++) {
          const after = last[last.length - 1].ref.revision
          const next = await this.api.history(session, artifact, after, signal)
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
    if (!session) this.invalidateProject()
    for (const [id, entry] of this.edits) if ((!session || session === id) && entry.resource.active) void entry.resource.refresh()
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
