import assert from 'node:assert/strict'
import test from 'node:test'

import { saveThinkingTagsSetting } from './save-thinking-tags-setting'
import { saveSwarmSettings } from './save-swarm-settings'
import { saveLocalContainerUpdateWarningDismissal } from './save-local-container-update-warning-dismissal'

function installFetchMock(handler: (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>) {
  const original = globalThis.fetch
  globalThis.fetch = handler as typeof fetch
  return () => {
    globalThis.fetch = original
  }
}

test('saveThinkingTagsSetting sends only thinking_tags patch', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({ chat: { thinking_tags: false } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveThinkingTagsSetting(false)
    assert.equal(response.chat?.thinking_tags, false)
    assert.deepEqual(JSON.parse(capturedBody), { chat: { thinking_tags: false } })
  } finally {
    restore()
  }
})

test('saveSwarmSettings sends only swarm name and default mode patch', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({
      chat: { default_new_session_mode: 'plan' },
      swarm: { name: 'Desk' },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveSwarmSettings({
      current: { chat: { default_new_session_mode: 'plan' } },
      name: 'Desk',
    })
    assert.equal(response.name, 'Desk')
    assert.deepEqual(JSON.parse(capturedBody), {
      chat: { default_new_session_mode: 'plan' },
      swarm: { name: 'Desk' },
    })
  } finally {
    restore()
  }
})

test('saveLocalContainerUpdateWarningDismissal sends only updates patch', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({ updates: { local_container_warning_dismissed: true } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveLocalContainerUpdateWarningDismissal(true)
    assert.equal(response.updates?.local_container_warning_dismissed, true)
    assert.deepEqual(JSON.parse(capturedBody), {
      updates: { local_container_warning_dismissed: true },
    })
  } finally {
    restore()
  }
})
