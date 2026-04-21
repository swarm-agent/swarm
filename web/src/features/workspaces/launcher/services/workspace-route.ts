import type { WorkspaceEntry } from '../types/workspace'

const RESERVED_WORKSPACE_SLUGS = new Set([
  'swarm',
])

function slugifySegment(value: string): string {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')

  return normalized || 'workspace'
}

function fallbackWorkspaceName(workspace: Pick<WorkspaceEntry, 'path' | 'workspaceName'>): string {
  const trimmedName = workspace.workspaceName.trim()
  if (trimmedName) {
    return trimmedName
  }

  const trimmedPath = workspace.path.trim().replace(/[\\/]+$/, '')
  const parts = trimmedPath.split(/[\\/]/).filter((part) => part.trim() !== '')
  return parts[parts.length - 1] || 'workspace'
}

function pathHash(path: string): string {
  let hash = 2166136261
  for (let index = 0; index < path.length; index += 1) {
    hash ^= path.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function normalizeWorkspaceRouteBase(workspace: Pick<WorkspaceEntry, 'path' | 'workspaceName'>): string {
  const base = slugifySegment(fallbackWorkspaceName(workspace))
  if (RESERVED_WORKSPACE_SLUGS.has(base)) {
    return `${base}-workspace`
  }
  return base
}

export function workspaceRouteSlugBase(workspace: Pick<WorkspaceEntry, 'path' | 'workspaceName'>): string {
  return normalizeWorkspaceRouteBase(workspace)
}

export function buildWorkspaceRouteSlugMap(workspaces: readonly Pick<WorkspaceEntry, 'path' | 'workspaceName'>[]): Map<string, string> {
  const baseCounts = new Map<string, number>()
  for (const workspace of workspaces) {
    const base = workspaceRouteSlugBase(workspace)
    baseCounts.set(base, (baseCounts.get(base) ?? 0) + 1)
  }

  const slugByPath = new Map<string, string>()
  for (const workspace of workspaces) {
    const base = workspaceRouteSlugBase(workspace)
    const slug = (baseCounts.get(base) ?? 0) > 1 ? `${base}-${pathHash(workspace.path).slice(0, 6)}` : base
    slugByPath.set(workspace.path, slug)
  }

  return slugByPath
}

export function resolveWorkspaceBySlug<TWorkspace extends Pick<WorkspaceEntry, 'path' | 'workspaceName'>>(
  workspaces: readonly TWorkspace[],
  slug: string,
): TWorkspace | null {
  const normalizedSlug = slugifySegment(slug)
  if (!normalizedSlug) {
    return null
  }

  const slugByPath = buildWorkspaceRouteSlugMap(workspaces)
  for (const workspace of workspaces) {
    if (slugByPath.get(workspace.path) === normalizedSlug) {
      return workspace
    }
  }

  return null
}
