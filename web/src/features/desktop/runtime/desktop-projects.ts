import { useEffect } from 'react'
import { requestJson } from '../../../app/api'
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

export interface DesktopProjectsRuntimeDeps {
  fetchTasks: (projectId: string) => Promise<{ tasks?: any[] }>
  fetchTask: (projectId: string, taskId: string) => Promise<{ task?: any }>
  fetchMedia: (projectId: string) => Promise<{ media?: ProjectTaskMediaRef[] }>
  getState: () => DesktopProjectsState
  dispatch: (action: DesktopProjectsAction) => void
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
  private readonly deps: DesktopProjectsRuntimeDeps
  private readonly taskQueue = new Map<string, { projectId: string; task: RunningTask; epoch: number; authoritative: boolean }>()
  private readonly taskReads = new Map<string, { identity: string; demand: unknown; epoch: number }>()
  private readonly taskVersions = new Map<string, number>()
  private taskEpoch = 0
  private unsubscribeCache: (() => void) | null = null

  private ensureSubscribed(): void {
    if (this.unsubscribeCache) return
    const subscribe = this.deps.subscribe ?? subscribeDesktopV3Cache
    this.unsubscribeCache = subscribe((mutation) => this.acceptSessionMutation(mutation))
  }

  acceptSessionMutation(mutation?: DesktopV3CacheMutation): void {
    for (const { projectId } of this.demand.values()) {
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) {
        if (!task.sessionId) continue
        const owners = mutation ? repositoryOwnerIds(task.sessionId, [], mutation.nextState) : new Set([task.sessionId])
        for (const id of extractTaskSessionIds(task)) owners.add(id)
        if (!mutation || repositoryEventInvalidates(mutation.action, owners) || taskPlanEventInvalidates(mutation.action, owners)) this.queueTask(projectId, task, false, true)
      }
    }
  }

  private queueTask(projectId: string, task: RunningTask, authoritative = false, invalidateGit = false): void {
    if ((!task.sessionId && !authoritative) || !this.demand.has(projectId)) return
    const key = JSON.stringify([projectId, task.id])
    if (invalidateGit) {
      this.taskVersions.set(key, (this.taskVersions.get(key) ?? 0) + 1)
      this.deps.dispatch({ type: 'projects.invalidateGit', projectId, taskId: task.id })
    }
    authoritative ||= this.taskQueue.get(key)?.authoritative ?? false
    this.taskQueue.set(key, { projectId, task, epoch: this.taskEpoch, authoritative })
    this.drainTasks()
  }

  private drainTasks(): void {
    for (const [key, entry] of this.taskQueue) {
      if (this.taskReads.size >= 4) break
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
      }
      void this.deps.fetchTask(entry.projectId, entry.task.id).then(response => {
        if (!response.task || response.task.id !== entry.task.id ||
          (!entry.authoritative && response.task.session_id !== entry.task.sessionId) ||
          (response.task.revision ?? 0) < (entry.task.revision ?? 0)) {
          apply(task => ({ ...task, gitStatus: 'unknown', isIntegrated: false, unintegratedCommits: 0,
            syncWarning: 'Task status response did not match the current execution' }), true)
          return
        }
        if (entry.authoritative && response.task.archived) {
          apply(() => undefined)
          return
        }
        apply(() => mapBackendTask(response.task), true)
      }).catch(error => {
        apply(task => ({ ...task, gitStatus: 'unknown', isIntegrated: false, unintegratedCommits: 0, syncWarning: error instanceof Error ? error.message : 'Task status refresh failed' }), true)
      }).finally(() => {
        this.taskReads.delete(key)
        this.drainTasks()
      })
    }
  }

  constructor(deps?: Partial<DesktopProjectsRuntimeDeps>) {
    this.deps = {
      fetchTasks:
        deps?.fetchTasks ??
        ((projectId: string) =>
          requestJson<{ tasks?: any[] }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks`)),
      fetchTask: deps?.fetchTask ?? ((projectId, taskId) =>
        requestJson<{ task?: any }>(`/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}`)),
      fetchMedia:
        deps?.fetchMedia ??
        ((projectId: string) =>
          requestJson<{ media?: ProjectTaskMediaRef[] }>(`/v3/projects/${encodeURIComponent(projectId)}/media`)),
      getState: deps?.getState ?? (() => getDesktopV3CacheSnapshot().projectsState ?? {}),
      dispatch: deps?.dispatch ?? ((action: DesktopProjectsAction) => dispatchDesktopV3Cache(action as any)),
      subscribe: deps?.subscribe ?? subscribeDesktopV3Cache,
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
          for (const [key, entry] of this.taskQueue) if (entry.projectId === projectId) this.taskQueue.delete(key)
          for (const key of this.taskVersions.keys()) if (JSON.parse(key)[0] === projectId) this.taskVersions.delete(key)
          this.inFlight.delete(projectId)
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
    for (const [key, entry] of this.taskQueue) if (entry.projectId === projectId) this.taskQueue.delete(key)
    for (const key of this.taskVersions.keys()) if (JSON.parse(key)[0] === projectId) this.taskVersions.delete(key)
    this.inFlight.delete(projectId)
    this.deps.dispatch({ type: 'projects.evict', projectId })
    if (this.demand.size === 0) {
      this.unsubscribeCache?.()
      this.unsubscribeCache = null
    }
  }

  reset(): void {
    this.taskEpoch++
    this.taskQueue.clear()
    this.taskVersions.clear()
    this.demand.clear()
    this.inFlight.clear()
    this.unsubscribeCache?.()
    this.unsubscribeCache = null
  }

  refresh(projectId: string, inspectGit = true): Promise<void> {
    if (!projectId) return Promise.resolve()
    if (inspectGit) {
      for (const task of this.deps.getState()[projectId]?.tasks ?? []) this.queueTask(projectId, task, false, true)
    }
    const pending = this.inFlight.get(projectId)
    if (pending) return pending

    const requestId = crypto.randomUUID()
    this.deps.dispatch({ type: 'projects.beginLoad', projectId, requestId })
    const state = this.deps.getState()[projectId]
    const generation = state?.generation ?? 0

    const promise: Promise<void> = Promise.all([
      this.deps.fetchTasks(projectId),
      this.deps.fetchMedia(projectId),
    ])
      .then(([tasksRes, mediaRes]) => {
        if (this.inFlight.get(projectId) !== promise) return
        const backendTasks = mapBackendTasks(tasksRes?.tasks || [])
        const media = mediaRes?.media || []
        this.deps.dispatch({
          type: 'projects.loadSuccess',
          projectId,
          requestId,
          generation,
          tasks: backendTasks,
          media,
        })
        if (this.deps.getState()[projectId]?.generation === generation) {
          const project = this.deps.getState()[projectId]
          for (const task of project?.tasks ?? []) {
            if (project?.gitObservations?.[task.id] !== taskGitIdentity(task)) {
              const key = JSON.stringify([projectId, task.id])
              // An unchanged card already being inspected needs no second read.
              const read = this.taskReads.get(key)
              const queued = this.taskQueue.get(key)
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
          error: error instanceof Error ? error.message : 'Failed to load project tasks and media',
        })
      })
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

  invalidate(projectId?: string): void {
    this.deps.dispatch({ type: 'projects.invalidate', projectId })
    for (const { projectId: id } of this.demand.values()) {
      if (!projectId || id === projectId) {
        void this.refresh(id, false)
      }
    }
  }

  acceptFrame(frame: { kind: string; project_id?: string; projectId?: string; event?: { payload?: unknown } }): void {
    const projectId = frame.project_id || frame.projectId
    if (frame.kind === 'project.updated') {
      const payload = frame.event?.payload
      const change = payload && typeof payload === 'object' ? payload as Record<string, unknown> : undefined
      const taskId = typeof change?.task_id === 'string' ? change.task_id : undefined
      const task = projectId && taskId ? this.deps.getState()[projectId]?.tasks.find(task => task.id === taskId) : undefined
      // Durable task updates already identify their card. Do not reload the board,
      // media or unrelated Git inspections. Collection/membership changes and
      // older/unknown frames still use the canonical snapshot repair below.
      if (projectId && task && change?.action === 'task_updated') {
        this.queueTask(projectId, task, true, true)
        return
      }
      if (change?.resource === 'designs') return
      this.invalidate(projectId)
      for (const listener of this.projectUpdateListeners) listener(projectId)
    } else if (
      frame.kind === 'cursor.error' ||
      frame.kind === 'rehydrate.required' ||
      frame.kind === 'auth.credentials.updated'
    ) {
      for (const { projectId: id } of this.demand.values()) {
        for (const task of this.deps.getState()[id]?.tasks ?? []) this.queueTask(id, task, false, true)
      }
      this.invalidate()
      for (const listener of this.projectUpdateListeners) listener()
    }
  }

  archiveReceipt(projectId: string, receipt: { id: string; revision: number; archived: boolean }): void {
    if (!receipt.archived || !receipt.id || !Number.isInteger(receipt.revision)) throw new Error('Invalid task archive receipt')
    this.deps.dispatch({ type: 'projects.updateTasks', projectId, tasks: tasks => tasks, archivedReceipt: receipt })
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

export function useDesktopProject(projectId: string): DesktopProjectState | undefined {
  useEffect(() => {
    if (!projectId) return
    const { release } = desktopProjects.acquire(projectId)
    return release
  }, [projectId])

  return useDesktopV3CacheSelector((state) => state.projectsState?.[projectId])
}
