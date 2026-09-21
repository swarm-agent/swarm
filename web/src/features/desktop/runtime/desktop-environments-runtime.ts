import { useEffect, useMemo } from 'react'
import {
  cancelEnvironmentOperation,
  fetchDeployments,
  fetchOperationHistory,
  stopDeployment,
} from '../environments/services/environments-api'
import type {
  CancelOperationParams,
  Deployment,
  DeploymentLease,
  EnvironmentOperation,
  EnvironmentSummary,
  HistoryFilter,
  OperationHistoryPage,
  OperationStatus,
} from '../environments/types/environments'
import {
  createInitialWorkspaceState,
  type DesktopEnvironmentsAction,
  type DesktopEnvironmentWorkspaceState,
} from '../state/desktop-environments-state'
import {
  dispatchDesktopV3Cache,
  getDesktopV3CacheSnapshot,
  useDesktopV3CacheSelector,
} from '../state/desktop-v3-cache-store'
import { retainDesktopV3RealtimeController, type DesktopV3RealtimeLease } from '../realtime/v3-realtime-controller'

export interface DesktopEnvironmentsRuntimeDeps {
  fetchDeployments: (
    workspaceId: string,
    signal?: AbortSignal,
    environmentId?: string,
    workspacePath?: string,
  ) => Promise<{ deployments: Deployment[]; activeLeases: Record<string, DeploymentLease> }>
  fetchOperationHistory: (
    workspaceId: string,
    query?: Partial<HistoryFilter>,
    signal?: AbortSignal,
    workspacePath?: string,
  ) => Promise<OperationHistoryPage>
  cancelOperation: (
    workspaceId: string,
    params: CancelOperationParams,
    workspacePath?: string,
  ) => Promise<{ operation: EnvironmentOperation; operation_id: string; status: OperationStatus }>
  stopDeployment: (
    workspaceId: string,
    deploymentId: string,
    workspacePath?: string,
  ) => Promise<{ operation?: EnvironmentOperation; operation_id?: string; status?: OperationStatus; deployment?: Deployment }>
  dispatch: (action: DesktopEnvironmentsAction) => void
  getState: () => Record<string, DesktopEnvironmentWorkspaceState>
  retainRealtime: (input?: { ownerKey?: string }) => DesktopV3RealtimeLease
}

export function msUntilNextMidnight(timezone: string, now: Date = new Date()): number {
  try {
    const formatter = new Intl.DateTimeFormat('en-US', {
      timeZone: timezone,
      year: 'numeric',
      month: 'numeric',
      day: 'numeric',
      hour: 'numeric',
      minute: 'numeric',
      second: 'numeric',
      hour12: false,
    })
    const parts = formatter.formatToParts(now)
    let hour = 0
    let minute = 0
    let second = 0
    for (const part of parts) {
      if (part.type === 'hour') hour = parseInt(part.value, 10) % 24
      if (part.type === 'minute') minute = parseInt(part.value, 10)
      if (part.type === 'second') second = parseInt(part.value, 10)
    }
    const secondsPassed = hour * 3600 + minute * 60 + second
    const secondsUntilMidnight = 86400 - secondsPassed
    const msUntilMidnight = secondsUntilMidnight * 1000 - now.getMilliseconds()
    return Math.max(1000, Math.min(86400000, msUntilMidnight + 1000))
  } catch {
    const utcMidnight = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + 1))
    return Math.max(1000, utcMidnight.getTime() - now.getTime() + 500)
  }
}

export class DesktopEnvironmentsRuntime {
  private readonly demand = new Map<string, number>()
  private readonly flights = new Map<string, Promise<void>>()
  private readonly pendingInvalidations = new Set<string>()
  private readonly abortControllers = new Map<string, AbortController>()
  private readonly rolloverTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private realtimeLease?: DesktopV3RealtimeLease
  private readonly deps: DesktopEnvironmentsRuntimeDeps

  constructor(deps?: Partial<DesktopEnvironmentsRuntimeDeps>) {
    this.deps = {
      fetchDeployments: deps?.fetchDeployments ?? fetchDeployments,
      fetchOperationHistory: deps?.fetchOperationHistory ?? fetchOperationHistory,
      cancelOperation: deps?.cancelOperation ?? cancelEnvironmentOperation,
      stopDeployment: deps?.stopDeployment ?? stopDeployment,
      dispatch: deps?.dispatch ?? ((action) => dispatchDesktopV3Cache(action)),
      getState: deps?.getState ?? (() => getDesktopV3CacheSnapshot().environmentsByWorkspace ?? {}),
      retainRealtime: deps?.retainRealtime ?? retainDesktopV3RealtimeController,
    }
  }

  acquire(workspaceId: string): { ready: Promise<void>; release: () => void } {
    const normalized = workspaceId.trim()
    if (!normalized) {
      return { ready: Promise.resolve(), release: () => {} }
    }

    const currentCount = this.demand.get(normalized) ?? 0
    this.demand.set(normalized, currentCount + 1)

    if (!this.realtimeLease) {
      this.realtimeLease = this.deps.retainRealtime({ ownerKey: 'desktop-environments' })
    }

    const ready = this.refresh(normalized)

    let released = false
    return {
      ready,
      release: () => {
        if (released) return
        released = true
        const count = this.demand.get(normalized) ?? 0
        if (count <= 1) {
          this.demand.delete(normalized)
          const timer = this.rolloverTimers.get(normalized)
          if (timer) {
            clearTimeout(timer)
            this.rolloverTimers.delete(normalized)
          }
          const controller = this.abortControllers.get(normalized)
          if (controller) {
            controller.abort()
            this.abortControllers.delete(normalized)
          }
        } else {
          this.demand.set(normalized, count - 1)
        }

        if (this.demand.size === 0 && this.realtimeLease) {
          this.realtimeLease.release()
          this.realtimeLease = undefined
        }
      },
    }
  }

  scheduleDayRollover(workspaceId: string, timezone: string): void {
    const existingTimer = this.rolloverTimers.get(workspaceId)
    if (existingTimer) {
      clearTimeout(existingTimer)
      this.rolloverTimers.delete(workspaceId)
    }

    if (!this.demand.has(workspaceId)) return

    const ms = msUntilNextMidnight(timezone)
    const timer = setTimeout(() => {
      this.rolloverTimers.delete(workspaceId)
      this.invalidate(workspaceId)
      // schedule next
      this.scheduleDayRollover(workspaceId, timezone)
    }, ms)

    this.rolloverTimers.set(workspaceId, timer)
  }

  acceptFrame(frame: { kind: string; [key: string]: unknown }): void {
    if (frame.kind === 'cursor.error' || frame.kind === 'rehydrate.required') {
      this.invalidate()
      return
    }

    const event = frame.event as { type?: string; event_type?: string; session_id?: string; payload?: unknown } | undefined
    const type = String(event?.type ?? event?.event_type ?? frame.event_type ?? frame.kind ?? '')

    if (type === 'environment.updated' || frame.kind === 'environment.updated') {
      const payload = (event?.payload ?? frame.payload ?? {}) as Record<string, unknown>
      let workspaceId = typeof payload.workspace_id === 'string' ? payload.workspace_id.trim() : ''

      if (!workspaceId && frame.session_id) {
        const parts = String(frame.session_id).split(':')
        if (parts[0] === '__environment__' && parts[2]) {
          workspaceId = parts[2]
        }
      }

      const summaryRevision = typeof payload.summary_revision === 'number' ? payload.summary_revision : undefined

      if (workspaceId) {
        const state = this.deps.getState()
        const currentWs = state[workspaceId]
        // Monotonic revision check: if incoming revision is known and <= existing revision, ignore duplicate
        if (summaryRevision !== undefined && currentWs && summaryRevision <= currentWs.summaryRevision) {
          return
        }
        this.invalidate(workspaceId, summaryRevision)
      } else {
        this.invalidate(undefined, summaryRevision)
      }
    }
  }

  onRealtimeStatus(status: 'connected' | 'connecting' | 'stale' | 'disconnected' | 'error'): void {
    this.deps.dispatch({ type: 'environments.realtimeStatusChanged', status })
    if (status === 'connected') {
      this.invalidate()
    }
  }

  invalidate(workspaceId?: string, summaryRevision?: number): void {
    this.deps.dispatch({ type: 'environments.invalidate', workspaceId, summaryRevision })
    const targetWs = workspaceId?.trim()

    for (const demandedWs of this.demand.keys()) {
      if (!targetWs || demandedWs === targetWs) {
        if (this.flights.has(demandedWs)) {
          this.pendingInvalidations.add(demandedWs)
        } else {
          void this.refresh(demandedWs)
        }
      }
    }
  }

  refresh(workspaceId: string, options?: { force?: boolean }): Promise<void> {
    const normalized = workspaceId.trim()
    if (!normalized) return Promise.resolve()

    const existingFlight = this.flights.get(normalized)
    if (existingFlight && !options?.force) {
      return existingFlight
    }

    if (options?.force && this.abortControllers.has(normalized)) {
      this.abortControllers.get(normalized)?.abort()
    }

    const abortController = new AbortController()
    this.abortControllers.set(normalized, abortController)
    const requestId = crypto.randomUUID()

    this.deps.dispatch({
      type: 'environments.beginLoad',
      workspaceId: normalized,
      requestId,
    })

    const state = this.deps.getState()
    const wsState = state[normalized]
    const filter = wsState?.historyFilter ?? { timezone: 'UTC' }

    const promise: Promise<void> = Promise.all([
      this.deps.fetchDeployments(normalized, abortController.signal),
      this.deps.fetchOperationHistory(normalized, filter, abortController.signal),
    ])
      .then(([deploymentsResult, historyPage]) => {
        if (abortController.signal.aborted) return

        this.deps.dispatch({
          type: 'environments.loadSuccess',
          workspaceId: normalized,
          requestId,
          summary: historyPage.summary,
          deployments: deploymentsResult.deployments,
          activeLeases: deploymentsResult.activeLeases,
          historyPage,
        })

        this.scheduleDayRollover(normalized, filter.timezone)
      })
      .catch((err: unknown) => {
        if (abortController.signal.aborted) return
        const message = err instanceof Error ? err.message : String(err)
        this.deps.dispatch({
          type: 'environments.loadError',
          workspaceId: normalized,
          requestId,
          error: message,
        })
      })
      .finally(() => {
        if (this.flights.get(normalized) === promise) {
          this.flights.delete(normalized)
        }
        if (this.pendingInvalidations.delete(normalized) && this.demand.has(normalized)) {
          void this.refresh(normalized)
        }
      })

    this.flights.set(normalized, promise)
    return promise
  }

  async stopDeployment(
    workspaceId: string,
    deploymentId: string,
    workspacePath = '',
  ): Promise<{ operation?: EnvironmentOperation; operation_id?: string; status?: OperationStatus; deployment?: Deployment }> {
    const result = await this.deps.stopDeployment(workspaceId, deploymentId, workspacePath)
    const opId = result.operation_id || result.operation?.operation_id || `stop-${deploymentId}`
    const status = result.status || result.operation?.status || 'running'

    this.deps.dispatch({
      type: 'environments.recordReceipt',
      workspaceId,
      receipt: {
        operationId: opId,
        action: 'stop',
        status,
        timestamp: Date.now(),
      },
    })

    this.invalidate(workspaceId)
    return result
  }

  async cancelOperation(
    workspaceId: string,
    operationId: string,
    reason?: string,
    workspacePath = '',
  ): Promise<{ operation: EnvironmentOperation; operation_id: string; status: OperationStatus }> {
    const result = await this.deps.cancelOperation(workspaceId, { operationId, reason }, workspacePath)
    this.deps.dispatch({
      type: 'environments.recordReceipt',
      workspaceId,
      receipt: {
        operationId,
        action: 'cancel',
        status: result.status || 'cancelling',
        timestamp: Date.now(),
      },
    })

    this.invalidate(workspaceId)
    return result
  }

  setHistoryFilter(workspaceId: string, filter: Partial<HistoryFilter>): void {
    this.deps.dispatch({
      type: 'environments.setHistoryFilter',
      workspaceId,
      filter,
    })
    if (filter.timezone) {
      this.scheduleDayRollover(workspaceId, filter.timezone)
    }
    void this.refresh(workspaceId, { force: true })
  }
}

export const desktopEnvironments = new DesktopEnvironmentsRuntime()

export function useDesktopEnvironments(workspaceId: string) {
  const normalized = workspaceId.trim()
  const workspaceState = useDesktopV3CacheSelector(
    (state) => (normalized ? state.environmentsByWorkspace?.[normalized] : undefined),
  )

  useEffect(() => {
    if (!normalized) return
    const lease = desktopEnvironments.acquire(normalized)
    return lease.release
  }, [normalized])

  const actions = useMemo(
    () => ({
      refresh: (options?: { force?: boolean }) => desktopEnvironments.refresh(normalized, options),
      stopDeployment: (deploymentId: string, workspacePath?: string) =>
        desktopEnvironments.stopDeployment(normalized, deploymentId, workspacePath),
      cancelOperation: (operationId: string, reason?: string, workspacePath?: string) =>
        desktopEnvironments.cancelOperation(normalized, operationId, reason, workspacePath),
      setHistoryFilter: (filter: Partial<HistoryFilter>) =>
        desktopEnvironments.setHistoryFilter(normalized, filter),
    }),
    [normalized],
  )

  return {
    state: workspaceState ?? (normalized ? createInitialWorkspaceState(normalized) : undefined),
    ...actions,
  }
}
