import type {
  Deployment,
  DeploymentLease,
  EnvironmentOperation,
  EnvironmentSummary,
  HistoryFilter,
  OperationHistoryPage,
  OperationStatus,
} from '../environments/types/environments'

export interface DesktopEnvironmentWorkspaceState {
  workspaceId: string
  summary?: EnvironmentSummary
  summaryRevision: number
  deployments: Deployment[]
  activeLeases: Record<string, DeploymentLease>
  operations: EnvironmentOperation[]
  historyPage?: OperationHistoryPage
  historyFilter: HistoryFilter
  loading: boolean
  refreshing: boolean
  stale: boolean
  realtimeStatus: 'connected' | 'connecting' | 'stale' | 'disconnected' | 'error'
  error?: string
  lastObservedAt?: number
  lastReceipt?: {
    operationId: string
    action: string
    status: OperationStatus
    timestamp: number
  }
}

export type DesktopEnvironmentsState = Record<string, DesktopEnvironmentWorkspaceState>

export type DesktopEnvironmentsAction =
  | { type: 'environments.beginLoad'; workspaceId: string; requestId: string }
  | {
      type: 'environments.loadSuccess'
      workspaceId: string
      requestId: string
      summary: EnvironmentSummary
      deployments: Deployment[]
      activeLeases: Record<string, DeploymentLease>
      historyPage: OperationHistoryPage
    }
  | { type: 'environments.loadError'; workspaceId: string; requestId: string; error: string }
  | { type: 'environments.invalidate'; workspaceId?: string; summaryRevision?: number }
  | { type: 'environments.setHistoryFilter'; workspaceId: string; filter: Partial<HistoryFilter> }
  | {
      type: 'environments.realtimeStatusChanged'
      status: 'connected' | 'connecting' | 'stale' | 'disconnected' | 'error'
    }
  | {
      type: 'environments.recordReceipt'
      workspaceId: string
      receipt: { operationId: string; action: string; status: OperationStatus; timestamp: number }
    }
  | { type: 'environments.operationUpdated'; workspaceId: string; operation: EnvironmentOperation }
  | { type: 'environments.evict'; workspaceId: string }

export function defaultHistoryFilter(): HistoryFilter {
  let tz = 'UTC'
  try {
    tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    // fallback
  }
  return {
    timezone: tz,
  }
}

export function createInitialWorkspaceState(workspaceId: string): DesktopEnvironmentWorkspaceState {
  return {
    workspaceId,
    summaryRevision: 0,
    deployments: [],
    activeLeases: {},
    operations: [],
    historyFilter: defaultHistoryFilter(),
    loading: true,
    refreshing: false,
    stale: false,
    realtimeStatus: 'disconnected',
  }
}

export function reduceDesktopEnvironmentsState(
  state: DesktopEnvironmentsState = {},
  action: DesktopEnvironmentsAction,
): DesktopEnvironmentsState {
  switch (action.type) {
    case 'environments.evict': {
      if (!state[action.workspaceId]) return state
      const next = { ...state }
      delete next[action.workspaceId]
      return next
    }

    case 'environments.beginLoad': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          loading: existing.summary === undefined,
          refreshing: true,
          error: undefined,
        },
      }
    }

    case 'environments.loadSuccess': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      const incomingRev = action.summary.revision ?? 0
      // Monotonic revision check: if incoming revision is older than existing revision, ignore summary regression
      const shouldUpdateSummary = incomingRev >= existing.summaryRevision || existing.summary === undefined
      const targetSummary = shouldUpdateSummary ? action.summary : existing.summary
      const targetRevision = Math.max(existing.summaryRevision, incomingRev)

      // Merge operations: keep operations from history page, and update with receipt if pending
      const operations = [...action.historyPage.operations]
      if (existing.lastReceipt) {
        const matchingOp = operations.find((o) => o.operation_id === existing.lastReceipt?.operationId)
        if (matchingOp && matchingOp.status !== existing.lastReceipt.status) {
          // preserve receipt status if receipt is newer than operation last observed
          if (existing.lastReceipt.timestamp >= (matchingOp.observed_at || 0)) {
            matchingOp.status = existing.lastReceipt.status
          }
        }
      }

      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          summary: targetSummary,
          summaryRevision: targetRevision,
          deployments: action.deployments,
          activeLeases: action.activeLeases,
          historyPage: action.historyPage,
          operations,
          loading: false,
          refreshing: false,
          stale: false,
          error: undefined,
          lastObservedAt: Date.now(),
        },
      }
    }

    case 'environments.loadError': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          loading: false,
          refreshing: false,
          stale: true,
          error: action.error,
        },
      }
    }

    case 'environments.invalidate': {
      const targetWorkspaceId = action.workspaceId?.trim()
      const entries = Object.entries(state)
      let changed = false
      const next: DesktopEnvironmentsState = {}

      for (const [wsId, wsState] of entries) {
        if (!targetWorkspaceId || wsId === targetWorkspaceId) {
          // Monotonic check: if revision is supplied and <= current revision, do not invalidate
          if (action.summaryRevision !== undefined && action.summaryRevision <= wsState.summaryRevision) {
            next[wsId] = wsState
            continue
          }
          const nextRevision = action.summaryRevision !== undefined
            ? Math.max(wsState.summaryRevision, action.summaryRevision)
            : wsState.summaryRevision
          changed = true
          next[wsId] = {
            ...wsState,
            summaryRevision: nextRevision,
            stale: true,
          }
        } else {
          next[wsId] = wsState
        }
      }

      return changed ? next : state
    }

    case 'environments.setHistoryFilter': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          historyFilter: {
            ...existing.historyFilter,
            ...action.filter,
          },
          stale: true,
        },
      }
    }

    case 'environments.realtimeStatusChanged': {
      const entries = Object.entries(state)
      if (entries.length === 0) return state
      let changed = false
      const next: DesktopEnvironmentsState = {}

      for (const [wsId, wsState] of entries) {
        if (wsState.realtimeStatus !== action.status) {
          changed = true
          next[wsId] = {
            ...wsState,
            realtimeStatus: action.status,
            stale: action.status !== 'connected' ? true : wsState.stale,
          }
        } else {
          next[wsId] = wsState
        }
      }

      return changed ? next : state
    }

    case 'environments.recordReceipt': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      const ops = existing.operations.map((op) => {
        if (op.operation_id === action.receipt.operationId) {
          return { ...op, status: action.receipt.status }
        }
        return op
      })

      // Also update deployment status if receipt is for stop
      let deps = existing.deployments
      if (action.receipt.action === 'stop') {
        deps = existing.deployments.map((d) => {
          if (d.id === action.receipt.operationId || action.receipt.operationId.includes(d.id)) {
            return { ...d, status: 'stopping' as const }
          }
          return d
        })
      }

      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          lastReceipt: action.receipt,
          operations: ops,
          deployments: deps,
        },
      }
    }

    case 'environments.operationUpdated': {
      const existing = state[action.workspaceId] ?? createInitialWorkspaceState(action.workspaceId)
      const opIndex = existing.operations.findIndex((o) => o.operation_id === action.operation.operation_id)
      let nextOps: EnvironmentOperation[]
      if (opIndex >= 0) {
        nextOps = [...existing.operations]
        nextOps[opIndex] = action.operation
      } else {
        nextOps = [action.operation, ...existing.operations]
      }
      return {
        ...state,
        [action.workspaceId]: {
          ...existing,
          operations: nextOps,
        },
      }
    }

    default:
      return state
  }
}
