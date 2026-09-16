import { useState } from 'react'
import {
  Check,
  CheckCircle2,
  HardDrive,
  Loader2,
  Pencil,
  Plus,
  Radio,
  Server,
  Terminal,
  Trash2,
  XCircle,
} from 'lucide-react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { checkConnection } from '../services/environments-api'
import type { Connection, ConnectionCheckResult } from '../types/environments'
import { ConnectionSetupDialog } from './connection-setup-dialog'

interface ConnectionsViewProps {
  workspaceId: string
  workspacePath?: string
  connections: Connection[]
  defaultConnectionId?: string
  loading?: boolean
  onRefresh: () => void
  onSaveConnection: (data: any) => Promise<void>
  onDeleteConnection: (id: string) => Promise<void>
  onSetDefaultConnection: (id: string) => Promise<void>
}

export function ConnectionsView({
  workspaceId,
  workspacePath = '',
  connections,
  defaultConnectionId,
  loading = false,
  onRefresh,
  onSaveConnection,
  onDeleteConnection,
  onSetDefaultConnection,
}: ConnectionsViewProps) {
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingConnection, setEditingConnection] = useState<Connection | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  const [testResults, setTestResults] = useState<Record<string, ConnectionCheckResult>>({})
  const [settingDefaultId, setSettingDefaultId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)

  const handleOpenCreate = () => {
    setEditingConnection(null)
    setDialogOpen(true)
  }

  const handleOpenEdit = (conn: Connection) => {
    setEditingConnection(conn)
    setDialogOpen(true)
  }

  const handleTest = async (conn: Connection) => {
    setTestingId(conn.id)
    try {
      const res = await checkConnection(workspaceId, conn.id, workspacePath)
      setTestResults((prev) => ({ ...prev, [conn.id]: res }))
    } catch (err) {
      setTestResults((prev) => ({
        ...prev,
        [conn.id]: {
          ok: false,
          healthy: false,
          error: err instanceof Error ? err.message : String(err),
          diagnostics: 'Network/protocol check failed.',
        },
      }))
    } finally {
      setTestingId(null)
    }
  }

  const handleSetDefault = async (connId: string) => {
    setSettingDefaultId(connId)
    try {
      await onSetDefaultConnection(connId)
    } finally {
      setSettingDefaultId(null)
    }
  }

  const handleDelete = async (connId: string) => {
    if (!confirm('Are you sure you want to delete this connection?')) return
    setDeletingId(connId)
    try {
      await onDeleteConnection(connId)
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <div className="space-y-6" data-testid="connections-view">
      {/* Section Header */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-[var(--app-text)] flex items-center gap-2">
            <Server size={16} className="text-[var(--app-primary)]" />
            Configured Connections
            <span className="text-xs text-[var(--app-text-subtle)] font-normal">({connections.length})</span>
          </h2>
          <p className="text-xs text-[var(--app-text-muted)] mt-0.5">
            Host endpoints and Docker daemons authorized to run execution environments.
          </p>
        </div>

        <Button
          variant="primary"
          size="sm"
          onClick={handleOpenCreate}
          className="gap-1.5"
          data-testid="add-connection-btn"
        >
          <Plus size={14} />
          Add Connection
        </Button>
      </div>

      {/* List */}
      {loading ? (
        <div className="flex items-center justify-center p-8 text-xs text-[var(--app-text-muted)] gap-2">
          <Loader2 size={16} className="animate-spin text-[var(--app-primary)]" />
          Loading connections…
        </div>
      ) : connections.length === 0 ? (
        <div
          className="rounded-2xl border border-dashed border-[var(--app-border)] bg-[var(--app-surface-subtle)]/40 p-8 text-center"
          data-testid="empty-connections"
        >
          <Server size={28} className="mx-auto text-[var(--app-text-subtle)] opacity-60" />
          <h3 className="mt-2 text-sm font-semibold text-[var(--app-text)]">No connections configured</h3>
          <p className="mt-1 text-xs text-[var(--app-text-muted)] max-w-sm mx-auto">
            Add a Local Docker socket or remote SSH host to enable on-demand deployment and testing.
          </p>
          <Button variant="secondary" size="sm" onClick={handleOpenCreate} className="mt-4 gap-1.5">
            <Plus size={14} />
            Configure Local Docker
          </Button>
        </div>
      ) : (
        <div className="grid gap-4" data-testid="connections-list">
          {connections.map((conn) => {
            const isDefault = defaultConnectionId === conn.id
            const check = testResults[conn.id]
            const isTesting = testingId === conn.id
            const isLocal = conn.kind === 'local_docker'

            return (
              <Card
                key={conn.id}
                className="p-4 sm:p-5 space-y-4 hover:border-[var(--app-border-strong)] transition-all"
                data-testid={`connection-card-${conn.id}`}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="flex items-start gap-3 min-w-0">
                    <div className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
                      {isLocal ? <HardDrive size={18} /> : <Terminal size={18} />}
                    </div>
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-sm font-semibold text-[var(--app-text)] truncate">{conn.name}</h3>
                        <Badge tone={isLocal ? 'neutral' : 'live'} className="text-[10.5px]">
                          {isLocal ? 'Local Docker' : 'SSH Host'}
                        </Badge>
                        {isDefault && (
                          <Badge tone="live" className="text-[10.5px] font-semibold" data-testid="default-connection-badge">
                            Default Connection
                          </Badge>
                        )}
                      </div>
                      {conn.description && (
                        <p className="text-xs text-[var(--app-text-muted)] mt-0.5 line-clamp-1">{conn.description}</p>
                      )}
                    </div>
                  </div>

                  {/* Actions */}
                  <div className="flex items-center gap-1.5 shrink-0">
                    {!isDefault && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleSetDefault(conn.id)}
                        disabled={settingDefaultId === conn.id}
                        className="text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
                        title="Set as workspace default connection"
                        data-testid={`set-default-btn-${conn.id}`}
                      >
                        {settingDefaultId === conn.id ? (
                          <Loader2 size={13} className="animate-spin" />
                        ) : (
                          <Check size={13} />
                        )}
                        <span className="hidden sm:inline">Set Default</span>
                      </Button>
                    )}

                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => handleTest(conn)}
                      disabled={isTesting}
                      className="text-xs gap-1.5"
                      data-testid={`test-btn-${conn.id}`}
                    >
                      {isTesting ? (
                        <>
                          <Loader2 size={13} className="animate-spin" />
                          Testing…
                        </>
                      ) : (
                        <>
                          <Radio size={13} />
                          Test
                        </>
                      )}
                    </Button>

                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleOpenEdit(conn)}
                      className="text-xs px-2"
                      title="Edit connection"
                      data-testid={`edit-btn-${conn.id}`}
                    >
                      <Pencil size={13} />
                    </Button>

                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => handleDelete(conn.id)}
                      disabled={deletingId === conn.id}
                      className="text-xs px-2 text-[var(--app-danger)] hover:bg-[var(--app-danger-bg,rgba(239,68,68,0.1))]"
                      title="Delete connection"
                      data-testid={`delete-btn-${conn.id}`}
                    >
                      <Trash2 size={13} />
                    </Button>
                  </div>
                </div>

                {/* Connection Meta & Endpoint */}
                <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2 text-xs text-[var(--app-text-muted)] bg-[var(--app-surface-subtle)]/60 rounded-xl p-3">
                  {isLocal ? (
                    <>
                      <div>
                        <span className="text-[11px] text-[var(--app-text-subtle)] block">Socket Path:</span>
                        <span className="font-mono text-[var(--app-text)]">
                          {conn.local_docker?.socket_path || '/var/run/docker.sock'}
                        </span>
                      </div>
                      {conn.local_docker?.host && (
                        <div>
                          <span className="text-[11px] text-[var(--app-text-subtle)] block">Docker Host:</span>
                          <span className="font-mono text-[var(--app-text)]">{conn.local_docker.host}</span>
                        </div>
                      )}
                    </>
                  ) : (
                    <>
                      <div>
                        <span className="text-[11px] text-[var(--app-text-subtle)] block">Host:</span>
                        <span className="font-mono text-[var(--app-text)]">
                          {conn.ssh?.host}:{conn.ssh?.port || 22}
                        </span>
                      </div>
                      <div>
                        <span className="text-[11px] text-[var(--app-text-subtle)] block">User:</span>
                        <span className="font-mono text-[var(--app-text)]">{conn.ssh?.user}</span>
                      </div>
                      {conn.ssh?.identity_file && (
                        <div>
                          <span className="text-[11px] text-[var(--app-text-subtle)] block">SSH Key Path:</span>
                          <span className="font-mono text-[var(--app-text)] truncate block" title={conn.ssh.identity_file}>
                            {conn.ssh.identity_file}
                          </span>
                        </div>
                      )}
                    </>
                  )}

                  {/* Capabilities */}
                  <div className="col-span-full pt-1 flex flex-wrap items-center gap-1.5">
                    <span className="text-[11px] text-[var(--app-text-subtle)] mr-1">Capabilities:</span>
                    {conn.capabilities.supports_docker && (
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface)] px-1.5 py-0.5 text-[10.5px]">
                        Docker
                      </span>
                    )}
                    {conn.capabilities.supports_direct_mount && (
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface)] px-1.5 py-0.5 text-[10.5px]">
                        Direct Mount
                      </span>
                    )}
                    {conn.capabilities.supports_ssh && (
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface)] px-1.5 py-0.5 text-[10.5px]">
                        SSH
                      </span>
                    )}
                    {conn.capabilities.supports_port_forward && (
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface)] px-1.5 py-0.5 text-[10.5px]">
                        Port Forward
                      </span>
                    )}
                    {conn.capabilities.remote_os && (
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface)] px-1.5 py-0.5 text-[10.5px] font-mono">
                        {conn.capabilities.remote_os}/{conn.capabilities.remote_arch || 'x64'}
                      </span>
                    )}
                  </div>
                </div>

                {/* Diagnostics Panel */}
                {check && (
                  <div
                    className={`rounded-xl border p-3 text-xs ${
                      check.healthy
                        ? 'border-[var(--app-success-border,rgba(16,185,129,0.3))] bg-[var(--app-success-bg,rgba(16,185,129,0.1))] text-[var(--app-success)]'
                        : 'border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.1))] text-[var(--app-danger)]'
                    }`}
                    data-testid={`diagnostic-result-${conn.id}`}
                  >
                    <div className="flex items-center gap-1.5 font-semibold">
                      {check.healthy ? <CheckCircle2 size={14} /> : <XCircle size={14} />}
                      <span>{check.healthy ? 'Connection Verified & Responsive' : 'Health Check Failed'}</span>
                    </div>
                    <p className="mt-1 text-[11px] text-[var(--app-text)] font-mono">
                      {check.diagnostics || check.error || (check.healthy ? 'Host is reachable.' : 'Check failed.')}
                    </p>
                  </div>
                )}
              </Card>
            )
          })}
        </div>
      )}

      {/* Setup Dialog */}
      <ConnectionSetupDialog
        isOpen={dialogOpen}
        workspaceId={workspaceId}
        workspacePath={workspacePath}
        connection={editingConnection}
        onClose={() => setDialogOpen(false)}
        onSave={async (data) => {
          await onSaveConnection(data)
          onRefresh()
        }}
      />
    </div>
  )
}
