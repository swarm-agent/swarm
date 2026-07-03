import type { DesktopV3CacheState, SessionCacheRecord } from '../state/desktop-v3-cache-types'

export const DESKTOP_DOCUMENT_TITLE_FALLBACK = 'Swarm Desktop'

export interface DesktopDocumentTitleInput {
  pathname: string
  unreadCount: number
  sessionsById: DesktopV3CacheState['sessionsById']
}

export function composeDesktopDocumentTitle(input: DesktopDocumentTitleInput): string {
  const routeSessionId = activeRouteSessionId(input.pathname)
  const label = activeSessionTitle(input.sessionsById[routeSessionId])
    || desktopAreaLabel(input.pathname)
  const unreadCount = Math.max(0, Math.floor(input.unreadCount))
  return unreadCount > 0 ? `(${unreadCount}) ${label}` : label
}

export function desktopAreaLabel(pathname: string): string {
  const parts = pathname.split('/').map((part) => decodeURIComponentSafe(part).trim()).filter(Boolean)
  if (parts.length === 0) return 'Workspace Launcher'

  const first = parts[0]?.toLowerCase() || ''
  if (first === 'settings') return 'Settings'
  if (first === 'integrations') return 'Integrations'
  if (first === 'tools') return toolAreaLabel(parts[1])

  const second = parts[1]?.toLowerCase() || ''
  if (second === 'settings') return 'Settings'
  if (second === 'tools') return toolAreaLabel(parts[2])

  return DESKTOP_DOCUMENT_TITLE_FALLBACK
}

function activeRouteSessionId(pathname: string): string {
  const parts = pathname.split('/').map((part) => decodeURIComponentSafe(part).trim()).filter(Boolean)
  if (parts.length !== 2) return ''
  const [firstSegment, secondSegment] = parts
  const first = firstSegment.toLowerCase()
  const second = secondSegment.toLowerCase()
  if (first === 'integrations') return secondSegment
  const rootReserved = new Set(['settings', 'tools'])
  const workspaceReserved = new Set(['settings', 'tools'])
  if (rootReserved.has(first) || workspaceReserved.has(second)) return ''
  return secondSegment
}

function activeSessionTitle(record: SessionCacheRecord | undefined): string {
  if (!record || record.kind !== 'full') return ''
  return record.session.title?.trim() || record.session.id.trim()
}

function toolAreaLabel(tool: string | undefined): string {
  switch (tool?.toLowerCase()) {
    case 'image':
      return 'Image Tool'
    case 'video':
      return 'Video Tool'
    default:
      return 'Tools'
  }
}

function decodeURIComponentSafe(value: string): string {
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}
