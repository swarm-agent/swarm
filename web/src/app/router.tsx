import { createRootRoute, createRoute, createRouter, lazyRouteComponent, useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'
import { DesktopDocumentTitleController } from '../features/desktop/runtime/desktop-document-title-controller'
import { DesktopVaultShell } from '../features/desktop/vault/components/desktop-vault-shell'
import { useWorkspaceLauncher } from '../features/workspaces/launcher/state/use-workspace-launcher'
import { workspaceRouteSlugBase } from '../features/workspaces/launcher/services/workspace-route'

const WorkspaceHomePage = lazyRouteComponent(() => import('../features/workspaces/pages/workspace-home-page'), 'WorkspaceHomePage')
const importDesktopAppPage = () => import('../features/desktop/layout/desktop-app-page')
const DesktopAppPage = lazyRouteComponent(importDesktopAppPage, 'DesktopAppPage')
const DesktopSettingsPage = lazyRouteComponent(() => import('../features/desktop/settings/components/desktop-settings-page'), 'DesktopSettingsPage')
const IntegrationsPage = lazyRouteComponent(() => import('../features/desktop/integrations/pages/integrations-page'), 'IntegrationsPage')
const SwarmToolsPage = lazyRouteComponent(() => import('../features/desktop/tools/pages/swarm-tools-page'), 'SwarmToolsPage')
const VideoToolPage = lazyRouteComponent(() => import('../features/desktop/tools/pages/video-tool-page'), 'VideoToolPage')
const ImageToolPage = lazyRouteComponent(() => import('../features/desktop/tools/pages/image-tool-page'), 'ImageToolPage')
const ROOT_RESERVED_ROUTE_SEGMENTS = new Set(['settings', 'integrations', 'tools', 'agents'])
const WORKSPACE_RESERVED_ROUTE_SEGMENTS = new Set(['settings', 'tools', 'task', 'worktree'])

function currentWorkspaceRoute(pathname: string): { sessionId?: string } | null {
  const parts = pathname.split('/').map((part) => decodeURIComponent(part).trim()).filter(Boolean)
  if (parts.length !== 1 && parts.length !== 2) {
    return null
  }
  const [workspaceSlug, sessionId] = parts
  if (!workspaceSlug || ROOT_RESERVED_ROUTE_SEGMENTS.has(workspaceSlug)) {
    return null
  }
  if (sessionId && WORKSPACE_RESERVED_ROUTE_SEGMENTS.has(sessionId)) {
    return null
  }
  return { sessionId: sessionId || undefined }
}

function currentWorkspaceSessionRoute(pathname: string): { sessionId: string } | null {
  const route = currentWorkspaceRoute(pathname)
  return route?.sessionId ? { sessionId: route.sessionId } : null
}

if (typeof window !== 'undefined') {
  const route = currentWorkspaceSessionRoute(window.location.pathname)
  if (route) {
    void importDesktopAppPage()
  }
}

function validateWorkspaceParams(params: Record<string, unknown>): { workspaceSlug: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  return { workspaceSlug }
}

function validateImageToolParams(params: Record<string, unknown>): { imageSessionId: string } {
  const imageSessionId = typeof params.imageSessionId === 'string' ? params.imageSessionId.trim() : ''
  return { imageSessionId }
}

function validateIntegrationSessionParams(params: Record<string, unknown>): { sessionId: string } {
  const sessionId = typeof params.sessionId === 'string' ? params.sessionId.trim() : ''
  return { sessionId }
}

function validateWorkspaceImageToolParams(params: Record<string, unknown>): { workspaceSlug: string; imageSessionId: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  const imageSessionId = typeof params.imageSessionId === 'string' ? params.imageSessionId.trim() : ''
  return { workspaceSlug, imageSessionId }
}

function validateWorkspaceSessionParams(params: Record<string, unknown>): { workspaceSlug: string; sessionId: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  const sessionId = typeof params.sessionId === 'string' ? params.sessionId.trim() : ''
  return { workspaceSlug, sessionId }
}

function validateSettingsSearch(search: Record<string, unknown>): { tab?: string; returnSessionId?: string; agentSetup?: string; agent?: string; newWorktree?: string; newPlan?: string } {
  const tab = typeof search.tab === 'string' ? search.tab.trim() : ''
  const returnSessionId = typeof search.returnSessionId === 'string' ? search.returnSessionId.trim() : ''
  const agentSetup = typeof search.agentSetup === 'string' ? search.agentSetup.trim() : ''
  const agent = typeof search.agent === 'string' ? search.agent.trim() : ''
  const newWorktree = typeof search.newWorktree === 'string' ? search.newWorktree.trim() : ''
  const newPlan = typeof search.newPlan === 'string' ? search.newPlan.trim() : ''
  return {
    ...(tab ? { tab } : {}),
    ...(returnSessionId ? { returnSessionId } : {}),
    ...(agentSetup ? { agentSetup } : {}),
    ...(agent ? { agent } : {}),
    ...(newWorktree ? { newWorktree } : {}),
    ...(newPlan ? { newPlan } : {}),
  }
}

function validateWorkspaceSessionSearch(search: Record<string, unknown>): ReturnType<typeof validateSettingsSearch> & { artifactSession?: string; artifact?: string; collection?: string } {
  const settingsSearch = validateSettingsSearch(search)
  const artifactSession = typeof search.artifactSession === 'string' ? search.artifactSession.trim() : ''
  const artifact = typeof search.artifact === 'string' ? search.artifact.trim() : ''
  const collection = typeof search.collection === 'string' ? search.collection.trim() : ''
  return {
    ...settingsSearch,
    ...(artifactSession ? { artifactSession } : {}),
    ...(artifact ? { artifact } : {}),
    ...(collection ? { collection } : {}),
  }
}

const rootRoute = createRootRoute({
  component: DesktopRootShell,
})

function AgentSetupRedirect() {
  const navigate = useNavigate()
  const { workspaces, currentWorkspacePath, loading } = useWorkspaceLauncher({ applyDocumentTheme: false, autoRefresh: false, browseDuringRefresh: false })
  useEffect(() => {
    if (loading) return
    const workspace = workspaces.find((entry) => entry.path === currentWorkspacePath) ?? workspaces[0]
    if (!workspace) {
      void navigate({ to: '/', replace: true })
      return
    }
    void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: workspaceRouteSlugBase(workspace) }, search: { agentSetup: '1', agent: 'swarm' }, replace: true })
  }, [currentWorkspacePath, loading, navigate, workspaces])
  return null
}

function DesktopRootShell() {
  const initialPreferredSessionId = initialDesktopV3PreferredSessionId()
  return (
    <>
      <DesktopDocumentTitleController />
      <DesktopVaultShell initialPreferredSessionId={initialPreferredSessionId} />
    </>
  )
}

function initialDesktopV3PreferredSessionId(): string | null | undefined {
  if (typeof window === 'undefined') return undefined
  const route = currentWorkspaceRoute(window.location.pathname)
  if (!route) return undefined
  return route.sessionId || null
}

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: WorkspaceHomePage,
})

const agentsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/agents',
  component: AgentSetupRedirect,
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  validateSearch: validateSettingsSearch,
  component: DesktopSettingsPage,
})

const integrationsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/integrations',
  component: IntegrationsPage,
})

const integrationSessionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/integrations/$sessionId',
  parseParams: validateIntegrationSessionParams,
  component: IntegrationsPage,
})

const toolsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/tools',
  component: SwarmToolsPage,
})

const videoToolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/tools/video',
  component: VideoToolPage,
})

const imageToolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/tools/image',
  component: ImageToolPage,
})

const imageToolSessionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/tools/image/$imageSessionId',
  parseParams: validateImageToolParams,
  component: ImageToolPage,
})

const workspaceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug',
  parseParams: validateWorkspaceParams,
  validateSearch: validateSettingsSearch,
  component: DesktopAppPage,
})

const workspaceSessionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/$sessionId',
  parseParams: validateWorkspaceSessionParams,
  validateSearch: validateWorkspaceSessionSearch,
  loader: ({ params }) => {
    const sessionId = params.sessionId.trim()
    if (!sessionId) {
      return null
    }
    return { sessionId }
  },
  component: DesktopAppPage,
})

const workspaceTaskRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/task',
  parseParams: validateWorkspaceParams,
  component: DesktopAppPage,
})

const workspaceWorktreeRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/worktree',
  parseParams: validateWorkspaceParams,
  component: DesktopAppPage,
})

const workspaceSettingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/settings',
  parseParams: validateWorkspaceParams,
  validateSearch: validateSettingsSearch,
  component: DesktopSettingsPage,
})

const workspaceToolsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/tools',
  parseParams: validateWorkspaceParams,
  component: SwarmToolsPage,
})

const workspaceVideoToolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/tools/video',
  parseParams: validateWorkspaceParams,
  component: VideoToolPage,
})

const workspaceImageToolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/tools/image',
  parseParams: validateWorkspaceParams,
  component: ImageToolPage,
})

const workspaceImageToolSessionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/tools/image/$imageSessionId',
  parseParams: validateWorkspaceImageToolParams,
  component: ImageToolPage,
})

const routeTree = rootRoute.addChildren([
  indexRoute,
  settingsRoute,
  agentsRoute,
  integrationsRoute,
  integrationSessionRoute,
  toolsRoute,
  videoToolRoute,
  imageToolRoute,
  imageToolSessionRoute,
  workspaceRoute,
  workspaceSessionRoute,
  workspaceTaskRoute,
  workspaceWorktreeRoute,
  workspaceSettingsRoute,
  workspaceToolsRoute,
  workspaceVideoToolRoute,
  workspaceImageToolRoute,
  workspaceImageToolSessionRoute,
])

export const router = createRouter({
  routeTree,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
