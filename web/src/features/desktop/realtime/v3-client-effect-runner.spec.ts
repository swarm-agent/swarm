import test from 'node:test'
import assert from 'node:assert/strict'
import { QueryClient } from '@tanstack/react-query'

import {
  createDefaultDesktopV3ClientEffectRunnerDeps,
  DesktopV3ClientEffectRunner,
  applyCanonicalDesktopTheme,
  desktopRouteWorkspacePath,
  durableClientEffectsFromRealtimeFrame,
} from './v3-client-effect-runner'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'
import type { WorkspaceOverviewResponse } from '../../workspaces/launcher/types/workspace-overview'
import { WORKSPACE_THEME_OPTIONS } from '../../workspaces/launcher/services/workspace-theme'

function toolCompletedFrame(input: {
  id?: string
  seq?: number
  eventType?: string
  effects?: unknown
} = {}): RealtimeMessage {
  return {
    kind: 'event',
    session_id: 'session-1',
    event_type: input.eventType ?? 'session.tool.completed',
    event: {
      id: input.id ?? 'event-1',
      session_id: 'session-1',
      seq: input.seq ?? 8,
      event_type: input.eventType ?? 'session.tool.completed',
      payload: {
        client_effects: input.effects ?? [{ type: 'refresh_agents' }],
      },
      ts_unix_ms: 1,
    },
  }
}

test('durableClientEffectsFromRealtimeFrame parses only typed successful tool completion effects', () => {
  assert.deepEqual(durableClientEffectsFromRealtimeFrame(toolCompletedFrame({
    effects: [{ type: 'refresh_agents' }, { type: 'refresh_themes' }, { type: 'reload_everything' }],
  })), {
    eventIdentity: 'event-1',
    effects: [{ type: 'refresh_agents' }, { type: 'refresh_themes' }],
  })
  assert.equal(durableClientEffectsFromRealtimeFrame(toolCompletedFrame({ eventType: 'session.tool.failed' })), null)
  assert.equal(durableClientEffectsFromRealtimeFrame(toolCompletedFrame({ effects: [] })), null)
})

test('auth realtime frames produce a durable provider refresh effect', () => {
  assert.deepEqual(durableClientEffectsFromRealtimeFrame({
    kind: 'auth.credentials.updated',
    auth: {
      account_scope_id: 'account-1',
      event_type: 'auth.credential.activated',
      provider: 'openai',
      recorded_at: 10,
      event_sequence: 42,
    },
  }), {
    eventIdentity: 'auth:account-1:42',
    effects: [{ type: 'refresh_providers' }],
  })
})

test('default provider effect force-fetches model options without waiting for stale time', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(['model-options'], { providers: [{ id: 'old-provider' }] })
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    calls.push(url)
    if (url === '/v1/providers') {
      return new Response(JSON.stringify({ providers: [{ id: 'new-provider', models: [] }] }), { headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v1/model') {
      return new Response(JSON.stringify({ preference: {} }), { headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ ok: true, state: { active_primary: '', active_subagent: {}, profiles: [] }, tool_inventory: { tools: [], presets: [] } }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  try {
    await createDefaultDesktopV3ClientEffectRunnerDeps(queryClient).refreshProviders()
    assert.ok(calls.includes('/v1/providers'))
    assert.deepEqual(queryClient.getQueryData(['model-options']), { providers: [{ id: 'new-provider', models: [] }] })
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('default agent effect force-fetches canonical active-agent state and invalidates profile contracts', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(['agent-tool-contract', 'deleted-agent'], { tools: {} })
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    calls.push(url)
    if (url === '/v1/model') {
      return new Response(JSON.stringify({ preference: {} }), { headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({
      ok: true,
      state: {
        active_primary: 'new-agent',
        active_subagent: {},
        profiles: [{ name: 'new-agent', mode: 'primary', enabled: true }],
      },
      tool_inventory: { tools: [], presets: [] },
    }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  try {
    await createDefaultDesktopV3ClientEffectRunnerDeps(queryClient).refreshAgents()
    assert.ok(calls.includes('/v1/model'))
    assert.ok(calls.includes('/v2/agents?limit=200&view=summary'))
    assert.ok(calls.includes('/v2/agents?limit=200'))
    assert.equal(queryClient.getQueryData<{ activePrimary?: string }>(['agent-state'])?.activePrimary, 'new-agent')
    assert.equal(queryClient.getQueryState(['agent-tool-contract', 'deleted-agent'])?.isInvalidated, true)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('client effect runner refreshes agent caches once across replayed durable events', async () => {
  let refreshAgents = 0
  let refreshThemes = 0
  const runner = new DesktopV3ClientEffectRunner({
    refreshAgents: async () => { refreshAgents += 1 },
    refreshThemes: async () => { refreshThemes += 1 },
    refreshProviders: async () => undefined,
    reportError: () => assert.fail('effect should not fail'),
  })
  const frame = toolCompletedFrame()

  runner.accept(frame)
  runner.accept(frame)
  await runner.waitForIdle()

  assert.equal(refreshAgents, 1)
  assert.equal(refreshThemes, 0)
})

test('client effect runner serializes and coalesces refreshes while preserving later mutations', async () => {
  let releaseFirst!: () => void
  const firstRefresh = new Promise<void>((resolve) => { releaseFirst = resolve })
  let refreshAgents = 0
  const runner = new DesktopV3ClientEffectRunner({
    refreshAgents: async () => {
      refreshAgents += 1
      if (refreshAgents === 1) await firstRefresh
    },
    refreshThemes: async () => undefined,
    refreshProviders: async () => undefined,
    reportError: () => assert.fail('effect should not fail'),
  })

  runner.accept(toolCompletedFrame({ id: 'event-1' }))
  await Promise.resolve()
  runner.accept(toolCompletedFrame({ id: 'event-2' }))
  runner.accept(toolCompletedFrame({ id: 'event-3' }))
  releaseFirst()
  await runner.waitForIdle()

  assert.equal(refreshAgents, 2)
})

test('client effect failures are reported without retry storms', async () => {
  let failures = 0
  const runner = new DesktopV3ClientEffectRunner({
    refreshAgents: async () => { throw new Error('offline') },
    refreshThemes: async () => undefined,
    refreshProviders: async () => undefined,
    reportError: (effect, error) => {
      failures += 1
      assert.equal(effect, 'refresh_agents')
      assert.match(String(error), /offline/)
    },
  })
  const frame = toolCompletedFrame()

  runner.accept(frame)
  await runner.waitForIdle()
  runner.accept(frame)
  await runner.waitForIdle()

  assert.equal(failures, 1)
})

test('desktopRouteWorkspacePath preserves workspace-over-global route precedence', () => {
  const overview = {
    ok: true,
    currentWorkspace: null,
    discovered: [],
    swarmTarget: null,
    workspaces: [{ path: '/repo/alpha', workspaceName: 'Alpha', themeId: 'workspace-theme' }],
  } as WorkspaceOverviewResponse

  assert.equal(desktopRouteWorkspacePath('/alpha/session-1', overview), '/repo/alpha')
  assert.equal(desktopRouteWorkspacePath('/settings', overview), null)
  assert.equal(desktopRouteWorkspacePath('/', overview), null)
})

test('applyCanonicalDesktopTheme installs canonical builtins and custom palettes before applying the route-effective theme', () => {
  const previousDocument = globalThis.document
  const properties = new Map<string, string>()
  const root = {
    dataset: {} as Record<string, string>,
    style: {
      setProperty: (name: string, value: string) => { properties.set(name, value) },
      removeProperty: (name: string) => { properties.delete(name) },
    },
    removeAttribute: (name: string) => { delete root.dataset[name] },
  }
  Object.defineProperty(globalThis, 'document', {
    value: { documentElement: root },
    configurable: true,
  })
  const overview = {
    ok: true,
    currentWorkspace: null,
    discovered: [],
    swarmTarget: null,
    workspaces: [{ path: '/repo/alpha', workspaceName: 'Alpha', themeId: 'workspace-theme' }],
  } as WorkspaceOverviewResponse

  try {
    applyCanonicalDesktopTheme({
      theme: {
        active_id: 'global-theme',
        default_theme_id: 'tide',
        builtin_themes: [{
          id: 'castor',
          name: 'Castor',
          palette: {
            background: '#36272B', panel: '#402E33', border: '#6B4D55', text: '#F7ECEF',
            text_muted: '#CBB1B8', primary: '#FF6B81', success: '#8CE6A4', warning: '#FFD275', error: '#FF5E6C',
          },
        }],
        custom_themes: [
          { id: 'global-theme', palette: { background: '#111111' } },
          { id: 'workspace-theme', palette: { background: '#222222' } },
        ],
      },
    }, overview, '/alpha/session-1')
    assert.equal(properties.get('--app-bg'), '#222222')
    assert.ok(WORKSPACE_THEME_OPTIONS.some((item) => item.id === 'castor' && item.label === 'Castor'))

    applyCanonicalDesktopTheme({
      theme: {
        active_id: 'global-theme',
        default_theme_id: 'tide',
        builtin_themes: [],
        custom_themes: [{ id: 'global-theme', palette: { background: '#333333' } }],
      },
    }, { ...overview, workspaces: [{ ...overview.workspaces[0], themeId: '' }] }, '/')
    assert.equal(properties.get('--app-bg'), '#333333')
  } finally {
    Object.defineProperty(globalThis, 'document', { value: previousDocument, configurable: true })
  }
})
