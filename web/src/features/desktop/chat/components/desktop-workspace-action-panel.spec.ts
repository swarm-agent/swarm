import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const panelURL = new URL('./desktop-workspace-action-panel.tsx', import.meta.url)

test('workspace Action panel can foreground an existing run without launching a duplicate', async () => {
  const source = await readFile(panelURL, 'utf8')

  assert.match(source, /initialRun\?: WorkspaceActionRun \| null/)
  assert.match(source, /useState<WorkspaceActionRun \| null>\(initialRun\)/)
  assert.match(source, /if \(!initialRun\) return/)
  assert.match(source, /if \(!run \|\| run\.status !== 'running'\) return/)
  assert.match(source, /fetchWorkspaceActionRun\(workspacePath, run\.id, controller\.signal\)/)
  assert.match(source, /onRunChangeRef\.current\?\.\(next\)/)
})

test('post-commit foreground run preserves output and partial-success context', async () => {
  const source = await readFile(panelURL, 'utf8')

  assert.match(source, /contextNotice \? <p/)
  assert.match(source, /The commit succeeded, but the Action failed:/)
  assert.match(source, /autoCloseOnSuccess = true/)
  assert.match(source, /!autoCloseOnSuccess \|\| run\?\.status !== 'succeeded'/)
  assert.match(source, /run\.output/)
  assert.match(source, /run\.error/)
})
