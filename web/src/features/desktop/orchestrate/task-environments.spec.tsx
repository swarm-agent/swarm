// Purpose: every canonical attachment must render truthful status and only exact
// deployment navigation. SSR of TaskEnvironments is the narrow rendering boundary;
// it proves absence of invented links before a user health check, not browser interaction.
import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskEnvironments } from './task-environments'
import { mapBackendTask } from '../state/desktop-projects-state'
import type { TaskEnvironmentAttachment } from '../environments/types/environments'

const attachment: TaskEnvironmentAttachment = { id: 'a', revision: 1, account_scope_id: 'account', project_id: 'p', task_id: 't',
  environment_id: 'env', environment_name: 'Test app', state: 'ready', expires_at: 4102444800000,
  source: { workspace_id: 'w', deployment_id: 'd' } }
const render = (attachments: TaskEnvironmentAttachment[]) => renderToStaticMarkup(<TaskEnvironments projectId="p"
  task={mapBackendTask({ id: 't', environment_attachments: attachments })} />)
test('all attachment states render without inventing browser readiness', () => {
  for (const state of ['ready', 'building', 'preparing', 'stopped', 'failed', 'stale'] as const) {
    const html = render([{ ...attachment, state }])
    assert.match(html, /Test app/)
    assert.match(html, new RegExp(state))
    assert.match(html, /workspace_id=w&amp;deployment_id=d/)
    assert.doesNotMatch(html, /Open in browser/)
    if (state !== 'ready') assert.match(html, /disabled=""/)
  }
  assert.doesNotMatch(render([{ ...attachment, state: 'preparing', source: { workspace_id: 'w' } }]), /Deployment details/)
  assert.match(render([{ ...attachment, expires_at: 1 }]), /stale/)
  assert.equal((render([attachment, { ...attachment, id: 'b' }]).match(/Test app/g) || []).length, 2)
  assert.doesNotMatch(render([]), /Test app|Deployment details/)
})

// Purpose: the card rendering boundary must expose bounded text, not executable
// markup, and must not render empty chrome or waive project/task identity.
test('empty attachments omit chrome and error text is escaped and bounded', () => {
  assert.equal(render([]), '')
  const html = render([{ ...attachment, error_message: '<script>bad</script>\u0000' + 'x'.repeat(900) }])
  assert.match(html, /&lt;script&gt;bad/)
  assert.doesNotMatch(html, /<script>|\u0000|x{513}/)
  const mismatch = render([{ ...attachment, project_id: 'foreign' }])
  assert.match(mismatch, /stale/)
  assert.match(mismatch, /disabled=""/)
})
