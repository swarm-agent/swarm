// Requirement: MediaTaskCard exposes every durable slot without expansion and
// sends actions to the exact ready output. Missing previews are not failures;
// media must never expose code integration or Reopen controls. The leaf render
// and callback boundary is the narrowest deterministic layer for this contract.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskCard } from './media-task-card'
import { mapBackendTask } from '../state/desktop-projects-state'

function buttons(node: React.ReactNode): React.ReactElement<any>[] {
  if (!React.isValidElement(node)) return []
  const props = node.props as { children?: React.ReactNode }
  return [...(node.type === 'button' ? [node] : []), ...React.Children.toArray(props.children).flatMap(buttons)]
}

test('ten slots stay visible with partial results and exact selected image actions', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', status: 'in_progress', variant_count: 10,
    deliverables: Array.from({ length: 10 }, (_, i) => ({ id: `slot-${i}`, kind: 'image', title: `Image ${i}`,
      status: i === 0 ? 'ready' : i === 1 ? 'failed' : i < 5 ? 'generating' : 'queued',
      media_url: i === 0 ? 'data:image/png;base64,selected' : undefined })) })
  const calls: unknown[] = []
  const tree = MediaTaskCard({ task, onPreview: (output, mode) => calls.push([output.id, mode]) })
  const html = renderToStaticMarkup(tree)
  assert.equal((html.match(/<section/g) || []).length, 10)
  assert.match(html, /1\/10 ready · 3 generating · 5 queued · 1 failed/)
  assert.doesNotMatch(html, /Reopen|Integrate|Plan, subtasks|Git/)
  const edit = buttons(tree).find(button => button.props.children === 'Edit')!
  edit.props.onClick()
  assert.deepEqual(calls, [['slot-0', 'fine_tune']])
  assert.equal(buttons(tree).filter(button => button.props.children === 'Edit' && !button.props.disabled).length, 1)
})

test('ready without a hydrated preview remains ready, not failed', () => {
  const task = mapBackendTask({ id: 'images', agent: 'image', deliverables: [{ id: 'one', kind: 'image', title: 'Image', status: 'ready' }] })
  const tree = MediaTaskCard({ task })
  const html = renderToStaticMarkup(tree)
  assert.match(html, /Preview loading/)
  assert.match(html, /1\/1 ready/)
  assert.equal(buttons(tree).find(button => button.props.children === 'Edit')!.props.disabled, true)
})
