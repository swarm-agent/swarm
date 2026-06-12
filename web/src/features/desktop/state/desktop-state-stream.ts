import { openDesktopWebSocket, type OpenDesktopWebSocketOptions } from '../realtime/client'
import {
  applyDesktopDaemonEvent,
  markDesktopStale,
  replaceDesktopFromSnapshot,
} from './desktop-state-store'
import { fetchDesktopStateSnapshot, type DesktopStateSnapshotRequest } from './desktop-state-snapshot'
import type { DesktopDaemonEvent, DesktopDaemonSnapshot } from './desktop-state'

export const DEFAULT_DESKTOP_STREAM_QUEUE_LIMIT = 1024

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

interface DesktopStateStreamSocket {
  addEventListener(type: 'message' | 'error' | 'close', listener: (event: MessageEvent | Event) => void): void
  close(): void
}

export interface DesktopStateStreamOptions {
  snapshotRequest?: DesktopStateSnapshotRequest
  afterRev?: number
  queueLimit?: number
  onControlFrame?: (frame: DesktopRealtimeFrame) => void
  onError?: (error: Error) => void
  openSocket?: (options: OpenDesktopWebSocketOptions) => Promise<DesktopStateStreamSocket>
  fetchSnapshot?: (request: DesktopStateSnapshotRequest, signal?: AbortSignal) => Promise<DesktopDaemonSnapshot>
}

export interface DesktopRealtimeFrameResult {
  kind: 'event' | 'control'
  resyncRequired: boolean
  reason?: string
}

export async function startDesktopStateStream(options: DesktopStateStreamOptions = {}): Promise<DesktopStateStreamHandle> {
  const queueLimit = normalizeQueueLimit(options.queueLimit)
  const openSocket = options.openSocket ?? openDesktopWebSocket
  const fetchSnapshot = options.fetchSnapshot ?? fetchDesktopStateSnapshot
  const snapshotRequest = options.snapshotRequest ?? {}
  const abortController = new AbortController()
  const queue: unknown[] = []

  let closed = false
  let draining = false
  let resyncing = false
  let socket: DesktopStateStreamSocket | null = null

  const closeCurrentSocket = () => {
    const current = socket
    socket = null
    if (current) {
      current.close()
    }
  }

  const connect = async (afterRev: number): Promise<void> => {
    if (closed) {
      return
    }
    const nextSocket = await openSocket({ afterRev })
    if (closed) {
      nextSocket.close()
      return
    }
    socket = nextSocket
    nextSocket.addEventListener('message', (message) => {
      if (closed || resyncing || socket !== nextSocket) {
        return
      }
      enqueueFrame((message as MessageEvent).data)
    })
    nextSocket.addEventListener('error', () => {
      if (closed || resyncing || socket !== nextSocket) {
        return
      }
      requestResync('desktop realtime websocket error')
    })
    nextSocket.addEventListener('close', () => {
      if (closed || resyncing || socket !== nextSocket) {
        return
      }
      requestResync('desktop realtime websocket closed')
    })
  }

  const reloadSnapshotAndReconnect = async (reason: string): Promise<void> => {
    try {
      const snapshot = await fetchSnapshot(snapshotRequest, abortController.signal)
      if (closed) {
        return
      }
      replaceDesktopFromSnapshot(snapshot)
      await connect(snapshot.rev)
    } catch (error) {
      if (closed) {
        return
      }
      const err = error instanceof Error ? error : new Error(String(error))
      markDesktopStale(`${reason}; snapshot reload failed: ${err.message}`)
      options.onError?.(err)
    } finally {
      resyncing = false
    }
  }

  const requestResync = (reason: string): void => {
    if (closed) {
      return
    }
    queue.length = 0
    closeCurrentSocket()
    markDesktopStale(reason)
    if (resyncing) {
      return
    }
    resyncing = true
    void reloadSnapshotAndReconnect(reason)
  }

  const drainQueue = (): void => {
    draining = false
    while (!closed && !resyncing && queue.length > 0) {
      const raw = queue.shift()
      try {
        const result = handleDesktopRealtimeFrame(raw, options)
        if (result.resyncRequired) {
          requestResync(result.reason ?? 'desktop realtime resync required')
          return
        }
      } catch (error) {
        const err = error instanceof Error ? error : new Error(String(error))
        requestResync(`desktop realtime frame error: ${err.message}`)
        options.onError?.(err)
        return
      }
    }
  }

  const enqueueFrame = (raw: unknown): void => {
    if (queue.length >= queueLimit) {
      requestResync(`desktop realtime event queue overflow (${queueLimit})`)
      return
    }
    queue.push(raw)
    if (!draining) {
      draining = true
      queueMicrotask(drainQueue)
    }
  }

  const snapshot = await fetchSnapshot(snapshotRequest, abortController.signal)
  replaceDesktopFromSnapshot(snapshot)
  await connect(options.afterRev ?? snapshot.rev)

  return {
    close() {
      closed = true
      queue.length = 0
      abortController.abort()
      closeCurrentSocket()
    },
  }
}

export function handleDesktopRealtimeFrame(raw: unknown, options: DesktopStateStreamOptions = {}): DesktopRealtimeFrameResult {
  const frame = parseDesktopRealtimeFrame(raw)
  if (frame.protocol !== 'v3.realtime' || frame.protocol_version !== 1) {
    throw new Error('Desktop realtime frame has invalid protocol metadata.')
  }

  if (frame.kind === 'event') {
    const event = frameToDaemonEvent(frame)
    const next = applyDesktopDaemonEvent(event)
    if (next.status === 'stale' && next.resyncRequested) {
      return { kind: 'event', resyncRequired: true, reason: next.staleReason ?? 'desktop realtime event requires resync' }
    }
    return { kind: 'event', resyncRequired: false }
  }

  if (isResyncControlFrame(frame.kind)) {
    const reason = `desktop realtime ${frame.kind}: ${frame.error ?? frame.reason ?? frame.error_code ?? 'resync required'}`
    markDesktopStale(reason)
    options.onControlFrame?.(frame)
    return { kind: 'control', resyncRequired: true, reason }
  }
  options.onControlFrame?.(frame)
  return { kind: 'control', resyncRequired: false }
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

function normalizeQueueLimit(limit: number | undefined): number {
  if (typeof limit !== 'number' || !Number.isFinite(limit)) {
    return DEFAULT_DESKTOP_STREAM_QUEUE_LIMIT
  }
  return Math.max(1, Math.floor(limit))
}
