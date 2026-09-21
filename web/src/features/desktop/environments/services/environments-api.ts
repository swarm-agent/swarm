import { requestJson } from '../../../../app/api'
import type {
  CancelOperationParams,
  CancelOperationResponse,
  Connection,
  ConnectionCheckResult,
  ConnectionKind,
  ConsumerType,
  DailyOperationCounts,
  Deployment,
  DeploymentLease,
  Environment,
  EnvironmentOperation,
  EnvironmentSummary,
  OperationHistoryPage,
  OperationHistoryQuery,
  OperationStatus,
  StopDeploymentResponse,
  WorkspaceSettings,
} from '../types/environments'

interface ConnectionsListResponse {
  ok: boolean
  connections?: Connection[]
  connection?: Connection
  count?: number
}

interface ConnectionMutationResponse {
  ok: boolean
  connection?: Connection
  deleted?: boolean
  id?: string
}

interface EnvironmentsListResponse {
  ok: boolean
  environments?: Environment[]
  environment?: Environment
  settings?: WorkspaceSettings
  count?: number
}

interface EnvironmentMutationResponse {
  ok: boolean
  environment?: Environment
  settings?: WorkspaceSettings
  deleted?: boolean
  id?: string
}

interface DeploymentsListResponse {
  ok: boolean
  deployments?: Deployment[]
  deployment?: Deployment
  active_leases?: Record<string, DeploymentLease>
  count?: number
}

interface DeploymentMutationResponse {
  ok: boolean
  deployment?: Deployment
  lease?: DeploymentLease
  reused?: boolean
  destroyed?: boolean
  id?: string
}

export async function fetchConnections(
  workspaceId: string,
  signal?: AbortSignal,
  workspacePath = '',
): Promise<Connection[]> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  const response = await requestJson<ConnectionsListResponse>(`/v1/connections?${search.toString()}`, { signal })
  return Array.isArray(response.connections) ? response.connections : []
}

export async function saveConnection(
  workspaceId: string,
  params: {
    id?: string
    name: string
    description?: string
    kind: ConnectionKind
    host?: string
    port?: number
    user?: string
    ssh_key_path?: string
    known_hosts_file?: string
    socket_path?: string
    docker_host?: string
    capabilities?: Connection['capabilities']
  },
  workspacePath = '',
): Promise<Connection> {
  const response = await requestJson<ConnectionMutationResponse>('/v1/connections', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: params.id ? 'update' : 'create',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      ...params,
    }),
  })
  if (!response.connection) throw new Error('Connection save returned no data')
  return response.connection
}

export async function deleteConnection(
  workspaceId: string,
  connectionId: string,
  workspacePath = '',
): Promise<void> {
  await requestJson<ConnectionMutationResponse>('/v1/connections', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'delete',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      id: connectionId,
    }),
  })
}

export async function checkConnection(
  workspaceId: string,
  target: string | { kind: ConnectionKind; socket_path?: string; host?: string; port?: number; user?: string; ssh_key_path?: string },
  workspacePath = '',
): Promise<ConnectionCheckResult> {
  const body: Record<string, unknown> = {
    action: 'check',
    workspace_id: workspaceId,
    workspace_path: workspacePath,
  }
  if (typeof target === 'string') {
    body.id = target
  } else {
    body.kind = target.kind
    body.socket_path = target.socket_path
    body.host = target.host
    body.port = target.port
    body.user = target.user
    body.ssh_key_path = target.ssh_key_path
  }

  return requestJson<ConnectionCheckResult>('/v1/connections', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
}

export async function fetchEnvironments(
  workspaceId: string,
  signal?: AbortSignal,
  workspacePath = '',
): Promise<{ environments: Environment[]; settings: WorkspaceSettings }> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  const response = await requestJson<EnvironmentsListResponse>(`/v1/environments?${search.toString()}`, { signal })
  return {
    environments: Array.isArray(response.environments) ? response.environments : [],
    settings: response.settings ?? { workspace_id: workspaceId, account_scope_id: '' },
  }
}

export async function saveEnvironment(
  workspaceId: string,
  environment: Partial<Environment> & { name: string; container: Environment['container'] },
  id?: string,
  workspacePath = '',
): Promise<Environment> {
  const response = await requestJson<EnvironmentMutationResponse>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: id ? 'update' : 'create',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      id: id || environment.id,
      environment: {
        ...environment,
        id: id || environment.id,
      },
    }),
  })
  if (!response.environment) throw new Error('Environment save returned no data')
  return response.environment
}

export async function deleteEnvironment(
  workspaceId: string,
  environmentId: string,
  workspacePath = '',
): Promise<void> {
  await requestJson<EnvironmentMutationResponse>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'delete',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      id: environmentId,
    }),
  })
}

export async function setDefaultTestEnvironment(
  workspaceId: string,
  environmentId: string,
  workspacePath = '',
): Promise<WorkspaceSettings> {
  const response = await requestJson<EnvironmentMutationResponse>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'set_default_test_environment',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      default_test_environment_id: environmentId,
    }),
  })
  return response.settings ?? { workspace_id: workspaceId, account_scope_id: '', default_test_environment_id: environmentId }
}

export async function setDefaultConnection(
  workspaceId: string,
  connectionId: string,
  workspacePath = '',
): Promise<WorkspaceSettings> {
  const response = await requestJson<EnvironmentMutationResponse>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'set_default_connection',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      default_connection_id: connectionId,
    }),
  })
  return response.settings ?? { workspace_id: workspaceId, account_scope_id: '', default_connection_id: connectionId }
}

export async function fetchDeployments(
  workspaceId: string,
  signal?: AbortSignal,
  environmentId = '',
  workspacePath = '',
): Promise<{ deployments: Deployment[]; activeLeases: Record<string, DeploymentLease> }> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  if (environmentId.trim()) search.set('environment_id', environmentId.trim())
  search.set('action', 'list_deployments')

  try {
    const response = await requestJson<DeploymentsListResponse>(`/v1/environments?${search.toString()}`, { signal })
    if (response && Array.isArray(response.deployments)) {
      return {
        deployments: response.deployments,
        activeLeases: response.active_leases ?? {},
      }
    }
  } catch {
    // fallback to /v1/deployments
  }

  search.delete('action')
  const fallback = await requestJson<DeploymentsListResponse>(`/v1/deployments?${search.toString()}`, { signal })
  return {
    deployments: Array.isArray(fallback.deployments) ? fallback.deployments : [],
    activeLeases: fallback.active_leases ?? {},
  }
}

export async function fetchCurrentOperations(
  workspaceId: string,
  signal?: AbortSignal,
  workspacePath = '',
): Promise<EnvironmentOperation[]> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  search.set('action', 'operations')

  try {
    const response = await requestJson<{ ok: boolean; operations?: EnvironmentOperation[] }>(
      `/v1/environments?${search.toString()}`,
      { signal },
    )
    if (response && Array.isArray(response.operations)) {
      return response.operations
    }
  } catch {
    // fallback if endpoint is unavailable
  }
  return []
}

export async function fetchEnvironmentSummary(
  workspaceId: string,
  signal?: AbortSignal,
  workspacePath = '',
): Promise<EnvironmentSummary> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  search.set('action', 'summary')
  const response = await requestJson<{ ok: boolean; summary?: EnvironmentSummary }>(
    `/v1/environments?${search.toString()}`,
    { signal },
  )
  if (!response.summary) throw new Error('Environment summary returned no data')
  return response.summary
}

export async function fetchOperationHistory(
  workspaceId: string,
  query: Partial<OperationHistoryQuery> = {},
  signal?: AbortSignal,
  workspacePath = '',
): Promise<OperationHistoryPage> {
  const search = new URLSearchParams()
  if (workspaceId.trim()) search.set('workspace_id', workspaceId.trim())
  if (workspacePath.trim()) search.set('workspace_path', workspacePath.trim())
  search.set('action', 'history')
  if (query.environment_id?.trim()) search.set('environment_id', query.environment_id.trim())
  if (query.deployment_id?.trim()) search.set('deployment_id', query.deployment_id.trim())
  if (query.actor?.trim()) search.set('actor', query.actor.trim())
  if (query.session_id?.trim()) search.set('session_id', query.session_id.trim())
  if (query.worker_id?.trim()) search.set('worker_id', query.worker_id.trim())
  if (query.status?.trim()) search.set('status', query.status.trim())
  if (query.action?.trim()) search.set('action_filter', query.action.trim())
  if (query.timezone?.trim()) search.set('timezone', query.timezone.trim())
  if (query.start_date?.trim()) search.set('start_date', query.start_date.trim())
  if (query.end_date?.trim()) search.set('end_date', query.end_date.trim())
  if (query.cursor?.trim()) search.set('cursor', query.cursor.trim())
  if (query.limit && query.limit > 0) search.set('limit', String(query.limit))

  const response = await requestJson<{
    ok: boolean
    history?: OperationHistoryPage
    operations?: EnvironmentOperation[]
    daily_totals?: DailyOperationCounts[]
    summary?: EnvironmentSummary
    next_cursor?: string
    has_more?: boolean
  }>(`/v1/environments?${search.toString()}`, { signal })

  return {
    operations: Array.isArray(response.operations)
      ? response.operations
      : (response.history?.operations ?? []),
    daily_totals: Array.isArray(response.daily_totals)
      ? response.daily_totals
      : (response.history?.daily_totals ?? []),
    summary: response.summary ?? response.history?.summary ?? {
      account_scope_id: '',
      workspace_id: workspaceId,
      revision: 0,
      updated_at: 0,
      active_deployments: 0,
      running_exec_ops: 0,
      queued_ops: 0,
      running_ops: 0,
      cancelling_ops: 0,
      failed_ops: 0,
      cleanup_failed_ops: 0,
      unknown_ops: 0,
      succeeded_ops: 0,
      cancelled_ops: 0,
      timed_out_ops: 0,
      total_ops: 0,
    },
    next_cursor: response.next_cursor ?? response.history?.next_cursor,
    has_more: Boolean(response.has_more ?? response.history?.has_more),
  }
}

export async function cancelEnvironmentOperation(
  workspaceId: string,
  params: CancelOperationParams,
  workspacePath = '',
): Promise<CancelOperationResponse> {
  const response = await requestJson<{
    ok: boolean
    operation: EnvironmentOperation
    operation_id: string
    status: OperationStatus
  }>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'cancel',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      operation_id: params.operationId,
      reason: params.reason || 'cancelled via UI',
    }),
  })
  if (!response.operation && !response.operation_id) {
    throw new Error('Cancel operation returned incomplete data')
  }
  return {
    ok: Boolean(response.ok),
    operation: response.operation,
    operation_id: response.operation_id || response.operation?.operation_id || params.operationId,
    status: response.status || response.operation?.status || 'cancelling',
  }
}

export async function ensureDeployment(
  workspaceId: string,
  params: {
    environmentId: string
    connectionId?: string
    consumerType?: ConsumerType
    consumerId?: string
    deploymentName?: string
  },
  workspacePath = '',
): Promise<{ deployment: Deployment; lease: DeploymentLease; reused: boolean }> {
  try {
    const response = await requestJson<DeploymentMutationResponse>('/v1/environments', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        action: 'ensure',
        workspace_id: workspaceId,
        workspace_path: workspacePath,
        environment_id: params.environmentId,
        connection_id: params.connectionId,
        consumer_type: params.consumerType || 'session',
        consumer_id: params.consumerId,
        deployment_name: params.deploymentName,
      }),
    })
    if (response.deployment && response.lease) {
      return {
        deployment: response.deployment,
        lease: response.lease,
        reused: Boolean(response.reused),
      }
    }
  } catch {
    // fallback to /v1/deployments
  }

  const response = await requestJson<DeploymentMutationResponse>('/v1/deployments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'ensure',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      environment_id: params.environmentId,
      connection_id: params.connectionId,
      consumer_type: params.consumerType || 'session',
      consumer_id: params.consumerId,
      deployment_name: params.deploymentName,
    }),
  })
  if (!response.deployment || !response.lease) {
    throw new Error('Deployment launch returned incomplete data')
  }
  return {
    deployment: response.deployment,
    lease: response.lease,
    reused: Boolean(response.reused),
  }
}

export async function startDeployment(
  workspaceId: string,
  deploymentId: string,
  workspacePath = '',
): Promise<Deployment> {
  const response = await requestJson<DeploymentMutationResponse>('/v1/deployments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'start',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      deployment_id: deploymentId,
    }),
  })
  if (!response.deployment) throw new Error('Start deployment returned no deployment')
  return response.deployment
}

export async function stopDeployment(
  workspaceId: string,
  deploymentId: string,
  workspacePath = '',
): Promise<Deployment & { operation?: EnvironmentOperation; operation_id?: string; status?: OperationStatus }> {
  const response = await requestJson<{
    ok: boolean
    deployment?: Deployment
    operation?: EnvironmentOperation
    operation_id?: string
    status?: OperationStatus
  }>('/v1/environments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'stop',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      deployment_id: deploymentId,
    }),
  })

  const opId = response.operation_id || response.operation?.operation_id
  if (!opId) {
    throw new Error(`Stop deployment returned no operation ID for deployment ${deploymentId}`)
  }
  const status = response.status || response.operation?.status
  if (!status) {
    throw new Error(`Stop deployment returned no operation status for deployment ${deploymentId}`)
  }

  if (response.deployment) {
    return {
      ...response.deployment,
      operation: response.operation,
      operation_id: opId,
      status,
    }
  }

  return {
    id: deploymentId,
    account_scope_id: '',
    workspace_id: workspaceId,
    environment_id: '',
    connection_id: '',
    name: `Deployment ${deploymentId.slice(0, 8)}`,
    status: (status === 'cancelling' ? 'stopping' : status) as any || 'stopping',
    health: 'unknown',
    runtime: {},
    lifecycle: { created_at: Date.now() },
    created_at: Date.now(),
    updated_at: Date.now(),
    operation: response.operation,
    operation_id: opId,
  }
}

export async function releaseDeployment(
  workspaceId: string,
  params: { leaseId?: string; deploymentId?: string; reason?: string },
  workspacePath = '',
): Promise<{ lease: DeploymentLease; deployment: Deployment }> {
  const response = await requestJson<DeploymentMutationResponse>('/v1/deployments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'release',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      lease_id: params.leaseId,
      deployment_id: params.deploymentId,
      release_reason: params.reason,
    }),
  })
  if (!response.lease || !response.deployment) {
    throw new Error('Release deployment returned incomplete data')
  }
  return { lease: response.lease, deployment: response.deployment }
}

export async function destroyDeployment(
  workspaceId: string,
  deploymentId: string,
  workspacePath = '',
): Promise<void> {
  await requestJson<DeploymentMutationResponse>('/v1/deployments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      action: 'destroy',
      workspace_id: workspaceId,
      workspace_path: workspacePath,
      deployment_id: deploymentId,
    }),
  })
}
