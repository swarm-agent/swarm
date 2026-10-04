import test from 'node:test'
import assert from 'node:assert/strict'
import { mediaSelectionCards } from './media-selection'
import type { RunningTask } from './orchestrate-types'
import type { ProjectDesign } from '../session-v3/design-api'

// Purpose: mediaSelectionCards owns the UI count/selection projection. Pure tests
// are the narrowest layer to prove thread grouping and exact archive scope without
// invoking providers or confusing code-task attachments with generation records.
test('card counts group turns, not candidates, and exclude code with media', () => {
  const rows = [
    { id: 'one', title: 'Image', agentType: 'image', deliverables: [{ id: 'out' }] },
    { id: 'two', title: 'Edit', agentType: 'image', deliverables: [{ id: 'edit', parentDeliverableId: 'out' }] },
    { id: 'code', title: 'Code', agentType: 'coder', deliverables: [{ id: 'code-out' }], attachedMedia: [{ id: 'out' }] },
  ] as RunningTask[]
  const cards = mediaSelectionCards(rows, [])
  assert.equal(cards.length, 1)
  assert.deepEqual(cards[0].records.map(record => record.kind === 'task' && record.task.id), ['one', 'two'])
  const selected = new Set(cards[0].records.map(record => record.key))
  const later = mediaSelectionCards([...rows, { id: 'three', agentType: 'image', deliverables: [{ id: 'new', parentDeliverableId: 'edit' }] } as RunningTask], [])
  assert.equal(later[0].records.filter(record => selected.has(record.key)).length, 2)
  assert.equal(later[0].records.length, 3, 'a new turn cannot expand a stored selection')
})

// Purpose: design archive authority is artifact-level, not request- or revision-
// level. Ensure shared revisions deduplicate, archived candidates are excluded,
// unfinished candidates are visible but not dispatched, and newest refs are used.
test('design selection deduplicates artifacts and excludes archived and unfinished records', () => {
  const ref = { artifact_id: 'design', revision: 1, sha256: 'first' }
  const rows: ProjectDesign[] = [{ project_id: 'p', title: 'Design', request: {
    id: 'first', parent_session_id: 's', state: 'succeeded', candidates: [
      { spec: { artifact_id: 'design', kind: 'html' }, state: 'succeeded', archive_version: 2, attempts: [{ number: 1, state: 'succeeded', result: ref }] },
      { spec: { artifact_id: 'hidden', kind: 'html' }, state: 'succeeded', archived: true, attempts: [{ number: 1, state: 'succeeded', result: { ...ref, artifact_id: 'hidden' } }] },
      { spec: { artifact_id: 'unfinished', kind: 'html' }, state: 'running' },
    ],
  } }, { project_id: 'p', title: 'Edit', request: {
    id: 'second', parent_session_id: 's', state: 'succeeded', candidates: [
      { spec: { artifact_id: 'design', kind: 'html', base: ref }, state: 'succeeded', archive_version: 2, attempts: [{ number: 1, state: 'succeeded', result: { ...ref, revision: 2, sha256: 'second' } }] },
    ],
  } }]
  const cards = mediaSelectionCards([], rows)
  assert.equal(cards.length, 1)
  assert.equal(cards[0].unavailable, 1)
  assert.equal(cards[0].records.length, 1)
  const record = cards[0].records[0]
  assert.equal(record.kind, 'design')
  if (record.kind === 'design') {
    assert.deepEqual(record.reference, { ...ref, revision: 2, sha256: 'second' })
    assert.equal(record.version, 2)
  }
  rows.forEach(row => row.request.candidates.forEach(candidate => { candidate.archived = true }))
  assert.deepEqual(mediaSelectionCards([], rows), [], 'archived-only requests must not inflate the active count')
})
