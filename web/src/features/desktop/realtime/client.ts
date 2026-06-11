import { ensureDesktopSession } from '../../../app/api'

export interface OpenDesktopWebSocketOptions {
  afterRev?: number
}

export async function openDesktopWebSocket(options: OpenDesktopWebSocketOptions = {}): Promise<WebSocket> {
  await ensureDesktopSession(true)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const hasAfterRev = typeof options.afterRev === 'number' && Number.isFinite(options.afterRev) && options.afterRev > 0
  const url = new URL(hasAfterRev ? '/v3/realtime/stream' : '/ws', `${protocol}//${window.location.host}`)
  if (hasAfterRev) {
    url.searchParams.set('endpoint_cursor', `cursor-${Math.floor(options.afterRev as number)}`)
  }
  return new WebSocket(url)
}
