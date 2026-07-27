import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Bot, CheckCircle2, ChevronLeft, ChevronRight, Link2, Loader2, MessageSquarePlus, Plus, Settings2, Sparkles } from 'lucide-react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { fetchDraftModelPreference } from '../../chat/queries/chat-queries'
import type { DesktopSessionRecord } from '../../types/realtime'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import { SwarmToolSidebar } from '../../tools/components/swarm-tool-sidebar'
import {
  createIntegrationWorkspaceChildSession,
  fetchIntegrationWorkspace,
  fetchIntegrationWorkspaces,
  openIntegrationWorkspace,
  switchIntegrationWorkspaceSession,
  type IntegrationWorkspaceRecord,
  type IntegrationWorkspaceSnapshot,
  workspaceIdFromName,
} from '../services/integration-workspace-sessions'

function workspaceStatus(workspace: IntegrationWorkspaceRecord): string {
  if (workspace.latestChildSessionId) return 'Drafting'
  return 'Ready to start'
}

function sortWorkspaces(workspaces: IntegrationWorkspaceRecord[]): IntegrationWorkspaceRecord[] {
  return [...workspaces].sort((left, right) => {
    const leftTime = Date.parse(left.latestChildSessionAt || left.updatedAt || left.createdAt || '') || 0
    const rightTime = Date.parse(right.latestChildSessionAt || right.updatedAt || right.createdAt || '') || 0
    return rightTime - leftTime
  })
}

export function IntegrationsPage() {
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const queryClient = useQueryClient()
  const routeSessionMatch = matchRoute({ to: '/integrations/$sessionId', fuzzy: false })
  const routeSessionId = routeSessionMatch ? routeSessionMatch.sessionId.trim() : ''
  const [newIntegrationName, setNewIntegrationName] = useState('')
  const [creatingIntegration, setCreatingIntegration] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const [darkModeEnabled, setDarkModeEnabled] = useState(false)
  const [aiSidebarOpen, setAiSidebarOpen] = useState(true)
  const [selectedWorkspaceIdState, setSelectedWorkspaceIdState] = useState('')

  const workspacesQuery = useQuery({
    queryKey: ['integration-workspaces'],
    queryFn: () => fetchIntegrationWorkspaces(200),
    staleTime: 15_000,
  })
  const draftModelQuery = useQuery({
    queryKey: ['draft-model'],
    queryFn: () => fetchDraftModelPreference(),
    staleTime: 60_000,
  })
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())

  const workspaces = useMemo(() => sortWorkspaces(workspacesQuery.data ?? []), [workspacesQuery.data])
  const inferredWorkspaceId = useMemo(() => {
    const fromLatest = workspaces.find((workspace) => workspace.latestChildSessionId === routeSessionId)?.workspaceId ?? ''
    return fromLatest
  }, [routeSessionId, workspaces])
  const selectedWorkspaceId = selectedWorkspaceIdState || inferredWorkspaceId || workspaces[0]?.workspaceId || ''

  useEffect(() => {
    if (inferredWorkspaceId && inferredWorkspaceId !== selectedWorkspaceIdState) {
      setSelectedWorkspaceIdState(inferredWorkspaceId)
    }
  }, [inferredWorkspaceId, selectedWorkspaceIdState])

  const snapshotQuery = useQuery({
    queryKey: ['integration-workspace', selectedWorkspaceId],
    queryFn: () => fetchIntegrationWorkspace(selectedWorkspaceId),
    enabled: selectedWorkspaceId !== '',
    staleTime: 10_000,
  })

  const selectedWorkspace = snapshotQuery.data?.workspace
    ?? workspaces.find((workspace) => workspace.workspaceId === selectedWorkspaceId)
    ?? null
  const selectedSession = snapshotQuery.data?.session ?? null
  const childSessions = snapshotQuery.data?.sessions ?? []
  const userThemeId = normalizeGlobalThemeSettings(uiSettingsQuery.data).activeId
  const darkOverrideButtonStyle = useMemo(() => createWorkspaceThemeStyle(userThemeId, '--integration-workspace-theme'), [userThemeId])

  const requirePreference = useCallback(() => {
    const preference = draftModelQuery.data?.preference
    if (!preference?.provider || !preference.model || !preference.thinking) {
      throw new Error('Select an authenticated model before starting an integration workspace chat.')
    }
    return preference
  }, [draftModelQuery.data?.preference])

  const cacheWorkspaceSnapshot = useCallback((workspaceId: string, snapshot: IntegrationWorkspaceSnapshot) => {
    queryClient.setQueryData(['integration-workspace', workspaceId], snapshot)
    if (snapshot.workspace) {
      const nextWorkspace = snapshot.workspace
      queryClient.setQueryData<IntegrationWorkspaceRecord[]>(['integration-workspaces'], (current = []) => {
        const without = current.filter((workspace) => workspace.workspaceId !== nextWorkspace.workspaceId)
        return sortWorkspaces([nextWorkspace, ...without])
      })
    }
  }, [queryClient])

  const handleOpenWorkspace = useCallback(async (workspace: IntegrationWorkspaceRecord, newChild = false) => {
    setCreateError(null)
    const preference = requirePreference()
    const snapshot = await openIntegrationWorkspace({
      workspaceId: workspace.workspaceId,
      displayName: workspace.displayName,
      packId: workspace.packId,
      draftVersionId: workspace.draftVersionId,
      title: workspace.displayName,
      mode: 'plan',
      createChild: !workspace.latestChildSessionId,
      newChild,
      preference,
    })
    cacheWorkspaceSnapshot(workspace.workspaceId, snapshot)
    setSelectedWorkspaceIdState(workspace.workspaceId)
    setAiSidebarOpen(true)
    if (snapshot.session?.id) {
      void navigate({ to: '/integrations/$sessionId', params: { sessionId: snapshot.session.id } })
    } else {
      void navigate({ to: '/integrations' })
    }
  }, [cacheWorkspaceSnapshot, navigate, requirePreference])

  const handleCreateIntegration = useCallback(async () => {
    const name = newIntegrationName.trim()
    if (!name) {
      setCreateError('Name the integration first.')
      setCreateDialogOpen(true)
      return
    }
    setCreatingIntegration(true)
    setCreateError(null)
    try {
      const preference = requirePreference()
      const workspaceId = workspaceIdFromName(name)
      const snapshot = await openIntegrationWorkspace({
        workspaceId,
        displayName: name,
        title: name,
        mode: 'plan',
        createChild: true,
        newChild: true,
        metadata: { source: 'integrations_page' },
        preference,
      })
      cacheWorkspaceSnapshot(workspaceId, snapshot)
      setSelectedWorkspaceIdState(workspaceId)
        setNewIntegrationName('')
      setCreateDialogOpen(false)
      setAiSidebarOpen(true)
      await queryClient.invalidateQueries({ queryKey: ['integration-workspaces'] })
      if (snapshot.session?.id) void navigate({ to: '/integrations/$sessionId', params: { sessionId: snapshot.session.id } })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : 'Failed to create integration')
    } finally {
      setCreatingIntegration(false)
    }
  }, [cacheWorkspaceSnapshot, navigate, newIntegrationName, queryClient, requirePreference])

  const handleCreateChildSession = useCallback(async (): Promise<DesktopSessionRecord> => {
    if (!selectedWorkspace) throw new Error('Select an integration first.')
    const preference = requirePreference()
    const session = await createIntegrationWorkspaceChildSession({
      workspaceId: selectedWorkspace.workspaceId,
      title: selectedWorkspace.displayName,
      mode: 'plan',
      metadata: { source: 'integrations_page_new_chat' },
      preference,
    })
    await queryClient.invalidateQueries({ queryKey: ['integration-workspace', selectedWorkspace.workspaceId] })
    await queryClient.invalidateQueries({ queryKey: ['integration-workspaces'] })
    void navigate({ to: '/integrations/$sessionId', params: { sessionId: session.id } })
    return session
  }, [navigate, queryClient, requirePreference, selectedWorkspace])

  const handleSwitchChildSession = useCallback(async (sessionId: string) => {
    if (!selectedWorkspace) return
    const session = await switchIntegrationWorkspaceSession(selectedWorkspace.workspaceId, sessionId)
    setAiSidebarOpen(true)
    void navigate({ to: '/integrations/$sessionId', params: { sessionId: session.id } })
  }, [navigate, selectedWorkspace])

  const sidebarWorkspaces = workspaces.map((workspace) => ({
    id: workspace.workspaceId,
    title: workspace.displayName || 'New integration',
    subtitle: workspaceStatus(workspace),
  }))

  return (
    <div className="absolute inset-0 flex overflow-hidden bg-[var(--app-bg)] text-[var(--app-text)]">
      <SwarmToolSidebar
        backLabel="Back to launcher"
        onBack={() => void navigate({ to: '/' })}
        darkModeEnabled={darkModeEnabled}
        onToggleDarkMode={() => setDarkModeEnabled((current) => !current)}
        darkModeStyle={darkOverrideButtonStyle}
        toolIcon={<Link2 size={17} strokeWidth={1.8} />}
        toolTitle="Integrations"
        toolDescription="Scoped Integration Packs. Select a draft to review settings and related workspace chats."
        createLabel="Create"
        createTitle={newIntegrationName}
        onCreateTitleChange={setNewIntegrationName}
        createPlaceholder="Integration name"
        onCreate={() => setCreateDialogOpen(true)}
        creating={creatingIntegration}
        createButtonLabel="Add integration"
        creatingButtonLabel="Starting…"
        sessionsLabel="Drafts"
        sessionsLoading={workspacesQuery.isLoading || snapshotQuery.isLoading}
        sessions={sidebarWorkspaces}
        selectedSessionId={selectedWorkspace?.workspaceId ?? null}
        onSelectSession={(workspaceId) => {
          const workspace = workspaces.find((item) => item.workspaceId === workspaceId)
          if (workspace) void handleOpenWorkspace(workspace)
        }}
        emptySessionsMessage="No integrations yet. Add one from the main panel."
        defaultSessionTitle="New integration"
      >
        {createError ? <div className="mt-3 border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-[11px] leading-5 text-[var(--app-danger)]">{createError}</div> : null}
      </SwarmToolSidebar>

      <main className="min-w-0 flex-1 overflow-hidden">
        <div className="flex h-full min-w-0 bg-[var(--app-bg)]">
          <section className="min-w-0 flex-1 overflow-y-auto px-7 py-8 sm:px-10 sm:py-10 lg:px-14">
            <div className="mx-auto w-full max-w-6xl">
              <header className="flex flex-col gap-5 border-b border-[var(--app-border)] pb-7 lg:flex-row lg:items-end lg:justify-between">
                <div className="min-w-0">
                  <Button variant="ghost" className="mb-7 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={() => void navigate({ to: '/' })}>
                    <ArrowLeft size={15} />
                    Back to launcher
                  </Button>
                  <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm-wide drafts</p>
                  <h1 className="mt-2 text-4xl font-semibold tracking-[-0.065em] text-[var(--app-text)]">Integrations</h1>
                  <p className="mt-4 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
                    Build scoped Integration Packs as reviewable drafts. Cards stay integration-first; related workspace chats remain grouped with the selected integration.
                  </p>
                </div>
                <Button onClick={() => setCreateDialogOpen(true)}>
                  <Plus size={16} />
                  Add integration
                </Button>
              </header>

              <div className="mt-8 grid gap-4">
                {workspacesQuery.isLoading ? (
                  <Card className="border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-sm text-[var(--app-text-muted)]">
                    <Loader2 className="mr-2 inline animate-spin" size={16} /> Loading integration drafts…
                  </Card>
                ) : null}
                {!workspacesQuery.isLoading && workspaces.length === 0 ? (
                  <button
                    type="button"
                    onClick={() => setCreateDialogOpen(true)}
                    className="group flex min-h-[220px] w-full items-center justify-center rounded-3xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-8 text-center transition hover:border-[var(--app-primary)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
                  >
                    <span className="flex max-w-md flex-col items-center">
                      <span className="grid h-14 w-14 place-items-center rounded-2xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-bg)] text-[var(--app-text-muted)] transition group-hover:border-[var(--app-primary)] group-hover:text-[var(--app-primary)]">
                        <Plus size={22} strokeWidth={1.8} />
                      </span>
                      <span className="mt-5 text-lg font-semibold tracking-[-0.035em] text-[var(--app-text)]">Add integration</span>
                      <span className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">Name a draft, then use the AI builder to design tools, auth, prompts, and permission reviews.</span>
                    </span>
                  </button>
                ) : workspaces.map((workspace) => {
                  const selected = workspace.workspaceId === selectedWorkspace?.workspaceId
                  return (
                    <button
                      key={workspace.workspaceId}
                      type="button"
                      onClick={() => void handleOpenWorkspace(workspace)}
                      className={selected
                        ? 'rounded-3xl border border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] p-5 text-left shadow-sm transition'
                        : 'rounded-3xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5 text-left shadow-sm transition hover:border-[var(--app-border-accent)] hover:bg-[var(--app-surface-hover)]'}
                    >
                      <div className="flex flex-col gap-5 lg:flex-row lg:items-center lg:justify-between">
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="inline-flex items-center gap-1 rounded-full border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 py-1 text-[11px] font-medium uppercase tracking-[0.08em] text-[var(--app-text-muted)]">
                              <CheckCircle2 size={12} /> {workspaceStatus(workspace)}
                            </span>
                            <span className="rounded-full border border-dashed border-[var(--app-border)] px-2.5 py-1 text-[11px] text-[var(--app-text-subtle)]">execution inactive</span>
                          </div>
                          <h2 className="mt-3 truncate text-xl font-semibold tracking-[-0.04em] text-[var(--app-text)]">{workspace.displayName || 'New integration'}</h2>
                          <p className="mt-2 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
                            {workspace.packId ? `Pack ${workspace.packId}` : 'Draft workspace'}{workspace.draftVersionId ? ` · ${workspace.draftVersionId}` : ''}. Review settings, tools, prompts, and permissions before any runtime adapter is enabled.
                          </p>
                        </div>
                        <div className="grid min-w-[220px] gap-2 text-xs text-[var(--app-text-muted)] sm:grid-cols-2 lg:text-right">
                          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2">
                            <div className="font-medium text-[var(--app-text)]">Settings</div>
                            <div>draft metadata</div>
                          </div>
                          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2">
                            <div className="font-medium text-[var(--app-text)]">AI chats</div>
                            <div>{workspace.latestChildSessionId ? 'latest saved' : 'none yet'}</div>
                          </div>
                        </div>
                      </div>
                    </button>
                  )
                })}
              </div>

              {selectedWorkspace ? (
                <div className="mt-8 grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
                  <Card className="border-[var(--app-border)] bg-[var(--app-surface)] p-5">
                    <div className="flex items-start justify-between gap-4">
                      <div>
                        <p className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Selected integration</p>
                        <h2 className="mt-2 text-2xl font-semibold tracking-[-0.05em]">{selectedWorkspace.displayName}</h2>
                      </div>
                      <Button variant="outline" onClick={() => setAiSidebarOpen((open) => !open)}>
                        {aiSidebarOpen ? <ChevronRight size={15} /> : <ChevronLeft size={15} />}
                        {aiSidebarOpen ? 'Hide AI' : 'Show AI'}
                      </Button>
                    </div>
                    <div className="mt-5 grid gap-3 sm:grid-cols-3">
                      <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                        <Settings2 className="text-[var(--app-text-subtle)]" size={17} />
                        <div className="mt-3 text-sm font-semibold">Settings</div>
                        <div className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Draft metadata and pack context are inspectable.</div>
                      </div>
                      <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                        <Bot className="text-[var(--app-text-subtle)]" size={17} />
                        <div className="mt-3 text-sm font-semibold">Builder</div>
                        <div className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Integration workspace chats.</div>
                      </div>
                      <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                        <Sparkles className="text-[var(--app-text-subtle)]" size={17} />
                        <div className="mt-3 text-sm font-semibold">Validation</div>
                        <div className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">Planned in a later checkpoint.</div>
                      </div>
                    </div>
                  </Card>
                  <Card className="border-[var(--app-border)] bg-[var(--app-surface)] p-5">
                    <p className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">What to give AI</p>
                    <ol className="mt-4 space-y-3 text-sm leading-6 text-[var(--app-text-muted)]">
                      <li><span className="font-medium text-[var(--app-text)]">1.</span> Paste API docs or local CLI help.</li>
                      <li><span className="font-medium text-[var(--app-text)]">2.</span> Say which actions need human review.</li>
                      <li><span className="font-medium text-[var(--app-text)]">3.</span> Review generated pack records before enabling runtime work.</li>
                    </ol>
                  </Card>
                </div>
              ) : null}
            </div>
          </section>

          <aside className={`${aiSidebarOpen && selectedWorkspace ? 'w-[420px] xl:w-[480px]' : 'w-[48px]'} flex h-full shrink-0 border-l border-[var(--app-border)] bg-[var(--app-surface)] transition-[width] duration-200`}>
            {aiSidebarOpen && selectedWorkspace ? (
              <div className="flex min-w-0 flex-1 flex-col">
                <div className="shrink-0 border-b border-[var(--app-border)] px-3 py-2">
                  <div className="flex items-center justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex min-w-0 items-center gap-2 text-sm font-semibold text-[var(--app-text)]">
                        <Sparkles size={15} className="text-[var(--app-primary)]" />
                        <span className="truncate">Integration workspace</span>
                      </div>
                      <div className="mt-0.5 truncate text-[11px] text-[var(--app-text-muted)]">{selectedWorkspace.displayName}</div>
                    </div>
                    <button type="button" className="grid h-8 w-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => setAiSidebarOpen(false)} aria-label="Collapse AI builder">
                      <ChevronRight size={16} />
                    </button>
                  </div>
                  <div className="mt-3 flex items-center gap-2 overflow-x-auto pb-1 [scrollbar-width:none] [-ms-overflow-style:none] [&::-webkit-scrollbar]:hidden">
                    <button
                      type="button"
                      onClick={() => { void handleCreateChildSession() }}
                      className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2.5 text-xs font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"
                    >
                      <MessageSquarePlus size={14} /> New Chat
                    </button>
                    {childSessions.map((child, index) => (
                      <button
                        key={child.session.id}
                        type="button"
                        onClick={() => { void handleSwitchChildSession(child.session.id) }}
                        className={child.session.id === selectedSession?.id
                          ? 'inline-flex h-8 max-w-[170px] shrink-0 items-center rounded-xl border border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] px-2.5 text-xs font-medium text-[var(--app-text)]'
                          : 'inline-flex h-8 max-w-[170px] shrink-0 items-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-2.5 text-xs text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}
                        title={child.title}
                      >
                        <span className="truncate">{child.title || `Chat ${index + 1}`}</span>
                      </button>
                    ))}
                  </div>
                </div>
                <div className="min-h-0 flex-1 overflow-y-auto px-4 py-6 text-sm leading-6 text-[var(--app-text-muted)]">
                  The old Desktop chat runtime has been removed from the integrations sidebar. Builder chat UI will be reintroduced through the new integration path in a later checkpoint.
                </div>
              </div>
            ) : (
              <button type="button" className="flex h-full w-full items-start justify-center pt-4 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => setAiSidebarOpen(true)} aria-label="Expand AI builder">
                <ChevronLeft size={18} />
              </button>
            )}
          </aside>
        </div>
      </main>

      {createDialogOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Name integration" className="z-[80] p-4">
          <DialogBackdrop onClick={creatingIntegration ? undefined : () => setCreateDialogOpen(false)} />
          <DialogPanel className="w-[min(460px,calc(100vw-32px))] rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
            <form
              className="p-5"
              onSubmit={(event) => {
                event.preventDefault()
                void handleCreateIntegration()
              }}
            >
              <h2 className="text-lg font-semibold tracking-[-0.04em] text-[var(--app-text)]">Name this integration</h2>
              <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">Use a human name like “GitHub publisher” or “Stripe reporting”.</p>
              <Input
                autoFocus
                value={newIntegrationName}
                onChange={(event) => setNewIntegrationName(event.currentTarget.value)}
                placeholder="Integration name"
                className="mt-5"
              />
              {createError ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{createError}</div> : null}
              <div className="mt-5 flex justify-end gap-2">
                <Button variant="ghost" type="button" onClick={() => setCreateDialogOpen(false)} disabled={creatingIntegration}>Cancel</Button>
                <Button type="submit" disabled={creatingIntegration}>
                  {creatingIntegration ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
                  Create
                </Button>
              </div>
            </form>
          </DialogPanel>
        </Dialog>
      ) : null}
    </div>
  )
}
