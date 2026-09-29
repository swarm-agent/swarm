import { useEffect, useMemo } from 'react'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import { mutateWorker, readWorkers, type WorkerMutation, type WorkerMutationResult, type WorkerRead, type WorkerReadResult } from '../state/desktop-workers-api'
import { workerPageKey, workerReadRecords, type WorkerCacheAction, type WorkerPages } from '../state/desktop-workers-state'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'

export interface DesktopWorkersDeps {
  read: (input: WorkerRead) => Promise<WorkerReadResult>
  mutate: (input: WorkerMutation) => Promise<WorkerMutationResult>
  pages: () => WorkerPages
  dispatch: (action: WorkerCacheAction) => void
  account: () => string | undefined
}
function errorMessage(error: unknown): string { return error instanceof Error ? error.message : 'Worker operation failed' }
function mutationWorkerId(input: WorkerMutation): string | undefined { return 'workerId' in input ? input.workerId : undefined }

/** Only the canonical Desktop cache retains worker data; this runtime owns requests and demand. */
export class DesktopWorkersRuntime {
  private demand = new Map<string, { input: WorkerRead; count: number }>()
  private flights = new Map<string, Promise<void>>()
  private deps: DesktopWorkersDeps
  constructor(deps: DesktopWorkersDeps = {
    read: readWorkers, mutate: mutateWorker,
    pages: () => getDesktopV3CacheSnapshot().workerPages,
    dispatch: dispatchDesktopV3Cache,
    account: () => getDesktopSessionIdentitySnapshot()?.accountScopeId,
  }) { this.deps = deps }

  acquire(input: WorkerRead): { ready: Promise<void>; release: () => void } {
    this.assertAccount(input.accountScopeId)
    const key = workerPageKey(input)
    const old = this.demand.get(key)
    if (!old && this.demand.size >= 48) throw new Error('Too many worker pages open')
    this.demand.set(key, { input, count: (old?.count ?? 0) + 1 })
    let released = false
    return { ready: this.refresh(input), release: () => {
      if (released) return
      released = true
      const entry = this.demand.get(key)
      if (entry && --entry.count === 0) {
        this.demand.delete(key)
        this.flights.delete(key)
        this.deps.dispatch({ type: 'workers.evict', key })
      }
    } }
  }
  private assertAccount(accountScopeId: string): void {
    if (!accountScopeId || this.deps.account() !== accountScopeId) throw new Error('Worker account scope changed; reopen the hub')
  }
  private verify(input: WorkerRead, data: WorkerReadResult): void {
    const { workers, runs } = workerReadRecords(data)
    if (workers.some(w => w.account_scope_id !== input.accountScopeId || (input.kind !== 'list' && w.id !== input.workerId))
      || runs.some(run => run.account_scope_id !== input.accountScopeId || (input.kind !== 'list' && run.worker_id !== input.workerId))
      || (input.kind === 'list' && 'workers' in data && data.workers.some(w => input.lifecycleState && w.lifecycle_state !== input.lifecycleState))) {
      throw new Error('Worker response scope mismatch')
    }
    if ((input.kind === 'list' && !('workers' in data)) || (input.kind === 'detail' && !('worker' in data))
      || (input.kind === 'runs' && !('runs' in data)) || (input.kind === 'history' && !('revisions' in data))
      || (input.kind === 'run' && (!('run' in data) || data.run.id !== input.runId))
      || (input.kind === 'summary' && (!('worker_id' in data) || data.worker_id !== input.workerId || data.runs?.date !== input.date || data.runs?.timezone !== input.timezone))) throw new Error('Worker response kind mismatch')
    if (input.kind === 'history' && 'revisions' in data && data.revisions.some(h => h.account_scope_id !== input.accountScopeId || h.worker_id !== input.workerId || h.worker.id !== input.workerId)) throw new Error('Worker history scope mismatch')
  }
  refresh(input: WorkerRead): Promise<void> {
    const key = workerPageKey(input)
    const old = this.flights.get(key)
    if (old) return old
    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'workers.begin', key, input, requestId })
    const generation = this.deps.pages()[key].generation
    const promise: Promise<void> = Promise.resolve().then(() => {
      this.assertAccount(input.accountScopeId)
      return this.deps.read(input)
    }).then(data => {
      if (this.flights.get(key) !== promise) return
      this.assertAccount(input.accountScopeId)
      this.verify(input, data)
      this.deps.dispatch({ type: 'workers.finish', key, requestId, generation, data })
    }).catch(error => {
      if (this.flights.get(key) === promise) this.deps.dispatch({ type: 'workers.finish', key, requestId, generation, error: errorMessage(error) })
    }).finally(() => {
      if (this.flights.get(key) !== promise) return
      this.flights.delete(key)
      // Invalidation during the request discards the old response and repairs once, on completion.
      if (this.demand.has(key) && this.deps.pages()[key]?.generation !== generation && this.deps.account() === input.accountScopeId) void this.refresh(input)
    })
    this.flights.set(key, promise)
    return promise
  }
  invalidate(workerId?: string, accountScopeId?: string): void {
    this.deps.dispatch({ type: 'workers.invalidate', workerId, accountScopeId })
    for (const { input } of this.demand.values()) {
      if ((!accountScopeId || input.accountScopeId === accountScopeId) && (!workerId || input.kind === 'list' || input.workerId === workerId)
        && this.deps.account() === input.accountScopeId) void this.refresh(input)
    }
  }
  acceptFrame(frame: { kind: string; account_scope_id?: string; worker_id?: string }): void {
    if (frame.kind === 'worker.updated') this.invalidate(frame.worker_id, frame.account_scope_id)
    else if (frame.kind === 'cursor.error' || frame.kind === 'rehydrate.required') this.invalidate()
  }
  async mutate(input: WorkerMutation, accountScopeId: string): Promise<WorkerMutationResult> {
    this.assertAccount(accountScopeId)
    const workerId = mutationWorkerId(input)
    this.deps.dispatch({ type: 'workers.mutationError', workerId, accountScopeId })
    try {
      const result = await this.deps.mutate(input)
      this.assertAccount(accountScopeId)
      if ('worker' in result && (result.worker.account_scope_id !== accountScopeId || (workerId && result.worker.id !== workerId))) throw new Error('Worker mutation response scope mismatch')
      if ('run' in result && (result.run.account_scope_id !== accountScopeId || result.run.worker_id !== workerId)) throw new Error('Worker run response scope mismatch')
      this.invalidate(workerId ?? ('worker' in result ? result.worker.id : undefined), accountScopeId)
      return result
    } catch (error) {
      if (this.deps.account() === accountScopeId) {
        this.deps.dispatch({ type: 'workers.mutationError', workerId, accountScopeId, error: errorMessage(error) })
        this.invalidate(workerId, accountScopeId) // Reconcile possible server commit; never invent successful state.
      }
      throw error
    }
  }
}
export const desktopWorkers = new DesktopWorkersRuntime()
export function useWorkerPage(input: WorkerRead) {
  const key = workerPageKey(input)
  const stable = useMemo(() => input, [key])
  const page = useDesktopV3CacheSelector(state => state.workerPages[key])
  useEffect(() => desktopWorkers.acquire(stable).release, [stable])
  return page
}
