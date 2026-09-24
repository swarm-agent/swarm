import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { AutomationV2SendRequestModal } from './automation-v2-send-request-modal'
import { AutomationV2WorkerDetailPage, AutomationV2Workspace } from './automation-v2-workspace'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'

const sampleSpecialistWorker: AutomationV2Record = {
  automation_id: 'av2_social_specialist_123',
  session_id: 'session-social-specialist',
  workspace_id: 'ws-swarm-go',
  account_id: 'account-1',
  user_id: 'user-1',
  created_at: 100000,
  accepted_at: 100500,
  accepted_by: 'user-1',
  enabled: true,
  generation: 1,
  revision: 1,
  digest: 'digest-social-1',
  proposal_id: 'proposal-social-1',
  authorization: { kind: 'indefinite' },
  document: {
    title: 'Swarm Social Specialist',
    info: { goal: 'Create, schedule, and optimize social media content and graphics.' },
    checkpoints: [],
    worker_v2: {
      version: 2,
      schedule: {
        kind: 'trigger',
      },
    },
  },
}

test('AutomationV2SendRequestModal renders worker details, prompt input, and submit button', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2SendRequestModal
      open={true}
      onOpenChange={() => {}}
      record={sampleSpecialistWorker}
      workspaceId="ws-swarm-go"
      workspaceSlug="swarm-go"
    />
  )

  assert.match(markup, /Send request to worker/)
  assert.match(markup, /Swarm Social Specialist/)
  assert.match(markup, /Specialist Worker/)
  assert.match(markup, /data-testid="send-request-prompt-input"/)
  assert.match(markup, /data-testid="send-request-submit-button"/)
})

test('AutomationV2WorkerDetailPage renders Send request to worker button', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2WorkerDetailPage
      workspaceId="ws-swarm-go"
      workspacePath="/path/to/swarm-go"
      workspaceSlug="swarm-go"
      record={sampleSpecialistWorker}
      onBack={() => {}}
      onControlRecord={async () => {}}
      onArchiveRecord={async () => {}}
      onDeleteRecord={() => {}}
      actionLoadingId={null}
    />
  )

  assert.match(markup, /data-testid="detail-send-request-to-worker-btn"/)
  assert.match(markup, /Send request to worker/)
})

test('AutomationV2Workspace flat overview renders Send request to worker on worker cards', () => {
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-swarm-go' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-swarm-go' },
    requestId: 'req-ws-send-req',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-ws-send-req',
    generation: 0,
    data: { records: [sampleSpecialistWorker] },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-swarm-go"
      workspacePath="/path/to/swarm-go"
      workspaceName="Swarm Go"
      workspaceSlug="swarm-go"
    />
  )

  assert.match(markup, /data-testid="send-request-to-worker-btn"/)
  assert.match(markup, /Send request to worker/)
  assert.match(markup, /Discuss with Swarm/)
})
