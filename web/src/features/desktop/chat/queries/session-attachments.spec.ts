import assert from 'node:assert/strict'
import test from 'node:test'
import { selectSessionAttachments, type AttachmentRepository } from './session-attachments'

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
