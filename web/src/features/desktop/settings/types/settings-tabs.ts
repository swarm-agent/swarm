export const SETTINGS_TABS = ['agents', 'auth', 'flows', 'permissions', 'swarm', 'themes', 'vault', 'worktrees'] as const

export type SettingsTabID = (typeof SETTINGS_TABS)[number]

export function isSettingsTabID(value: unknown): value is SettingsTabID {
  return typeof value === 'string' && SETTINGS_TABS.includes(value as SettingsTabID)
}

export function normalizeSettingsTabID(value: unknown): SettingsTabID {
  return isSettingsTabID(value) ? value : 'agents'
}
