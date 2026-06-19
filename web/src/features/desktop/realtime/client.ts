// Legacy desktop realtime client connection loop removed.
// This module keeps the dormant V3 socket URL contract explicit without opening production sockets.

export const DESKTOP_V3_REALTIME_STREAM_PATH = '/v3/realtime/stream'

export interface DesktopV3RealtimeTransportSocketUrlInput {
  endpointCursor: string
  protocol?: 'ws:' | 'wss:'
  host?: string
}

export function buildDesktopV3RealtimeTransportSocketURL(input: DesktopV3RealtimeTransportSocketUrlInput): URL {
  const endpointCursor = normalizeRequired(input.endpointCursor, 'endpointCursor')
  const protocol = input.protocol ?? (globalThis.window?.location?.protocol === 'https:' ? 'wss:' : 'ws:')
  const host = normalizeRequired(input.host ?? globalThis.window?.location?.host, 'host')
  const url = new URL(DESKTOP_V3_REALTIME_STREAM_PATH, `${protocol}//${host}`)
  url.searchParams.set('endpoint_cursor', endpointCursor)
  return url
}

function normalizeRequired(value: unknown, label: string): string {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`Desktop V3 realtime transport ${label} is required`)
  }
  return value.trim()
}
