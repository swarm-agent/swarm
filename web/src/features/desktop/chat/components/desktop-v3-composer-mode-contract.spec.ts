import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('existing routed composer keeps Plan visible only while canonical V3 mode remains Plan', async () => {
  const pane = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(pane, /mode=\{mode\}[\s\S]*showModePicker[\s\S]*resolvedSessionControls/)
  assert.doesNotMatch(pane, /onModeSelect=\{/)
  assert.doesNotMatch(pane, /updateSessionV3Mode/)
  assert.doesNotMatch(pane, /durableWorktreeActive/)
  assert.match(composer, /resolvedSessionControls && showModePicker && mode === 'plan'/)
  assert.match(composer, /<DesktopComposerPlanToggle active readOnly/)
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
