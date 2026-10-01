import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

test('OrchestrateView excludes legacy Active Project Automations panel and links to canonical Workers Hub', () => {
  const orchestrateSourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(orchestrateSourcePath, 'utf8')

  // Invariant 1: Top of project must NOT render legacy Active Project Automations ticker/cards
  assert.ok(!source.includes('Active Project Automations'), 'OrchestrateView must NOT show Active Project Automations ticker at top of project')
  assert.ok(!source.includes('Manage Fleet in Workers Hub'), 'OrchestrateView must not link to legacy fleet from top ticker')

  // Invariant 2: Workers Hub tab has a single canonical render path, not the retired fleet.
  assert.ok(source.includes('<WorkerHub workspaceSlug={workspaceSlug} initialWorkerId={workerDetailId || routeWorkerId}'))
  assert.ok(source.includes('onAddWorker={() =>'))
  assert.ok(!source.includes("false && activeNavTab === 'workers'"))
  assert.ok(!source.includes('Registered Workers Fleet'))
})

// Requirement: Desktop navigation must target the canonical Swarm page and
// Workers section, never the retired orchestrate URL. These source assertions
// check registration/caller wiring only; browser history needs a rendered test.
test('DesktopAppPage delists old workers menu and provides Chat vs Swarm mode switch', () => {
  const desktopAppSourcePath = path.join(__dirname, '../layout/desktop-app-page.tsx')
  const source = fs.readFileSync(desktopAppSourcePath, 'utf8')

  // Invariant 1: Chat vs Swarm mode switch exists in sidebar
  assert.ok(source.includes('Switch to Chat Mode'), 'DesktopAppPage must provide Chat Mode switch')
  assert.ok(source.includes('Switch to Swarm Orchestrate Mode'), 'DesktopAppPage must provide Swarm Orchestrate Mode switch')

  // Invariant 2: Old Workers sidebar button is delisted
  assert.ok(!source.includes('title="Workers"'), 'DesktopAppPage must not render old standalone Workers button in sidebar')

  // Invariant 3: Automations links open the addressable Workers section.
  assert.ok(source.includes("void navigate({ to: '/$workspaceSlug/swarm/$swarmSection'"), 'DesktopAppPage onOpenAutomations must navigate to Swarm Workers')
  assert.ok(source.includes("swarmSection: 'workers'"))
  assert.ok(!source.includes('/orchestrate\''), 'Navigation must not emit retired URLs')
})

// Requirement: router.tsx redirects legacy worker entry points into the
// canonical Workers section without confusing them with durable session URLs.
test('Router redirects legacy workers routes to Swarm Workers', () => {
  const routerSourcePath = path.join(__dirname, '../../../app/router.tsx')
  const source = fs.readFileSync(routerSourcePath, 'utf8')

  assert.ok(source.includes("to: '/swarm/$swarmSection'"), 'Global workers redirect must target a Swarm section')
  assert.ok(source.includes("to: '/$workspaceSlug/swarm/$swarmSection'"), 'Workspace workers redirect must retain workspace scope')
  assert.ok(source.includes("params: { swarmSection: 'workers' }"))
  assert.ok(source.includes("params: { workspaceSlug: params.workspaceSlug, swarmSection: 'workers' }"))
})

// Requirement: pending legacy automation reviews retain an explicit decision path
// on the project page while the worker hub itself has only the durable authority.
// Threat: removal of dead fleet JSX accidentally hiding pending permissions.
// This checks caller wiring; rendered actions require a browser fixture.
test('project page retains pending review actions outside canonical worker hub', () => {
  const source = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  assert.ok(source.includes('Pending Worker Proposal for'))
  assert.ok(source.includes('decidePendingWorkerReview(getDesktopV3CacheSnapshot()'))
  assert.ok(source.includes('role="alert" className="text-xs text-red-300"'))
  assert.ok(!source.includes('listStorageWorkers'))
  assert.ok(!source.includes('handleUpdateWorkerWorkspaceScope'))
})

// Requirement: Tasks view shows worker-generated automation tasks as ordinary
// entries in the SAME project task list without separate worker overview panels.
// Standalone TasksDurableWorkersSection and WorkerTaskActivity in Tasks view are removed.
test('OrchestrateView excludes separate worker overview panels and renders All tasks / Worker tasks filter', () => {
  const source = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  // Invariant 1: No standalone worker overview boxes/panels in Tasks view
  assert.ok(!source.includes('TasksDurableWorkersSection'), 'OrchestrateView must NOT define or render TasksDurableWorkersSection')
  assert.ok(!source.includes('<WorkerTaskActivity'), 'OrchestrateView must NOT render WorkerTaskActivity in Tasks view')

  // Invariant 2: Tasks view has All tasks vs Worker tasks filter based on durable worker_id
  const toolbar = fs.readFileSync(path.join(__dirname, 'task-list-toolbar.tsx'), 'utf8')
  assert.ok(source.includes('<TaskListToolbar'), 'OrchestrateView must render the scope toolbar')
  assert.ok(toolbar.includes("'filter-all-tasks'"), 'Toolbar must provide All scope filter')
  assert.ok(toolbar.includes("'filter-worker-tasks'"), 'Toolbar must provide Workers scope filter')

  // Invariant 3: Worker-generated tasks render exact worker tag and canonical link
  assert.ok(source.includes('data-testid="worker-tag"'), 'OrchestrateView must render worker-tag for worker-generated tasks')
  assert.ok(source.includes('data-testid="worker-link"'), 'OrchestrateView must render worker-link for worker-generated tasks')
  assert.ok(source.includes('data-testid="worker-task-spec"'), 'OrchestrateView must render worker-task-spec for task details')
  assert.ok(source.includes('swarmWorkerLink'), 'OrchestrateView must link to worker detail via canonical route')
  assert.ok(source.includes('swarmWorkerHref'), 'OrchestrateView must use canonical worker href')
})
