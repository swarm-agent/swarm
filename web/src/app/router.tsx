import { createRootRoute, createRoute, createRouter, lazyRouteComponent } from '@tanstack/react-router'
import { DesktopVaultShell } from '../features/desktop/vault/components/desktop-vault-shell'

const WorkspaceHomePage = lazyRouteComponent(() => import('../features/workspaces/pages/workspace-home-page'), 'WorkspaceHomePage')
const DesktopAppPage = lazyRouteComponent(() => import('../features/desktop/layout/desktop-app-page'), 'DesktopAppPage')
const DesktopSwarmPage = lazyRouteComponent(() => import('../features/desktop/swarm/pages/desktop-swarm-page'), 'DesktopSwarmPage')

function validateWorkspaceParams(params: Record<string, unknown>): { workspaceSlug: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  return { workspaceSlug }
}

function validateWorkspaceSessionParams(params: Record<string, unknown>): { workspaceSlug: string; sessionId: string } {
  const workspaceSlug = typeof params.workspaceSlug === 'string' ? params.workspaceSlug.trim() : ''
  const sessionId = typeof params.sessionId === 'string' ? params.sessionId.trim() : ''
  return { workspaceSlug, sessionId }
}

const rootRoute = createRootRoute({
  component: DesktopVaultShell,
})

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/',
  component: WorkspaceHomePage,
})

const swarmRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/swarm',
  component: DesktopSwarmPage,
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

const workspaceSwarmRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/$workspaceSlug/swarm',
  parseParams: validateWorkspaceParams,
  component: DesktopSwarmPage,
})

const routeTree = rootRoute.addChildren([indexRoute, swarmRoute, workspaceRoute, workspaceSessionRoute, workspaceSwarmRoute])

export const router = createRouter({
  routeTree,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
