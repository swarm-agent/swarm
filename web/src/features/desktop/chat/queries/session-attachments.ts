import { requestJson } from '../../../../app/api'

// Read-only projection of /v3/sessions/{id}/repositories, not attachment authority.
export interface AttachmentRepository {
  id: string
  workspace_id: string
  workspace_name: string
  source_path: string
  kind: string
  lifecycle?: string
  session_id?: string
  attached: boolean
  default: boolean
  availability: string
  error?: string
}
export interface AttachmentRepositoryPage {
  ok: boolean
  items: AttachmentRepository[]
  next_cursor?: string
}

export async function fetchAttachmentPage(sessionId: string, cursor: string, signal?: AbortSignal): Promise<AttachmentRepositoryPage> {
  const params = new URLSearchParams({ limit: '20' })
  if (cursor) params.set('cursor', cursor)
  const page = await requestJson<AttachmentRepositoryPage>(`/v3/sessions/${encodeURIComponent(sessionId)}/repositories?${params}`, { signal })
  if (!page.ok || !Array.isArray(page.items)) throw new Error('Workspace inventory unavailable')
  return page
}

export function selectSessionAttachments(pages: AttachmentRepositoryPage[]): AttachmentRepository[] {
  const attachments = new Map<string, AttachmentRepository>()
  for (const page of pages) {
    for (const item of page.items) {
      if (item.attached !== true || item.kind !== 'source' || !item.workspace_id) continue
      // The first occurrence is the current context; later retained contexts must
      // not replace it. Names and physical paths are not workspace identities.
      if (!attachments.has(item.workspace_id)) attachments.set(item.workspace_id, item)
    }
  }
  return [...attachments.values()].slice(0, 64)
}

// Only live delegated execution is evidence of additional work in progress.
// Dirty source checkouts and retained/completed lanes are not session activity.
export function selectWorkingRepositorySources(items: AttachmentRepository[]): string[] {
  return [...new Set(items.filter(item =>
    (item.kind === 'worker' || item.kind === 'lane') &&
    (item.lifecycle === 'running' || item.lifecycle === 'in_progress') && item.source_path,
  ).map(item => item.source_path))].slice(0, 64)
}

export function selectWorkingWorkspaces(items: AttachmentRepository[], sources: string[]): AttachmentRepository[] {
  const working = new Set(sources)
  return items.filter(item => item.default || working.has(item.source_path))
    .sort((a, b) => Number(b.default) - Number(a.default))
}

// Retain only attachment identities and bounded working source identities. Each gesture
// reads at most four pages; reaching later attachments has no total-page cutoff.
export class SessionAttachmentInventory {
  state = { items: [] as AttachmentRepository[], workingSources: [] as string[], workingSessionIds: [] as string[], loading: false, stale: true, error: false, nextCursor: '' }
  private listeners = new Set<() => void>()
  private controller?: AbortController
  private generation = 0
  private invalidation = 0
  constructor(private fetchPage: (cursor: string, signal: AbortSignal) => Promise<AttachmentRepositoryPage>) {}
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  snapshot = () => this.state
  private update(patch: Partial<typeof this.state>) { this.state = { ...this.state, ...patch }; this.listeners.forEach(listener => listener()) }
  invalidate = () => { this.invalidation++; this.update({ stale: true }) }
  dispose = () => { this.generation++; this.controller?.abort(); this.update({ loading: false, stale: true }) }
  refresh = () => this.load(false)
  loadMore = () => this.load(true)
  private async load(append: boolean) {
    if (this.state.loading || (append && (this.state.stale || !this.state.nextCursor))) return
    const controller = new AbortController()
    this.controller = controller
    const generation = ++this.generation
    const invalidation = this.invalidation
    let cursor = append ? this.state.nextCursor : ''
    let items = append ? this.state.items : []
    let workingSources = append ? this.state.workingSources : []
    let workingSessionIds = append ? this.state.workingSessionIds : []
    const seen = new Set<string>()
    this.update({ loading: true, error: false })
    try {
      for (let count = 0; count < 4; count++) {
        seen.add(cursor)
        const page = await this.fetchPage(cursor, controller.signal)
        if (generation !== this.generation || controller.signal.aborted) return
        items = selectSessionAttachments([{ ok: true, items }, page])
        workingSources = [...new Set([...workingSources, ...selectWorkingRepositorySources(page.items)])].slice(0, 64)
        workingSessionIds = [...new Set([...workingSessionIds, ...page.items.filter(item => item.kind === 'worker' && (item.lifecycle === 'running' || item.lifecycle === 'in_progress') && item.session_id).map(item => item.session_id!)])].slice(0, 64)
        cursor = page.next_cursor || ''
        if (cursor && seen.has(cursor)) throw new Error('Workspace pagination did not advance')
        if (!cursor) break
      }
      this.update({ items, workingSources, workingSessionIds, nextCursor: cursor, loading: false, stale: invalidation !== this.invalidation })
    } catch {
      if (generation === this.generation && !controller.signal.aborted) this.update({ loading: false, stale: true, error: true })
    }
  }
}
