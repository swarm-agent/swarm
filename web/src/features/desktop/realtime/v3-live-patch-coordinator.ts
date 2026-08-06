import type { DesktopV3CacheAction, DesktopV3CacheState, MessageSnapshot, RealtimeMessage } from '../state/desktop-v3-cache-types'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'
import { applyDesktopV3LivePatchBatch, resolveDesktopV3CacheEventRunId } from '../state/desktop-v3-cache-reducer'
import { normalizeRealtimeEventFrame } from '../state/desktop-v3-cache-wire'

const PER_STREAM_PENDING_BYTE_LIMIT = 64 * 1024
const TOTAL_PENDING_BYTE_LIMIT = 256 * 1024
const HIDDEN_DOCUMENT_FLUSH_MS = 50
const INITIAL_BUFFER_CAPACITY = 256
const textEncoder = new TextEncoder()
const textDecoder = new TextDecoder('utf-8', { fatal: true })

export interface DesktopV3LivePatchCoordinatorDeps {
  getSnapshot: () => DesktopV3CacheState
  commitSnapshot: (
    previous: DesktopV3CacheState,
    next: DesktopV3CacheState,
    actions: DesktopV3CacheAction[],
  ) => void
  requestFrame: (callback: FrameRequestCallback) => number
  cancelFrame: (id: number) => void
  setTimer: (callback: () => void, ms: number) => number
  clearTimer: (id: number) => void
  isDocumentHidden: () => boolean
}

export interface DesktopV3LivePatchDebugSnapshot {
  generation: number
  pendingKeys: number
  pendingBytes: number
  pendingAllocatedBytes: number
  pausedKeys: number
  scheduled: boolean
}

export interface DurableAssistantStreamEffect {
  key: string
  sessionId: string
  runId: string
  streamId: string
  kind: 'checkpoint' | 'committed-message'
}

interface PendingLiveAppend {
  sessionId: string
  runId: string
  streamId: string
  step: number
  stepId: string
  liveSeqStart: number
  liveSeqEnd: number
  offsetStart: number
  offsetEnd: number
  text: Utf8AppendBuffer
  bytes: number
  recordedAt: number
}

class Utf8AppendBuffer {
  private storage = new Uint8Array(INITIAL_BUFFER_CAPACITY)
  private length = 0

  append(text: string): number {
    const encoded = textEncoder.encode(text)
    const nextLength = this.length + encoded.byteLength
    if (nextLength > PER_STREAM_PENDING_BYTE_LIMIT) {
      throw new Error('Desktop V3 live patch stream pending byte limit exceeded')
    }
    if (nextLength > this.storage.byteLength) {
      let nextCapacity = this.storage.byteLength
      while (nextCapacity < nextLength) nextCapacity *= 2
      const next = new Uint8Array(nextCapacity)
      next.set(this.storage.subarray(0, this.length))
      this.storage = next
    }
    this.storage.set(encoded, this.length)
    this.length = nextLength
    return encoded.byteLength
  }

  byteLength(): number {
    return this.length
  }

  allocatedByteLength(): number {
    return this.storage.byteLength
  }

  toString(): string {
    return textDecoder.decode(this.storage.subarray(0, this.length))
  }
}

export class DesktopV3LivePatchCoordinator {
  private generation = 0
  private pending = new Map<string, PendingLiveAppend>()
  private pausedStreams = new Set<string>()
  private committedStreamTombstones = new Set<string>()
  private pendingBytes = 0
  private scheduleToken = 0
  private frameId: number | null = null
  private timerId: number | null = null

  constructor(private readonly deps: DesktopV3LivePatchCoordinatorDeps) {}

  accept(patch: SessionV3RealtimeLivePatchWire, generation: number): void {
    if (this.generation === 0) this.generation = generation
    if (generation !== this.generation) return

    const key = livePatchKey(patch)
    if (patch.stream_kind !== 'assistant_text') return
    if (this.committedStreamTombstones.has(key) || this.pausedStreams.has(key)) return

    const existingPending = this.pending.get(key)
    if (existingPending) {
      if (patch.live_seq_start !== existingPending.liveSeqEnd + 1 || patch.offset_start !== existingPending.offsetEnd) {
        this.pauseStream(key)
        return
      }
      this.appendToPending(existingPending, patch)
      this.scheduleFlush()
      return
    }

    const existing = findLiveStreamState(this.deps.getSnapshot(), patch.session_id, patch.run_id, patch.stream_id)
    if (existing?.livePaused) {
      this.pauseStream(key)
      return
    }
    const existingLiveSeqEnd = existing?.liveSeqEnd ?? 0
    const existingOffsetEnd = existing?.offsetEnd ?? 0
    if (patch.live_seq_end <= existingLiveSeqEnd && patch.offset_end <= existingOffsetEnd) {
      return
    }
    if (patch.live_seq_start !== existingLiveSeqEnd + 1 || patch.offset_start !== existingOffsetEnd) {
      this.pauseStream(key)
      return
    }

    const pending = createPendingAppend(patch)
    this.pending.set(key, pending)
    this.pendingBytes += pending.bytes
    if (pending.bytes > PER_STREAM_PENDING_BYTE_LIMIT || this.pendingBytes > TOTAL_PENDING_BYTE_LIMIT) {
      this.pauseStream(key)
      return
    }
    this.scheduleFlush()
  }

  beforeDurableFrame(frame: RealtimeMessage): void {
    const effect = durableAssistantStreamEffect(frame)
    if (!effect) return
    if (this.pending.has(effect.key)) this.flushStreams([effect.key])
    if (effect.kind === 'committed-message') this.tombstoneCommittedStream(effect.key)
  }

  afterDurableFrame(_frame: RealtimeMessage): void {
    // Committed-stream tombstones intentionally remain until generation reset.
  }

  flushNow(): void {
    this.cancelScheduledCallback()
    this.flushEntries(Array.from(this.pending.keys()))
  }

  flushStreams(keys: string[]): void {
    this.cancelScheduledCallback()
    this.flushEntries(keys)
    if (this.pending.size > 0) this.scheduleFlush()
  }

  resetGeneration(generation: number): void {
    this.cancelScheduledCallback()
    this.pending.clear()
    this.pausedStreams.clear()
    this.committedStreamTombstones.clear()
    this.pendingBytes = 0
    this.generation = generation
  }

  dispose(): void {
    this.cancelScheduledCallback()
    this.pending.clear()
    this.pausedStreams.clear()
    this.committedStreamTombstones.clear()
    this.pendingBytes = 0
  }

  debugSnapshotForTests(): DesktopV3LivePatchDebugSnapshot {
    let pendingAllocatedBytes = 0
    for (const pending of this.pending.values()) {
      pendingAllocatedBytes += pending.text.allocatedByteLength()
    }
    return {
      generation: this.generation,
      pendingKeys: this.pending.size,
      pendingBytes: this.pendingBytes,
      pendingAllocatedBytes,
      pausedKeys: this.pausedStreams.size,
      scheduled: this.frameId !== null || this.timerId !== null,
    }
  }

  private appendToPending(pending: PendingLiveAppend, patch: SessionV3RealtimeLivePatchWire): void {
    let appendedBytes = 0
    try {
      appendedBytes = pending.text.append(patch.text)
    } catch {
      this.pauseStream(`${pending.sessionId}\u0000${pending.runId}\u0000${pending.streamId}`)
      return
    }
    pending.liveSeqEnd = patch.live_seq_end
    pending.offsetEnd = patch.offset_end
    pending.recordedAt = patch.recorded_at
    pending.bytes += appendedBytes
    this.pendingBytes += appendedBytes
    if (pending.bytes > PER_STREAM_PENDING_BYTE_LIMIT || this.pendingBytes > TOTAL_PENDING_BYTE_LIMIT) {
      this.pauseStream(`${pending.sessionId}\u0000${pending.runId}\u0000${pending.streamId}`)
    }
  }

  private tombstoneCommittedStream(key: string): void {
    const pending = this.pending.get(key)
    if (pending) {
      this.pendingBytes = Math.max(0, this.pendingBytes - pending.bytes)
      this.pending.delete(key)
    }
    this.committedStreamTombstones.add(key)
    this.cancelScheduledCallback()
    if (this.pending.size > 0) this.scheduleFlush()
  }

  private pauseStream(key: string): void {
    const pending = this.pending.get(key)
    if (pending) {
      this.pendingBytes = Math.max(0, this.pendingBytes - pending.bytes)
      this.pending.delete(key)
    }
    this.pausedStreams.add(key)
  }

  private scheduleFlush(): void {
    if (this.frameId !== null || this.timerId !== null || this.pending.size === 0) return
    const token = this.scheduleToken
    if (this.deps.isDocumentHidden()) {
      this.timerId = this.deps.setTimer(() => {
        if (token !== this.scheduleToken) return
        this.timerId = null
        this.flushEntries(Array.from(this.pending.keys()))
      }, HIDDEN_DOCUMENT_FLUSH_MS)
      return
    }
    this.frameId = this.deps.requestFrame(() => {
      if (token !== this.scheduleToken) return
      this.frameId = null
      this.flushEntries(Array.from(this.pending.keys()))
    })
  }

  private cancelScheduledCallback(): void {
    this.scheduleToken += 1
    if (this.frameId !== null) {
      this.deps.cancelFrame(this.frameId)
      this.frameId = null
    }
    if (this.timerId !== null) {
      this.deps.clearTimer(this.timerId)
      this.timerId = null
    }
  }

  private flushEntries(keys: string[]): void {
    const patches: SessionV3RealtimeLivePatchWire[] = []
    for (const key of keys) {
      const pending = this.pending.get(key)
      if (!pending) continue
      this.pending.delete(key)
      this.pendingBytes = Math.max(0, this.pendingBytes - pending.bytes)
      patches.push({
        session_id: pending.sessionId,
        run_id: pending.runId,
        stream_id: pending.streamId,
        stream_kind: 'assistant_text',
        operation: 'append',
        step: pending.step,
        step_id: pending.stepId,
        live_seq_start: pending.liveSeqStart,
        live_seq_end: pending.liveSeqEnd,
        offset_start: pending.offsetStart,
        offset_end: pending.offsetEnd,
        text: pending.text.toString(),
        recorded_at: pending.recordedAt,
      })
    }
    if (patches.length === 0) return
    const action: DesktopV3CacheAction = {
      type: 'realtime.applyLivePatchBatch',
      patches,
    }
    const previous = this.deps.getSnapshot()
    const next = applyDesktopV3LivePatchBatch(previous, patches)
    this.deps.commitSnapshot(previous, next, [action])
  }
}

export function livePatchKey(patch: Pick<SessionV3RealtimeLivePatchWire, 'session_id' | 'run_id' | 'stream_id'>): string {
  return `${patch.session_id}\u0000${patch.run_id}\u0000${patch.stream_id}`
}

function createPendingAppend(patch: SessionV3RealtimeLivePatchWire): PendingLiveAppend {
  const text = new Utf8AppendBuffer()
  const bytes = text.append(patch.text)
  return {
    sessionId: patch.session_id,
    runId: patch.run_id,
    streamId: patch.stream_id,
    step: patch.step,
    stepId: patch.step_id,
    liveSeqStart: patch.live_seq_start,
    liveSeqEnd: patch.live_seq_end,
    offsetStart: patch.offset_start,
    offsetEnd: patch.offset_end,
    text,
    bytes,
    recordedAt: patch.recorded_at,
  }
}

function findLiveStreamState(
  state: DesktopV3CacheState,
  sessionId: string,
  runId: string,
  streamId: string,
): { liveSeqEnd?: number; offsetEnd?: number; livePaused?: boolean } | null {
  const run = state.liveRunsBySession[sessionId]?.[runId]
  if (!run) return null
  if (run.assistantDraft?.streamId === streamId) return run.assistantDraft
  return run.assistantSegments?.find((segment) => segment.streamId === streamId) ?? null
}

export function createDefaultDesktopV3LivePatchCoordinatorDeps(input: {
  getSnapshot: () => DesktopV3CacheState
  commitSnapshot: DesktopV3LivePatchCoordinatorDeps['commitSnapshot']
}): DesktopV3LivePatchCoordinatorDeps {
  return {
    getSnapshot: input.getSnapshot,
    commitSnapshot: input.commitSnapshot,
    requestFrame: (callback) => {
      if (typeof requestAnimationFrame === 'function') return requestAnimationFrame(callback)
      return window.setTimeout(() => callback(Date.now()), 16)
    },
    cancelFrame: (id) => {
      if (typeof cancelAnimationFrame === 'function') {
        cancelAnimationFrame(id)
      } else {
        window.clearTimeout(id)
      }
    },
    setTimer: (callback, ms) => window.setTimeout(callback, ms),
    clearTimer: (id) => window.clearTimeout(id),
    isDocumentHidden: () => typeof document !== 'undefined' && document.hidden,
  }
}


export function durableAssistantStreamEffect(frame: RealtimeMessage): DurableAssistantStreamEffect | null {
  if (frame.kind !== 'event') return null
  const event = normalizeRealtimeEventFrame(frame)
  const payload = event.payload ?? {}
  if (event.eventType === 'session.assistant.delta' || event.eventType === 'session.message.delta') {
    const streamId = stringValue(payload.stream_id)
    const runId = resolveDesktopV3CacheEventRunId(event)
    const sessionId = event.sessionId
    if (!sessionId || !runId || !streamId) return null
    return {
      key: livePatchKey({ session_id: sessionId, run_id: runId, stream_id: streamId }),
      sessionId,
      runId,
      streamId,
      kind: 'checkpoint',
    }
  }

  const message = messageFromPayload(payload)
  if (message?.role !== 'assistant') return null
  const streamId = stringFromMetadata(message.metadata, 'stream_id')
  const runId = resolveDesktopV3CacheEventRunId(event)
    || stringFromMetadata(message.metadata, 'run_id')
    || stringFromMetadata(message.metadata, 'runId')
  const sessionId = event.sessionId || message.session_id
  if (!sessionId || !runId || !streamId) return null
  return {
    key: livePatchKey({ session_id: sessionId, run_id: runId, stream_id: streamId }),
    sessionId,
    runId,
    streamId,
    kind: 'committed-message',
  }
}

function messageFromPayload(payload: Record<string, unknown>): MessageSnapshot | null {
  const nested = payload.message
  if (nested && typeof nested === 'object' && !Array.isArray(nested)) return nested as MessageSnapshot
  return null
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stringFromMetadata(metadata: Record<string, unknown> | undefined, key: string): string {
  const value = metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}
