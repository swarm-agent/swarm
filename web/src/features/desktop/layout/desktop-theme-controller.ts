import { applyWorkspaceTheme } from '../../workspaces/launcher/services/workspace-theme'
import { normalizeGlobalThemeSettings, type UISettingsWire } from '../settings/swarm/types/swarm-settings'
import type { WorkspaceEntry } from '../../workspaces/launcher/types/workspace'

export function desktopEffectiveThemeId(
  selectedWorkspacePath: string | null | undefined,
  workspaces: readonly Pick<WorkspaceEntry, 'path' | 'themeId'>[],
  settings?: UISettingsWire | null,
  waitForSelectedWorkspace = true,
): string | null {
  const normalizedWorkspacePath = selectedWorkspacePath?.trim() ?? ''
  if (!normalizedWorkspacePath) {
    return waitForSelectedWorkspace ? null : normalizeGlobalThemeSettings(settings).activeId
  }

  const workspace = workspaces.find((entry) => entry.path === normalizedWorkspacePath)
  if (!workspace) {
    return waitForSelectedWorkspace ? null : normalizeGlobalThemeSettings(settings).activeId
  }
  const workspaceThemeId = workspace.themeId?.trim().toLowerCase() ?? ''
  if (workspaceThemeId) {
    return workspaceThemeId
  }
  return normalizeGlobalThemeSettings(settings).activeId
}

export function applyDesktopRouteTheme(
  selectedWorkspacePath: string | null | undefined,
  workspaces: readonly Pick<WorkspaceEntry, 'path' | 'themeId'>[],
  settings?: UISettingsWire | null,
  waitForSelectedWorkspace = true,
): void {
  const themeId = desktopEffectiveThemeId(selectedWorkspacePath, workspaces, settings, waitForSelectedWorkspace)
  if (!themeId) {
    return
  }
  applyWorkspaceTheme(themeId)
}
