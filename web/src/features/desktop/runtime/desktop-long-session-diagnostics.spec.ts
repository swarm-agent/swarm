import test from 'node:test'
import assert from 'node:assert/strict'
import { aggregateDesktopV3Cache, captureDesktopLongSessionDiagnostics, retainDesktopLongSessionDiagnostics } from './desktop-long-session-diagnostics'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type { DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'

test('aggregateDesktopV3Cache reports section counts and only stable session hashes', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById['private-session-id'] = { kind: 'stub', id: 'private-session-id', needsHydrate: true }
  state.messagesBySession['private-session-id'] = {
    items: [{ id: 'message-1', session_id: 'private-session-id', global_seq: 1, role: 'assistant', content: 'payload', created_at: 1 }],
    byMessageId: { 'message-1': 0 },
    byGlobalSeq: { '1': 0 },
  }
  const snapshot = aggregateDesktopV3Cache(state)
  assert.equal(snapshot.sections.sessions, 1)
  assert.equal(snapshot.sections.messages, 1)
  assert.equal(snapshot.largestSessions.length, 1)
  assert.match(snapshot.largestSessions[0].session_hash, /^[a-f0-9]{16}$/)
  assert.doesNotMatch(JSON.stringify(snapshot), /private-session-id|payload/)
})

test('diagnostics sampler remains disabled on a flag-gated 404', async () => {
  let timerCount = 0
  const state = createEmptyDesktopV3CacheState()
  const lease = await retainDesktopLongSessionDiagnostics({
    fetch: async () => new Response('{"enabled":false}', { status: 404 }),
    now: () => 0,
    setInterval: () => ++timerCount,
    clearInterval: () => {},
    getCacheSnapshot: () => state,
    subscribeCache: () => () => {},
    observeLongTasks: () => () => {},
    observeLongAnimationFrames: () => () => {},
    getDOMNodeCount: () => 0,
    measureBrowserMemory: async () => null,
    monotonicNow: () => 0,
    getQueryCache: () => [],
  })
  assert.equal(lease.enabled, false)
  assert.equal(timerCount, 0)
  lease.release()
})

test('enabled sampler exposes manual renderer-and-daemon capture and tears down resources', async () => {
  const state = createEmptyDesktopV3CacheState()
  let intervalCallback: (() => void) | undefined
  let cleared = 0
  let cacheReleased = 0
  let observerReleased = 0
  let animationObserverReleased = 0
  let listener: ((mutation?: DesktopV3CacheMutation) => void) | undefined
  const requests: RequestInfo[] = []
  const lease = await retainDesktopLongSessionDiagnostics({
    fetch: async (input) => {
      requests.push(input)
      return requests.length === 1
        ? new Response('{"ok":true,"enabled":true,"sample_interval_ms":30000,"artifact_location":"/logs/run-1"}', { status: 200 })
        : new Response('{"ok":true,"artifact_location":"/logs/run-1","artifacts":["desktop-samples.jsonl","samples.jsonl","latest-findings.json","profile-test-heap.pprof"]}', { status: 202 })
    },
    now: () => 30_000,
    setInterval: (callback) => {
      intervalCallback = callback
      return 7
    },
    clearInterval: (id) => {
      assert.equal(id, 7)
      cleared++
    },
    getCacheSnapshot: () => state,
    subscribeCache: (next) => {
      listener = next
      return () => cacheReleased++
    },
    observeLongTasks: () => () => observerReleased++,
    observeLongAnimationFrames: () => () => animationObserverReleased++,
    getDOMNodeCount: () => 10,
    measureBrowserMemory: async () => 300,
    monotonicNow: () => 1,
    getQueryCache: () => [],
  })
  listener?.({ action: { type: 'session.select', sessionId: undefined }, previousState: state, nextState: state, durationMS: 4 })
  assert.ok(intervalCallback)
  const capture = await captureDesktopLongSessionDiagnostics()
  assert.equal(capture.artifactLocation, '/logs/run-1')
  assert.deepEqual(capture.artifacts, ['desktop-samples.jsonl', 'samples.jsonl', 'latest-findings.json', 'profile-test-heap.pprof'])
  lease.release()
  lease.release()
  assert.equal(requests.length, 2)
  assert.equal(cleared, 1)
  assert.equal(cacheReleased, 1)
  assert.equal(observerReleased, 1)
  assert.equal(animationObserverReleased, 1)
})
