import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import {
  AutomationSidebarMetadataRow,
  automationSidebarStatus,
  selectAutomationSummaryCounts,
  AutomationSidebarSummaryBadge,
} from './automation-v2-sidebar-metadata'

// Requirement: the sidebar identifies cadence, active running, and scheduled automation state,
// not generic session activity, and provides a clear top-down entry point to the Automations tool.
// Threat: active running occurrences look idle, pending/paused/expired schedules look live,
// or individual execution runs leak into the sidebar instead of a calm high-level summary.
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

test('automation status recognizes active running state while preserving safety guards', () => {
  const record = { enabled: true, cancelled: false, authorization: { kind: 'indefinite' } } as AutomationV2Record
  assert.equal(automationSidebarStatus(record, 1000, false), 'Scheduled')
  assert.equal(automationSidebarStatus(record, 1000, false, true), 'Running')
  assert.equal(automationSidebarStatus(record, 1000, false, [{ state: 'running' }]), 'Running')
  assert.equal(automationSidebarStatus(record, 1000, false, [{ state: 'in_progress' }]), 'Running')
  assert.equal(automationSidebarStatus(record, 1000, false, [{ state: 'succeeded' }]), 'Scheduled')
  assert.equal(automationSidebarStatus(record, 1000, true, true), 'Needs approval')
  assert.equal(automationSidebarStatus({ ...record, enabled: false }, 1000, false, true), 'Paused')
  assert.equal(automationSidebarStatus({ ...record, cancelled: true }, 1000, false, true), 'Cancelled')
  assert.equal(automationSidebarStatus({ ...record, authorization: { kind: 'at', expires_at: 1000 } }, 1000, false, true), 'Expired')
  assert.equal(automationSidebarStatus({ ...record, authorization: { kind: 'at', expires_at: 1001 } }, 1000, false, true), 'Running')
})

test('automation metadata row renders active running state and top-down entry point', () => {
  let navigated = false
  const runningMarkup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 1800 }}
      status="Running"
      running={true}
      workspaceSlug="my-project"
      onNavigateToAutomations={() => { navigated = true }}
    />
  )
  assert.match(runningMarkup, /Running/)
  assert.match(runningMarkup, /Every 30 minutes/)
  assert.match(runningMarkup, /data-testid="automation-running-dot"/)
  assert.match(runningMarkup, /animate-pulse/)
  assert.match(runningMarkup, /aria-label="Open Automations view"/)
  assert.match(runningMarkup, /Open Automations view/)

  const scheduledLinkMarkup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 3600 }}
      status="Scheduled"
      nextDueAt={2000000000000}
      workspaceSlug="my-project"
    />
  )
  assert.match(scheduledLinkMarkup, /Scheduled/)
  assert.match(scheduledLinkMarkup, /href="\/my-project\/automations"/)
  assert.match(scheduledLinkMarkup, /aria-label="Open Automations view"/)
  assert.doesNotMatch(scheduledLinkMarkup, /data-testid="automation-running-dot"/)

  // Direct click test
  const rowElement = AutomationSidebarMetadataRow({
    status: 'Running',
    onNavigateToAutomations: () => { navigated = true },
  })
  assert.ok(rowElement)
  const button = (rowElement as any).props.children[1]
  assert.equal(button.type, 'button')
  button.props.onClick({ preventDefault() {}, stopPropagation() {} })
  assert.equal(navigated, true)
})

test('summary counts aggregate active running and scheduled states without listing individual runs', () => {
  const progressKey = automationV2PageKey({ action: 'progress', workspace_id: 'ws-1', session_id: 's1', timezone: 'UTC' })
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-1' })
  const fakeState = {
    automationV2Pages: {
      [progressKey]: {
        input: { action: 'progress', workspace_id: 'ws-1', session_id: 's1', timezone: 'UTC' },
        generation: 1,
        loading: false,
        stale: false,
        data: {
          record: {
            automation_id: 'auto-1',
            session_id: 's1',
            workspace_id: 'ws-1',
            enabled: true,
            cancelled: false,
            authorization: { kind: 'indefinite' },
          } as AutomationV2Record,
          progress: {
            record: {
              automation_id: 'auto-1',
              session_id: 's1',
              workspace_id: 'ws-1',
              enabled: true,
              cancelled: false,
              authorization: { kind: 'indefinite' },
            } as AutomationV2Record,
            occurrences: [
              { id: 'occ-1', state: 'running', session_id: 'exec-1', due_at: 1000, accepted: {} as any },
              { id: 'occ-2', state: 'succeeded', session_id: 'exec-2', due_at: 500, accepted: {} as any },
            ],
          },
        },
      },
      [listKey]: {
        input: { action: 'list', workspace_id: 'ws-1' },
        generation: 1,
        loading: false,
        stale: false,
        data: {
          records: [
            {
              automation_id: 'auto-2',
              session_id: 's2',
              workspace_id: 'ws-1',
              enabled: true,
              cancelled: false,
              authorization: { kind: 'indefinite' },
            } as AutomationV2Record,
            {
              automation_id: 'auto-3',
              session_id: 's3',
              workspace_id: 'ws-1',
              enabled: false,
              cancelled: false,
              authorization: { kind: 'indefinite' },
            } as AutomationV2Record,
          ],
        },
      },
    },
    sessionsById: {},
    permissionsBySession: {},
    currentRunIntentBySession: {},
    runIntentsBySession: {},
  } as unknown as DesktopV3CacheState

  const counts = selectAutomationSummaryCounts(fakeState, 'ws-1', 1000)
  assert.equal(counts.running, 1)
  assert.equal(counts.scheduled, 1)
  assert.equal(counts.paused, 1)
  assert.equal(counts.pending, 0)
  assert.equal(counts.total, 3)
  assert.equal(counts.runsToday, 2)
  assert.equal(counts.upcoming, 0)
})

test('sidebar summary badge and metadata row display running state, runs today, and upcoming counts', () => {
  const badgeMarkup = renderToStaticMarkup(
    <AutomationSidebarSummaryBadge
      counts={{ running: 1, scheduled: 0, paused: 0, pending: 0, total: 1, runsToday: 4, upcoming: 2 }}
      workspaceSlug="team-workspace"
    />
  )
  assert.match(badgeMarkup, /1 running · 4 ran today · 2 upcoming/)
  assert.match(badgeMarkup, /data-testid="summary-running-dot"/)

  const rowMarkup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 3600 }}
      status="Running"
      running={true}
      runsToday={3}
      upcomingCount={2}
      workspaceSlug="my-project"
    />
  )
  assert.match(rowMarkup, /3 ran today · 2 upcoming/)
  assert.match(rowMarkup, /data-testid="automation-running-dot"/)
  assert.match(rowMarkup, /Running/)
})

test('sidebar summary badge displays calm high-level state and navigates on click', () => {
  let navigated = false
  const activeMarkup = renderToStaticMarkup(
    <AutomationSidebarSummaryBadge
      counts={{ running: 1, scheduled: 2, paused: 0, pending: 0, total: 3 }}
      workspaceSlug="team-workspace"
      onNavigate={() => { navigated = true }}
    />
  )
  assert.match(activeMarkup, /1 running · 2 scheduled/)
  assert.match(activeMarkup, /data-testid="summary-running-dot"/)
  assert.match(activeMarkup, /animate-pulse/)
  assert.match(activeMarkup, /Automations summary: 1 running · 2 scheduled/)
  assert.match(activeMarkup, /Open top-down Automations view/)
  // Ensures individual execution IDs are not exposed in calm indicator
  assert.doesNotMatch(activeMarkup, /occ-|exec-|av2-/)

  const scheduledOnlyMarkup = renderToStaticMarkup(
    <AutomationSidebarSummaryBadge
      counts={{ running: 0, scheduled: 2, paused: 0, pending: 0, total: 2 }}
      workspaceSlug="team-workspace"
    />
  )
  assert.match(scheduledOnlyMarkup, /2 scheduled/)
  assert.doesNotMatch(scheduledOnlyMarkup, /running/)
  assert.doesNotMatch(scheduledOnlyMarkup, /data-testid="summary-running-dot"/)
  assert.match(scheduledOnlyMarkup, /href="\/team-workspace\/automations"/)

  const emptyMarkup = renderToStaticMarkup(
    <AutomationSidebarSummaryBadge
      counts={{ running: 0, scheduled: 0, paused: 0, pending: 0, total: 0 }}
      workspaceSlug="team-workspace"
    />
  )
  assert.equal(emptyMarkup, '')

  // Direct click test
  const badgeElement = AutomationSidebarSummaryBadge({
    counts: { running: 1, scheduled: 1, paused: 0, pending: 0, total: 2 },
    onNavigate: () => { navigated = true },
  })
  assert.ok(badgeElement)
  assert.equal((badgeElement as any).type, 'button')
  ;(badgeElement as any).props.onClick({ preventDefault() {}, stopPropagation() {} })
  assert.equal(navigated, true)
})
