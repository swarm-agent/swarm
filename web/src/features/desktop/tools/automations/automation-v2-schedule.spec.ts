import assert from 'node:assert/strict'
import test from 'node:test'
import { intervalLabel, scheduleFrequency, scheduleLabel, scheduleMode, scheduleTime } from './automation-v2-schedule'

// Requirement: friendly schedule controls preserve exact V2 elapsed/calendar
// semantics. Threat: rounding unusual intervals, misclassifying stepped cron,
// or promising daily runs on restricted calendar days. Pure presentation helpers
// are the narrowest layer; dispatch and DST execution remain server-owned.
test('schedule presentation preserves unusual intervals and calendar restrictions', () => {
  assert.equal(intervalLabel(3600), 'Every hour')
  assert.equal(intervalLabel(900), 'Every 15 minutes')
  assert.equal(intervalLabel(61), 'Every 1 min 1 sec')
  assert.equal(scheduleFrequency({ kind: 'interval', interval_seconds: 3600 }), '24 runs / day on average')
  assert.equal(scheduleFrequency({ kind: 'interval', interval_seconds: 172800 }), 'Less than 1 run / day on average')
  assert.equal(scheduleFrequency({ kind: 'interval', interval_seconds: 0 }), 'Choose a valid interval')
  const weekly = { kind: 'cron' as const, cron: '30 14 * * 5', timezone: 'UTC' }
  assert.equal(scheduleMode(weekly), 'weekly')
  assert.equal(scheduleTime(weekly), '14:30')
  assert.equal(scheduleLabel(weekly), 'Friday at 14:30')
  assert.equal(scheduleFrequency(weekly), '1 scheduled run / week')
  const stepped = { kind: 'cron' as const, cron: '*/17 */5 * * *', timezone: 'America/New_York' }
  const original = structuredClone(stepped)
  assert.equal(scheduleMode(stepped), 'advanced')
  assert.equal(scheduleFrequency(stepped), '20 scheduled runs / day · DST may vary')
  assert.equal(scheduleTime(stepped), '')
  assert.deepEqual(stepped, original)
  assert.equal(scheduleFrequency({ ...stepped, cron: '0 9 1 * *' }), 'Runs on matching calendar days')
  assert.equal(scheduleMode({ ...stepped, cron: '0 9 1 * *' }), 'advanced')
})
