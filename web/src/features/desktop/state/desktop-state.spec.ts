import assert from 'node:assert/strict'
import test from 'node:test'

import {
  createEmptyDesktopState,
  desktopReducer,
  type DesktopDaemonEvent,
  type DesktopDaemonSnapshot,
  type DesktopState,
} from './desktop-state'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'

function session(id: string, updatedAt: number): DesktopSessionRecord {
  return {
    id,
    title: `Session ${id}`,
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    mode: 'auto',
    messageCount: 0,
    updatedAt,
    createdAt: updatedAt,
    permissionsHydrated: true,
    lifecycle: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
      step: 0,
      toolName: null,
    sidebarToolName: null,
      toolCallId: null,
      toolArguments: null,
      toolOutput: '',
      retainedToolName: null,
      retainedToolCallId: null,
      retainedToolArguments: null,
      retainedToolOutput: '',
      retainedToolState: null,
      summary: null,
      lastEventType: null,
      lastEventAt: null,
      error: null,
      seq: 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function message(sessionId: string, id: string, globalSeq: number): ChatMessageRecord {
  return {
    id,
    sessionId,
    globalSeq,
    role: 'assistant',
    content: id,
    createdAt: globalSeq,
  }
}

function replaceSnapshot(state: DesktopState, snapshot: DesktopDaemonSnapshot): DesktopState {
  return desktopReducer(state, { type: 'snapshot/replace', snapshot })
}

function mergeSnapshot(state: DesktopState, snapshot: DesktopDaemonSnapshot): DesktopState {
  return desktopReducer(state, { type: 'snapshot/merge', snapshot })
}

function applyEvent(state: DesktopState, event: DesktopDaemonEvent): DesktopState {
  return desktopReducer(state, { type: 'daemon/event', event })
}

test('snapshot replacement sets state.rev, replaces old records, and clears stale state', () => {
  const oldSession = session('old', 10)
  const nextSession = session('next', 20)
  const stale = desktopReducer(createEmptyDesktopState(), { type: 'connection/stale', reason: 'boot' })
  const previous = replaceSnapshot(stale, {
    rev: 4,
    sessionsById: { [oldSession.id]: oldSession },
    sessionOrder: [oldSession.id],
    messagesBySessionId: { [oldSession.id]: [message(oldSession.id, 'old-message', 1)] },
  })

  const next = replaceSnapshot(previous, {
    rev: 9,
    sessionsById: { [nextSession.id]: nextSession },
    sessionOrder: [nextSession.id],
    messagesBySessionId: { [nextSession.id]: [message(nextSession.id, 'next-message', 2)] },
  })

  assert.equal(next.rev, 9)
  assert.equal(next.status, 'ready')
  assert.equal(next.staleReason, null)
  assert.equal(next.resyncRequested, false)
  assert.deepEqual(Object.keys(next.sessionsById), ['next'])
  assert.equal(next.messagesBySessionId.old, undefined)
  assert.equal(next.messagesBySessionId.next?.[0]?.id, 'next-message')
})

test('scoped snapshot reconciliation removes stale persisted sessions missing from canonical workset', () => {
  const staleSession = session('stale-running', 10)
  staleSession.live.status = 'running'
  staleSession.live.runId = 'old-run'
  const canonicalSession = session('canonical-active', 20)
  canonicalSession.live.status = 'running'
  canonicalSession.live.runId = 'new-run'
  const otherWorkspaceSession = { ...session('other-workspace', 15), workspacePath: '/other', workspaceName: 'other' }
  const previous = replaceSnapshot(createEmptyDesktopState(), {
    rev: 5,
    sessionsById: {
      [staleSession.id]: staleSession,
      [canonicalSession.id]: { ...canonicalSession, title: 'Old canonical' },
      [otherWorkspaceSession.id]: otherWorkspaceSession,
    },
    sessionOrder: [staleSession.id, canonicalSession.id, otherWorkspaceSession.id],
    messagesBySessionId: {
      [staleSession.id]: [message(staleSession.id, 'stale-message', 1)],
      [canonicalSession.id]: [message(canonicalSession.id, 'hot-message', 2)],
    },
    runIntentsBySessionId: {
      [staleSession.id]: { sessionId: staleSession.id, runId: 'old-run', status: 'running', blockedReason: '', createdAt: 10, updatedAt: 10, eventSeq: 10 },
    },
    workspacesByPath: {
      '/workspace': { path: '/workspace', name: 'workspace', sessionIds: [staleSession.id, canonicalSession.id] },
      '/other': { path: '/other', name: 'other', sessionIds: [otherWorkspaceSession.id] },
    },
  })

  const reconciled = desktopReducer(previous, {
    type: 'snapshot/reconcile',
    snapshot: {
      rev: 6,
      reconcileSessionScope: { workspacePaths: ['/workspace'] },
      sessionsById: { [canonicalSession.id]: { ...canonicalSession, title: 'Canonical from API' } },
      sessionOrder: [canonicalSession.id],
      messagesBySessionId: { [canonicalSession.id]: [] },
      workspacesByPath: {
        '/workspace': { path: '/workspace', name: 'workspace', sessionIds: [canonicalSession.id] },
      },
    },
  })

  assert.equal(reconciled.sessionsById[staleSession.id], undefined)
  assert.equal(reconciled.messagesBySessionId[staleSession.id], undefined)
  assert.equal(reconciled.runIntentsBySessionId[staleSession.id], undefined)
  assert.deepEqual(reconciled.workspacesByPath['/workspace']?.sessionIds, [canonicalSession.id])
  assert.equal(reconciled.sessionsById[canonicalSession.id]?.title, 'Canonical from API')
  assert.equal(reconciled.sessionsById[canonicalSession.id]?.live.status, 'running')
  assert.deepEqual(reconciled.messagesBySessionId[canonicalSession.id]?.map((record) => record.id), ['hot-message'])
  assert.equal(reconciled.sessionsById[otherWorkspaceSession.id]?.id, otherWorkspaceSession.id)
})

test('metadata-only snapshot merge preserves existing messages when backend omits history resources', () => {
  const activeSession = session('active', 20)
  const existing = replaceSnapshot(createEmptyDesktopState(), {
    rev: 5,
    sessionsById: { [activeSession.id]: activeSession },
    sessionOrder: [activeSession.id],
    messagesBySessionId: { [activeSession.id]: [message(activeSession.id, 'hot-message', 5)] },
  })

  const merged = mergeSnapshot(existing, {
    rev: 6,
    sessionsById: { [activeSession.id]: { ...activeSession, updatedAt: 21 } },
    sessionOrder: [activeSession.id],
    messagesBySessionId: { [activeSession.id]: [] },
  })

  assert.deepEqual(merged.messagesBySessionId.active?.map((record) => record.id), ['hot-message'])
})

test('snapshot replacement trims messages to newest 200 and deduplicates by id', () => {
  const activeSession = session('active', 20)
  const messages = Array.from({ length: 205 }, (_, index) => message(activeSession.id, `msg-${index + 1}`, index + 1))
  const updatedDuplicate = {
    ...message(activeSession.id, 'msg-150', 150),
    content: 'updated duplicate',
    createdAt: 9999,
  }

  const next = replaceSnapshot(createEmptyDesktopState(), {
    rev: 9,
    sessionsById: { [activeSession.id]: activeSession },
    sessionOrder: [activeSession.id],
    messagesBySessionId: { [activeSession.id]: [messages[10]!, ...messages, updatedDuplicate] },
  })

  const hotMessages = next.messagesBySessionId[activeSession.id] ?? []
  assert.equal(hotMessages.length, 200)
  assert.equal(hotMessages[0]?.id, 'msg-6')
  assert.equal(hotMessages[hotMessages.length - 1]?.id, 'msg-205')
  assert.equal(hotMessages.find((record) => record.id === 'msg-150')?.content, 'updated duplicate')
  assert.equal(hotMessages.filter((record) => record.id === 'msg-150').length, 1)
})

test('snapshot merge preserves newer streamed messages while trimming older hydrated messages', () => {
  const activeSession = session('active', 20)
  const existing = replaceSnapshot(createEmptyDesktopState(), {
    rev: 5,
    sessionsById: { [activeSession.id]: activeSession },
    sessionOrder: [activeSession.id],
    messagesBySessionId: {
      [activeSession.id]: Array.from({ length: 200 }, (_, index) => message(activeSession.id, `msg-${index + 1}`, index + 1)),
    },
  })

  const merged = mergeSnapshot(existing, {
    rev: 6,
    sessionsById: { [activeSession.id]: { ...activeSession, updatedAt: 21 } },
    sessionOrder: [activeSession.id],
    messagesBySessionId: {
      [activeSession.id]: [message(activeSession.id, 'msg-201', 201)],
    },
  })

  const hotMessages = merged.messagesBySessionId[activeSession.id] ?? []
  assert.equal(hotMessages.length, 200)
  assert.equal(hotMessages[0]?.id, 'msg-2')
  assert.equal(hotMessages[hotMessages.length - 1]?.id, 'msg-201')
})

test('snapshot replacement hydrates session usage for context badge projections', () => {
  const nextSession = session('next', 20)

  const next = replaceSnapshot(createEmptyDesktopState(), {
    rev: 9,
    sessionsById: { [nextSession.id]: nextSession },
    sessionOrder: [nextSession.id],
    usageBySessionId: {
      [nextSession.id]: {
        sessionId: nextSession.id,
        provider: 'codex',
        model: 'gpt-5.4',
        source: 'provider_api_usage',
        contextWindow: 1000,
        totalTokens: 250,
        remainingTokens: 750,
        updatedAt: 30,
      },
    },
  })

  assert.equal(next.sessionsById.next?.usage?.remainingTokens, 750)
  assert.equal(next.sessionsById.next?.usage?.contextWindow, 1000)
  assert.equal(next.sessionsById.next?.updatedAt, 30)
})

test('event with matching prevRev applies and advances state.rev', () => {
  const state = replaceSnapshot(createEmptyDesktopState(), { rev: 10 })
  const upserted = session('session-1', 30)

  // matching prevRev / continuous rev: prevRev is intentionally state.rev.
  const next = applyEvent(state, {
    rev: 11,
    prevRev: state.rev,
    type: 'desktop/session/upsert',
    payload: { session: upserted },
  })

  assert.equal(next.rev, 11)
  assert.equal(next.status, 'ready')
  assert.equal(next.staleReason, null)
  assert.equal(next.sessionsById['session-1'], upserted)
  assert.deepEqual(next.sessionOrder, ['session-1'])
})

test('usage events update session usage without marking the desktop state stale', () => {
  const existing = session('session-1', 30)
  const state = replaceSnapshot(createEmptyDesktopState(), {
    rev: 10,
    sessionsById: { [existing.id]: existing },
    sessionOrder: [existing.id],
  })

  const next = applyEvent(state, {
    rev: 11,
    prevRev: 10,
    type: 'run.usage.updated',
    payload: {
      session_id: existing.id,
      source_seq: 4,
      ts_unix_ms: 40,
      usage_summary: {
        session_id: existing.id,
        provider: 'codex',
        model: 'gpt-5.4',
        source: 'provider_api_usage',
        context_window: 1000,
        total_tokens: 250,
        remaining_tokens: 750,
        updated_at: 40,
      },
    },
  })

  assert.equal(next.status, 'ready')
  assert.equal(next.staleReason, null)
  assert.equal(next.usageBySessionId['session-1']?.remainingTokens, 750)
  assert.equal(next.sessionsById['session-1']?.usage?.remainingTokens, 750)
  assert.equal(next.sessionsById['session-1']?.live.lastEventType, 'run.usage.updated')
  assert.equal(next.sessionsById['session-1']?.lastEventSeq, 4)
})

test('event with prevRev mismatch marks stale and does not apply payload', () => {
  const state = replaceSnapshot(createEmptyDesktopState(), { rev: 10 })

  const next = applyEvent(state, {
    rev: 12,
    prevRev: 8,
    type: 'desktop/session/upsert',
    payload: { session: session('bad', 30) },
  })

  assert.equal(next.rev, 10)
  assert.equal(next.status, 'stale')
  assert.equal(next.resyncRequested, true)
  assert.match(next.staleReason ?? '', /rev mismatch/)
  assert.equal(next.sessionsById.bad, undefined)
})

test('duplicate old event with rev <= state.rev does not corrupt state', () => {
  const existing = session('existing', 30)
  const state = replaceSnapshot(createEmptyDesktopState(), {
    rev: 10,
    sessionsById: { [existing.id]: existing },
    sessionOrder: [existing.id],
  })

  const next = applyEvent(state, {
    rev: 9,
    prevRev: 8,
    type: 'desktop/session/upsert',
    payload: { session: session('old-event', 40) },
  })

  assert.equal(next, state)
  assert.equal(next.sessionsById['old-event'], undefined)
  assert.equal(next.sessionsById.existing, existing)
})

test('missing or non-finite revision metadata does not apply payload', () => {
  const state = replaceSnapshot(createEmptyDesktopState(), { rev: 10 })

  const missingRev = applyEvent(state, {
    rev: Number.NaN,
    prevRev: 10,
    type: 'desktop/session/upsert',
    payload: { session: session('missing-rev', 40) },
  })

  const missingPrevRev = applyEvent(state, {
    rev: 11,
    prevRev: Number.POSITIVE_INFINITY,
    type: 'desktop/session/upsert',
    payload: { session: session('missing-prev-rev', 50) },
  })

  assert.equal(missingRev.status, 'stale')
  assert.equal(missingRev.sessionsById['missing-rev'], undefined)
  assert.equal(missingPrevRev.status, 'stale')
  assert.equal(missingPrevRev.sessionsById['missing-prev-rev'], undefined)
})

test('resync snapshot after stale replaces state and clears stale flag', () => {
  const state = replaceSnapshot(createEmptyDesktopState(), { rev: 3 })
  const stale = applyEvent(state, {
    rev: 5,
    prevRev: 1,
    type: 'desktop/message/upsert',
    payload: { message: message('session-1', 'bad', 1) },
  })

  const resynced = replaceSnapshot(stale, {
    rev: 6,
    sessionsById: { 'session-1': session('session-1', 50) },
    sessionOrder: ['session-1'],
    messagesBySessionId: { 'session-1': [message('session-1', 'good', 2)] },
  })

  assert.equal(stale.status, 'stale')
  assert.equal(resynced.rev, 6)
  assert.equal(resynced.status, 'ready')
  assert.equal(resynced.staleReason, null)
  assert.equal(resynced.resyncRequested, false)
  assert.deepEqual(resynced.messagesBySessionId['session-1'].map((record) => record.id), ['good'])
})
