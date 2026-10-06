// Purpose: TaskListHeader/TaskListToolbar own the header presentation contract:
// honest per-workspace status, zero-count accessible tabs and selection callbacks.
// Prevent false clean Git claims and broken bulk selection. Leaf rendering/action
// tests are the narrowest proof; browser tests prove focus, popovers and geometry.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskListHeader, TaskListToolbar, WorkspaceGitState, taskSearchShortcut } from './task-list-toolbar'
import type { ProjectWorkspaceGit } from '../runtime/use-project-workspace-git'
import type { GitSnapshot } from '../git/types'

const noop = () => {}
const props = {
  search: '', onSearch: noop, source: 'all' as const, onSource: noop,
  status: 'all' as const, onStatus: noop,
  counts: { all: 4, running: 0, needs_review: 3, queued: 0, completed: 1 },
  total: 4, selected: 0, busy: false,
  onSelectAll: noop, onClear: noop, onArchive: noop, onDelete: noop, onArchived: noop,
}
const snapshot: GitSnapshot = {
  workspace_path: '/fixture/repo', has_git: true, clean: true, branch: 'dev',
  ahead_count: 0, behind_count: 0, dirty_count: 0, staged_count: 0,
  modified_count: 0, untracked_count: 0, conflict_count: 0, stash_count: 0,
  files: [], refreshed_at: '', duration_ms: 0,
}
const workspace: ProjectWorkspaceGit = { key: 'repo', name: 'repo', path: '/fixture/repo', loading: false, status: snapshot }
function elements(node: React.ReactNode): React.ReactElement<any>[] {
  if (Array.isArray(node)) return node.flatMap(elements)
  if (!React.isValidElement<{ children?: React.ReactNode }>(node)) return []
  return [node, ...elements(node.props.children)]
}

test('header shows three workspace chips plus overflow and no orchestrator badge', () => {
  const html = renderToStaticMarkup(<TaskListHeader workspaces={Array.from({ length: 6 }, (_, i) => ({ ...workspace, key: String(i), name: `repo-${i}` }))} selectedWorkspace={null} onWorkspace={noop} onRefreshGit={noop} onNewTask={noop} />)
  assert.match(html, /Swarm Local/)
  assert.match(html, /6 workspaces/)
  assert.match(html, /\+3 more/)
  assert.match(html, /New task/)
  assert.equal((html.match(/class="swarm-workspace-chip"/g) || []).length, 3)
  assert.doesNotMatch(html, /repo-3|Orchestrator/)
})

test('Git state has no clean marker and never substitutes zeros for loading or failure', () => {
  assert.equal(renderToStaticMarkup(<WorkspaceGitState workspace={workspace} />), '')
  const render = (overrides: Partial<ProjectWorkspaceGit>) => renderToStaticMarkup(<WorkspaceGitState workspace={{ ...workspace, ...overrides }} />)
  assert.match(render({ status: undefined, loading: true }), /Loading…/)
  const failed = render({ error: 'Read failed' })
  assert.match(failed, /Status unavailable/)
  assert.doesNotMatch(failed, /changed files|ahead/)
  assert.match(render({ status: { ...snapshot, has_git: false } }), /No Git repository/)
  const dirty = render({ status: { ...snapshot, dirty_count: 3, ahead_count: 2, behind_count: 1 } })
  assert.match(dirty, /3 changed files/)
  assert.match(dirty, /↑2.*↓1/)
  assert.match(render({ status: { ...snapshot, conflict_count: 1 } }), /aria-label="Conflicts"/)
  assert.match(render({ status: { ...snapshot, branch: 'detached' } }), /aria-label="Detached HEAD"/)
})

test('bulk controls appear only with selection, and checkbox delegates select/clear', () => {
  const empty = renderToStaticMarkup(<TaskListToolbar {...props} />)
  assert.match(empty, /4 tasks/)
  assert.match(empty, /type="checkbox"/)
  assert.doesNotMatch(empty, />Archive<|>Delete<|Clear selection/)
  const selected = renderToStaticMarkup(<TaskListToolbar {...props} selected={2} />)
  assert.match(selected, /2 selected/)
  assert.match(selected, /Clear selection/)
  for (const label of ['Archive', 'Delete']) assert.match(selected, new RegExp(`>${label}<`))
  const calls: string[] = []
  for (const count of [0, 2]) {
    const tree = TaskListToolbar({ ...props, selected: count, onSelectAll: () => calls.push('select'), onClear: () => calls.push('clear') })
    elements(tree).find(node => node.type === 'input' && node.props.type === 'checkbox')!.props.onChange()
  }
  assert.deepEqual(calls, ['select', 'clear'])
  assert.match(renderToStaticMarkup(<TaskListToolbar {...props} busy />), /type="checkbox"[^>]*disabled/)
})

test('zero-count tabs stay actionable; tabs and source controls dispatch existing filters', () => {
  const calls: string[] = []
  const tree = TaskListToolbar({ ...props, onStatus: value => calls.push(value), onSource: value => calls.push(value) })
  const buttons = elements(tree).filter(node => node.type === 'button')
  const running = buttons.find(node => node.props.children?.includes?.('Running'))!
  assert.equal(running.props.disabled, undefined)
  assert.equal(running.props.role, 'tab')
  assert.equal(running.props['aria-selected'], false)
  running.props.onClick()
  const worker = buttons.find(node => node.props['data-testid'] === 'filter-worker-tasks')!
  worker.props.onClick()
  assert.deepEqual(calls, ['running', 'worker'])
  const html = renderToStaticMarkup(tree)
  assert.match(html, /role="tablist"/)
  assert.equal((html.match(/role="tab"/g) || []).length, 6)
  assert.match(html, /data-zero="true"/)
})

test('slash shortcut ignores editable targets, modifiers and previously handled keys', () => {
  const event = { key: '/', altKey: false, ctrlKey: false, metaKey: false, defaultPrevented: false }
  assert.equal(taskSearchShortcut(event, null), true)
  const editable = { closest: () => ({}) } as unknown as Element
  assert.equal(taskSearchShortcut(event, editable), false)
  for (const key of ['altKey', 'ctrlKey', 'metaKey', 'defaultPrevented']) assert.equal(taskSearchShortcut({ ...event, [key]: true }, null), false)
  assert.equal(taskSearchShortcut({ ...event, key: 'a' }, null), false)
})
