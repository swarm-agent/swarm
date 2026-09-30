import { useEffect } from 'react'
import { requestJson } from '../../../app/api'
import type { RunningTask, ProjectTaskMediaRef } from '../orchestrate/orchestrate-types'
import {
  mapBackendTask,
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
  private readonly taskQueue = new Map<string, { projectId: string; task: RunningTask; epoch: number }>()
  private readonly taskReads = new Set<string>()
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
        if (!mutation || repositoryEventInvalidates(mutation.action, owners) || taskPlanEventInvalidates(mutation.action, owners)) this.queueTask(projectId, task)
      }
    }
  }

  private queueTask(projectId: string, task: RunningTask): void {
    if (!task.sessionId || !this.demand.has(projectId)) return
    this.taskQueue.set(JSON.stringify([projectId, task.id]), { projectId, task, epoch: this.taskEpoch })
    this.drainTasks()
  }

  private drainTasks(): void {
    for (const [key, entry] of this.taskQueue) {
      if (this.taskReads.size >= 4) break
      if (this.taskReads.has(key)) continue
      this.taskQueue.delete(key)
      if (!this.demand.has(entry.projectId)) continue
      const currentTask = this.deps.getState()[entry.projectId]?.tasks.find(task => task.id === entry.task.id)
      if (!currentTask || currentTask.sessionId !== entry.task.sessionId) continue
      entry.task = currentTask
      this.taskReads.add(key)
      const demand = this.demand.get(entry.projectId)
      const generation = this.deps.getState()[entry.projectId]?.generation
      const apply = (update: (task: RunningTask) => RunningTask) => {
        if (entry.epoch !== this.taskEpoch || this.demand.get(entry.projectId) !== demand || this.deps.getState()[entry.projectId]?.generation !== generation) return
        this.setOptimisticTasks(entry.projectId, tasks => tasks.map(task =>
          task.id === entry.task.id && task.sessionId === entry.task.sessionId && task.revision === entry.task.revision ? update(task) : task))
      }
      void this.deps.fetchTask(entry.projectId, entry.task.id).then(response => {
        if (!response.task || response.task.id !== entry.task.id || response.task.session_id !== entry.task.sessionId) return
        apply(() => mapBackendTask(response.task))
      }).catch(error => {
        apply(task => ({ ...task, gitStatus: 'unknown', isIntegrated: false, syncWarning: error instanceof Error ? error.message : 'Task status refresh failed' }))
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
      ready: this.refresh(projectId),
      release: () => {
        if (released) return
        released = true
        const entry = this.demand.get(projectId)
        if (entry && --entry.count === 0) {
          this.demand.delete(projectId)
          for (const [key, entry] of this.taskQueue) if (entry.projectId === projectId) this.taskQueue.delete(key)
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
    this.demand.clear()
    this.inFlight.clear()
    this.unsubscribeCache?.()
    this.unsubscribeCache = null
  }

  refresh(projectId: string): Promise<void> {
    if (!projectId) return Promise.resolve()
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
          for (const task of backendTasks) this.queueTask(projectId, task)
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
          void this.refresh(projectId)
        }
      })

    this.inFlight.set(projectId, promise)
    return promise
  }

  invalidate(projectId?: string): void {
    this.deps.dispatch({ type: 'projects.invalidate', projectId })
    for (const { projectId: id } of this.demand.values()) {
      if (!projectId || id === projectId) {
        void this.refresh(id)
      }
    }
  }

  acceptFrame(frame: { kind: string; project_id?: string; projectId?: string }): void {
    const projectId = frame.project_id || frame.projectId
    if (frame.kind === 'project.updated') {
      this.invalidate(projectId)
      for (const listener of this.projectUpdateListeners) listener(projectId)
    } else if (
      frame.kind === 'cursor.error' ||
      frame.kind === 'rehydrate.required' ||
      frame.kind === 'auth.credentials.updated'
    ) {
      this.invalidate()
      for (const listener of this.projectUpdateListeners) listener()
    }
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
