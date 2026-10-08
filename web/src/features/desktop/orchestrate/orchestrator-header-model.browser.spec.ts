import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { existsSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'
import type { resolveCanonicalHeaderModelLabel } from './orchestrator-header-actions'
import type { modelOptionKey } from '../chat/services/model-options'
import type { ModelOptionRecord } from '../chat/types/chat'

declare global {
  interface Window {
    resolveCanonicalHeaderModelLabel: typeof resolveCanonicalHeaderModelLabel
    modelOptionKey: typeof modelOptionKey
  }
}

// Purpose: In Orchestrator chat, the header must present the current project identity in the
// Workspaces slot (rather than generic 'Workspaces'), open model favorites on clicking the model
// name, send full selection parameters (provider, model, thinking, serviceTier, contextMode)
// to the canonical session API while keeping agent and mode unchanged, rehydrate the header on
// successful save via canonical Desktop V3 cache mutations, preserve identity on rejection,
// route 'Agents' to Orchestrator-owned agents settings with Orchestrator selected (not the legacy chat dialog),
// allow conversation rename and session switching, and gracefully truncate long labels in narrow
// responsive viewports without horizontal overflow.
// Regular Chat and task/repair sessions must not regress to showing project identity or model buttons.
// Boundary/ownership: DesktopV3ChatHeader, AgentModelControl, OrchestrateAgents,
// orchestrator-header-actions (applySessionModelFavorite, navigateToProjectAgents, useCanonicalSessionModelLabel),
// and Desktop V3 cache store hydration. Unknown cached preferences (flat or wrapped, camel/snake
// context fields) must resolve safely without overriding authoritative metadata; malformed cache
// values must not crash the header. These cases exercise the actual header resolver in the browser.
// Browser integration with compiled theme CSS and TanStack Router is the narrowest layer proving geometry, DOM identity,
// real event handlers, and intercepted HTTP/cache contracts together.
test('orchestrator header shows project name, opens favorites, persists model, and routes to Orchestrator agents', { timeout: 60000 }, async () => {
  const webDir = existsSync('src/theme.css') ? process.cwd() : path.resolve(process.cwd(), 'web')
  const themePath = path.resolve(webDir, 'src/theme.css')
  const sectionCssPath = path.resolve(webDir, 'src/features/desktop/orchestrate/swarm-section.css')

  const fixture = `import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  Outlet,
  useNavigate,
  useSearch,
  useParams,
} from '@tanstack/react-router';
import { DesktopV3ChatHeader } from './src/features/desktop/chat/components/desktop-v3-chat-header';
import { AgentModelControl } from './src/features/desktop/chat/components/agent-model-control';
import { OrchestrateAgents } from './src/features/desktop/orchestrate/orchestrate-agents';
import { updateSessionV3Title } from './src/features/desktop/session-v3/api';
import { agentModelSettingsQueryKey } from './src/features/desktop/settings/swarm/queries/get-agent-model-settings';
import { modelOptionsQueryOptions } from './src/features/queries/query-options';
import {
  applySessionModelFavorite,
  navigateToProjectAgents,
  useCanonicalSessionModelLabel,
  resolveCanonicalHeaderModelLabel,
} from './src/features/desktop/orchestrate/orchestrator-header-actions';
import { modelOptionKey } from './src/features/desktop/chat/services/model-options';
import {
  dispatchDesktopV3Cache,
  getDesktopV3CacheSnapshot,
  resetDesktopV3CacheForTests,
} from './src/features/desktop/state/desktop-v3-cache-store';
import { hydrateResponseToAction } from './src/features/desktop/state/desktop-v3-cache-wire';

const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
window.client = client;
window.getDesktopV3CacheSnapshot = getDesktopV3CacheSnapshot;
window.resolveCanonicalHeaderModelLabel = resolveCanonicalHeaderModelLabel;
window.modelOptionKey = modelOptionKey;

const assignment = { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high', serviceTier: '', contextMode: '' };
client.setQueryData(agentModelSettingsQueryKey, {
  roles: [
    { id: 'system-orchestrator', label: 'Swarm Orchestrator', group: 'swarm', slot: 'plan' },
    { id: 'swarm', label: 'Swarm', group: 'swarm', slot: 'action' },
    { id: 'system-coder', label: 'Coder', group: 'system_agents', slot: 'coder' },
  ],
  swarm: { action: assignment, plan: assignment },
  systemAgents: { compact: assignment, finder: assignment, coder: assignment, designer: assignment, router: assignment },
  updatedAt: 1,
});
client.setQueryData(modelOptionsQueryOptions().queryKey, []);

resetDesktopV3CacheForTests();
dispatchDesktopV3Cache(hydrateResponseToAction({
  sessions_by_id: {
    'orchestrator-session-1': {
      id: 'orchestrator-session-1',
      title: 'Conversation 1',
      metadata: {
        agent_name: 'system-orchestrator',
        model_profile: {
          action: { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high' },
          plan: { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high' },
        },
      },
    },
    'orchestrator-session-2': {
      id: 'orchestrator-session-2',
      title: 'Review Session',
      metadata: {
        agent_name: 'system-orchestrator',
        model_profile: {
          action: { provider: 'openai', model: 'gpt-4o' },
          plan: { provider: 'openai', model: 'gpt-4o' },
        },
      },
    },
  },
}, ['orchestrator-session-1', 'orchestrator-session-2']));

const favorite = {
  profileId: 'favorite-sonnet',
  name: 'Favorite Sonnet',
  provider: 'anthropic',
  model: 'claude-3-7-sonnet',
  thinking: 'high',
  serviceTier: 'fast',
  contextMode: 'long',
};

window.rejectSave = false;

function OrchestratorApp() {
  const navigate = useNavigate();
  const search = useSearch({ from: projectRoute.id });
  const params = useParams({ from: projectRoute.id });
  const sessionId = params.sessionId || 'orchestrator-session-1';
  const projectId = params.projectId || 'project-atlas';

  const [projectName, setProjectName] = useState('Project Atlas');
  const [title, setTitle] = useState(sessionId === 'orchestrator-session-2' ? 'Review Session' : 'Conversation 1');
  const [modelLabelOverride, setModelLabelOverride] = useState(undefined);
  const [openSignal, setOpenSignal] = useState(0);

  const canonicalModelLabel = useCanonicalSessionModelLabel(sessionId, 'auto', []);
  const resolvedModelLabel = modelLabelOverride !== undefined ? modelLabelOverride : canonicalModelLabel;

  window.setHarnessProps = (props) => {
    if (props.sessionId !== undefined && props.sessionId !== sessionId) {
      void navigate({
        to: '/projects/$projectId/sessions/$sessionId',
        params: { projectId: projectId || 'project-atlas', sessionId: props.sessionId },
        search: search.section ? { section: search.section } : {},
      });
      if (props.title !== undefined) setTitle(props.title);
      else setTitle(props.sessionId === 'orchestrator-session-2' ? 'Review Session' : 'Conversation 1');
    } else if (props.title !== undefined) {
      setTitle(props.title);
    }
    if (props.projectName !== undefined) setProjectName(props.projectName);
    if (props.modelLabel !== undefined) setModelLabelOverride(props.modelLabel);
  };

  const handleApplyModelFavorite = async (profile) => {
    return await applySessionModelFavorite({
      sessionId,
      profile,
      mode: 'auto',
    });
  };

  const handleRename = async (newTitle) => {
    await updateSessionV3Title(sessionId, newTitle, 'client-rename-req');
    setTitle(newTitle);
  };

  const handleOpenAgents = () => {
    navigateToProjectAgents(navigate, {
      projectId,
      sessionId,
    });
  };

  return (
    <>
      <div id="orchestrator-surface">
        <DesktopV3ChatHeader
          sessionId={sessionId}
          projectName={projectName}
          title={title}
          workspaceName="Workspace"
          modelLabel={resolvedModelLabel}
          onOpenModelFavorites={() => setOpenSignal((s) => s + 1)}
          modelFavoritesAnchorId={'orchestrator-model:' + sessionId}
          sessionActions={{
            pinned: false,
            canPin: true,
            onTogglePinned: () => {},
            onArchive: () => {},
            onRename: handleRename,
          }}
        />
        <AgentModelControl
          key={sessionId}
          currentAgent="system-orchestrator"
          selectedPrimaryAgent="system-orchestrator"
          agents={[]}
          modelOptions={[]}
          selectedModel={null}
          modelProfiles={[favorite]}
          showTrigger={false}
          openSignal={openSignal}
          popoverAnchorId={'orchestrator-model:' + sessionId}
          onOpenAgents={handleOpenAgents}
          onApplyModelFavorite={handleApplyModelFavorite}
          onApplyModelFavoriteChatOnly={handleApplyModelFavorite}
        />
      </div>

      {search.section === 'agents' && (
        <div id="orchestrator-agents-section">
          <OrchestrateAgents />
        </div>
      )}

      <div id="regular-chat-surface" style={{ marginTop: '20px' }}>
        <DesktopV3ChatHeader
          sessionId="regular-chat-session"
          title="Regular Chat Session"
          workspaceName="Workspace Alpha"
          modelLabel="regular-model"
        />
      </div>

      <div id="task-repair-surface" style={{ marginTop: '20px' }}>
        <DesktopV3ChatHeader
          sessionId="task-session"
          title="Task Repair Session"
          workspaceName="Task Worktree"
          modelLabel="task-model"
        />
      </div>
    </>
  );
}

const rootRoute = createRootRoute({
  component: () => (
    <QueryClientProvider client={client}>
      <Outlet />
    </QueryClientProvider>
  ),
});
const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/projects/$projectId/sessions/$sessionId',
  validateSearch: (search) => ({
    section: typeof search?.section === 'string' ? search.section : undefined,
  }),
  component: OrchestratorApp,
});

const router = createRouter({
  routeTree: rootRoute.addChildren([projectRoute]),
});
window.router = router;

createRoot(document.getElementById('root')).render(<RouterProvider router={router} />);`

  const bundle = await build({
    stdin: { contents: fixture, resolveDir: webDir, loader: 'tsx' },
    bundle: true,
    write: false,
    format: 'iife',
    platform: 'browser',
    jsx: 'automatic',
    logLevel: 'silent',
  })
  const styles = await buildStyles({
    root: webDir,
    configFile: false,
    logLevel: 'silent',
    publicDir: false,
    plugins: [tailwindcss()],
    build: {
      write: false,
      rollupOptions: { input: themePath },
    },
  })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap((r) => ('output' in r ? r.output : []))
  const css = outputs.filter((a) => a.type === 'asset' && a.fileName.endsWith('.css')).map((a) => String(a.source)).join('\n')
  const sectionCss = existsSync(sectionCssPath) ? await readFile(sectionCssPath, 'utf8') : ''

  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 440, height: 844 } })
    page.setDefaultTimeout(5000)

    const modelPuts: any[] = []
    const defaultPatches: any[] = []
    const titlePosts: any[] = []
    const unexpectedCalls: string[] = []

    await page.route('**/*', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const p = url.pathname

      if (p === '/' || p.startsWith('/projects')) {
        await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
        return
      }
      if (p.endsWith('/repositories')) {
        await route.fulfill({ json: { ok: true, items: [] } })
        return
      }
      if (p === '/v1/agent-model-settings') {
        const asgn = { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high', serviceTier: '', contextMode: '' }
        const action = { ...asgn, model: 'deployed-action' }
        const patch = request.method() === 'PATCH' ? request.postDataJSON() : null
        if (patch) defaultPatches.push(patch)
        await route.fulfill({
          json: {
            roles: [
              { id: 'system-orchestrator', label: 'Swarm Orchestrator', group: 'swarm', slot: 'plan' },
              { id: 'swarm', label: 'Swarm', group: 'swarm', slot: 'action' },
              { id: 'system-coder', label: 'Coder', group: 'system_agents', slot: 'coder' },
            ],
            agent_model_settings: {
              swarm: patch?.swarm ?? { action, plan: asgn },
              system_agents: { compact: asgn, finder: asgn, coder: asgn, designer: asgn, router: asgn },
            },
            updated_at: 1,
          },
        })
        return
      }
      if (p === '/v1/model-options') {
        await route.fulfill({ json: [] })
        return
      }
      if (p.includes('/model-profile') && request.method() === 'PUT') {
        const body = request.postDataJSON()
        modelPuts.push(body)
        const reject = await page.evaluate(() => (window as any).rejectSave)
        if (reject) {
          await route.fulfill({ status: 500, json: { error: 'Failed to update model profile on server' } })
          return
        }
        const profile = body.choice?.profile || {}
        await route.fulfill({
          json: {
            ok: true,
            session_id: 'orchestrator-session-1',
            metadata: {
              agent_name: 'system-orchestrator',
              model_profile: {
                source: 'temporary',
                action: profile,
                plan: profile,
                applied_at: Date.now(),
              },
            },
          },
        })
        return
      }
      if (p.includes('/title') && request.method() === 'POST') {
        const body = request.postDataJSON()
        titlePosts.push(body)
        await route.fulfill({ json: { ok: true, session: { id: 'orchestrator-session-1', title: body.title } } })
        return
      }
      if (p.includes('/agent') || p.includes('/mode')) {
        unexpectedCalls.push(`${request.method()} ${p}`)
        await route.fulfill({ json: { ok: true } })
        return
      }
      await route.fulfill({ status: 200, json: {} })
    })

    await page.goto('https://orchestrator-header.test/projects/project-atlas/sessions/orchestrator-session-1')
    await page.addStyleTag({ content: css + '\n' + sectionCss })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })

    // 1. Initial Orchestrator header: project name replaces Workspaces, model is clickable
    const orchRow = page.locator('#orchestrator-surface [data-testid="session-workspace-row"]')
    await orchRow.waitFor()
    const projLabel = orchRow.locator('[data-testid="desktop-v3-project-name"]')
    assert.equal(await projLabel.count(), 1, 'project name element exists in workspace position')
    assert.equal(await projLabel.innerText(), 'Project Atlas')
    assert.doesNotMatch(await orchRow.innerText(), /Workspaces/, 'Workspaces label is replaced by project name')

    // Model name in orchestrator header is a button for opening favorites
    const orchModelBtn = page.locator('#orchestrator-surface [data-testid="desktop-v3-resolved-model"]')
    assert.equal(await orchModelBtn.evaluate((el) => el.tagName), 'BUTTON')
    assert.equal(await orchModelBtn.innerText(), 'gemini-3.8-flash')

    // Unknown cache wire values are narrowed at the presentation boundary, not cast to a preference.
    const cacheLabels = await page.evaluate(() => {
      const resolve = window.resolveCanonicalHeaderModelLabel
      const key = window.modelOptionKey('openai', 'gpt-4o', 'long')
      const modelOptions: ModelOptionRecord[] = [{
        key, label: 'Long-context favorite', provider: 'openai', model: 'gpt-4o',
        contextMode: 'long', thinking: '', thinkingOptions: [], defaultThinking: '',
        thinkingProviderParameter: '', thinkingMappings: [], favorite: true,
        contextWindow: 0, pricing: null, serviceTiers: [], defaultServiceTier: '',
        serviceTierMappings: [], contextModes: [],
      }]
      const cachedPreference = { provider: 'openai', model: 'gpt-4o', context_mode: 'long' }
      return {
        flat: resolve({ cachedPreference, modelOptions }),
        wrapped: resolve({ cachedPreference: { preference: cachedPreference }, modelOptions }),
        camel: resolve({ cachedPreference: { ...cachedPreference, contextMode: 'long' }, modelOptions }),
        authoritative: resolve({
          cachedPreference,
          metadata: { agent_name: 'system-orchestrator', model_profile: {
            action: { provider: 'openai', model: 'action-model' },
            plan: { provider: 'google', model: 'plan-model' },
          } },
        }),
        malformed: [undefined, null, [], 'invalid', { provider: 42, model: 'invalid' },
          { provider: 'openai', model: {} }, { preference: null }].map(
          value => resolve({ cachedPreference: value })),
      }
    })
    assert.deepEqual(cacheLabels, {
      flat: 'Long-context favorite', wrapped: 'Long-context favorite', camel: 'Long-context favorite',
      authoritative: 'plan-model', malformed: ['', '', '', '', '', '', ''],
    })

    // 2. Non-regression: Regular Chat and task/repair headers do not show project name and retain plain model span
    const regRow = page.locator('#regular-chat-surface [data-testid="session-workspace-row"]')
    assert.equal(await regRow.locator('[data-testid="desktop-v3-project-name"]').count(), 0)
    assert.match(await regRow.innerText(), /Workspaces/)
    const regModel = page.locator('#regular-chat-surface [data-testid="desktop-v3-resolved-model"]')
    assert.equal(await regModel.evaluate((el) => el.tagName), 'SPAN')

    const taskRow = page.locator('#task-repair-surface [data-testid="session-workspace-row"]')
    assert.equal(await taskRow.locator('[data-testid="desktop-v3-project-name"]').count(), 0)
    assert.match(await taskRow.innerText(), /Workspaces/)
    const taskModel = page.locator('#task-repair-surface [data-testid="desktop-v3-resolved-model"]')
    assert.equal(await taskModel.evaluate((el) => el.tagName), 'SPAN')

    // Purpose: AgentModelControl's rendered scope buttons must isolate default
    // writes from session writes and preserve Swarm Action when Orchestrator is
    // selected. This browser layer proves actual button wiring, API payloads,
    // and unchanged canonical header identity, beyond the service unit tests.
    await orchModelBtn.click()
    await page.getByRole('button', { name: 'Make Favorite Sonnet the default model for future chats' }).click()
    await page.getByRole('menu', { name: 'Model favorites' }).waitFor({ state: 'hidden' })
    assert.equal(defaultPatches.length, 1)
    assert.equal(defaultPatches[0].swarm.action.model, 'deployed-action')
    assert.equal(defaultPatches[0].swarm.plan.model, 'claude-3-7-sonnet')
    assert.equal(modelPuts.length, 0, 'Default does not mutate this chat')
    assert.equal(await orchModelBtn.innerText(), 'gemini-3.8-flash')

    // 3. Open favorites via keyboard activation and verify rejected save preserves identity/model
    await orchModelBtn.focus()
    await page.keyboard.press('Enter')
    await page.getByRole('menu', { name: 'Model favorites' }).waitFor()
    await page.evaluate(() => { (window as any).rejectSave = true })
    await page.getByRole('button', { name: 'Use Favorite Sonnet in this chat only' }).click()
    await page.getByText('Failed to update model profile on server').waitFor()
    assert.equal(modelPuts.length, 1, 'one PUT request attempted')
    assert.equal(await orchModelBtn.innerText(), 'gemini-3.8-flash', 'rejected save preserves canonical model label')
    assert.deepEqual(unexpectedCalls, [], 'agent and mode remain completely untouched')
    const cacheBefore = await page.evaluate(() => (window as any).getDesktopV3CacheSnapshot().sessionsById['orchestrator-session-1']?.session?.metadata?.model_profile)
    assert.equal(cacheBefore?.action?.model, 'gemini-3.8-flash')

    // 4. Successful save sends provider/model/thinking/tier/context and canonical rehydration updates header
    await page.evaluate(() => { (window as any).rejectSave = false })
    await page.getByRole('button', { name: 'Use Favorite Sonnet in this chat only' }).click()
    await page.getByRole('button', { name: 'Model favorites: claude-3-7-sonnet' }).waitFor()

    assert.equal(modelPuts.length, 2, 'second PUT succeeded')
    const lastPut = modelPuts[1]
    assert.match(lastPut.client_request_id, /^desktop-model-profile:.+/)
    assert.equal(lastPut.choice.kind, 'temporary')
    assert.equal(lastPut.choice.profile.provider, 'anthropic')
    assert.equal(lastPut.choice.profile.model, 'claude-3-7-sonnet')
    assert.equal(lastPut.choice.profile.thinking, 'high')
    assert.equal(lastPut.choice.profile.service_tier, 'fast')
    assert.equal(lastPut.choice.profile.context_mode, 'long')
    assert.deepEqual(unexpectedCalls, [], 'agent and mode remain unchanged across model update')

    // Verify real cache hydration from the response
    const cacheAfter = await page.evaluate(() => (window as any).getDesktopV3CacheSnapshot().sessionsById['orchestrator-session-1']?.session?.metadata?.model_profile)
    assert.equal(cacheAfter?.action?.provider, 'anthropic')
    assert.equal(cacheAfter?.action?.model, 'claude-3-7-sonnet')
    assert.equal(cacheAfter?.action?.thinking, 'high')
    assert.equal(cacheAfter?.action?.service_tier, 'fast')
    assert.equal(cacheAfter?.action?.context_mode, 'long')

    // Combined scope explicitly saves both authorities; neither agent nor mode
    // is changed. Chat-only attempts above must not add account-default writes.
    assert.equal(defaultPatches.length, 1, 'This chat does not save a default')
    await page.getByRole('button', { name: 'Model favorites: claude-3-7-sonnet' }).click()
    await page.getByRole('button', { name: 'Make Favorite Sonnet the default and use it in this chat' }).click()
    await page.getByRole('menu', { name: 'Model favorites' }).waitFor({ state: 'hidden' })
    assert.equal(defaultPatches.length, 2)
    assert.equal(defaultPatches[1].swarm.action.model, 'deployed-action')
    assert.equal(defaultPatches[1].swarm.plan.model, 'claude-3-7-sonnet')
    assert.equal(modelPuts.length, 3)
    assert.deepEqual(unexpectedCalls, [])

    // 5. Open favorites and click Agents -> navigates to Orchestrator agents with Orchestrator selected
    await page.getByRole('button', { name: 'Model favorites: claude-3-7-sonnet' }).click()
    await page.getByRole('button', { name: 'Agents', exact: true }).click()
    assert.match(page.url(), /\/projects\/project-atlas\/sessions\/orchestrator-session-1\?section=agents/)
    const routeState = await page.evaluate(() => {
      const loc = (window as any).router.state.location
      return { pathname: loc.pathname, search: loc.search }
    })
    assert.equal(routeState.pathname, '/projects/project-atlas/sessions/orchestrator-session-1')
    assert.deepEqual(routeState.search, { section: 'agents' })
    assert.equal(await page.getByRole('dialog', { name: 'Agent and model settings' }).count(), 0, 'legacy dialog is not shown')

    const agentsNav = page.locator('#orchestrator-agents-section nav[aria-label="System roles"]')
    await agentsNav.waitFor()
    const roleButtons = agentsNav.getByRole('button')
    assert.equal(await roleButtons.first().locator('strong').innerText(), 'Swarm Orchestrator')
    assert.equal(await roleButtons.first().getAttribute('aria-pressed'), 'true', 'Swarm Orchestrator role is selected')
    assert.match(await page.locator('#orchestrator-agents-section .swarm-agent-plan-note').innerText(), /Orchestrator and Plan share this assignment/)

    // 6. Conversation rename and session switch
    // Scope to visible control because header renders both mobile and desktop title buttons in DOM
    const renameBtn = page.locator('#orchestrator-surface button[aria-label="Rename conversation: Conversation 1"]').filter({ visible: true })
    await renameBtn.click()
    const titleInput = page.locator('#orchestrator-surface input[aria-label="Conversation title"]').filter({ visible: true })
    await titleInput.fill('Sprint Planning')
    await titleInput.press('Enter')
    assert.equal(titlePosts.length, 1)
    assert.equal(titlePosts[0].title, 'Sprint Planning')
    await page.locator('#orchestrator-surface button[aria-label="Rename conversation: Sprint Planning"]').filter({ visible: true }).waitFor()

    // Switch session: title and model update while project identity remains
    await page.evaluate(() => {
      (window as any).setHarnessProps({
        sessionId: 'orchestrator-session-2',
        title: 'Review Session',
      })
    })
    await page.locator('#orchestrator-surface button[aria-label="Rename conversation: Review Session"]').filter({ visible: true }).waitFor()
    assert.equal(await page.locator('#orchestrator-surface [data-testid="desktop-v3-resolved-model"]').innerText(), 'gpt-4o')
    assert.equal(await projLabel.innerText(), 'Project Atlas', 'project identity retained across session switch')

    // 7. Narrow layout & long labels responsive validation with real compiled styles
    await page.setViewportSize({ width: 390, height: 844 })
    await page.evaluate(() => {
      (window as any).setHarnessProps({
        projectName: 'Alpha-Long-Project-Specification-Enterprise-Workspace-Cluster-2026',
        modelLabel: 'anthropic / claude-3-7-sonnet-thinking-super-extended-identifier',
      })
    })
    // Wait for React to commit the new long text before asserting geometry
    await projLabel.filter({ hasText: 'Alpha-Long-Project-Specification-Enterprise-Workspace-Cluster-2026' }).waitFor()
    await page.locator('#orchestrator-surface [data-testid="desktop-v3-resolved-model"]').filter({ hasText: 'anthropic / claude-3-7-sonnet-thinking-super-extended-identifier' }).waitFor()

    const noOverflow = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
    assert.equal(noOverflow, true, 'narrow layout has no horizontal overflow')

    const headerBox = await page.locator('#orchestrator-surface header').boundingBox()
    const projBox = await projLabel.boundingBox()
    const modelBox = await page.locator('#orchestrator-surface [data-testid="desktop-v3-resolved-model"]').boundingBox()
    assert.ok(headerBox && projBox && modelBox)
    assert.ok(projBox.x >= headerBox.x && projBox.x + projBox.width <= headerBox.x + headerBox.width)
    assert.ok(modelBox.x >= headerBox.x && modelBox.x + modelBox.width <= headerBox.x + headerBox.width + 2)

    const pnTruncate = await projLabel.evaluate((el) => {
      const s = getComputedStyle(el)
      return s.overflow === 'hidden' && s.textOverflow === 'ellipsis'
    })
    assert.equal(pnTruncate, true, 'project name element truncates with ellipsis')

    const modelTruncate = await page.locator('#orchestrator-surface [data-testid="desktop-v3-resolved-model"]').evaluate((el) => {
      const s = getComputedStyle(el)
      return s.overflow === 'hidden' && s.textOverflow === 'ellipsis'
    })
    assert.equal(modelTruncate, true, 'model button truncates with ellipsis')
  } finally {
    await browser.close()
  }
})
