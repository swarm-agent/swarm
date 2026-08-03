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

test('Desktop V3 new session composer warns and blocks every nested /new form', async () => {
  const source = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(source, /slashCommandContext === 'new-session' && \/\^\\s\*\\\/new\(\?:\\s\|\$\)\/i\.test\(draft\)/)
  assert.match(source, /slashCommandContext !== 'new-session' \|\| command\.action\.kind !== 'new-session'/)
  assert.match(source, /if \(newSessionCommandBlocked\) return/)
  assert.match(source, /You’re already starting a new session\. Remove[\s\S]*\/new[\s\S]*and type your request here\./)
  assert.match(source, /disabled=\{!canStop && \(newSessionCommandBlocked \|\| uploadingAttachment/)
})
