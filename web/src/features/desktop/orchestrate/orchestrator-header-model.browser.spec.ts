import test from 'node:test'
import assert from 'node:assert/strict'
import path from 'node:path'
import { existsSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { build } from 'esbuild'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { chromium } from 'playwright'

// Purpose: In Orchestrator chat, the header must present the current project identity in the
// Workspaces slot (rather than generic 'Workspaces'), open model favorites on clicking the model
// name, send full selection parameters (provider, model, thinking, serviceTier, contextMode)
// to the canonical session API while keeping agent and mode unchanged, rehydrate the header on
// successful save, preserve identity on rejection, route 'Agents' to Orchestrator-owned agents
// settings with Orchestrator selected (not the legacy chat dialog), allow conversation rename and
// session switching, and gracefully truncate long labels in narrow responsive viewports without
// horizontal overflow.
// Regular Chat and task/repair sessions must not regress to showing project identity or model buttons.
// Boundary/ownership: DesktopV3ChatHeader, AgentModelControl, OrchestrateAgents, and updateSessionV3ModelProfile.
// Browser integration with compiled theme CSS is the narrowest layer proving geometry, DOM identity,
// real event handlers, and intercepted HTTP contracts together.
test('orchestrator header shows project name, opens favorites, persists model, and routes to Orchestrator agents', { timeout: 60000 }, async () => {
  const webDir = existsSync('src/theme.css') ? process.cwd() : path.resolve(process.cwd(), 'web')
  const themePath = path.resolve(webDir, 'src/theme.css')
  const sectionCssPath = path.resolve(webDir, 'src/features/desktop/orchestrate/swarm-section.css')

  const fixture = `import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { DesktopV3ChatHeader } from './src/features/desktop/chat/components/desktop-v3-chat-header';
import { AgentModelControl } from './src/features/desktop/chat/components/agent-model-control';
import { OrchestrateAgents } from './src/features/desktop/orchestrate/orchestrate-agents';
import { updateSessionV3ModelProfile, updateSessionV3Title } from './src/features/desktop/session-v3/api';
import { agentModelSettingsQueryKey } from './src/features/desktop/settings/swarm/queries/get-agent-model-settings';
import { modelOptionsQueryOptions } from './src/features/queries/query-options';

const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
window.client = client;

const assignment = { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high', serviceTier: '', contextMode: '' };
client.setQueryData(agentModelSettingsQueryKey, {
  roles: [
    { id: 'system-orchestrator', label: 'Swarm Orchestrator', group: 'swarm', slot: 'plan' },
    { id: 'swarm', label: 'Swarm', group: 'swarm', slot: 'action' },
    { id: 'system-coder', label: 'Coder', group: 'system_agents', slot: 'coder' }
  ],
  swarm: { action: assignment, plan: assignment },
  systemAgents: { compact: assignment, finder: assignment, coder: assignment, designer: assignment, router: assignment },
  updatedAt: 1
});
client.setQueryData(modelOptionsQueryOptions().queryKey, []);

const favorite = {
  profileId: 'favorite-sonnet',
  name: 'Favorite Sonnet',
  provider: 'anthropic',
  model: 'claude-3-7-sonnet',
  thinking: 'high',
  serviceTier: 'fast',
  contextMode: 'long'
};

window.rejectSave = false;
window.modelPuts = [];
window.titlePosts = [];
window.unexpectedCalls = [];
window.destination = '';

function Harness() {
  const [sessionId, setSessionId] = useState('orchestrator-session-1');
  const [projectName, setProjectName] = useState('Project Atlas');
  const [title, setTitle] = useState('Conversation 1');
  const [modelLabel, setModelLabel] = useState('gemini-3.8-flash');
  const [openSignal, setOpenSignal] = useState(0);
  const [activeSection, setActiveSection] = useState('chat');

  window.setHarnessProps = (props) => {
    if (props.sessionId !== undefined) setSessionId(props.sessionId);
    if (props.projectName !== undefined) setProjectName(props.projectName);
    if (props.title !== undefined) setTitle(props.title);
    if (props.modelLabel !== undefined) setModelLabel(props.modelLabel);
  };

  const handleApplyModelFavorite = async (profile) => {
    await updateSessionV3ModelProfile(sessionId, {
      kind: 'temporary',
      profile: {
        name: profile.name,
        provider: profile.provider,
        model: profile.model,
        thinking: profile.thinking,
        serviceTier: profile.serviceTier,
        contextMode: profile.contextMode,
      }
    });
    setModelLabel(profile.model);
  };

  const handleRename = async (newTitle) => {
    await updateSessionV3Title(sessionId, newTitle, 'client-rename-req');
    setTitle(newTitle);
  };

  return (
    <QueryClientProvider client={client}>
      <div id="orchestrator-surface">
        <DesktopV3ChatHeader
          sessionId={sessionId}
          projectName={projectName}
          title={title}
          workspaceName="Workspace"
          modelLabel={modelLabel}
          onOpenModelFavorites={() => setOpenSignal(s => s + 1)}
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
          onOpenAgents={() => {
            window.destination = 'agents';
            setActiveSection('agents');
          }}
          onApplyModelFavorite={handleApplyModelFavorite}
          onApplyModelFavoriteChatOnly={handleApplyModelFavorite}
        />
      </div>

      {activeSection === 'agents' && (
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
    </QueryClientProvider>
  );
}

createRoot(document.getElementById('root')).render(<Harness />);`

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
    const titlePosts: any[] = []
    const unexpectedCalls: string[] = []

    await page.route('**/*', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const p = url.pathname

      if (p === '/') {
        await route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
        return
      }
      if (p.endsWith('/repositories')) {
        await route.fulfill({ json: { ok: true, items: [] } })
        return
      }
      if (p === '/v1/agent-model-settings') {
        const asgn = { provider: 'google', model: 'gemini-3.8-flash', thinking: 'high', serviceTier: '', contextMode: '' }
        await route.fulfill({
          json: {
            roles: [
              { id: 'system-orchestrator', label: 'Swarm Orchestrator', group: 'swarm', slot: 'plan' },
              { id: 'swarm', label: 'Swarm', group: 'swarm', slot: 'action' },
              { id: 'system-coder', label: 'Coder', group: 'system_agents', slot: 'coder' },
            ],
            agent_model_settings: {
              swarm: { action: asgn, plan: asgn },
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
        await route.fulfill({ json: { ok: true, metadata: { model_profile: body.choice } } })
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

    await page.goto('https://orchestrator-header.test/')
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

    // 3. Open favorites and verify rejected save preserves identity/model
    await orchModelBtn.click()
    await page.getByRole('menu', { name: 'Model favorites' }).waitFor()
    await page.evaluate(() => { (window as any).rejectSave = true })
    await page.getByRole('button', { name: 'Use Favorite Sonnet in this chat only' }).click()
    await page.getByText('Failed to update model profile on server').waitFor()
    assert.equal(modelPuts.length, 1, 'one PUT request attempted')
    assert.equal(await orchModelBtn.innerText(), 'gemini-3.8-flash', 'rejected save preserves canonical model label')
    assert.deepEqual(unexpectedCalls, [], 'agent and mode remain completely untouched')

    // 4. Successful save sends provider/model/thinking/tier/context and canonical rehydration updates header
    await page.evaluate(() => { (window as any).rejectSave = false })
    await page.getByRole('button', { name: 'Use Favorite Sonnet in this chat only' }).click()
    await page.getByRole('button', { name: 'Model favorites: claude-3-7-sonnet' }).waitFor()

    assert.equal(modelPuts.length, 2, 'second PUT succeeded')
    const lastPut = modelPuts[1]
    assert.equal(lastPut.choice.kind, 'temporary')
    assert.equal(lastPut.choice.profile.provider, 'anthropic')
    assert.equal(lastPut.choice.profile.model, 'claude-3-7-sonnet')
    assert.equal(lastPut.choice.profile.thinking, 'high')
    assert.equal(lastPut.choice.profile.service_tier, 'fast')
    assert.equal(lastPut.choice.profile.context_mode, 'long')
    assert.deepEqual(unexpectedCalls, [], 'agent and mode remain unchanged across model update')

    // 5. Open favorites and click Agents -> navigates to Orchestrator agents with Orchestrator selected
    await page.getByRole('button', { name: 'Model favorites: claude-3-7-sonnet' }).click()
    await page.getByRole('button', { name: 'Agents', exact: true }).click()
    assert.equal(await page.evaluate(() => (window as any).destination), 'agents')
    assert.equal(await page.getByRole('dialog', { name: 'Agent and model settings' }).count(), 0, 'legacy dialog is not shown')

    const agentsNav = page.locator('#orchestrator-agents-section nav[aria-label="System roles"]')
    await agentsNav.waitFor()
    const roleButtons = agentsNav.getByRole('button')
    assert.equal(await roleButtons.first().locator('strong').innerText(), 'Swarm Orchestrator')
    assert.equal(await roleButtons.first().getAttribute('aria-pressed'), 'true', 'Swarm Orchestrator role is selected')
    assert.match(await page.locator('#orchestrator-agents-section .swarm-agent-plan-note').innerText(), /Orchestrator and Plan share this assignment/)

    // 6. Conversation rename and session switch
    const renameBtn = page.locator('#orchestrator-surface button[aria-label="Rename conversation: Conversation 1"]')
    await renameBtn.click()
    const titleInput = page.locator('#orchestrator-surface input[aria-label="Conversation title"]')
    await titleInput.fill('Sprint Planning')
    await titleInput.press('Enter')
    assert.equal(titlePosts.length, 1)
    assert.equal(titlePosts[0].title, 'Sprint Planning')
    await page.locator('#orchestrator-surface button[aria-label="Rename conversation: Sprint Planning"]').waitFor()

    // Switch session: title and model update while project identity remains
    await page.evaluate(() => {
      (window as any).setHarnessProps({
        sessionId: 'orchestrator-session-2',
        title: 'Review Session',
        modelLabel: 'gpt-4o',
      })
    })
    await page.locator('#orchestrator-surface button[aria-label="Rename conversation: Review Session"]').waitFor()
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
