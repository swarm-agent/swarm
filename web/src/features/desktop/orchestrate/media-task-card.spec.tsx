// Purpose: MediaTaskCard maps durable candidates into one request turn, preserving
// exact output actions and unavailable-preview guards. mediaTaskThreads must use
// parent IDs rather than shared titles. Adapter/SSR assertions are the narrowest
// layer here; actual DOM identity and keyboard actions are tested in Chromium.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskCard, mediaTaskThreads } from './media-task-card'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { CreativeCardTurn } from './creative-thread-card'

function buttons(node: React.ReactNode): React.ReactElement<any>[] {
  if (!React.isValidElement(node)) return []
  const props = node.props as { children?: React.ReactNode }
  return [...(node.type === 'button' ? [node] : []), ...React.Children.toArray(props.children).flatMap(buttons)]
}

test('ten candidates occupy one turn with exact ready image actions', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', status: 'in_progress', variant_count: 10,
    deliverables: Array.from({ length: 10 }, (_, i) => ({ id: `slot-${i}`, kind: 'image', title: `Image ${i}`,
      status: i === 0 ? 'ready' : i === 1 ? 'failed' : i < 5 ? 'generating' : 'queued',
      media_url: i === 0 ? 'data:image/png;base64,selected' : undefined })) })
  const calls: unknown[] = []
  const tree = MediaTaskCard({ task, onPreview: (output, mode) => calls.push([output.id, mode]) })!
  const html = renderToStaticMarkup(tree)
  assert.equal((html.match(/role="tab"/g) || []).length, 1)
  assert.equal((html.match(/class="creative-candidate-chip"/g) || []).length, 10)
  assert.match(html, /1 turn · 1 ready/)
  assert.doesNotMatch(html, /Reopen|Integrate|Plan, subtasks|Git/)
  const turns = tree.props.turns as CreativeCardTurn[]
  assert.equal(turns[0].outputs.length, 10)
  const edit = buttons(turns[0].outputs[0].actions).find(button => button.props.children === 'Edit')!
  edit.props.onClick()
  turns[0].outputs[0].open()
  assert.deepEqual(calls, [['slot-0', 'fine_tune'], ['slot-0', undefined]])
  assert.equal(turns[0].outputs.filter(output => output.ready).length, 1)
})

test('ready without a hydrated preview stays ready but cannot invoke UI actions', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', deliverables: [{ id: 'one', kind: 'image', title: 'Image', status: 'ready' }] })
  const tree = MediaTaskCard({ task })!
  const html = renderToStaticMarkup(tree)
  assert.match(html, /Preview loading/)
  assert.match(html, /1 turn · 1 ready/)
  const output = (tree.props.turns as CreativeCardTurn[])[0].outputs[0]
  assert.equal(output.ready, false)
  assert.equal(buttons(output.actions).find(button => button.props.children === 'Edit')!.props.disabled, true)
})

test('task grouping follows exact output parents and excludes code tasks', () => {
  const root = mapBackendTask({ id: 'root', title: 'Same title', agent: 'image', deliverables: [{ id: 'a', kind: 'image', status: 'ready' }] })
  const child = { ...root, id: 'child', deliverables: [{ ...root.deliverables![0], id: 'b', parentDeliverableId: 'a' }] }
  const independent = { ...root, id: 'independent', deliverables: [{ ...root.deliverables![0], id: 'c' }] }
  const code = { ...root, id: 'code', agentType: 'coder' }
  assert.deepEqual(mediaTaskThreads([child, independent, root, code]).map(thread => ({ id: thread.id, turns: thread.turns.map(task => task.id) })), [
    { id: 'root', turns: ['root', 'child'] }, { id: 'independent', turns: ['independent'] },
  ])
})

// Purpose: replacing card presentation must retain the owner's approval/archive/
// delete callbacks and permission surface, never turn an edit into UI-only status.
// Direct adapter closure assertions prove routing without mutating backend state.
test('card preserves attention and invokes only the explicitly selected task action', () => {
  const task = mapBackendTask({ id: 'pending', agent: 'video', status: 'pending_approval', deliverables: [] })
  const calls: string[] = []
  const tree = MediaTaskCard({ task, attention: <section aria-label="Task needs your attention">Review permission</section>,
    onApprove: () => calls.push('approve:pending'), onArchive: () => calls.push('archive:pending'), onDelete: () => calls.push('delete:pending') })!
  assert.match(renderToStaticMarkup(tree), /Task needs your attention/)
  assert.deepEqual(calls, [])
  const controls = buttons((tree.props.turns as CreativeCardTurn[])[0].controls)
  for (const name of ['Generate media', 'Archive', 'Delete']) controls.find(button => button.props.children === name)!.props.onClick()
  assert.deepEqual(calls, ['approve:pending', 'archive:pending', 'delete:pending'])
  assert.equal(task.status, 'pending_approval')
  const approving = MediaTaskCard({ task, isApproving: true, onApprove: () => calls.push('duplicate') })!
  assert.equal(buttons((approving.props.turns as CreativeCardTurn[])[0].controls)[0].props.disabled, true)
  const failed = MediaTaskCard({ task: { ...task, status: 'failed' }, onApprove: () => calls.push('unauthorized-retry') })!
  assert.equal(buttons((failed.props.turns as CreativeCardTurn[])[0].controls).length, 0)
  assert.deepEqual(calls, ['approve:pending', 'archive:pending', 'delete:pending'])
})
