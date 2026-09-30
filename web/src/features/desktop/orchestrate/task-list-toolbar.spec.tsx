// Purpose: TaskListHeader/TaskListToolbar must expose honest orchestrator state,
// accessible single-select filters (including zero counts), and conditional bulk
// controls. These leaf render/action tests are the narrowest presentation layer;
// full-view filter composition, focus, layout and console checks need browser validation.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskListHeader, TaskListToolbar } from './task-list-toolbar'

const noop = () => {}
const props = {
  search: '', onSearch: noop, source: 'all' as const, onSource: noop,
  status: 'all' as const, onStatus: noop,
  counts: { all: 4, running: 0, needs_review: 3, queued: 0, completed: 1 },
  total: 4, selected: 0, busy: false,
  onSelectAll: noop, onClear: noop, onArchive: noop, onDelete: noop, onArchived: noop,
}
function elements(node: React.ReactNode): React.ReactElement<any>[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!React.isValidElement<{ children?: React.ReactNode }>(node)) return []
  return [node, ...elements(node.props.children)]
}

test('header renders live metadata and never reports unavailable state as active', () => {
  for (const state of ['active', 'inactive', 'unknown'] as const) {
    const html = renderToStaticMarkup(<TaskListHeader title="Swarm Local" branch="feature/test" workspaceCount={6} orchestratorState={state} onNewTask={noop} />)
    assert.match(html, /feature\/test/)
    assert.match(html, /6 workspaces/)
    assert.match(html, /New task/)
    assert.match(html, new RegExp(`Orchestrator ${state === 'unknown' ? 'state unavailable' : state}`))
    if (state !== 'active') assert.doesNotMatch(html, /Orchestrator active/)
  }
})

test('bulk controls are absent until selection exists and disappear when cleared', () => {
  const empty = renderToStaticMarkup(<TaskListToolbar {...props} />)
  assert.match(empty, /4 tasks/)
  assert.doesNotMatch(empty, />Clear<|>Archive<|>Delete<|selected/)
  const selected = renderToStaticMarkup(<TaskListToolbar {...props} selected={2} />)
  assert.match(selected, /2 of 4 selected/)
  for (const label of ['Clear', 'Archive', 'Delete']) assert.match(selected, new RegExp(`>${label}<`))
  assert.match(empty, /h-11/)
  assert.match(selected, /h-11/)
})

test('zero-count status chips stay actionable and scope controls omit counts', () => {
  const calls: string[] = []
  const tree = TaskListToolbar({ ...props, onStatus: value => calls.push(value), onSource: value => calls.push(value) })
  const buttons = elements(tree).filter(node => node.type === 'button')
  const running = buttons.find(node => node.props.children?.includes?.('Running'))!
  assert.ok(running)
  assert.equal(running.props.disabled, undefined)
  assert.equal(running.props['aria-pressed'], false)
  running.props.onClick()
  const worker = buttons.find(node => node.props['data-testid'] === 'filter-worker-tasks')!
  assert.equal(worker.props.children, 'Workers')
  worker.props.onClick()
  assert.deepEqual(calls, ['running', 'worker'])
  assert.match(renderToStaticMarkup(tree), /text-slate-600/)
})

test('selection and bulk buttons dispatch their supplied handlers without changing selection authority', () => {
  const calls: string[] = []
  const tree = TaskListToolbar({ ...props, selected: 2,
    onSelectAll: () => calls.push('select'), onClear: () => calls.push('clear'),
    onArchive: () => calls.push('archive'), onDelete: () => calls.push('delete'),
  })
  const buttons = elements(tree).filter(node => node.type === 'button')
  for (const label of ['Select all', 'Clear', 'Archive', 'Delete']) {
    const button = buttons.find(node => renderToStaticMarkup(node).includes(`>${label}<`))!
    assert.ok(button, label)
    button.props.onClick()
  }
  assert.deepEqual(calls, ['select', 'clear', 'archive', 'delete'])
})
