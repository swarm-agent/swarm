import { useState } from 'react'
import { CheckCircle2, Loader2, Server, Shield, X, XCircle } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { checkConnection } from '../services/environments-api'
import type { Connection, ConnectionCheckResult, ConnectionKind } from '../types/environments'

interface ConnectionSetupDialogProps {
  isOpen: boolean
  workspaceId: string
  workspacePath?: string
  connection?: Connection | null
  onClose: () => void
  onSave: (data: {
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
  }) => Promise<void>
}

export function ConnectionSetupDialog({
  isOpen,
  workspaceId,
  workspacePath = '',
  connection,
  onClose,
  onSave,
}: ConnectionSetupDialogProps) {
  const isEditing = Boolean(connection?.id)
  const [kind, setKind] = useState<ConnectionKind>(connection?.kind ?? 'local_docker')
  const [name, setName] = useState(connection?.name ?? '')
  const [description, setDescription] = useState(connection?.description ?? '')

  // Local Docker fields
  const [socketPath, setSocketPath] = useState(connection?.local_docker?.socket_path ?? '/var/run/docker.sock')
  const [dockerHost, setDockerHost] = useState(connection?.local_docker?.host ?? '')

  // SSH fields
  const [host, setHost] = useState(connection?.ssh?.host ?? '')
  const [port, setPort] = useState(connection?.ssh?.port ? String(connection.ssh.port) : '22')
  const [user, setUser] = useState(connection?.ssh?.user ?? '')
  const [sshKeyPath, setSshKeyPath] = useState(connection?.ssh?.identity_file ?? '')
  const [knownHostsFile, setKnownHostsFile] = useState(connection?.ssh?.known_hosts_file ?? '')

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  // Connectivity check state
  const [checking, setChecking] = useState(false)
  const [checkResult, setCheckResult] = useState<ConnectionCheckResult | null>(null)

  if (!isOpen) return null

  const handleTestConnection = async () => {
    setChecking(true)
    setCheckResult(null)
    setSaveError(null)
    try {
      const res = await checkConnection(
        workspaceId,
        {
          kind,
          socket_path: socketPath.trim() || undefined,
          host: host.trim() || undefined,
          port: port ? parseInt(port, 10) : 22,
          user: user.trim() || undefined,
          ssh_key_path: sshKeyPath.trim() || undefined,
        },
        workspacePath,
      )
      setCheckResult(res)
    } catch (err) {
      setCheckResult({
        ok: false,
        healthy: false,
        error: err instanceof Error ? err.message : String(err),
        diagnostics: 'Connection attempt failed unexpectedly.',
      })
    } finally {
      setChecking(false)
    }
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaveError(null)

    if (!name.trim()) {
      setSaveError('Connection name is required.')
      return
    }

    if (kind === 'ssh') {
      if (!host.trim()) {
        setSaveError('SSH host is required.')
        return
      }
      if (!user.trim()) {
        setSaveError('SSH user is required.')
        return
      }
    }

    setSaving(true)
    try {
      await onSave({
        id: connection?.id,
        name: name.trim(),
        description: description.trim() || undefined,
        kind,
        socket_path: kind === 'local_docker' ? socketPath.trim() || undefined : undefined,
        docker_host: kind === 'local_docker' ? dockerHost.trim() || undefined : undefined,
        host: kind === 'ssh' ? host.trim() : undefined,
        port: kind === 'ssh' ? (port ? parseInt(port, 10) : 22) : undefined,
        user: kind === 'ssh' ? user.trim() : undefined,
        ssh_key_path: kind === 'ssh' ? sshKeyPath.trim() || undefined : undefined,
        known_hosts_file: kind === 'ssh' ? knownHostsFile.trim() || undefined : undefined,
      })
      onClose()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog role="dialog" aria-labelledby="connection-dialog-title" data-testid="connection-setup-dialog">
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="max-w-xl">
        <div className="flex items-center justify-between border-b border-[var(--app-border)] pb-3">
          <div className="flex items-center gap-2">
            <Server size={18} className="text-[var(--app-primary)]" />
            <h2 id="connection-dialog-title" className="text-base font-semibold text-[var(--app-text)]">
              {isEditing ? 'Edit Connection' : 'Add Execution Host Connection'}
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

          {/* Connection Kind Selector */}
          <div>
            <label className="block text-xs font-medium text-[var(--app-text-muted)] mb-1.5">
              Connection Type
            </label>
            <div className="grid grid-cols-2 gap-2" role="radiogroup" aria-label="Connection Type">
              <button
                type="button"
                role="radio"
                aria-checked={kind === 'local_docker'}
                onClick={() => setKind('local_docker')}
                disabled={isEditing}
                className={`flex flex-col items-start rounded-xl border p-3 text-left transition-all ${
                  kind === 'local_docker'
                    ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)]'
                    : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]'
                } ${isEditing ? 'opacity-70 cursor-not-allowed' : 'cursor-pointer'}`}
              >
                <span className="text-xs font-semibold">Local Docker</span>
                <span className="text-[11px] text-[var(--app-text-subtle)] mt-0.5">
                  Execute via local Docker daemon socket
                </span>
              </button>

              <button
                type="button"
                role="radio"
                aria-checked={kind === 'ssh'}
                onClick={() => setKind('ssh')}
                disabled={isEditing}
                className={`flex flex-col items-start rounded-xl border p-3 text-left transition-all ${
                  kind === 'ssh'
                    ? 'border-[var(--app-primary)] bg-[var(--app-primary-soft)] text-[var(--app-text)]'
                    : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]'
                } ${isEditing ? 'opacity-70 cursor-not-allowed' : 'cursor-pointer'}`}
              >
                <span className="text-xs font-semibold">Remote SSH Host</span>
                <span className="text-[11px] text-[var(--app-text-subtle)] mt-0.5">
                  Execute via SSH on remote machine or VM
                </span>
              </button>
            </div>
          </div>

          {/* Common Fields */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label htmlFor="conn-name" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Name *
              </label>
              <Input
                id="conn-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={kind === 'local_docker' ? 'e.g. Local Docker Daemon' : 'e.g. GPU Test Server'}
                required
              />
            </div>
            <div>
              <label htmlFor="conn-desc" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                Description
              </label>
              <Input
                id="conn-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Optional description"
              />
            </div>
          </div>

          {/* Local Docker Fields */}
          {kind === 'local_docker' && (
            <div className="space-y-3 rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/50 p-3.5">
              <div>
                <label htmlFor="docker-socket" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Docker Socket Path
                </label>
                <Input
                  id="docker-socket"
                  value={socketPath}
                  onChange={(e) => setSocketPath(e.target.value)}
                  placeholder="/var/run/docker.sock"
                />
                <p className="mt-1 text-[11px] text-[var(--app-text-subtle)]">
                  Standard Linux/macOS Unix socket path (default: /var/run/docker.sock).
                </p>
              </div>

              <div>
                <label htmlFor="docker-host" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Docker Host URL (Optional)
                </label>
                <Input
                  id="docker-host"
                  value={dockerHost}
                  onChange={(e) => setDockerHost(e.target.value)}
                  placeholder="e.g. unix:///var/run/docker.sock"
                />
              </div>
            </div>
          )}

          {/* SSH Fields */}
          {kind === 'ssh' && (
            <div className="space-y-3 rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/50 p-3.5">
              <div className="grid grid-cols-3 gap-3">
                <div className="col-span-2">
                  <label htmlFor="ssh-host" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                    Host *
                  </label>
                  <Input
                    id="ssh-host"
                    value={host}
                    onChange={(e) => setHost(e.target.value)}
                    placeholder="hostname or IP (e.g. 192.168.1.50)"
                    required
                  />
                </div>
                <div>
                  <label htmlFor="ssh-port" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                    Port
                  </label>
                  <Input
                    id="ssh-port"
                    type="number"
                    value={port}
                    onChange={(e) => setPort(e.target.value)}
                    placeholder="22"
                  />
                </div>
              </div>

              <div>
                <label htmlFor="ssh-user" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  User *
                </label>
                <Input
                  id="ssh-user"
                  value={user}
                  onChange={(e) => setUser(e.target.value)}
                  placeholder="e.g. ubuntu, root, roy"
                  required
                />
              </div>

              <div>
                <label htmlFor="ssh-key-path" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  SSH Identity File Path (Optional)
                </label>
                <Input
                  id="ssh-key-path"
                  value={sshKeyPath}
                  onChange={(e) => setSshKeyPath(e.target.value)}
                  placeholder="e.g. ~/.ssh/id_ed25519"
                />
                <div className="mt-1 flex items-center gap-1.5 text-[11px] text-[var(--app-text-subtle)]">
                  <Shield size={12} className="text-[var(--app-success)] shrink-0" />
                  <span>Only file path references are stored. Secret keys/passwords are never saved in Swarm.</span>
                </div>
              </div>

              <div>
                <label htmlFor="known-hosts" className="block text-xs font-medium text-[var(--app-text-muted)] mb-1">
                  Known Hosts File Path (Optional)
                </label>
                <Input
                  id="known-hosts"
                  value={knownHostsFile}
                  onChange={(e) => setKnownHostsFile(e.target.value)}
                  placeholder="e.g. ~/.ssh/known_hosts"
                />
              </div>
            </div>
          )}

          {/* Connectivity Test Diagnostics */}
          {checkResult && (
            <div
              className={`rounded-xl border p-3 text-xs ${
                checkResult.healthy
                  ? 'border-[var(--app-success-border,rgba(16,185,129,0.3))] bg-[var(--app-success-bg,rgba(16,185,129,0.12))] text-[var(--app-success)]'
                  : 'border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.1))] text-[var(--app-danger)]'
              }`}
              data-testid="connection-check-result"
            >
              <div className="flex items-center gap-2 font-semibold">
                {checkResult.healthy ? (
                  <>
                    <CheckCircle2 size={14} />
                    <span>Connection Verified</span>
                  </>
                ) : (
                  <>
                    <XCircle size={14} />
                    <span>Connection Unreachable</span>
                  </>
                )}
              </div>
              <p className="mt-1 text-[11px] text-[var(--app-text)] font-mono">
                {checkResult.diagnostics || checkResult.error || (checkResult.healthy ? 'Host is reachable.' : 'Check failed.')}
              </p>
              {checkResult.capabilities && (
                <div className="mt-1.5 flex flex-wrap gap-1.5 text-[10.5px]">
                  {checkResult.capabilities.supports_docker && (
                    <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)]">Docker Supported</span>
                  )}
                  {checkResult.capabilities.supports_direct_mount && (
                    <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)]">Direct Mount Supported</span>
                  )}
                  {checkResult.capabilities.supports_ssh && (
                    <span className="rounded bg-[var(--app-surface)] px-1.5 py-0.5 border border-[var(--app-border)]">SSH Supported</span>
                  )}
                </div>
              )}
            </div>
          )}

          {/* Action buttons */}
          <div className="flex items-center justify-between pt-2 border-t border-[var(--app-border)]">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleTestConnection}
              disabled={checking || (kind === 'ssh' && (!host.trim() || !user.trim()))}
              data-testid="test-connection-btn"
            >
              {checking ? (
                <>
                  <Loader2 size={13} className="animate-spin" />
                  Testing...
                </>
              ) : (
                'Test Connectivity'
              )}
            </Button>

            <div className="flex items-center gap-2">
              <Button type="button" variant="ghost" size="sm" onClick={onClose} disabled={saving}>
                Cancel
              </Button>
              <Button type="submit" variant="primary" size="sm" disabled={saving}>
                {saving ? (
                  <>
                    <Loader2 size={13} className="animate-spin" />
                    Saving...
                  </>
                ) : isEditing ? (
                  'Save Changes'
                ) : (
                  'Add Connection'
                )}
              </Button>
            </div>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  )
}
