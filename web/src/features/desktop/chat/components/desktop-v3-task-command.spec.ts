import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('/task submit is intercepted before ordinary session creation', async () => {
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const submitService = await readFile(new URL('../services/composer-submit.ts', import.meta.url), 'utf8')
  const app = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(composer, /await submitDesktopComposer\(\{/)
  assert.match(composer, /slashPalette\.exactMatch\?\.action\.kind === 'queue-ai-task'[\s\S]*?void handleSubmitClick\(\)/)
  assert.match(composer, /onClick=\{handleSubmitClick\}/)
  assert.ok(submitService.indexOf('if (taskCommand)') < submitService.indexOf('if (input.canStop)'))
  assert.match(submitService, /await input\.onSlashCommand\(taskCommand, input\.draft\)[\s\S]*?input\.clear\(\)/)
  assert.match(submitService, /catch \{\s*return 'task-queue-failed'\s*\}/)
  assert.match(pane, /onSubmit=\{handleSubmit\}/)
  assert.match(app, /replace\(\/\^\\\/task\(\?:\\s\+\|\$\)\/i, ''\)\.trim\(\)/)
  assert.match(app, /const idempotencyKey = globalThis\.crypto/)
  assert.match(app, /createWorkspaceAITask\(workspacePath, request, idempotencyKey, routeSessionId \?\? undefined\)/)
  const queueTaskCase = app.match(/case 'queue-ai-task': \{([\s\S]*?)\n      case 'show-help':/)?.[1] ?? ''
  assert.notEqual(queueTaskCase, '')
  assert.doesNotMatch(queueTaskCase, /navigate\(/)
  assert.match(app, /reconcileWorkspaceAITask\(workspacePath, result\.item\.id\)/)
  assert.match(app, /hydrateWorkspaceAITasks\(workspace\.path\)/)
  assert.match(app, /aiTaskPollersRef\.current\.has\(normalizedTaskID\)/)
  assert.match(app, /Task queued for Swarm\.', tone: 'info'/)
  assert.match(app, /Task started in a managed session\.', tone: 'success'/)
  assert.match(app, /observed\.aiError \|\| 'Swarm could not start the task\.'/)
  assert.match(app, /Enter a task request after \/task\./)
})

test('/task fails explicitly without issuing workspace todo API requests', async () => {
  const api = await readFile(new URL('../../../workspaces/todos/types.ts', import.meta.url), 'utf8')
  assert.match(api, /fetchWorkspaceTodos\([^)]*signal\?: AbortSignal/)
  assert.match(api, /Workspace todos and \/task are temporarily unavailable/)
  assert.doesNotMatch(api, /requestJson/)
  assert.doesNotMatch(api, /\/v1\/workspace\/todos/)
  assert.match(api, /preparation_session_id\?: string/)
  assert.match(api, /ai_state_version\?: number/)
})
