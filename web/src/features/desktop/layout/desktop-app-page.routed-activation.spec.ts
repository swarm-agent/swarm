import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function readDesktopAppPage(): Promise<string> {
  return readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
}

test('routed new-chat activation only publishes a validated canonical result', async () => {
  const source = await readDesktopAppPage()
  const activationStart = source.indexOf('export async function activateDesktopV3RoutedSession')
  const activationEnd = source.indexOf('function desktopRunIntentFromV3', activationStart)
  const activation = source.slice(activationStart, activationEnd)

  assert.ok(activationStart >= 0 && activationEnd > activationStart)
  assert.match(activation, /normalizeDesktopV3RoutedSessionStartResponse\(result\)/)
  assert.match(activation, /shouldActivate\(\)[\s\S]*requireRealtimeController\(\)[\s\S]*structuredClone\(previousState\)[\s\S]*commitSnapshot\(previousState, nextState, \[\.\.\.actions\]\)[\s\S]*ensureSessionConnected\(sessionId\)/)
  assert.match(activation, /sessionCreateResponseToAction\(createResponse, sidebarScopeId\)/)
  assert.match(activation, /messageMutationResponseToAction\(messageResponse,[\s\S]*response\.first_message\.id\)/)
  assert.match(activation, /nextState\.sessionViewsById\[sessionId\] = desktopV3RoutedSessionView\(response\)/)
  assert.match(activation, /selectSession\(sessionId\)/)
  assert.match(activation, /commitSnapshot\(previousState, nextState, \[\.\.\.actions\]\)/)
  assert.doesNotMatch(activation, /selectAndHydrateDesktopV3Session/)
})

test('routed activation derives URL identity from the returned source workspace and ignores stale completions', async () => {
  const source = await readDesktopAppPage()
  const handlerStart = source.indexOf('const handleRoutedSessionResolved = useCallback')
  const handlerEnd = source.indexOf('const handleArchivePlanSession', handlerStart)
  const handler = source.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handler, /source_workspace_path\.trim\(\)/)
  assert.match(handler, /source_workspace_id/)
  assert.match(handler, /unknown source workspace/)
  assert.match(handler, /activationGeneration !== routedActivationGenerationRef\.current/)
  assert.match(handler, /routedActivationWorkspaceRef\.current !== expectedWorkspacePath/)
  assert.match(source, /routedActivationGenerationRef\.current \+= 1|\+\+routedActivationGenerationRef\.current/)
  assert.match(handler, /params: \{ workspaceSlug, sessionId: response\.session_id \}/)
  assert.match(source, /onRoutedSessionResolved:[\s\S]*handleRoutedSessionResolved\(result, routeWorkspace\.path\)/)
})

test('app-level new-chat wiring has no local pending authority or generic mode command', async () => {
  const source = await readDesktopAppPage()

  assert.doesNotMatch(source, /newSessionModeByWorkspace/)
  assert.doesNotMatch(source, /sessionModeCommand/)
  assert.doesNotMatch(source, /toggle-plan-auto/)
  assert.doesNotMatch(source, /DesktopV3RoutedNewSessionController/)
  assert.doesNotMatch(source, /createDesktopV3RoutedDraftState/)
  assert.doesNotMatch(source, /createDesktopV3RoutedStartOperation/)
})
