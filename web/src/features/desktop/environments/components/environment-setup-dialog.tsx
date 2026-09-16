import { useState } from 'react'
import { Box, Layers, Loader2, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { Select } from '../../../../components/ui/select'
import type {
  Connection,
  Environment,
  EnvironmentMode,
  EnvironmentRole,
  ReleaseBehavior,
  SourceStrategyKind,
} from '../types/environments'

interface EnvironmentSetupDialogProps {
  isOpen: boolean
  workspaceId: string
  workspacePath?: string
  environment?: Environment | null
  connections: Connection[]
  onClose: () => void
  onSave: (data: Partial<Environment> & { name: string; container: Environment['container'] }) => Promise<void>
}

export function EnvironmentSetupDialog({
  isOpen,
  workspaceId: _workspaceId,
  workspacePath: _workspacePath = '',
  environment,
  connections,
  onClose,
  onSave,
}: EnvironmentSetupDialogProps) {
  const isEditing = Boolean(environment?.id)

  const [name, setName] = useState(environment?.name ?? '')
  const [description, setDescription] = useState(environment?.description ?? '')
  const [mode, setMode] = useState<EnvironmentMode>(environment?.mode ?? 'deployable')
  const [role, setRole] = useState<EnvironmentRole>(environment?.role ?? 'testing')
  const [preferredConnectionId, setPreferredConnectionId] = useState(environment?.preferred_connection_id ?? '')

  // Container fields
  const [image, setImage] = useState(environment?.container.image ?? 'golang:1.24')
  const [workingDir, setWorkingDir] = useState(environment?.container.working_dir ?? '/workspace')
  const [privileged, setPrivileged] = useState(environment?.container.privileged ?? false)
  const [containerPort, setContainerPort] = useState(
    environment?.container.exposed_ports?.[0]?.container_port ? String(environment.container.exposed_ports[0].container_port) : '',
  )
  const [hostPort, setHostPort] = useState(
    environment?.container.exposed_ports?.[0]?.host_port ? String(environment.container.exposed_ports[0].host_port) : '',
  )

  // Provisioning
  const [strategyKind, setStrategyKind] = useState<SourceStrategyKind>(
    environment?.provisioning.strategy.kind ?? 'local_mount',
  )
  const [containerMountPath, setContainerMountPath] = useState(
    environment?.provisioning.strategy.local_mount?.container_path ??
      environment?.provisioning.strategy.remote_existing_path?.container_path ??
      '/workspace',
  )
  const [hostMountPath, setHostMountPath] = useState(
    environment?.provisioning.strategy.local_mount?.host_path ?? '',
  )
  const [remoteMountPath, setRemoteMountPath] = useState(
    environment?.provisioning.strategy.remote_existing_path?.remote_path ?? '',
  )

  // Deployment Policy
  const [reuse, setReuse] = useState(environment?.deployment_policy.reuse ?? true)
  const [maxInstances, setMaxInstances] = useState(environment?.deployment_policy.max_instances ?? 1)
  const [releaseBehavior, setReleaseBehavior] = useState<ReleaseBehavior>(
    environment?.deployment_policy.release_behavior ?? 'restart',
  )

  // Resource limits
  const [cpuLimit, setCpuLimit] = useState(environment?.resources?.cpu_limit ?? '')
  const [memoryLimit, setMemoryLimit] = useState(environment?.resources?.memory_limit ?? '')

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  if (!isOpen) return null

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaveError(null)

    if (!name.trim()) {
      setSaveError('Environment name is required.')
      return
    }
    if (!image.trim()) {
      setSaveError('Container image is required.')
      return
    }

    const exposedPorts = containerPort.trim()
      ? [
          {
            container_port: parseInt(containerPort.trim(), 10),
            host_port: hostPort.trim() ? parseInt(hostPort.trim(), 10) : undefined,
            protocol: 'tcp',
          },
        ]
      : undefined

    const provisioningStrategy: Environment['provisioning']['strategy'] = {
      kind: strategyKind,
    }

    if (strategyKind === 'local_mount') {
      provisioningStrategy.local_mount = {
        container_path: containerMountPath.trim() || '/workspace',
        host_path: hostMountPath.trim() || undefined,
      }
    } else if (strategyKind === 'remote_existing_path') {
      provisioningStrategy.remote_existing_path = {
        remote_path: remoteMountPath.trim() || '/opt/swarm/workspace',
        container_path: containerMountPath.trim() || '/workspace',
      }
    } else if (strategyKind === 'registry_image') {
      provisioningStrategy.registry_image = {
        image: image.trim(),
      }
    }

    const payload: Partial<Environment> & { name: string; container: Environment['container'] } = {
      id: environment?.id,
      name: name.trim(),
      description: description.trim() || undefined,
      mode,
      role,
      preferred_connection_id: preferredConnectionId.trim() || undefined,
      container: {
        image: image.trim(),
        working_dir: workingDir.trim() || undefined,
        privileged,
        exposed_ports: exposedPorts,
      },
      provisioning: {
        strategy: provisioningStrategy,
      },
      deployment_policy: {
        reuse,
        max_instances: maxInstances > 0 ? maxInstances : 1,
        release_behavior: releaseBehavior,
      },
      resources:
        cpuLimit.trim() || memoryLimit.trim()
          ? {
              cpu_limit: cpuLimit.trim() || undefined,
              memory_limit: memoryLimit.trim() || undefined,
            }
          : undefined,
    }

    setSaving(true)
    try {
      await onSave(payload)
      onClose()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog role="dialog" aria-labelledby="env-dialog-title" data-testid="environment-setup-dialog">
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="max-w-2xl max-h-[85vh] overflow-y-auto">
        <div className="flex items-center justify-between border-b border-[var(--app-border)] pb-3 sticky top-0 bg-[var(--app-surface)] z-10">
          <div className="flex items-center gap-2">
            <Box size={18} className="text-[var(--app-primary)]" />
            <h2 id="env-dialog-title" className="text-base font-semibold text-[var(--app-text)]">
              {isEditing ? 'Edit Environment Definition' : 'Create Reusable Environment'}
            </h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg p-1 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
            aria-label="Close dialog"
          >
            <X size={16} />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="mt-4 space-y-4">
          {saveError && (
            <div
              className="rounded-xl border border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.1))] p-3 text-xs text-[var(--app-danger)]"
              role="alert"
            >
              {saveError}
            </div>
          )}

          {/* Basic Identity */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label htmlFor="env-name" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Name *
              </label>
              <Input
                id="env-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. Go 1.24 Testbench"
                required
              />
            </div>
            <div>
              <label htmlFor="env-desc" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Description
              </label>
              <Input
                id="env-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional description"
              />
            </div>
          </div>

          {/* Mode & Role */}
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <div>
              <label htmlFor="env-mode" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Mode
              </label>
              <Select
                id="env-mode"
                value={mode}
                onChange={(e) => setMode(e.target.value as EnvironmentMode)}
              >
                <option value="deployable">Deployable (On-Demand)</option>
                <option value="attached">Attached (Interactive)</option>
              </Select>
            </div>

            <div>
              <label htmlFor="env-role" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Role
              </label>
              <Select
                id="env-role"
                value={role}
                onChange={(e) => setRole(e.target.value as EnvironmentRole)}
              >
                <option value="testing">Testing</option>
                <option value="development">Development</option>
                <option value="build">Build</option>
                <option value="custom">Custom</option>
              </Select>
            </div>

            <div>
              <label htmlFor="env-conn" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Preferred Connection
              </label>
              <Select
                id="env-conn"
                value={preferredConnectionId}
                onChange={(e) => setPreferredConnectionId(e.target.value)}
              >
                <option value="">Use Workspace Default</option>
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} ({c.kind === 'local_docker' ? 'Local' : 'SSH'})
                  </option>
                ))}
              </Select>
            </div>
          </div>

          {/* Container Specs */}
          <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-4 space-y-3">
            <h3 className="text-xs font-semibold text-[var(--app-text)] flex items-center gap-1.5">
              <Layers size={14} className="text-[var(--app-primary)]" />
              Container Specification
            </h3>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label htmlFor="env-image" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Container Image *
                </label>
                <Input
                  id="env-image"
                  value={image}
                  onChange={(e) => setImage(e.target.value)}
                  placeholder="e.g. golang:1.24, python:3.12-slim"
                  required
                />
              </div>

              <div>
                <label htmlFor="env-workdir" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Working Directory
                </label>
                <Input
                  id="env-workdir"
                  value={workingDir}
                  onChange={(e) => setWorkingDir(e.target.value)}
                  placeholder="/workspace"
                />
              </div>
            </div>

            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div>
                <label htmlFor="env-cport" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Container Port
                </label>
                <Input
                  id="env-cport"
                  type="number"
                  value={containerPort}
                  onChange={(e) => setContainerPort(e.target.value)}
                  placeholder="e.g. 8080"
                />
              </div>

              <div>
                <label htmlFor="env-hport" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Host Port (Optional)
                </label>
                <Input
                  id="env-hport"
                  type="number"
                  value={hostPort}
                  onChange={(e) => setHostPort(e.target.value)}
                  placeholder="0 (auto)"
                />
              </div>

              <div>
                <label htmlFor="env-cpu" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  CPU Limit
                </label>
                <Input
                  id="env-cpu"
                  value={cpuLimit}
                  onChange={(e) => setCpuLimit(e.target.value)}
                  placeholder="e.g. 2.0"
                />
              </div>

              <div>
                <label htmlFor="env-mem" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Memory Limit
                </label>
                <Input
                  id="env-mem"
                  value={memoryLimit}
                  onChange={(e) => setMemoryLimit(e.target.value)}
                  placeholder="e.g. 4Gi"
                />
              </div>
            </div>

            <div className="flex items-center gap-2 pt-1">
              <input
                type="checkbox"
                id="env-privileged"
                checked={privileged}
                onChange={(e) => setPrivileged(e.target.checked)}
                className="rounded border-[var(--app-border)] text-[var(--app-primary)] focus:ring-[var(--app-primary)]"
              />
              <label htmlFor="env-privileged" className="text-xs text-[var(--app-text-muted)] cursor-pointer">
                Run container in privileged mode (required for nested containerization or systemd)
              </label>
            </div>
          </div>

          {/* Provisioning Strategy */}
          <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-4 space-y-3">
            <h3 className="text-xs font-semibold text-[var(--app-text)]">Source Provisioning Strategy</h3>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label htmlFor="env-strategy" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Strategy
                </label>
                <Select
                  id="env-strategy"
                  value={strategyKind}
                  onChange={(e) => setStrategyKind(e.target.value as SourceStrategyKind)}
                >
                  <option value="local_mount">Local Mount (Bind)</option>
                  <option value="remote_existing_path">Remote Host Path</option>
                  <option value="registry_image">Pure Registry Image</option>
                </Select>
              </div>

              <div>
                <label htmlFor="env-mount-target" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Container Mount Path
                </label>
                <Input
                  id="env-mount-target"
                  value={containerMountPath}
                  onChange={(e) => setContainerMountPath(e.target.value)}
                  placeholder="/workspace"
                />
              </div>

              {strategyKind === 'local_mount' && (
                <div>
                  <label htmlFor="env-host-mount" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                    Host Path (Default: workspace)
                  </label>
                  <Input
                    id="env-host-mount"
                    value={hostMountPath}
                    onChange={(e) => setHostMountPath(e.target.value)}
                    placeholder="Leave empty for workspace path"
                  />
                </div>
              )}

              {strategyKind === 'remote_existing_path' && (
                <div>
                  <label htmlFor="env-remote-mount" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                    Remote Host Path
                  </label>
                  <Input
                    id="env-remote-mount"
                    value={remoteMountPath}
                    onChange={(e) => setRemoteMountPath(e.target.value)}
                    placeholder="/opt/code/workspace"
                    required
                  />
                </div>
              )}
            </div>
          </div>

          {/* Deployment Policy */}
          <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-4 space-y-3">
            <h3 className="text-xs font-semibold text-[var(--app-text)]">Deployment &amp; Lifecycle Policy</h3>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <label htmlFor="env-release-behavior" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Release Behavior
                </label>
                <Select
                  id="env-release-behavior"
                  value={releaseBehavior}
                  onChange={(e) => setReleaseBehavior(e.target.value as ReleaseBehavior)}
                >
                  <option value="restart">Restart container (clean processes)</option>
                  <option value="none">None (keep container as-is)</option>
                  <option value="recreate">Recreate (destroy and fresh container)</option>
                </Select>
              </div>

              <div>
                <label htmlFor="env-max-instances" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Max Concurrent Instances
                </label>
                <Input
                  id="env-max-instances"
                  type="number"
                  min="1"
                  max="10"
                  value={maxInstances}
                  onChange={(e) => setMaxInstances(parseInt(e.target.value, 10) || 1)}
                />
              </div>

              <div className="flex items-center pt-5">
                <label className="flex items-center gap-2 text-xs text-[var(--app-text-muted)] cursor-pointer">
                  <input
                    type="checkbox"
                    checked={reuse}
                    onChange={(e) => setReuse(e.target.checked)}
                    className="rounded border-[var(--app-border)] text-[var(--app-primary)]"
                  />
                  <span>Reuse released instances</span>
                </label>
              </div>
            </div>
          </div>

          {/* Footer Actions */}
          <div className="flex items-center justify-end gap-2 pt-3 border-t border-[var(--app-border)]">
            <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={saving}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" size="sm" disabled={saving}>
              {saving ? (
                <>
                  <Loader2 size={13} className="animate-spin" />
                  Saving…
                </>
              ) : isEditing ? (
                'Save Changes'
              ) : (
                'Create Environment'
              )}
            </Button>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  )
}
