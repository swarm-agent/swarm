import { useState } from 'react'
import {
  Activity,
  Clock,
  ExternalLink,
  Globe,
  HardDrive,
  Key,
  Loader2,
  Play,
  Rocket,
  Square,
  Trash2,
  Unlock,
} from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import type {
  Deployment,
  DeploymentLease,
  DeploymentStatus,
  Environment,
  HealthStatus,
} from '../types/environments'

interface DeploymentsViewProps {
  workspaceId: string
  workspacePath?: string
  deployments: Deployment[]
  activeLeases: Record<string, DeploymentLease>
  environments: Environment[]
  loading?: boolean
  onRefresh: () => void
  onStartDeployment: (id: string) => Promise<void>
  onStopDeployment: (id: string) => Promise<void>
  onReleaseDeployment: (params: { deploymentId: string; leaseId?: string; reason?: string }) => Promise<void>
  onDestroyDeployment: (id: string) => Promise<void>
  onQuickLaunch?: (envId: string) => Promise<void>
}

export function DeploymentsView({
  workspaceId: _workspaceId,
  workspacePath: _workspacePath = '',
  deployments,
  activeLeases,
  environments,
  loading = false,
  onRefresh: _onRefresh,
  onStartDeployment,
  onStopDeployment,
  onReleaseDeployment,
  onDestroyDeployment,
  onQuickLaunch,
}: DeploymentsViewProps) {
  const [actingId, setActingId] = useState<string | null>(null)
  const [selectedEnvForLaunch, setSelectedEnvForLaunch] = useState<string>(environments[0]?.id ?? '')
  const [launching, setLaunching] = useState(false)

  const handleStart = async (depId: string) => {
    setActingId(depId)
    try {
      await onStartDeployment(depId)
    } finally {
      setActingId(null)
    }
  }

  const handleStop = async (depId: string) => {
    setActingId(depId)
    try {
      await onStopDeployment(depId)
    } finally {
      setActingId(null)
    }
  }

  const handleRelease = async (depId: string, leaseId?: string) => {
    setActingId(depId)
    try {
      await onReleaseDeployment({ deploymentId: depId, leaseId, reason: 'released by user' })
    } finally {
      setActingId(null)
    }
  }

  const handleDestroy = async (depId: string) => {
    if (!confirm('Are you sure you want to destroy this deployment container?')) return
    setActingId(depId)
    try {
      await onDestroyDeployment(depId)
    } finally {
      setActingId(null)
    }
  }

  const handleLaunch = async () => {
    if (!selectedEnvForLaunch || !onQuickLaunch) return
    setLaunching(true)
    try {
      await onQuickLaunch(selectedEnvForLaunch)
    } finally {
      setLaunching(false)
    }
  }

  const getEnvName = (envId: string) => {
    const env = environments.find((e) => e.id === envId)
    return env ? env.name : envId
  }

  const statusTone = (status: DeploymentStatus): 'live' | 'warning' | 'danger' | 'neutral' => {
    switch (status) {
      case 'running':
      case 'ready':
        return 'live'
      case 'provisioning':
      case 'starting':
      case 'busy':
        return 'warning'
      case 'failed':
        return 'danger'
      default:
        return 'neutral'
    }
  }

  const healthTone = (health: HealthStatus): 'live' | 'warning' | 'danger' | 'neutral' => {
    switch (health) {
      case 'healthy':
        return 'live'
      case 'starting':
        return 'warning'
      case 'unhealthy':
        return 'danger'
      default:
        return 'neutral'
    }
  }

  return (
    <div className="space-y-6" data-testid="deployments-view">
      {/* Section Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-[var(--app-text)] flex items-center gap-2">
            <Activity size={16} className="text-[var(--app-primary)]" />
            Active &amp; Historical Deployments
            <span className="text-xs text-[var(--app-text-subtle)] font-normal">({deployments.length})</span>
          </h2>
          <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
            Running container instances, leased sandboxes, and runtime port allocations.
          </p>
        </div>

        {environments.length > 0 && onQuickLaunch && (
          <div className="flex items-center gap-2">
            <select
              value={selectedEnvForLaunch}
              onChange={(e) => setSelectedEnvForLaunch(e.target.value)}
              className="h-9 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] focus:outline-none focus:ring-1 focus:ring-[var(--app-primary)]"
              data-testid="quick-launch-env-select"
            >
              {environments.map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
            </select>
            <Button
              variant="primary"
              size="sm"
              onClick={handleLaunch}
              disabled={launching || !selectedEnvForLaunch}
              className="gap-1.5"
              data-testid="quick-launch-btn"
            >
              {launching ? (
                <>
                  <Loader2 size={13} className="animate-spin" />
                  Launching…
                </>
              ) : (
                <>
                  <Rocket size={13} />
                  Launch Instance
                </>
              )}
            </Button>
          </div>
        )}
      </div>

      {/* List */}
      {loading ? (
        <div className="flex items-center justify-center p-8 text-xs text-[var(--app-text-muted)] gap-2">
          <Loader2 size={16} className="animate-spin text-[var(--app-primary)]" />
          Loading deployments…
        </div>
      ) : deployments.length === 0 ? (
        <div
          className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)]/40 p-8 text-center"
          data-testid="empty-deployments"
        >
          <Rocket size={28} className="mx-auto text-[var(--app-text-subtle)] opacity-60" />
          <h3 className="mt-2 text-sm font-semibold text-[var(--app-text)]">No deployments found</h3>
          <p className="mt-1 text-xs text-[var(--app-text-muted)] max-w-sm mx-auto">
            Launch an environment instance or run an agent test session to instantiate a deployment.
          </p>
        </div>
      ) : (
        <div className="grid gap-4" data-testid="deployments-list">
          {deployments.map((dep) => {
            const lease = activeLeases[dep.id]
            const isActing = actingId === dep.id
            const isRunning = dep.status === 'running' || dep.status === 'ready' || dep.status === 'busy'
            const isStopped = dep.status === 'stopped'
            const hasActiveLease = lease && lease.active

            return (
              <Card
                key={dep.id}
                className="p-4 sm:p-5 space-y-4 hover:border-[var(--app-border-strong)] transition-all"
                data-testid={`deployment-card-${dep.id}`}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="flex items-start gap-3 min-w-0">
                    <div className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
                      <HardDrive size={18} />
                    </div>
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-sm font-semibold text-[var(--app-text)] truncate">
                          {dep.name || `Deployment ${dep.id.slice(0, 8)}`}
                        </h3>

                        {/* Status Badge */}
                        <Badge tone={statusTone(dep.status)} className="text-[10.5px] uppercase tracking-wider" data-testid="deployment-status-badge">
                          {dep.status}
                        </Badge>

                        {/* Health Badge */}
                        <Badge tone={healthTone(dep.health)} className="text-[10.5px] capitalize" data-testid="deployment-health-badge">
                          {dep.health}
                        </Badge>

                        {/* Lease Status Badge */}
                        {hasActiveLease ? (
                          <Badge tone="live" className="text-[10.5px] gap-1" data-testid="deployment-lease-badge">
                            <Key size={10} />
                            Leased ({lease.consumer_type})
                          </Badge>
                        ) : (
                          <Badge tone="neutral" className="text-[10.5px]">
                            Available
                          </Badge>
                        )}
                      </div>

                      <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
                        Environment: <span className="text-[var(--app-text)] font-medium">{getEnvName(dep.environment_id)}</span>
                        {dep.runtime?.container_id && (
                          <span className="ml-2 font-mono text-[11px] text-[var(--app-text-subtle)]">
                            ID: {dep.runtime.container_id.slice(0, 12)}
                          </span>
                        )}
                      </p>
                    </div>
                  </div>

                  {/* Lifecycle controls */}
                  <div className="flex items-center gap-1.5 shrink-0">
                    {hasActiveLease && (
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => handleRelease(dep.id, lease.id)}
                        disabled={isActing}
                        className="text-xs gap-1"
                        data-testid={`release-lease-btn-${dep.id}`}
                      >
                        {isActing ? (
                          <Loader2 size={13} className="animate-spin" />
                        ) : (
                          <Unlock size={13} />
                        )}
                        Release Lease
                      </Button>
                    )}

                    {isStopped && (
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => handleStart(dep.id)}
                        disabled={isActing}
                        className="text-xs gap-1"
                        data-testid={`start-dep-btn-${dep.id}`}
                      >
                        <Play size={13} />
                        Start
                      </Button>
                    )}

                    {isRunning && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleStop(dep.id)}
                        disabled={isActing}
                        className="text-xs gap-1"
                        data-testid={`stop-dep-btn-${dep.id}`}
                      >
                        <Square size={13} />
                        Stop
                      </Button>
                    )}

                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleDestroy(dep.id)}
                      disabled={isActing}
                      className="text-xs px-2 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg,rgba(239,68,68,0.1))]"
                      title="Destroy container instance"
                      data-testid={`destroy-dep-btn-${dep.id}`}
                    >
                      <Trash2 size={13} />
                    </Button>
                  </div>
                </div>

                {/* Runtime Metadata & Endpoints */}
                <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-3 text-xs bg-[var(--app-surface-subtle)]/60 rounded-xl p-3.5">
                  {/* Primary Endpoint */}
                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Primary Endpoint:</span>
                    {dep.runtime?.endpoint ? (
                      <a
                        href={dep.runtime.endpoint}
                        target="_blank"
                        rel="noreferrer"
                        className="font-mono text-[var(--app-primary)] hover:underline inline-flex items-center gap-1 font-medium"
                        data-testid="deployment-endpoint-link"
                      >
                        <Globe size={11} />
                        {dep.runtime.endpoint}
                        <ExternalLink size={10} />
                      </a>
                    ) : (
                      <span className="text-[var(--app-text-muted)] font-mono">None exposed</span>
                    )}
                  </div>

                  {/* Ports */}
                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Assigned Ports:</span>
                    {dep.runtime?.assigned_ports?.length ? (
                      <div className="flex flex-wrap gap-1 mt-0.5" data-testid="deployment-assigned-ports">
                        {dep.runtime.assigned_ports.map((p, idx) => (
                          <span
                            key={idx}
                            className="font-mono text-[11px] rounded bg-[var(--app-surface)] px-1.5 py-0.2 border border-[var(--app-border)]"
                          >
                            {p.container_port} → {p.host_port} ({p.protocol})
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="text-[var(--app-text-muted)] font-mono">None</span>
                    )}
                  </div>

                  {/* Container Runtime ID */}
                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Container ID:</span>
                    <span className="font-mono text-[var(--app-text)] truncate block" title={dep.runtime?.container_id}>
                      {dep.runtime?.container_id || 'Not provisioned'}
                    </span>
                  </div>

                  {/* Lease / Consumer ownership details */}
                  {hasActiveLease && (
                    <div className="col-span-full pt-2 border-t border-[var(--app-border)]/50 flex flex-wrap items-center justify-between gap-2 text-[11.5px]" data-testid="deployment-lease-details">
                      <div className="flex items-center gap-2">
                        <span className="text-[var(--app-text-subtle)]">Consumer:</span>
                        <span className="rounded bg-[var(--app-surface)] px-2 py-0.5 border border-[var(--app-border)] font-medium text-[var(--app-text)]">
                          {lease.consumer_type}: {lease.consumer_id}
                        </span>
                      </div>
                      <div className="text-[var(--app-text-subtle)] flex items-center gap-1">
                        <Clock size={11} />
                        <span>Acquired {new Date(lease.acquired_at).toLocaleTimeString()}</span>
                        {lease.expires_at ? (
                          <span>· Expires {new Date(lease.expires_at).toLocaleTimeString()}</span>
                        ) : (
                          <span>· No expiry</span>
                        )}
                      </div>
                    </div>
                  )}

                  {/* Error Message if failed */}
                  {dep.error_message && (
                    <div className="col-span-full rounded-lg border border-[var(--app-danger-border,rgba(239,68,68,0.3))] bg-[var(--app-danger-bg,rgba(239,68,68,0.1))] p-2.5 text-[11.5px] text-[var(--app-danger)]">
                      {dep.error_message}
                    </div>
                  )}
                </div>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}
