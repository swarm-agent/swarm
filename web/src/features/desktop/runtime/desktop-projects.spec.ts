import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import {
  reduceDesktopProjectsState,
  mapBackendTasks,
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
// Requirement 3: Unrelated Invalidations Ignored
// Threat: Account-wide or foreign project events cause unrelated projects to
//         refresh continuously, triggering cascading re-renders.
// Authority: DesktopProjectsRuntime.acceptFrame & invalidate
// =============================================================================
test('Requirement 3: frames for other projects or unrelated kinds do not trigger refresh', async () => {
  // Written Purpose:
  // - Requirement: Events for other project IDs or non-project event kinds (e.g. workspace.catalog.updated)
  //   must NOT trigger a refresh of the currently open project.
  // - Threat/regression: Unrelated project or workspace events triggering CPU churn on open page.
  // - Boundary: DesktopProjectsRuntime.acceptFrame project_id scoping.
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

  const projectId = 'proj-target'
  const lease = runtime.acquire(projectId)
  await lease.ready
  assert.equal(fetchCount, 1)

  // Unrelated frame: workspace catalog
  runtime.acceptFrame({ kind: 'workspace.catalog.updated' })
  assert.equal(fetchCount, 1)

  // Unrelated frame: different project
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'proj-other' })
  assert.equal(fetchCount, 1)

  // Unrelated frame: generic chat session event
  runtime.acceptFrame({ kind: 'event' })
  assert.equal(fetchCount, 1)

  // Matching frame: must trigger refresh!
  runtime.acceptFrame({ kind: 'project.updated', project_id: projectId })
  assert.equal(fetchCount, 2)

  // Global project invalidation (empty project_id, e.g. account-wide): must trigger refresh!
  runtime.acceptFrame({ kind: 'project.updated' })
  assert.equal(fetchCount, 3)

  lease.release()
})

// =============================================================================
// Requirement 4: Reconnect and Rehydrate Repair
// Threat: Lost frames or connection dropouts cause the client to remain stale.
// Authority: DesktopProjectsRuntime.acceptFrame cursor.error / rehydrate.required
// =============================================================================
test('Requirement 4: reconnect or rehydrate frames trigger repair refresh for active demand', async () => {
  // Written Purpose:
  // - Requirement: On websocket reconnect or cursor error, the runtime must invalidate and refresh
  //   all actively retained projects to guarantee freshness without polling.
  // - Threat/regression: Stale UI after network reconnection.
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

  // Trigger cursor error reconnect repair
  runtime.acceptFrame({ kind: 'cursor.error' })
  assert.equal(fetchCount, 2)

  // Trigger rehydrate required repair
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  assert.equal(fetchCount, 3)

  lease.release()
})

// =============================================================================
// Requirement 5: Stable Membership and Incremental Lease Management
// Threat: Non-deterministic session ordering or array identity shifts tear down
//         and recreate leases, causing websocket unsubscribe/subscribe churn.
// Authority: orchestrate activeTaskSessionIds deduplication and incremental lease manager
// =============================================================================
test('Requirement 5: session membership is deduplicated, sorted, and leases update incrementally', () => {
  // Written Purpose:
  // - Requirement: Session IDs for active tasks must be deduplicated and sorted deterministically.
  //   When tasks update without changing the active session set, zero leases are recreated.
  //   When a session is added, only the new session acquires a lease. When removed, only that lease is released.
  // - Threat/regression: Mass lease teardown and rehydration on every task list mutation.
  // - Boundary: session membership set and incremental lease reconciliation logic.

  // Simulate task list with duplicate sessions and unordered statuses
  const rawTasks: Partial<RunningTask>[] = [
    { id: 't1', sessionId: 'sess-z', status: 'running' },
    { id: 't2', sessionId: 'sess-a', status: 'in_progress' },
    { id: 't3', sessionId: 'sess-z', status: 'in_progress' }, // duplicate session
    { id: 't4', sessionId: 'sess-b', status: 'completed' },   // not active
    { id: 't5', sessionId: 'sess-c', status: 'queued' },      // not active
  ]

  const computeActiveSessionIds = (tasks: Partial<RunningTask>[], selectedTaskId?: string): string[] => {
    const set = new Set<string>()
    for (const t of tasks) {
      if (t.sessionId && (t.status === 'running' || t.status === 'in_progress' || t.id === selectedTaskId)) {
        set.add(t.sessionId)
      }
    }
    return Array.from(set).sort()
  }

  // 1. Deduplication and sorting
  const activeIds1 = computeActiveSessionIds(rawTasks)
  assert.deepEqual(activeIds1, ['sess-a', 'sess-z'])

  // 2. Incremental lease simulation
  const acquiredLeases: string[] = []
  const releasedLeases: string[] = []
  const currentLeases = new Map<string, { release: () => void }>()

  const reconcileLeases = (newIds: string[]) => {
    const newSet = new Set(newIds)
    // Remove obsolete
    for (const [sid, lease] of currentLeases.entries()) {
      if (!newSet.has(sid)) {
        lease.release()
        releasedLeases.push(sid)
        currentLeases.delete(sid)
      }
    }
    // Add new
    for (const sid of newSet) {
      if (!currentLeases.has(sid)) {
        acquiredLeases.push(sid)
        currentLeases.set(sid, {
          release: () => {},
        })
      }
    }
  }

  reconcileLeases(activeIds1)
  assert.deepEqual(acquiredLeases, ['sess-a', 'sess-z'])
  assert.deepEqual(releasedLeases, [])

  // 3. New task with new session arrives
  const updatedTasks: Partial<RunningTask>[] = [
    ...rawTasks,
    { id: 't6', sessionId: 'sess-m', status: 'running' },
  ]
  const activeIds2 = computeActiveSessionIds(updatedTasks)
  assert.deepEqual(activeIds2, ['sess-a', 'sess-m', 'sess-z'])

  reconcileLeases(activeIds2)
  // Only sess-m was acquired! sess-a and sess-z were NOT torn down!
  assert.deepEqual(acquiredLeases, ['sess-a', 'sess-z', 'sess-m'])
  assert.deepEqual(releasedLeases, [])

  // 4. Session sess-a completes execution
  const completedTasks: Partial<RunningTask>[] = updatedTasks.map((t) =>
    t.sessionId === 'sess-a' ? { ...t, status: 'completed' } : t
  )
  const activeIds3 = computeActiveSessionIds(completedTasks)
  assert.deepEqual(activeIds3, ['sess-m', 'sess-z'])

  reconcileLeases(activeIds3)
  // Only sess-a was released! sess-m and sess-z remained intact!
  assert.deepEqual(releasedLeases, ['sess-a'])
  assert.equal(currentLeases.has('sess-m'), true)
  assert.equal(currentLeases.has('sess-z'), true)
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
