import { ensureDesktopSession } from '../../../app/api'

export interface OpenDesktopWebSocketOptions {
  afterRev?: number
}

export async function openDesktopWebSocket(options: OpenDesktopWebSocketOptions = {}): Promise<WebSocket> {
  await ensureDesktopSession(true)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const afterRev = options.afterRev
  const hasAfterRev = typeof afterRev === 'number' && Number.isFinite(afterRev) && afterRev >= 0
  const url = new URL(hasAfterRev ? '/v3/realtime/stream' : '/ws', `${protocol}//${window.location.host}`)
  if (hasAfterRev && afterRev > 0) {
    url.searchParams.set('endpoint_cursor', `cursor-${Math.floor(afterRev)}`)
  }
  return new WebSocket(url)
}
