import { ensureDesktopSession } from '../../../app/api'

export async function openDesktopWebSocket(): Promise<WebSocket> {
  await ensureDesktopSession(true)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL('/ws', `${protocol}//${window.location.host}`)
  return new WebSocket(url)
}

export async function openDesktopV3RealtimeStream(options: { endpointCursor?: string | null } = {}): Promise<WebSocket> {
  await ensureDesktopSession(true)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL('/v3/realtime/stream', `${protocol}//${window.location.host}`)
  const endpointCursor = options.endpointCursor?.trim() ?? ''
  if (endpointCursor) {
    url.searchParams.set('endpoint_cursor', endpointCursor)
  }
  return new WebSocket(url)
}
