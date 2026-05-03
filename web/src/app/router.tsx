import { createRootRoute, createRoute, createRouter, lazyRouteComponent } from '@tanstack/react-router'
import { DesktopVaultShell } from '../features/desktop/vault/components/desktop-vault-shell'

const WorkspaceHomePage = lazyRouteComponent(() => import('../features/workspaces/pages/workspace-home-page'), 'WorkspaceHomePage')
const DesktopAppPage = lazyRouteComponent(() => import('../features/desktop/layout/desktop-app-page'), 'DesktopAppPage')
const DesktopSettingsPage = lazyRouteComponent(() => import('../features/desktop/settings/components/desktop-settings-page'), 'DesktopSettingsPage')
const SwarmToolsPage = lazyRouteComponent(() => import('../features/desktop/tools/pages/swarm-tools-page'), 'SwarmToolsPage')
const VideoToolPage = lazyRouteComponent(() => import('../features/desktop/tools/pages/video-tool-page'), 'VideoToolPage')
const FlowRedirectRoute = lazyRouteComponent(() => import('./flow-redirect-route'), 'FlowRedirectRoute')

function validateWorkspaceParams(params: Record<string, unknown>): { workspaceSlug: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  return { workspaceSlug }
}

function validateWorkspaceSessionParams(params: Record<string, unknown>): { workspaceSlug: string; sessionId: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  const sessionId = typeof params.sessionId === 'string' ? params.sessionId.trim() : ''
  return { workspaceSlug, sessionId }
}

function validateSettingsSearch(search: Record<string, unknown>): { tab?: string } {
  const tab = typeof search.tab === 'string' ? search.tab.trim() : ''
  return tab ? { tab } : {}
}

const rootRoute = createRootRoute({
  component: DesktopVaultShell,
})

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: WorkspaceHomePage,
})

const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  validateSearch: validateSettingsSearch,
  component: DesktopSettingsPage,
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

const flowRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/flow',
  component: FlowRedirectRoute,
})

const workspaceRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug',
  parseParams: validateWorkspaceParams,
  component: DesktopAppPage,
})

const workspaceSessionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/$sessionId',
  parseParams: validateWorkspaceSessionParams,
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

const routeTree = rootRoute.addChildren([
  indexRoute,
  settingsRoute,
  toolsRoute,
  videoToolRoute,
  flowRoute,
  workspaceRoute,
  workspaceSessionRoute,
  workspaceSettingsRoute,
  workspaceToolsRoute,
  workspaceVideoToolRoute,
])

export const router = createRouter({
  routeTree,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
