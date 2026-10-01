import type { WorkerRead, WorkerReadResult, WorkerRecord, WorkerRun } from './desktop-workers-api'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'

export interface WorkerPage {
  input: WorkerRead
  data?: WorkerReadResult
  requestId?: string
  generation: number
  loading: boolean
  stale: boolean
  error?: string
  mutationError?: string
}
export type WorkerPages = Record<string, WorkerPage>
export type WorkerCacheAction =
  | { type: 'workers.begin'; key: string; input: WorkerRead; requestId: string }
  | { type: 'workers.finish'; key: string; requestId: string; generation: number; data?: WorkerReadResult; error?: string }
  | { type: 'workers.invalidate'; workerId?: string; accountScopeId?: string }
  | { type: 'workers.mutationError'; workerId?: string; accountScopeId: string; error?: string }
  | { type: 'workers.evict'; key: string }

export function workerPageKey(input: WorkerRead): string {
  return JSON.stringify(Object.entries(input).filter(([, value]) => value !== undefined).sort(([a], [b]) => a.localeCompare(b)))
}
export function workerReadRecords(data: WorkerReadResult): { workers: WorkerRecord[]; runs: WorkerRun[] } {
  if ('workers' in data) return { workers: data.workers, runs: [] }
  if ('worker' in data) return { workers: [data.worker], runs: [] }
  if ('runs' in data && Array.isArray(data.runs)) return { workers: [], runs: data.runs }
  if ('run' in data) return { workers: [], runs: [data.run] }
  if ('revisions' in data) return { workers: data.revisions.map(revision => revision.worker), runs: [] }
  return { workers: [], runs: [] }
}
function affected(input: WorkerRead, accountScopeId?: string, workerId?: string): boolean {
  return (!accountScopeId || input.accountScopeId === accountScopeId)
    && (!workerId || input.kind === 'list' || input.workerId === workerId)
}
export function reduceWorkerPages(pages: WorkerPages = {}, action: WorkerCacheAction): WorkerPages {
  if (action.type === 'workers.evict') {
    if (!(action.key in pages)) return pages
    const next = { ...pages }; delete next[action.key]; return next
  }
  if (action.type === 'workers.invalidate') {
    return Object.fromEntries(Object.entries(pages).map(([key, page]) => [key, affected(page.input, action.accountScopeId, action.workerId)
      ? { ...page, stale: true, generation: page.generation + 1 } : page]))
  }
  if (action.type === 'workers.mutationError') {
    return Object.fromEntries(Object.entries(pages).map(([key, page]) => [key, affected(page.input, action.accountScopeId, action.workerId)
      ? { ...page, mutationError: action.error } : page]))
  }
  const old = pages[action.key]
  if (action.type === 'workers.begin') return { ...pages, [action.key]: {
    ...old, input: action.input, requestId: action.requestId, generation: old?.generation ?? 0,
    loading: true, stale: old?.stale ?? true, error: undefined,
  } }
  if (!old || old.requestId !== action.requestId) return pages
  if (old.generation !== action.generation) return { ...pages, [action.key]: { ...old, loading: false, requestId: undefined } }
  return { ...pages, [action.key]: { ...old, data: action.data ?? old.data, loading: false,
    requestId: undefined, stale: Boolean(action.error), error: action.error } }
}

/** The list is the server's paginated order; detail is an independent canonical read. */
export function selectWorkerPage(state: DesktopV3CacheState, input: WorkerRead): WorkerPage | undefined {
  return state.workerPages[workerPageKey(input)]
}
