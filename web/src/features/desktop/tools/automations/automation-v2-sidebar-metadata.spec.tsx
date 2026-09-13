import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { AutomationSidebarMetadataRow, automationSidebarStatus } from './automation-v2-sidebar-metadata'

// Requirement: the sidebar's second row identifies cadence and automation state,
// not generic session activity. Threat: pending, paused or expired schedules look
// live, or cadence/timezone disappears. These presentation-layer assertions use
// the actual row and status formatter; network authority stays in the V2 runtime.
test('automation metadata renders cadence, timezone and explicit pending state', () => {
  const interval = renderToStaticMarkup(<AutomationSidebarMetadataRow schedule={{ kind: 'interval', interval_seconds: 3600 }} status="Scheduled" />)
  assert.match(interval, /Every hour/)
  assert.match(interval, /24 runs \/ day on average/)
  assert.match(interval, /Scheduled/)
  assert.match(interval, /aria-label="Automation metadata"/)
  const pending = renderToStaticMarkup(<AutomationSidebarMetadataRow schedule={{ kind: 'cron', cron: '0 9 * * *', timezone: 'Europe/Paris' }} status="Awaiting acceptance" />)
  assert.match(pending, /Daily at 09:00 · Europe\/Paris/)
  assert.match(pending, /Awaiting acceptance/)
  assert.doesNotMatch(pending, /Next scheduled:/)
  const missing = renderToStaticMarkup(<AutomationSidebarMetadataRow status="Schedule unavailable" />)
  assert.match(missing, /Schedule unavailable/)
  assert.doesNotMatch(missing, /Scheduled|Next scheduled:/)
})

test('automation status never describes cancelled, paused or expired records as scheduled', () => {
  const record = { enabled: true, cancelled: false, authorization: { kind: 'indefinite' } } as AutomationV2Record
  assert.equal(automationSidebarStatus(record, 1000, false), 'Scheduled')
  assert.equal(automationSidebarStatus(record, 1000, true), 'Needs approval')
  assert.equal(automationSidebarStatus({ ...record, enabled: false }, 1000, false), 'Paused')
  assert.equal(automationSidebarStatus({ ...record, cancelled: true }, 1000, true), 'Cancelled')
  assert.equal(automationSidebarStatus({ ...record, authorization: { kind: 'at', expires_at: 1000 } }, 1000, false), 'Expired')
  assert.equal(automationSidebarStatus({ ...record, authorization: { kind: 'at', expires_at: 1001 } }, 1000, false), 'Scheduled')
})
