import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

test('existing routed composer keeps mode separate and removes plan mode toggle', async () => {
  const pane = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(pane, /mode=\{mode\}[\s\S]*showModePicker[\s\S]*resolvedSessionControls/)
  assert.doesNotMatch(pane, /onModeSelect=\{/)
  assert.doesNotMatch(pane, /updateSessionV3Mode\b/)
  assert.doesNotMatch(pane, /durableWorktreeActive/)
  assert.doesNotMatch(composer, /<DesktopComposerPlanToggle/)
})

test('new routed Desktop labels transition from Waiting to Routing before durable activation', async () => {
  const pane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const pending = await readFile(new URL('./desktop-v3-routed-pending-shell.tsx', import.meta.url), 'utf8')
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')

  assert.match(pane, /modelStatusLabel="(Waiting…|Ready)"/)
  assert.match(pending, /routerPath \? 'Routing…'/)
  assert.match(composer, /statusLabel=\{modelStatusLabel\}/)
  assert.match(composer, /planModeRequested: newSessionCommand\?\.planModeRequested \?\? mode === 'plan'/)
})
