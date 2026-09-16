import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AutomationV2WorkerDetailPage, AutomationV2Workspace } from './automation-v2-workspace'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import { type AutomationV2Record, type AutomationV2Occurrence } from '../../state/desktop-automation-v2-api'

const sampleWorkerRecord: AutomationV2Record = {
  automation_id: 'av2_9876543210abcdef',
  session_id: 'worker-author-session-123',
  workspace_id: 'ws-test',
  account_id: 'account-1',
  user_id: 'user-1',
  created_at: 100000,
  accepted_at: 100500,
  accepted_by: 'user-1',
  enabled: true,
  generation: 1,
  revision: 1,
  digest: 'digest-worker-1',
  proposal_id: 'proposal-worker-1',
  authorization: { kind: 'indefinite' },
  document: {
    title: 'Daily Security and Drift Worker',
    info: { goal: 'Verify repo integrity, security posture, and report alerts' },
    checkpoints: [
      {
        id: 'cp-1',
        title: 'Check dependency vulnerabilities',
        status: 'completed',
        objective: 'Run dependency vulnerability scan',
        order: 1,
        tasks: ['Audit npm packages', 'Audit Go packages'],
        acceptance_criteria: ['No high severity CVEs'],
      },
      {
        id: 'cp-2',
        title: 'Check configuration drift',
        status: 'pending',
        objective: 'Detect drift against main branch',
        order: 2,
        tasks: ['Check git status', 'Check environment variables'],
        acceptance_criteria: ['Working tree is clean'],
      },
    ],
    automation_v2: {
      schema_version: 2,
      schedule: {
        kind: 'interval',
        interval_seconds: 3600,
        timezone: 'UTC',
      },
      missed: 'skip',
      overlap: 'serialize',
      activate_on_accept: true,
      expiration: { kind: 'indefinite' },
    },
  },
}

const sampleOccurrences: AutomationV2Occurrence[] = [
  {
    id: 'occ-1',
    session_id: 'occ-session-1',
    run_id: 'run-1',
    due_at: Date.now() - 3600000,
    admitted_at: Date.now() - 3590000,
    state: 'completed',
    closing_state: 'routine_clean',
    summary: 'Clean security audit completed without findings',
    report: 'Audited 42 packages cleanly',
    version: 1,
    observed_at: Date.now() - 3500000,
    accepted: sampleWorkerRecord,
  },
  {
    id: 'occ-2',
    session_id: 'occ-session-2',
    run_id: 'run-2',
    due_at: Date.now() - 1800000,
    admitted_at: Date.now() - 1790000,
    state: 'completed',
    closing_state: 'deliverable_ready',
    summary: 'Generated daily security report document',
    deliverables: [
      {
        label: 'security-audit-report.md',
        path: 'reports/security-audit-report.md',
        media_type: 'text/markdown',
      },
    ],
    version: 1,
    observed_at: Date.now() - 1700000,
    accepted: sampleWorkerRecord,
  },
  {
    id: 'occ-3',
    session_id: 'occ-session-3',
    run_id: 'run-3',
    due_at: Date.now() - 600000,
    admitted_at: Date.now() - 590000,
    state: 'failed',
    closing_state: 'attention_alert',
    summary: 'Config drift detected in production branch',
    detail: 'Uncommitted file changes detected in deploy lane',
    version: 1,
    observed_at: Date.now() - 500000,
    accepted: sampleWorkerRecord,
  },
]

test('AutomationV2WorkerDetailPage renders dedicated worker page with worker ID, plan checkpoints, today pulse, and runs', () => {
  const progressKey = automationV2PageKey({
    action: 'progress',
    workspace_id: 'ws-test',
    session_id: sampleWorkerRecord.session_id,
    timezone: 'UTC',
  })

  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: progressKey,
    input: { action: 'progress', workspace_id: 'ws-test', session_id: sampleWorkerRecord.session_id, timezone: 'UTC' },
    requestId: 'req-worker-page',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: progressKey,
    requestId: 'req-worker-page',
    generation: 0,
    data: {
      progress: {
        record: sampleWorkerRecord,
        observed_at: Date.now(),
        timezone: 'UTC',
        forecast: [Date.now() + 3600000],
        occurrences: sampleOccurrences,
        complete: true,
      },
    },
  })

  let openedSessionId = ''
  let backed = false

  const markup = renderToStaticMarkup(
    <AutomationV2WorkerDetailPage
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceSlug="my-workspace"
      record={sampleWorkerRecord}
      onBack={() => { backed = true }}
      onOpenSession={(id) => { openedSessionId = id }}
      onControlRecord={async () => {}}
      onArchiveRecord={async () => {}}
      onDeleteRecord={() => {}}
      actionLoadingId={null}
    />
  )

  // 1. Worker ID & Breadcrumbs
  assert.match(markup, /data-testid="worker-id-page"/)
  assert.match(markup, /data-testid="selected-worker-banner"/)
  assert.match(markup, /ID: av2_9876543210abcdef/)
  assert.match(markup, /Daily Security and Drift Worker/)
  assert.match(markup, /Scheduled/)

  // 2. Active Plan & Instructions
  assert.match(markup, /Active Plan &amp; Instructions/)
  assert.match(markup, /Verify repo integrity, security posture, and report alerts/)
  assert.match(markup, /Check dependency vulnerabilities/)
  assert.match(markup, /Audit npm packages/)
  assert.match(markup, /No high severity CVEs/)
  assert.match(markup, /Check configuration drift/)

  // 3. Today's Pulse & Overall Summary
  assert.match(markup, /Today&#x27;s Pulse/)
  assert.match(markup, /Clean Runs/)
  assert.match(markup, /Deliverables Ready/)
  assert.match(markup, /Attention Alerts/)

  // 4. Execution Sessions housed under worker
  assert.match(markup, /Execution Sessions/)
  assert.match(markup, /Clean security audit completed without findings/)
  assert.match(markup, /Generated daily security report document/)
  assert.match(markup, /Config drift detected in production branch/)
  assert.match(markup, /security-audit-report\.md/)
  assert.match(markup, /data-testid="open-execution-session-link"/)
  assert.match(markup, /Open execution session/)
  assert.match(markup, /href="\/my-workspace\/occ-session-1"/)
  assert.match(markup, /href="\/my-workspace\/occ-session-2"/)
  assert.match(markup, /href="\/my-workspace\/occ-session-3"/)

  // 5. Controls
  assert.match(markup, /Pause schedule/)
  assert.match(markup, /Edit worker/)
  assert.match(markup, />Optimize with Swarm<\/button>/)
  assert.match(markup, /data-testid="worker-detail-back-button"/)
})

test('AutomationV2Workspace switches to AutomationV2WorkerDetailPage when initialSessionId matches a worker ID', () => {
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-test' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-test' },
    requestId: 'req-ws-switch',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-ws-switch',
    generation: 0,
    data: { records: [sampleWorkerRecord] },
  })

  // Render with worker ID (av2_...)
  const markupWithWorkerId = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="My Workspace"
      workspaceSlug="my-workspace"
      initialSessionId="av2_9876543210abcdef"
    />
  )

  assert.match(markupWithWorkerId, /data-testid="worker-id-page"/)
  assert.match(markupWithWorkerId, /ID: av2_9876543210abcdef/)
  assert.match(markupWithWorkerId, /Active Plan &amp; Instructions/)

  // Render with authoring session ID
  const markupWithSessionId = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-test"
      workspacePath="/path/to/work"
      workspaceName="My Workspace"
      workspaceSlug="my-workspace"
      initialSessionId="worker-author-session-123"
    />
  )

  assert.match(markupWithSessionId, /data-testid="worker-id-page"/)
  assert.match(markupWithSessionId, /ID: av2_9876543210abcdef/)
})
