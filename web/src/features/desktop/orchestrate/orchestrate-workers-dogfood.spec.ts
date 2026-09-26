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

  // Invariant 3: Workers Hub tab renders registered automations fleet
  assert.ok(source.includes('Registered Workers Fleet'), 'OrchestrateView must render Registered Workers Fleet in Workers Hub')
})

test('DesktopAppPage delists old workers menu and provides Chat vs Swarm mode switch', () => {
  const desktopAppSourcePath = path.join(__dirname, '../layout/desktop-app-page.tsx')
  const source = fs.readFileSync(desktopAppSourcePath, 'utf8')

  // Invariant 1: Chat vs Swarm mode switch exists in sidebar
  assert.ok(source.includes('Switch to Chat Mode'), 'DesktopAppPage must provide Chat Mode switch')
  assert.ok(source.includes('Switch to Swarm Orchestrate Mode'), 'DesktopAppPage must provide Swarm Orchestrate Mode switch')

  // Invariant 2: Old Workers sidebar button is delisted
  assert.ok(!source.includes('title="Workers"'), 'DesktopAppPage must not render old standalone Workers button in sidebar')

  // Invariant 3: Automations link routes to orchestrate
  assert.ok(source.includes("void navigate({ to: '/$workspaceSlug/orchestrate'"), 'DesktopAppPage onOpenAutomations must navigate to orchestrate')
})

test('Router redirects legacy workers routes to orchestrate', () => {
  const routerSourcePath = path.join(__dirname, '../../../app/router.tsx')
  const source = fs.readFileSync(routerSourcePath, 'utf8')

  // Invariant 1: /workers redirects to /orchestrate
  assert.ok(source.includes("to: '/orchestrate'"), 'Router must redirect /workers to /orchestrate')

  // Invariant 2: /$workspaceSlug/workers redirects to /$workspaceSlug/orchestrate
  assert.ok(source.includes("to: '/$workspaceSlug/orchestrate'"), 'Router must redirect /$workspaceSlug/workers to /$workspaceSlug/orchestrate')
})

test('OrchestrateView renders Scoped Project Workspaces for pending worker proposals with suggest changes capability', () => {
  const orchestrateSourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(orchestrateSourcePath, 'utf8')

  // Invariant 1: Pending worker proposals display Scoped Project Workspaces
  assert.ok(source.includes('Scoped Project Workspaces'), 'Must render Scoped Project Workspaces section')

  // Invariant 2: Displays Suggest Changes / Edit Workspaces button
  assert.ok(source.includes('Suggest Changes / Edit Workspaces'), 'Must provide Suggest Changes / Edit Workspaces button')

  // Invariant 3: Interactive workspace selection allows updating workspace scope via mutation
  assert.ok(source.includes('handleUpdateWorkerWorkspaceScope'), 'Must implement handleUpdateWorkerWorkspaceScope handler')
  assert.ok(source.includes('Apply Workspace Scope'), 'Must provide Apply Workspace Scope button')

  // Invariant 4: Displays pre-configured worker pipeline checkpoints if present
  assert.ok(source.includes('Pre-Configured Worker Pipeline'), 'Must display planned pipeline checkpoints')
})
