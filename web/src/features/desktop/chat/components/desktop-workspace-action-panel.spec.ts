import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const panelURL = new URL('./desktop-workspace-action-panel.tsx', import.meta.url)
const pageURL = new URL('../../layout/desktop-app-page.tsx', import.meta.url)
const settingsURL = new URL('../../settings/actions/components/actions-settings-page.tsx', import.meta.url)

test('workspace Action panel can foreground an existing run without launching a duplicate', async () => {
  const source = await readFile(panelURL, 'utf8')

  assert.match(source, /initialRun\?: WorkspaceActionRun \| null/)
  assert.match(source, /useState<WorkspaceActionRun \| null>\(initialRun\)/)
  assert.match(source, /if \(!initialRun\) return/)
  assert.match(source, /if \(!run \|\| run\.status !== 'running'\) return/)
  assert.match(source, /fetchWorkspaceActionRun\(workspacePath, run\.id, controller\.signal, sessionId\)/)
  assert.match(source, /onRunChangeRef\.current\?\.\(next\)/)
})

test('Action definitions and execution remain separate surfaces', async () => {
  const [panel, page, settings] = await Promise.all([
    readFile(panelURL, 'utf8'),
    readFile(pageURL, 'utf8'),
    readFile(settingsURL, 'utf8'),
  ])

  assert.match(page, /sessionId=\{workspaceActionPresentation\.sessionId\}/)
  assert.match(page, /action=\{workspaceActionPresentation\.action\}/)
  assert.match(settings, /Action saved\. Nothing was executed\./)
  assert.match(settings, /onClick=\{\(\) => runAction\(action\)\}/)
  assert.doesNotMatch(panel, /saveWorkspaceAction|deleteWorkspaceAction|reorderWorkspaceActions/)
})

test('foreground run preserves output and run-state context', async () => {
  const source = await readFile(panelURL, 'utf8')

  assert.match(source, /contextNotice \? <p/)
  assert.match(source, /The commit succeeded, but the Action failed:/)
  assert.match(source, /autoCloseOnSuccess = true/)
  assert.match(source, /!autoCloseOnSuccess \|\| run\?\.status !== 'succeeded'/)
  assert.match(source, /run\.output/)
  assert.match(source, /run\.error/)
})

test('execution panel supports an explicit commit-first launch without duplicating Action input UI', async () => {
  const source = await readFile(panelURL, 'utf8')

  assert.match(source, /onLaunch\?: \(values: Record<string, string>\) => Promise<WorkspaceActionRun>/)
  assert.match(source, /onLaunch \? await onLaunch\(values\) : await startWorkspaceAction/)
  assert.match(source, /launchLabel = 'Run'/)
  assert.match(source, /\{launchLabel\}/)
})

test('worktree Action requests preserve the exact session identity', async () => {
  const [panel, page] = await Promise.all([readFile(panelURL, 'utf8'), readFile(pageURL, 'utf8')])

  assert.match(panel, /startWorkspaceAction\(workspacePath, action\.id, values, sessionId\)/)
  assert.match(panel, /cancelWorkspaceActionRun\(workspacePath, run\.id, sessionId\)/)
  assert.match(page, /startWorkspaceAction\(workspacePath, action\.id, inputs, sessionId\)/)
  assert.match(page, /workspacePath: selectedGitWorkspacePath \|\| action\.workspacePath, sessionId: selectedGitSessionId/)
})
