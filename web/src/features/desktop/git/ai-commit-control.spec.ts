import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const controlURL = new URL('./ai-commit-control.tsx', import.meta.url)
const pageURL = new URL('../layout/desktop-app-page.tsx', import.meta.url)
const sidebarURL = new URL('../settings/actions/components/workspace-actions-sidebar-section.tsx', import.meta.url)
const settingsURL = new URL('../settings/actions/components/actions-settings-page.tsx', import.meta.url)
const iconsURL = new URL('../settings/actions/components/workspace-action-icons.tsx', import.meta.url)

test('Git sidebar keeps plain commit controls while Workspace Actions own the commit-plus-Action menu', async () => {
  const [control, page] = await Promise.all([readFile(controlURL, 'utf8'), readFile(pageURL, 'utf8')])
  const sidebarStart = page.indexOf('const planSidebarGitPanel =')
  const sidebarEnd = page.indexOf('const focusedSidebarContent =', sidebarStart)
  const sidebar = page.slice(sidebarStart, sidebarEnd)

  assert.match(sidebar, /data-plan-git-header/)
  assert.match(sidebar, /data-plan-git-action-row data-plan-git-commit/)
  assert.match(sidebar, /<AICommitButton compact/)
  assert.match(sidebar, /aria-label="Commit changes"/)
  assert.doesNotMatch(sidebar, /GitActionFlowControl|pinned-git-flows/)
  assert.doesNotMatch(control, /GitActionFlowControl|PINNED_GIT_FLOWS_STORAGE_KEY|WorkspaceAction/)
})

test('Workspace Actions render directly after Git from canonical pinned definitions', async () => {
  const [page, sidebar] = await Promise.all([readFile(pageURL, 'utf8'), readFile(sidebarURL, 'utf8')])
  assert.match(page, /<\/section>\s*<WorkspaceActionsSidebarSection workspacePath=\{selectedGitWorkspacePath\}/)
  assert.match(sidebar, /orderWorkspaceActionsForQuickAccess\(actions\)\.filter\(\(action\) => action\.pinned\)/)
  assert.match(sidebar, /data-pinned-workspace-actions/)
  assert.match(sidebar, /<WorkspaceActionIcon icon=\{action\.icon\}/)
  assert.match(sidebar, /onClick=\{\(\) => onRun\(action\)\}/)
  assert.match(sidebar, /data-plan-section-treatment="integrated"/)
  assert.match(sidebar, /className="mt-3 shrink-0 border-t border-\[var\(--app-border\)\] pt-3"/)
  assert.doesNotMatch(sidebar, /app-primary-border|app-primary-soft/)
})

test('quick manager reuses the full Settings Actions management implementation', async () => {
  const [sidebar, settings] = await Promise.all([readFile(sidebarURL, 'utf8'), readFile(settingsURL, 'utf8')])
  assert.match(sidebar, /<ActionsSettingsPage[\s\S]*compact[\s\S]*onRun=/)
  assert.match(settings, /data-actions-management-surface=\{compact \? 'compact' : 'full'\}/)
  assert.match(settings, /saveWorkspaceAction/)
  assert.match(settings, /deleteWorkspaceAction/)
  assert.match(settings, /reorderWorkspaceActions/)
  assert.match(settings, /Action saved\. Nothing was executed\./)
  assert.match(settings, /onClick=\{\(\) => runAction\(action\)\}/)
  assert.match(settings, /<DesktopWorkspaceActionPanel/)
})

test('shared icon picker is visual and unknown values use a deterministic fallback', async () => {
  const [settings, icons] = await Promise.all([readFile(settingsURL, 'utf8'), readFile(iconsURL, 'utf8')])
  assert.match(settings, /<WorkspaceActionIconPicker/)
  assert.doesNotMatch(settings, /Icon name<Input/)
  assert.match(icons, /DEFAULT_WORKSPACE_ACTION_ICON = 'zap'/)
  assert.match(icons, /normalizeWorkspaceActionIcon/)
  assert.match(icons, /data-workspace-action-icon-picker/)
  assert.match(icons, /aria-pressed=\{selected\}/)
})

test('Workspace Action menu offers explicit AI Commit then run orchestration', async () => {
  const [page, sidebar] = await Promise.all([readFile(pageURL, 'utf8'), readFile(sidebarURL, 'utf8')])
  const workflowStart = page.indexOf('const handleAICommitWorkspaceAction = async')
  const workflowEnd = page.indexOf('const handleAICommit = async', workflowStart)
  const workflow = page.slice(workflowStart, workflowEnd)

  assert.match(sidebar, /AI Commit, then run/)
  assert.match(sidebar, /onAICommitRun\(action\)/)
  assert.match(page, /onAICommitRun=\{\(action\) => openAICommitWorkspaceAction/)
  assert.match(page, /launchLabel=\{workspaceActionPresentation\.mode === 'ai-commit' \? 'AI Commit, then run'/)
  assert.ok(workflow.indexOf('await commitWorkspaceChanges') < workflow.indexOf('await startWorkspaceAction'), 'the Action must start only after the commit resolves')
  assert.match(workflow, /mode: 'post-commit'/)
  assert.match(workflow, /throw error/)
})

test('standalone Workspace Action execution remains in the existing execution panel', async () => {
  const page = await readFile(pageURL, 'utf8')
  assert.match(page, /<DesktopWorkspaceActionPanel[\s\S]*action=\{workspaceActionPresentation\.action\}/)
  assert.match(page, /mode: 'standalone'/)
  assert.match(page, /onLaunch=\{workspaceActionPresentation\.mode === 'ai-commit'/)
})
