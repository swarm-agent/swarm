import assert from 'node:assert/strict'
import test from 'node:test'

import { saveDefaultNewSessionMode } from './save-default-new-session-mode'
import { savePlanContextGuardSettings } from './save-plan-context-guard-settings'
import { saveThinkingTagsSetting } from './save-thinking-tags-setting'
import { saveShowCompactButtonSetting } from './save-show-compact-button-setting'
import { saveShowTipsSetting } from './save-show-tips-setting'
import { saveSwarmSettings } from './save-swarm-settings'
import { saveDefaultWorkspaceRoute } from './save-default-workspace-route'

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

test('saveShowTipsSetting sends only show_tips patch', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({ chat: { show_tips: false } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveShowTipsSetting(false)
    assert.equal(response.chat?.show_tips, false)
    assert.deepEqual(JSON.parse(capturedBody), { chat: { show_tips: false } })
  } finally {
    restore()
  }
})

test('saveShowCompactButtonSetting sends only show_compact_button patch', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({ chat: { show_compact_button: true } }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveShowCompactButtonSetting(true)
    assert.equal(response.chat?.show_compact_button, true)
    assert.deepEqual(JSON.parse(capturedBody), { chat: { show_compact_button: true } })
  } finally {
    restore()
  }
})

test('savePlanContextGuardSettings sends only normalized guard fields', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({
      chat: {
        plan_context_guard_enabled: false,
        plan_context_guard_used_percent: 95,
        plan_context_guard_max_compactions: 3,
      },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await savePlanContextGuardSettings(
      { theme: { active_id: 'tide' }, chat: { thinking_tags: false } },
      { enabled: false, usedPercent: 99, maxCompactions: 9 },
    )
    assert.equal(response.chat?.plan_context_guard_used_percent, 95)
    assert.deepEqual(JSON.parse(capturedBody), {
      chat: {
        plan_context_guard_enabled: false,
        plan_context_guard_used_percent: 95,
        plan_context_guard_max_compactions: 3,
      },
    })
  } finally {
    restore()
  }
})

test('saveSwarmSettings sends only swarm name patch and returns refreshed target name', async () => {
  let capturedBody = ''
  const seenURLs: string[] = []
  const restore = installFetchMock(async (input, init) => {
    const url = String(input)
    seenURLs.push(url)
    if (url.includes('/v1/swarm/targets')) {
      return new Response(JSON.stringify({
        ok: true,
        targets: [{ swarm_id: 'primary-swarm', name: 'Primary Renamed', role: 'master', relationship: 'self', kind: 'self', online: true, selectable: true, current: true }],
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({
      swarm: { name: 'Primary Renamed' },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveSwarmSettings({ name: 'Primary Renamed' })
    assert.equal(response.name, 'Primary Renamed')
    assert.deepEqual(JSON.parse(capturedBody), {
      swarm: { name: 'Primary Renamed' },
    })
    assert(seenURLs.some((url) => url.includes('/v1/swarm/targets')), 'expected immediate target refresh')
  } finally {
    restore()
  }
})

test('saveDefaultNewSessionMode preserves existing chat fields and writes default mode', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({
      chat: { thinking_tags: false, default_new_session_mode: 'plan', default_workspace_routes: { '/repo': 'self' } },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveDefaultNewSessionMode({
      current: { chat: { thinking_tags: false, default_workspace_routes: { '/repo': 'self' } } },
      mode: 'plan',
    })
    assert.equal(response.chat?.default_new_session_mode, 'plan')
    assert.deepEqual(JSON.parse(capturedBody), {
      chat: {
        thinking_tags: false,
        default_workspace_routes: { '/repo': 'self' },
        default_new_session_mode: 'plan',
      },
    })
  } finally {
    restore()
  }
})

test('saveDefaultWorkspaceRoute preserves existing chat fields and writes server route default', async () => {
  let capturedBody = ''
  const restore = installFetchMock(async (_input, init) => {
    capturedBody = String(init?.body ?? '')
    return new Response(JSON.stringify({
      chat: { thinking_tags: false, default_workspace_routes: { '/repo': 'swarm:child:/repo' } },
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  })

  try {
    const response = await saveDefaultWorkspaceRoute({
      current: { chat: { thinking_tags: false, default_new_session_mode: 'plan' } },
      workspacePath: '/repo',
      routeId: 'swarm:child:/repo',
    })
    assert.equal(response.chat?.default_workspace_routes?.['/repo'], 'swarm:child:/repo')
    assert.deepEqual(JSON.parse(capturedBody), {
      chat: {
        thinking_tags: false,
        default_new_session_mode: 'plan',
        default_workspace_routes: { '/repo': 'swarm:child:/repo' },
      },
    })
  } finally {
    restore()
  }
})
