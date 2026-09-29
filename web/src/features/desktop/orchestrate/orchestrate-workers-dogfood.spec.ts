import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

test('OrchestrateView displays active running automations at top of project with click-through to Workers Hub', () => {
  const orchestrateSourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(orchestrateSourcePath, 'utf8')

  // Invariant 1: Top of project must render active project automations
  assert.ok(source.includes('Active Project Automations'), 'OrchestrateView must show Active Project Automations ticker at top of project')
  assert.ok(source.includes('Manage Fleet in Workers Hub') || source.includes('Explore Workers Hub'), 'OrchestrateView must link to Workers Hub from top ticker')

  // Invariant 2: Clicking an automation or link switches to Workers Hub
  assert.ok(source.includes("setActiveNavTab('workers')"), 'OrchestrateView must support 1-click navigation to Workers Hub')

  // Invariant 3: Workers Hub tab has a single canonical render path, not the retired fleet.
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

// Requirement: Orchestrator proposed durable workers appear in regular Tasks view worker section
// as an expandable PendingWorkerCard with exact intent and granular worker detail navigation.
// Threat: durable workers only appear in the standalone hub, stranding human acceptance in regular view.
test('OrchestrateView integrates TasksDurableWorkersSection and PendingWorkerCard in Tasks view', () => {
  const source = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  assert.ok(source.includes('TasksDurableWorkersSection'), 'OrchestrateView must define and render TasksDurableWorkersSection')
  const activity = fs.readFileSync(path.join(__dirname, 'worker-task-activity.tsx'), 'utf8')
  assert.ok(source.includes('<WorkerTaskActivity'))
  assert.ok(activity.includes('<PendingWorkerCard'), 'Pending proposals retain human acceptance')
  assert.ok(activity.includes('<WorkerRunHistory'), 'Accepted workers expose actual runs')
  assert.ok(!activity.includes("lifecycleState: 'pending'"), 'Accepted workers must not disappear')
  assert.ok(source.includes('swarmWorkerLink'), 'OrchestrateView must link to worker detail via canonical route')
})
