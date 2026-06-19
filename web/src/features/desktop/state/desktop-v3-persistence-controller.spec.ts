import 'fake-indexeddb/auto'

import assert from 'node:assert/strict'
import test from 'node:test'

import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import { readDesktopV3MessageTail, readDesktopV3Owner, resetDesktopV3CacheDBForTests } from './desktop-v3-cache-db'
import { buildPersistedDesktopV3MessageTailV1 } from './desktop-v3-cache-persisted-types'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, resetDesktopV3CacheForTests } from './desktop-v3-cache-store'
import { restoreDesktopV3CacheFromActiveOwner } from './desktop-v3-bootstrap-controller'
import {
  buildPersistedDesktopV3MessageTailV1FromState,
  buildPersistedDesktopV3OwnerV1FromState,
  createDesktopV3PersistenceController,
  startDesktopV3PersistenceController,
  stopDesktopV3PersistenceControllerForTests,
} from './desktop-v3-persistence-controller'
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
} from './desktop-v3-cache.backend-fixtures'
import type { DesktopV3CacheAction, DesktopV3CacheState, MessageSnapshot } from './desktop-v3-cache-types'

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

test('transient live-run overlays are never written', () => {
  const state = persistedStateFixture()
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
  assert.equal(JSON.stringify(owner).includes('draft assistant text'), false)
  assert.equal(JSON.stringify(owner).includes('streaming tool delta'), false)
  assert.equal(JSON.stringify(tail).includes('draft assistant text'), false)
})

test('failed IndexedDB write leaves in-memory cache usable', async () => {
  await resetHarness()
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => ownerA,
    writeOwnerAndTails: async () => false,
  })
  controller.start()

  dispatchDesktopV3Cache(bootstrapResponseToAction(snapshotFixture({ messages_by_session: {}, events_by_session: {} })))
  await controller.flushNow()

  assert.equal(getDesktopV3CacheSnapshot().sessionsById[sessionA.id]?.kind, 'full')
  controller.stop()
  await resetHarness()
})

test('owner change cannot write data into the previous owner partition', async () => {
  await resetHarness()
  let resolveCalls = 0
  const writes: string[] = []
  const controller = createDesktopV3PersistenceController({
    resolveOwner: () => {
      resolveCalls += 1
      return resolveCalls === 1 ? ownerA : ownerB
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
