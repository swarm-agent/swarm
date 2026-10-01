// Purpose: Task cards subscribe before chat ever mounts and recover after initial
// hydrate failure without replacing existing demand. Threat: readiness races or a
// failed first hydration permanently lose the first tokens and terminal replay.
// Boundary: TaskSessionLeaseManager is the narrowest owner of task-card demand.
import assert from 'node:assert/strict'
import test from 'node:test'
import { TaskSessionLeaseManager } from './desktop-projects-membership'

test('card demand survives late controller readiness, retries failed hydrate and cleans up', async () => {
  let resolveReady!: (value: { acquireSessionDemand: (owner: string, id: string) => { release: () => void } }) => void
  const ready = new Promise<{ acquireSessionDemand: (owner: string, id: string) => { release: () => void } }>((resolve) => { resolveReady = resolve })
  let attempts = 0
  const acquired: string[] = []
  const released: string[] = []
  const manager = new TaskSessionLeaseManager({
    getControllerReady: () => ready,
    hydrate: async () => { attempts++; if (attempts === 1) throw new Error('session not yet visible') },
  })
  manager.reconcile(['new-session'])
  await Promise.resolve()
  manager.reconcile(['new-session'])
  assert.equal(attempts, 2)
  resolveReady({ acquireSessionDemand: (_owner, id) => { acquired.push(id); return { release: () => { released.push(id) } } } })
  await Promise.resolve()
  await Promise.resolve()
  assert.deepEqual(acquired, ['new-session'])
  manager.reconcile(['new-session'])
  assert.equal(attempts, 2)
  assert.deepEqual(acquired, ['new-session'])
  manager.cleanup()
  assert.deepEqual(released, ['new-session'])
})
