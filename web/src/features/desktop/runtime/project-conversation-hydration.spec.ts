import assert from 'node:assert/strict'
import test from 'node:test'
import { hydrateProjectConversationRows } from './project-conversation-hydration'
import type { DesktopV3HydrateInput } from '../state/desktop-v3-sync-api'
import type { SyncSnapshotResponse } from '../state/desktop-v3-cache-types'

// Requirement: independent sidebar batches start concurrently, publish without a
// sibling barrier, and canceled route reads cannot publish late results. Test the
// exact production scheduling function, not source strings or synthetic timings.
test('13 conversations overlap without requesting composer resources or stale publication', async () => {
  const controller = new AbortController()
  const calls: DesktopV3HydrateInput[] = []
  const finish: Array<(value: SyncSnapshotResponse) => void> = []
  const published: string[][] = []
  const pending = hydrateProjectConversationRows(Array.from({ length: 13 }, (_, i) => `s${i}`), controller.signal, (_, ids) => published.push(ids), input => {
    calls.push(input); return new Promise(resolve => finish.push(resolve))
  })
  assert.deepEqual(calls.map(call => call.session_ids.length), [8, 5])
  assert.ok(calls.every(call => call.resources.session_view === false && call.resources.active_plan === false && call.history.mode === 'none'))
  finish[1]({} as SyncSnapshotResponse)
  await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(published, [calls[1].session_ids])
  controller.abort(); finish[0]({} as SyncSnapshotResponse)
  await pending
  assert.deepEqual(published, [calls[1].session_ids])
  assert.equal(calls.length, 2)
})
