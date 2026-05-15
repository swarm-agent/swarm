import { useCallback, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, ChevronLeft, ChevronRight, Link2, Loader2, Plus, Sparkles } from 'lucide-react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { DesktopChatPanel } from '../../chat/components/desktop-chat-panel'
import { fetchDraftModelPreference, fetchSession } from '../../chat/queries/chat-queries'
import type { DesktopSessionRecord } from '../../types/realtime'
import { useDesktopStore } from '../../state/use-desktop-store'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { getSwarmSettings } from '../../settings/swarm/queries/get-swarm-settings'
import { normalizeGlobalThemeSettings } from '../../settings/swarm/types/swarm-settings'
import { createWorkspaceThemeStyle } from '../../../workspaces/launcher/services/workspace-theme'
import { SwarmToolSidebar } from '../../tools/components/swarm-tool-sidebar'
import {
  createIntegrationBuilderSession,
  fetchIntegrationBuilderSessions,
  INTEGRATION_BUILDER_WORKSPACE_NAME,
  INTEGRATION_BUILDER_WORKSPACE_PATH,
  isIntegrationBuilderSession,
} from '../services/integration-builder-sessions'

function integrationSessionSubtitle(session: DesktopSessionRecord): string {
  if (session.live.status === 'running' || session.live.status === 'starting') return 'AI working'
  if (session.pendingPermissionCount > 0) return 'needs review'
  if (session.messageCount > 0) return `${session.messageCount} messages`
  return 'draft integration'
}

function integrationDisplayName(session: DesktopSessionRecord | null): string {
  return session?.title?.trim() || 'New integration'
}

export function IntegrationsPage() {
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const queryClient = useQueryClient()
  const routeSessionMatch = matchRoute({ to: '/integrations/$sessionId', fuzzy: false })
  const routeSessionId = routeSessionMatch ? routeSessionMatch.sessionId.trim() : ''
  const setActiveSession = useDesktopStore((state) => state.setActiveSession)
  const setActiveWorkspacePath = useDesktopStore((state) => state.setActiveWorkspacePath)
  const upsertSession = useDesktopStore((state) => state.upsertSession)

  const [newSessionTitle, setNewSessionTitle] = useState('')
  const [creatingSession, setCreatingSession] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [darkModeEnabled, setDarkModeEnabled] = useState(false)
  const [createDialogOpen, setCreateDialogOpen] = useState(false)
  const [aiSidebarOpen, setAiSidebarOpen] = useState(true)

  const sessionsQuery = useQuery({
    queryKey: ['integration-builder-sessions'],
    queryFn: () => fetchIntegrationBuilderSessions(200),
    staleTime: 15_000,
  })
  const routeSessionQuery = useQuery({
    queryKey: ['integration-builder-session', routeSessionId],
    queryFn: () => fetchSession(routeSessionId),
    enabled: routeSessionId !== '' && !(sessionsQuery.data ?? []).some((session) => session.id === routeSessionId),
    staleTime: 15_000,
  })
  const draftModelQuery = useQuery({
    queryKey: ['draft-model'],
    queryFn: () => fetchDraftModelPreference(),
    staleTime: 60_000,
  })
  const uiSettingsQuery = useQuery(uiSettingsQueryOptions())
  const swarmSettingsQuery = useQuery({
    queryKey: ['swarm-settings'],
    queryFn: getSwarmSettings,
    staleTime: 30_000,
  })

  const sessions = useMemo(() => {
    const byId = new Map<string, DesktopSessionRecord>()
    for (const session of sessionsQuery.data ?? []) byId.set(session.id, session)
    const routeSession = routeSessionQuery.data
    if (routeSession && isIntegrationBuilderSession(routeSession)) byId.set(routeSession.id, routeSession)
    return [...byId.values()].sort((left, right) => right.updatedAt - left.updatedAt)
  }, [routeSessionQuery.data, sessionsQuery.data])
  const selectedSession = routeSessionId
    ? sessions.find((session) => session.id === routeSessionId) ?? null
    : null
  const selectedSessionIsLoading = Boolean(routeSessionId && routeSessionQuery.isLoading && !selectedSession)
  const swarmName = swarmSettingsQuery.data?.name?.trim() || 'Local'
  const userThemeId = normalizeGlobalThemeSettings(uiSettingsQuery.data).activeId
  const darkOverrideButtonStyle = useMemo(() => createWorkspaceThemeStyle(userThemeId, '--integration-builder-theme'), [userThemeId])
  const hasSessions = sessions.length > 0

  const handleSelectSession = useCallback((sessionId: string) => {
    const session = sessions.find((item) => item.id === sessionId)
    if (session) {
      setActiveSession(session.id)
      setActiveWorkspacePath(session.workspacePath || null)
    }
    setAiSidebarOpen(true)
    void navigate({ to: '/integrations/$sessionId', params: { sessionId } })
  }, [navigate, sessions, setActiveSession, setActiveWorkspacePath])

  const handleSessionCreated = useCallback((session: DesktopSessionRecord) => {
    upsertSession(session)
    setActiveSession(session.id)
    setActiveWorkspacePath(session.workspacePath || null)
    queryClient.setQueryData<DesktopSessionRecord[]>(['integration-builder-sessions'], (current = []) => {
      const withoutSession = current.filter((item) => item.id !== session.id)
      return [session, ...withoutSession]
    })
    setAiSidebarOpen(true)
    void navigate({ to: '/integrations/$sessionId', params: { sessionId: session.id } })
  }, [navigate, queryClient, setActiveSession, setActiveWorkspacePath, upsertSession])

  const handleCreateSession = useCallback(async () => {
    const title = newSessionTitle.trim()
    if (!title) {
      setCreateError('Name the integration first.')
      setCreateDialogOpen(true)
      return
    }
    const preference = draftModelQuery.data?.preference
    if (!preference?.provider || !preference.model || !preference.thinking) {
      setCreateError('Select an authenticated model before starting an integration builder session.')
      setCreateDialogOpen(true)
      return
    }
    setCreatingSession(true)
    setCreateError(null)
    try {
      const created = await createIntegrationBuilderSession({
        title,
        mode: 'plan',
        agentName: 'swarm',
        preference,
      })
      handleSessionCreated(created)
      setNewSessionTitle('')
      setCreateDialogOpen(false)
      await queryClient.invalidateQueries({ queryKey: ['integration-builder-sessions'] })
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : 'Failed to start integration builder session')
    } finally {
      setCreatingSession(false)
    }
  }, [draftModelQuery.data?.preference, handleSessionCreated, newSessionTitle, queryClient])

  const openCreateDialog = useCallback(() => {
    setCreateError(null)
    setCreateDialogOpen(true)
  }, [])

  const handleStartNewSession = useCallback(() => {
    setActiveSession(null)
    setActiveWorkspacePath(null)
    setCreateError(null)
    setCreateDialogOpen(true)
    void navigate({ to: '/integrations' })
  }, [navigate, setActiveSession, setActiveWorkspacePath])

  const sidebarSessions = sessions.map((session) => ({
    id: session.id,
    title: session.title || 'New integration',
    subtitle: integrationSessionSubtitle(session),
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
        toolDescription="Swarm-wide integration drafts. Open one to build tools with AI and review permissions before anything runs."
        createLabel="Create"
        createTitle={newSessionTitle}
        onCreateTitleChange={setNewSessionTitle}
        createPlaceholder="Integration name"
        onCreate={openCreateDialog}
        creating={creatingSession}
        createButtonLabel="Add integration"
        creatingButtonLabel="Starting…"
        sessionsLabel="Integrations"
        sessionsLoading={sessionsQuery.isLoading || selectedSessionIsLoading}
        sessions={sidebarSessions}
        selectedSessionId={selectedSession?.id ?? null}
        onSelectSession={handleSelectSession}
        emptySessionsMessage="No integrations yet. Add one from the main panel."
        defaultSessionTitle="New integration"
      >
        {createError ? <div className="mt-3 border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-[11px] leading-5 text-[var(--app-danger)]">{createError}</div> : null}
      </SwarmToolSidebar>

      <main className="min-w-0 flex-1 overflow-hidden">
        {selectedSession ? (
          <div className="flex h-full min-w-0 bg-[var(--app-bg)]">
            <section className="min-w-0 flex-1 overflow-y-auto px-7 py-8 sm:px-10 sm:py-10 lg:px-14">
              <div className="mx-auto w-full max-w-5xl">
                <div className="flex items-start justify-between gap-5 border-b border-[var(--app-border)] pb-6">
                  <div className="min-w-0">
                    <p className="text-[11px] font-medium uppercase tracking-[0.24em] text-[var(--app-text-subtle)]">Integration draft</p>
                    <h1 className="mt-2 truncate text-3xl font-semibold tracking-[-0.055em] text-[var(--app-text)]">{integrationDisplayName(selectedSession)}</h1>
                    <p className="mt-3 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
                      Use the AI builder on the right. Paste API docs, auth notes, endpoint examples, or links. Swarm will turn them into tools, permission controls, and a final review request before anything is enabled.
                    </p>
                  </div>
                  <Button variant="outline" className="shrink-0" onClick={() => setAiSidebarOpen((open) => !open)}>
                    {aiSidebarOpen ? <ChevronRight size={15} /> : <ChevronLeft size={15} />}
                    {aiSidebarOpen ? 'Hide AI' : 'Show AI'}
                  </Button>
                </div>

                <div className="mt-8 grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
                  <div className="rounded-2xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-6">
                    <p className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Workspace preview</p>
                    <div className="mt-5 min-h-[300px] rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg)] p-5">
                      <div className="h-3 w-40 rounded-full bg-[var(--app-surface-hover)]" />
                      <div className="mt-4 h-3 w-64 rounded-full bg-[var(--app-surface-hover)]" />
                      <div className="mt-8 grid gap-3 sm:grid-cols-2">
                        <div className="h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]" />
                        <div className="h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]" />
                      </div>
                      <div className="mt-4 h-28 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]" />
                    </div>
                  </div>

                  <aside className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5">
                    <p className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">What to give the AI</p>
                    <ol className="mt-4 space-y-4 text-sm leading-6 text-[var(--app-text-muted)]">
                      <li><span className="font-medium text-[var(--app-text)]">1.</span> Paste API docs or a link to the docs.</li>
                      <li><span className="font-medium text-[var(--app-text)]">2.</span> Tell it what actions humans should review.</li>
                      <li><span className="font-medium text-[var(--app-text)]">3.</span> Review the proposed tools, auth, and permissions at the end.</li>
                    </ol>
                  </aside>
                </div>
              </div>
            </section>

            <aside className={`${aiSidebarOpen ? 'w-[420px] xl:w-[480px]' : 'w-[48px]'} flex h-full shrink-0 border-l border-[var(--app-border)] bg-[var(--app-surface)] transition-[width] duration-200`}>
              {aiSidebarOpen ? (
                <div className="flex min-w-0 flex-1 flex-col">
                  <div className="flex h-12 shrink-0 items-center justify-between border-b border-[var(--app-border)] px-3">
                    <div className="flex min-w-0 items-center gap-2 text-sm font-medium text-[var(--app-text)]">
                      <Sparkles size={15} className="text-[var(--app-primary)]" />
                      <span className="truncate">AI builder</span>
                    </div>
                    <button type="button" className="grid h-8 w-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => setAiSidebarOpen(false)} aria-label="Collapse AI builder">
                      <ChevronRight size={16} />
                    </button>
                  </div>
                  <DesktopChatPanel
                    hostSwarmName={swarmName}
                    workspacePath={selectedSession.workspacePath || INTEGRATION_BUILDER_WORKSPACE_PATH}
                    workspaceName={selectedSession.workspaceName || INTEGRATION_BUILDER_WORKSPACE_NAME}
                    workspaceWorktreeEnabled={false}
                    workspaceTopologyRoutes={[]}
                    session={selectedSession}
                    sessionCreateOverride={createIntegrationBuilderSession}
                    onSessionCreated={handleSessionCreated}
                    onOpenSettingsTab={(tab) => void navigate({ to: '/settings', search: { tab } })}
                    onOpenQuickSettings={(tab) => void navigate({ to: '/settings', search: { tab } })}
                    onOpenPermissions={() => void navigate({ to: '/settings', search: { tab: 'permissions' } })}
                    onOpenWorkspaceLauncher={() => void navigate({ to: '/' })}
                    onOpenSidebarMenu={() => {}}
                    onStartNewSession={handleStartNewSession}
                    compactHeader
                    emptyStateMessage="Paste API docs, endpoint examples, auth notes, or links. I’ll build the integration tools and permission review flow."
                  />
                </div>
              ) : (
                <button type="button" className="flex h-full w-full items-start justify-center pt-4 text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" onClick={() => setAiSidebarOpen(true)} aria-label="Expand AI builder">
                  <ChevronLeft size={18} />
                </button>
              )}
            </aside>
          </div>
        ) : selectedSessionIsLoading ? (
          <div className="flex h-full items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <Loader2 className="mx-auto mb-3 animate-spin text-[var(--app-primary)]" size={24} />
              <div className="text-lg font-semibold">Loading integration…</div>
            </Card>
          </div>
        ) : routeSessionId ? (
          <div className="flex h-full items-center justify-center px-6">
            <Card className="max-w-lg border-[var(--app-border)] bg-[var(--app-surface)] p-6 text-center">
              <div className="text-lg font-semibold">Integration not found</div>
              <p className="mt-2 text-sm text-[var(--app-text-muted)]">We couldn’t find that builder session on this swarm.</p>
            </Card>
          </div>
        ) : (
          <div className="h-full overflow-y-auto">
            <div className="mx-auto flex min-h-full w-full max-w-6xl flex-col px-7 pb-10 pt-10 sm:px-10 sm:pt-12 lg:px-14 lg:pt-14">
              <header className="flex flex-col gap-6 border-b border-[var(--app-border)] pb-7 sm:flex-row sm:items-end sm:justify-between">
                <div className="min-w-0">
                  <Button variant="ghost" className="mb-7 h-9 rounded-xl px-3 text-[var(--app-text-muted)]" onClick={() => void navigate({ to: '/' })}>
                    <ArrowLeft size={15} />
                    Back to launcher
                  </Button>
                  <p className="text-[11px] font-medium uppercase tracking-[0.28em] text-[var(--app-text-subtle)]">Swarm-wide</p>
                  <h1 className="mt-2 text-4xl font-semibold tracking-[-0.065em] text-[var(--app-text)]">Integrations</h1>
                  <p className="mt-4 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">
                    Create integrations by talking to AI. Name it first, then paste API docs or links and review the generated tools before enabling them.
                  </p>
                </div>
              </header>

              <main className="flex-1 py-8">
                <button
                  type="button"
                  onClick={openCreateDialog}
                  className="group flex min-h-[260px] w-full items-center justify-center rounded-3xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-8 text-center transition hover:border-[var(--app-primary)] hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
                >
                  <span className="flex max-w-md flex-col items-center">
                    <span className="grid h-14 w-14 place-items-center rounded-2xl border border-dashed border-[var(--app-border-strong)] bg-[var(--app-bg)] text-[var(--app-text-muted)] transition group-hover:border-[var(--app-primary)] group-hover:text-[var(--app-primary)]">
                      <Plus size={22} strokeWidth={1.8} />
                    </span>
                    <span className="mt-5 text-lg font-semibold tracking-[-0.035em] text-[var(--app-text)]">Add integration</span>
                    <span className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">This empty space becomes the integration dashboard after the AI builds it.</span>
                  </span>
                </button>

                {hasSessions ? (
                  <section className="mt-8">
                    <p className="text-xs font-medium uppercase tracking-[0.2em] text-[var(--app-text-subtle)]">Recent drafts</p>
                    <div className="mt-3 divide-y divide-[var(--app-border)] border-y border-[var(--app-border)]">
                      {sessions.slice(0, 5).map((session) => (
                        <button key={session.id} type="button" onClick={() => handleSelectSession(session.id)} className="flex w-full items-center justify-between gap-4 py-3 text-left hover:text-[var(--app-primary)]">
                          <span className="min-w-0">
                            <span className="block truncate text-sm font-medium text-[var(--app-text)]">{session.title || 'New integration'}</span>
                            <span className="mt-1 block text-xs text-[var(--app-text-muted)]">{integrationSessionSubtitle(session)}</span>
                          </span>
                          <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" />
                        </button>
                      ))}
                    </div>
                  </section>
                ) : null}
              </main>
            </div>
          </div>
        )}
      </main>

      {createDialogOpen ? (
        <Dialog role="dialog" aria-modal="true" aria-label="Name integration" className="z-[80] p-4">
          <DialogBackdrop onClick={creatingSession ? undefined : () => setCreateDialogOpen(false)} />
          <DialogPanel className="w-[min(460px,calc(100vw-32px))] rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
            <form
              className="p-5"
              onSubmit={(event) => {
                event.preventDefault()
                void handleCreateSession()
              }}
            >
              <h2 className="text-lg font-semibold tracking-[-0.04em] text-[var(--app-text)]">Name this integration</h2>
              <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">Use a human name like “GitHub publisher” or “Stripe reporting”.</p>
              <Input
                autoFocus
                value={newSessionTitle}
                onChange={(event) => setNewSessionTitle(event.currentTarget.value)}
                placeholder="Integration name"
                className="mt-5"
              />
              {createError ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{createError}</div> : null}
              <div className="mt-5 flex justify-end gap-2">
                <Button variant="ghost" type="button" onClick={() => setCreateDialogOpen(false)} disabled={creatingSession}>Cancel</Button>
                <Button type="submit" disabled={creatingSession}>
                  {creatingSession ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}
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
