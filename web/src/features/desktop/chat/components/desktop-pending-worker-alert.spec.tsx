import React from 'react';
import assert from 'node:assert/strict';
import test from 'node:test';
import { renderToStaticMarkup } from 'react-dom/server';
import type { DesktopPermissionRecord } from '../../types/realtime';
import { DesktopAcceptedWorkerCard, DesktopPendingWorkerAlert } from './desktop-pending-worker-alert';

// Requirement: the authoring chat displays a reviewable worker request and a
// distinct accepted result; permission acceptance still uses the canonical
// revision-guarded mutation tested by pending-worker-sidebar-reviews.spec.tsx.
// Threat: a hidden modal or missing chat result strands the request.
const worker = {
  id: 'permission_p', sessionId: 'author', toolName: 'manage_workers',
  requirement: 'automation_v2_acceptance', status: 'pending',
  toolArguments: JSON.stringify({ review_kind: 'worker_v2', scope: { workspace_id: 'workspace' },
    worker_review: { proposal_id: 'p', revision: 1, digest: 'a'.repeat(64) },
    document: { title: 'Trigger audit', info: { goal: 'Check on demand' },
      checkpoints: [{ id: 'cp-1', title: 'Check', acceptance_criteria: ['Done'] }],
      worker_v2: { schema_version: 2, schedule: { kind: 'trigger' }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true },
    },
  }),
} as DesktopPermissionRecord;

test('worker request and accepted result are visible as distinct desktop cards', () => {
  const markup = renderToStaticMarkup(<DesktopPendingWorkerAlert permission={worker} onAccepted={() => {}} />);
  assert.match(markup, /data-testid="pending-worker-alert"/);
  assert.match(markup, /Trigger audit/);
  assert.match(markup, /Review worker/);
  assert.doesNotMatch(markup, /Accept worker/); // review must be opened before accepting
  const accepted = renderToStaticMarkup(<DesktopAcceptedWorkerCard title="Trigger audit" workerId="worker-1" />);
  assert.match(accepted, /Worker accepted/);
  assert.match(accepted, /worker-1/);
});
