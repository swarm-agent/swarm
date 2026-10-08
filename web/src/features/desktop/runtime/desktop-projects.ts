import { useEffect } from 'react'
import { requestStartupJson } from '../../../app/api'
import { subscribeGit, type SubscribeGit, type GitWatchSelector } from '../git/subscriptions'
import { fetchProjectTaskCollection } from './project-task-collection'
import type { RunningTask, ProjectTaskMediaRef } from '../orchestrate/orchestrate-types'
import {
  mapBackendTask,
  taskGitIdentity,
  mapBackendTasks,
  type DesktopProjectState,
  type DesktopProjectsAction,
  type DesktopProjectsState,
} from '../state/desktop-projects-state'
import {
  dispatchDesktopV3Cache,
  getDesktopV3CacheSnapshot,
  useDesktopV3CacheSelector,
  subscribeDesktopV3Cache,
  type DesktopV3CacheMutation,
} from '../state/desktop-v3-cache-store'

import { repositoryEventInvalidates, repositoryOwnerIds } from '../state/session-repositories'

import { extractTaskSessionIds } from './desktop-projects-membership'
export * from './desktop-projects-membership'

let retainProjectsRealtime: (() => { release(): void }) | undefined
export function setDesktopProjectsRealtimeRetainer(retain: () => { release(): void }): void { retainProjectsRealtime = retain }

export interface DesktopProjectsRuntimeDeps {
  fetchTasks: (projectId: string, signal?: AbortSignal) => Promise<{ tasks?: any[] }>
  fetchTask: (projectId: string, taskId: string) => Promise<{ task?: any }>
  fetchMedia: (projectId: string, signal?: AbortSignal) => Promise<{ media?: ProjectTaskMediaRef[] }>
  getState: () => DesktopProjectsState
  dispatch: (action: DesktopProjectsAction) => void
  subscribeGit?: SubscribeGit
  subscribe?: (listener: (mutation?: DesktopV3CacheMutation) => void) => () => void
}

// Plan definitions are task-card authority too, not just Git invalidations.
function taskPlanEventInvalidates(action: DesktopV3CacheMutation['action'], owners: ReadonlySet<string>): boolean {
  const relevant = (event: { sessionId: string; eventType: string }) =>
    owners.has(event.sessionId) && event.eventType.startsWith('session.plan.')
  switch (action.type) {
    case 'realtime.applyEvent': return relevant(action.event)
    case 'syncStream.applyBatch':
    case 'liveRun.mergeRepairEvents': return action.events.some(relevant)
    default: return false
  }
}

export class DesktopProjectsRuntime {
  private readonly projectUpdateListeners = new Set<(projectId?: string) => void>()
  onProjectUpdate(listener: (projectId?: string) => void): () => void {
    this.projectUpdateListeners.add(listener)
    return () => { this.projectUpdateListeners.delete(listener) }
  }
  private readonly demand = new Map<string, { projectId: string; count: number }>()
  private readonly inFlight = new Map<string, Promise<void>>()
  private readonly collectionControllers = new Map<string, AbortController>()
  private readonly deps: DesktopProjectsRuntimeDeps
  private readonly taskQueue = new Map<string, { projectId: string; task: RunningTask; epoch: number; authoritative: boolean }>()
  private readonly taskReads = new Map<string, { identity: string; demand: unknown; epoch: number }>()
  private readonly taskVersions = new Map<string, number>()
  private taskEpoch = 0
  private unsubscribeCache: (() => void) | null = null
  private readonly gitWatches = new Map<string, { signature: string; release: () => void }>()
  private readonly gitChanges = new Map<string, { projectId: string; task: RunningTask }>()
  private gitFlushScheduled = false
  private readonly lostGitWatches = new Map<string, string>()
  private gitAdmissionOrder: string[] = []

  private taskWatchLost(task: RunningTask): boolean {
    return !!this.taskWatchError(task)
  }

  private taskWatchError(task: RunningTask): string | undefined {
    for (const [key, error] of this.lostGitWatches) {
      const repository = JSON.parse(key) as GitWatchSelector
      const matches = repository.session_id
        ? task.workspacePath === repository.workspace_path && task.sessionId === repository.session_id
        : task.sourceWorkspacePath === repository.workspace_path && task.baseBranch === repository.branch
      if (matches) return error
    }
  }

  // Only an explicit repair signal retries failed setup. No timer and no retry
  // induced by healthy filesystem notices or ordinary task assessment responses.
  private retryLostGitWatches(): void {
    if (!this.lostGitWatches.size) return
    for (const watch of this.gitWatches.values()) watch.release()
    this.gitWatches.clear()
    this.syncGitWatches()
  }

  private syncGitWatches(): void {
    // Deduplicate source and target selectors in one board-owned push stream.
    // Multiple long-lived HTTP/1 streams can starve task GETs at the browser's
    // connection limit. Membership changes reconnect once; Git notices remain
    // strictly repository-scoped. The server enforces a 256-selector bound.
    const groups = new Map<string, Map<string, GitWatchSelector>>()
    for (const { projectId } of this.demand.values()) {
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) {
        if (!task.sessionId || !task.workspacePath || !task.sourceWorkspacePath || !task.worktreeBranch) continue
        const group = groups.get(task.sourceWorkspacePath) ?? new Map<string, GitWatchSelector>()
        for (const selector of [
          { workspace_path: task.workspacePath, session_id: task.sessionId, branch: task.worktreeBranch },
          { workspace_path: task.sourceWorkspacePath, branch: task.baseBranch ?? '' },
        ]) group.set(JSON.stringify(selector), selector)
        groups.set(task.sourceWorkspacePath, group)
      }
    }
    const desired = new Map<string, GitWatchSelector[]>()
    const all = new Map([...groups.values()].flatMap(group => [...group.entries()]))
    // FIFO admission survives board order changes: new rows cannot evict healthy
    // slots. Vacancies go to the oldest waiting selectors; targets precede sources
    // on first admission so a shared target does not strand admitted task lanes.
    const retained = this.gitAdmissionOrder.filter(key => all.has(key))
    const known = new Set(retained)
    const added = [...all.entries()].filter(([key]) => !known.has(key))
      .sort(([a, av], [b, bv]) => Number(!!av.session_id) - Number(!!bv.session_id) || a.localeCompare(b))
    this.gitAdmissionOrder = [...retained, ...added.map(([key]) => key)]
    const ordered = this.gitAdmissionOrder.map(key => all.get(key)!)
    if (ordered.length) desired.set('board', ordered)
    const selectors = new Set([...desired.values()].flat().map(selector => JSON.stringify(selector)))
    for (const key of this.lostGitWatches.keys()) if (!selectors.has(key)) this.lostGitWatches.delete(key)
    for (const [key, watch] of this.gitWatches) {
      if (JSON.stringify(desired.get(key)) !== watch.signature) { watch.release(); this.gitWatches.delete(key) }
    }
    for (const [key, repositories] of desired) {
      if (this.gitWatches.has(key)) continue
      const watch = { signature: JSON.stringify(repositories), release: () => {} }
      this.gitWatches.set(key, watch)
      watch.release = this.deps.subscribeGit!(repositories, notice => {
        if (this.gitWatches.get(key) !== watch) return
        const repository = repositories[notice.index]
        if (!repository) return
        const selectorKey = JSON.stringify(repository)
        if (notice.kind === 'lost') this.lostGitWatches.set(selectorKey, notice.error || 'Git filesystem watch unavailable; reconnecting')
        else if (notice.kind === 'ready') this.lostGitWatches.delete(selectorKey)
        for (const { projectId } of this.demand.values()) {
          for (const task of this.deps.getState()[projectId]?.tasks ?? []) {
            const matches = repository.session_id
              ? task.workspacePath === repository.workspace_path && task.sessionId === repository.session_id
              : task.sourceWorkspacePath === repository.workspace_path && task.baseBranch === repository.branch
            if (!matches) continue
            const taskKey = JSON.stringify([projectId, task.id])
            // Fence immediately, before a stale in-flight HTTP result can apply.
            this.taskVersions.set(taskKey, (this.taskVersions.get(taskKey) ?? 0) + 1)
            this.deps.dispatch({ type: 'projects.invalidateGit', projectId, taskId: task.id,
              error: this.taskWatchError(task) })
            if (this.taskWatchLost(task)) {
              this.taskQueue.delete(taskKey)
              this.gitChanges.delete(taskKey)
            } else this.gitChanges.set(taskKey, { projectId, task })
          }
        }
        if (!this.gitFlushScheduled && this.gitChanges.size) {
          this.gitFlushScheduled = true
          queueMicrotask(() => {
            this.gitFlushScheduled = false
            const changes = [...this.gitChanges.values()]
            this.gitChanges.clear()
            for (const { projectId, task } of changes) this.queueTask(projectId, task)
          })
        }
      })
    }
    // Collection hydration can replace task fields, but cannot heal an unwatched
    // lane. Preserve its exact failure until that selector emits ready.
    for (const { projectId } of this.demand.values()) {
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) {
        const error = this.taskWatchError(task)
        if (error) this.deps.dispatch({ type: 'projects.invalidateGit', projectId, taskId: task.id, error })
      }
    }
  }

  private ensureSubscribed(): void {
    if (this.unsubscribeCache) return
    const subscribe = this.deps.subscribe ?? subscribeDesktopV3Cache
    this.unsubscribeCache = subscribe((mutation) => this.acceptSessionMutation(mutation))
  }

  acceptSessionMutation(mutation?: DesktopV3CacheMutation): void {
    // Card hydration is not a Git invalidation. Initial collection identities
    // receive one bounded inspection; durable events and reconnect repair own
    // subsequent invalidations, never cache enrichment or recurring timers.
    if (mutation?.action.type === 'hydrate.apply') return
    let retryWatches = false
    for (const { projectId } of this.demand.values()) {
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) {
        if (!task.sessionId) continue
        const owners = mutation ? repositoryOwnerIds(task.sessionId, [], mutation.nextState) : new Set([task.sessionId])
        for (const id of extractTaskSessionIds(task)) owners.add(id)
        if (!mutation || repositoryEventInvalidates(mutation.action, owners) || taskPlanEventInvalidates(mutation.action, owners)) {
          if (this.taskWatchLost(task)) retryWatches = true
          this.queueTask(projectId, task, false, true)
        }
      }
    }
    if (retryWatches) this.retryLostGitWatches()
  }

  private queueTask(projectId: string, task: RunningTask, authoritative = false, invalidateGit = false): void {
    if ((!task.sessionId && !authoritative) || !this.demand.has(projectId)) return
    if (!authoritative && this.taskWatchLost(task)) return
    const key = JSON.stringify([projectId, task.id])
    if (invalidateGit) {
      this.taskVersions.set(key, (this.taskVersions.get(key) ?? 0) + 1)
      this.deps.dispatch({ type: 'projects.invalidateGit', projectId, taskId: task.id })
      this.deps.dispatch({ type: 'projects.updateTasks', projectId, tasks: tasks => tasks.map(item => item.id === task.id ? { ...item, environmentsStale: true } : item) })
    }
    authoritative ||= this.taskQueue.get(key)?.authoritative ?? false
    this.taskQueue.set(key, { projectId, task, epoch: this.taskEpoch, authoritative })
    this.drainTasks()
  }

  private drainTasks(): void {
    for (const [key, entry] of this.taskQueue) {
      // Match the assessor's per-repository admission bound even when all cards
      // share one repository; avoid turning initial hydration into capacity errors.
      if (this.taskReads.size >= 2) break
      if (this.taskReads.has(key)) continue
      this.taskQueue.delete(key)
      if (!this.demand.has(entry.projectId)) continue
      const currentTask = this.deps.getState()[entry.projectId]?.tasks.find(task => task.id === entry.task.id)
      if (!currentTask || (!entry.authoritative && currentTask.sessionId !== entry.task.sessionId)) continue
      entry.task = currentTask
      this.taskReads.set(key, { identity: taskGitIdentity(currentTask), demand: this.demand.get(entry.projectId), epoch: this.taskEpoch })
      const demand = this.demand.get(entry.projectId)
      const version = this.taskVersions.get(key)
      const identity = taskGitIdentity(entry.task)
      const apply = (update: (task: RunningTask) => RunningTask | undefined, inspected = false) => {
        if (entry.epoch !== this.taskEpoch || this.demand.get(entry.projectId) !== demand || this.taskVersions.get(key) !== version) return
        const current = this.deps.getState()[entry.projectId]?.tasks.find(task => task.id === entry.task.id)
        if (!current || taskGitIdentity(current) !== identity) return
        this.deps.dispatch({ type: 'projects.updateTasks', projectId: entry.projectId,
          inspectedTaskId: inspected ? entry.task.id : undefined,
          tasks: tasks => tasks.flatMap(task => {
            if (task.id !== entry.task.id || taskGitIdentity(task) !== identity) return [task]
            const updated = update(task)
            return updated ? [updated] : []
          }),
        })
        this.syncGitWatches()
        const updated = this.deps.getState()[entry.projectId]?.tasks.find(task => task.id === entry.task.id)
        if (updated && this.taskWatchLost(updated)) this.deps.dispatch({ type: 'projects.invalidateGit', projectId: entry.projectId, taskId: updated.id,
          error: this.taskWatchError(updated) })
      }
      void this.deps.fetchTask(entry.projectId, entry.task.id).then(response => {
        if (!response.task || response.task.id !== entry.task.id ||
          (!entry.authoritative && response.task.session_id !== entry.task.sessionId) ||
          (response.task.revision ?? 0) < (entry.task.revision ?? 0)) {
          apply(task => ({ ...task, gitStatus: 'unknown', isIntegrated: false, unintegratedCommits: 0,
            detailError: 'Task status response did not match the current execution',
            syncWarning: 'Task status response did not match the current execution' }), true)
          return
        }
        if (entry.authoritative && response.task.archived) {
          apply(() => undefined)
          return
        }
        apply(() => mapBackendTask(response.task), true)
      }).catch(error => {
        apply(task => ({ ...task, gitStatus: 'unknown', isIntegrated: false, unintegratedCommits: 0,
          detailError: error instanceof Error ? error.message : 'Task detail refresh failed',
          syncWarning: error instanceof Error ? error.message : 'Task status refresh failed' }), true)
      }).finally(() => {
        this.taskReads.delete(key)
        this.drainTasks()
      })
    }
  }

  constructor(deps?: Partial<DesktopProjectsRuntimeDeps>) {
    this.deps = {
      fetchTasks: deps?.fetchTasks ?? fetchProjectTaskCollection,
      fetchTask: deps?.fetchTask ?? ((projectId, taskId) =>
        requestStartupJson<{ task?: any }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`)),
      fetchMedia:
        deps?.fetchMedia ??
        ((projectId: string, signal?: AbortSignal) =>
          requestStartupJson<{ media?: ProjectTaskMediaRef[] }>(`/v3/projects/${encodeURIComponent(projectId)}/media`, { signal })),
      getState: deps?.getState ?? (() => getDesktopV3CacheSnapshot().projectsState ?? {}),
      dispatch: deps?.dispatch ?? ((action: DesktopProjectsAction) => dispatchDesktopV3Cache(action as any)),
      subscribe: deps?.subscribe ?? subscribeDesktopV3Cache,
      subscribeGit: deps?.subscribeGit ?? subscribeGit,
    }
  }

  acquire(projectId: string): { ready: Promise<void>; release: () => void } {
    if (!projectId) return { ready: Promise.resolve(), release: () => {} }
    this.ensureSubscribed()
    const current = this.demand.get(projectId)
    if (current) current.count++
    else this.demand.set(projectId, { projectId, count: 1 })
    let released = false
    return {
      ready: current ? (this.inFlight.get(projectId) ?? Promise.resolve()) : this.refresh(projectId, false),
      release: () => {
        if (released) return
        released = true
        const entry = this.demand.get(projectId)
        if (entry && --entry.count === 0) {
          this.demand.delete(projectId)
          for (const [key, change] of this.gitChanges) if (change.projectId === projectId) this.gitChanges.delete(key)
          this.syncGitWatches()
          for (const [key, entry] of this.taskQueue) if (entry.projectId === projectId) this.taskQueue.delete(key)
          for (const key of this.taskVersions.keys()) if (JSON.parse(key)[0] === projectId) this.taskVersions.delete(key)
          this.inFlight.delete(projectId)
          this.collectionControllers.get(projectId)?.abort()
          this.collectionControllers.delete(projectId)
          this.deps.dispatch({ type: 'projects.evict', projectId })
          if (this.demand.size === 0) {
            this.unsubscribeCache?.()
            this.unsubscribeCache = null
          }
        }
      },
    }
  }

  evict(projectId: string): void {
    if (!projectId) return
    this.demand.delete(projectId)
    for (const [key, change] of this.gitChanges) if (change.projectId === projectId) this.gitChanges.delete(key)
    this.syncGitWatches()
    for (const [key, entry] of this.taskQueue) if (entry.projectId === projectId) this.taskQueue.delete(key)
    for (const key of this.taskVersions.keys()) if (JSON.parse(key)[0] === projectId) this.taskVersions.delete(key)
    this.inFlight.delete(projectId)
    this.collectionControllers.get(projectId)?.abort()
    this.collectionControllers.delete(projectId)
    this.deps.dispatch({ type: 'projects.evict', projectId })
    if (this.demand.size === 0) {
      this.unsubscribeCache?.()
      this.unsubscribeCache = null
    }
  }

  reset(): void {
    this.taskEpoch++
    for (const watch of this.gitWatches.values()) watch.release()
    this.gitWatches.clear()
    this.gitChanges.clear()
    this.lostGitWatches.clear()
    this.gitAdmissionOrder = []
    this.taskQueue.clear()
    this.taskVersions.clear()
    this.demand.clear()
    this.inFlight.clear()
    for (const controller of this.collectionControllers.values()) controller.abort()
    this.collectionControllers.clear()
    this.unsubscribeCache?.()
    this.unsubscribeCache = null
  }

  refresh(projectId: string, inspectGit = true): Promise<void> {
    if (!projectId) return Promise.resolve()
    const pending = this.inFlight.get(projectId)
    if (pending) return pending
    if (inspectGit) {
      this.retryLostGitWatches()
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) this.queueTask(projectId, task, false, true)
    }

    this.collectionControllers.get(projectId)?.abort()
    const controller = new AbortController()
    this.collectionControllers.set(projectId, controller)
    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'projects.beginLoad', projectId, requestId })
    const state = this.deps.getState()[projectId]
    const generation = state?.generation ?? 0

    // Publish tasks independently: media latency/failure must not hide the board.
    const tasksPromise = this.deps.fetchTasks(projectId, controller.signal)
      .then((tasksRes) => {
        if (this.inFlight.get(projectId) !== promise) return
        const backendTasks = mapBackendTasks(tasksRes?.tasks || [])
        this.deps.dispatch({
          type: 'projects.loadSuccess',
          projectId,
          requestId,
          generation,
          tasks: backendTasks,
        })
        this.syncGitWatches()
        if (this.deps.getState()[projectId]?.generation === generation) {
          const project = this.deps.getState()[projectId]
          for (const task of project?.tasks ?? []) {
            if (task.status === 'pending_approval') continue
            if (project?.gitObservations?.[task.id] !== taskGitIdentity(task)) {
              const key = JSON.stringify([projectId, task.id])
              // An unchanged card already being inspected needs no second read.
              const read = this.taskReads.get(key)
              const queued = this.taskQueue.get(key)
              // Hydrate each new execution identity once, including initial entry.
              // Collection refreshes retain observations; no timer retries reads.
              if (queued ? taskGitIdentity(queued.task) !== taskGitIdentity(task) :
                (!read || read.identity !== taskGitIdentity(task) || read.demand !== this.demand.get(projectId) || read.epoch !== this.taskEpoch)) {
                this.queueTask(projectId, task)
              }
            }
          }
        }
      })
      .catch((error: unknown) => {
        if (this.inFlight.get(projectId) !== promise) return
        this.deps.dispatch({
          type: 'projects.loadError',
          projectId,
          requestId,
          generation,
          error: error instanceof Error ? error.message : 'Failed to load project tasks',
        })
      })
    const demand = this.demand.get(projectId)
    void this.deps.fetchMedia(projectId, controller.signal).then(response => {
      if (this.demand.get(projectId) !== demand) return
      this.deps.dispatch({ type: 'projects.mediaResult', projectId, requestId, generation, media: response.media ?? [] })
    }).catch(error => {
      if (this.demand.get(projectId) !== demand) return
      this.deps.dispatch({ type: 'projects.mediaResult', projectId, requestId, generation,
        error: error instanceof Error ? error.message : 'Failed to load project media' })
    })
    const promise: Promise<void> = tasksPromise.then(() => undefined)
      .finally(() => {
        if (this.inFlight.get(projectId) !== promise) return
        this.inFlight.delete(projectId)
        const current = this.deps.getState()[projectId]
        // Coalesce trailing refresh if generation changed during in-flight fetch
        if (this.demand.has(projectId) && (!current || current.generation !== generation)) {
          void this.refresh(projectId, false)
        }
      })

    this.inFlight.set(projectId, promise)
    return promise
  }

  inspectTask(projectId: string, taskId: string): void {
    const task = this.deps.getState()[projectId]?.tasks.find(item => item.id === taskId)
    if (!task || task.detailLoaded) return
    const key = JSON.stringify([projectId, taskId])
    const read = this.taskReads.get(key)
    if (this.taskQueue.has(key) || (read && read.identity === taskGitIdentity(task) &&
      read.demand === this.demand.get(projectId) && read.epoch === this.taskEpoch)) return
    this.queueTask(projectId, task, true, false)
  }

  invalidate(projectId?: string): void {
    for (const { projectId: id } of this.demand.values()) {
      if (!projectId || id === projectId) {
        for (const task of this.deps.getState()[id]?.tasks ?? []) {
          const key = JSON.stringify([id, task.id])
          this.taskVersions.set(key, (this.taskVersions.get(key) ?? 0) + 1)
        }
        this.deps.dispatch({ type: 'projects.updateTasks', projectId: id, tasks: tasks => tasks.map(task => ({ ...task, environmentsStale: true })) })
      }
    }
    this.deps.dispatch({ type: 'projects.invalidate', projectId })
    for (const { projectId: id } of this.demand.values()) {
      if (!projectId || id === projectId) {
        void this.refresh(id, false)
      }
    }
  }

  acceptFrame(frame: { kind: string; project_id?: string; projectId?: string; payload?: unknown; event_type?: string; event?: { type?: string; event_type?: string; payload?: unknown } }): void {
    if (frame.kind === 'environment.updated' || (frame.event?.type ?? frame.event?.event_type ?? frame.event_type) === 'environment.updated') {
      const payload = (frame.event?.payload ?? frame.payload) as { workspace_id?: string; task_environment_workspace_invalidated?: boolean; task_environment_targets?: Array<{ project_id: string; task_id: string }> } | undefined
      if (!payload?.workspace_id) return
      for (const { projectId } of this.demand.values()) {
        const tasks = this.deps.getState()[projectId]?.tasks ?? []
        const relevant = tasks.filter(task =>
          payload.task_environment_targets?.some(target => target.project_id === projectId && target.task_id === task.id) ||
          (payload.task_environment_workspace_invalidated && (task.sourceWorkspaceId === payload.workspace_id || task.environmentAttachments?.some(a => a.source.workspace_id === payload.workspace_id))))
        // Before initial hydration there may be no task/attachment identities to
        // match. A broad invalidation must fence that in-flight snapshot too.
        const project = this.deps.getState()[projectId]
        const catalog = project?.environmentWorkspaceCatalog
        const initialUnknown = this.inFlight.has(projectId) && !project?.lastObservedAt && (!catalog?.length || catalog.some(w => w.workspaceId === payload.workspace_id))
        if (relevant.length || (payload.task_environment_workspace_invalidated && initialUnknown) || payload.task_environment_targets?.some(target => target.project_id === projectId)) this.invalidate(projectId)
      }
      return
    }
    const projectId = frame.project_id || frame.projectId
    if (frame.kind === 'project.updated') {
      const payload = frame.event?.payload
      const change = payload && typeof payload === 'object' ? payload as Record<string, unknown> : undefined
      const taskId = typeof change?.task_id === 'string' ? change.task_id : undefined
      const task = projectId && taskId ? this.deps.getState()[projectId]?.tasks.find(task => task.id === taskId) : undefined
      // Durable task updates already identify their card. Do not reload the board,
      // media or unrelated Git inspections. Collection/membership changes and
      // older/unknown frames still use the canonical snapshot repair below.
      // Exact durable archive receipts fence stale snapshots without a detail or
      // full-board read, regardless of HTTP/event delivery ordering.
      if (projectId && taskId && change?.project_id === projectId && change.action === 'task_updated' &&
        change.archived === true && typeof change.revision === 'number' && Number.isSafeInteger(change.revision) && change.revision > 0) {
        this.archiveReceipt(projectId, { id: taskId, revision: change.revision, archived: true })
        return
      }
      if (projectId && task && change?.action === 'task_updated') {
        if (this.taskWatchLost(task)) this.retryLostGitWatches()
        this.queueTask(projectId, task, true, true)
        return
      }
      if (change?.resource === 'designs') return
      this.invalidate(projectId)
      for (const listener of this.projectUpdateListeners) listener(projectId)
    } else if (
      frame.kind === 'replay.complete' ||
      frame.kind === 'cursor.error' ||
      frame.kind === 'rehydrate.required' ||
      frame.kind === 'auth.credentials.updated'
    ) {
      this.retryLostGitWatches()
      this.invalidate()
      for (const { projectId: id } of this.demand.values()) {
        for (const task of this.deps.getState()[id]?.tasks ?? []) this.queueTask(id, task, false, true)
      }
      for (const listener of this.projectUpdateListeners) listener()
    }
  }

  archiveReceipt(projectId: string, receipt: { id: string; revision: number; archived: boolean }): void {
    if (!receipt.archived || !receipt.id || !Number.isSafeInteger(receipt.revision) || receipt.revision <= 0) throw new Error('Invalid task archive receipt')
    const current = this.deps.getState()[projectId]?.tasks.find(task => task.id === receipt.id)
    if (!current || (current.revision ?? 0) <= receipt.revision) this.taskQueue.delete(JSON.stringify([projectId, receipt.id]))
    this.deps.dispatch({ type: 'projects.updateTasks', projectId, tasks: tasks => tasks, archivedReceipt: receipt })
    this.syncGitWatches()
  }

  unarchiveReceipt(projectId: string, receipt: { id: string; revision: number; archived: false; status: string }): void {
    if (receipt.archived !== false || !receipt.id || !Number.isSafeInteger(receipt.revision) || receipt.revision <= 0) throw new Error('Invalid task unarchive receipt')
    const restored = mapBackendTask(receipt)
    this.deps.dispatch({ type: 'projects.updateTasks', projectId, tasks: tasks => [...tasks.filter(task => task.id !== restored.id), restored] })
  }

  setOptimisticTasks(
    projectId: string,
    tasks: RunningTask[] | ((prev: RunningTask[]) => RunningTask[])
  ): void {
    this.deps.dispatch({
      type: 'projects.updateTasks',
      projectId,
      tasks,
    })
  }

  setOptimisticMedia(
    projectId: string,
    media: ProjectTaskMediaRef[] | ((prev: ProjectTaskMediaRef[]) => ProjectTaskMediaRef[])
  ): void {
    this.deps.dispatch({
      type: 'projects.updateMedia',
      projectId,
      media,
    })
  }
}

export const desktopProjects = new DesktopProjectsRuntime()

export function useDesktopProject(projectId: string, workspaces?: Array<{ workspace_id?: string; path: string }>): DesktopProjectState | undefined {
  useEffect(() => {
    if (!projectId) return
    const realtime = retainProjectsRealtime?.()
    const { release } = desktopProjects.acquire(projectId)
    return () => { release(); realtime?.release() }
  }, [projectId])

  const catalogKey = JSON.stringify(workspaces ?? [])
  useEffect(() => {
    if (!projectId) return
    const catalog = JSON.parse(catalogKey) as Array<{ workspace_id?: string; path: string }>
    dispatchDesktopV3Cache({ type: 'projects.environmentCatalog', projectId, workspaces: catalog.map(w => ({ workspaceId: w.workspace_id, path: w.path })) })
  }, [projectId, catalogKey])

  return useDesktopV3CacheSelector((state) => state.projectsState?.[projectId])
}
