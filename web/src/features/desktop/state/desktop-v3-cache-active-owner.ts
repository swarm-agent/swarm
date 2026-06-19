import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'

import { parseDesktopV3CacheOwnerKey } from './desktop-v3-cache-owner'

export const DESKTOP_V3_CACHE_ACTIVE_OWNER_STORAGE_KEY = 'swarm.web.desktop.v3Cache.activeOwnerKey.v1'

export function loadDesktopV3CacheActiveOwnerKey(): string | undefined {
  const value = loadStoredValue(DESKTOP_V3_CACHE_ACTIVE_OWNER_STORAGE_KEY)
  if (!value) return undefined
  try {
    return parseDesktopV3CacheOwnerKey(value).key
  } catch {
    clearDesktopV3CacheActiveOwnerKey()
    return undefined
  }
}

export function saveDesktopV3CacheActiveOwnerKey(ownerKey: string): boolean {
  try {
    const normalizedOwnerKey = parseDesktopV3CacheOwnerKey(ownerKey).key
    saveStoredValue(DESKTOP_V3_CACHE_ACTIVE_OWNER_STORAGE_KEY, normalizedOwnerKey)
    return true
  } catch {
    return false
  }
}

export function clearDesktopV3CacheActiveOwnerKey(): void {
  saveStoredValue(DESKTOP_V3_CACHE_ACTIVE_OWNER_STORAGE_KEY, null)
}
