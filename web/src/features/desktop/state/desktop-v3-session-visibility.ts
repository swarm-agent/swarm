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
export const DESKTOP_V3_VIDEO_PROJECT_LINEAGE_KIND = 'video_project'

/**
 * Video Studio sessions have two canonical creation paths: the dedicated tool
 * writes the experience/source pair, while AI-created studio sessions use the
 * stronger video_project lineage marker. Accept either complete contract so a
 * studio session remains discoverable when the user returns to its main chat.
 */
export function isDesktopV3VideoStudioMetadata(metadata: Record<string, unknown> | undefined): boolean {
  if (metadataString(metadata, 'lineage_kind') === DESKTOP_V3_VIDEO_PROJECT_LINEAGE_KIND) return true
  return metadataString(metadata, 'experience') === DESKTOP_V3_VIDEO_STUDIO_EXPERIENCE
    && metadataString(metadata, 'launch_source') === DESKTOP_V3_VIDEO_STUDIO_LAUNCH_SOURCE
}

export function isDesktopV3VideoStudioSession(session: SessionSnapshot | undefined): boolean {
  return Boolean(session && isDesktopV3VideoStudioMetadata(session.metadata))
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
