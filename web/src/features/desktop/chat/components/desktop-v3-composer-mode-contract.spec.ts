import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('existing Desktop Plan control uses canonical V3 mode mutation and durable projected state', async () => {
  const pane = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(pane, /updateSessionV3Mode\(normalizedSessionId, nextMode\)/)
  assert.match(pane, /sessionV3ModeSettingsMutationResponse\(response, normalizedSessionId, nextMode\)/)
  assert.match(pane, /type: "mutation\.sessionSettingsResult"/)
  assert.match(pane, /setMode\(normalizeSessionMode\(response\.mode \?\? nextMode\)\)/)
  assert.match(pane, /setSendError\(error instanceof Error \? error\.message : "Failed to switch session mode"\)/)
  assert.match(pane, /mode=\{mode\}/)
  assert.match(pane, /onModeSelect=\{\(nextMode\) => \{ void handleModeSelect\(nextMode\); \}\}/)
  assert.match(pane, /durableWorktreeActive=\{Boolean\(session\?\.worktreeEnabled[\s\S]*cacheSession\?\.worktree_branch/)
})

test('new routed Desktop labels transition from Waiting to Routing before durable activation', async () => {
  const pane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const pending = await readFile(new URL('./desktop-v3-routed-pending-shell.tsx', import.meta.url), 'utf8')
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(pane, /modelStatusLabel="Waiting…"/)
  assert.match(pending, /routing \? 'Routing…'/)
  assert.match(composer, /statusLabel=\{modelStatusLabel\}/)
  assert.match(composer, /planModeRequested: mode === 'plan'/)
})
