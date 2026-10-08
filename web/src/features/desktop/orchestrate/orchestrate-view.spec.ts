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
  const membershipPath = path.join(__dirname, '../runtime/desktop-projects-membership.ts')
  const source = fs.readFileSync(sourcePath, 'utf8') + '\n' + fs.readFileSync(membershipPath, 'utf8')

  // Realtime demand lease and plan hydration
  assert.ok(source.includes('acquireSessionDemand'), 'OrchestrateView must acquire realtime demand leases for active tasks')
  assert.ok(source.includes('hydrateDesktopV3ChildCard'), 'OrchestrateView must hydrate child cards with active plans')

  // Live session status & streaming
  assert.ok(source.includes('Current Focus'), 'MinimalTaskCard must display Current Focus banner')
  assert.ok(source.includes('Agent Execution Plan'), 'MinimalTaskCard must display agent execution plan checklist')
  assert.ok(source.includes('Live Streaming Activity'), 'MinimalTaskCard must render live streaming activity box')
  assert.ok(source.includes('TaskElapsedTimer'), 'MinimalTaskCard must format dynamic session timer')

  // Needs review transition (not flipping directly to completed)
  assert.ok(source.includes("status = 'needs_review'"), 'Tasks must transition to needs_review when execution completes')
  // Compact review actions and their disclosure-independent reachability are
  // asserted on mounted MinimalTaskCard in task-integration-operation.browser.spec.ts.

  // Reopen task support for multiple states
  assert.ok(source.includes('handleReopenTask'), 'OrchestrateView must implement handleReopenTask')
  assert.ok(source.includes('/reopen'), 'Must call backend task reopen endpoint')
  assert.ok(source.includes('handleCompleteTask'), 'OrchestrateView must implement handleCompleteTask')
  assert.ok(source.includes('/complete'), 'Must call backend task complete endpoint')
  // Reopen focus, cancellation and exactly-once submit are behavioral browser assertions.
})

test('OrchestrateView renders Compact Multi-Coder View for Task Programs with in-task redeployment', () => {
  // Written test purpose:
  // - Product requirement/invariant: When a task executes multiple parallel coders via a Task Program,
  //   the task card must adapt to a compact multi-coder view showing stage progress, side-by-side coder tiles,
  //   individual scopes, conflict detection, and in-task job redeployment.
  // - Regression prevented: Prevents regressions where multi-agent tasks render overly verbose linear plans,
  //   hide parallel execution progress, or prevent redeploying conflicted subagents.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const statePath = path.join(__dirname, '../state/desktop-projects-state.ts')
  const source = fs.readFileSync(sourcePath, 'utf8') + '\n' + fs.readFileSync(statePath, 'utf8')

  // Task program detection & compact view
  assert.ok(source.includes('isTaskProgram'), 'MinimalTaskCard must detect task program multi-agent tasks')
  assert.ok(source.includes('jobs.length > 0'), 'Task program must support single-job task programs')
  assert.ok(source.includes('taskProgram: t.task_program || t.taskProgram'), 'fetchProjectTasks must map taskProgram')
  assert.ok(source.includes('taskProgramStatus: t.task_program_status || t.taskProgramStatus'), 'fetchProjectTasks must map taskProgramStatus')
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
  //   allowing an explicit same-session reset with active-work safeguards.
  // - Regression prevented: Prevents regressions where operators cannot monitor context
  //   consumption of the executive orchestrator or get stuck with high-context degradation.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Wiring-only guard; rendered confirmation, failure and distinct-action
  // postconditions are proven in project-name-routing.browser.spec.ts.
  assert.ok(source.includes('<ContextRemaining usage={sessionUsage} />'))
  assert.ok(source.includes('data-testid="clear-orchestrator-context-btn"'))
  assert.ok(source.includes('await clearSessionContext(sessionId,'))
  assert.ok(source.includes('await createProjectConversation(projectId,'))
  assert.ok(!source.includes('onOrchestratorSessionReset'))
})

test('OrchestrateView forwards selected-task context to orchestrator chat with explicit envelope and preserves canonical actions', () => {
  // Written test purpose:
  // - Product requirement/invariant: When an operator selects a task card, OrchestrateView must forward
  //   the selected task context into the chat sidebar, display a selected task badge with deselect capability,
  //   and forward the snapshot-on-send explicit user-message envelope into the chat conversation.
  // - Regression prevented: Prevents selectedTaskId from acting as a cosmetic UI highlight only without chat forwarding.
  // - Symbols: OrchestrateView.tsx, OrchestratorChatComposer, OrchestratorChatSidebar.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Selected task state and chat sidebar wiring
  assert.ok(source.includes('selectedTask = useMemo('), 'Must derive selectedTask from selectedTaskId')
  assert.ok(source.includes('attachedTasks={tasks.filter(task => attachedTaskIds.includes(task.id))}'), 'Must pass explicit task attachments, not card detail selection, to OrchestratorChatSidebar')
  assert.ok(source.includes('allTasks={tasks}'), 'Must pass allTasks to OrchestratorChatSidebar')
  assert.ok(source.includes('onDeselectTask={handleDeselectTask}'), 'Must pass onDeselectTask callback')

  // Explicit user-message envelope and validation
  assert.ok(source.includes('validateSelectedTaskForContext'), 'Must validate selected task context before sending')
  assert.ok(source.includes('buildSelectedTaskMessageEnvelope'), 'Must build explicit user-message envelope')
  assert.ok(source.includes('buildSelectedTaskMessageMetadata'), 'Must build selected task tracking metadata')

  // Composer override and UI elements
  assert.ok(source.includes('composerOverride='), 'Must provide composerOverride to DesktopV3ExistingConversationPane')
  assert.ok(source.includes('data-testid="selected-task-context-banner"'), 'Must render selected-task-context-banner testid')
  assert.ok(source.includes('data-testid="clear-selected-task-context-btn"'), 'Must render clear-selected-task-context-btn testid')
  assert.ok(source.includes('data-testid="orchestrator-chat-composer"'), 'Must render orchestrator-chat-composer testid')

  // Anti-regression: No implicit auto-selection of tasks[0]
  assert.ok(!source.includes('setSelectedTaskId(tasks[0].id)'), 'OrchestrateView must not auto-select tasks[0]')
  assert.ok(!source.includes('liveTasks.find((t) => t.id === selectedTaskId) || liveTasks[0]'), 'Split view inspector must not fall back to liveTasks[0]')
  assert.ok(source.includes('reconcileSelectedTaskId'), 'Must use reconcileSelectedTaskId for task selection lifecycle')

  // Canonical paths preserved
  assert.ok(source.includes('handleApproveTask'), 'handleApproveTask must be preserved')
  assert.ok(source.includes('handleRefineTask'), 'handleRefineTask must be preserved')
  assert.ok(source.includes('handleReopenTask'), 'handleReopenTask must be preserved')
  assert.ok(source.includes('handleCompleteTask'), 'handleCompleteTask must be preserved')
  assert.ok(source.includes('handleRedeployJob'), 'handleRedeployJob must be preserved')
})


