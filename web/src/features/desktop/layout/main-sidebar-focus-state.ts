export type DesktopMainSidebarMode = 'full' | 'focus'

export const DESKTOP_MAIN_SIDEBAR_MODE_STORAGE_KEY = 'swarm.web.desktop.main-sidebar.mode'
export const DESKTOP_FOCUS_ACTIVE_CHATS_STORAGE_KEY = 'swarm.web.desktop.focus.show-active-chats'
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

export function loadDesktopFocusActiveChatsVisible(): boolean {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(DESKTOP_FOCUS_ACTIVE_CHATS_STORAGE_KEY) === 'true'
  } catch {
    return false
  }
}

export function saveDesktopFocusActiveChatsVisible(visible: boolean): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(DESKTOP_FOCUS_ACTIVE_CHATS_STORAGE_KEY, String(visible))
  } catch {
    // Client-local layout persistence is best effort.
  }
}
