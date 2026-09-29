import { useEffect } from 'react'
import { requestJson } from '../../../app/api'
import type { RunningTask, ProjectTaskMediaRef } from '../orchestrate/orchestrate-types'
import {
  mapBackendTasks,
  mapBackendTask,
  type DesktopProjectState,
  type DesktopProjectsAction,
  type DesktopProjectsState,
} from '../state/desktop-projects-state'
import {
  dispatchDesktopV3Cache,
  getDesktopV3CacheSnapshot,
  useDesktopV3CacheSelector,
} from '../state/desktop-v3-cache-store'

export * from './desktop-projects-membership'

export interface DesktopProjectsRuntimeDeps {
  fetchTasks: (projectId: string) => Promise<{ tasks?: any[] }>
  fetchTask: (projectId: string, taskId: string) => Promise<{ task?: any }>
  fetchMedia: (projectId: string) => Promise<{ media?: ProjectTaskMediaRef[] }>
  getState: () => DesktopProjectsState
  dispatch: (action: DesktopProjectsAction) => void
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
    }
  }

  acquire(projectId: string): { ready: Promise<void>; release: () => void } {
    if (!projectId) return { ready: Promise.resolve(), release: () => {} }
    const current = this.demand.get(projectId)
    this.demand.set(projectId, { projectId, count: (current?.count ?? 0) + 1 })
    let released = false
    return {
      ready: this.refresh(projectId),
      release: () => {
        if (released) return
        released = true
        const entry = this.demand.get(projectId)
        if (entry && --entry.count === 0) {
          this.demand.delete(projectId)
          this.inFlight.delete(projectId)
          this.deps.dispatch({ type: 'projects.evict', projectId })
        }
      },
    }
  }

  evict(projectId: string): void {
    if (!projectId) return
    this.demand.delete(projectId)
    this.inFlight.delete(projectId)
    this.deps.dispatch({ type: 'projects.evict', projectId })
  }

  reset(): void {
    this.demand.clear()
    this.inFlight.clear()
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
      .then(async ([tasksRes, mediaRes]) => {
        if (this.inFlight.get(projectId) !== promise) return
        const backendTasks = mapBackendTasks(tasksRes?.tasks || [])
        // Collection responses intentionally contain only stale Git hints. Inspect
        // settled worktrees through the existing single-task authority, at most
        // two at a time. Never run Git scans on token/usage events or a timer.
        const pending = backendTasks.filter(task => task.worktreeBranch && task.sessionId &&
          !['running', 'in_progress', 'planning', 'queued', 'pending_approval'].includes(task.status))
        const inspect = async () => {
          while (pending.length && this.inFlight.get(projectId) === promise) {
            const task = pending.shift()!
            try {
              const result = await this.deps.fetchTask(projectId, task.id)
              if (!result.task || result.task.id !== task.id) throw new Error('Task Git inspection returned no matching task')
              Object.assign(task, mapBackendTask(result.task))
            } catch (error) {
              task.gitStatus = 'stale'
              task.syncWarning = error instanceof Error ? error.message : 'Git inspection unavailable'
            }
          }
        }
        await Promise.all([inspect(), inspect()])
        if (this.inFlight.get(projectId) !== promise) return
        const media = mediaRes?.media || []
        this.deps.dispatch({
          type: 'projects.loadSuccess',
          projectId,
          requestId,
          generation,
          tasks: backendTasks,
          media,
        })
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

  acceptFrame(frame: { kind: string; project_id?: string; projectId?: string; session_id?: string; event?: { session_id?: string; event_type?: string } }): void {
    const projectId = frame.project_id || frame.projectId
    const eventType = frame.event?.event_type || ''
    const sessionId = frame.session_id || frame.event?.session_id
    if (frame.kind === 'event' && sessionId &&
      (/^session\.run\.(completed|failed|cancelled|interrupted)$/.test(eventType) || eventType.startsWith('session.worktree.'))) {
      for (const { projectId: id } of this.demand.values()) {
        if (this.deps.getState()[id]?.tasks.some(task => task.sessionId === sessionId ||
          task.associatedSessionIds?.includes(sessionId) || task.taskProgramStatus?.jobs?.some(job => job.child_session_id === sessionId || job.current_session_id === sessionId))) {
          this.invalidate(id)
        }
      }
    } else if (frame.kind === 'project.updated') {
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
