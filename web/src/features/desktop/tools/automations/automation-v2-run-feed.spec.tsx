import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Occurrence, AutomationV2Record } from '../../state/desktop-automation-v2-api'
import {
  AutomationV2Detail,
  AutomationV2RunFeed,
  CalmRunCard,
  DeliverableRunCard,
  AwaitingDocumentRunCard,
  StandardRunCard,
  OpenExecutionSessionButton,
  DeliverablePreviewLink,
  extractOccurrenceDeliverables,
  formatCalmStatus,
  groupOccurrencesByDay,
  isOccurrenceRoutineClean,
  isOccurrenceDeliverableReady,
  isOccurrenceAwaitingDocument,
} from './automation-v2-workspace'

const baseRecord: AutomationV2Record = {
  automation_id: 'auto-test',
  proposal_id: 'prop-1',
  revision: 1,
  digest: 'a'.repeat(64),
  account_id: 'acct-1',
  workspace_id: 'ws-1',
  session_id: 'author-session',
  generation: 1,
  enabled: true,
  cancelled: false,
  accepted_at: 1000,
  authorization: { kind: 'indefinite' },
  document: {
    title: 'Daily Cleanup Automation',
    info: { goal: 'Clean up temporary files and report status' },
    checkpoints: [
      {
        id: 'cp-1',
        title: 'Run cleanup task',
        tasks: ['Remove old files'],
        acceptance_criteria: ['Temporary files removed'],
      },
    ],
    automation_v2: {
      schema_version: 2,
      schedule: { kind: 'interval', interval_seconds: 86400 },
      expiration: { kind: 'indefinite' },
      missed: 'skip',
      overlap: 'serialize',
      activate_on_accept: true,
    },
  },
}

test('occurrence classification predicates identify routine clean, deliverable, and awaiting document states', () => {
  const routineOccurrence: AutomationV2Occurrence = {
    id: 'occ-1',
    state: 'succeeded',
    detail: 'Cleaned up sessions, all good',
    due_at: 5000,
    session_id: 'exec-1',
    accepted: baseRecord,
  }
  assert.equal(isOccurrenceRoutineClean(routineOccurrence), true)
  assert.equal(isOccurrenceDeliverableReady(routineOccurrence), false)
  assert.equal(isOccurrenceAwaitingDocument(routineOccurrence), false)
  assert.equal(formatCalmStatus(routineOccurrence), 'Cleaned up sessions, all good')

  const deliverableOccurrence: AutomationV2Occurrence = {
    id: 'occ-2',
    state: 'succeeded',
    closing_state: 'deliverable_ready',
    detail: 'Generated monthly summary report',
    due_at: 6000,
    session_id: 'exec-2',
    accepted: baseRecord,
    deliverables: [
      {
        label: 'Monthly Audit Report.md',
        path: 'reports/audit.md',
        media_type: 'text/markdown',
      },
    ],
  }
  assert.equal(isOccurrenceRoutineClean(deliverableOccurrence), false)
  assert.equal(isOccurrenceDeliverableReady(deliverableOccurrence), true)
  assert.equal(isOccurrenceAwaitingDocument(deliverableOccurrence), false)
  const deliverables = extractOccurrenceDeliverables(deliverableOccurrence)
  assert.equal(deliverables.length, 1)
  assert.equal(deliverables[0].label, 'Monthly Audit Report.md')

  const awaitingOccurrence: AutomationV2Occurrence = {
    id: 'occ-3',
    state: 'awaiting_document',
    detail: 'Plan awaits review before execution proceeds',
    due_at: 7000,
    session_id: 'exec-3',
    accepted: baseRecord,
  }
  assert.equal(isOccurrenceRoutineClean(awaitingOccurrence), false)
  assert.equal(isOccurrenceAwaitingDocument(awaitingOccurrence), true)

  const reviewRequiredOccurrence: AutomationV2Occurrence = {
    id: 'occ-4',
    state: 'review_required',
    detail: 'Checkpoint awaits resolution or review',
    due_at: 8000,
    session_id: 'exec-4',
    accepted: baseRecord,
  }
  assert.equal(isOccurrenceAwaitingDocument(reviewRequiredOccurrence), true)
})

test('top-down run feed sorts occurrences newest first (by due_at descending)', () => {
  const occOld: AutomationV2Occurrence = {
    id: 'occ-old',
    state: 'succeeded',
    detail: 'Older run',
    due_at: 1000,
    session_id: 'exec-old',
    accepted: baseRecord,
  }
  const occMid: AutomationV2Occurrence = {
    id: 'occ-mid',
    state: 'succeeded',
    detail: 'Mid run',
    due_at: 5000,
    session_id: 'exec-mid',
    accepted: baseRecord,
  }
  const occNew: AutomationV2Occurrence = {
    id: 'occ-new',
    state: 'succeeded',
    detail: 'Newest run',
    due_at: 10000,
    session_id: 'exec-new',
    accepted: baseRecord,
  }

  // Pass in mixed order
  const markup = renderToStaticMarkup(
    <AutomationV2RunFeed
      occurrences={[occMid, occOld, occNew]}
      timezone="UTC"
      workspaceSlug="proj-slug"
    />
  )

  // Verify top-down ordering in markup
  const newIndex = markup.indexOf('data-run-id="occ-new"')
  const midIndex = markup.indexOf('data-run-id="occ-mid"')
  const oldIndex = markup.indexOf('data-run-id="occ-old"')

  assert.ok(newIndex !== -1 && midIndex !== -1 && oldIndex !== -1)
  assert.ok(newIndex < midIndex, 'Newest run must appear before mid run')
  assert.ok(midIndex < oldIndex, 'Mid run must appear before oldest run')
})

test('routine successful runs show concise one-line calm status without overwhelming text', () => {
  const occurrence: AutomationV2Occurrence = {
    id: 'occ-calm',
    state: 'succeeded',
    detail: 'Cleaned up sessions, all good',
    due_at: 1700000000000,
    session_id: 'exec-calm-1',
    accepted: baseRecord,
  }

  const markup = renderToStaticMarkup(
    <CalmRunCard
      occurrence={occurrence}
      timeStr="Nov 14, 2023, 10:13 PM"
      workspaceSlug="my-workspace"
    />
  )

  // Acceptance Criterion 1: Concise one-line calm status
  assert.match(markup, /data-testid="calm-run-card"/)
  assert.match(markup, /data-testid="calm-run-status"/)
  assert.match(markup, /Cleaned up sessions, all good/)
  assert.match(markup, /Nov 14, 2023, 10:13 PM/)
  // Must provide Open execution session action
  assert.match(markup, /Open execution session/)
  assert.match(markup, /href="\/my-workspace\/exec-calm-1"/)
  // Detail should not be duplicated in full paragraphs by default
  assert.doesNotMatch(markup, /<p>Cleaned up sessions, all good<\/p>/)
})

test('deliverable runs highlight reports and documents directly on the card with preview links', () => {
  const occurrence: AutomationV2Occurrence = {
    id: 'occ-deliv',
    state: 'succeeded',
    closing_state: 'deliverable_ready',
    detail: 'Security analysis completed successfully',
    due_at: 1700000000000,
    session_id: 'exec-deliv-1',
    accepted: baseRecord,
    deliverables: [
      {
        label: 'Vulnerability Summary Report.md',
        path: 'docs/vulnerabilities.md',
        media_type: 'text/markdown',
        artifact_id: 'art-report-1',
      },
      {
        label: 'System Topology Map.html',
        path: 'artifacts/topology.html',
        media_type: 'text/html',
        artifact_id: 'art-topology-1',
      },
    ],
  }

  const markup = renderToStaticMarkup(
    <DeliverableRunCard
      occurrence={occurrence}
      deliverables={occurrence.deliverables!}
      timeStr="Nov 14, 2023, 10:13 PM"
      workspaceSlug="security-ops"
    />
  )

  // Acceptance Criterion 2: Deliverables highlighted directly on run card with preview/open links
  assert.match(markup, /data-testid="deliverable-run-card"/)
  assert.match(markup, /Deliverable ready/)
  assert.match(markup, /Security analysis completed successfully/)
  assert.match(markup, /Vulnerability Summary Report\.md/)
  assert.match(markup, /System Topology Map\.html/)
  assert.match(markup, /text\/markdown/)
  assert.match(markup, /text\/html/)
  assert.match(markup, /data-testid="run-deliverable-link"/)
  assert.match(markup, /Preview/)
  assert.match(markup, /href="\/security-ops\/exec-deliv-1\?artifact=art-report-1"/)
  // Acceptance Criterion 3: Open execution session action
  assert.match(markup, /Open execution session/)
})

test('pending awaiting document states surface prominently with action prompts', () => {
  const occurrence: AutomationV2Occurrence = {
    id: 'occ-await',
    state: 'awaiting_document',
    detail: 'Generated database migration document awaiting explicit user review',
    due_at: 1700000000000,
    session_id: 'exec-await-1',
    accepted: baseRecord,
  }

  const markup = renderToStaticMarkup(
    <AwaitingDocumentRunCard
      occurrence={occurrence}
      timeStr="Nov 14, 2023, 10:13 PM"
      workspaceSlug="database-ops"
    />
  )

  assert.match(markup, /data-testid="awaiting-document-run-card"/)
  assert.match(markup, /Awaiting document review/)
  assert.match(markup, /Generated database migration document awaiting explicit user review/)
  assert.match(markup, /Action needed/)
  assert.match(markup, /Open execution session/)
  assert.match(markup, /href="\/database-ops\/exec-await-1"/)
})

test('every run card provides an Open execution session action for on-demand drill-down', () => {
  let drilledSessionId = ''
  const onOpen = (id: string) => {
    drilledSessionId = id
  }

  // 1. With workspaceSlug -> renders accessible anchor link with exact href and click handler
  const linkMarkup = renderToStaticMarkup(
    <OpenExecutionSessionButton
      sessionId="exec-audit-123"
      workspaceSlug="audit-ws"
      onOpenSession={onOpen}
    />
  )
  assert.match(linkMarkup, /data-testid="open-execution-session-link"/)
  assert.match(linkMarkup, /Open execution session/)
  assert.match(linkMarkup, /aria-label="Open execution session"/)
  assert.match(linkMarkup, /title="Inspect raw messages, tool calls, and changes on demand"/)
  assert.match(linkMarkup, /href="\/audit-ws\/exec-audit-123"/)

  // 2. Without workspaceSlug -> renders button with onOpenSession handler
  const buttonElement = OpenExecutionSessionButton({
    sessionId: 'exec-audit-456',
    onOpenSession: onOpen,
  })
  assert.ok(buttonElement)
  assert.equal((buttonElement as any).type, 'button')
  ;(buttonElement as any).props.onClick({ preventDefault() {}, stopPropagation() {} })
  assert.equal(drilledSessionId, 'exec-audit-456')

  // 3. Fallback to onChat handler if onOpenSession is not passed
  let chatSessionId = ''
  const chatElement = OpenExecutionSessionButton({
    sessionId: 'exec-audit-789',
    onChat: (id) => { chatSessionId = id },
  })
  ;(chatElement as any).props.onClick({ preventDefault() {}, stopPropagation() {} })
  assert.equal(chatSessionId, 'exec-audit-789')
})

test('deliverable preview link triggers navigation or preview callback', () => {
  let openedSession = ''
  const previewElement = DeliverablePreviewLink({
    deliverable: {
      label: 'Summary.md',
      path: 'summary.md',
      session_id: 'exec-preview-1',
    },
    sessionId: 'fallback-session',
    onOpenSession: (id) => { openedSession = id },
  })
  assert.ok(previewElement)
  ;(previewElement as any).props.onClick({ preventDefault() {}, stopPropagation() {} })
  assert.equal(openedSession, 'exec-preview-1')
})

test('standard run card renders running active states and admitted slots', () => {
  const runningOcc: AutomationV2Occurrence = {
    id: 'occ-run',
    state: 'running',
    detail: 'Executing checkpoint 2 of 3',
    due_at: 1700000000000,
    session_id: 'exec-running-1',
    accepted: baseRecord,
  }
  const runningMarkup = renderToStaticMarkup(
    <StandardRunCard
      occurrence={runningOcc}
      timeStr="Now"
      workspaceSlug="my-ws"
    />
  )
  assert.match(runningMarkup, /data-testid="standard-run-card"/)
  assert.match(runningMarkup, /data-testid="run-running-dot"/)
  assert.match(runningMarkup, /animate-pulse/)
  assert.match(runningMarkup, /Executing checkpoint 2 of 3/)
  assert.match(runningMarkup, /Active execution in progress/)
  assert.match(runningMarkup, /Open execution session/)

  const admittedOcc: AutomationV2Occurrence = {
    id: 'occ-admitted',
    state: 'admitted',
    detail: 'Waiting for dispatch',
    due_at: 1700000000000,
    session_id: 'exec-admitted-1',
    accepted: baseRecord,
  }
  const admittedMarkup = renderToStaticMarkup(
    <StandardRunCard
      occurrence={admittedOcc}
      timeStr="Scheduled"
      workspaceSlug="my-ws"
    />
  )
  assert.match(admittedMarkup, /Waiting for dispatch/)
  assert.match(admittedMarkup, /Admitted means queued for dispatch/)
  assert.match(admittedMarkup, /Open execution session/)
})

test('AutomationV2Detail renders top-down run feed with calm runs, deliverables, and session drill-down', () => {
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const input = {
    action: 'progress' as const,
    workspace_id: 'ws-1',
    session_id: 'author-session',
    timezone,
  }
  const key = automationV2PageKey(input)
  const occurrences: AutomationV2Occurrence[] = [
    {
      id: 'occ-deliv',
      state: 'succeeded',
      closing_state: 'deliverable_ready',
      detail: 'Generated audit deliverable',
      due_at: 20000,
      session_id: 'exec-deliv',
      accepted: baseRecord,
      deliverables: [{ label: 'Audit.md', media_type: 'text/markdown' }],
    },
    {
      id: 'occ-calm',
      state: 'succeeded',
      detail: 'Cleaned up sessions, all good',
      due_at: 10000,
      session_id: 'exec-calm',
      accepted: baseRecord,
    },
    {
      id: 'occ-await',
      state: 'awaiting_document',
      detail: 'Review required',
      due_at: 30000,
      session_id: 'exec-await',
      accepted: baseRecord,
    },
  ]

  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key,
    input,
    requestId: 'req-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key,
    requestId: 'req-1',
    generation: 0,
    data: {
      record: baseRecord,
      progress: {
        record: baseRecord,
        observed_at: 40000,
        timezone,
        forecast: [50000, 60000],
        forecast_is_admission: false,
        complete: true,
        occurrences,
      },
    },
  })

  let openedSession = ''
  const markup = renderToStaticMarkup(
    <AutomationV2Detail
      workspaceId="ws-1"
      sessionId="author-session"
      workspaceSlug="my-workspace"
      onOpenSession={(id) => { openedSession = id }}
    />
  )

  // Verify top-level structure
  assert.match(markup, /aria-label="Worker details"/)
  assert.match(markup, /Daily Cleanup Automation/)
  assert.match(markup, /Run history (&amp;|&) upcoming times/)
  assert.match(markup, /data-testid="automation-run-feed"/)

  // Verify top-down order: occ-await (30000) then occ-deliv (20000) then occ-calm (10000)
  const awaitPos = markup.indexOf('data-run-id="occ-await"')
  const delivPos = markup.indexOf('data-run-id="occ-deliv"')
  const calmPos = markup.indexOf('data-run-id="occ-calm"')
  assert.ok(awaitPos !== -1 && delivPos !== -1 && calmPos !== -1)
  assert.ok(awaitPos < delivPos, 'Newest run (awaiting) must be first')
  assert.ok(delivPos < calmPos, 'Mid run (deliverable) must be second')

  // Verify calm run
  assert.match(markup, /data-testid="calm-run-status"/)
  assert.match(markup, /Cleaned up sessions, all good/)

  // Verify deliverable run
  assert.match(markup, /Deliverable ready/)
  assert.match(markup, /Audit\.md/)
  assert.match(markup, /data-testid="run-deliverable-link"/)

  // Verify awaiting document run
  assert.match(markup, /Awaiting document review/)

  // Verify drill-down on each
  assert.match(markup, /href="\/my-workspace\/exec-await"/)
  assert.match(markup, /href="\/my-workspace\/exec-deliv"/)
  assert.match(markup, /href="\/my-workspace\/exec-calm"/)
})

test('attention_alert closing state triggers alert badge and blocked closing state triggers action needed', () => {
  const alertOccurrence: AutomationV2Occurrence = {
    id: 'occ-alert',
    state: 'succeeded',
    closing_state: 'attention_alert',
    detail: 'Alert · Disk usage exceeded 92%',
    due_at: 1700000000000,
    session_id: 'exec-alert-1',
    accepted: baseRecord,
  }

  const alertMarkup = renderToStaticMarkup(
    <StandardRunCard
      occurrence={alertOccurrence}
      timeStr="Nov 14, 2023, 10:13 PM"
      workspaceSlug="my-workspace"
    />
  )
  assert.match(alertMarkup, /data-testid="standard-run-card"/)
  assert.match(alertMarkup, /Alert · succeeded/)
  assert.match(alertMarkup, /Alert · Disk usage exceeded 92%/)

  const blockedOccurrence: AutomationV2Occurrence = {
    id: 'occ-blocked',
    state: 'unavailable',
    closing_state: 'blocked',
    detail: 'Blocked · AWS credentials expired',
    due_at: 1700000000000,
    session_id: 'exec-blocked-1',
    accepted: baseRecord,
  }

  const blockedMarkup = renderToStaticMarkup(
    <AwaitingDocumentRunCard
      occurrence={blockedOccurrence}
      timeStr="Nov 14, 2023, 10:13 PM"
      workspaceSlug="my-workspace"
    />
  )
  assert.match(blockedMarkup, /data-testid="awaiting-document-run-card"/)
  assert.match(blockedMarkup, /Blocked · Action needed/)
  assert.match(blockedMarkup, /Blocked · AWS credentials expired/)
  assert.match(blockedMarkup, /Action needed/)
})

test('run feed groups occurrences by day and renders daily summary headers with metrics', () => {
  const now = Date.now()
  const todayOccurrence1: AutomationV2Occurrence = {
    id: 'occ-today-1',
    state: 'succeeded',
    closing_state: 'routine_clean',
    detail: 'Routine clean run 1',
    due_at: now - 3600000,
    session_id: 'exec-t1',
    accepted: baseRecord,
  }
  const todayOccurrence2: AutomationV2Occurrence = {
    id: 'occ-today-2',
    state: 'succeeded',
    closing_state: 'deliverable_ready',
    detail: 'Generated daily report',
    due_at: now - 1800000,
    session_id: 'exec-t2',
    accepted: baseRecord,
    deliverables: [{ label: 'Daily Digest.md', path: 'docs/digest.md' }],
  }
  const yesterdayOccurrence: AutomationV2Occurrence = {
    id: 'occ-yesterday',
    state: 'succeeded',
    closing_state: 'attention_alert',
    detail: 'High CPU warning',
    due_at: now - 86400000 - 3600000,
    session_id: 'exec-y1',
    accepted: baseRecord,
  }

  const groups = groupOccurrencesByDay([todayOccurrence2, todayOccurrence1, yesterdayOccurrence], 'UTC')
  assert.equal(groups.length, 2)
  assert.equal(groups[0].label, 'Today')
  assert.equal(groups[0].stats.total, 2)
  assert.equal(groups[0].stats.clean, 1)
  assert.equal(groups[0].stats.deliverables, 1)

  const markup = renderToStaticMarkup(
    <AutomationV2RunFeed
      occurrences={[todayOccurrence2, todayOccurrence1, yesterdayOccurrence]}
      timezone="UTC"
      workspaceSlug="team-ops"
    />
  )

  assert.match(markup, /data-testid="run-feed-day-group"/)
  assert.match(markup, /data-testid="run-feed-day-header"/)
  assert.match(markup, /Today/)
  assert.match(markup, /2 runs/)
  assert.match(markup, /1 clean/)
  assert.match(markup, /1 deliverable/)
  assert.match(markup, /Yesterday/)
  assert.match(markup, /1 alert/)
})

test('AutomationV2Detail renders Today executive pulse strip with clean metric pills', () => {
  const now = Date.now()
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const todayProgressOccurrence: AutomationV2Occurrence = {
    id: 'occ-today-exec',
    state: 'succeeded',
    closing_state: 'routine_clean',
    detail: 'Cleaned up sessions, all good',
    due_at: now,
    session_id: 'exec-today-1',
    accepted: baseRecord,
  }

  const input = { action: 'progress' as const, workspace_id: 'ws-1', session_id: 'author-session', timezone }
  const key = automationV2PageKey(input)
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key,
    input,
    requestId: 'req-today-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key,
    requestId: 'req-today-1',
    generation: 0,
    data: {
      action: 'progress',
      workspace_id: 'ws-1',
      progress: {
        record: baseRecord,
        timezone,
        observed_at: now,
        occurrences: [todayProgressOccurrence],
        complete: true,
      },
    },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Detail
      workspaceId="ws-1"
      sessionId="author-session"
      workspaceSlug="team-workspace"
    />
  )

  assert.match(markup, /data-testid="today-executive-strip"/)
  assert.match(markup, /Today(&#x27;|')s Pulse/)
  assert.match(markup, /1 clean/)
  assert.match(markup, /bg-\[var\(--app-success-bg,/)
  assert.match(markup, /border-\[var\(--app-success-border,/)
  assert.doesNotMatch(markup, /bg-\[var\(--app-success,/)
  assert.match(markup, /0 alerts/)
})

