import type { SessionCacheRecord, SessionSnapshot } from './desktop-v3-cache-types'

function metadataBoolean(metadata: Record<string, unknown> | undefined, key: string): boolean {
  return metadata?.[key] === true
}

function metadataString(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

/**
 * Canonical fail-closed predicate for sessions that must remain outside ordinary
 * Desktop chat navigation. The session may still be retained and addressed by
 * ID for an owning embedded surface such as the Plan sidecar.
 */
export function isDesktopV3NavigationHiddenSession(session: SessionSnapshot | undefined): boolean {
  if (!session) return false
  const metadata = session.metadata
  return session.navigation_hidden === true
    || session.system_session === true
    || session.system_sidechat === true
    || session.lineage_kind?.trim().toLowerCase() === 'system_sidechat'
    || metadataBoolean(metadata, 'navigation_hidden')
    || metadataBoolean(metadata, 'system_session')
    || metadataBoolean(metadata, 'system_sidechat')
    || metadataString(metadata, 'lineage_kind') === 'system_sidechat'
}

export function isDesktopV3NavigationHiddenRecord(record: SessionCacheRecord | undefined): boolean {
  return record?.kind === 'full' && isDesktopV3NavigationHiddenSession(record.session)
}
