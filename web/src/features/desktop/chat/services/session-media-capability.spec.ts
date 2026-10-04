import test from 'node:test'
import assert from 'node:assert/strict'
import { SessionMediaCapabilityReader } from './session-media-capability'
import type { DesktopV3MediaCapability } from '../../state/desktop-v3-cache-types'

const capability: DesktopV3MediaCapability = { status: 'available', contract_version: 1, capabilities: [], provider: 'configured', model: 'configured' }
const input = { scope: 'account:a', authority: 'model-a', hydrated: capability, ready: true, connected: true }

// Purpose: SessionMediaCapabilityReader is the composer read coordinator. Hydration
// must be the sole initial authority; deferred hydration cannot trigger a duplicate
// GET. This pure async layer tests request counts without mounting unrelated UI.
test('uses hydration once and refreshes on preference and reconnect, exposing failure', async () => {
  const reader = new SessionMediaCapabilityReader()
  let reads = 0
  const values: Array<DesktopV3MediaCapability | null> = []
  const errors: Array<string | undefined> = []
  const publish = (value: DesktopV3MediaCapability | null, error?: string) => { values.push(value); errors.push(error) }
  const read = async () => { reads++; return capability }
  await reader.update({ ...input, ready: false, hydrated: null }, read, publish)
  assert.equal(reads, 0)
  await reader.update(input, read, publish)
  await reader.update(input, read, publish)
  assert.equal(reads, 0)
  assert.deepEqual(values, [capability])
  await reader.update({ ...input, authority: 'model-b' }, read, publish)
  assert.equal(reads, 1)
  await reader.update({ ...input, authority: 'model-b', connected: false }, read, publish)
  await reader.update({ ...input, authority: 'model-b' }, async () => { reads++; throw new Error('denied') }, publish)
  assert.equal(reads, 2)
  assert.equal(values.at(-1), null, 'failed refresh must not revive hydrated authorization')
  assert.equal(errors.at(-1), 'denied')
})

// Purpose: the same reader must reject stale A-B-A responses and account-reset
// completions. Ignoring cancellation deliberately proves the generation guard,
// not transport cooperation, prevents obsolete capability publication.
test('late capability requests cannot publish across A-B-A or reset', async () => {
  const reader = new SessionMediaCapabilityReader()
  const values: Array<DesktopV3MediaCapability | null> = []
  const publish = (value: DesktopV3MediaCapability | null) => { values.push(value) }
  let finish!: (value: DesktopV3MediaCapability) => void
  const first = reader.update({ ...input, hydrated: null }, () => new Promise(resolve => { finish = resolve }), publish)
  await reader.update({ ...input, scope: 'account:b' }, async () => capability, publish)
  await reader.update(input, async () => capability, publish)
  const before = values.length
  finish({ ...capability, model: 'obsolete' })
  await first
  assert.equal(values.length, before)
  const pending = reader.update({ ...input, authority: 'new-model' }, () => new Promise(resolve => { finish = resolve }), publish)
  reader.reset()
  finish(capability)
  await pending
  assert.equal(values.at(-1), null)
})

// Purpose: the composer must expose canonical hydration while optional credential
// discovery is pending. Reader-level request/publication assertions prove initial
// discovery does not duplicate GET, while a later revocation fails closed visibly.
test('initial credential discovery is optional but later credential changes reauthorize', async () => {
  const reader = new SessionMediaCapabilityReader()
  let reads = 0
  const values: Array<DesktopV3MediaCapability | null> = []
  const errors: Array<string | undefined> = []
  const publish = (value: DesktopV3MediaCapability | null, error?: string) => { values.push(value); errors.push(error) }
  const read = async () => { reads++; throw new Error('credential revoked') }
  await reader.update(input, read, publish)
  assert.deepEqual(values, [capability], 'optional query must not gate hydration')
  await reader.update({ ...input, credentials: 'connected' }, read, publish)
  assert.equal(reads, 0, 'initial credential query completion is not an invalidation')
  await reader.update({ ...input, credentials: 'revoked' }, read, publish)
  assert.equal(reads, 1)
  assert.equal(values.at(-1), null)
  assert.equal(errors.at(-1), 'credential revoked')
  await reader.update({ ...input, credentials: 'revoked' }, read, publish)
  assert.equal(reads, 1)
  assert.equal(values.at(-1), null, 'unchanged hydrated data must not revive denied capability')
})

// Requirement: optional hydrate resolution errors stay visible without a duplicate
// capability GET or stale authority. The mounted-consumer coordinator owns this.
test('hydrated resolution failure is visible without another request', async () => {
  const reader = new SessionMediaCapabilityReader()
  let calls = 0
  let actual: unknown
  await reader.update({ scope: 'account/session', authority: 'model', ready: true, connected: true,
    hydrated: { status: 'unavailable', capabilities: [], resolution_error: 'scope resolver unavailable' } },
    async () => { calls++; throw new Error('unexpected duplicate') },
    (value, error) => { actual = { value, error } })
  assert.equal(calls, 0)
  assert.deepEqual(actual, { value: null, error: 'scope resolver unavailable' })
})
