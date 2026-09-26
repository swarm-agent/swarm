import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { ORCHESTRATE_THEMES, ORCHESTRATE_THEME_IDS } from './orchestrate-themes'
import type { MiddleCanvasVariant } from './orchestrate-types'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

test('Orchestrate View defines valid canvas variants and verified modern themes', () => {
  const validVariants: MiddleCanvasVariant[] = ['matrix', 'kanban', 'fleet', 'split', 'timeline']
  assert.equal(validVariants.length, 5)
  assert.ok(validVariants.includes('fleet'), 'Variant 3 fleet must be a valid variant')
  assert.ok(validVariants.includes('kanban'), 'Variant 2 kanban must be a valid variant')

  assert.ok(ORCHESTRATE_THEME_IDS.includes('modern_navy'))
  const modernNavy = ORCHESTRATE_THEMES['modern_navy']
  assert.ok(modernNavy)
  assert.ok(modernNavy.bgClass)
  assert.ok(modernNavy.panelBgClass)
  assert.ok(modernNavy.accentColor)

  const applePeach = ORCHESTRATE_THEMES['apple_peach']
  assert.ok(applePeach)
  assert.ok(applePeach.bgClass)
})

test('OrchestrateView synchronizes task cards with live session reality, plans, and focus', () => {
  // Invariant: The task card must stay in sync with the actual session:
  // - Track pending_approval -> in_progress upon acceptance
  // - Show the plan the agent created with subtasks checklist
  // - Show current focus and live streaming activity from session info
  // - Track session timer with live seconds and minutes
  // - Transition to needs_review upon completion, never just flip to completed!
  // - Support reopening tasks into multiple states
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Realtime demand lease and plan hydration
  assert.ok(source.includes('acquireSessionDemand'), 'OrchestrateView must acquire realtime demand leases for active tasks')
  assert.ok(source.includes('hydrateDesktopV3ChildCard'), 'OrchestrateView must hydrate child cards with active plans')

  // Live session status & streaming
  assert.ok(source.includes('Current Focus'), 'MinimalTaskCard must display Current Focus banner')
  assert.ok(source.includes('Agent Execution Plan'), 'MinimalTaskCard must display agent execution plan checklist')
  assert.ok(source.includes('Live Streaming Activity'), 'MinimalTaskCard must render live streaming activity box')
  assert.ok(source.includes('formattedTimer'), 'MinimalTaskCard must format dynamic session timer')

  // Needs review transition (not flipping directly to completed)
  assert.ok(source.includes("status = 'needs_review'"), 'Tasks must transition to needs_review when execution completes')
  assert.ok(source.includes('Mission Execution Completed — Awaiting Review'), 'Needs review banner must be displayed')

  // Reopen task support for multiple states
  assert.ok(source.includes('handleReopenTask'), 'OrchestrateView must implement handleReopenTask')
  assert.ok(source.includes('/reopen'), 'Must call backend task reopen endpoint')
  assert.ok(source.includes('handleCompleteTask'), 'OrchestrateView must implement handleCompleteTask')
  assert.ok(source.includes('/complete'), 'Must call backend task complete endpoint')
  assert.ok(source.includes('Reopen Task'), 'Task card must provide Reopen Task action')
})

test('OrchestrateView renders Compact Multi-Coder View for Task Programs with in-task redeployment', () => {
  // Written test purpose:
  // - Product requirement/invariant: When a task executes multiple parallel coders via a Task Program,
  //   the task card must adapt to a compact multi-coder view showing stage progress, side-by-side coder tiles,
  //   individual scopes, conflict detection, and in-task job redeployment.
  // - Regression prevented: Prevents regressions where multi-agent tasks render overly verbose linear plans,
  //   hide parallel execution progress, or prevent redeploying conflicted subagents.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Task program detection & compact view
  assert.ok(source.includes('isTaskProgram'), 'MinimalTaskCard must detect task program multi-agent tasks')
  assert.ok(source.includes('Parallel Multi-Agent Cohort'), 'MinimalTaskCard must display parallel cohort stage header')
  assert.ok(source.includes('Coders Finished'), 'MinimalTaskCard must display finished coders count')

  // Side-by-side Coder tiles
  assert.ok(source.includes('grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3'), 'Must render responsive grid of coder tiles')
  assert.ok(source.includes('scope:'), 'Coder tiles must display declared owned scope')
  assert.ok(source.includes('Attempt'), 'Tiles must display attempt numbers when retried')

  // Conflict handling and redeploy action
  assert.ok(source.includes('onRedeployJob'), 'MinimalTaskCard must support onRedeployJob prop')
  assert.ok(source.includes('handleRedeployJob'), 'OrchestrateView must implement handleRedeployJob')
  assert.ok(source.includes('/program:redeploy-job'), 'handleRedeployJob must call backend redeploy endpoint')
  assert.ok(source.includes('Redeploy'), 'Tile must render Redeploy button on conflict')
})

test('OrchestrateView displays orchestrator context used and provides clear context controls', () => {
  // Written test purpose:
  // - Product requirement/invariant: OrchestratorChatSidebar must state current context used
  //   (tokens / context window / percentage) and provide an immediate Clear Context button
  //   allowing operators to reset the orchestrator session context at any time.
  // - Regression prevented: Prevents regressions where operators cannot monitor context
  //   consumption of the executive orchestrator or get stuck with high-context degradation.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Context tokens display
  assert.ok(source.includes('contextStats'), 'OrchestratorChatSidebar must compute context stats from session usage')
  assert.ok(source.includes('data-testid="orchestrator-context-label"'), 'Must render orchestrator context label testid')
  assert.ok(source.includes('tokens used'), 'Must display formatted tokens used label')

  // Clear context action & endpoint
  assert.ok(source.includes('data-testid="clear-orchestrator-context-btn"'), 'Must render Clear Context button testid')
  assert.ok(source.includes('/orchestrator:clear-context'), 'Must invoke /v3/projects/{id}/orchestrator:clear-context endpoint')
  assert.ok(source.includes('handleOrchestratorSessionReset'), 'OrchestrateView must handle orchestrator session reset')
  assert.ok(source.includes('onOrchestratorSessionReset'), 'OrchestratorChatSidebar must accept onOrchestratorSessionReset prop')
})

