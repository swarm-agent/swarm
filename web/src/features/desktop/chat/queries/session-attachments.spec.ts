import assert from 'node:assert/strict'
import test from 'node:test'
import { selectSessionAttachments, SessionAttachmentInventory, type AttachmentRepository } from './session-attachments'

export function attachment(index: number): AttachmentRepository {
  return { id: `row-${index}`, workspace_id: `workspace-${index}`, workspace_name: 'Same name', source_path: `/workspaces/source-${index}`, kind: 'source', attached: true, default: index === 63, availability: 'available' }
}
// Requirement: header reflects current attached source identities, never history or
// ordering-derived defaults. selectSessionAttachments is the narrow projection
// boundary; pure fixtures prove collision, capacity and replacement postconditions.
test('64 same-name attachments remain distinct and default is not the first', () => {
  const items = Array.from({ length: 64 }, (_, i) => attachment(i))
  const pages = Array.from({ length: 4 }, (_, i) => ({ ok: true, items: items.slice(i * 20, i * 20 + 20) }))
  const result = selectSessionAttachments(pages)
  assert.equal(result.length, 64)
  assert.deepEqual(result.filter((item) => item.default).map((item) => item.workspace_id), ['workspace-63'])
  assert.equal(result[0].default, false)
})
test('removed, runtime, and identity-less rows cannot become attachments; refresh replaces default', () => {
  const prior = [attachment(0), attachment(63)]
  assert.equal(selectSessionAttachments([{ ok: true, items: prior }]).length, 2)
  const current = [{ ...attachment(0), default: true }, { ...attachment(63), attached: false }, { ...attachment(2), kind: 'worker' }, { ...attachment(3), workspace_id: '' }]
  const result = selectSessionAttachments([{ ok: true, items: current }])
  assert.deepEqual(result.map((item) => [item.workspace_id, item.default]), [['workspace-0', true]])
  assert.equal(selectSessionAttachments([{ ok: true, items: [attachment(0), { ...attachment(0), default: true }] }])[0].default, false)
})

// Requirement: history preceding attachments must not hide them after page 16.
// The header's transport/state boundary must retain <=64 attachment rows, fetch
// <=4 pages per gesture, and preserve stale data on failure until fresh recovery.
test('manual windows reach all late attachments with bounded storage and recover refresh failure', async () => {
  const all = [...Array.from({ length: 400 }, (_, i) => ({ ...attachment(i), attached: false, kind: 'worker' })), ...Array.from({ length: 64 }, (_, i) => attachment(i))]
  let calls = 0; let fail = false
  const inventory = new SessionAttachmentInventory(async cursor => {
    calls++
    if (fail) throw new Error('403')
    const offset = Number(cursor || 0)
    return { ok: true, items: all.slice(offset, offset + 20), next_cursor: offset + 20 < all.length ? String(offset + 20) : '' }
  })
  await inventory.refresh()
  assert.equal(calls, 4); assert.equal(inventory.state.items.length, 0)
  for (let i = 0; i < 5; i++) {
    const before = calls
    await inventory.loadMore()
    assert.ok(calls - before <= 4)
    assert.ok(inventory.state.items.length <= 64)
    assert.ok(inventory.state.items.every(item => item.attached && item.kind === 'source'))
  }
  assert.equal(calls, 24)
  assert.equal(inventory.state.items.length, 64)
  assert.equal(inventory.state.nextCursor, '')
  assert.equal(inventory.state.items[63].default, true)
  fail = true; await inventory.refresh()
  assert.equal(inventory.state.error, true); assert.equal(inventory.state.stale, true)
  assert.equal(inventory.state.items.length, 64)
  fail = false; await inventory.refresh()
  assert.equal(inventory.state.error, false); assert.equal(inventory.state.stale, false)
  assert.equal(inventory.state.items.length, 0)
  assert.notEqual(inventory.state.nextCursor, '')
})
