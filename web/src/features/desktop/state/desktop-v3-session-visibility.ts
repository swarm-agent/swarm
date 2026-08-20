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
export const DESKTOP_V3_VIDEO_STUDIO_EXPERIENCE = 'video_studio'
export const DESKTOP_V3_VIDEO_STUDIO_LAUNCH_SOURCE = 'video_tool'

/**
 * Creative-mode videos remain ordinary durable V3 sessions, but their stable
 * creation metadata routes them to the dedicated Video surface instead of the
 * ordinary chat list. Requiring both markers avoids reclassifying older or
 * unrelated sessions that happen to use one generic metadata value.
 */
export function isDesktopV3VideoStudioSession(session: SessionSnapshot | undefined): boolean {
  if (!session) return false
  return metadataString(session.metadata, 'experience') === DESKTOP_V3_VIDEO_STUDIO_EXPERIENCE
    && metadataString(session.metadata, 'launch_source') === DESKTOP_V3_VIDEO_STUDIO_LAUNCH_SOURCE
}

export function isDesktopV3VideoStudioRecord(record: SessionCacheRecord | undefined): boolean {
  return record?.kind === 'full' && isDesktopV3VideoStudioSession(record.session)
}

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
