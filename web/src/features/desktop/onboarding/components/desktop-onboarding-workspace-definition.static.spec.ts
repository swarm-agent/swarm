import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')

test('onboarding saves, refreshes, selects, finalizes, and navigates without definition polling', () => {
  assert.match(source, /await saveWorkspace\(\{/)
  assert.match(source, /await refreshWorkspaces\(\)/)
  assert.match(source, /const selectedResolution = await openWorkspace\(entry\.path\)/)
  assert.match(source, /await finishWithWorkspace\(selectedResolution, entry\.path\)/)
  assert.match(source, /waitForOnboardingReadyHold\(\)/)
  assert.match(source, /patchDesktopOnboarding\(\{ desktopOnboardingComplete: true \}\)\.then\(\(\) => reloadStatus\(\)\)/)
  assert.match(source, /await navigateToWorkspace\(resolution, fallbackPath\)/)
  assert.doesNotMatch(source, /waitForWorkspaceDefinition/)
})

test('onboarding does not gate saved workspaces on definition state or show personalization copy', () => {
  assert.match(source, /const resolution = await openWorkspace\(path\)/)
  assert.doesNotMatch(source, /definitionStatus === 'pending'/)
  assert.doesNotMatch(source, /definitionStatus === 'failed'/)
  assert.doesNotMatch(source, /workspaceDefinitionFailureMessage/)
  assert.doesNotMatch(source, /personaliz/i)
  assert.doesNotMatch(source, /Router is reading the bounded workspace context/)
  assert.match(source, /Setting up your workspace…/)
  assert.match(source, /Holding the onboarding surface steady while the workspace is confirmed\./)
})
