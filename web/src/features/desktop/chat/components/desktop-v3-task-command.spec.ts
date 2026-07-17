import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('/task submit is intercepted before ordinary session creation', async () => {
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const app = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(composer, /submittedPalette\.exactMatch\?\.action\.kind === 'queue-ai-task'/)
  assert.match(composer, /await onSlashCommand\?\.\(submittedPalette\.exactMatch, submittedDraft\)/)
  assert.match(composer, /await onSlashCommand[\s\S]*?clearComposerForSubmit\(\)[\s\S]*?catch/)
  assert.match(composer, /task request stays editable/)
  assert.match(pane, /onSubmit=\{handleSubmit\}/)
  assert.match(app, /replace\(\/\^\\\/task\(\?:\\s\+\|\$\)\/i, ''\)\.trim\(\)/)
  assert.match(app, /createWorkspaceAITask\(workspacePath, request\)/)
  assert.match(app, /Enter a task request after \/task\./)
})
