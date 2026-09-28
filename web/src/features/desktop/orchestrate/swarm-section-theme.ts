import { createWorkspaceThemeStyle, workspaceThemeColorScheme, workspaceThemeExists } from '../../workspaces/launcher/services/workspace-theme'

/** A saved project theme takes precedence; an empty selection inherits the existing Orchestrate default. */
export function resolveSwarmProjectTheme(themeId: string | null | undefined) {
  const selected = (themeId || '').trim()
  if (!selected) return { state: 'inherited' as const, style: {}, colorScheme: null }
  if (!workspaceThemeExists(selected)) return { state: 'missing' as const, style: {}, colorScheme: null }
  return {
    state: 'selected' as const,
    style: createWorkspaceThemeStyle(selected, '--swarm'),
    colorScheme: workspaceThemeColorScheme(selected),
  }
}

export function projectThemePatch(themeId: string): { theme_id: string } {
  return { theme_id: themeId }
}
