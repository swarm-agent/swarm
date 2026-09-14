import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AutomationV2Sidecar, isGenericSessionTitle, formatAutomationSessionTitle } from './automation-v2-sidecar'
import {
  AutomationV2Workspace,
  AutomationUpcomingScheduleChart,
  AutomationCardPulse,
  computeUpcomingAutomationEvents,
  formatRelativeTime,
} from './automation-v2-workspace'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'

const sampleRecord: AutomationV2Record = {
  automation_id: 'auto-plan-1',
  proposal_id: 'prop-1',
  revision: 2,
  digest: 'b'.repeat(64),
  account_id: 'acct-1',
  workspace_id: 'ws-test',
  session_id: 'session-main-plan',
  generation: 1,
  enabled: true,
  cancelled: false,
  accepted_at: 1000,
  authorization: { kind: 'indefinite' },
  document: {
    title: 'Hourly Health Check',
    info: { goal: 'Check server health and report drift' },
    checkpoints: [
      {
        id: 'cp-1',
        title: 'Query metrics',
        tasks: ['Inspect error rate'],
        acceptance_criteria: ['Report status routine_clean or alert'],
      },
    ],
    automation_v2: {
      schema_version: 2,
      schedule: { kind: 'interval', interval_seconds: 3600 },
      missed: 'skip',
      overlap: 'serialize',
      activate_on_accept: true,
      expiration: { kind: 'indefinite' },
    },
  },
}

test('AutomationV2Sidecar renders plan-like sidebar ready to chat by default', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      selectedAutomation={sampleRecord}
    />
  )

  // Verify container and plan-sidecar data attributes
  assert.match(markup, /data-testid="automation-v2-sidecar-container"/)
  assert.match(markup, /data-testid="desktop-plan-agent-sidecar"/)
  assert.match(markup, /data-embedded="true"/)

  // Verify ready to chat by default: composer and scroller are mounted immediately
  assert.match(markup, /data-testid="desktop-plan-composer"/)
  assert.match(markup, /data-testid="desktop-plan-agent-scroller"/)
  assert.match(markup, /data-testid="desktop-plan-agent-tail-anchor"/)

  // Verify title reflects selected automation
  assert.match(markup, /Optimize: Hourly Health Check/)

  // Verify session switcher and "+ New" button in header
  assert.match(markup, /data-testid="automation-sidecar-session-switcher"/)
  assert.match(markup, /aria-label="Prior automation sessions"/)
  assert.match(markup, /Hourly Health Check/)
  assert.match(markup, />New<\/span>/)
})

test('AutomationV2Sidecar handles direct session selection cleanly', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      activeSessionId="custom-prior-session-1"
    />
  )

  assert.match(markup, /data-testid="automation-v2-sidecar-container"/)
  assert.match(markup, /data-testid="desktop-plan-composer"/)
  assert.match(markup, /data-testid="desktop-plan-agent-scroller"/)
  assert.match(markup, /Automations Assistant/)
})

test('AutomationV2Workspace integrates full-height sidebar and eliminates halfway-down inline sidecar', () => {
  const localTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-test' })
  const progressKey = automationV2PageKey({ action: 'progress', workspace_id: 'ws-test', session_id: sampleRecord.session_id, timezone: localTimezone })

  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-test' },
    requestId: 'req-list-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-1',
    generation: 0,
    data: { records: [sampleRecord] },
  })

  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: progressKey,
    input: { action: 'progress', workspace_id: 'ws-test', session_id: sampleRecord.session_id, timezone: localTimezone },
    requestId: 'req-prog-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: progressKey,
    requestId: 'req-prog-1',
    generation: 0,
    data: {
      progress: {
        record: sampleRecord,
        observed_at: 2000,
        timezone: localTimezone,
        complete: true,
        occurrences: [],
      },
    },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
      initialSessionId={sampleRecord.session_id}
    />
  )

  // Verify full-height layout with aside containing AutomationV2Sidecar
  assert.match(markup, /aria-label="Automations Assistant"/)
  assert.match(markup, /data-testid="automation-v2-sidecar-container"/)
  assert.match(markup, /data-testid="desktop-plan-composer"/)

  // Verify halfway-down inline sidecar (mobileInline) is absent inside the detail card
  assert.doesNotMatch(markup, /data-mobile-inline="true"/)
  assert.doesNotMatch(markup, /id="mobile-plan-agent-panel"/)

  // Verify Optimize with Swarm button exists in detail
  assert.match(markup, />Optimize with Swarm<\/button>/)
})

test('AutomationV2Sidecar displays running status badge and today/upcoming run counts', () => {
  const progressKey = automationV2PageKey({ action: 'progress', workspace_id: 'ws-test', session_id: sampleRecord.session_id, timezone: 'UTC' })
  const now = Date.now()
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: progressKey,
    input: { action: 'progress', workspace_id: 'ws-test', session_id: sampleRecord.session_id, timezone: 'UTC' },
    requestId: 'req-prog-sidecar-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: progressKey,
    requestId: 'req-prog-sidecar-1',
    generation: 0,
    data: {
      progress: {
        record: sampleRecord,
        observed_at: now,
        timezone: 'UTC',
        complete: true,
        forecast: [now + 3600000, now + 7200000],
        occurrences: [
          { id: 'occ-run-1', state: 'running', session_id: 'exec-1', due_at: now, accepted: sampleRecord },
          { id: 'occ-run-2', state: 'succeeded', session_id: 'exec-2', due_at: now - 60000, accepted: sampleRecord },
        ],
      },
    },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      selectedAutomation={sampleRecord}
    />
  )

  assert.match(markup, /data-testid="sidecar-running-badge"/)
  assert.match(markup, /Running/)
  assert.match(markup, /data-testid="sidecar-run-stats"/)
  assert.match(markup, /2 today/)
  assert.match(markup, /2 upcoming/)
})

const sampleRecord2: AutomationV2Record = {
  automation_id: 'auto-plan-2',
  proposal_id: 'prop-2',
  revision: 1,
  digest: 'c'.repeat(64),
  account_id: 'acct-1',
  workspace_id: 'ws-test',
  session_id: 'session-daily-digest',
  generation: 1,
  enabled: false,
  cancelled: false,
  accepted_at: 1500,
  authorization: { kind: 'indefinite' },
  document: {
    title: 'Daily Commit Digest',
    info: { goal: 'Summarize daily commits and PRs into markdown report' },
    checkpoints: [
      {
        id: 'cp-digest-1',
        title: 'Collect commits',
        tasks: ['Inspect git log'],
        acceptance_criteria: ['Deliver report'],
      },
    ],
    automation_v2: {
      schema_version: 2,
      schedule: { kind: 'cron', cron: '0 18 * * *', timezone: 'UTC' },
      missed: 'skip',
      overlap: 'serialize',
      activate_on_accept: true,
      expiration: { kind: 'indefinite' },
    },
  },
}

test('AutomationV2Workspace renders flat overview of all workspace automations and summary strip', () => {
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-test' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-test' },
    requestId: 'req-list-multi',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-multi',
    generation: 0,
    data: { records: [sampleRecord, sampleRecord2] },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
    />
  )

  // Verify flat overview container and both automation cards rendered
  assert.match(markup, /data-testid="automations-flat-overview"/)
  assert.match(markup, /Hourly Health Check/)
  assert.match(markup, /Daily Commit Digest/)
  assert.match(markup, />Enabled<\/span>/)
  assert.match(markup, />Paused<\/span>/)

  // Verify executive summary metrics strip
  assert.match(markup, /data-testid="automations-summary-strip"/)
  assert.match(markup, /Total[\s\S]*?>2<\/div>/)
  assert.match(markup, /Scheduled[\s\S]*?>1<\/div>/)
  assert.match(markup, /Paused[\s\S]*?>1<\/div>/)

  // Verify Discuss with Swarm action in header and on cards
  assert.match(markup, /title="Discuss all automations with the assistant"/)
  assert.match(markup, />Discuss with Swarm<\/span>/)

  // Verify Archive and Delete buttons on cards and Archived tab
  assert.match(markup, /title="Archive automation"/)
  assert.match(markup, /title="Permanently delete automation"/)
  assert.match(markup, />Archived \(0\)<\/button>/)

  // Verify Consider adding new automations section with starter templates
  assert.match(markup, /aria-label="Consider adding new automations"/)
  assert.match(markup, /Repository Health Check/)
  assert.match(markup, /Test &amp; Build Sentinel|Test & Build Sentinel/)
  assert.match(markup, />Propose with Swarm →<\/span>/)
})

test('AutomationV2Workspace renders Unarchive button and Archived status for archived automations', () => {
  const archivedRecord: AutomationV2Record = {
    ...sampleRecord,
    automation_id: 'auto-archived-1',
    session_id: 'session-archived-1',
    archived: true,
    archived_at: 1789000000000,
  }
  const archivedKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-test', archived_mode: 'only' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: archivedKey,
    input: { action: 'list', workspace_id: 'ws-test', archived_mode: 'only' },
    requestId: 'req-list-archived',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: archivedKey,
    requestId: 'req-list-archived',
    generation: 0,
    data: { records: [archivedRecord] },
  })

  // Verify archived tab button count is rendered
  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
    />
  )
  assert.match(markup, />Archived \(1\)<\/button>/)
})

test('AutomationV2Sidecar supports switching to workspace-level discussion of all automations', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      records={[sampleRecord, sampleRecord2]}
    />
  )

  // When no single automation is selected, title defaults to Automations Assistant
  assert.match(markup, /Automations Assistant/)
  // Session switcher includes All workspace automations and records list
  assert.match(markup, /All workspace automations/)
  assert.match(markup, /Hourly Health Check/)
  assert.match(markup, /Daily Commit Digest/)
  assert.match(markup, />New<\/span>/)
  assert.match(markup, /placeholder="Talk to your automations"/, 'sidecar composer placeholder should be Talk to your automations in workspace overview')
})

test('computeUpcomingAutomationEvents and formatRelativeTime compute future slots and countdowns', () => {
  const now = 1000000000
  const recordA: AutomationV2Record = {
    ...sampleRecord,
    next_due_at: now + 1800000, // in 30 minutes
    document: {
      ...sampleRecord.document,
      automation_v2: {
        schema_version: 2,
        schedule: { kind: 'interval', interval_seconds: 3600 },
        missed: 'skip',
        overlap: 'serialize',
        activate_on_accept: true,
        expiration: { kind: 'indefinite' },
      },
    },
  }

  const recordB: AutomationV2Record = {
    ...sampleRecord2,
    next_due_at: now + 7200000, // in 2 hours
    enabled: true,
  }

  const events = computeUpcomingAutomationEvents([recordA, recordB], now)
  assert.ok(events.length >= 2)
  // First event should be recordA (in 30 mins)
  assert.equal(events[0].title, 'Hourly Health Check')
  assert.equal(events[0].dueAt, now + 1800000)
  // Projected interval slot should also be present
  assert.ok(events.some((e) => e.dueAt === now + 1800000 + 3600000))

  // Relative time checks
  assert.equal(formatRelativeTime(now + 20000, now), 'due now')
  assert.equal(formatRelativeTime(now + 45000, now), 'in 45s')
  assert.equal(formatRelativeTime(now + 1800000, now), 'in 30m')
  assert.equal(formatRelativeTime(now + 7200000, now), 'in 2h')
  assert.equal(formatRelativeTime(now + 90000000, now), 'in 1d')
})

test('AutomationUpcomingScheduleChart renders 24-hour rhythm chart and upcoming event queue', () => {
  const now = Date.now()
  const recordWithDue: AutomationV2Record = {
    ...sampleRecord,
    next_due_at: now + 1200000, // in 20 mins
  }

  const markup = renderToStaticMarkup(
    <AutomationUpcomingScheduleChart
      records={[recordWithDue]}
      timezone="UTC"
      workspaceSlug="test-ws"
    />
  )

  // Verify container and header
  assert.match(markup, /data-testid="automations-schedule-continuity"/)
  assert.match(markup, /Upcoming Schedule &amp; Continuity/)
  assert.match(markup, /24-Hour Schedule Rhythm/)

  // Verify rhythm buckets
  assert.match(markup, /data-testid="schedule-bucket-morning"/)
  assert.match(markup, /data-testid="schedule-bucket-afternoon"/)
  assert.match(markup, /data-testid="schedule-bucket-evening"/)
  assert.match(markup, /data-testid="schedule-bucket-night"/)

  // Verify upcoming events queue
  assert.match(markup, /data-testid="upcoming-events-grid"/)
  assert.match(markup, /data-testid="upcoming-event-item"/)
  assert.match(markup, /Hourly Health Check/)
})

test('AutomationV2Workspace renders summary cards with pulse and open session links without inlining run feed', () => {
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-test' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-test' },
    requestId: 'req-list-overview',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-overview',
    generation: 0,
    data: { records: [{ ...sampleRecord, next_due_at: Date.now() + 3600000 }] },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
      workspaceSlug="test-slug"
    />
  )

  // Verify overview card rendered with summary pulse and open session link
  assert.match(markup, /data-testid="automation-overview-card"/)
  assert.match(markup, /data-testid="automation-card-pulse"/)
  assert.match(markup, /data-testid="open-automation-session-link"/)
  assert.match(markup, />Open session<\/span>/)

  // Verify Upcoming Schedule & Continuity section is rendered
  assert.match(markup, /data-testid="automations-schedule-continuity"/)

  // Crucially: verify that granular run feed is NOT inlined by default when opening the page
  assert.doesNotMatch(markup, /data-testid="automation-run-feed"/)
  assert.doesNotMatch(markup, /Run history &amp; upcoming times/)
})

test('isGenericSessionTitle identifies generic and custom session titles', () => {
  assert.equal(isGenericSessionTitle(undefined), true)
  assert.equal(isGenericSessionTitle(''), true)
  assert.equal(isGenericSessionTitle('New Session'), true)
  assert.equal(isGenericSessionTitle('new session'), true)
  assert.equal(isGenericSessionTitle('Automation conversation'), true)
  assert.equal(isGenericSessionTitle('automation session'), true)
  assert.equal(isGenericSessionTitle('New chat'), true)
  assert.equal(isGenericSessionTitle('Hourly Postgres Backup'), false)
  assert.equal(isGenericSessionTitle('Lint and Test Monitor'), false)
})

test('formatAutomationSessionTitle formats real titles and provides clean fallbacks for generic ones', () => {
  const customSession = {
    id: 's-1',
    workspace_path: '/work',
    workspace_name: 'work',
    title: 'Hourly Postgres Backup',
    mode: 'auto',
    created_at: 1700000000000,
    updated_at: 1700000000000,
    message_count: 5,
    last_message_at: 1700000000000,
  }
  assert.equal(formatAutomationSessionTitle(customSession), 'Hourly Postgres Backup')

  const genericWithMessages = {
    id: 's-2',
    workspace_path: '/work',
    workspace_name: 'work',
    title: 'Automation conversation',
    mode: 'auto',
    created_at: 1700000000000,
    updated_at: 1700000000000,
    message_count: 3,
    last_message_at: 1700000000000,
  }
  const formatted = formatAutomationSessionTitle(genericWithMessages)
  assert.match(formatted, /^Chat · /)
  assert.doesNotMatch(formatted, /Automation conversation/)

  const emptySession = {
    id: 's-3',
    workspace_path: '/work',
    workspace_name: 'work',
    title: 'New Session',
    mode: 'auto',
    created_at: 1700000000000,
    updated_at: 1700000000000,
    message_count: 0,
    last_message_at: 0,
  }
  assert.equal(formatAutomationSessionTitle(emptySession), 'New chat')
})

test('AutomationV2Sidecar renders recent automation chats with real titles and New chat option', () => {
  const sessionRealTitle = {
    id: 'session-real-1',
    workspace_path: '/path/to/work',
    workspace_name: 'work',
    title: 'Hourly Postgres Backup',
    mode: 'auto',
    created_at: 1700000000000,
    updated_at: 1700000000000,
    message_count: 4,
    last_message_at: 1700000000000,
  }
  dispatchDesktopV3Cache({
    type: 'sync.applied',
    snapshot: {
      sessions_by_id: {
        'session-real-1': sessionRealTitle,
      },
      projections_by_session: {},
      messages_by_session: {},
      run_intents_by_session: {},
    },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      records={[sampleRecord]}
    />
  )

  // Verify dropdown includes New chat option
  assert.match(markup, /✨ New chat/)
  // Verify dropdown includes All workspace automations
  assert.match(markup, /All workspace automations/)
  // Verify composer is mounted and ready to chat
  assert.match(markup, /data-testid="desktop-plan-composer"/)
  assert.match(markup, /placeholder="Talk to your automations"/)
})

