import { apiFetch, readErrorMessage } from '../../../app/api'
import { queryClient } from '../../../app/query-client'
import {
  getDesktopV3CacheSnapshot,
  subscribeDesktopV3Cache,
  type DesktopV3CacheMutation,
} from '../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../state/desktop-v3-cache-types'

const CONFIG_PATH = '/v3/diagnostics/long-session/config'
const SAMPLE_PATH = '/v3/diagnostics/long-session/samples'
const CAPTURE_PATH = '/v3/diagnostics/long-session/captures'
const MAX_LARGEST_SESSIONS = 10
const textEncoder = new TextEncoder()

interface DiagnosticsConfig {
  ok: boolean
  enabled: boolean
  sample_interval_ms?: number
  artifact_location?: string
}

export interface DesktopDiagnosticsAvailability {
  checked: boolean
  enabled: boolean
  artifactLocation: string
  error: string | null
}

export interface DesktopDiagnosticsCaptureResult {
  artifactLocation: string
  artifacts: string[]
}

let diagnosticsAvailability: DesktopDiagnosticsAvailability = {
  checked: false,
  enabled: false,
  artifactLocation: '',
  error: null,
}
let activeManualCapture: (() => Promise<DesktopDiagnosticsCaptureResult>) | null = null
const availabilityListeners = new Set<() => void>()

export function getDesktopLongSessionDiagnosticsAvailability(): DesktopDiagnosticsAvailability {
  return diagnosticsAvailability
}

export function subscribeDesktopLongSessionDiagnosticsAvailability(listener: () => void): () => void {
  availabilityListeners.add(listener)
  return () => availabilityListeners.delete(listener)
}

export async function captureDesktopLongSessionDiagnostics(): Promise<DesktopDiagnosticsCaptureResult> {
  if (!activeManualCapture) {
    throw new Error(diagnosticsAvailability.error || 'Long-session diagnostics are not ready. Confirm the flag is enabled and the daemon is connected.')
  }
  return activeManualCapture()
}

function publishDiagnosticsAvailability(next: DesktopDiagnosticsAvailability): void {
  diagnosticsAvailability = next
  for (const listener of availabilityListeners) listener()
}

export interface DesktopDiagnosticsSample {
  timestamp_ms: number
  browser_memory_available: boolean
  browser_memory_bytes?: number
  event_loop_drift_ms: number
  long_task_count: number
  long_task_duration_ms: number
  long_animation_frame_count: number
  long_animation_frame_duration_ms: number
  long_animation_frame_blocking_duration_ms: number
  dom_nodes: number
  cache_mutation_count: number
  cache_mutation_duration_ms: number
  cache_mutation_max_duration_ms: number
  cache_action_counts: Record<string, number>
  cache_action_duration_ms: Record<string, number>
  cache_action_max_duration_ms: Record<string, number>
  diagnostics_sample_duration_ms: number
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
  observeLongAnimationFrames: (callback: (durationMS: number, blockingDurationMS: number) => void) => () => void
  getDOMNodeCount: () => number
  measureBrowserMemory: () => Promise<number | null>
  monotonicNow: () => number
  getQueryCache: () => unknown[]
}

export interface DesktopDiagnosticsLease {
  enabled: boolean
  release: () => void
}

export async function retainDesktopLongSessionDiagnostics(
  deps: DesktopDiagnosticsSamplerDeps = browserDiagnosticsDeps(),
): Promise<DesktopDiagnosticsLease> {
  let response: Response
  try {
    response = await deps.fetch(CONFIG_PATH, { cache: 'no-store', credentials: 'same-origin' })
    if (response.status === 404) {
      publishDiagnosticsAvailability({ checked: true, enabled: false, artifactLocation: '', error: null })
      return { enabled: false, release: () => {} }
    }
    if (!response.ok) throw new Error(`long-session diagnostics config failed: ${response.status} ${await readErrorMessage(response)}`)
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error)
    publishDiagnosticsAvailability({ checked: true, enabled: false, artifactLocation: '', error: message })
    throw error
  }
  const config = (await response.json()) as DiagnosticsConfig
  if (!config.ok || !config.enabled) {
    publishDiagnosticsAvailability({ checked: true, enabled: false, artifactLocation: '', error: null })
    return { enabled: false, release: () => {} }
  }
  const configuredArtifactLocation = config.artifact_location?.trim() || ''
  publishDiagnosticsAvailability({ checked: true, enabled: true, artifactLocation: configuredArtifactLocation, error: null })

  let released = false
  const intervalMS = Math.max(5_000, config.sample_interval_ms || 30_000)
  let eventLoopProbeExpectedAt = deps.now() + intervalMS
  let eventLoopDriftMS = 0
  let longTaskCount = 0
  let longTaskDurationMS = 0
  let longAnimationFrameCount = 0
  let longAnimationFrameDurationMS = 0
  let longAnimationFrameBlockingDurationMS = 0
  let cacheMutationCount = 0
  let cacheMutationDurationMS = 0
  let cacheMutationMaxDurationMS = 0
  let cacheActionCounts: Record<string, number> = {}
  let cacheActionDurationMS: Record<string, number> = {}
  let cacheActionMaxDurationMS: Record<string, number> = {}
  const unsubscribeCache = deps.subscribeCache((mutation?: DesktopV3CacheMutation) => {
    if (!mutation) return
    cacheMutationCount++
    cacheMutationDurationMS += mutation.durationMS
    cacheMutationMaxDurationMS = Math.max(cacheMutationMaxDurationMS, mutation.durationMS)
    const action = mutation.action.type
    cacheActionCounts[action] = (cacheActionCounts[action] ?? 0) + 1
    cacheActionDurationMS[action] = (cacheActionDurationMS[action] ?? 0) + mutation.durationMS
    cacheActionMaxDurationMS[action] = Math.max(cacheActionMaxDurationMS[action] ?? 0, mutation.durationMS)
  })
  const stopLongTasks = deps.observeLongTasks((durationMS) => {
    longTaskCount++
    longTaskDurationMS += durationMS
  })
  const stopLongAnimationFrames = deps.observeLongAnimationFrames((durationMS, blockingDurationMS) => {
    longAnimationFrameCount++
    longAnimationFrameDurationMS += durationMS
    longAnimationFrameBlockingDurationMS += blockingDurationMS
  })
  let deliveryInFlight = false
  const sampleAndDeliver = async (captureDaemon = true): Promise<DesktopDiagnosticsCaptureResult> => {
    if (released) throw new Error('Long-session diagnostics are no longer active.')
    if (deliveryInFlight) throw new Error('A long-session diagnostics capture is already in progress.')
    deliveryInFlight = true
    const sampleStartedAt = deps.monotonicNow()
    let browserMemoryBytes: number | null = null
    try {
      browserMemoryBytes = await deps.measureBrowserMemory()
    } catch (error) {
      console.error('[desktop-v3] browser memory measurement unavailable', error)
    }
    const sample = buildDesktopDiagnosticsSample(deps, {
      browser_memory_available: browserMemoryBytes !== null,
      ...(browserMemoryBytes !== null ? { browser_memory_bytes: browserMemoryBytes } : {}),
      event_loop_drift_ms: eventLoopDriftMS,
      long_task_count: longTaskCount,
      long_task_duration_ms: longTaskDurationMS,
      long_animation_frame_count: longAnimationFrameCount,
      long_animation_frame_duration_ms: longAnimationFrameDurationMS,
      long_animation_frame_blocking_duration_ms: longAnimationFrameBlockingDurationMS,
      cache_mutation_count: cacheMutationCount,
      cache_mutation_duration_ms: cacheMutationDurationMS,
      cache_mutation_max_duration_ms: cacheMutationMaxDurationMS,
      cache_action_counts: cacheActionCounts,
      cache_action_duration_ms: cacheActionDurationMS,
      cache_action_max_duration_ms: cacheActionMaxDurationMS,
      diagnostics_sample_duration_ms: 0,
    })
    sample.diagnostics_sample_duration_ms = Math.max(0, deps.monotonicNow() - sampleStartedAt)
    eventLoopDriftMS = 0
    longTaskCount = 0
    longTaskDurationMS = 0
    longAnimationFrameCount = 0
    longAnimationFrameDurationMS = 0
    longAnimationFrameBlockingDurationMS = 0
    cacheMutationCount = 0
    cacheMutationDurationMS = 0
    cacheMutationMaxDurationMS = 0
    cacheActionCounts = {}
    cacheActionDurationMS = {}
    cacheActionMaxDurationMS = {}

    try {
      const sampleResponse = await deps.fetch(captureDaemon ? CAPTURE_PATH : SAMPLE_PATH, {
        method: 'POST',
        cache: 'no-store',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(sample),
      })
      if (!sampleResponse.ok) {
        throw new Error(`sample delivery failed: ${sampleResponse.status} ${await readErrorMessage(sampleResponse)}`)
      }
      const result = (await sampleResponse.json()) as { ok?: boolean; artifact_location?: string; artifacts?: string[] }
      if (!result.ok) throw new Error('Diagnostics capture did not return a success result.')
      const artifactLocation = result.artifact_location?.trim() || configuredArtifactLocation
      const artifacts = Array.isArray(result.artifacts) ? result.artifacts.filter((artifact): artifact is string => typeof artifact === 'string' && artifact.length > 0) : []
      publishDiagnosticsAvailability({ checked: true, enabled: true, artifactLocation, error: null })
      return { artifactLocation, artifacts }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      publishDiagnosticsAvailability({ checked: true, enabled: true, artifactLocation: configuredArtifactLocation, error: message })
      throw error
    } finally {
      deliveryInFlight = false
    }
  }
  const timer = deps.setInterval(() => {
    const probeNow = deps.now()
    eventLoopDriftMS = Math.max(eventLoopDriftMS, probeNow - eventLoopProbeExpectedAt)
    eventLoopProbeExpectedAt = probeNow + intervalMS
    void sampleAndDeliver(false).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : String(error)
      console.error('[desktop-v3] long-session diagnostics sample delivery failed', error)
      publishDiagnosticsAvailability({ checked: true, enabled: true, artifactLocation: configuredArtifactLocation, error: message })
    })
  }, intervalMS)
  const manualCapture = () => sampleAndDeliver(true)
  activeManualCapture = manualCapture
  publishDiagnosticsAvailability({ checked: true, enabled: true, artifactLocation: configuredArtifactLocation, error: null })

  console.info(`[desktop-v3] long-session diagnostics enabled; authenticated browser samples run every ${intervalMS}ms`)

  return {
    enabled: true,
    release: () => {
      if (released) return
      released = true
      if (activeManualCapture === manualCapture) activeManualCapture = null
      deps.clearInterval(timer)
      unsubscribeCache()
      stopLongTasks()
      stopLongAnimationFrames()
    },
  }
}

export function buildDesktopDiagnosticsSample(
  deps: Pick<DesktopDiagnosticsSamplerDeps, 'now' | 'getCacheSnapshot' | 'getDOMNodeCount' | 'getQueryCache'>,
  timing: Pick<
    DesktopDiagnosticsSample,
    | 'browser_memory_available'
    | 'browser_memory_bytes'
    | 'event_loop_drift_ms'
    | 'long_task_count'
    | 'long_task_duration_ms'
    | 'long_animation_frame_count'
    | 'long_animation_frame_duration_ms'
    | 'long_animation_frame_blocking_duration_ms'
    | 'cache_mutation_count'
    | 'cache_mutation_duration_ms'
    | 'cache_mutation_max_duration_ms'
    | 'cache_action_counts'
    | 'cache_action_duration_ms'
    | 'cache_action_max_duration_ms'
    | 'diagnostics_sample_duration_ms'
  >,
): DesktopDiagnosticsSample {
  const cache = aggregateDesktopV3Cache(deps.getCacheSnapshot())
  const queryCache = deps.getQueryCache()
  return {
    timestamp_ms: Math.round(deps.now()),
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

function observePerformanceEntries(
  type: string,
  callback: (entry: PerformanceEntry) => void,
): () => void {
  if (typeof PerformanceObserver === 'undefined' || !PerformanceObserver.supportedEntryTypes.includes(type)) return () => {}
  try {
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) callback(entry)
    })
    observer.observe({ type, buffered: true })
    return () => observer.disconnect()
  } catch {
    return () => {}
  }
}

function browserDiagnosticsDeps(): DesktopDiagnosticsSamplerDeps {
  return {
    fetch: apiFetch,
    now: Date.now,
    monotonicNow: performance.now.bind(performance),
    setInterval: window.setInterval.bind(window),
    clearInterval: window.clearInterval.bind(window),
    getCacheSnapshot: getDesktopV3CacheSnapshot,
    subscribeCache: subscribeDesktopV3Cache,
    observeLongTasks: (callback) => observePerformanceEntries('longtask', (entry) => callback(entry.duration)),
    observeLongAnimationFrames: (callback) => observePerformanceEntries('long-animation-frame', (entry) => {
      const blockingDuration = (entry as PerformanceEntry & { blockingDuration?: number }).blockingDuration ?? 0
      callback(entry.duration, blockingDuration)
    }),
    getDOMNodeCount: () => document.getElementsByTagName('*').length,
    measureBrowserMemory: async () => {
      const measure = (performance as Performance & {
        measureUserAgentSpecificMemory?: () => Promise<{ bytes?: number }>
      }).measureUserAgentSpecificMemory
      if (!globalThis.crossOriginIsolated || typeof measure !== 'function') return null
      const result = await measure.call(performance)
      return typeof result.bytes === 'number' && Number.isFinite(result.bytes) && result.bytes >= 0 ? result.bytes : null
    },
    getQueryCache: () => queryClient.getQueryCache().getAll(),
  }
}
