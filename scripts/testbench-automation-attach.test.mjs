// Requirement: live Automation tests may reach only their explicitly owned fixture.
// Threat: a broad attach opt-in authorizes a different automation or account discovery.
// Authority: AttachClient.request route guard, before any network operation.
// A fake fetch proves rejected requests publish nothing without a live daemon.
import test from 'node:test'
import assert from 'node:assert/strict'
import { AttachClient } from './testbench-attach.mjs'

test('Automation attach rejects unbound and foreign operations before transport', async () => {
  const previous = globalThis.fetch
  const calls = []
  globalThis.fetch = async (url, init) => { calls.push({url, init}); return new Response('{}', {status:200}) }
  try {
    const c = new AttachClient('http://127.0.0.1:18081')
    const route = '/v3/automations/v2/review?workspace_id=workspace&session_id=fixture'
    await assert.rejects(c.request('GET', route), /unreviewed/)
    c.automationTrial = {sessionId:'fixture', workspaceId:'workspace'}
    await assert.rejects(c.request('GET', route.replace('fixture', 'foreign')), /unreviewed/)
    await assert.rejects(c.request('POST', '/v3/automations/v2/accept', {session_id:'foreign',workspace_id:'workspace'}), /unreviewed/)
    await assert.rejects(c.request('POST', '/v3/sessions/foreign/sidechats/plan', {}), /unreviewed/)
    await assert.rejects(c.request('GET', '/v3/automations/v2?workspace_id=workspace'), /unreviewed/)
    assert.equal(calls.length, 0)
    await c.request('GET', route)
    await c.request('POST', '/v3/sessions/fixture/sidechats/plan', {})
    assert.equal(calls.length, 2)
  } finally { globalThis.fetch = previous }
})
