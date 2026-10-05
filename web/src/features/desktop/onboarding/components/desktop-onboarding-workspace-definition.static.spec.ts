import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

// Purpose: DesktopOnboardingGate no longer owns workspace setup or retained
// assistant admission. This is only a source-wiring removal guard; provider
// behavior is exercised by desktop-onboarding-flow.browser.spec.ts, and project
// creation by project-creation-flow.browser.spec.ts. Shared TUI APIs stay intact.
test('Desktop account setup cannot restore the retired workspace wizard', async () => {
  const source = await readFile(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')
  assert.doesNotMatch(source, /WorkspaceOnboardingAssistant|startWorkspaceOnboardingSession|useWorkspaceLauncher|view === 'workspace'|workspace-onboarding-assistant\.v1/)
  assert.match(source, /completeAccountOnboarding\(/)
  assert.match(source, /openProjects: \(\) => navigate\(\{ to: '\/projects' \}\)/)
  assert.match(source, /acceptOnboardingProviderCredential\(payload\)/)
})

test('Desktop Step 4 provides rich workspace picker with AGENTS.md prioritization, multi-select, and folder creation', async () => {
  const source = await readFile(new URL('./desktop-onboarding-gate.tsx', import.meta.url), 'utf8')
  assert.match(source, /agentsWorkspaces/)
  assert.match(source, /Workspaces with AGENTS\.md/)
  assert.match(source, /togglePathSelection/)
  assert.match(source, /\/v1\/workspace\/browse/)
  assert.match(source, /\/v1\/workspace\/folders\/create/)
  assert.match(source, /Personalize & Talk to Swarm/)
})
