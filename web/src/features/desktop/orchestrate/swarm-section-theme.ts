import { createWorkspaceThemeStyle, workspaceThemeColorScheme, workspaceThemeExists } from '../../workspaces/launcher/services/workspace-theme'
import { ORCHESTRATE_THEMES } from './orchestrate-themes'
import type { OrchestrateThemeId } from './orchestrate-types'

/** Map the inherited Orchestrate palette to the same semantic boundary as saved themes. */
export function inheritedSwarmThemeStyle(themeId: OrchestrateThemeId): Record<string, string> {
  const theme = ORCHESTRATE_THEMES[themeId] || ORCHESTRATE_THEMES.modern_navy
  const vars = { ...ORCHESTRATE_THEMES.modern_navy.customVars, ...theme.customVars } as Record<string, string>
  const secondary = theme.secondaryColor || theme.accentColor
  return {
    '--swarm-background': vars['--orch-bg'],
    '--swarm-surface': vars['--orch-panel'],
    '--swarm-surface-subtle': vars['--orch-bg'],
    '--swarm-surface-hover': vars['--orch-card'],
    '--swarm-background-inset': vars['--orch-bg'],
    '--swarm-border': vars['--orch-border'],
    '--swarm-border-muted': vars['--orch-border'],
    '--swarm-border-accent': theme.accentColor,
    '--swarm-text': '#f1f5f9',
    '--swarm-text-muted': '#a1afc4',
    '--swarm-text-subtle': '#8290a5',
    '--swarm-text-accent': secondary,
    '--swarm-accent': theme.accentColor,
    '--swarm-accent-hover': secondary,
    '--swarm-accent-text': '#fff',
    '--swarm-warning': '#e9ba65', '--swarm-warning-bg': '#33271b', '--swarm-warning-border': '#8e6a36',
    '--swarm-success': '#65c5a0', '--swarm-success-bg': '#15372e', '--swarm-success-border': '#408c70',
    '--swarm-danger': '#e17784', '--swarm-danger-bg': '#40212c', '--swarm-danger-border': '#a25365',
    '--swarm-info': secondary, '--swarm-info-bg': vars['--orch-card'],
  }
}

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
