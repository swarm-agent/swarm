import { useState } from 'react'
import {
  Activity,
  Calendar,
  CheckCircle2,
  Clock,
  ExternalLink,
  Globe,
  HardDrive,
  Key,
  Loader2,
  Play,
  RefreshCw,
  Rocket,
  Square,
  Terminal,
  Trash2,
  Unlock,
  XCircle,
} from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import type {
  Deployment,
  DeploymentLease,
  DeploymentStatus,
  Environment,
  EnvironmentOperation,
  EnvironmentSummary,
  HealthStatus,
  HistoryFilter,
  OperationHistoryPage,
  OperationStatus,
} from '../types/environments'

export interface DeploymentsViewProps {
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
  summary?: EnvironmentSummary
  operations?: EnvironmentOperation[]
  historyPage?: OperationHistoryPage
  historyFilter?: HistoryFilter
  onSetHistoryFilter?: (filter: Partial<HistoryFilter>) => void
  realtimeStatus?: 'connected' | 'connecting' | 'stale' | 'disconnected' | 'error'
  stale?: boolean
  lastObservedAt?: number
  lastReceipt?: { operationId: string; action: string; status: OperationStatus; timestamp: number }
  onCancelOperation?: (operationId: string, reason?: string) => Promise<void>
}

export function DeploymentsView({
  workspaceId: _workspaceId,
  workspacePath: _workspacePath = '',
  deployments,
  activeLeases,
  environments,
  loading = false,
  onRefresh,
  onStartDeployment,
  onStopDeployment,
  onReleaseDeployment,
  onDestroyDeployment,
  onQuickLaunch,
  summary,
  operations = [],
  historyPage,
  historyFilter,
  onSetHistoryFilter,
  realtimeStatus = 'connected',
  stale = false,
  lastObservedAt,
  lastReceipt,
  onCancelOperation,
}: DeploymentsViewProps) {
  const [actingId, setActingId] = useState<string | null>(null)
  const [selectedEnvForLaunch, setSelectedEnvForLaunch] = useState<string>(environments[0]?.id ?? '')
  const [launching, setLaunching] = useState(false)
  const [cancellingOpIds, setCancellingOpIds] = useState<Set<string>>(new Set())
  const [stoppingDepIds, setStoppingDepIds] = useState<Set<string>>(new Set())

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
    setStoppingDepIds((prev) => new Set(prev).add(depId))
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

  const handleCancelOp = async (opId: string) => {
    if (!onCancelOperation) return
    setActingId(opId)
    setCancellingOpIds((prev) => new Set(prev).add(opId))
    try {
      await onCancelOperation(opId, 'cancelled via environments view')
    } finally {
      setActingId(null)
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

  const operationStatusTone = (status: OperationStatus): 'live' | 'warning' | 'danger' | 'neutral' => {
    switch (status) {
      case 'running':
        return 'live'
      case 'queued':
      case 'cancelling':
        return 'warning'
      case 'failed':
      case 'cleanup_failed':
      case 'unknown':
      case 'timed_out':
        return 'danger'
      default:
        return 'neutral'
    }
  }

  return (
    <div className="space-y-6" data-testid="deployments-view">
      {/* 1. Authoritative Summary Counts & Realtime Connectivity Section */}
      <div className="space-y-3" data-testid="environments-summary-section">
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
          <div className="flex items-center gap-3">
            <div className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
              <Activity size={18} />
            </div>
            <div>
              <div className="flex items-center gap-2">
                <h2 className="text-sm font-semibold text-[var(--app-text)]">
                  Environment Operations &amp; Counts
                </h2>
                {realtimeStatus === 'connected' && !stale ? (
                  <Badge tone="live" className="text-[10.5px] gap-1" data-testid="realtime-status-badge">
                    <span className="size-1.5 rounded-full bg-emerald-500 animate-pulse" />
                    Live Realtime
                  </Badge>
                ) : (
                  <Badge tone="warning" className="text-[10.5px] gap-1" data-testid="realtime-status-badge">
                    <span className="size-1.5 rounded-full bg-amber-500" />
                    {realtimeStatus === 'connecting' ? 'Connecting…' : 'Stale / Disconnected'}
                  </Badge>
                )}
              </div>
              <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
                Authoritative backend metrics. {lastObservedAt ? `Observed ${new Date(lastObservedAt).toLocaleTimeString()}` : ''}
                {summary ? ` · Rev #${summary.revision}` : ''}
              </p>
            </div>
          </div>
          <Button
            variant="outline"
            size="sm"
            onClick={onRefresh}
            disabled={loading}
            className="text-xs gap-1.5"
            data-testid="refresh-summary-btn"
          >
            <RefreshCw size={12} className={loading ? 'animate-spin text-[var(--app-primary)]' : ''} />
            Refresh
          </Button>
        </div>

        {/* Compact Metrics Grid */}
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2.5 text-xs" data-testid="authoritative-summary-counts">
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">Active Deployments</div>
            <div className="text-xl font-bold text-[var(--app-text)]" data-testid="count-active-deployments">
              {summary?.active_deployments ?? deployments.filter((d) => d.status === 'running' || d.status === 'ready').length}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">Container sandboxes</div>
          </div>

          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">Executing Commands</div>
            <div className="text-xl font-bold text-[var(--app-primary)]" data-testid="count-executing-commands">
              {summary?.running_exec_ops ?? 0}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">Active command runs</div>
          </div>

          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">In-Flight Operations</div>
            <div className="text-xl font-bold text-[var(--app-text)]" data-testid="count-inflight-ops">
              {(summary?.running_ops ?? 0) + (summary?.queued_ops ?? 0) + (summary?.cancelling_ops ?? 0)}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">
              {summary?.running_ops ?? 0} run · {summary?.queued_ops ?? 0} q · {summary?.cancelling_ops ?? 0} cancel
            </div>
          </div>

          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">Attention Required</div>
            <div
              className={`text-xl font-bold ${(summary?.failed_ops || summary?.cleanup_failed_ops || summary?.unknown_ops) ? 'text-[var(--app-danger)]' : 'text-[var(--app-text)]'}`}
              data-testid="count-attention-ops"
            >
              {(summary?.failed_ops ?? 0) + (summary?.cleanup_failed_ops ?? 0) + (summary?.unknown_ops ?? 0)}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">
              {summary?.failed_ops ?? 0} fail · {summary?.cleanup_failed_ops ?? 0} cleanup · {summary?.unknown_ops ?? 0} unk
            </div>
          </div>

          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">Daily Succeeded</div>
            <div className="text-xl font-bold text-emerald-600 dark:text-emerald-400" data-testid="count-daily-succeeded">
              {summary?.succeeded_ops ?? 0}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">Terminal successful</div>
          </div>

          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1">
            <div className="text-[11px] text-[var(--app-text-subtle)] font-medium">Cancelled / Timed Out</div>
            <div className="text-xl font-bold text-[var(--app-text-muted)]" data-testid="count-daily-cancelled-timeout">
              {(summary?.cancelled_ops ?? 0) + (summary?.timed_out_ops ?? 0)}
            </div>
            <div className="text-[10px] text-[var(--app-text-muted)]">
              {summary?.cancelled_ops ?? 0} cancel · {summary?.timed_out_ops ?? 0} timeout
            </div>
          </div>
        </div>

        {/* Durable Receipt Notification Banner */}
        {lastReceipt && (
          <div
            className="flex items-center justify-between rounded-xl border border-[var(--app-primary-border,rgba(59,130,246,0.3))] bg-[var(--app-primary-soft)] px-3.5 py-2 text-xs text-[var(--app-primary)]"
            data-testid="operation-receipt-banner"
          >
            <div className="flex items-center gap-2">
              <CheckCircle2 size={14} />
              <span>
                <strong>Durable Receipt:</strong> Action <code>{lastReceipt.action}</code> ({lastReceipt.operationId}) recorded with status <code>{lastReceipt.status}</code> at {new Date(lastReceipt.timestamp).toLocaleTimeString()}.
              </span>
            </div>
          </div>
        )}
      </div>

      {/* 2. Deployments Section Header */}
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

      {/* 3. Deployments List */}
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
            const isStopping = dep.status === 'stopping' || stoppingDepIds.has(dep.id)
            const hasActiveLease = lease && lease.active
            const depOps = operations.filter((op) => op.deployment_id === dep.id)

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
                          {isStopping ? 'stopping' : dep.status}
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
                        disabled={isActing || isStopping}
                        className="text-xs gap-1"
                        data-testid={`stop-dep-btn-${dep.id}`}
                      >
                        {isStopping ? (
                          <>
                            <Loader2 size={13} className="animate-spin" />
                            Stopping…
                          </>
                        ) : (
                          <>
                            <Square size={13} />
                            Stop
                          </>
                        )}
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

                  {/* Nested Supervised Operations */}
                  {depOps.length > 0 && (
                    <div className="col-span-full pt-2.5 border-t border-[var(--app-border)]/60 space-y-2" data-testid={`deployment-nested-ops-${dep.id}`}>
                      <div className="flex items-center justify-between text-xs">
                        <span className="font-semibold text-[var(--app-text)] flex items-center gap-1.5">
                          <Terminal size={12} className="text-[var(--app-primary)]" />
                          Supervised Operations ({depOps.length})
                        </span>
                      </div>

                      <div className="space-y-1.5">
                        {depOps.map((op) => {
                          const isActive = op.status === 'queued' || op.status === 'running' || op.status === 'cancelling'
                          const isCancelling = op.status === 'cancelling' || cancellingOpIds.has(op.operation_id)

                          return (
                            <div
                              key={op.operation_id}
                              className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-[var(--app-surface)] p-2 border border-[var(--app-border)]/70 text-xs"
                              data-testid={`operation-row-${op.operation_id}`}
                            >
                              <div className="flex items-center gap-2 min-w-0">
                                <span className="font-mono font-medium text-[var(--app-text)] uppercase text-[10.5px] px-1.5 py-0.5 rounded bg-[var(--app-surface-subtle)] border border-[var(--app-border)]">
                                  {op.action}
                                </span>
                                <span className="font-mono text-[10.5px] text-[var(--app-text-subtle)] truncate" title={op.operation_id}>
                                  #{op.operation_id.slice(0, 8)}
                                </span>
                                <Badge tone={operationStatusTone(op.status)} className="text-[10px] uppercase">
                                  {isCancelling ? 'cancelling' : op.status}
                                </Badge>
                                {op.activity?.phase && (
                                  <span className="text-[11px] text-[var(--app-text-muted)] truncate max-w-xs">
                                    · {op.activity.phase}
                                  </span>
                                )}
                              </div>

                              <div className="flex items-center gap-2">
                                {typeof op.activity?.progress_pct === 'number' && op.activity.progress_pct > 0 && (
                                  <div className="flex items-center gap-1 text-[10px] text-[var(--app-text-subtle)]">
                                    <div className="w-10 h-1.5 rounded-full bg-[var(--app-surface-hover)] overflow-hidden">
                                      <div className="h-full bg-[var(--app-primary)]" style={{ width: `${op.activity.progress_pct}%` }} />
                                    </div>
                                    <span>{op.activity.progress_pct}%</span>
                                  </div>
                                )}

                                <div className="text-[10px] text-[var(--app-text-subtle)] flex items-center gap-1">
                                  <Clock size={10} />
                                  <span>Observed {new Date(op.activity?.last_observed_at || op.observed_at || op.started_at || op.created_at).toLocaleTimeString()}</span>
                                </div>

                                {isActive && onCancelOperation && (
                                  <Button
                                    variant="outline"
                                    size="sm"
                                    onClick={() => handleCancelOp(op.operation_id)}
                                    disabled={isCancelling || actingId === op.operation_id}
                                    className="h-6 px-1.5 text-[10.5px] text-[var(--app-danger)] border-[var(--app-danger-border,rgba(239,68,68,0.3))] hover:bg-[var(--app-danger-bg,rgba(239,68,68,0.1))]"
                                    data-testid={`cancel-op-btn-${op.operation_id}`}
                                  >
                                    {isCancelling ? (
                                      <>
                                        <Loader2 size={10} className="animate-spin" />
                                        Cancelling…
                                      </>
                                    ) : (
                                      <>
                                        <XCircle size={10} />
                                        Cancel
                                      </>
                                    )}
                                  </Button>
                                )}
                              </div>

                              {op.result?.error_message && (
                                <div className="w-full text-[11px] text-[var(--app-danger)] font-mono">
                                  {op.result.error_message}
                                </div>
                              )}
                            </div>
                          )
                        })}
                      </div>
                    </div>
                  )}
                </div>
              </Card>
            )
          })}
        </div>
      )}

      {/* 4. Daily History & Date Range / Timezone Section */}
      <div className="space-y-4 pt-6 border-t border-[var(--app-border)]" data-testid="environments-history-section">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-semibold text-[var(--app-text)] flex items-center gap-2">
              <Calendar size={16} className="text-[var(--app-primary)]" />
              Daily History &amp; Operations Log
            </h3>
            <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
              Authoritative terminal totals and execution records in your chosen timezone.
            </p>
          </div>

          {/* Filter controls */}
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <div className="flex items-center gap-1 bg-[var(--app-surface)] px-2.5 py-1 rounded-xl border border-[var(--app-border)]">
              <Globe size={12} className="text-[var(--app-text-subtle)]" />
              <span className="text-[11px] text-[var(--app-text-subtle)]">TZ:</span>
              <select
                value={historyFilter?.timezone ?? 'UTC'}
                onChange={(e) => onSetHistoryFilter?.({ timezone: e.target.value })}
                className="bg-transparent text-xs text-[var(--app-text)] focus:outline-none"
                data-testid="history-timezone-select"
              >
                <option value="UTC">UTC</option>
                {Intl.DateTimeFormat().resolvedOptions().timeZone !== 'UTC' && (
                  <option value={Intl.DateTimeFormat().resolvedOptions().timeZone}>
                    Local ({Intl.DateTimeFormat().resolvedOptions().timeZone})
                  </option>
                )}
                <option value="America/New_York">America/New_York</option>
                <option value="America/Chicago">America/Chicago</option>
                <option value="America/Denver">America/Denver</option>
                <option value="America/Los_Angeles">America/Los_Angeles</option>
                <option value="Europe/London">Europe/London</option>
                <option value="Europe/Paris">Europe/Paris</option>
                <option value="Asia/Tokyo">Asia/Tokyo</option>
              </select>
            </div>

            <input
              type="date"
              value={historyFilter?.startDate ?? ''}
              onChange={(e) => onSetHistoryFilter?.({ startDate: e.target.value })}
              className="h-8 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs text-[var(--app-text)] focus:outline-none"
              placeholder="Start Date"
              data-testid="history-start-date"
            />

            <input
              type="date"
              value={historyFilter?.endDate ?? ''}
              onChange={(e) => onSetHistoryFilter?.({ endDate: e.target.value })}
              className="h-8 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs text-[var(--app-text)] focus:outline-none"
              placeholder="End Date"
              data-testid="history-end-date"
            />

            <select
              value={historyFilter?.status ?? ''}
              onChange={(e) => onSetHistoryFilter?.({ status: (e.target.value || undefined) as OperationStatus | undefined })}
              className="h-8 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-xs text-[var(--app-text)] focus:outline-none"
              data-testid="history-status-select"
            >
              <option value="">All Statuses</option>
              <option value="succeeded">Succeeded</option>
              <option value="failed">Failed</option>
              <option value="cancelled">Cancelled</option>
              <option value="timed_out">Timed Out</option>
              <option value="cleanup_failed">Cleanup Failed</option>
              <option value="unknown">Unknown</option>
              <option value="running">Running</option>
              <option value="queued">Queued</option>
            </select>
          </div>
        </div>

        {/* Daily Totals Cards */}
        {historyPage?.daily_totals && historyPage.daily_totals.length > 0 && (
          <div className="space-y-2" data-testid="daily-totals-container">
            <div className="text-xs font-medium text-[var(--app-text-muted)]">
              Daily Terminal Totals ({historyFilter?.timezone ?? 'UTC'})
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2.5" data-testid="daily-totals-cards">
              {historyPage.daily_totals.map((dt) => (
                <div
                  key={dt.date}
                  className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 space-y-1.5 text-xs"
                  data-testid={`daily-total-card-${dt.date}`}
                >
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-[var(--app-text)]">{dt.date}</span>
                    <span className="rounded-full bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] font-medium text-[var(--app-text-subtle)]">
                      {dt.total_ops} Total
                    </span>
                  </div>
                  <div className="grid grid-cols-3 gap-1.5 text-[11px] pt-1 border-t border-[var(--app-border)]/50">
                    <div className="text-emerald-600 dark:text-emerald-400">
                      <span className="text-[10px] text-[var(--app-text-subtle)] block">Succeeded</span>
                      {dt.succeeded}
                    </div>
                    <div className="text-[var(--app-danger)]">
                      <span className="text-[10px] text-[var(--app-text-subtle)] block">Failed</span>
                      {dt.failed + dt.cleanup_failed}
                    </div>
                    <div className="text-[var(--app-text-muted)]">
                      <span className="text-[10px] text-[var(--app-text-subtle)] block">Cancelled/TO</span>
                      {dt.cancelled + dt.timed_out}
                    </div>
                  </div>
                  <div className="text-[10.5px] text-[var(--app-text-subtle)] flex items-center justify-between pt-1">
                    <span>Commands: {dt.exec_ops}</span>
                    <span>Deploys: {dt.deploy_ops}</span>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Historical Operations List */}
        <div className="space-y-2">
          <div className="text-xs font-medium text-[var(--app-text-muted)]">
            Historical Operations ({historyPage?.operations?.length ?? 0} loaded)
          </div>

          {(!historyPage?.operations || historyPage.operations.length === 0) ? (
            <div
              className="rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)]/30 p-6 text-center text-xs text-[var(--app-text-muted)]"
              data-testid="empty-history"
            >
              No operations recorded for the selected date range and filters.
            </div>
          ) : (
            <div className="overflow-x-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]" data-testid="history-operations-table">
              <table className="w-full text-left text-xs">
                <thead>
                  <tr className="border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)]/50 text-[11px] text-[var(--app-text-subtle)]">
                    <th className="p-3 font-medium">Operation ID</th>
                    <th className="p-3 font-medium">Action</th>
                    <th className="p-3 font-medium">Status</th>
                    <th className="p-3 font-medium">Target</th>
                    <th className="p-3 font-medium">Actor / Session</th>
                    <th className="p-3 font-medium">Observed</th>
                    <th className="p-3 font-medium">Actions</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[var(--app-border)]">
                  {historyPage.operations.map((op) => (
                    <tr key={op.operation_id} className="hover:bg-[var(--app-surface-subtle)]/40 transition-colors">
                      <td className="p-3 font-mono text-[11px] text-[var(--app-text)] font-medium">
                        #{op.operation_id.slice(0, 8)}
                      </td>
                      <td className="p-3 uppercase font-medium text-[11px]">
                        {op.action}
                      </td>
                      <td className="p-3">
                        <Badge tone={operationStatusTone(op.status)} className="text-[10px] uppercase">
                          {op.status}
                        </Badge>
                      </td>
                      <td className="p-3 text-[11px] text-[var(--app-text-subtle)]">
                        {op.deployment_id ? `dep:${op.deployment_id.slice(0, 8)}` : (op.environment_id || '—')}
                      </td>
                      <td className="p-3 text-[11px] text-[var(--app-text-muted)]">
                        {op.attribution?.actor || op.attribution?.session_id ? `${op.attribution?.actor || 'ai'} (${(op.attribution?.session_id || '').slice(0, 8)})` : '—'}
                      </td>
                      <td className="p-3 text-[11px] text-[var(--app-text-subtle)] whitespace-nowrap">
                        {new Date(op.observed_at || op.started_at || op.created_at).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' })}
                      </td>
                      <td className="p-3">
                        {(op.status === 'queued' || op.status === 'running' || op.status === 'cancelling') && onCancelOperation && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => handleCancelOp(op.operation_id)}
                            disabled={op.status === 'cancelling'}
                            className="h-6 px-1.5 text-[10.5px] text-[var(--app-danger)]"
                            data-testid={`history-cancel-btn-${op.operation_id}`}
                          >
                            Cancel
                          </Button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {historyPage?.has_more && historyPage.next_cursor && (
            <div className="flex justify-center pt-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => onSetHistoryFilter?.({ cursor: historyPage.next_cursor })}
                className="text-xs"
                data-testid="history-load-more-btn"
              >
                Load More History
              </Button>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
