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
  shared.emit({ index: source, kind: 'lost', reason_code: 'selector_unavailable', error: 'Git repository selector is missing, stale, or not authorized; retry on reconnect' })
  shared.emit({ index: target, kind: 'ready' })
  resolve(3); resolve(4); await flush()
  assert.equal(state.project.tasks[0].gitStatus, 'stale', 'target ready cannot heal a lost source')
  assert.match(state.project.tasks[0].syncWarning!, /selector is missing, stale, or not authorized/, 'healthy target ready preserves the source-specific error')
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

// Requirement: board admission must preserve existing healthy slots above the
// transport limit and promote the oldest waiting lane when membership shrinks.
// DesktopProjectsRuntime owns selector order and stale fencing; the transport
// and native HTTP budget are tested separately, not inferred from this seam.
test('board admission is stable FIFO and overflow never remains fresh', { timeout: 5000 }, async () => {
  let state: DesktopProjectsState = {}
  const record = (id: string) => ({ id, session_id: id, revision: 1, status: 'needs_review', agent: 'coder',
    workspace_path: `/fixture/${id}`, worktree_branch: `agent/${id}`, base_branch: 'dev', base_commit: 'base',
    source_workspace: { path: '/fixture/repo', workspace_id: 'repo', workspace_generation: 1 },
  })
  let records = Array.from({ length: 256 }, (_, index) => record(`task-${String(index).padStart(3, '0')}`))
  const watches: Array<{ repositories: GitWatchSelector[]; released: boolean; emit: (notice: GitWatchNotice) => void }> = []
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {}, fetchTasks: async () => ({ tasks: records }), fetchMedia: async () => ({ media: [] }),
    fetchTask: async (_project, id) => ({ task: { ...records.find(task => task.id === id), git_status: 'clean', is_integrated: true } }),
    subscribeGit: (repositories, emit) => {
      const watch = { repositories, emit, released: false }; watches.push(watch)
      queueMicrotask(() => {
        repositories.forEach((_, index) => emit(index < 256 ? { index, kind: 'ready' } : {
          index, kind: 'lost', reason_code: 'watch_capacity', error: 'Git watch capacity reached',
        }))
      })
      return () => { watch.released = true }
    },
  })
  const lease = runtime.acquire('project')
  try {
    await lease.ready
    for (let i = 0; i < 40; i++) await flush()
    const first = watches[0].repositories
    assert.equal(first.length, 257)
    assert.equal(first[0].session_id, undefined, 'shared target admitted before dependent lanes')
    assert.equal(state.project.tasks.find(task => task.id === 'task-000')!.gitStatus, 'clean')
    assert.equal(state.project.tasks.find(task => task.id === 'task-255')!.gitStatus, 'stale')
    records = [record('aaa-new'), ...records.slice().reverse()]
    await runtime.refresh('project', false)
    for (let i = 0; i < 40; i++) await flush()
    assert.deepEqual(watches.at(-1)!.repositories.slice(0, 256), first.slice(0, 256), 'new earlier-sorting task cannot evict admitted lanes')
    assert.match(state.project.tasks.find(task => task.id === 'task-255')!.syncWarning!, /capacity/)
    assert.equal(state.project.tasks.find(task => task.id === 'aaa-new')!.gitStatus, 'stale')
    runtime.archiveReceipt('project', { id: 'task-000', revision: 2, archived: true })
    for (let i = 0; i < 40; i++) await flush()
    assert.ok(watches.at(-1)!.repositories.slice(0, 256).some(selector => selector.session_id === 'task-255'))
    assert.equal(state.project.tasks.find(task => task.id === 'task-255')!.gitStatus, 'clean')
    assert.equal(state.project.tasks.find(task => task.id === 'aaa-new')!.gitStatus, 'stale')
    assert.ok(watches.slice(0, -1).every(watch => watch.released))
  } finally { lease.release(); runtime.reset() }
  assert.ok(watches.every(watch => watch.released))
})
