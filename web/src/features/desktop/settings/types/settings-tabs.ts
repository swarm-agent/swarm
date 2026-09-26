export const SETTINGS_TABS = ['account', 'auth', 'actions', 'behavior', 'cloud', 'media', 'permissions', 'tailscale', 'notifications', 'themes', 'shortcuts', 'vault', 'worktrees'] as const

export type SettingsTabID = (typeof SETTINGS_TABS)[number]

export function isSettingsTabID(value: unknown): value is SettingsTabID {
  return typeof value === 'string' && SETTINGS_TABS.includes(value as SettingsTabID)
}

export function normalizeSettingsTabID(value: unknown): SettingsTabID {
  return value === 'agents'
    ? 'account'
    : value === 'models'
      ? 'actions'
      : value === 'images'
        ? 'media'
        : value === 's3' || value === 'gcp' || value === 'storage'
          ? 'cloud'
          : value === 'quick-actions' || value === 'quick_actions'
          ? 'shortcuts'
          : isSettingsTabID(value)
            ? value
            : 'account'
}
