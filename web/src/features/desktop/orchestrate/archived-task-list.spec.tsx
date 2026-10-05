import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { ArchivedTaskControls } from './archived-task-list'
import type { RunningTask } from './orchestrate-types'

// Purpose: ArchivedTaskControls exposes restore, never archive, for exact selected
// records; pending prevents duplicate clicks and partial failures retain controls.
// Pure event props plus rendered accessibility prove this without a browser cache.
test('archived selection restores exact rows and exposes pending/errors', () => {
  const tasks = [{ id: 'a', title: 'A', revision: 2, status: 'needs_review' }, { id: 'b', title: 'B', revision: 6, status: 'blocked' }] as RunningTask[]
  let selected = new Set<string>()
  let restored: RunningTask[] = []
  const props = { tasks, busy: false, errors: {}, selected, setSelected: (update: Set<string> | ((previous: Set<string>) => Set<string>)) => { selected = typeof update === 'function' ? update(selected) : update }, onUnarchive: (rows: RunningTask[]) => { restored = rows } }
  function buttons(node: React.ReactNode): React.ReactElement<any>[] {
    if (!React.isValidElement(node)) return []
    const element = node as React.ReactElement<any>
    return [...(element.type === 'button' ? [element] : []), ...React.Children.toArray(element.props.children).flatMap(buttons)]
  }
  let controls = buttons(ArchivedTaskControls(props))
  controls.find(button => button.props.children === 'Select all archived tasks')!.props.onClick()
  controls = buttons(ArchivedTaskControls({ ...props, selected }))
  controls.find(button => button.props.children === 'Unarchive selected')!.props.onClick()
  assert.deepEqual(restored, tasks)
  controls.find(button => button.props['aria-label'] === 'Unarchive task: B')!.props.onClick()
  assert.deepEqual(restored, [tasks[1]])
  const partial = { ...props, tasks: [tasks[1]], selected: new Set(['b']), errors: { b: 'stale revision' } }
  const markup = renderToStaticMarkup(<ArchivedTaskControls {...partial} />)
  assert.match(markup, /role="alert">stale revision/)
  assert.match(markup, /Unarchive task: B/)
  assert.doesNotMatch(markup, /Archive task:/)
  assert.ok(buttons(ArchivedTaskControls({ ...partial, busy: true })).every(button => button.props.disabled))
})
