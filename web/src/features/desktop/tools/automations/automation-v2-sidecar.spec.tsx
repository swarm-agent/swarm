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
      workspacePath="/home/roy/work"
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
      workspacePath="/home/roy/work"
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
      workspacePath="/home/roy/work"
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
