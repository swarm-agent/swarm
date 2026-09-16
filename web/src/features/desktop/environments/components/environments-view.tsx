import { useState } from 'react'
import {
  Box,
  Check,
  Layers,
  Loader2,
  Pencil,
  Play,
  Plus,
  Sparkles,
  Trash2,
} from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import type { Connection, Environment, WorkspaceSettings } from '../types/environments'
import { EnvironmentSetupDialog } from './environment-setup-dialog'

interface EnvironmentsViewProps {
  workspaceId: string
  workspacePath?: string
  environments: Environment[]
  connections: Connection[]
  settings: WorkspaceSettings
  loading?: boolean
  onRefresh: () => void
  onSaveEnvironment: (data: any) => Promise<void>
  onDeleteEnvironment: (id: string) => Promise<void>
  onSetDefaultTestEnvironment: (id: string) => Promise<void>
  onDeployEnvironment: (envId: string) => Promise<void>
}

export function EnvironmentsView({
  workspaceId,
  workspacePath = '',
  environments,
  connections,
  settings,
  loading = false,
  onRefresh,
  onSaveEnvironment,
  onDeleteEnvironment,
  onSetDefaultTestEnvironment,
  onDeployEnvironment,
}: EnvironmentsViewProps) {
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingEnv, setEditingEnv] = useState<Environment | null>(null)
  const [settingDefaultId, setSettingDefaultId] = useState<string | null>(null)
  const [deployingId, setDeployingId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)

  const defaultTestEnvId = settings.default_test_environment_id

  const handleOpenCreate = () => {
    setEditingEnv(null)
    setDialogOpen(true)
  }

  const handleOpenEdit = (env: Environment) => {
    setEditingEnv(env)
    setDialogOpen(true)
  }

  const handleSetDefault = async (envId: string) => {
    setSettingDefaultId(envId)
    try {
      await onSetDefaultTestEnvironment(envId)
    } finally {
      setSettingDefaultId(null)
    }
  }

  const handleDeploy = async (envId: string) => {
    setDeployingId(envId)
    try {
      await onDeployEnvironment(envId)
    } finally {
      setDeployingId(null)
    }
  }

  const handleDelete = async (envId: string) => {
    if (!confirm('Are you sure you want to delete this environment definition?')) return
    setDeletingId(envId)
    try {
      await onDeleteEnvironment(envId)
    } finally {
      setDeletingId(null)
    }
  }

  const getConnectionName = (connId?: string) => {
    if (!connId) return 'Workspace Default'
    const found = connections.find((c) => c.id === connId)
    return found ? `${found.name} (${found.kind === 'local_docker' ? 'Local' : 'SSH'})` : connId
  }

  return (
    <div className="space-y-6" data-testid="environments-view">
      {/* Section Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-[var(--app-text)] flex items-center gap-2">
            <Box size={16} className="text-[var(--app-primary)]" />
            Reusable Environment Definitions
            <span className="text-xs text-[var(--app-text-subtle)] font-normal">({environments.length})</span>
          </h2>
          <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
            Declarative specifications for testbenches, sandboxes, and task runtimes.
          </p>
        </div>

        <Button
          variant="primary"
          size="sm"
          onClick={handleOpenCreate}
          className="gap-1.5"
          data-testid="create-environment-btn"
        >
          <Plus size={14} />
          New Environment
        </Button>
      </div>

      {/* List */}
      {loading ? (
        <div className="flex items-center justify-center p-8 text-xs text-[var(--app-text-muted)] gap-2">
          <Loader2 size={16} className="animate-spin text-[var(--app-primary)]" />
          Loading environments…
        </div>
      ) : environments.length === 0 ? (
        <div
          className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)]/40 p-8 text-center"
          data-testid="empty-environments"
        >
          <Box size={28} className="mx-auto text-[var(--app-text-subtle)] opacity-60" />
          <h3 className="mt-2 text-sm font-semibold text-[var(--app-text)]">No environments defined</h3>
          <p className="mt-1 text-xs text-[var(--app-text-muted)] max-w-sm mx-auto">
            Define container images, mount configurations, and resource limits for automated testing and agent execution.
          </p>
          <Button variant="secondary" size="sm" onClick={handleOpenCreate} className="mt-4 gap-1.5">
            <Plus size={14} />
            Define Testbench Environment
          </Button>
        </div>
      ) : (
        <div className="grid gap-4" data-testid="environments-list">
          {environments.map((env) => {
            const isDefaultTestbench = defaultTestEnvId === env.id
            const isDeploying = deployingId === env.id
            const isSettingDefault = settingDefaultId === env.id

            // Format provisioning description
            const strat = env.provisioning?.strategy
            let stratDesc = 'Direct Container'
            if (strat?.kind === 'local_mount') {
              const host = strat.local_mount?.host_path || 'workspace root'
              stratDesc = `Local Mount: ${host} → ${strat.local_mount?.container_path || '/workspace'}`
            } else if (strat?.kind === 'remote_existing_path') {
              stratDesc = `Remote Path: ${strat.remote_existing_path?.remote_path} → ${strat.remote_existing_path?.container_path || '/workspace'}`
            } else if (strat?.kind === 'registry_image') {
              stratDesc = `Registry Image: ${strat.registry_image?.image || env.container.image}`
            } else if (strat?.kind === 'git_checkout') {
              stratDesc = `Git Checkout: ${strat.git_checkout?.repository_url}`
            }

            return (
              <Card
                key={env.id}
                className="p-4 sm:p-5 space-y-4 hover:border-[var(--app-border-strong)] transition-all"
                data-testid={`environment-card-${env.id}`}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="flex items-start gap-3 min-w-0">
                    <div className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
                      <Layers size={18} />
                    </div>
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-sm font-semibold text-[var(--app-text)] truncate">{env.name}</h3>

                        {/* Mode badge */}
                        <Badge tone="neutral" className="text-[10.5px] capitalize" data-testid="env-mode-badge">
                          {env.mode}
                        </Badge>

                        {/* Role badge */}
                        <Badge
                          tone={env.role === 'testing' ? 'live' : 'neutral'}
                          className="text-[10.5px] capitalize"
                          data-testid="env-role-badge"
                        >
                          {env.role}
                        </Badge>

                        {/* Default Testbench Badge */}
                        {isDefaultTestbench && (
                          <Badge
                            tone="live"
                            className="text-[10.5px] font-semibold gap-1"
                            data-testid="default-testbench-badge"
                          >
                            <Sparkles size={11} />
                            Default Testbench
                          </Badge>
                        )}
                      </div>

                      {env.description && (
                        <p className="text-xs text-[var(--app-text-muted)] mt-0.5 line-clamp-1">{env.description}</p>
                      )}
                    </div>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1.5 shrink-0">
                    {!isDefaultTestbench && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleSetDefault(env.id)}
                        disabled={isSettingDefault}
                        className="text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
                        title="Set as workspace default test environment"
                        data-testid={`set-default-test-btn-${env.id}`}
                      >
                        {isSettingDefault ? (
                          <Loader2 size={13} className="animate-spin" />
                        ) : (
                          <Check size={13} />
                        )}
                        <span className="hidden sm:inline">Set as Default Testbench</span>
                      </Button>
                    )}

                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => handleDeploy(env.id)}
                      disabled={isDeploying}
                      className="text-xs gap-1.5"
                      data-testid={`deploy-env-btn-${env.id}`}
                    >
                      {isDeploying ? (
                        <>
                          <Loader2 size={13} className="animate-spin" />
                          Deploying…
                        </>
                      ) : (
                        <>
                          <Play size={13} />
                          Deploy
                        </>
                      )}
                    </Button>

                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleOpenEdit(env)}
                      className="text-xs px-2"
                      title="Edit environment"
                      data-testid={`edit-env-btn-${env.id}`}
                    >
                      <Pencil size={13} />
                    </Button>

                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleDelete(env.id)}
                      disabled={deletingId === env.id}
                      className="text-xs px-2 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg,rgba(239,68,68,0.1))]"
                      title="Delete environment"
                      data-testid={`delete-env-btn-${env.id}`}
                    >
                      <Trash2 size={13} />
                    </Button>
                  </div>
                </div>

                {/* Environment Details Grid */}
                <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-3 text-xs text-[var(--app-text-muted)] bg-[var(--app-surface-subtle)]/60 rounded-xl p-3.5">
                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Container Image:</span>
                    <span className="font-mono text-[var(--app-text)] font-medium truncate block" title={env.container.image}>
                      {env.container.image}
                    </span>
                  </div>

                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Preferred Connection:</span>
                    <span className="text-[var(--app-text)] font-medium truncate block">
                      {getConnectionName(env.preferred_connection_id)}
                    </span>
                  </div>

                  <div>
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Release Policy:</span>
                    <span className="text-[var(--app-text)] font-medium capitalize">
                      {env.deployment_policy.release_behavior} (Max: {env.deployment_policy.max_instances})
                    </span>
                  </div>

                  <div className="col-span-full">
                    <span className="text-[11px] text-[var(--app-text-subtle)] block">Source Provisioning:</span>
                    <span className="font-mono text-[var(--app-text)] text-[11.5px] truncate block" title={stratDesc}>
                      {stratDesc}
                    </span>
                  </div>

                  {/* Ports and Limits */}
                  {(env.container.exposed_ports?.length || env.resources?.cpu_limit || env.resources?.memory_limit) ? (
                    <div className="col-span-full flex flex-wrap items-center gap-2 pt-1 border-t border-[var(--app-border)]/50">
                      {env.container.exposed_ports?.map((p, idx) => (
                        <span key={idx} className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)] text-[11px] font-mono">
                          Port: {p.container_port}{p.host_port ? ` → ${p.host_port}` : ''}
                        </span>
                      ))}
                      {env.resources?.cpu_limit && (
                        <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)] text-[11px]">
                          CPU: {env.resources.cpu_limit}
                        </span>
                      )}
                      {env.resources?.memory_limit && (
                        <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)] text-[11px]">
                          Memory: {env.resources.memory_limit}
                        </span>
                      )}
                      {env.container.privileged && (
                        <span className="rounded bg-[var(--app-warning-bg,rgba(245,158,11,0.1))] px-1.5 py-0.5 border border-[var(--app-warning-border,rgba(245,158,11,0.3))] text-[11px] text-[var(--app-warning)]">
                          Privileged
                        </span>
                      )}
                    </div>
                  ) : null}
                </div>
              </Card>
            )
          })}
        </div>
      )}

      {/* Setup Dialog */}
      <EnvironmentSetupDialog
        isOpen={dialogOpen}
        workspaceId={workspaceId}
        workspacePath={workspacePath}
        environment={editingEnv}
        connections={connections}
        onClose={() => setDialogOpen(false)}
        onSave={async (data) => {
          await onSaveEnvironment(data)
          onRefresh()
        }}
      />
    </div>
  )
}
