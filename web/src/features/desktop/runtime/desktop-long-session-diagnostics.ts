import { queryClient } from '../../../app/query-client'
import {
  getDesktopV3CacheSnapshot,
  subscribeDesktopV3Cache,
  type DesktopV3CacheMutation,
} from '../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../state/desktop-v3-cache-types'

const CONFIG_PATH = '/v3/diagnostics/long-session/config'
const SAMPLE_PATH = '/v3/diagnostics/long-session/samples'
const DEFAULT_INTERVAL_MS = 30_000
const MAX_LARGEST_SESSIONS = 10
const textEncoder = new TextEncoder()

interface DiagnosticsConfig {
  ok: boolean
  enabled: boolean
  sample_interval_ms?: number
}

export interface DesktopDiagnosticsSample {
  timestamp_ms: number
  js_heap_available: boolean
  js_heap_used_bytes?: number
  js_heap_total_bytes?: number
  event_loop_drift_ms: number
  long_task_count: number
  long_task_duration_ms: number
  dom_nodes: number
  cache_mutation_count: number
  cache_mutation_duration_ms: number
  cache_mutation_max_duration_ms: number
  query_cache_entries: number
  query_cache_estimated_bytes: number
  v3_cache_estimated_bytes: number
  v3_sections: Record<string, number>
  largest_sessions: Array<{ session_hash: string; estimated_bytes: number; messages: number; events: number }>
}

export interface DesktopDiagnosticsSamplerDeps {
  fetch: typeof fetch
  now: () => number
  setInterval: (callback: () => void, ms: number) => number
  clearInterval: (id: number) => void
  getCacheSnapshot: () => DesktopV3CacheState
  subscribeCache: typeof subscribeDesktopV3Cache
  observeLongTasks: (callback: (durationMS: number) => void) => () => void
  getDOMNodeCount: () => number
  getHeap: () => { used: number; total: number } | null
  getQueryCache: () => unknown[]
}

export interface DesktopDiagnosticsLease {
  enabled: boolean
  release: () => void
}

export async function retainDesktopLongSessionDiagnostics(
  deps: DesktopDiagnosticsSamplerDeps = browserDiagnosticsDeps(),
): Promise<DesktopDiagnosticsLease> {
  const response = await deps.fetch(CONFIG_PATH, { headers: { Accept: 'application/json' } })
  if (response.status === 404) return { enabled: false, release: () => {} }
  if (!response.ok) throw new Error(`long-session diagnostics config failed: ${response.status}`)
  const config = (await response.json()) as DiagnosticsConfig
  if (!config.enabled) return { enabled: false, release: () => {} }

  let released = false
  let expectedAt = deps.now() + (config.sample_interval_ms || DEFAULT_INTERVAL_MS)
  let eventLoopDriftMS = 0
  let longTaskCount = 0
  let longTaskDurationMS = 0
  let cacheMutationCount = 0
  let cacheMutationDurationMS = 0
  let cacheMutationMaxDurationMS = 0
  const unsubscribeCache = deps.subscribeCache((mutation?: DesktopV3CacheMutation) => {
    if (!mutation) return
    cacheMutationCount++
    cacheMutationDurationMS += mutation.durationMS
    cacheMutationMaxDurationMS = Math.max(cacheMutationMaxDurationMS, mutation.durationMS)
  })
  const stopLongTasks = deps.observeLongTasks((durationMS) => {
    longTaskCount++
    longTaskDurationMS += durationMS
  })
  const intervalMS = Math.max(5_000, config.sample_interval_ms || DEFAULT_INTERVAL_MS)
  const timer = deps.setInterval(() => {
    const now = deps.now()
    eventLoopDriftMS = Math.max(eventLoopDriftMS, now - expectedAt)
    expectedAt = now + intervalMS
    const sample = buildDesktopDiagnosticsSample(deps, {
      event_loop_drift_ms: eventLoopDriftMS,
      long_task_count: longTaskCount,
      long_task_duration_ms: longTaskDurationMS,
      cache_mutation_count: cacheMutationCount,
      cache_mutation_duration_ms: cacheMutationDurationMS,
      cache_mutation_max_duration_ms: cacheMutationMaxDurationMS,
    })
    eventLoopDriftMS = 0
    longTaskCount = 0
    longTaskDurationMS = 0
    cacheMutationCount = 0
    cacheMutationDurationMS = 0
    cacheMutationMaxDurationMS = 0
    void deps.fetch(SAMPLE_PATH, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(sample),
    }).catch(() => {})
  }, intervalMS)

  return {
    enabled: true,
    release: () => {
      if (released) return
      released = true
      deps.clearInterval(timer)
      unsubscribeCache()
      stopLongTasks()
    },
  }
}

export function buildDesktopDiagnosticsSample(
  deps: Pick<DesktopDiagnosticsSamplerDeps, 'now' | 'getCacheSnapshot' | 'getDOMNodeCount' | 'getHeap' | 'getQueryCache'>,
  timing: Pick<
    DesktopDiagnosticsSample,
    | 'event_loop_drift_ms'
    | 'long_task_count'
    | 'long_task_duration_ms'
    | 'cache_mutation_count'
    | 'cache_mutation_duration_ms'
    | 'cache_mutation_max_duration_ms'
  >,
): DesktopDiagnosticsSample {
  const cache = aggregateDesktopV3Cache(deps.getCacheSnapshot())
  const queryCache = deps.getQueryCache()
  const heap = deps.getHeap()
  return {
    timestamp_ms: Math.round(deps.now()),
    js_heap_available: heap !== null,
    ...(heap ? { js_heap_used_bytes: heap.used, js_heap_total_bytes: heap.total } : {}),
    ...timing,
    dom_nodes: deps.getDOMNodeCount(),
    query_cache_entries: queryCache.length,
    query_cache_estimated_bytes: estimateBytes(queryCache),
    v3_cache_estimated_bytes: cache.estimatedBytes,
    v3_sections: cache.sections,
    largest_sessions: cache.largestSessions,
  }
}

export function aggregateDesktopV3Cache(state: DesktopV3CacheState) {
  const sections: Record<string, number> = {
    sessions: Object.keys(state.sessionsById).length,
    projections: Object.keys(state.projectionsBySession).length,
    session_views: Object.keys(state.sessionViewsById).length,
    message_sessions: Object.keys(state.messagesBySession).length,
    messages: Object.values(state.messagesBySession).reduce((sum, entry) => sum + entry.items.length, 0),
    event_sessions: Object.keys(state.eventsBySession).length,
    events: Object.values(state.eventsBySession).reduce((sum, entry) => sum + entry.length, 0),
    run_intents: Object.values(state.runIntentsBySession).reduce((sum, entry) => sum + Object.keys(entry).length, 0),
    live_runs: Object.values(state.liveRunsBySession).reduce((sum, entry) => sum + Object.keys(entry).length, 0),
    subscriptions: Object.keys(state.subscriptionsById).length,
    worksets: Object.keys(state.worksetsById).length,
    plans: Object.keys(state.plansBySession).length,
    permissions: Object.values(state.permissionsBySession).reduce((sum, entry) => sum + entry.length, 0),
    notifications: Object.keys(state.notificationsById).length,
    history_chunks: Object.keys(state.historyChunksById).length,
  }
  const sessionIds = new Set([...Object.keys(state.sessionsById), ...Object.keys(state.messagesBySession), ...Object.keys(state.eventsBySession)])
  const largestSessions = [...sessionIds]
    .map((sessionId) => {
      const messages = state.messagesBySession[sessionId]?.items.length || 0
      const events = state.eventsBySession[sessionId]?.length || 0
      const estimatedBytes = estimateBytes({
        session: state.sessionsById[sessionId],
        projection: state.projectionsBySession[sessionId],
        view: state.sessionViewsById[sessionId],
        messages: state.messagesBySession[sessionId],
        events: state.eventsBySession[sessionId],
        runs: state.liveRunsBySession[sessionId],
      })
      return { session_hash: stableSessionHash(sessionId), estimated_bytes: estimatedBytes, messages, events }
    })
    .sort((left, right) => right.estimated_bytes - left.estimated_bytes)
    .slice(0, MAX_LARGEST_SESSIONS)
  return { sections, estimatedBytes: estimateBytes(state), largestSessions }
}

function stableSessionHash(value: string): string {
  let left = 0x811c9dc5
  let right = 0x9e3779b9
  for (const byte of textEncoder.encode(value)) {
    left = Math.imul(left ^ byte, 0x01000193) >>> 0
    right = Math.imul(right ^ byte, 0x85ebca6b) >>> 0
  }
  return left.toString(16).padStart(8, '0') + right.toString(16).padStart(8, '0')
}

function estimateBytes(value: unknown): number {
  const seen = new WeakSet<object>()
  let bytes = 0
  let visited = 0
  const walk = (current: unknown, depth: number) => {
    if (current === null || current === undefined || depth > 32 || visited++ > 100_000) return
    if (typeof current === 'string') {
      bytes += textEncoder.encode(current).byteLength
      return
    }
    if (typeof current === 'number' || typeof current === 'bigint') {
      bytes += 8
      return
    }
    if (typeof current === 'boolean') {
      bytes++
      return
    }
    if (typeof current !== 'object' || seen.has(current)) return
    seen.add(current)
    if (ArrayBuffer.isView(current)) {
      bytes += current.byteLength
      return
    }
    for (const [key, child] of Object.entries(current)) {
      bytes += textEncoder.encode(key).byteLength
      walk(child, depth + 1)
    }
  }
  walk(value, 0)
  return bytes
}

function browserDiagnosticsDeps(): DesktopDiagnosticsSamplerDeps {
  return {
    fetch: window.fetch.bind(window),
    now: Date.now,
    setInterval: window.setInterval.bind(window),
    clearInterval: window.clearInterval.bind(window),
    getCacheSnapshot: getDesktopV3CacheSnapshot,
    subscribeCache: subscribeDesktopV3Cache,
    observeLongTasks: (callback) => {
      if (typeof PerformanceObserver === 'undefined') return () => {}
      try {
        const observer = new PerformanceObserver((list) => {
          for (const entry of list.getEntries()) callback(entry.duration)
        })
        observer.observe({ type: 'longtask', buffered: true })
        return () => observer.disconnect()
      } catch {
        return () => {}
      }
    },
    getDOMNodeCount: () => document.getElementsByTagName('*').length,
    getHeap: () => {
      const memory = (performance as Performance & { memory?: { usedJSHeapSize: number; totalJSHeapSize: number } }).memory
      return memory ? { used: memory.usedJSHeapSize, total: memory.totalJSHeapSize } : null
    },
    getQueryCache: () => queryClient.getQueryCache().getAll(),
  }
}
