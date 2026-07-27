export type DesktopMainSidebarMode = 'full' | 'focus'

export const DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY = 'swarm.web.desktop.main-sidebar.mode'
const LEGACY_DESKTOP_SIDEBAR_MODE_STORAGE_KEY = 'swarm.web.desktop.sidebar.display-mode'

export function normalizeDesktopMainSidebarMode(value: unknown): DesktopMainSidebarMode {
  if (value === 'focus' || value === 'thin') return 'focus'
  return 'full'
}

export function loadDesktopMainSidebarMode(): DesktopMainSidebarMode {
  if (typeof window === 'undefined') return 'full'
  try {
    const stored = window.localStorage.getItem(DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY)
    if (stored !== null) return normalizeDesktopMainSidebarMode(stored)
    return normalizeDesktopMainSidebarMode(window.localStorage.getItem(LEGACY_DESKTOP_SIDEBAR_MODE_STORAGE_KEY))
  } catch {
    return 'full'
  }
}

export function saveDesktopMainSidebarMode(mode: DesktopMainSidebarMode): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY, mode)
  } catch {
    // Client-local layout persistence is best effort.
  }
}
