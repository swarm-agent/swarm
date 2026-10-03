import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { fixtureRead, project, sessionId, snapshot } from './swarm-responsive-browser-fixtures'

// Purpose: ProjectEntryPage and OrchestrateView must route first/later creation
// through the same UI, resume pending IDs, and keep existing conversations usable
// despite failed task/source reads. Real router mounts prove entry/navigation
// behavior that isolated runtime tests cannot. Coding submission without an explicit
// source must reject locally without task writes or loss of the chat draft.
// No live daemon/provider claims.
test('project entry shares creation and resumes pending projects without blocking existing chat', { timeout: 60000 }, async () => {
  const js = await build({ stdin: { resolveDir: process.cwd(), loader: 'tsx', contents: `import {mountResponsiveFixture} from './src/features/desktop/orchestrate/swarm-responsive-browser-fixtures'; mountResponsiveFixture('populated', true);` }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } }); page.setDefaultTimeout(6000)
    let rows: any[] = []
    const posts: string[] = []
    await page.route('**/*', route => {
      const req = route.request(), url = new URL(req.url())
      if (req.isNavigationRequest()) return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (req.method() === 'POST') posts.push(url.pathname)
      const conversation = { ...snapshot().sessions_by_id[sessionId], metadata: { project_id: project.id, agent_name: 'system-orchestrator' } }
      if (url.pathname === `/v3/sessions/${sessionId}`) return route.fulfill({ json: { session: conversation } })
      if (url.pathname === `/v3/projects/${project.id}/sessions`) return route.fulfill({ json: { sessions: [{ session: conversation }] } })
      if (url.pathname === '/v3/sync/hydrate') return route.fulfill({ json: { ...snapshot(), sessions_by_id: { [sessionId]: conversation } } })
      if (url.pathname === '/v3/projects') return route.fulfill({ json: { projects: rows } })
      if (url.pathname === '/v3/projects/pending') return route.fulfill({ json: { project: rows.find(p => p.id === 'pending') } })
      if (url.pathname === '/v3/projects/pending/sessions') return route.fulfill({ json: { sessions: [] } })
      if (url.pathname.endsWith('/tasks') || url.pathname === '/v1/git/status') return route.fulfill({ status: 409, json: { error: 'Coding source is not Git ready' } })
      const value = fixtureRead(url, 'populated')
      return route.fulfill({ status: value === undefined ? 501 : 200, json: value ?? { error: 'Unknown fixture read' } })
    })
    const mount = async (url: string) => { await page.goto(url); await page.addScriptTag({ content: js.outputFiles[0].text }) }
    await mount('https://entry.test/projects')
    await page.getByRole('region', { name: 'Project creation' }).waitFor()
    assert.equal(await page.getByLabel('Project name', { exact: true }).inputValue(), '')
    rows = [project, { id: 'pending', name: 'Pending', context_generation: { status: 'failed', attempt: 1, error: 'Generation interrupted' } }]
    await mount('https://entry.test/projects')
    await page.getByRole('link', { name: 'Create a project', exact: true }).click()
    await page.getByRole('region', { name: 'Project creation' }).waitFor()
    await page.getByRole('button', { name: 'Back to projects' }).click()
    await page.getByRole('link', { name: /Pending.*resume/ }).click()
    await page.getByRole('alert').filter({ hasText: 'Generation interrupted' }).waitFor()
    assert.equal(posts.some(p => p.endsWith('/sessions') || p === '/v3/projects'), false)
    await mount('https://entry.test/projects/' + project.id + '/sessions/' + sessionId)
    await page.getByTestId('orchestrator-chat-input').waitFor()
    assert.equal(await page.getByRole('region', { name: 'Project creation' }).count(), 0)
    await page.getByTestId('orchestrator-chat-input').fill('Conversation still available')
    assert.equal(await page.getByTestId('orchestrator-chat-input').inputValue(), 'Conversation still available')
    await page.getByRole('button', { name: 'New task', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    await dialog.locator('textarea').first().fill('Implement a change')
    // This fixture bundles components without the application stylesheet; use
    // keyboard activation rather than claiming pointer/layout validation.
    await dialog.getByRole('button', { name: 'Create Pending Task', exact: true }).focus()
    await page.keyboard.press('Enter')
    await page.getByTestId('deploy-modal-error').filter({ hasText: 'Select the intended coding workspace' }).waitFor()
    assert.equal(posts.some(p => p.endsWith('/tasks')), false)
    await page.keyboard.press('Escape')
    assert.equal(await page.getByTestId('orchestrator-chat-input').inputValue(), 'Conversation still available')
  } finally { await browser.close() }
})
