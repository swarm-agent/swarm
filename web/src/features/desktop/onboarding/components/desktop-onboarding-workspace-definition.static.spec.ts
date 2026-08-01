import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')

test('onboarding reuses workspace add and gates completion on durable Router analysis', () => {
  assert.match(source, /const resolution = await saveWorkspace\(\{/)
  assert.match(source, /await waitForWorkspaceDefinition\(resolution\)/)
  assert.match(source, /const selectedResolution = await openWorkspace\(entry\.path\)/)
  assert.match(source, /await finishWithWorkspace\(selectedResolution, entry\.path\)/)
  assert.match(source, /Router is personalizing this workspace/)
  assert.doesNotMatch(source, /personalization\//)
})

test('onboarding does not finalize an existing pending or failed workspace', () => {
  assert.match(source, /workspace\?\.definitionStatus === 'pending'/)
  assert.match(source, /await waitForWorkspaceDefinition\(workspaceDefinitionResolution\(workspace\)\)/)
  assert.match(source, /workspace\?\.definitionStatus === 'failed'/)
  assert.match(source, /throw new Error\(workspaceDefinitionFailureMessage\(workspace\)\)/)
})
