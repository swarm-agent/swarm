import { bootstrapDesktopV3Sidebar } from './desktop-v3-bootstrap-controller'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'
import { selectSession } from './desktop-v3-cache-wire'

const ROOT_RESERVED_ROUTE_SEGMENTS = new Set(['settings', 'integrations', 'tools', 'flow'])
const WORKSPACE_RESERVED_ROUTE_SEGMENTS = new Set(['settings', 'tools'])

export interface DesktopV3StartupInput {
  shouldStart: boolean
  selectedSessionId?: string
}

export interface DesktopV3StartupHandle {
  bootstrap: Promise<void> | null
}

export function startDesktopV3Refresh(input: DesktopV3StartupInput): DesktopV3StartupHandle {
  if (!input.shouldStart) {
    return { bootstrap: null }
  }

  const selectedSessionId = input.selectedSessionId?.trim()
  if (selectedSessionId && getDesktopV3CacheSnapshot().selectedSessionId !== selectedSessionId) {
    dispatchDesktopV3Cache(selectSession(selectedSessionId))
  }

  return {
    bootstrap: bootstrapDesktopV3Sidebar(),
  }
}

export function readDesktopV3StartupInputFromLocation(location: Pick<Location, 'pathname'>): DesktopV3StartupInput {
  return readDesktopV3StartupInputFromPathname(location.pathname)
}

export function readDesktopV3StartupInputFromPathname(pathname: string): DesktopV3StartupInput {
  const parts = pathname.split('/').map((part) => decodeURIComponent(part).trim()).filter(Boolean)
  if (parts.length === 0) {
    return { shouldStart: false }
  }

  const [workspaceSlug, routeLeaf] = parts
  if (!workspaceSlug || ROOT_RESERVED_ROUTE_SEGMENTS.has(workspaceSlug)) {
    return { shouldStart: false }
  }

  if (parts.length === 1) {
    return { shouldStart: true }
  }

  if (!routeLeaf || WORKSPACE_RESERVED_ROUTE_SEGMENTS.has(routeLeaf)) {
    return { shouldStart: false }
  }

  if (routeLeaf === 'flow') {
    return { shouldStart: true }
  }

  return {
    shouldStart: true,
    selectedSessionId: routeLeaf,
  }
}
