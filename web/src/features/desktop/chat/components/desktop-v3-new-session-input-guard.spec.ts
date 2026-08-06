import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('Desktop V3 new session input is neutral until the routed response resolves', async () => {
  const source = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /DesktopV3RoutedNewSessionController/)
  assert.match(source, /DesktopV3RoutedPendingShell/)
  assert.match(source, /routedNewSession/)
  assert.match(source, /slashCommandContext="new-session"/)
  assert.match(source, /canSubmit=\{Boolean\(draft\.trim\(\)\) \|\| stagedAttachments\.length > 0\}/)
  assert.doesNotMatch(source, /selectedRoute|selectedAgent|selectedModel|modelProfile|handleModeSelect/)
  assert.doesNotMatch(source, /draftModelQueryOptions|agentStateQueryOptions|modelOptionsQueryOptions/)
})

test('Desktop V3 new session composer submits /new prompts through the routed flow', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(source, /routedNewSession \? parseDesktopNewSessionCommand\(rawDraft\) : null/)
  assert.match(source, /const commandDraft = newSessionCommand\?\.prompt \?\? rawDraft/)
  assert.match(source, /worktreePrimed: newSessionCommand\?\.worktreeRequested \?\? routedWorktreeRequested/)
  assert.match(source, /planModeRequested: newSessionCommand\?\.planModeRequested \?\? mode === 'plan'/)
  assert.match(source, /routedSubmit = onRoutedSubmit\(routedSnapshot\)/)
  assert.doesNotMatch(source, /newSessionCommandBlocked|You’re already starting a new session/)
})
