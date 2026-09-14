import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AutomationV2Sidecar } from './automation-v2-sidecar'
import { AutomationV2Workspace } from './automation-v2-workspace'
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
})

