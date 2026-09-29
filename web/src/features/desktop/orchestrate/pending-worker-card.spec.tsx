import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { WorkerAutomation, WorkerRecord } from '../state/desktop-workers-api'
import { PendingWorkerCard } from './pending-worker-card'

// Requirement: Orchestrator proposed durable workers appear as reviewable Pending cards.
// Threat: Acceptance happens on stale revisions, execution controls leak before approval,
// or proposed workspaces / job intents are obscured from human review.

const noJobWorker: WorkerRecord = {
  id: 'worker-no-job-1',
  account_scope_id: 'account-alpha',
  name: 'Code Reviewer',
  description: 'Autonomous PR reviewer and linter',
  instructions: 'Review pull requests carefully and provide structured comments.',
  lifecycle_state: 'pending',
  revision: 1,
  created_at: 1000,
  updated_at: 1000,
  workspace_requirements: [
    { role: 'primary', description: 'Target code repository', required: true },
  ],
  proposed_bindings: {
    primary: 'ws-repo-alpha',
  },
  local_bindings: null,
  requested_capabilities: [
    { type: 'fs', name: 'read', required: true, description: 'Read repository files' },
    { type: 'git', name: 'status', required: true },
  ],
}

const withJobWorker: WorkerRecord = {
  id: 'worker-with-job-2',
  account_scope_id: 'account-alpha',
  name: 'Security Scanner',
  description: 'Scans dependencies nightly',
  instructions: 'Run security checks and report CVEs.',
  lifecycle_state: 'pending',
  revision: 3,
  created_at: 2000,
  updated_at: 2000,
  workspace_requirements: [
    { role: 'primary', description: 'Target workspace', required: true },
  ],
  proposed_bindings: {
    primary: 'ws-security-target',
  },
  local_bindings: null,
  automations: [
    {
      id: 'auto-scan-1',
      worker_id: 'worker-with-job-2',
      name: 'Nightly CVE Audit',
      description: 'Check dependencies for known CVEs',
      activation_mode: 'interval',
      schedule: { kind: 'interval', interval_seconds: 86400, timezone: 'UTC' },
      enabled: true,
      revision: 1,
      created_at: 2000,
      updated_at: 2000,
      plan_document: {
        title: 'CVE Audit Execution',
        info: { goal: 'Identify and report known vulnerabilities in dependencies' },
        checkpoints: [
          {
            id: 'cp-audit',
            title: 'Audit Dependencies',
            tasks: ['Inspect lockfile', 'Query vulnerability database'],
            acceptance_criteria: ['Audit report generated with severity classifications'],
          },
        ],
      },
      input_requirements: [
        { name: 'severity_threshold', kind: 'string', required: true, description: 'Minimum severity to alert' },
      ],
      deliverable_requirements: [
        { name: 'audit_summary', kind: 'markdown', required: true },
      ],
    } as WorkerAutomation,
  ],
}

test('PendingWorkerCard renders collapsed summary for worker without jobs', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={noJobWorker}
      accountScopeId="account-alpha"
      workspaceSlug="demo-space"
      initialExpanded={false}
    />
  )

  // Invariant: Card identity, pending state, revision, no job indicator
  assert.match(html, /data-testid="pending-worker-card"/)
  assert.match(html, /Code Reviewer/)
  assert.match(html, /Pending Acceptance/)
  assert.match(html, /r1/)
  assert.match(html, /No job attached/)
  assert.match(html, /worker-no-job-1/)
  assert.match(html, /Autonomous PR reviewer and linter/)

  // Collapsed content must not show expanded sections
  assert.doesNotMatch(html, /data-testid="pending-worker-expanded"/)
})

test('PendingWorkerCard expanded view surfaces instructions, workspaces, capabilities, and no-job message', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={noJobWorker}
      accountScopeId="account-alpha"
      workspaceSlug="demo-space"
      initialExpanded={true}
    />
  )

  assert.match(html, /data-testid="pending-worker-expanded"/)

  // Standing instructions
  assert.match(html, /Standing Instructions/)
  assert.match(html, /Review pull requests carefully and provide structured comments\./)

  // Workspaces: Proposed vs Approved clearly separated
  assert.match(html, /Proposed Workspaces:/)
  assert.match(html, /primary/)
  assert.match(html, /ws-repo-alpha/)
  assert.match(html, /Approved Local Bindings:/)
  assert.match(html, /None approved \(pending human acceptance\)/)

  // Capabilities: Requests, not grants
  assert.match(html, /Requested Capabilities/)
  assert.match(html, /Capabilities are requests, not grants/)
  assert.match(html, /fs\/read \(required\)/)
  assert.match(html, /git\/status \(required\)/)

  // Job intent: Clear no-job statement
  assert.match(html, /data-testid="pending-worker-no-job"/)
  assert.match(html, /No job attached; waits for a task after acceptance/)

  // Blocked execution controls
  assert.match(html, /data-testid="pending-worker-execution-blocked"/)
  assert.match(html, /Nothing runs while this worker is pending/)

  // Actions: Accept worker button & Granular URL
  assert.match(html, /data-testid="accept-pending-worker"/)
  assert.match(html, /Accept worker/)
  assert.match(html, /data-testid="pending-worker-detail-link"/)
  assert.match(html, /href="\/demo-space\/workers\/worker-no-job-1"/)
})

test('PendingWorkerCard expanded view surfaces complete job intent with checkpoints and deliverables', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={withJobWorker}
      accountScopeId="account-alpha"
      workspaceSlug="demo-space"
      initialExpanded={true}
    />
  )

  // Header has job count indicator
  assert.match(html, /1 job planned/)

  // Job details
  assert.match(html, /Nightly CVE Audit/)
  assert.match(html, /Check dependencies for known CVEs/)
  assert.match(html, /Timing:\s*Every 86400 seconds \(UTC\)/)
  assert.match(html.replace(/<[^>]+>/g, ' '), /Plan:\s*CVE Audit Execution/)
  assert.match(html, /Goal: Identify and report known vulnerabilities in dependencies/)

  // Complete Checkpoints, tasks & acceptance criteria
  assert.match(html, /Checkpoints &amp; Acceptance Criteria/)
  assert.match(html, /cp-audit:\s*Audit Dependencies/)
  assert.match(html, /Inspect lockfile/)
  assert.match(html, /Query vulnerability database/)
  assert.match(html, /Audit report generated with severity classifications/)

  // Inputs and deliverables
  assert.match(html, /severity_threshold \(string, req\)/)
  assert.match(html, /audit_summary \(markdown, req\)/)
})

test('PendingWorkerCard blocks acceptance and shows warning on stale revision', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={noJobWorker}
      accountScopeId="account-alpha"
      workspaceSlug="demo-space"
      initialExpanded={true}
      stale={true}
    />
  )

  // Stale warning is rendered
  assert.match(html, /Worker definition is refreshing\. Acceptance is blocked on stale revisions\./)

  // Accept worker button must be disabled
  assert.match(html, /<button[^>]*disabled=""[^>]*data-testid="accept-pending-worker"/)
})

test('PendingWorkerCard displays mutation error when acceptance fails', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={noJobWorker}
      accountScopeId="account-alpha"
      workspaceSlug="demo-space"
      initialExpanded={true}
      mutationError="Stale revision conflict: expected 1, current is 2"
    />
  )

  assert.match(html, /role="alert"/)
  assert.match(html, /Stale revision conflict: expected 1, current is 2/)
})

test('PendingWorkerCard granular URL falls back cleanly without workspace slug', () => {
  const html = renderToStaticMarkup(
    <PendingWorkerCard
      worker={noJobWorker}
      accountScopeId="account-alpha"
      initialExpanded={true}
    />
  )

  assert.match(html, /href="\/workers\/worker-no-job-1"/)
})
