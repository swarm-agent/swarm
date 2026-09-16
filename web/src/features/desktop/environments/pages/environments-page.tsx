import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams, useSearch } from '@tanstack/react-router'
import {
  Activity,
  ArrowLeft,
  Box,
  Loader2,
  Server,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { useWorkspaceLauncher } from '../../../workspaces/launcher/state/use-workspace-launcher'
import { resolveWorkspaceBySlug } from '../../../workspaces/launcher/services/workspace-route'
import {
  deleteConnection,
  deleteEnvironment,
  destroyDeployment,
  ensureDeployment,
  fetchConnections,
  fetchDeployments,
  fetchEnvironments,
  releaseDeployment,
  saveConnection,
  saveEnvironment,
  setDefaultConnection,
  setDefaultTestEnvironment,
  startDeployment,
  stopDeployment,
} from '../services/environments-api'
import { ConnectionsView } from '../components/connections-view'
import { DeploymentsView } from '../components/deployments-view'
import { EnvironmentsView } from '../components/environments-view'

export type EnvironmentsTabID = 'environments' | 'connections' | 'deployments'

interface EnvironmentsSearch {
  tab?: string
}

export function EnvironmentsPage() {
  const params = useParams({ strict: false }) as { workspaceSlug?: string }
  const search = useSearch({ strict: false }) as EnvironmentsSearch
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { workspaces, currentWorkspacePath, loading: workspaceLoading } = useWorkspaceLauncher({
    applyDocumentTheme: false,
    autoRefresh: false,
    browseDuringRefresh: false,
  })

  const workspace = useMemo(() => {
    if (params.workspaceSlug) {
      return resolveWorkspaceBySlug(workspaces, params.workspaceSlug)
    }
    return workspaces.find((w) => w.path === currentWorkspacePath) ?? workspaces[0]
  }, [currentWorkspacePath, params.workspaceSlug, workspaces])

  const initialTab: EnvironmentsTabID =
    search.tab === 'connections' || search.tab === 'deployments' ? search.tab : 'environments'
  const [activeTab, setActiveTab] = useState<EnvironmentsTabID>(initialTab)
  const [errorMessage, setErrorMessage] = useState<string | null>(null)

  const workspaceId = workspace?.workspaceId ?? ''
  const workspacePath = workspace?.path ?? ''
  const workspaceSlug = params.workspaceSlug ?? ''

  // Query: Connections
  const connectionsQuery = useQuery({
    queryKey: ['environments-connections', workspaceId],
    queryFn: ({ signal }) => fetchConnections(workspaceId, signal, workspacePath),
    enabled: Boolean(workspaceId),
  })

  // Query: Environments & Settings
  const environmentsQuery = useQuery({
    queryKey: ['environments-list', workspaceId],
    queryFn: ({ signal }) => fetchEnvironments(workspaceId, signal, workspacePath),
    enabled: Boolean(workspaceId),
  })

  // Query: Deployments
  const deploymentsQuery = useQuery({
    queryKey: ['environments-deployments', workspaceId],
    queryFn: ({ signal }) => fetchDeployments(workspaceId, signal, '', workspacePath),
    enabled: Boolean(workspaceId),
    refetchInterval: 5000, // Poll deployments periodically for live container updates
  })

  const handleTabChange = (tab: EnvironmentsTabID) => {
    setActiveTab(tab)
    if (workspaceSlug) {
      void navigate({
        to: '/$workspaceSlug/environments',
        params: { workspaceSlug },
        search: { tab },
        replace: true,
      })
    } else {
      void navigate({
        to: '/environments',
        search: { tab },
        replace: true,
      })
    }
  }

  const handleBack = () => {
    if (workspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug } })
    } else {
      void navigate({ to: '/' })
    }
  }

  const invalidateAll = () => {
    void queryClient.invalidateQueries({ queryKey: ['environments-connections', workspaceId] })
    void queryClient.invalidateQueries({ queryKey: ['environments-list', workspaceId] })
    void queryClient.invalidateQueries({ queryKey: ['environments-deployments', workspaceId] })
  }

  // Mutation Handlers
  const handleSaveConnection = async (data: any) => {
    setErrorMessage(null)
    try {
      await saveConnection(workspaceId, data, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleDeleteConnection = async (id: string) => {
    setErrorMessage(null)
    try {
      await deleteConnection(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleSetDefaultConnection = async (id: string) => {
    setErrorMessage(null)
    try {
      await setDefaultConnection(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleSaveEnvironment = async (data: any) => {
    setErrorMessage(null)
    try {
      await saveEnvironment(workspaceId, data, data.id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleDeleteEnvironment = async (id: string) => {
    setErrorMessage(null)
    try {
      await deleteEnvironment(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleSetDefaultTestEnvironment = async (id: string) => {
    setErrorMessage(null)
    try {
      await setDefaultTestEnvironment(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleDeployEnvironment = async (envId: string) => {
    setErrorMessage(null)
    try {
      await ensureDeployment(
        workspaceId,
        {
          environmentId: envId,
          consumerType: 'session',
          deploymentName: `Manual ${new Date().toLocaleTimeString()}`,
        },
        workspacePath,
      )
      invalidateAll()
      setActiveTab('deployments')
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleStartDeployment = async (id: string) => {
    setErrorMessage(null)
    try {
      await startDeployment(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleStopDeployment = async (id: string) => {
    setErrorMessage(null)
    try {
      await stopDeployment(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleReleaseDeployment = async (params: { deploymentId: string; leaseId?: string; reason?: string }) => {
    setErrorMessage(null)
    try {
      await releaseDeployment(workspaceId, params, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  const handleDestroyDeployment = async (id: string) => {
    setErrorMessage(null)
    try {
      await destroyDeployment(workspaceId, id, workspacePath)
      invalidateAll()
    } catch (err) {
      setErrorMessage(err instanceof Error ? err.message : String(err))
      throw err
    }
  }

  if (workspaceLoading) {
    return (
      <main role="status" className="flex h-screen items-center justify-center p-6 text-sm text-[var(--app-text-muted)] gap-2">
        <Loader2 size={18} className="animate-spin text-[var(--app-primary)]" />
        Loading workspace…
      </main>
    )
  }

  if (!workspace?.workspaceId) {
    return (
      <main className="flex h-screen flex-col items-center justify-center p-6 text-center space-y-3">
        <Server size={32} className="text-[var(--app-text-subtle)]" />
        <h1 className="text-base font-semibold text-[var(--app-text)]">Workspace unavailable</h1>
        <p className="text-xs text-[var(--app-text-muted)] max-w-sm">
          Choose an accessible workspace to manage execution environments and host connections.
        </p>
        <Button variant="primary" size="sm" onClick={() => void navigate({ to: '/' })}>
          Open Workspaces
        </Button>
      </main>
    )
  }

  const connections = connectionsQuery.data ?? []
  const environments = environmentsQuery.data?.environments ?? []
  const settings = environmentsQuery.data?.settings ?? { workspace_id: workspaceId, account_scope_id: '' }
  const deployments = deploymentsQuery.data?.deployments ?? []
  const activeLeases = deploymentsQuery.data?.activeLeases ?? {}

  return (
    <div className="flex h-full min-h-screen min-w-0 flex-1 flex-col bg-[var(--app-bg)] text-sm text-[var(--app-text)]" data-testid="environments-page">
      {/* Top Navigation Header */}
      <header className="flex min-h-[60px] flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-3 bg-[var(--app-surface)]">
        <div className="flex min-w-0 items-center gap-3">
          <Button
            variant="ghost"
            size="sm"
            onClick={handleBack}
            className="h-8 px-2 text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
            aria-label="Back to chat or workspaces"
            data-testid="env-back-btn"
          >
            <ArrowLeft size={16} />
          </Button>

          <div className="flex items-center gap-2">
            <Server size={18} className="text-[var(--app-primary)]" />
            <h1 className="font-semibold text-base text-[var(--app-text)]">Environments</h1>
            <span className="text-xs text-[var(--app-text-subtle)] truncate max-w-xs">
              · {workspace.workspaceName}
            </span>
          </div>
        </div>

        {/* Tab switcher */}
        <div className="flex items-center rounded-xl bg-[var(--app-surface-subtle)] p-1 border border-[var(--app-border)]/60" role="tablist" aria-label="Environment management views">
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'environments'}
            onClick={() => handleTabChange('environments')}
            className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-all ${
              activeTab === 'environments'
                ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs font-semibold'
                : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            }`}
            data-testid="tab-environments"
          >
            <Box size={13} />
            Environments
            <span className="rounded-full bg-[var(--app-surface-hover)] px-1.5 py-0.2 text-[10px] text-[var(--app-text-subtle)]">
              {environments.length}
            </span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'connections'}
            onClick={() => handleTabChange('connections')}
            className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-all ${
              activeTab === 'connections'
                ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs font-semibold'
                : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            }`}
            data-testid="tab-connections"
          >
            <Server size={13} />
            Connections
            <span className="rounded-full bg-[var(--app-surface-hover)] px-1.5 py-0.2 text-[10px] text-[var(--app-text-subtle)]">
              {connections.length}
            </span>
          </button>

          <button
            type="button"
            role="tab"
            aria-selected={activeTab === 'deployments'}
            onClick={() => handleTabChange('deployments')}
            className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-all ${
              activeTab === 'deployments'
                ? 'bg-[var(--app-surface)] text-[var(--app-text)] shadow-xs font-semibold'
                : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            }`}
            data-testid="tab-deployments"
          >
            <Activity size={13} />
            Deployments
            <span className="rounded-full bg-[var(--app-surface-hover)] px-1.5 py-0.2 text-[10px] text-[var(--app-text-subtle)]">
              {deployments.length}
            </span>
          </button>
        </div>
      </header>

      {/* Error alert banner */}
      {errorMessage && (
        <div className="mx-6 mt-4 flex items-center justify-between rounded-xl border border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.1))] p-3 text-xs text-[var(--app-danger)]" role="alert">
          <span>{errorMessage}</span>
          <button type="button" onClick={() => setErrorMessage(null)} className="text-xs hover:underline">
            Dismiss
          </button>
        </div>
      )}

      {/* Main Body */}
      <main className="min-w-0 flex-1 p-5 sm:p-8 max-w-6xl mx-auto w-full overflow-y-auto">
        {activeTab === 'environments' && (
          <EnvironmentsView
            workspaceId={workspaceId}
            workspacePath={workspacePath}
            environments={environments}
            connections={connections}
            settings={settings}
            loading={environmentsQuery.isLoading}
            onRefresh={invalidateAll}
            onSaveEnvironment={handleSaveEnvironment}
            onDeleteEnvironment={handleDeleteEnvironment}
            onSetDefaultTestEnvironment={handleSetDefaultTestEnvironment}
            onDeployEnvironment={handleDeployEnvironment}
          />
        )}

        {activeTab === 'connections' && (
          <ConnectionsView
            workspaceId={workspaceId}
            workspacePath={workspacePath}
            connections={connections}
            defaultConnectionId={settings.default_connection_id}
            loading={connectionsQuery.isLoading}
            onRefresh={invalidateAll}
            onSaveConnection={handleSaveConnection}
            onDeleteConnection={handleDeleteConnection}
            onSetDefaultConnection={handleSetDefaultConnection}
          />
        )}

        {activeTab === 'deployments' && (
          <DeploymentsView
            workspaceId={workspaceId}
            workspacePath={workspacePath}
            deployments={deployments}
            activeLeases={activeLeases}
            environments={environments}
            loading={deploymentsQuery.isLoading}
            onRefresh={invalidateAll}
            onStartDeployment={handleStartDeployment}
            onStopDeployment={handleStopDeployment}
            onReleaseDeployment={handleReleaseDeployment}
            onDestroyDeployment={handleDestroyDeployment}
            onQuickLaunch={handleDeployEnvironment}
          />
        )}
      </main>
    </div>
  )
}
