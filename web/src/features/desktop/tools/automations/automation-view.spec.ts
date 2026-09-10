import assert from 'node:assert/strict'
import test from 'node:test'
import { dayKey, nextDayDelay, groupUpdates, needsAttention, parseInstructions, validatePlans } from './automation-view'
import type { AutomationRecord } from '../../state/desktop-automation-api'

// Requirement: presentation follows the selected zone across DST without status
// polling. Threat: UTC grouping mislabels daily outcomes. These pure helpers own
// grouping/rollover; this is the narrowest layer proving their date arithmetic.
test('dates and next rollover honor timezone and DST', () => {
  const now = Date.parse('2026-03-08T05:00:00Z')
  assert.equal(dayKey(now, 'America/New_York'), '2026-03-08')
  const delay = nextDayDelay(now, 'America/New_York')
  assert.ok(Math.abs(delay - 23 * 3600000) < 1100)
  assert.equal(dayKey(Date.parse('2026-01-01T01:00:00Z'), 'America/Los_Angeles'), '2025-12-31')
})
// Requirement: blocked incidents retain individual identities; no aggregation can
// hide an unrelated success. groupUpdates/needsAttention are the display boundary.
test('grouping preserves isolated incidents and immutable input order', () => {
  const rows = [
    { id: 'success', written_at: 2000, outcome: { kind: 'completed', summary: 'Done' } },
    { id: 'blocked', written_at: 1000, outcome: { kind: 'blocked', summary: 'Approval needed' } },
  ] as AutomationRecord[]
  assert.deepEqual(groupUpdates(rows, 'UTC')[0][1].map(row => row.id), ['success', 'blocked'])
  assert.deepEqual(rows.filter(needsAttention).map(row => row.id), ['blocked'])
  assert.equal(rows[0].id, 'success')
})
// Requirement: ordered references reject forward/cyclic/duplicate dependencies
// before submission, and user context cannot accidentally contain structured agent
// authority. Server remains authoritative; these are editor input tests only.
test('plan ordering and context input reject ambiguous writes', () => {
  const first = { id: 'first', plan: { session_id: 's', plan_id: 'p', revision: 1 } }
  const second = { id: 'second', plan: { session_id: 's', plan_id: 'q', revision: 2 }, depends_on: ['first'] }
  assert.doesNotThrow(() => validatePlans([first, second]))
  assert.throws(() => validatePlans([second, first]), /earlier/)
  assert.throws(() => validatePlans([first, first]), /unique/)
  assert.throws(() => validatePlans([{ ...first, plan: { ...first.plan, revision: 0 } }]))
  assert.throws(() => parseInstructions('{"policy":{"approved":true}}'))
  assert.throws(() => parseInstructions('[]'))
  assert.deepEqual(parseInstructions('{"brief":"Keep it concise"}'), { brief: 'Keep it concise' })
})
