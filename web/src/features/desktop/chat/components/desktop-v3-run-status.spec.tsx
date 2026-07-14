import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { buildDesktopV3RunStatusModel, DesktopV3RunStatusPill, formatDesktopV3CurrentRunTimer, formatDesktopV3RunTimer, formatDesktopV3RunTimerLabel } from './desktop-v3-run-status'

test('DesktopV3RunStatusPill renders compacting label with live timer', () => {
  const startedAt = 10_000
  const model = { kind: 'active' as const, label: 'Compacting', startedAt, active: true }

  assert.equal(formatDesktopV3RunTimer(model, 72_345), '1:02')
  const markup = renderToStaticMarkup(<DesktopV3RunStatusPill model={model} now={72_345} />)

  assert.match(markup, /Compacting/)
  assert.match(markup, /1:02/)
})

test('buildDesktopV3RunStatusModel uses backend timing after refresh', () => {
  const model = buildDesktopV3RunStatusModel({
    currentRunIntent: {
      session_id: 'session-a',
      run_id: 'run-a',
      status: 'running',
      created_at: 1_000,
      started_at: 10_000,
      cumulative_duration_ms: 60_000,
      updated_at: 10_000,
      event_seq: 7,
    },
  })

  assert.equal(model?.startedAt, 10_000)
  assert.equal(model?.cumulativeDurationMs, 60_000)
  assert.equal(formatDesktopV3RunTimer(model!, 25_500), '1:15')
})

test('checkpoint timer continues from cumulative duration across backend runs', () => {
  const model = buildDesktopV3RunStatusModel({
    currentRunIntent: {
      session_id: 'session-a',
      run_id: 'run-b',
      status: 'running',
      created_at: 1_000,
      started_at: 120_000,
      cumulative_duration_ms: 90_000,
      updated_at: 120_000,
      event_seq: 9,
    },
  })

  assert.equal(formatDesktopV3CurrentRunTimer(model!, 125_000), '1:35')
  assert.equal(formatDesktopV3RunTimer(model!, 125_000), '1:35')
  assert.equal(formatDesktopV3RunTimerLabel(model!, 125_000), '1:35')

  const markup = renderToStaticMarkup(<DesktopV3RunStatusPill model={model} now={125_000} />)
  assert.match(markup, /Running/)
  assert.match(markup, /1:35/)
  assert.doesNotMatch(markup, /0:05/)
  assert.doesNotMatch(markup, /\(1:35\)/)
  assert.match(markup, /w-20/)
  assert.match(markup, /w-\[8ch\]/)
  assert.match(markup, /Checkpoint timer continues across checkpoint runs/)
})

test('terminal timer uses exact backend cumulative duration', () => {
  const model = buildDesktopV3RunStatusModel({
    latestRunIntent: {
      session_id: 'session-a',
      run_id: 'run-b',
      status: 'completed',
      created_at: 1_000,
      started_at: 120_000,
      completed_at: 125_000,
      duration_ms: 5_000,
      cumulative_duration_ms: 95_000,
      updated_at: 125_000,
      event_seq: 10,
    },
  })

  assert.equal(formatDesktopV3CurrentRunTimer(model!, 999_000), '0:05')
  assert.equal(formatDesktopV3RunTimer(model!, 999_000), '1:35')

  const markup = renderToStaticMarkup(<DesktopV3RunStatusPill model={model} now={999_000} />)
  assert.match(markup, /Completed/)
  assert.match(markup, /1:35/)
  assert.doesNotMatch(markup, />0:05</)
  assert.doesNotMatch(markup, /\(1:35\)/)
  assert.match(markup, /Loop timer: 0:05/)
  assert.equal(formatDesktopV3RunTimerLabel(model!, 999_000), '1:35')
})

test('terminal timer can render exact stored run duration without local timestamps', () => {
  const model = buildDesktopV3RunStatusModel({
    latestRunIntent: {
      session_id: 'session-a',
      run_id: 'run-a',
      status: 'completed',
      created_at: 1_000,
      duration_ms: 42_000,
      updated_at: 2_000,
      event_seq: 3,
    },
  })

  assert.equal(formatDesktopV3RunTimer(model!, 999_000), '0:42')
})

test('active run intent without started_at uses backend created_at timing', () => {
  const model = buildDesktopV3RunStatusModel({
    currentRunIntent: {
      session_id: 'session-a',
      run_id: 'run-a',
      status: 'running',
      created_at: 1_000,
      updated_at: 1_000,
      event_seq: 1,
    },
  })

  assert.equal(model?.label, 'Running')
  assert.equal(model?.startedAt, 1_000)
  assert.equal(formatDesktopV3RunTimer(model!, 3_500), '0:02')
})

test('pending executor run intent with timing is displayed as running', () => {
  const model = buildDesktopV3RunStatusModel({
    currentRunIntent: {
      session_id: 'session-a',
      run_id: 'run-pending',
      status: 'pending_executor',
      created_at: 1_000,
      updated_at: 1_000,
      event_seq: 1,
    },
  })

  assert.equal(model?.kind, 'active')
  assert.equal(model?.label, 'Running')
  assert.equal(model?.startedAt, 1_000)
  assert.equal(formatDesktopV3RunTimer(model!, 3_500), '0:02')

  const markup = renderToStaticMarkup(<DesktopV3RunStatusPill model={model} now={3_500} />)
  assert.match(markup, /Running/)
  assert.match(markup, /0:02/)
  assert.doesNotMatch(markup, /Starting/)
  assert.doesNotMatch(markup, /Pending execution/)
})

test('live run overlay does not render timerless Running fallback', () => {
  const model = buildDesktopV3RunStatusModel({
    liveRuns: [{ sessionId: 'session-a', runId: 'run-live', status: 'running', toolCallsByCallId: {} }],
  })

  assert.equal(model, null)
})

test('live run overlay uses backend timing when current run intent is temporarily terminal', () => {
  const model = buildDesktopV3RunStatusModel({
    latestRunIntent: {
      session_id: 'session-a',
      run_id: 'run-live',
      status: 'running',
      created_at: 1_000,
      started_at: 120_000,
      cumulative_duration_ms: 90_000,
      updated_at: 120_000,
      event_seq: 9,
    },
    liveRuns: [{ sessionId: 'session-a', runId: 'run-live', status: 'running', toolCallsByCallId: {} }],
  })

  assert.equal(formatDesktopV3RunTimer(model!, 125_000), '1:35')
})
