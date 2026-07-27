import test from 'node:test'
import assert from 'node:assert/strict'
import type { CodexResetCredit, CodexUsageWindow } from './api'
import {
  exactResetTime,
  remainingUsagePercent,
  resetCreditExpiry,
  resetCreditRedeemable,
  sortResetCreditsByExpiry,
  usageWindowLabel,
} from './view-model'

const windowBase: CodexUsageWindow = {
  used_percent: 25,
  limit_window_seconds: 300 * 60,
  reset_after_seconds: 60,
  reset_at: 1_700_000_000,
}

function credit(id: string, expiresAt: string | null, status = 'available'): CodexResetCredit {
  return { id, reset_type: 'codex_rate_limits', status, granted_at: '2026-01-01T00:00:00Z', expires_at: expiresAt }
}

test('maps backend usage percentages, durations, and Unix-second reset timestamps truthfully', () => {
  assert.equal(remainingUsagePercent(windowBase), 75)
  assert.equal(remainingUsagePercent({ ...windowBase, used_percent: 140 }), 0)
  assert.equal(remainingUsagePercent({ ...windowBase, used_percent: -5 }), 100)
  assert.equal(usageWindowLabel(300 * 60, 'Primary'), '5-hour')
  assert.equal(usageWindowLabel(10_080 * 60, 'Secondary'), 'Weekly')
  assert.equal(usageWindowLabel(48 * 60 * 60, 'Secondary'), '2-day')
  assert.equal(exactResetTime(windowBase.reset_at), new Date(windowBase.reset_at * 1000).toLocaleString())
})

test('sorts reset credits by nearest expiry and only enables available unexpired rows', () => {
  const now = Date.parse('2026-06-01T00:00:00Z')
  const sorted = sortResetCreditsByExpiry([
    credit('never', null),
    credit('later', '2026-08-01T00:00:00Z'),
    credit('sooner', '2026-07-01T00:00:00Z'),
  ])
  assert.deepEqual(sorted.map((item) => item.id), ['sooner', 'later', 'never'])
  assert.equal(resetCreditRedeemable(credit('available', '2026-07-01T00:00:00Z'), now), true)
  assert.equal(resetCreditRedeemable(credit('expired', '2026-05-01T00:00:00Z'), now), false)
  assert.equal(resetCreditRedeemable(credit('used', null, 'consumed'), now), false)
  assert.equal(resetCreditExpiry(credit('never', null)), 'Does not expire')
})
