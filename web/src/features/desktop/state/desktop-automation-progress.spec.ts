import assert from 'node:assert/strict'
import test from 'node:test'
import type { AutomationDefinition, AutomationProgress } from './desktop-automation-api'
import { automationFrequency, automationProgressSummary, editedAutomationDefinition } from './desktop-automation-progress'

// Requirement: ScheduleProgress is bounded occurrence evidence, never a daily quota.
// Threat: partial pages, retries and forecasts falsely presented as completed work.
// This narrow presentation layer consumes server counts without a second scheduler.
const schedule: AutomationDefinition['schedule'] = { kind: 'cron', expression: '0 18 * * *', timezone: 'UTC', missed_policy: 'skip', overlap_policy: 'independent' }
const progress: AutomationProgress = {
  automation_id: 'automation', definition_revision: 2, schedule, display_timezone: 'UTC', day_start: 1000, day_end: 100000, as_of: 2000,
  freshness: 'non_atomic_read', history_complete: true, forecast_complete: true, upcoming_complete: true, forecast_horizon_end: 200000,
  planned_slots: [{ scheduled_at: 5000, definition_revision: 2, forecast: true }], upcoming_slots: [], counts: { completed: 0, failed: 1, running: 1 }, manual_counts: { completed: 4 }, unknown_trigger_count: 0, occurrences: [],
  timing_availability: 'unavailable', missed_availability: 'unavailable', outcome_availability: 'unavailable', next_eligible: { scheduled_at: 5000, definition_revision: 2, forecast: true },
}
test('daily forecasts are not a denominator; failures and manual completions remain separate', () => {
  const summary = automationProgressSummary(progress, false, 2000)
  assert.match(summary, /Daily · 18:00 UTC/)
  assert.match(summary, /0 completed/)
  assert.match(summary, /1 failed/)
  assert.match(summary, /1 running/)
  assert.doesNotMatch(summary, /0\/1|4 completed/)
  const multiple = { ...progress, schedule: { ...schedule, expression: '0 */6 * * *' }, counts: { completed: 2 }, planned_slots: [...progress.planned_slots, ...progress.planned_slots] }
  assert.match(automationProgressSummary(multiple, false, 2000), /2 completed/)
  assert.doesNotMatch(automationProgressSummary(multiple, false, 2000), /2\/2/)
})
test('partial observations, old days, pause and expiry never claim runnable forecasts', () => {
  assert.match(automationProgressSummary({ ...progress, history_complete: false }, false, 2000), /at least 0 completed/)
  for (const reason of ['paused', 'expired', 'approval_required']) {
    const summary = automationProgressSummary({ ...progress, no_next_reason: reason }, false, 2000)
    assert.doesNotMatch(summary, /next .*conditional/)
    assert.match(summary, new RegExp(reason.replaceAll('_', ' ')))
  }
  assert.match(automationProgressSummary(progress, false, progress.day_end), /stale/)
  assert.match(automationProgressSummary(progress, true, 2000), /stale/)
  assert.equal(automationProgressSummary(undefined, false, 2000), 'Progress unavailable')
})
// Requirement: NormalizeSchedule forbids cron timezone on other triggers and limits
// intervals to 60..366*86400. Editing must invalidate the grant without changing pins.
test('edits strip cron-only timezone, reject unsupported intervals and retain exact pins', () => {
  const original: AutomationDefinition = { name: 'Review', session_id: 'conversation', enabled: true, plans: [{ id: 'primary', plan: { session_id: 'conversation', plan_id: 'plan', revision: 3, document_sha256: 'pin' } }], schedule, authorization: { mode: 'approved_policy', approval_reference: 'old' } }
  for (const kind of ['manual', 'interval', 'event'] as const) {
    const result = editedAutomationDefinition({ ...original, schedule: { kind, timezone: 'UTC', interval_seconds: kind === 'interval' ? 60 : undefined, trigger_source: kind === 'event' ? 'source' : undefined, missed_policy: 'skip', overlap_policy: 'serialize' } })
    assert.equal(result.schedule.timezone, undefined)
    assert.equal(result.enabled, false)
    assert.equal(result.authorization.approval_reference, undefined)
    assert.equal(result.authorization.mode, 'approval_required')
    assert.deepEqual(result.plans, original.plans)
    assert.equal(result.session_id, original.session_id)
  }
  for (const interval_seconds of [1, 59, 60.5, 31622401, NaN]) assert.throws(() => editedAutomationDefinition({ ...original, schedule: { ...schedule, kind: 'interval', interval_seconds } }), /Interval/)
  assert.equal(original.authorization.approval_reference, 'old')
  assert.match(automationFrequency({ ...schedule, kind: 'interval', interval_seconds: 3600 }), /elapsed/)
})
