import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import {
  computeActiveTaskSessionIds,
  computeActiveTaskSessionIdsKey,
  TaskSessionLeaseManager,
  type RealtimeDemandController,
  type SessionDemandLease,
} from './desktop-projects-membership'
import {
  reduceDesktopProjectsState,
  type DesktopProjectsState,
} from '../state/desktop-projects-state'
import type { RunningTask, ProjectTaskMediaRef } from '../orchestrate/orchestrate-types'

// =============================================================================
// Requirement 1: Coalesced Single-Flight + Dirty Trailing Refresh
// Threat: Realtime event bursts or rapid mutations trigger concurrent HTTP storms,
//         saturating the backend and causing high CPU churn in the browser.
// Authority: DesktopProjectsRuntime.refresh, invalidate, and inFlight map
// =============================================================================
test('Requirement 1: in-flight event burst coalesces into exactly one trailing refresh', async () => {
  // Written Purpose:
  // - Requirement: When a project tasks/media refresh is in-flight, any subsequent invalidations
  //   (realtime frames, local mutations) must be coalesced so only a single trailing refresh executes.
  // - Threat/regression: Network storm and CPU churn from duplicate in-flight requests.
  // - Boundary: DesktopProjectsRuntime.refresh, inFlight, generation check in finally.
  let state: DesktopProjectsState = {}
  let fetchTasksCount = 0
  let fetchMediaCount = 0
  const taskResolvers: Array<(res: { tasks: any[] }) => void> = []
  const mediaResolvers: Array<(res: { media: ProjectTaskMediaRef[] }) => void> = []

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: (_projectId: string) => {
      fetchTasksCount++
      return new Promise((resolve) => taskResolvers.push(resolve))
    },
    fetchMedia: (_projectId: string) => {
      fetchMediaCount++
      return new Promise((resolve) => mediaResolvers.push(resolve))
    },
  })

  const projectId = 'proj-alpha'
  const lease = runtime.acquire(projectId)

  // Initial fetch is in-flight
  assert.equal(fetchTasksCount, 1)
  assert.equal(fetchMediaCount, 1)

  // Burst of 5 realtime events while first request is in-flight
  for (let i = 0; i < 5; i++) {
    runtime.acceptFrame({ kind: 'project.updated', project_id: projectId })
  }

  // Must remain single-flight: no concurrent fetches launched
  assert.equal(fetchTasksCount, 1)
  assert.equal(fetchMediaCount, 1)

  // Resolve first fetch
  const resolverTask1 = taskResolvers.shift()!
  const resolverMedia1 = mediaResolvers.shift()!
  resolverTask1({
    tasks: [{ id: 'task-1', title: 'Task 1', agent: 'coder', status: 'in_progress' }],
  })
  resolverMedia1({ media: [] })

  await lease.ready

  // Because invalidations arrived while in flight, exactly ONE trailing refresh should be triggered
  assert.equal(fetchTasksCount, 2)
  assert.equal(fetchMediaCount, 2)

  // Resolve second fetch
  const resolverTask2 = taskResolvers.shift()!
  const resolverMedia2 = mediaResolvers.shift()!
  resolverTask2({
    tasks: [
      { id: 'task-1', title: 'Task 1', agent: 'coder', status: 'completed' },
      { id: 'task-2', title: 'Task 2', agent: 'designer', status: 'in_progress' },
    ],
  })
  resolverMedia2({ media: [] })

  // Flush microtasks
  await new Promise((resolve) => setTimeout(resolve, 0))

  // No further trailing fetches should occur
  assert.equal(fetchTasksCount, 2)
  assert.equal(fetchMediaCount, 2)
  assert.equal(state[projectId].tasks.length, 2)
  assert.equal(state[projectId].tasks[0].status, 'completed')
  assert.equal(state[projectId].tasks[1].status, 'running')
  assert.equal(state[projectId].loading, false)
  assert.equal(state[projectId].stale, false)

  lease.release()
})

// =============================================================================
// Requirement 2: No Idle Requests & No Error Retry Timer
// Threat: Periodic polling timers (e.g. 1.5s or 8s) or retry loops run while idle
//         or in error state, burning CPU continuously when nothing has changed.
// Authority: DesktopProjectsRuntime event-driven lifecycle
// =============================================================================
test('Requirement 2: idle projects do not emit background requests; errors fail visibly without retry loops', async () => {
  // Written Purpose:
  // - Requirement: When idle, zero requests are sent. When an error occurs, the error is recorded
  //   visibly on the project state without a retry loop or timer.
  // - Threat/regression: Background polling or error retry timer spinning at 100% CPU.
  // - Boundary: DesktopProjectsRuntime.refresh error path and absence of retry loops.
  let state: DesktopProjectsState = {}
  let fetchCount = 0

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: async (_projectId: string) => {
      fetchCount++
      throw new Error('Project not found (404)')
    },
    fetchMedia: async (_projectId: string) => {
      return { media: [] }
    },
  })

  const projectId = 'proj-deleted'
  const lease = runtime.acquire(projectId)

  await lease.ready

  assert.equal(fetchCount, 1)
  assert.equal(state[projectId].loading, false)
  assert.equal(state[projectId].stale, true)
  assert.equal(state[projectId].error, 'Project not found (404)')

  // Simulate idle passage of time: zero background requests should fire
  await new Promise((resolve) => setTimeout(resolve, 50))
  assert.equal(fetchCount, 1)

  lease.release()
  assert.equal(state[projectId], undefined)
})

// =============================================================================
// Requirement 3: Scoped Invalidations & Single-Flight Coalescing
// Threat: Account-wide or foreign project events cause unrelated projects to
//         refresh continuously, or concurrent invalidations violate single-flight.
// Authority: DesktopProjectsRuntime.acceptFrame & invalidate
// =============================================================================
test('Requirement 3: frames for other projects do not trigger refresh; in-flight frames preserve single-flight', async () => {
  // Written Purpose:
  // - Requirement: Events for other project IDs or non-project event kinds (e.g. workspace.catalog.updated)
  //   must NOT trigger a refresh of the currently open project. When multiple invalidations occur
  //   while a request is in flight, single-flight is preserved without launching concurrent fetches,
  //   and a single trailing refresh executes after completion.
  // - Threat/regression: Unrelated events triggering CPU churn or duplicate concurrent fetches.
  // - Boundary: DesktopProjectsRuntime.acceptFrame project_id scoping and single-flight lock.
  let state: DesktopProjectsState = {}
  let fetchCount = 0

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: async (_projectId: string) => {
      fetchCount++
      // Delay to test in-flight coalescing
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { tasks: [] }
    },
    fetchMedia: async (_projectId: string) => {
      return { media: [] }
    },
  })

  const projectId = 'proj-target'
  const lease = runtime.acquire(projectId)
  await lease.ready
  assert.equal(fetchCount, 1)

  // 1. Unrelated frames must NOT trigger refresh
  runtime.acceptFrame({ kind: 'workspace.catalog.updated' })
  assert.equal(fetchCount, 1)

  runtime.acceptFrame({ kind: 'project.updated', project_id: 'proj-other' })
  assert.equal(fetchCount, 1)

  runtime.acceptFrame({ kind: 'event' })
  assert.equal(fetchCount, 1)

  // 2. Matching frame triggers refresh (request 2 launched)
  runtime.acceptFrame({ kind: 'project.updated', project_id: projectId })
  assert.equal(fetchCount, 2)

  // 3. Global project invalidation arrives while request 2 is in-flight:
  // Must preserve single-flight: fetchCount MUST remain 2 synchronously!
  runtime.acceptFrame({ kind: 'project.updated' })
  assert.equal(fetchCount, 2)

  // 4. Await request 2 completion and dirty trailing refresh
  await new Promise((resolve) => setTimeout(resolve, 50))
  // Trailing refresh has now executed as request 3!
  assert.equal(fetchCount, 3)

  // 5. Subsequent unrelated frames still do not trigger refresh
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'proj-other' })
  assert.equal(fetchCount, 3)

  lease.release()
})

// =============================================================================
// Requirement 4: Reconnect and Rehydrate Single-Flight Repair
// Threat: Lost frames or connection dropouts cause the client to remain stale,
//         or rapid reconnect frames launch concurrent duplicate repairs.
// Authority: DesktopProjectsRuntime.acceptFrame cursor.error / rehydrate.required
// =============================================================================
test('Requirement 4: reconnect or rehydrate frames trigger repair refresh with single-flight coalescing', async () => {
  // Written Purpose:
  // - Requirement: On websocket reconnect or cursor error, the runtime must invalidate and refresh
  //   all actively retained projects. Rapid back-to-back reconnect frames must coalesce single-flight.
  // - Threat/regression: Stale UI after reconnection, or concurrent request storms on reconnect.
  // - Boundary: DesktopProjectsRuntime.acceptFrame handling of cursor.error and rehydrate.required.
  let state: DesktopProjectsState = {}
  let fetchCount = 0

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: async (_projectId: string) => {
      fetchCount++
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { tasks: [] }
    },
    fetchMedia: async (_projectId: string) => {
      return { media: [] }
    },
  })

  const projectId = 'proj-reconnect'
  const lease = runtime.acquire(projectId)
  await lease.ready
  assert.equal(fetchCount, 1)

  // 1. Trigger cursor error reconnect repair (starts request 2)
  runtime.acceptFrame({ kind: 'cursor.error' })
  assert.equal(fetchCount, 2)

  // 2. Trigger rehydrate required repair while request 2 is in-flight:
  // Must coalesce single-flight without launching concurrent request 3!
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  assert.equal(fetchCount, 2)

  // 3. Await request 2 completion and trailing refresh
  await new Promise((resolve) => setTimeout(resolve, 50))
  assert.equal(fetchCount, 3)

  // 4. Sequential auth credentials update triggers clean single refresh
  runtime.acceptFrame({ kind: 'auth.credentials.updated' })
  assert.equal(fetchCount, 4)
  await new Promise((resolve) => setTimeout(resolve, 50))

  lease.release()
})

// =============================================================================
// Requirement 5: Stable Membership and Incremental Lease Management
// Threat: Non-deterministic session ordering, array identity shifts, or controller
//         readiness races tear down and recreate leases, causing websocket churn.
// Authority: computeActiveTaskSessionIds and TaskSessionLeaseManager
// =============================================================================
test('Requirement 5: production membership deduplicates/sorts; TaskSessionLeaseManager handles unchanged refs, incremental leases, and readiness races', async () => {
  // Written Purpose:
  // - Requirement:
  //   1) computeActiveTaskSessionIds must deduplicate and sort session IDs deterministically.
  //   2) When tasks update with identical active session membership, zero leases are recreated.
  //   3) When sessions are added or removed, leases update incrementally.
  //   4) Readiness races (controller resolution while desired set changes) must not leak leases.
  //   5) Unmount cleanup must release all active leases.
  // - Threat/regression: Lease thrashing, websocket reconnect loops, and unmount memory leaks.
  // - Boundary: desktop-projects-membership production helpers.

  // 1. Production membership deduplication and sorting
  const rawTasks = [
    { id: 't1', sessionId: 'sess-z', status: 'running' },
    { id: 't2', sessionId: 'sess-a', status: 'in_progress' },
    { id: 't3', sessionId: 'sess-z', status: 'in_progress' }, // duplicate session
    { id: 't4', sessionId: 'sess-b', status: 'completed' },   // completed (inactive unless selected)
    { id: 't5', status: 'queued' }, // Undeployed tasks have no session to lease.
  ]

  const activeIds1 = computeActiveTaskSessionIds(rawTasks)
  assert.deepEqual(activeIds1, ['sess-a', 'sess-z'])
  assert.equal(computeActiveTaskSessionIdsKey(rawTasks), 'sess-a,sess-z')

  // Deployed queued sessions need updates before execution begins.
  assert.deepEqual(computeActiveTaskSessionIds([...rawTasks, { id: 'deployed', sessionId: 'sess-c', status: 'queued' }]), ['sess-a', 'sess-c', 'sess-z'])

  // Selected task is included even if completed
  const activeIdsWithSelected = computeActiveTaskSessionIds(rawTasks, 't4')
  assert.deepEqual(activeIdsWithSelected, ['sess-a', 'sess-b', 'sess-z'])

  // 2. Production TaskSessionLeaseManager testing
  const acquiredLeases: string[] = []
  const releasedLeases: string[] = []
  const hydratedSessions: string[] = []

  let resolveController: ((c: RealtimeDemandController) => void) | undefined
  let controllerReadyPromise = new Promise<RealtimeDemandController>((resolve) => {
    resolveController = resolve
  })

  const mockController: RealtimeDemandController = {
    acquireSessionDemand: (ownerKey: string, sid: string): SessionDemandLease => {
      acquiredLeases.push(sid)
      return {
        release: () => {
          releasedLeases.push(sid)
        },
      }
    },
  }

  const manager = new TaskSessionLeaseManager({
    getControllerReady: () => controllerReadyPromise,
    hydrate: (sid) => {
      hydratedSessions.push(sid)
    },
    ownerKeyPrefix: 'test-orchestrate',
  })

  // Reconcile initial sessions
  const handle1 = manager.reconcile(activeIds1)
  assert.deepEqual(hydratedSessions, ['sess-a', 'sess-z'])

  // Before controller resolves: no leases acquired yet
  assert.equal(acquiredLeases.length, 0)

  // Controller becomes ready!
  resolveController!(mockController)
  await new Promise((resolve) => setTimeout(resolve, 0))

  assert.deepEqual(acquiredLeases, ['sess-a', 'sess-z'])
  assert.deepEqual(releasedLeases, [])

  // 3. Unchanged refs: reconciling with identical session IDs produces zero churn!
  const unchangedSameIds = ['sess-a', 'sess-z']
  manager.reconcile(unchangedSameIds)
  assert.deepEqual(acquiredLeases, ['sess-a', 'sess-z']) // No new acquisitions
  assert.deepEqual(releasedLeases, [])                   // No releases

  // 4. Incremental addition: new session arrives
  const activeIds2 = ['sess-a', 'sess-m', 'sess-z']
  manager.reconcile(activeIds2)
  await new Promise((resolve) => setTimeout(resolve, 0))

  // Only sess-m was acquired! sess-a and sess-z were NOT torn down or re-acquired!
  assert.deepEqual(acquiredLeases, ['sess-a', 'sess-z', 'sess-m'])
  assert.deepEqual(releasedLeases, [])

  // 5. Incremental removal: sess-a completes
  const activeIds3 = ['sess-m', 'sess-z']
  manager.reconcile(activeIds3)
  // sess-a is immediately released!
  assert.deepEqual(releasedLeases, ['sess-a'])
  assert.equal(manager.activeLeases.has('sess-m'), true)
  assert.equal(manager.activeLeases.has('sess-z'), true)

  // 6. Readiness race test: session added and removed before controller becomes ready
  let resolveRaceController: ((c: RealtimeDemandController) => void) | undefined
  const raceControllerPromise = new Promise<RealtimeDemandController>((resolve) => {
    resolveRaceController = resolve
  })

  const raceAcquired: string[] = []
  const raceReleased: string[] = []
  const raceManager = new TaskSessionLeaseManager({
    getControllerReady: () => raceControllerPromise,
    ownerKeyPrefix: 'race',
  })

  // Session 'sess-race' is desired
  raceManager.reconcile(['sess-race'])

  // Before controller resolves, 'sess-race' is removed!
  raceManager.reconcile([])

  // Now controller resolves
  resolveRaceController!({
    acquireSessionDemand: (_ownerKey, sid) => {
      raceAcquired.push(sid)
      return { release: () => raceReleased.push(sid) }
    },
  })
  await new Promise((resolve) => setTimeout(resolve, 0))

  // sess-race was NOT acquired because it was no longer in the desired set!
  assert.deepEqual(raceAcquired, [])

  // 7. Unmount race test: manager cleanup called before controller resolves
  let resolveUnmountController: ((c: RealtimeDemandController) => void) | undefined
  const unmountControllerPromise = new Promise<RealtimeDemandController>((resolve) => {
    resolveUnmountController = resolve
  })
  const unmountAcquired: string[] = []
  const unmountManager = new TaskSessionLeaseManager({
    getControllerReady: () => unmountControllerPromise,
  })

  unmountManager.reconcile(['sess-unmount'])
  // Component unmounts
  unmountManager.cleanup()

  resolveUnmountController!({
    acquireSessionDemand: (_ownerKey, sid) => {
      unmountAcquired.push(sid)
      return { release: () => {} }
    },
  })
  await new Promise((resolve) => setTimeout(resolve, 0))
  assert.deepEqual(unmountAcquired, [])

  // 8. Full unmount cleanup releases all remaining leases
  manager.cleanup()
  // Cleanup follows acquisition order; every lease must be released exactly once.
  assert.deepEqual([...releasedLeases].sort(), ['sess-a', 'sess-m', 'sess-z'])
  assert.equal(manager.activeLeases.size, 0)
  assert.equal(manager.hydratedSessions.size, 0)
})

// =============================================================================
// Requirement 6: Project Switching and Stale Response Immunity
// Threat: Slow in-flight responses from previous projects arrive after the user
//         has switched projects, overwriting newer state with obsolete data.
// Authority: reduceDesktopProjectsState keying and generation checks
// =============================================================================
test('Requirement 6: project switching isolates cache; late responses from previous projects do not corrupt current', async () => {
  // Written Purpose:
  // - Requirement: Each project is isolated by ID. A delayed response for Project A must not
  //   clobber or affect Project B after the user switches projects.
  // - Threat/regression: Race condition where old project's tasks overwrite new project's view.
  // - Boundary: reduceDesktopProjectsState per-project isolation and request ID validation.
  let state: DesktopProjectsState = {}
  let projAResolve: (res: { tasks: any[] }) => void
  let projBResolve: (res: { tasks: any[] }) => void

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: (projectId: string) => {
      if (projectId === 'proj-A') {
        return new Promise((resolve) => {
          projAResolve = resolve
        })
      }
      return new Promise((resolve) => {
        projBResolve = resolve
      })
    },
    fetchMedia: async () => ({ media: [] }),
  })

  // 1. User opens Project A
  const leaseA = runtime.acquire('proj-A')
  assert.equal(state['proj-A'].loading, true)

  // 2. User immediately switches to Project B before Project A completes
  const leaseB = runtime.acquire('proj-B')
  assert.equal(state['proj-B'].loading, true)

  // 3. Project B finishes first
  projBResolve!({
    tasks: [{ id: 'task-b1', title: 'Task B1', agent: 'coder', status: 'running' }],
  })
  await leaseB.ready
  assert.equal(state['proj-B'].loading, false)
  assert.equal(state['proj-B'].tasks[0].id, 'task-b1')

  // 4. Project A finishes late
  projAResolve!({
    tasks: [{ id: 'task-a1', title: 'Task A1', agent: 'coder', status: 'running' }],
  })
  await leaseA.ready
  assert.equal(state['proj-A'].loading, false)
  assert.equal(state['proj-A'].tasks[0].id, 'task-a1')

  // Verify Project B was completely unharmed by Project A's late completion
  assert.equal(state['proj-B'].tasks.length, 1)
  assert.equal(state['proj-B'].tasks[0].id, 'task-b1')

  // Releasing Project A evicts Project A, while Project B remains active
  leaseA.release()
  assert.equal(state['proj-A'], undefined)
  assert.equal(state['proj-B']?.tasks[0].id, 'task-b1')

  leaseB.release()
  assert.equal(state['proj-B'], undefined)
})

// =============================================================================
// Requirement 7: Explicit Eviction and Deletion Immunity
// Threat: Deleted projects remain in demand or cache, sending spurious HTTP requests
//         that return 404 and log errors or burn CPU.
// Authority: DesktopProjectsRuntime.evict and reduceDesktopProjectsState
// =============================================================================
test('Requirement 7: evict removes project demand, in-flight tracking, and state without subsequent requests', async () => {
  // Written Purpose:
  // - Requirement: When a project is deleted, calling evict(projectId) must remove its demand,
  //   in-flight tracking, and cache entry. Subsequent realtime frames or invalidations must not
  //   fire requests for the evicted project.
  // - Threat/regression: Deleted project continuously requesting 404s in background.
  // - Boundary: DesktopProjectsRuntime.evict and demand map.
  let state: DesktopProjectsState = {}
  let fetchCount = 0

  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: (action) => {
      state = reduceDesktopProjectsState(state, action)
    },
    fetchTasks: async (_projectId: string) => {
      fetchCount++
      return { tasks: [] }
    },
    fetchMedia: async (_projectId: string) => {
      return { media: [] }
    },
  })

  const projectId = 'proj-to-delete'
  const lease = runtime.acquire(projectId)
  await lease.ready
  assert.equal(fetchCount, 1)
  assert.ok(state[projectId])

  // Explicit project deletion/eviction
  runtime.evict(projectId)
  assert.equal(state[projectId], undefined)

  // Realtime frames arrive after deletion
  runtime.acceptFrame({ kind: 'project.updated', project_id: projectId })
  runtime.acceptFrame({ kind: 'project.updated' }) // global invalidation
  runtime.acceptFrame({ kind: 'cursor.error' })

  // Since projectId was evicted from demand, zero new fetches should fire!
  assert.equal(fetchCount, 1)
})

// =============================================================================
// Requirement 8: State Referential Stability & Synthetic Frame Safety
// Threat: Reducer unconditionally clones state even when no matching project was
//         invalidated, triggering cascading re-renders across the whole desktop UI.
// Authority: reduceDesktopProjectsState referential equality check
// =============================================================================
test('Requirement 8: reduceDesktopProjectsState preserves reference identity when nothing changed', () => {
  // Written Purpose:
  // - Requirement: reduceDesktopProjectsState must return the existing state reference when
  //   invalidation or eviction does not affect any project in the state.
  // - Threat/regression: Root cache re-allocating on every unrelated invalidation.
  // - Boundary: reduceDesktopProjectsState referential stability.
  const initial: DesktopProjectsState = {
    'proj-1': {
      projectId: 'proj-1',
      tasks: [],
      media: [],
      loading: false,
      stale: false,
      generation: 1,
    },
  }

  // 1. Invalidation for non-existent project returns SAME state reference!
  const afterUnrelatedInvalidate = reduceDesktopProjectsState(initial, {
    type: 'projects.invalidate',
    projectId: 'proj-nonexistent',
  })
  assert.equal(afterUnrelatedInvalidate, initial)

  // 2. Eviction of non-existent project returns SAME state reference!
  const afterUnrelatedEvict = reduceDesktopProjectsState(initial, {
    type: 'projects.evict',
    projectId: 'proj-nonexistent',
  })
  assert.equal(afterUnrelatedEvict, initial)

  // 3. Invalidation of empty state returns SAME state reference!
  const emptyState: DesktopProjectsState = {}
  const afterEmptyInvalidate = reduceDesktopProjectsState(emptyState, {
    type: 'projects.invalidate',
    projectId: 'proj-1',
  })
  assert.equal(afterEmptyInvalidate, emptyState)

  // 4. Invalidation of matching project produces NEW state with bumped generation
  const afterMatchingInvalidate = reduceDesktopProjectsState(initial, {
    type: 'projects.invalidate',
    projectId: 'proj-1',
  })
  assert.notEqual(afterMatchingInvalidate, initial)
  assert.equal(afterMatchingInvalidate['proj-1'].generation, 2)
  assert.equal(afterMatchingInvalidate['proj-1'].stale, true)
})

// Purpose: external promotion receipts must reach active task state through
// project.updated/reconnect, without a local click or refresh. Runtime + reducer
// is the narrowest authority for event-driven reads and stale-response rejection.
test('external promotion moves canonical tasks to Done and reconnect cannot regress it', async () => {
  const { taskIntegrationPhase } = await import('../orchestrate/task-integration-operation')
  let state: DesktopProjectsState = {}
  let record: any = { id: 'task', title: 'Task', session_id: 'source', status: 'needs_review', revision: 1 }
  let reads = 0
  let taskResponse: ((response: { task: any }) => void) | undefined
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {},
    fetchTasks: async () => { reads++; return { tasks: [{ ...record }] } },
    fetchMedia: async () => ({ media: [] }),
    fetchTask: () => new Promise(resolve => { taskResponse = resolve }),
  })
  const lease = runtime.acquire('project')
  await lease.ready
  record = { ...record, revision: 2, integration: { state: 'in_progress' } }
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await runtime.refresh('project')
  assert.equal(taskIntegrationPhase(state.project.tasks[0]), 'pending')
  assert.equal(state.project.tasks.filter(task => task.status === 'completed').length, 0)
  record = { ...record, revision: 3, status: 'completed', is_integrated: true, integration: { state: 'integrated' } }
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await runtime.refresh('project')
  await runtime.refresh('project')
  assert.equal(taskIntegrationPhase(state.project.tasks[0]), 'success')
  assert.equal(state.project.tasks.filter(task => task.status === 'completed').length, 1)
  taskResponse?.({ task: { ...record, revision: 1, status: 'needs_review', is_integrated: false } })
  await Promise.resolve()
  assert.equal(state.project.tasks[0].status, 'completed', 'old per-task read cannot undo completion')
  // A reordered server/cache response is also guarded by durable revision.
  record = { ...record, revision: 2, status: 'needs_review', is_integrated: false, integration: { state: 'in_progress' } }
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await runtime.refresh('project')
  assert.equal(state.project.tasks[0].status, 'completed')
  record = { ...record, revision: 4, status: 'completed', is_integrated: true, integration: { state: 'integrated' } }
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  await runtime.refresh('project')
  assert.equal(state.project.tasks[0].revision, 4)
  const settledReads = reads
  await Promise.resolve()
  assert.equal(reads, settledReads, 'no recurring polling')
  lease.release()
})
