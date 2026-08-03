import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const controlURL = new URL('./ai-commit-control.tsx', import.meta.url)
const pageURL = new URL('../layout/desktop-app-page.tsx', import.meta.url)

test('Git sidebar separates its identity header from the dedicated flow and commit control row', async () => {
  const source = await readFile(pageURL, 'utf8')
  const sidebarStart = source.indexOf('const planSidebarGitPanel =')
  const sidebarEnd = source.indexOf('const focusedSidebarContent =', sidebarStart)
  const sidebar = source.slice(sidebarStart, sidebarEnd)

  assert.ok(sidebarStart >= 0 && sidebarEnd > sidebarStart)
  assert.match(sidebar, /data-plan-git-header/)
  assert.match(sidebar, /data-plan-git-action-row data-plan-git-commit/)
  assert.ok(sidebar.indexOf('data-plan-git-header') < sidebar.indexOf('data-plan-git-action-row'))
  assert.match(sidebar, /<GitActionFlowControl compact/)
  assert.match(sidebar, /<AICommitButton compact/)
  assert.match(sidebar, /aria-label="Commit changes"[^\n]*<Save size=\{14\}/)
  assert.doesNotMatch(sidebar, /<AICommitControl/)
})

test('AI Commit and persistent Actions are distinct, quiet controls with the plus leading the row', async () => {
  const source = await readFile(controlURL, 'utf8')
  const actionsButtonStart = source.indexOf('data-git-actions-button')
  const actionsButtonEnd = source.indexOf('</button>', actionsButtonStart)
  const actionsButton = source.slice(actionsButtonStart, actionsButtonEnd)
  const aiButtonStart = source.indexOf('data-ai-commit-button')
  const aiButtonEnd = source.indexOf('</button>', aiButtonStart)
  const aiButton = source.slice(aiButtonStart, aiButtonEnd)

  assert.match(source, /export function AICommitButton/)
  assert.match(aiButton, /onClick=\{onGenerate\}/)
  assert.doesNotMatch(aiButton, /border-\[var\(--app-primary\)\]|text-\[var\(--app-primary\)\]/)
  assert.match(source, /export function GitActionFlowControl/)
  assert.match(source, /data-git-action-flow-control/)
  assert.match(source, /aria-label="Open workspace Actions and flows"/)
  assert.doesNotMatch(actionsButton, /border-\[var\(--app-primary\)\]|text-\[var\(--app-primary\)\]/)
  assert.match(source, /data-pinned-git-flows/)
  assert.ok(actionsButtonStart < source.indexOf('data-pinned-git-flows'), 'the plus control should lead the pinned Git flow row')
})

test('Actions popup independently runs Actions and starts named standalone or AI Commit combo pins', async () => {
  const source = await readFile(controlURL, 'utf8')

  assert.match(source, /role="menu" aria-label="Workspace Actions and flows"/)
  assert.match(source, /Run an Action now, pin it, or commit changes before it runs\./)
  assert.match(source, /if \(flow\.kind === 'action'\) \{[\s\S]*onActionRun\(action\)/)
  assert.match(source, /aria-pressed=\{actionPinned\}[\s\S]*beginPin\(actionFlow\)/)
  assert.match(source, /aria-pressed=\{comboPinned\}[\s\S]*beginPin\(comboFlow\)/)
  assert.match(source, /kind: 'action', actionId: action\.id/)
  assert.match(source, /kind: 'ai-commit-action', actionId: action\.id/)
  assert.match(source, /savePinnedGitFlows\(workspacePath, next\)/)
  assert.match(source, /swarm\.web\.desktop\.git\.pinned-flows\.v1/)
})

test('pinning requires an inline name and displays that saved name in the sidebar', async () => {
  const source = await readFile(controlURL, 'utf8')

  assert.match(source, /data-pin-name-editor/)
  assert.match(source, /aria-label="Pinned flow name"/)
  assert.match(source, /onSubmit=\{\(event\) => \{ event\.preventDefault\(\); confirmPin\(\) \}\}/)
  assert.match(source, /disabled=\{!draftForAction\.name\.trim\(\)\}/)
  assert.match(source, /if \(!name\) return/)
  assert.match(source, /\{ \.\.\.pinDraft\.flow, name \}/)
  assert.match(source, /const displayName = flowDisplayName\(flow, action\)/)
  assert.match(source, /<span className="max-w-28 truncate">\{displayName\}<\/span>/)
})

test('pinned AI Commit combos collect inputs and route through commit-first orchestration', async () => {
  const control = await readFile(controlURL, 'utf8')
  const page = await readFile(pageURL, 'utf8')
  const workflowStart = page.indexOf('const handleAICommit = async')
  const workflowEnd = page.indexOf('const openGitCommitReview', workflowStart)
  const workflow = page.slice(workflowStart, workflowEnd)
  const commitCall = workflow.indexOf('await commitWorkspaceChanges')
  const actionCall = workflow.indexOf('await startWorkspaceAction')

  assert.match(control, /data-ai-commit-action-inputs/)
  assert.match(control, /onAICommitActionRun\(configuredAction, inputValues\)/)
  assert.match(page, /onAICommitActionRun=\{\(action, inputs\) => \{ void handleAICommit\([^\n]*\{ action, inputs \}\) \}\}/)
  assert.match(workflow, /const selectedAction = flow\?\.action \?\? /)
  assert.match(workflow, /const selectedActionInputs = flow\?\.inputs \?\? /)
  assert.ok(commitCall >= 0 && actionCall > commitCall, 'the chosen Action must start only after commit resolves')
  assert.doesNotMatch(workflow.slice(0, commitCall), /startWorkspaceAction/)
})

test('standalone pinned Actions remain runnable when the worktree is clean', async () => {
  const source = await readFile(pageURL, 'utf8')
  const sidebarStart = source.indexOf('const planSidebarGitPanel =')
  const sidebarEnd = source.indexOf('const focusedSidebarContent =', sidebarStart)
  const sidebar = source.slice(sidebarStart, sidebarEnd)

  assert.match(sidebar, /<GitActionFlowControl compact workspacePath=\{selectedGitWorkspacePath\} canAICommit=\{gitSnapshot\.files\.length > 0\}/)
  assert.match(sidebar, /onActionRun=\{openWorkspaceAction\}/)
  assert.match(sidebar, /\{gitSnapshot\.files\.length > 0 \? <>[\s\S]*<AICommitButton compact/)
  assert.doesNotMatch(sidebar, /gitSnapshot\.files\.length > 0 \? \([\s\S]*<GitActionFlowControl/)
})
