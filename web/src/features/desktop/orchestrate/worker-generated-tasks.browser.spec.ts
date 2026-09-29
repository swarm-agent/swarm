import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { chromium } from 'playwright'
import { build as buildStyles } from 'vite'
import tailwindcss from '@tailwindcss/vite'
import { mkdir } from 'node:fs/promises'
import path from 'node:path'

// Requirement: generated tasks share the actual project task list with human tasks;
// the Worker tasks filter uses durable identity, not a generic worker name.
// Threat: the UI can pass source checks while hiding generated tasks or showing a
// separate worker dashboard. Boundary: real OrchestrateView + DesktopProjectsRuntime.
// This browser component test supplies HTTP fixtures; it is NOT a live worker run.
test('project task list renders generated tasks, exact worker links and source filtering', { timeout: 60000 }, async () => {
  const fixture = `import React from 'react'; import {createRoot} from 'react-dom/client';
    import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
    import {createRootRoute,createRoute,createRouter,RouterProvider,createMemoryHistory} from '@tanstack/react-router';
    import {OrchestrateView} from './src/features/desktop/orchestrate/OrchestrateView';
    import {desktopProjects} from './src/features/desktop/runtime/desktop-projects';
    const root=createRootRoute({component:()=> <QueryClientProvider client={client}><OrchestrateView workspaceSlug='demo'/></QueryClientProvider>});
    const child=createRoute({getParentRoute:()=>root,path:'/demo/swarm/$swarmSection'});
    const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
    const router=createRouter({routeTree:root.addChildren([child]),history:createMemoryHistory({initialEntries:['/demo/swarm/home']})});
    window.refreshTasks=()=>desktopProjects.invalidate('project_demo');
    createRoot(document.getElementById('root')).render(<RouterProvider router={router}/>);`
  const bundle = await build({ stdin: { contents: fixture, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', platform: 'browser', jsx: 'automatic', logLevel: 'silent' })
  const tasks: Record<string, unknown>[] = [
    { id: 'task_human', title: 'Manual review', agent: 'coder', worker_name: '@Coder Worker', status: 'completed', created_at: Date.now() - 120000 },
    { id: 'task_generated', title: 'Scheduled audit', agent: 'swarm', worker_id: 'worker_alpha', worker_name: 'Daily Audit', worker_run_id: 'run_alpha', automation_id: 'automation_a', status: 'in_progress', created_at: Date.now() - 60000 },
    { id: 'task_generated_2', title: 'Scheduled backup', agent: 'swarm', worker_id: 'worker_beta', worker_name: 'Daily Audit', worker_run_id: 'run_beta', status: 'queued', created_at: Date.now() - 10000 },
  ]
  const styles = await buildStyles({ configFile: false, logLevel: 'silent', publicDir: false, plugins: [tailwindcss()], build: { write: false, rollupOptions: { input: path.resolve('src/theme.css') } } })
  const outputs = (Array.isArray(styles) ? styles : [styles]).flatMap(result => 'output' in result ? result.output : [])
  const css = outputs.filter(asset => asset.type === 'asset' && asset.fileName.endsWith('.css')).map(asset => asset.type === 'asset' ? String(asset.source) : '').join('\n')
  const browser = await chromium.launch({ headless: true, channel: process.env.SWARM_TEST_BROWSER_CHANNEL || 'chrome' })
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await page.route('**/*', route => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = {}
      if (path === '/') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
      if (path.endsWith('/projects')) body = { projects: [{ id: 'project_demo', name: 'Demo', workspaces: [] }] }
      else if (path.endsWith('/projects/project_demo/tasks')) body = { tasks }
      else if (path.endsWith('/projects/project_demo/media')) body = { media: [] }
      else if (path.endsWith('/auth/desktop/session')) body = { ok: true, user_id: 'operator', account_scope_id: 'account_demo' }
      else if (path.endsWith('/me')) body = { username: 'Operator' }
      else if (path.includes('/workers')) body = { workers: [] }
      else if (path.includes('/workspace/list')) body = { workspaces: [] }
      return route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) })
    })
    await page.goto('https://tasks.test/')
    await page.addStyleTag({ content: css })
    await page.addScriptTag({ content: bundle.outputFiles[0].text })
    await page.getByTestId('filter-worker-tasks').waitFor()
    await page.getByText('Scheduled audit', { exact: true }).waitFor()
    assert.equal(await page.getByText('Manual review', { exact: true }).count(), 1)
    assert.equal(await page.getByText('Scheduled backup', { exact: true }).count(), 1)
    assert.equal(await page.getByTestId('worker-link').filter({ hasText: 'Daily Audit' }).count(), 2)
    const links = await page.getByTestId('worker-link').evaluateAll(nodes => nodes.map(node => node.getAttribute('href')))
    assert.ok(links.some(link => link?.includes('worker_alpha')))
    assert.ok(links.some(link => link?.includes('worker_beta')))
    if (process.env.SWARM_TEST_SCREENSHOT_DIR) {
      await mkdir(process.env.SWARM_TEST_SCREENSHOT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(process.env.SWARM_TEST_SCREENSHOT_DIR, 'worker-tasks-all.png'), fullPage: true })
    }
    await page.getByTestId('filter-worker-tasks').click()
    assert.equal(await page.getByText('Manual review', { exact: true }).count(), 0)
    assert.equal(await page.getByText('Scheduled audit', { exact: true }).count(), 1)
    assert.equal(await page.getByText('Scheduled backup', { exact: true }).count(), 1)
    assert.equal(await page.getByTestId('filter-worker-tasks').getAttribute('aria-pressed'), 'true')
    assert.equal(await page.getByTestId('filter-all-tasks').getAttribute('aria-pressed'), 'false')
    await page.getByTestId('filter-all-tasks').evaluate(node => node.getAnimations().forEach(animation => animation.finish()))
    await page.getByTestId('filter-worker-tasks').evaluate(node => node.getAnimations().forEach(animation => animation.finish()))
    if (process.env.SWARM_TEST_SCREENSHOT_DIR) await page.screenshot({ path: path.join(process.env.SWARM_TEST_SCREENSHOT_DIR, 'worker-tasks-filtered.png'), fullPage: true })
    await page.getByPlaceholder('Search tasks by title, worker, or tag...').fill('worker_alpha')
    assert.equal(await page.getByText('Scheduled backup', { exact: true }).count(), 0)
    assert.equal(await page.getByText('Scheduled audit', { exact: true }).count(), 1)
    await page.getByPlaceholder('Search tasks by title, worker, or tag...').fill('')
    tasks.push({ id: 'task_new', title: 'New generated task', agent: 'swarm', worker_id: 'worker_alpha', worker_name: 'Daily Audit', worker_run_id: 'run_new', status: 'queued', created_at: 4 })
    await page.evaluate(() => (window as any).refreshTasks())
    await page.getByText('New generated task', { exact: true }).waitFor()
    await page.getByTestId('filter-all-tasks').click()
    assert.equal(await page.getByText('Manual review', { exact: true }).count(), 1)
    assert.equal(await page.getByText(/Active Project Automations|Manage Fleet in Workers Hub|No background automations running/).count(), 0)
    assert.deepEqual(errors, [])
  } finally { await browser.close() }
})
