import { openDesktopWebSocket } from '../realtime/client'
import { applyDesktopDaemonEvent, getDesktopSnapshot, markDesktopStale, replaceDesktopFromSnapshot } from './desktop-state-store'
import { fetchDesktopStateSnapshot } from './desktop-state-snapshot'
import type { DesktopDaemonEvent } from './desktop-state'

export type DesktopRealtimeControlKind =
  | 'keepalive'
  | 'replay.started'
  | 'replay.complete'
  | 'projection.high_watermark'
  | 'cursor.error'
  | 'auth.denied'
  | 'slow_consumer.reconnect_required'

interface DesktopRealtimeFrame {
  protocol?: string
  protocol_version?: number
  kind?: string
  rev?: number
  prevRev?: number
  event_type?: string
  event?: {
    event_type?: string
    payload?: unknown
  }
  error_code?: string
  error?: string
  reason?: string
}

export interface DesktopStateStreamHandle {
  close(): void
}

export interface DesktopStateStreamOptions {
  snapshotRequest?: Parameters<typeof fetchDesktopStateSnapshot>[0]
  afterRev?: number
  onControlFrame?: (frame: DesktopRealtimeFrame) => void
  onError?: (error: Error) => void
}

export async function startDesktopStateStream(options: DesktopStateStreamOptions = {}): Promise<DesktopStateStreamHandle> {
  const snapshot = await fetchDesktopStateSnapshot(options.snapshotRequest ?? {})
  replaceDesktopFromSnapshot(snapshot)
  const afterRev = options.afterRev ?? snapshot.rev
  const socket = await openDesktopWebSocket({ afterRev })
  let closed = false

  socket.addEventListener('message', (message) => {
    if (closed) {
      return
    }
    try {
      handleDesktopRealtimeFrame(message.data, options)
    } catch (error) {
      const err = error instanceof Error ? error : new Error(String(error))
      applyDesktopDaemonEvent({ rev: getDesktopSnapshot().rev + 1, prevRev: Number.NaN, type: 'desktop/stream/error', payload: { error: err.message } })
      options.onError?.(err)
    }
  })

  socket.addEventListener('error', () => {
    applyDesktopDaemonEvent({ rev: getDesktopSnapshot().rev + 1, prevRev: Number.NaN, type: 'desktop/stream/error', payload: { error: 'websocket error' } })
  })

  return {
    close() {
      closed = true
      socket.close()
    },
  }
}

export function handleDesktopRealtimeFrame(raw: unknown, options: DesktopStateStreamOptions = {}): void {
  const frame = parseDesktopRealtimeFrame(raw)
  if (frame.protocol !== 'v3.realtime' || frame.protocol_version !== 1) {
    throw new Error('Desktop realtime frame has invalid protocol metadata.')
  }

  if (frame.kind === 'event') {
    applyDesktopDaemonEvent(frameToDaemonEvent(frame))
    return
  }

  if (isResyncControlFrame(frame.kind)) {
    markDesktopStale(`desktop realtime ${frame.kind}: ${frame.error ?? frame.reason ?? frame.error_code ?? 'resync required'}`)
  }
  options.onControlFrame?.(frame)
}

function frameToDaemonEvent(frame: DesktopRealtimeFrame): DesktopDaemonEvent {
  if (typeof frame.rev !== 'number' || !Number.isFinite(frame.rev)) {
    throw new Error('Desktop realtime event missing valid rev.')
  }
  if (typeof frame.prevRev !== 'number' || !Number.isFinite(frame.prevRev)) {
    throw new Error('Desktop realtime event missing valid prevRev.')
  }
  const eventType = String(frame.event_type ?? frame.event?.event_type ?? '')
  if (!eventType) {
    throw new Error('Desktop realtime event missing event_type.')
  }
  return {
    rev: frame.rev,
    prevRev: frame.prevRev,
    type: eventType,
    payload: frame.event?.payload,
  }
}

function parseDesktopRealtimeFrame(raw: unknown): DesktopRealtimeFrame {
  if (typeof raw === 'string') {
    return JSON.parse(raw) as DesktopRealtimeFrame
  }
  if (raw instanceof Blob) {
    throw new Error('Desktop realtime Blob frames are not supported by the synchronous reducer boundary.')
  }
  if (typeof raw === 'object' && raw !== null) {
    return raw as DesktopRealtimeFrame
  }
  throw new Error('Desktop realtime frame is not an object.')
}

function isResyncControlFrame(kind: string | undefined): kind is DesktopRealtimeControlKind {
  return kind === 'cursor.error' || kind === 'auth.denied' || kind === 'slow_consumer.reconnect_required'
}

