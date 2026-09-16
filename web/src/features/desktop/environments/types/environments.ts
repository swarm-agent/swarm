export type ConnectionKind = 'local_docker' | 'ssh'

export interface ConnectionCapabilities {
  supports_docker?: boolean
  supports_ssh?: boolean
  supports_direct_mount?: boolean
  supports_port_forward?: boolean
  remote_os?: string
  remote_arch?: string
  engine_version?: string
}

export interface LocalDockerConfig {
  socket_path?: string
  host?: string
}

export interface SSHConfig {
  host: string
  port?: number
  user?: string
  identity_file?: string
  known_hosts_file?: string
}

export interface Connection {
  id: string
  account_scope_id: string
  workspace_id: string
  name: string
  description?: string
  kind: ConnectionKind
  capabilities: ConnectionCapabilities
  local_docker?: LocalDockerConfig
  ssh?: SSHConfig
  created_at: number
  updated_at: number
}

export type EnvironmentMode = 'deployable' | 'attached'
export type EnvironmentRole = 'development' | 'testing' | 'build' | 'custom'
export type ReleaseBehavior = 'none' | 'restart' | 'recreate'

export interface PortMapping {
  container_port: number
  host_port?: number
  protocol?: string
}

export interface ContainerDefinition {
  image: string
  command?: string[]
  args?: string[]
  env_vars?: Record<string, string>
  exposed_ports?: PortMapping[]
  privileged?: boolean
  user?: string
  working_dir?: string
  setup_commands?: string[]
}

export type SourceStrategyKind = 'local_mount' | 'remote_existing_path' | 'sync' | 'registry_image' | 'git_checkout'

export interface SourceStrategy {
  kind: SourceStrategyKind
  local_mount?: {
    host_path?: string
    container_path: string
    read_only?: boolean
  }
  remote_existing_path?: {
    remote_path: string
    container_path: string
    read_only?: boolean
  }
  sync?: {
    source_path?: string
    remote_path?: string
    exclude_patterns?: string[]
    delete_orphaned?: boolean
  }
  registry_image?: {
    image: string
    pull_policy?: string
  }
  git_checkout?: {
    repository_url: string
    ref?: string
    depth?: number
  }
}

export interface AdditionalMount {
  host_path: string
  container_path: string
  read_only?: boolean
}

export interface WorkspaceProvisioning {
  strategy: SourceStrategy
  additional_mounts?: AdditionalMount[]
}

export interface DeploymentPolicy {
  reuse: boolean
  max_instances: number
  release_behavior: ReleaseBehavior
  idle_timeout_seconds?: number
}

export interface HealthCheck {
  test?: string[]
  http_path?: string
  http_port?: number
  interval_seconds?: number
  timeout_seconds?: number
  retries?: number
  start_period_seconds?: number
}

export interface ResourceRequirements {
  cpu_limit?: string
  memory_limit?: string
  gpu_required?: boolean
  gpu_count?: number
}

export interface Environment {
  id: string
  account_scope_id: string
  workspace_id: string
  name: string
  description?: string
  mode: EnvironmentMode
  role: EnvironmentRole
  preferred_connection_id?: string
  container: ContainerDefinition
  provisioning: WorkspaceProvisioning
  deployment_policy: DeploymentPolicy
  health_check?: HealthCheck
  resources?: ResourceRequirements
  labels?: Record<string, string>
  created_at: number
  updated_at: number
}

export interface WorkspaceSettings {
  workspace_id: string
  account_scope_id: string
  default_test_environment_id?: string
  default_connection_id?: string
  updated_at?: number
}

export type DeploymentStatus =
  | 'pending'
  | 'provisioning'
  | 'starting'
  | 'running'
  | 'ready'
  | 'busy'
  | 'stopping'
  | 'stopped'
  | 'failed'
  | 'terminated'

export type HealthStatus = 'unknown' | 'starting' | 'healthy' | 'unhealthy'

export interface AssignedPort {
  container_port: number
  host_port: number
  protocol: string
  endpoint_url?: string
}

export interface RuntimeMetadata {
  container_id?: string
  provider_resource_id?: string
  endpoint?: string
  assigned_ports?: AssignedPort[]
  remote_workspace_path?: string
  runtime_ip?: string
  engine_version?: string
}

export interface DeploymentLifecycle {
  created_at: number
  started_at?: number
  ready_at?: number
  stopped_at?: number
  terminated_at?: number
  last_active_at?: number
}

export interface Deployment {
  id: string
  account_scope_id: string
  workspace_id: string
  environment_id: string
  connection_id: string
  name: string
  status: DeploymentStatus
  health: HealthStatus
  error_message?: string
  runtime: RuntimeMetadata
  lifecycle: DeploymentLifecycle
  created_at: number
  updated_at: number
}

export type ConsumerType = 'session' | 'test_run' | 'worker' | 'custom'

export interface DeploymentLease {
  id: string
  account_scope_id: string
  workspace_id: string
  deployment_id: string
  environment_id: string
  consumer_type: ConsumerType
  consumer_id: string
  consumer_metadata?: Record<string, string>
  acquired_at: number
  released_at?: number
  expires_at?: number
  active: boolean
  release_reason?: string
}

export interface ConnectionCheckResult {
  ok: boolean
  healthy: boolean
  error?: string
  diagnostics?: string
  capabilities?: ConnectionCapabilities
}
