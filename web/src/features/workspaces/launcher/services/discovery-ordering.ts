import { formatWorkspacePath } from './workspace-format'
import type { WorkspaceDiscoverEntry } from '../types/workspace'

export function sortDiscoveredWorkspaces(entries: WorkspaceDiscoverEntry[]): WorkspaceDiscoverEntry[] {
  return [...entries].sort((left, right) => {
    if (left.hasClaude !== right.hasClaude) {
      return left.hasClaude ? -1 : 1
    }
    if (left.hasSwarm !== right.hasSwarm) {
      return left.hasSwarm ? -1 : 1
    }
    if (left.isGitRepo !== right.isGitRepo) {
      return left.isGitRepo ? -1 : 1
    }
    if (left.lastModified !== right.lastModified) {
      return right.lastModified - left.lastModified
    }
    return left.path.localeCompare(right.path)
  })
}

export function dedupeDiscoveredAgainstWorkspaces(entries: WorkspaceDiscoverEntry[], workspacePaths: string[]): WorkspaceDiscoverEntry[] {
  const occupied = new Set(workspacePaths.map((path) => path.trim()))
  return entries.filter((entry) => !occupied.has(entry.path.trim()))
}

export function displayDiscoveredPath(path: string): string {
  return formatWorkspacePath(path)
}
