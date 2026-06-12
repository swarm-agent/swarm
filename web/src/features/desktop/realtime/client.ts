import { ensureDesktopSession } from '../../../app/api'

export async function openDesktopWebSocket(): Promise<WebSocket> {
  await ensureDesktopSession(true)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = new URL('/ws', `${protocol}//${window.location.host}`)
  return new WebSocket(url)
}
