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
  assert.match(app, /const idempotencyKey = globalThis\.crypto/)
  assert.match(app, /createWorkspaceAITask\(workspacePath, request, idempotencyKey, routeSessionId/)
  assert.match(app, /reconcileWorkspaceAITask\(workspacePath, result\.item\.id\)/)
  assert.match(app, /hydrateWorkspaceAITasks\(workspace\.path\)/)
  assert.match(app, /aiTaskPollersRef\.current\.has\(normalizedTaskID\)/)
  assert.match(app, /Task queued for Swarm\.', tone: 'info'/)
  assert.match(app, /Task started in a managed session\.', tone: 'success'/)
  assert.match(app, /observed\.aiError \|\| 'Swarm could not start the task\.'/)
  assert.match(app, /Enter a task request after \/task\./)
})

test('/task API carries a stable key, optional origin, abortable reconciliation, and diagnostics', async () => {
  const api = await readFile(new URL('../../../workspaces/todos/types.ts', import.meta.url), 'utf8')
  assert.match(api, /'Idempotency-Key': idempotencyKey\.trim\(\)/)
  assert.match(api, /session_id: originSessionId\?\.trim\(\) \|\| undefined/)
  assert.match(api, /fetchWorkspaceTodos\([^)]*signal\?: AbortSignal/)
  assert.match(api, /requestJson<WorkspaceTodosResponseWire>\([^\n]+, \{ signal \}\)/)
  assert.match(api, /preparation_session_id\?: string/)
  assert.match(api, /ai_state_version\?: number/)
})
