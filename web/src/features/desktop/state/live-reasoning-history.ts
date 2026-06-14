import type { DesktopLiveReasoningRecord, DesktopSessionRecord } from '../types/realtime'

type LiveState = DesktopSessionRecord['live']

function stringField(payload: Record<string, unknown>, key: string): string {
  const value = payload[key]
  return typeof value === 'string' ? value.trim() : ''
}

function numberField(payload: Record<string, unknown>, key: string, fallback: number): number {
  const value = payload[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function reasoningRecordKey(runId: string, stepId: string, reasoningId: string): string {
  return `${runId}:${stepId}:${reasoningId}`
}

function fallbackReasoningId(payload: Record<string, unknown>, step: number): string {
  return stringField(payload, 'reasoning_key') || `reasoning-${step}`
}

function normalizeReasoningPayload(
  live: LiveState,
  payload: Record<string, unknown>,
): Omit<DesktopLiveReasoningRecord, 'text' | 'summary' | 'state' | 'startedAt' | 'completedAt' | 'timelineSeq' | 'updatedSeq'> {
  const runId = stringField(payload, 'run_id') || live.runId || ''
  const step = numberField(payload, 'step', live.step > 0 ? live.step : 1)
  const stepId = stringField(payload, 'step_id') || `step-${step}`
  const reasoningId = stringField(payload, 'reasoning_id') || fallbackReasoningId(payload, step)
  const reasoningKey = stringField(payload, 'reasoning_key') || reasoningId
  return {
    key: reasoningRecordKey(runId, stepId, reasoningId),
    runId,
    step,
    stepId,
    reasoningId,
    reasoningKey,
  }
}

function sortReasoningHistory(history: DesktopLiveReasoningRecord[]): DesktopLiveReasoningRecord[] {
  return history.slice().sort((a, b) => {
    if (a.timelineSeq !== b.timelineSeq) {
      return b.timelineSeq - a.timelineSeq
    }
    if (a.startedAt !== b.startedAt) {
      return b.startedAt - a.startedAt
    }
    return b.key.localeCompare(a.key)
  })
}

function upsertReasoningRecord(
  live: LiveState,
  payload: Record<string, unknown>,
  eventType: string,
  ts: number,
  seq: number,
): DesktopLiveReasoningRecord {
  const identity = normalizeReasoningPayload(live, payload)
  const history = live.reasoningHistory ?? []
  const existingIndex = history.findIndex((record) => record.key === identity.key)
  const existing = existingIndex >= 0 ? history[existingIndex] : null
  const delta = typeof payload.delta === 'string' ? payload.delta : ''
  const summary = typeof payload.summary === 'string' ? payload.summary : ''
  const isStarted = eventType === 'session.reasoning.started'
  const isCompleted = eventType === 'session.reasoning.completed'
  const isErrored = eventType === 'session.reasoning.error' || eventType === 'session.reasoning.failed'
  const previousText = existing?.text ?? ''
  // The V3 backend coalesces provider reasoning into snapshot deltas for the
  // canonical session.reasoning.delta event. Replace the live text with the
  // newest snapshot instead of appending, or the real stream duplicates text.
  const nextText = delta || previousText
  const nextSummary = summary || existing?.summary || nextText
  const state: DesktopLiveReasoningRecord['state'] = isErrored ? 'error' : isCompleted ? 'done' : 'running'
  const record: DesktopLiveReasoningRecord = {
    ...identity,
    text: nextText,
    summary: nextSummary,
    state,
    startedAt: existing?.startedAt ?? ts,
    completedAt: isCompleted || isErrored ? ts : existing?.completedAt ?? null,
    timelineSeq: existing?.timelineSeq || seq,
    updatedSeq: seq,
  }
  if (isStarted && existing) {
    record.text = existing.text
    record.summary = existing.summary
    record.state = existing.state === 'done' || existing.state === 'error' ? existing.state : 'running'
    record.completedAt = existing.completedAt
  }

  const nextHistory = existingIndex >= 0 ? history.slice() : history.concat(record)
  if (existingIndex >= 0) {
    nextHistory[existingIndex] = record
  }
  live.reasoningHistory = sortReasoningHistory(nextHistory)
  return record
}

export function applyCanonicalReasoningEventToLiveHistory(
  live: LiveState,
  payload: Record<string, unknown>,
  eventType: string,
  ts: number,
  seq: number,
): DesktopLiveReasoningRecord {
  if (!live.reasoningHistory) {
    live.reasoningHistory = []
  }
  return upsertReasoningRecord(live, payload, eventType, ts, seq)
}

export function completeLiveReasoningHistory(
  live: LiveState,
  ts: number,
  seq: number,
  state: 'done' | 'error' = 'done',
): void {
  const history = live.reasoningHistory ?? []
  if (history.length === 0) {
    return
  }
  let changed = false
  live.reasoningHistory = sortReasoningHistory(history.map((record) => {
    if (record.state !== 'running') {
      return record
    }
    changed = true
    return {
      ...record,
      state,
      completedAt: record.completedAt ?? ts,
      updatedSeq: Math.max(record.updatedSeq ?? 0, seq),
    }
  }))
  if (!changed) {
    return
  }
}
