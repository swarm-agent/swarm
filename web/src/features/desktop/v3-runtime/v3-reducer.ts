import {
  desktopReducer,
  type DesktopDaemonEvent,
  type DesktopState,
} from '../state/desktop-state'
import {
  validateV3Envelope,
  type V3CanonicalEvent,
  type V3Envelope,
} from './v3-envelope'

export interface V3EnvelopeApplyResult {
  state: DesktopState
  applied: boolean
  rejected: boolean
  stale: boolean
  shouldAdvanceCursor: boolean
  reason?: string
  envelope: V3Envelope
}

export function applyV3Envelope(state: DesktopState, envelope: V3Envelope): V3EnvelopeApplyResult {
  const validation = validateV3Envelope(envelope)
  if (!validation.ok) {
    return staleResult(state, envelope, `invalid V3 envelope: ${validation.reason}`)
  }

  switch (envelope.kind) {
    case 'snapshot':
      return reducerResult(state, envelope, desktopReducer(state, {
        type: envelope.mode === 'replace' ? 'snapshot/replace' : envelope.mode === 'reconcile' ? 'snapshot/reconcile' : 'snapshot/merge',
        snapshot: envelope.snapshot,
      }))
    case 'persisted.restore':
      return reducerResult(state, envelope, desktopReducer(state, {
        type: envelope.mode === 'replace' ? 'snapshot/replace' : 'snapshot/merge',
        snapshot: envelope.snapshot,
      }))
    case 'event':
      return reducerResult(state, envelope, desktopReducer(state, {
        type: 'daemon/event',
        event: materializeDesktopEvent(state, envelope.event),
      }))
    case 'optimistic.send':
      return applyOptimisticSend(state, envelope)
    case 'connection.status':
      return reducerResult(state, envelope, desktopReducer(state, {
        type: 'connection/status',
        status: envelope.status,
        error: envelope.error,
      }), { advanceCursor: false })
    case 'connection.stale':
      return reducerResult(state, envelope, desktopReducer(state, {
        type: 'connection/stale',
        reason: envelope.reason,
      }), { advanceCursor: false })
    case 'control':
      if (envelope.control === 'cursor.error' || envelope.control === 'auth.denied' || envelope.control === 'slow_consumer.reconnect_required') {
        return reducerResult(state, envelope, desktopReducer(state, {
          type: 'connection/stale',
          reason: envelope.reason || envelope.error || `V3 realtime ${envelope.control}`,
        }), { advanceCursor: false })
      }
      return {
        state,
        applied: false,
        rejected: false,
        stale: false,
        shouldAdvanceCursor: hasCursor(envelope),
        envelope,
      }
    default:
      return staleResult(state, envelope, 'unsupported V3 envelope kind')
  }
}

function applyOptimisticSend(state: DesktopState, envelope: Extract<V3Envelope, { kind: 'optimistic.send' }>): V3EnvelopeApplyResult {
  let next = state
  let rev = next.rev
  next = desktopReducer(next, {
    type: 'daemon/event',
    event: {
      rev: rev + 1,
      prevRev: rev,
      type: 'desktop/message/upsert',
      payload: { message: envelope.message },
      entityId: envelope.sessionId,
      stream: `v3/session:${envelope.sessionId}`,
    },
  })
  rev = next.rev
  if (envelope.runIntent) {
    next = desktopReducer(next, {
      type: 'daemon/event',
      event: {
        rev: rev + 1,
        prevRev: rev,
        type: 'desktop/run-intent/set',
        payload: { sessionId: envelope.sessionId, runIntent: envelope.runIntent },
        entityId: envelope.sessionId,
        stream: `v3/session:${envelope.sessionId}`,
      },
    })
  }
  return reducerResult(state, envelope, next, { advanceCursor: false })
}

function materializeDesktopEvent(state: DesktopState, event: V3CanonicalEvent): DesktopDaemonEvent {
  const rev = positiveInteger(event.rev) ?? state.rev + 1
  const prevRev = nonNegativeInteger(event.prevRev) ?? state.rev
  return {
    rev,
    prevRev,
    type: event.type.trim(),
    payload: event.payload,
    stream: event.stream,
    entityId: event.entityId,
    globalSeq: positiveInteger(event.globalSeq),
    sourceSeq: positiveInteger(event.sourceSeq),
    tsUnixMs: positiveInteger(event.tsUnixMs),
  }
}

function reducerResult(state: DesktopState, envelope: V3Envelope, next: DesktopState, options: { advanceCursor?: boolean } = {}): V3EnvelopeApplyResult {
  const stale = next.status === 'stale' && next.resyncRequested
  const mutated = !Object.is(next, state)
  const applied = mutated && !stale
  return {
    state: next,
    applied,
    rejected: stale,
    stale,
    shouldAdvanceCursor: Boolean((options.advanceCursor ?? true) && applied && hasCursor(envelope)),
    reason: stale ? next.staleReason ?? 'V3 envelope made state stale' : undefined,
    envelope,
  }
}

function staleResult(state: DesktopState, envelope: V3Envelope, reason: string): V3EnvelopeApplyResult {
  const next = desktopReducer(state, { type: 'connection/stale', reason })
  return {
    state: next,
    applied: false,
    rejected: true,
    stale: true,
    shouldAdvanceCursor: false,
    reason,
    envelope,
  }
}

function hasCursor(envelope: V3Envelope): boolean {
  const cursor = envelope.meta.cursor
  return Boolean(
    cursor.endpointCursor
      || cursor.stream
      || cursor.rev !== undefined
      || cursor.globalSeq !== undefined
      || cursor.sourceSeq !== undefined
      || cursor.highWatermarkSeq !== undefined,
  )
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : undefined
}
