import { SESSION_V3_REALTIME_STREAM_PATH } from '../session-v3/types'

export interface DesktopV3RealtimeSocketURLInput {
  endpointCursor: string
  protocol?: string
  host?: string
}

export function buildDesktopV3RealtimeTransportSocketURL(
  input: DesktopV3RealtimeSocketURLInput,
): URL {
  const endpointCursor = input.endpointCursor.trim()
  if (!endpointCursor) {
    throw new Error('Desktop V3 realtime requires endpoint_cursor')
  }

  const protocol = input.protocol ?? (window.location.protocol === 'https:' ? 'wss:' : 'ws:')
  const host = input.host ?? window.location.host
  const url = new URL(SESSION_V3_REALTIME_STREAM_PATH, `${protocol}//${host}`)
  url.searchParams.set('endpoint_cursor', endpointCursor)
  return url
}

export function openDesktopV3RealtimeTransportSocket(input: { endpointCursor: string }): WebSocket {
  return new WebSocket(buildDesktopV3RealtimeTransportSocketURL(input))
}
