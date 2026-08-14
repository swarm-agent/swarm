export const SETTINGS_TABS = ['account', 'auth', 'actions', 'behavior', 'media', 'permissions', 'tailscale', 'notifications', 'themes', 'shortcuts', 'vault', 'worktrees'] as const

export type SettingsTabID = (typeof SETTINGS_TABS)[number]

export function isSettingsTabID(value: unknown): value is SettingsTabID {
  return typeof value === 'string' && SETTINGS_TABS.includes(value as SettingsTabID)
}

export function normalizeSettingsTabID(value: unknown): SettingsTabID {
  return value === 'agents' ? 'account' : value === 'models' ? 'actions' : value === 'images' ? 'media' : isSettingsTabID(value) ? value : 'account'
}
