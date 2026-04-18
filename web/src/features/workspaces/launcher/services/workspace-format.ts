export function formatWorkspacePath(path: string): string {
  return path.replace(/^\/home\/[^/]+/, '~')
}

export function formatWorkspaceDirectories(directories: string[]): string[] {
  return directories.map(formatWorkspacePath)
}
