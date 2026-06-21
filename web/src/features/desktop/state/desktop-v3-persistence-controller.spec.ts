import 'fake-indexeddb/auto'

import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import { readDesktopV3MessageTail, readDesktopV3Owner, resetDesktopV3CacheDBForTests } from './desktop-v3-cache-db'
import {
  desktopV3CachePersistenceCoordinator,
  persistDesktopV3OwnerAndTails,
} from './desktop-v3-cache-persistence-coordinator'
import { buildPersistedDesktopV3MessageTailV1 } from './desktop-v3-cache-persisted-types'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, resetDesktopV3CacheForTests } from './desktop-v3-cache-store'
import { restoreDesktopV3CacheFromActiveOwner } from './desktop-v3-bootstrap-controller'
import {
  buildPersistedDesktopV3LiveRunsBySessionV1FromState,
  buildPersistedDesktopV3MessageTailV1FromState,
  buildPersistedDesktopV3OwnerV1FromState,
  createDesktopV3PersistenceController,
  startDesktopV3PersistenceController,
  stopDesktopV3PersistenceControllerForTests,
} from './desktop-v3-persistence-controller'
import { selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import { bootstrapResponseToAction, hydrateResponseToAction, selectSession } from './desktop-v3-cache-wire'
import {
  hydrateSnapshotFixture,
  messageA1,
  messageA2,
  messageB1,
  projectionA,
  projectionB,
  runIntentA,
  sessionA,
  sessionB,
  snapshotFixture,
  tombstoneB,
} from './desktop-v3-cache.backend-fixtures'
import type { PersistedDesktopV3MessageTailV1, PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'
import type { DesktopV3CacheAction, DesktopV3CacheState, LiveRunOverlay, MessageSnapshot, SessionSnapshot } from './desktop-v3-cache-types'

const ownerA = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-a',
  userId: 'user-a',
  surface: 'desktop',
})

const ownerB = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-b',
  userId: 'user-b',
  surface: 'desktop',
})

async function resetHarness(): Promise<void> {
  stopDesktopV3PersistenceControllerForTests()
  resetDesktopV3CacheForTests(createEmptyDesktopV3CacheState())
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
}

function apply(actions: DesktopV3CacheAction[], state = createEmptyDesktopV3CacheState()): DesktopV3CacheState {
  let next = state
  for (const action of actions) next = desktopV3CacheReducer(next, action)
  return next
}

function persistedStateFixture(): DesktopV3CacheState {
  return apply([
    bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })),
    hydrateResponseToAction(hydrateSnapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
      session_order: [sessionA.id],
      selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    }), [sessionA.id]),
    selectSession(sessionA.id),
  ])
}

function tailFromMessages(sessionId: string, messages: MessageSnapshot[]) {
  return buildPersistedDesktopV3MessageTailV1({
    ownerKey: ownerA.key,
    sessionId,
    persistedAt: 1_000,
    messages,
  })
}

function sessionFixture(id: string, index: number): SessionSnapshot {
  const base = id === sessionA.id ? sessionA : id === sessionB.id ? sessionB : sessionA
  return {
    ...base,
    id,
    title: `Session ${id}`,
    created_at: 100 + index,
    updated_at: 200 + index,
    message_count: 0,
    last_message_at: 0,
  }
}

function liveRunOverlayFixture(sessionId: string, runId: string, index = 0): LiveRunOverlay {
  return {
    sessionId,
    runId,
    status: 'running',
    assistantDraft: {
      content: `assistant draft ${sessionId}`,
      updatedAt: 1_000 + index,
      timelineSeq: 10 + index,
    },
    assistantSegments: [{
      id: `segment-${sessionId}`,
      content: `assistant segment ${sessionId}`,
      createdAt: 1_010 + index,
      updatedAt: 1_011 + index,
      timelineSeq: 11 + index,
    }],
    toolCallsByCallId: {
      [`call-${sessionId}`]: {
        callId: `call-${sessionId}`,
        stepId: `step-${sessionId}`,
        toolInstanceId: `tool-${sessionId}`,
        toolName: 'read',
        argumentsText: `{"session":"${sessionId}"}`,
        outputText: `tool output ${sessionId}`,
        status: 'running',
        createdAt: 1_020 + index,
        updatedAt: 1_021 + index,
        timelineSeq: 12 + index,
      },
    },
    reasoning: {
      key: `reasoning-${sessionId}`,
      reasoningId: `reasoning-id-${sessionId}`,
      reasoningKey: `reasoning-key-${sessionId}`,
      stepId: `reasoning-step-${sessionId}`,
      step: index,
      state: 'running',
      summary: `reasoning summary ${sessionId}`,
      text: `reasoning text ${sessionId}`,
      startedAt: 1_030 + index,
      completedAt: null,
      updatedAt: 1_031 + index,
      timelineSeq: 13 + index,
      updatedSeq: 14 + index,
    },
    reasoningByKey: {
      [`reasoning-map-${sessionId}`]: {
        key: `reasoning-map-${sessionId}`,
        state: 'running',
        summary: `mapped reasoning summary ${sessionId}`,
        text: `mapped reasoning text ${sessionId}`,
        startedAt: 1_040 + index,
        updatedAt: 1_041 + index,
      },
    },
    lastEventSeqSeen: 50 + index,
  }
}

function stateWithFiveLiveSessions(): DesktopV3CacheState {
  const sessionIds = ['session-a', 'session-b', 'session-c', 'session-d', 'session-e']
  const sessionsById = Object.fromEntries(sessionIds.map((id, index) => [id, sessionFixture(id, index)]))
  const projectionsBySession = Object.fromEntries(sessionIds.map((id, index) => [id, {
    ...projectionA,
    session_id: id,
    last_event_seq: 50 + index,
    projection_high_watermark_seq: 50 + index,
    updated_at: 200 + index,
  }]))
  const runIntentsBySession = Object.fromEntries(sessionIds.map((id, index) => [id, [{
    ...runIntentA,
    session_id: id,
    run_id: `run-${id}`,
    status: 'running',
    event_seq: 50 + index,
  }]]))

  const state = apply([
    bootstrapResponseToAction(snapshotFixture({
      sessions_by_id: sessionsById,
      projections_by_session: projectionsBySession,
      messages_by_session: { [sessionA.id]: [messageA1] },
      events_by_session: {},
      run_intents_by_session: runIntentsBySession,
      session_order: sessionIds,
      tombstones_by_session: {},
    })),
    selectSession(sessionA.id),
  ])
  state.realtime.endpointCursor = 'opaque-realtime-cursor-5'
  state.liveRunsBySession = Object.fromEntries(sessionIds.map((id, index) => [id, {
    [`run-${id}`]: liveRunOverlayFixture(id, `run-${id}`, index),
  }]))
  return state
}

function restoreOwnerIntoEmptyState(
  owner: PersistedDesktopV3OwnerV1,
  selectedMessageTail?: PersistedDesktopV3MessageTailV1,
): DesktopV3CacheState {
  return desktopV3CacheReducer(createEmptyDesktopV3CacheState(), {
    type: 'desktopV3Cache.restore',
    owner,
    selectedMessageTail,
  })
}

test('bootstrap/hydrate cache mutation writes a restorable owner record and selected transcript tail', async () => {
  await resetHarness()
  let now = 10_000
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    now: () => now++,
  })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  await controller.flushNow()
  assert.equal((await readDesktopV3Owner(ownerA.key))?.ownerKey, ownerA.key)
  assert.equal(await readDesktopV3MessageTail(ownerA.key, sessionA.id), undefined)

  dispatchDesktopV3Cache(hydrateResponseToAction(hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id]))
  dispatchDesktopV3Cache(selectSession(sessionA.id))
  await controller.flushNow()

  const owner = await readDesktopV3Owner(ownerA.key)
  const tail = await readDesktopV3MessageTail(ownerA.key, sessionA.id)
  assert.equal(owner?.selectedSessionId, sessionA.id)
  assert.deepEqual(tail?.messages.map((message) => message.id), [messageA1.id, messageA2.id])

  resetDesktopV3CacheForTests(createEmptyDesktopV3CacheState())
  const restored = await restoreDesktopV3CacheFromActiveOwner({
    dispatch: dispatchDesktopV3Cache,
    loadActiveOwnerKey: () => ownerA.key,
  })
  assert.equal(restored, true)
  assert.deepEqual(getDesktopV3CacheSnapshot().messagesBySession[sessionA.id]?.items.map((message) => message.id), [messageA1.id, messageA2.id])

  controller.stop()
  await resetHarness()
})

test('changing one transcript does not rewrite every cached transcript', async () => {
  const state = apply([
    bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })),
    hydrateResponseToAction(hydrateSnapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      messages_by_session: { [sessionA.id]: [messageA1] },
      session_order: [sessionA.id],
      selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    }), [sessionA.id]),
    hydrateResponseToAction(hydrateSnapshotFixture({
      sessions_by_id: { [sessionB.id]: sessionB },
      projections_by_session: { [sessionB.id]: projectionB },
      messages_by_session: { [sessionB.id]: [messageB1] },
      session_order: [sessionB.id],
      selector: { kind: 'session_ids', session_ids: [sessionB.id] },
    }), [sessionB.id]),
  ])

  const tail = buildPersistedDesktopV3MessageTailV1FromState(state, ownerA.key, sessionA.id, 1_000)
  assert.equal(tail?.sessionId, sessionA.id)
  assert.deepEqual(tail?.messages.map((message) => message.id), [messageA1.id])
})

test('rapid mutations are coalesced into one debounced write', async () => {
  await resetHarness()
  const timers: Array<() => void> = []
  let writes = 0
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async () => {
      writes += 1
      return true
    },
    scheduler: {
      setTimeout(callback) {
        timers.push(callback)
        return callback
      },
      clearTimeout(timer) {
        const index = timers.indexOf(timer as () => void)
        if (index >= 0) timers.splice(index, 1)
      },
    },
  })
  resetDesktopV3CacheForTests(apply([
    bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })),
    hydrateResponseToAction(hydrateSnapshotFixture({
      sessions_by_id: { [sessionB.id]: sessionB },
      projections_by_session: { [sessionB.id]: projectionB },
      messages_by_session: { [sessionB.id]: [messageB1] },
      session_order: [sessionB.id],
    }), [sessionB.id]),
  ]))
  controller.start()

  dispatchDesktopV3Cache({ type: 'mutation.messageResult', raw: { ok: true, session_id: sessionB.id, message: { ...messageB1, id: 'msg-b-2', global_seq: 2 }, run_intent: null, mutation: {}, realtime_outbox: null }, clientRequestId: 'client-1', messageId: 'pending-1' })
  dispatchDesktopV3Cache({ type: 'mutation.messageResult', raw: { ok: true, session_id: sessionB.id, message: { ...messageB1, id: 'msg-b-3', global_seq: 3 }, run_intent: null, mutation: {}, realtime_outbox: null }, clientRequestId: 'client-2', messageId: 'pending-2' })

  await Promise.resolve()
  assert.equal(timers.length, 1)
  assert.equal(writes, 0)
  timers.shift()?.()
  await controller.flushNow()
  assert.equal(writes, 1)

  controller.stop()
  await resetHarness()
})

test('shared controller restarts after cleanup and writes one changed owner/tail', async () => {
  await resetHarness()
  const writes: Array<{ ownerKey: string; tailIds: string[] }> = []
  const startShared = () => startDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async (owner, tails) => {
      writes.push({ ownerKey: owner.ownerKey, tailIds: tails.map((tail) => tail.sessionId) })
      return true
    },
    saveActiveOwnerKey: () => true,
  })

  const firstCleanup = startShared()
  firstCleanup()
  const secondCleanup = startShared()

  dispatchDesktopV3Cache(hydrateResponseToAction(hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: { [sessionA.id]: [messageA1] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id]))
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))

  assert.deepEqual(writes, [{ ownerKey: ownerA.key, tailIds: [sessionA.id] }])
  secondCleanup()
  await resetHarness()
})

test('StrictMode-shaped setup cleanup setup leaves the second shared controller subscribed', async () => {
  await resetHarness()
  const writes: Array<{ ownerKey: string; tailIds: string[] }> = []

  const setup = () => startDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async (owner, tails) => {
      writes.push({ ownerKey: owner.ownerKey, tailIds: tails.map((tail) => tail.sessionId) })
      return true
    },
    saveActiveOwnerKey: () => true,
  })

  const cleanup = setup()
  cleanup()
  const realCleanup = setup()

  dispatchDesktopV3Cache(hydrateResponseToAction(hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: { [sessionA.id]: [messageA2] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id]))
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))

  assert.deepEqual(writes, [{ ownerKey: ownerA.key, tailIds: [sessionA.id] }])
  realCleanup()
  await resetHarness()
})

test('stopping controller flushes already scheduled dirty transcript state', async () => {
  await resetHarness()
  const timers: Array<() => void> = []
  const writes: Array<{ ownerKey: string; tailIds: string[] }> = []
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async (owner, tails) => {
      writes.push({ ownerKey: owner.ownerKey, tailIds: tails.map((tail) => tail.sessionId) })
      return true
    },
    scheduler: {
      setTimeout(callback) {
        timers.push(callback)
        return callback
      },
      clearTimeout(timer) {
        const index = timers.indexOf(timer as () => void)
        if (index >= 0) timers.splice(index, 1)
      },
    },
  })
  resetDesktopV3CacheForTests(apply([
    bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })),
    hydrateResponseToAction(hydrateSnapshotFixture({
      sessions_by_id: { [sessionB.id]: sessionB },
      projections_by_session: { [sessionB.id]: projectionB },
      messages_by_session: { [sessionB.id]: [messageB1] },
      session_order: [sessionB.id],
    }), [sessionB.id]),
  ]))
  controller.start()

  dispatchDesktopV3Cache({ type: 'mutation.messageResult', raw: { ok: true, session_id: sessionB.id, message: { ...messageB1, id: 'msg-b-stop', global_seq: 4 }, run_intent: null, mutation: {}, realtime_outbox: null }, clientRequestId: 'client-stop', messageId: 'pending-stop' })
  await Promise.resolve()
  assert.equal(timers.length, 1)

  controller.stop()
  await controller.flushNow()

  assert.deepEqual(writes, [{ ownerKey: ownerA.key, tailIds: [sessionB.id] }])
  assert.equal(timers.length, 0)
  await resetHarness()
})

test('authoritative empty messages persist as an empty tail', async () => {
  const state = apply([
    bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })),
    hydrateResponseToAction(hydrateSnapshotFixture({ messages_by_session: { [sessionB.id]: [] } }), [sessionB.id]),
  ])
  const tail = buildPersistedDesktopV3MessageTailV1FromState(state, ownerA.key, sessionB.id, 1_000)
  assert.deepEqual(tail?.messages, [])
})

test('metadata-only bootstrap does not erase a persisted transcript', async () => {
  await resetHarness()
  const writes: Array<{ tailIds: string[] }> = []
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async (_owner, tails) => {
      writes.push({ tailIds: tails.map((tail) => tail.sessionId) })
      return true
    },
  })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  await controller.flushNow()

  assert.deepEqual(writes.at(-1)?.tailIds, [])
  controller.stop()
  await resetHarness()
})

test('persistable live-run overlays are written to the owner record but never transcript tails', () => {
  const state = persistedStateFixture()
  state.realtime.endpointCursor = 'opaque-realtime-cursor-1'
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      assistantDraft: { content: 'draft assistant text', updatedAt: 1_234 },
      toolCallsByCallId: {
        'call-1': { callId: 'call-1', outputText: 'streaming tool delta', updatedAt: 1_235 },
      },
    },
  }

  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 1_000)
  const tail = buildPersistedDesktopV3MessageTailV1FromState(state, ownerA.key, sessionA.id, 1_000)

  assert.equal(owner?.realtimeEndpointCursor, 'opaque-realtime-cursor-1')
  assert.deepEqual(owner?.liveRunsBySession?.[sessionA.id]?.['run-live'], state.liveRunsBySession[sessionA.id]['run-live'])
  assert.equal(JSON.stringify(tail).includes('draft assistant text'), false)
  assert.equal(JSON.stringify(tail).includes('streaming tool delta'), false)
})

test('restore populates live overlays for A-E while selected session is A', () => {
  const state = stateWithFiveLiveSessions()
  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 1_000)
  assert.ok(owner)

  const restored = restoreOwnerIntoEmptyState(owner, tailFromMessages(sessionA.id, [messageA1]))

  assert.equal(restored.selectedSessionId, sessionA.id)
  assert.equal(restored.realtime.endpointCursor, 'opaque-realtime-cursor-5')
  assert.deepEqual(Object.keys(restored.liveRunsBySession).sort(), ['session-a', 'session-b', 'session-c', 'session-d', 'session-e'])
  assert.deepEqual(restored.liveRunsBySession, owner.liveRunsBySession)
})

test('offscreen E renders without reading E message tail', () => {
  const state = stateWithFiveLiveSessions()
  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 1_000)
  assert.ok(owner)

  const restored = restoreOwnerIntoEmptyState(owner, tailFromMessages(sessionA.id, [messageA1]))
  const rendered = selectRenderedSessionMessages(restored, 'session-e')

  assert.equal(restored.selectedSessionId, sessionA.id)
  assert.equal(restored.messagesBySession['session-e'], undefined)
  assert.equal(rendered.committed.length, 0)
  assert.equal(rendered.liveRuns.length, 1)
  assert.equal(rendered.liveRuns[0].assistantDraft?.content, 'assistant draft session-e')
  assert.equal(rendered.liveRuns[0].toolCallsByCallId['call-session-e']?.outputText, 'tool output session-e')
})

test('restore ignores missing and tombstoned live sessions', () => {
  const state = stateWithFiveLiveSessions()
  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 1_000)
  assert.ok(owner)

  owner.sidebarSessionsById['session-b'].tombstone = { ...tombstoneB, session_id: 'session-b' }
  owner.liveRunsBySession = {
    [sessionA.id]: owner.liveRunsBySession?.[sessionA.id] ?? {},
    [sessionB.id]: owner.liveRunsBySession?.[sessionB.id] ?? {},
    'session-missing': {
      'run-session-missing': liveRunOverlayFixture('session-missing', 'run-session-missing'),
    },
  }

  const restored = restoreOwnerIntoEmptyState(owner)

  assert.ok(restored.liveRunsBySession[sessionA.id]?.['run-session-a'])
  assert.equal(restored.liveRunsBySession[sessionB.id], undefined)
  assert.equal(restored.liveRunsBySession['session-missing'], undefined)
})

test('restore retains assistant tool and reasoning state byte-for-byte', () => {
  const state = stateWithFiveLiveSessions()
  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 1_000)
  assert.ok(owner)

  const restored = restoreOwnerIntoEmptyState(owner)

  assert.deepEqual(restored.liveRunsBySession, owner.liveRunsBySession)
  assert.notEqual(restored.liveRunsBySession[sessionA.id]?.['run-session-a'], owner.liveRunsBySession?.[sessionA.id]?.['run-session-a'])
})

test('failed IndexedDB write leaves in-memory cache usable', async () => {
  await resetHarness()
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async () => false,
  })
  controller.start()

  try {
    dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
    await assert.rejects(
      controller.flushNow(),
      /Desktop V3 IndexedDB transaction failed/,
    )

    assert.equal(getDesktopV3CacheSnapshot().sessionsById[sessionA.id]?.kind, 'full')
  } finally {
    controller.stop()
    await resetHarness()
  }
})

test('owner change after snapshot cannot write into previous owner partition', async () => {
  await resetHarness()
  let currentOwner = ownerA
  const writes: string[] = []
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => currentOwner,
    getSnapshot: () => {
      currentOwner = ownerB
      return getDesktopV3CacheSnapshot()
    },
    writeOwnerAndTails: async (record) => {
      writes.push(record.ownerKey)
      return true
    },
    saveActiveOwnerKey: () => {
      throw new Error('active owner must not update after owner changes')
    },
  })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  await controller.flushNow()

  assert.deepEqual(writes, [])
  controller.stop()
  await resetHarness()
})

test('owner change during write cannot reactivate previous owner', async () => {
  await resetHarness()
  let currentOwner = ownerA
  const writes: string[] = []
  const activeOwnerKeys: string[] = []
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => currentOwner,
    writeOwnerAndTails: async (record) => {
      writes.push(record.ownerKey)
      currentOwner = ownerB
      return true
    },
    saveActiveOwnerKey: (ownerKey) => {
      activeOwnerKeys.push(ownerKey)
      return true
    },
  })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  await controller.flushNow()

  assert.deepEqual(writes, [ownerA.key])
  assert.equal(activeOwnerKeys.includes(ownerA.key), false)
  controller.stop()
  await resetHarness()
})

test('write reset memory restore reproduces durable state', async () => {
  await resetHarness()
  const controller = createDesktopV3PersistenceController({ resolveOwner: () => ownerA })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  dispatchDesktopV3Cache(hydrateResponseToAction(hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
    run_intents_by_session: { [sessionA.id]: [runIntentA] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id]))
  dispatchDesktopV3Cache(selectSession(sessionA.id))
  await controller.flushNow()

  resetDesktopV3CacheForTests(createEmptyDesktopV3CacheState())
  const restored = await restoreDesktopV3CacheFromActiveOwner({
    dispatch: dispatchDesktopV3Cache,
    loadActiveOwnerKey: () => ownerA.key,
  })

  assert.equal(restored, true)
  const snapshot = getDesktopV3CacheSnapshot()
  assert.equal(snapshot.sessionsById[sessionA.id]?.kind, 'full')
  assert.equal(snapshot.selectedSessionId, sessionA.id)
  assert.deepEqual(snapshot.messagesBySession[sessionA.id]?.items.map((message) => message.id), [messageA1.id, messageA2.id])

  controller.stop()
  await resetHarness()
})

async function readStateSource(relativePath: string): Promise<string> {
  return readFile(new URL(relativePath, import.meta.url), 'utf8')
}

test('normal persistence cannot overwrite a later durable stream commit', async () => {
  await resetHarness()
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    now: () => 1_000,
  })
  controller.start()

  resetDesktopV3CacheForTests(persistedStateFixture())
  dispatchDesktopV3Cache({ type: 'session.select', sessionId: sessionA.id })

  await desktopV3CachePersistenceCoordinator.enqueue(async () => {
    const current = getDesktopV3CacheSnapshot()
    resetDesktopV3CacheForTests({
      ...current,
      realtime: { ...current.realtime, endpointCursor: 'cursor-from-durable-stream' },
      liveRunsBySession: {
        ...current.liveRunsBySession,
        [sessionA.id]: {
          'run-durable': {
            sessionId: sessionA.id,
            runId: 'run-durable',
            status: 'running',
            assistantDraft: { content: 'durable stream draft', updatedAt: 2_000 },
            toolCallsByCallId: {},
          },
        },
      },
    })
  })

  await controller.flushNow()

  const owner = await readDesktopV3Owner(ownerA.key)
  assert.equal(owner?.realtimeEndpointCursor, 'cursor-from-durable-stream')
  assert.equal(owner?.liveRunsBySession?.[sessionA.id]?.['run-durable']?.assistantDraft?.content, 'durable stream draft')

  controller.stop()
  await resetHarness()
})

test('stream commit followed by normal flush writes the latest state', async () => {
  await resetHarness()
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    now: () => 2_000,
  })
  controller.start()

  resetDesktopV3CacheForTests(persistedStateFixture())
  await desktopV3CachePersistenceCoordinator.enqueue(async () => {
    const current = getDesktopV3CacheSnapshot()
    resetDesktopV3CacheForTests({
      ...current,
      realtime: { ...current.realtime, endpointCursor: 'cursor-before-normal-flush' },
      liveRunsBySession: {
        [sessionA.id]: {
          'run-before-normal': {
            sessionId: sessionA.id,
            runId: 'run-before-normal',
            status: 'running',
            assistantDraft: { content: 'stream state before normal flush', updatedAt: 2_100 },
            toolCallsByCallId: {},
          },
        },
      },
    })
  })

  dispatchDesktopV3Cache({ type: 'session.select', sessionId: sessionA.id })
  await controller.flushNow()

  const owner = await readDesktopV3Owner(ownerA.key)
  assert.equal(owner?.realtimeEndpointCursor, 'cursor-before-normal-flush')
  assert.equal(owner?.liveRunsBySession?.[sessionA.id]?.['run-before-normal']?.assistantDraft?.content, 'stream state before normal flush')

  controller.stop()
  await resetHarness()
})

test('coordinator serializes writes in submission order', async () => {
  const order: string[] = []
  let releaseFirst!: () => void
  const firstCanFinish = new Promise<void>((resolve) => {
    releaseFirst = resolve
  })

  const first = desktopV3CachePersistenceCoordinator.enqueue(async () => {
    order.push('first-start')
    await firstCanFinish
    order.push('first-end')
    return 'first'
  })
  const second = desktopV3CachePersistenceCoordinator.enqueue(async () => {
    order.push('second')
    return 'second'
  })

  await Promise.resolve()
  assert.deepEqual(order, ['first-start'])
  releaseFirst()
  assert.deepEqual(await Promise.all([first, second]), ['first', 'second'])
  assert.deepEqual(order, ['first-start', 'first-end', 'second'])
})

test('owner and final assistant message tail commit atomically', async () => {
  await resetHarness()
  const state = persistedStateFixture()
  state.liveRunsBySession[sessionA.id] = {
    'run-final': {
      sessionId: sessionA.id,
      runId: 'run-final',
      status: 'completed',
      assistantDraft: { content: 'final draft before tail', updatedAt: 3_000 },
      toolCallsByCallId: {},
    },
  }
  state.realtime.endpointCursor = 'cursor-final-commit'
  const owner = buildPersistedDesktopV3OwnerV1FromState(state, ownerA, 3_100)
  const tail = buildPersistedDesktopV3MessageTailV1FromState(state, ownerA.key, sessionA.id, 3_100)
  assert.ok(owner)
  assert.ok(tail)

  const wrote = await desktopV3CachePersistenceCoordinator.enqueue(() =>
    persistDesktopV3OwnerAndTails(owner, [tail]),
  )

  assert.equal(wrote, true)
  assert.equal((await readDesktopV3Owner(ownerA.key))?.realtimeEndpointCursor, 'cursor-final-commit')
  assert.deepEqual((await readDesktopV3MessageTail(ownerA.key, sessionA.id))?.messages.map((message) => message.id), [messageA1.id, messageA2.id])

  await resetHarness()
})

test('production owner and tail writes are only imported by the persistence coordinator', async () => {
  const coordinator = await readStateSource('./desktop-v3-cache-persistence-coordinator.ts')
  const persistenceController = await readStateSource('./desktop-v3-persistence-controller.ts')

  assert.match(coordinator, /import \{ writeDesktopV3OwnerAndTails \} from '\.\/desktop-v3-cache-db'/)
  assert.doesNotMatch(persistenceController, /from '\.\/desktop-v3-cache-db'/)
  assert.match(persistenceController, /desktopV3CachePersistenceCoordinator\.enqueue\(async \(\) => \{/)
})
