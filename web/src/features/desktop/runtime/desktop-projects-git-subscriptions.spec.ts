import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import type { GitWatchNotice, GitWatchSelector } from '../git/subscriptions'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'

const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }

// Requirement: board-owned source/target subscriptions fence stale responses,
// deduplicate ownership and never inspect idle/unrelated repositories. This
// runtime seam tests loss, burst and teardown ordering deterministically; the
// native producer/HTTP/transport path is tested separately by the Go fixture.
test('board subscriptions are scoped, coalesced, loss-fenced and released', { timeout: 5000 }, async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'] })
  let state: DesktopProjectsState = {}
  const records = ['a', 'b', 'foreign'].map(id => ({ id, session_id: id, revision: 1, status: 'needs_review', agent: 'coder',
    workspace_path: `/fixture/${id}`, worktree_branch: `agent/${id}`, base_branch: 'dev', base_commit: 'base',
    source_workspace: { path: id === 'foreign' ? '/fixture/other' : '/fixture/repo', workspace_id: id === 'foreign' ? 'other' : 'repo', workspace_generation: 1 },
  }))
  const watches: Array<{ repositories: GitWatchSelector[]; emit: (notice: GitWatchNotice) => void; released: boolean }> = []
  const reads: Array<{ id: string; resolve: (value: any) => void }> = []
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {},
    subscribeGit: (repositories, emit) => { const watch = { repositories, emit, released: false }; watches.push(watch); return () => { watch.released = true } },
    fetchTasks: async () => ({ tasks: records }), fetchMedia: async () => ({ media: [] }),
    fetchTask: (_project, id) => new Promise(resolve => { reads.push({ id, resolve }) }),
  })
  const resolve = (index: number) => reads[index].resolve({ task: { ...records.find(task => task.id === reads[index].id), git_status: 'clean', is_integrated: true } })
  const lease = runtime.acquire('project'); await lease.ready
  resolve(0); resolve(1); await flush(); resolve(2); await flush()
  assert.equal(watches.length, 1, 'one stream avoids browser connection starvation')
  const shared = watches.find(watch => watch.repositories.some(selector => selector.workspace_path === '/fixture/repo'))!
  assert.equal(shared.repositories.length, 5, 'three sources and two deduplicated targets')
  const target = shared.repositories.findIndex(selector => selector.workspace_path === '/fixture/repo')
  const source = shared.repositories.findIndex(selector => selector.workspace_path === '/fixture/a')
  t.mock.timers.tick(3_600_000); await flush()
  assert.equal(reads.length, 3)
  for (let i = 0; i < 25; i++) shared.emit({ index: target, kind: 'changed' })
  await flush()
  assert.deepEqual(reads.slice(3).map(read => read.id).sort(), ['a', 'b'])
  shared.emit({ index: source, kind: 'lost' })
  shared.emit({ index: target, kind: 'ready' })
  resolve(3); resolve(4); await flush()
  assert.equal(state.project.tasks[0].gitStatus, 'stale', 'target ready cannot heal a lost source')
  assert.match(state.project.tasks[0].syncWarning!, /watch unavailable/)
  assert.equal(state.project.tasks[2].gitStatus, 'clean', 'foreign repository is untouched')
  assert.equal(reads[5].id, 'b'); resolve(5); await flush()
  shared.emit({ index: source, kind: 'ready' }); await flush()
  assert.equal(reads[6].id, 'a'); resolve(6); await flush()
  assert.equal(state.project.tasks[0].gitStatus, 'clean')
  assert.equal(state.project.tasks[0].syncWarning, undefined)
  lease.release()
  assert.ok(watches.every(watch => watch.released))
  shared.emit({ index: target, kind: 'changed' }); await flush()
  assert.equal(reads.length, 7)
})
