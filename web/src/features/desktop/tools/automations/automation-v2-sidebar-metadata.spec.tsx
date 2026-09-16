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
  AutomationSidebarCompactCard,
  AutomationSidebarCompactCardView,
  AutomationSidebarExpandedContainerView,
  formatAutomationHeadline,
} from './automation-v2-sidebar-metadata'
import {
  sidebarVisibleGroupNodes,
  SIDEBAR_AUTOMATION_VISIBLE_ROOT_LIMIT,
} from '../../layout/desktop-app-page'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'

// Requirement: the sidebar identifies cadence, active running, and scheduled automation state,
// not generic session activity, and provides a clear top-down entry point to the Automations tool.
// Threat: active running occurrences look idle, pending/paused/expired schedules look live,
// or individual execution runs leak into the sidebar instead of a calm high-level summary.
test('automation metadata renders cadence, timezone and explicit pending state', () => {
  const interval = renderToStaticMarkup(<AutomationSidebarMetadataRow schedule={{ kind: 'interval', interval_seconds: 3600 }} status="Scheduled" />)
  assert.match(interval, /Every hour/)
  assert.match(interval, /24 runs \/ day on average/)
  assert.match(interval, /Scheduled/)
  assert.match(interval, /aria-label="Worker metadata"/)
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
  assert.match(runningMarkup, /aria-label="Open Workers view"/)
  assert.match(runningMarkup, /Open Workers view/)

  const scheduledLinkMarkup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 3600 }}
      status="Scheduled"
      nextDueAt={2000000000000}
      workspaceSlug="my-project"
    />
  )
  assert.match(scheduledLinkMarkup, /Scheduled/)
  assert.match(scheduledLinkMarkup, /href="\/my-project\/workers"/)
  assert.match(scheduledLinkMarkup, /aria-label="Open Workers view"/)
  assert.doesNotMatch(scheduledLinkMarkup, /data-testid="automation-running-dot"/)

  // Direct click test
  const rowElement = AutomationSidebarMetadataRow({
    status: 'Running',
    onNavigateToAutomations: () => { navigated = true },
  })
  assert.ok(rowElement)
  const button = (rowElement as any).props.children[0].props.children[1]
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
  assert.match(rowMarkup, /3 done today · 2 left/)
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
  assert.match(activeMarkup, /Workers summary: 1 running · 2 scheduled/)
  assert.match(activeMarkup, /Open top-down Workers view/)
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
  assert.match(scheduledOnlyMarkup, /href="\/team-workspace\/workers"/)

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

test('compact card renders overview, running pulse dot, stats, and expands on click or keypress', () => {
  let expanded = false
  let navigated = false

  const compactMarkup = renderToStaticMarkup(
    <AutomationSidebarCompactCardView
      counts={{ running: 1, scheduled: 2, paused: 0, pending: 0, total: 3, runsToday: 4, upcoming: 2, totalJobs: 6, alerts: 0 }}
      rootCount={8}
      onExpand={() => { expanded = true }}
      onOpenAutomations={() => { navigated = true }}
    />
  )
  // Verifies compact card is strictly constrained to sidebar width without overflowing
  assert.match(compactMarkup, /w-full min-w-0 max-w-full box-border/)
  assert.match(compactMarkup, /overflow-hidden/)
  assert.match(compactMarkup, /3 workers today/)
  assert.match(compactMarkup, /data-testid="compact-running-dot"/)
  assert.match(compactMarkup, /6 jobs · 2 left/)
  assert.match(compactMarkup, /4 jobs done today/)
  assert.match(compactMarkup, /Expand/)
  assert.match(compactMarkup, /data-testid="compact-expand-button"/)
  assert.match(compactMarkup, /data-testid="compact-view-button"/)

  // Direct element test: clicking card navigates to top-down view when onOpenAutomations is present
  const element = AutomationSidebarCompactCardView({
    counts: { running: 0, scheduled: 5, paused: 0, pending: 0, total: 5, runsToday: 0, upcoming: 0, alerts: 0 },
    rootCount: 5,
    onExpand: () => { expanded = true },
    onOpenAutomations: () => { navigated = true },
  })
  assert.ok(element)
  assert.equal((element as any).type, 'div')
  ;(element as any).props.onClick()
  assert.equal(navigated, true)

  navigated = false
  ;(element as any).props.onKeyDown({ key: 'Enter', preventDefault() {} })
  assert.equal(navigated, true)

  navigated = false
  ;(element as any).props.onKeyDown({ key: ' ', preventDefault() {} })
  assert.equal(navigated, true)

  // When onOpenAutomations is absent, clicking card expands
  const expandFallbackElement = AutomationSidebarCompactCardView({
    counts: { running: 0, scheduled: 5, paused: 0, pending: 0, total: 5, runsToday: 0, upcoming: 0, alerts: 0 },
    rootCount: 5,
    onExpand: () => { expanded = true },
  })
  ;(expandFallbackElement as any).props.onClick()
  assert.equal(expanded, true)

  // Find and trigger the explicit Expand button
  expanded = false
  const bottomRow = (element as any).props.children[1]
  const actionsContainer = bottomRow.props.children[1]
  const expandButton = actionsContainer.props.children[1]
  assert.ok(expandButton)
  assert.equal(expandButton.props['data-testid'], 'compact-expand-button')
  expandButton.props.onClick({ stopPropagation() {} })
  assert.equal(expanded, true)

  // Find and trigger the View button
  navigated = false
  const viewButton = actionsContainer.props.children[0]
  assert.ok(viewButton)
  assert.equal(viewButton.props['data-testid'], 'compact-view-button')
  viewButton.props.onClick({ stopPropagation() {} })
  assert.equal(navigated, true)
})

test('compact card renders alert pill and warning icon when alerts are present', () => {
  const alertMarkup = renderToStaticMarkup(
    <AutomationSidebarCompactCardView
      counts={{ running: 0, scheduled: 2, paused: 0, pending: 0, total: 2, runsToday: 3, upcoming: 1, totalJobs: 4, alerts: 2 }}
      rootCount={2}
      onExpand={() => {}}
      onOpenAutomations={() => {}}
    />
  )
  assert.match(alertMarkup, /2 alerts/)
  assert.match(alertMarkup, /bg-\[var\(--app-warning-bg\)\]/)
  assert.match(alertMarkup, /4 jobs · 1 left/)
  assert.match(alertMarkup, /3 jobs done today/)
  assert.match(alertMarkup, /2 workers today/)

  const pendingMarkup = renderToStaticMarkup(
    <AutomationSidebarCompactCardView
      counts={{ running: 0, scheduled: 1, paused: 0, pending: 1, total: 2, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }}
      rootCount={2}
      onExpand={() => {}}
    />
  )
  assert.match(pendingMarkup, /2 workers today/)
  assert.match(pendingMarkup, /0 jobs/)
  assert.match(pendingMarkup, /0 jobs done today/)
})

test('sidebarVisibleGroupNodes limits visible automation cards and supports overflow expansion for 50+ automations', () => {
  const fakeNodes = Array.from({ length: 50 }, (_, i) => ({
    session: { id: `session-${i}` } as any,
    depth: 0,
    label: `auto-${i}`,
    assignmentLabel: null,
    kind: 'root' as const,
  }))

  assert.equal(SIDEBAR_AUTOMATION_VISIBLE_ROOT_LIMIT, 6)

  // When not expanded, only returns visible root limit
  const bounded = sidebarVisibleGroupNodes(fakeNodes as any, 'automation', false)
  assert.equal(bounded.length, 6)
  assert.equal(bounded[0].session.id, 'session-0')
  assert.equal(bounded[5].session.id, 'session-5')

  // When overflow expanded, returns all 50 items
  const expanded = sidebarVisibleGroupNodes(fakeNodes as any, 'automation', true)
  assert.equal(expanded.length, 50)
  assert.equal(expanded[49].session.id, 'session-49')
})

test('automation metadata multi-row card renders 3-row layout with distinct schedule and stats rows', () => {
  const markup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 3600 }}
      status="Scheduled"
      nextDueAt={1789400000000}
      runsToday={5}
      upcomingCount={3}
      totalJobs={24}
      workspaceSlug="proj-1"
    />
  )
  assert.match(markup, /Every hour/)
  assert.match(markup, /Scheduled/)
  assert.match(markup, /24 jobs · 5 done · 3 left/)
  assert.match(markup, /Next:/)
  // Verify clean separation into flex rows
  assert.match(markup, /flex-col gap-1/)
})

test('formatAutomationHeadline states workers today cleanly', () => {
  // 150 workers today
  assert.equal(formatAutomationHeadline({ running: 150, scheduled: 0, paused: 0, pending: 0, total: 150, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }, 150), '150 workers today')
  assert.equal(formatAutomationHeadline({ running: 1, scheduled: 0, paused: 0, pending: 0, total: 1, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }, 1), '1 worker today')
  assert.equal(formatAutomationHeadline({ running: 0, scheduled: 150, paused: 0, pending: 0, total: 150, runsToday: 150, upcoming: 0, totalJobs: 0, alerts: 0 }, 150), '150 workers today')
  assert.equal(formatAutomationHeadline({ running: 5, scheduled: 145, paused: 0, pending: 0, total: 150, runsToday: 150, upcoming: 0, totalJobs: 0, alerts: 0 }, 150), '150 workers today')
  assert.equal(formatAutomationHeadline({ running: 0, scheduled: 1, paused: 0, pending: 0, total: 1, runsToday: 1, upcoming: 0, totalJobs: 0, alerts: 0 }, 1), '1 worker today')

  // Fallback to total / rootCount as workers today
  assert.equal(formatAutomationHeadline({ running: 0, scheduled: 150, paused: 0, pending: 0, total: 150, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }, 150), '150 workers today')
  assert.equal(formatAutomationHeadline({ running: 0, scheduled: 0, paused: 0, pending: 0, total: 0, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }, 3), '3 workers today')

  // Empty fallback
  assert.equal(formatAutomationHeadline({ running: 0, scheduled: 0, paused: 0, pending: 0, total: 0, runsToday: 0, upcoming: 0, totalJobs: 0, alerts: 0 }, 0), 'Workers')
})

test('expanded container holds automation sessions with collapse controls and headline', () => {
  let collapsed = false
  let navigated = false

  const expandedMarkup = renderToStaticMarkup(
    <AutomationSidebarExpandedContainerView
      counts={{ running: 2, scheduled: 3, paused: 0, pending: 0, total: 5, runsToday: 5, upcoming: 1, alerts: 0 }}
      rootCount={5}
      onCollapse={() => { collapsed = true }}
      onOpenAutomations={() => { navigated = true }}
    >
      <div data-testid="test-session-child">Session 1</div>
      <div data-testid="test-session-child">Session 2</div>
    </AutomationSidebarExpandedContainerView>
  )

  // Verifies container exists and is strictly constrained to sidebar width without overflowing
  assert.match(expandedMarkup, /data-testid="automation-sidebar-expanded-container"/)
  assert.match(expandedMarkup, /w-full min-w-0 max-w-full box-border/)
  assert.match(expandedMarkup, /overflow-hidden/)
  // Verifies headline
  assert.match(expandedMarkup, /5 workers today/)
  assert.match(expandedMarkup, /6 jobs · 1 left/)
  assert.match(expandedMarkup, /data-testid="expanded-running-dot"/)
  // Verifies footer controls: All workers on left, Collapse on far right
  assert.match(expandedMarkup, /data-testid="expanded-view-button"/)
  assert.match(expandedMarkup, /All workers \(5\) →/)
  assert.match(expandedMarkup, /data-testid="expanded-collapse-button"/)
  assert.match(expandedMarkup, /Collapse/)
  // Verifies children rendered inside container
  assert.match(expandedMarkup, /Session 1/)
  assert.match(expandedMarkup, /Session 2/)

  // Direct element test: footer All workers and Collapse buttons
  const element = AutomationSidebarExpandedContainerView({
    counts: { running: 0, scheduled: 3, paused: 0, pending: 0, total: 3, runsToday: 3, upcoming: 0, alerts: 0 },
    rootCount: 3,
    onCollapse: () => { collapsed = true },
    onOpenAutomations: () => { navigated = true },
    children: <div>Child</div>,
  })
  assert.ok(element)
  assert.equal((element as any).type, 'div')

  // Verify top header has controls
  const header = (element as any).props.children[0]
  const headerControls = header.props.children[1]
  assert.equal(headerControls.props.children.length, 3)

  // Footer left: All workers button
  navigated = false
  const footer = (element as any).props.children[2]
  const allWorkersBtn = footer.props.children[0]
  assert.equal(allWorkersBtn.props['data-testid'], 'expanded-view-button')
  allWorkersBtn.props.onClick({ stopPropagation() {} })
  assert.equal(navigated, true)

  // Footer right: Collapse button
  collapsed = false
  const collapseBtn = footer.props.children[1]
  assert.equal(collapseBtn.props['data-testid'], 'expanded-collapse-button')
  collapseBtn.props.onClick({ stopPropagation() {} })
  assert.equal(collapsed, true)
})

test('worker scheduled every 5 minutes accurately calculates 288 jobs today, jobs done, jobs left, and 1 worker today', () => {
  const now = new Date('2026-09-16T00:00:00.000Z').getTime()
  const nextDue = now + 300000 // in 5 minutes

  const record = {
    automation_id: 'auto-5min',
    session_id: 's-5min',
    workspace_id: 'ws-1',
    enabled: true,
    cancelled: false,
    accepted_at: now,
    next_due_at: nextDue,
    authorization: { kind: 'indefinite' },
    document: {
      title: 'Periodic check',
      info: { goal: 'Check status' },
      checkpoints: [],
      automation_v2: {
        schema_version: 2,
        schedule: {
          kind: 'interval',
          interval_seconds: 300, // every 5 minutes
          timezone: 'UTC',
        },
        expiration: { kind: 'indefinite' },
        missed: 'skip',
        overlap: 'independent',
        activate_on_accept: true,
      },
    },
  } as unknown as AutomationV2Record

  const progressKey = automationV2PageKey({ action: 'progress', workspace_id: 'ws-1', session_id: 's-5min', timezone: 'UTC' })
  const fakeState: DesktopV3CacheState = {
    automationV2Pages: {
      [progressKey]: {
        input: { action: 'progress', workspace_id: 'ws-1', session_id: 's-5min', timezone: 'UTC' },
        generation: 1,
        loading: false,
        stale: false,
        data: {
          record,
          progress: {
            record,
            timezone: 'UTC',
            observed_at: now,
            forecast: [], // Even with empty forecast, schedule calculation gives accurate 288 jobs
            forecast_is_admission: false,
            complete: true,
            occurrences: [],
          },
        },
      } as any,
    },
    sessionsById: {},
    permissionsBySession: {},
    currentRunIntentBySession: {},
    runIntentsBySession: {},
  } as unknown as DesktopV3CacheState

  // 1. Verify selectAutomationSummaryCounts
  const counts = selectAutomationSummaryCounts(fakeState, 'ws-1', now)
  assert.equal(counts.total, 1, 'must be exactly 1 worker, not 5 or 288')
  assert.equal(counts.totalJobs, 288, 'must report 288 total jobs scheduled today for every 5 minute schedule')
  assert.equal(counts.upcoming, 288, 'must report 288 jobs left today when 0 ran')
  assert.equal(counts.runsToday, 0, 'must report 0 jobs done today')

  // 2. Verify compact card rendering
  const compactMarkup = renderToStaticMarkup(
    <AutomationSidebarCompactCardView
      counts={counts}
      rootCount={1}
      onExpand={() => {}}
    />
  )
  assert.match(compactMarkup, /1 worker today/, 'compact card headline must be 1 worker today')
  assert.match(compactMarkup, /288 jobs · 288 left/, 'compact card top right must show total jobs and jobs left')
  assert.match(compactMarkup, /0 jobs done today/, 'compact card bottom left must show jobs done today')

  // 3. Verify when 12 jobs have completed today
  const countsWithRuns = {
    ...counts,
    runsToday: 12,
    upcoming: 276,
    totalJobs: 288,
  }
  const compactMarkupWithRuns = renderToStaticMarkup(
    <AutomationSidebarCompactCardView
      counts={countsWithRuns}
      rootCount={1}
      onExpand={() => {}}
    />
  )
  assert.match(compactMarkupWithRuns, /1 worker today/)
  assert.match(compactMarkupWithRuns, /288 jobs · 276 left/)
  assert.match(compactMarkupWithRuns, /12 jobs done today/)

  // 4. Verify expanded container header
  const expandedMarkup = renderToStaticMarkup(
    <AutomationSidebarExpandedContainerView
      counts={countsWithRuns}
      rootCount={1}
      onCollapse={() => {}}
    >
      <div>Child Session</div>
    </AutomationSidebarExpandedContainerView>
  )
  assert.match(expandedMarkup, /1 worker today/, 'expanded container headline must be 1 worker today')
  assert.match(expandedMarkup, /288 jobs · 276 left/, 'expanded container top right must show total jobs and jobs left')

  // 5. Verify individual worker metadata row
  const rowMarkup = renderToStaticMarkup(
    <AutomationSidebarMetadataRow
      schedule={{ kind: 'interval', interval_seconds: 300, timezone: 'UTC' }}
      status="Scheduled"
      nextDueAt={nextDue}
      runsToday={12}
      upcomingCount={276}
      totalJobs={288}
    />
  )
  assert.match(rowMarkup, /Every 5 minutes · UTC/)
  assert.match(rowMarkup, /288 jobs · 12 done · 276 left/)
  assert.doesNotMatch(rowMarkup, /5 upcoming/, 'must not report hardcoded 5')
})
