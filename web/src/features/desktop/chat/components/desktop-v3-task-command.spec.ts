import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

test('/task always uses the dedicated background Router session API', async () => {
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const submitService = await readFile(new URL('../services/composer-submit.ts', import.meta.url), 'utf8')
  const app = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  const writeAPI = await readFile(new URL('../../session-v3/write-api.ts', import.meta.url), 'utf8')

  assert.match(composer, /desktopComposerBackgroundRouterCommand\(submittedDraft\)/)
  assert.match(composer, /start-background-router-session/)
  const slashCommands = await readFile(new URL('../services/slash-commands.ts', import.meta.url), 'utf8')
  assert.match(slashCommands, /id: 'task-plan'[\s\S]*command: '\/task plan'[\s\S]*start-background-router-session/)
  assert.match(slashCommands, /tips: \['\/task plan \[--workspace <saved-workspace>\] <prompt>'/)
  assert.match(submitService, /desktopComposerBackgroundRouterCommand\(input\.draft\)/)
  assert.ok(submitService.indexOf('if (backgroundRouterCommand)') < submitService.indexOf('if (input.canStop)'))

  const backgroundCase = app.match(/case 'start-background-router-session': \{([\s\S]*?)\n      case 'show-help':/)?.[1] ?? ''
  assert.notEqual(backgroundCase, '')
  assert.match(backgroundCase, /parseDesktopTaskCommand\(draft\)/)
  assert.match(backgroundCase, /resolveDesktopTaskWorkspace\(\{[\s\S]*?selector: workspaceSelector[\s\S]*?activeWorkspace: topWorkspace[\s\S]*?savedWorkspaces: workspaces/)
  assert.match(backgroundCase, /targetWorkspaceAuthority = workspaceAuthorityFor\(targetWorkspace\)/)
  assert.match(backgroundCase, /launch = postDesktopV3BackgroundRouterSessionStart\(\{[\s\S]*?\.\.\.targetWorkspaceAuthority[\s\S]*?input: request/)
  assert.match(backgroundCase, /plan_mode_requested: mode === 'plan'/)
  assert.match(backgroundCase, /Background Router task sent\.', tone: 'success'/)
  assert.match(backgroundCase, /void launch\.catch\([\s\S]*?tone: 'error'/)
  assert.doesNotMatch(backgroundCase, /await postDesktopV3BackgroundRouterSessionStart|createWorkspaceAITask|\/v1\/workspace\/todos|aiTasks\.mergeItems|onRoutedSubmit/)

  assert.match(writeAPI, /postDesktopV3BackgroundRouterSessionStart/)
  assert.match(writeAPI, /'\/v3\/sessions:background-router'/)
  assert.doesNotMatch(writeAPI.match(/postDesktopV3BackgroundRouterSessionStart\([\s\S]*?\n\}/)?.[0] ?? '', /managed_worktree_requested/)
})

test('/task in a pending routed session takes background precedence without consuming the pending session', async () => {
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const pane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')

  const backgroundPrecedence = composer.indexOf('if (routedNewSession && submittedBackgroundRouterCommand)')
  const pendingSubmit = composer.indexOf('if (routedNewSession && onRoutedSubmit)')
  assert.ok(backgroundPrecedence >= 0 && backgroundPrecedence < pendingSubmit)
  const pendingTaskCase = composer.slice(backgroundPrecedence, pendingSubmit)
  assert.match(pendingTaskCase, /void submitDesktopComposer\(\{[\s\S]*?onSlashCommand,[\s\S]*?\}\)/)
  assert.doesNotMatch(pendingTaskCase, /onRoutedSubmit/)
  assert.match(composer, /command\.action\.kind === 'start-background-router-session'[\s\S]*?void handleSubmitClick\(\)/)
  assert.match(pane, /onSubmit=\{\(\) => undefined\}/)
  assert.match(pane, /onRoutedSubmit=\{handleSubmit\}/)
})

test('bare /task forms stay editable on the shared missing-request validation path', async () => {
  const app = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  assert.match(app, /const \{ request, mode, workspaceSelector, workspaceSelectionInvalid \} = parseDesktopTaskCommand\(draft\)[\s\S]*?if \(!request\)[\s\S]*?Enter a task request after \/task\.[\s\S]*?throw error/)
})

test('/flag is dev-only, requires an existing session, and expands through /task', async () => {
  const composer = await readFile(new URL('./desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8')
  const existingPane = await readFile(new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')
  const newPane = await readFile(new URL('./desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
  const app = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(composer, /buildDesktopSlashPaletteState\(draft, \{ developerMode: developerMode && Boolean\(sessionId\.trim\(\)\) \}\)/)
  assert.match(composer, /buildDesktopFlagTaskPrompt\(rawDraft, sessionId\)/)
  assert.match(composer, /const commandDraft = flagTaskPrompt \? `\/task \$\{flagTaskPrompt\}`/)
  assert.ok(composer.indexOf('const commandDraft = flagTaskPrompt') < composer.indexOf('desktopComposerBackgroundRouterCommand(submittedDraft)'))
  assert.match(existingPane, /sessionId=\{normalizedSessionId\}[\s\S]*?developerMode=\{developerMode\}/)
  assert.match(newPane, /developerMode=\{developerMode\}/)
  assert.match(app, /onSlashCommand=\{handleSlashCommand\}[\s\S]*?developerMode=\{updateDevMode\}/)
})
