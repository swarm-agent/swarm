import { createStore, type StoreApi } from 'zustand/vanilla'

import {
  createEmptyDesktopState,
  type DesktopState,
} from '../state/desktop-state'
import type { V3Envelope, V3EnvelopeCursor } from './v3-envelope'
import { applyV3Envelope, type V3EnvelopeApplyResult } from './v3-reducer'

const MAX_APPLIED_ENVELOPE_IDS = 2048

export interface V3RuntimeCursorState extends V3EnvelopeCursor {
  envelopeId: string
  sourceKind: V3Envelope['meta']['source']['kind']
  updatedAt: number
}

export interface V3RuntimeApplySummary {
  envelopeId: string
  envelopeKind: V3Envelope['kind']
  sourceKind: V3Envelope['meta']['source']['kind']
  domain: V3Envelope['meta']['domain']
  sessionId: string | null
  applied: boolean
  rejected: boolean
  stale: boolean
  duplicate: boolean
  shouldAdvanceCursor: boolean
  cursorScope: string | null
  reason: string | null
  mutationSeq: number
  receivedAt: number
}

export interface V3RuntimeState {
  desktop: DesktopState
  cursorsByScope: Record<string, V3RuntimeCursorState>
  appliedEnvelopeIds: Record<string, true>
  appliedEnvelopeOrder: string[]
  lastApply: V3RuntimeApplySummary | null
  mutationSeq: number
}

export interface V3RuntimeApplyOutcome extends V3EnvelopeApplyResult {
  duplicate: boolean
  cursorScope: string | null
  snapshot: V3RuntimeState
}

export interface V3RuntimeController {
  applyEnvelope(envelope: V3Envelope): V3RuntimeApplyOutcome
  getSnapshot(): V3RuntimeState
  getDesktopSnapshot(): DesktopState
  subscribe(listener: () => void): () => void
  destroy(): void
}

export type V3RuntimeStoreApi = StoreApi<V3RuntimeState>

export function createV3RuntimeInitialState(desktop: DesktopState = createEmptyDesktopState()): V3RuntimeState {
  return {
    desktop,
    cursorsByScope: {},
    appliedEnvelopeIds: {},
    appliedEnvelopeOrder: [],
    lastApply: null,
    mutationSeq: 0,
  }
}

export function createV3RuntimeController(initialDesktopState?: DesktopState): V3RuntimeController {
  const store = createV3RuntimeStore(initialDesktopState)
  return createController(store)
}

/**
 * Internal vanilla Zustand store. V3 UI code must consume this through v3-hooks;
 * transport, hydration, replay, and persistence code must mutate only via
 * applyV3RuntimeEnvelope/applyEnvelope.
 */
const v3RuntimeStore = createV3RuntimeStore()

const defaultController = createController(v3RuntimeStore)

export const applyV3RuntimeEnvelope = defaultController.applyEnvelope
export const getV3RuntimeSnapshot = defaultController.getSnapshot
export const getV3RuntimeDesktopSnapshot = defaultController.getDesktopSnapshot
export const subscribeV3Runtime = defaultController.subscribe

function createV3RuntimeStore(initialDesktopState?: DesktopState): V3RuntimeStoreApi {
  return createStore<V3RuntimeState>(() => createV3RuntimeInitialState(initialDesktopState))
}

function createController(store: V3RuntimeStoreApi): V3RuntimeController {
  return {
    applyEnvelope(envelope) {
      return applyEnvelopeToStore(store, envelope)
    },
    getSnapshot() {
      return store.getState()
    },
    getDesktopSnapshot() {
      return store.getState().desktop
    },
    subscribe(listener) {
      return store.subscribe(listener)
    },
    destroy() {
      const destroy = (store as StoreApi<V3RuntimeState> & { destroy?: () => void }).destroy
      destroy?.()
    },
  }
}

function applyEnvelopeToStore(store: V3RuntimeStoreApi, envelope: V3Envelope): V3RuntimeApplyOutcome {
  const current = store.getState()
  if (current.appliedEnvelopeIds[envelope.meta.id]) {
    return duplicateOutcome(current, envelope)
  }

  const reducerResult = applyV3Envelope(current.desktop, envelope)
  const cursorScope = reducerResult.shouldAdvanceCursor ? cursorScopeForEnvelope(envelope) : null
  const mutationSeq = current.mutationSeq + 1
  const summary = applySummary(envelope, reducerResult, {
    duplicate: false,
    cursorScope,
    mutationSeq,
  })

  const shouldRememberEnvelope = reducerResult.applied
  const shouldPublish = reducerResult.applied
    || reducerResult.rejected
    || reducerResult.stale
    || reducerResult.shouldAdvanceCursor

  if (!shouldPublish) {
    return {
      ...reducerResult,
      duplicate: false,
      cursorScope,
      snapshot: current,
    }
  }

  const remembered = shouldRememberEnvelope
    ? rememberAppliedEnvelope(current, envelope.meta.id)
    : {
        appliedEnvelopeIds: current.appliedEnvelopeIds,
        appliedEnvelopeOrder: current.appliedEnvelopeOrder,
      }
  const restoredCursors = envelope.kind === 'persisted.restore' && envelope.cursorsByScope
    ? restorePersistedCursors(envelope.cursorsByScope, envelope)
    : current.cursorsByScope
  const nextState: V3RuntimeState = {
    desktop: reducerResult.state,
    cursorsByScope: cursorScope
      ? advanceCursor(restoredCursors, cursorScope, envelope)
      : restoredCursors,
    appliedEnvelopeIds: remembered.appliedEnvelopeIds,
    appliedEnvelopeOrder: remembered.appliedEnvelopeOrder,
    lastApply: summary,
    mutationSeq,
  }
  store.setState(nextState, true)

  return {
    ...reducerResult,
    duplicate: false,
    cursorScope,
    snapshot: nextState,
  }
}

function duplicateOutcome(state: V3RuntimeState, envelope: V3Envelope): V3RuntimeApplyOutcome {
  return {
    state: state.desktop,
    applied: false,
    rejected: false,
    stale: false,
    shouldAdvanceCursor: false,
    reason: 'duplicate V3 envelope',
    envelope,
    duplicate: true,
    cursorScope: null,
    snapshot: state,
  }
}

function applySummary(
  envelope: V3Envelope,
  result: V3EnvelopeApplyResult,
  options: { duplicate: boolean; cursorScope: string | null; mutationSeq: number },
): V3RuntimeApplySummary {
  return {
    envelopeId: envelope.meta.id,
    envelopeKind: envelope.kind,
    sourceKind: envelope.meta.source.kind,
    domain: envelope.meta.domain,
    sessionId: envelope.meta.sessionId ?? null,
    applied: result.applied,
    rejected: result.rejected,
    stale: result.stale,
    duplicate: options.duplicate,
    shouldAdvanceCursor: result.shouldAdvanceCursor,
    cursorScope: options.cursorScope,
    reason: result.reason ?? null,
    mutationSeq: options.mutationSeq,
    receivedAt: envelope.meta.receivedAt,
  }
}

function rememberAppliedEnvelope(state: V3RuntimeState, envelopeId: string): Pick<V3RuntimeState, 'appliedEnvelopeIds' | 'appliedEnvelopeOrder'> {
  const order = [...state.appliedEnvelopeOrder, envelopeId]
  const ids: Record<string, true> = {
    ...state.appliedEnvelopeIds,
    [envelopeId]: true,
  }
  while (order.length > MAX_APPLIED_ENVELOPE_IDS) {
    const evicted = order.shift()
    if (evicted) {
      delete ids[evicted]
    }
  }
  return {
    appliedEnvelopeIds: ids,
    appliedEnvelopeOrder: order,
  }
}

function restorePersistedCursors(cursors: Record<string, V3EnvelopeCursor>, envelope: V3Envelope): Record<string, V3RuntimeCursorState> {
  const restored: Record<string, V3RuntimeCursorState> = {}
  for (const [scope, cursor] of Object.entries(cursors)) {
    restored[scope] = {
      ...cursor,
      envelopeId: envelope.meta.id,
      sourceKind: envelope.meta.source.kind,
      updatedAt: envelope.meta.receivedAt,
    }
  }
  return restored
}

function advanceCursor(cursors: Record<string, V3RuntimeCursorState>, scope: string, envelope: V3Envelope): Record<string, V3RuntimeCursorState> {
  const current = cursors[scope]
  const cursor = envelope.meta.cursor
  return {
    ...cursors,
    [scope]: {
      endpointCursor: cursor.endpointCursor ?? current?.endpointCursor,
      stream: cursor.stream ?? current?.stream,
      rev: maxOptional(cursor.rev, current?.rev),
      prevRev: maxOptional(cursor.prevRev, current?.prevRev),
      globalSeq: maxOptional(cursor.globalSeq, current?.globalSeq),
      sourceSeq: maxOptional(cursor.sourceSeq, current?.sourceSeq),
      highWatermarkSeq: maxOptional(cursor.highWatermarkSeq, current?.highWatermarkSeq),
      tsUnixMs: maxOptional(cursor.tsUnixMs, current?.tsUnixMs),
      envelopeId: envelope.meta.id,
      sourceKind: envelope.meta.source.kind,
      updatedAt: envelope.meta.receivedAt,
    },
  }
}

function cursorScopeForEnvelope(envelope: V3Envelope): string {
  const sessionId = envelope.meta.sessionId?.trim()
  if (sessionId) {
    return `session:${sessionId}`
  }
  const stream = envelope.meta.cursor.stream?.trim()
  if (stream) {
    return `stream:${stream}`
  }
  return 'global'
}

function maxOptional(left: number | undefined, right: number | undefined): number | undefined {
  if (left === undefined) return right
  if (right === undefined) return left
  return Math.max(left, right)
}
