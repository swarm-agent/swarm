import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function readDesktopAppPage(): Promise<string> {
  return readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
}

async function readNewSessionPane(): Promise<string> {
  return readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
}

test('Desktop new-session startup uses the Router-owned worktree transaction', async () => {
  const [app, pane] = await Promise.all([readDesktopAppPage(), readNewSessionPane()])

  assert.match(pane, /DesktopV3RoutedNewSessionController/)
  assert.match(pane, /postDesktopV3RoutedSessionStart\(request\)/)
  assert.match(pane, /agentName: 'swarm'/)
  assert.match(pane, /onRoutedSessionResolved: \(result: DesktopV3RoutedStartResult\)/)
  assert.match(app, /onRoutedSessionResolved=\{handleRoutedSessionResolved\}/)
  assert.match(app, /applyDesktopV3RoutedStartResponse\(response\)[\s\S]*await selectAndHydrateDesktopV3Session\(response\.session_id\)/)
  assert.doesNotMatch(pane, /createDesktopV3NewSessionOperation|startNewDesktopV3Session|worktree: \{ mode: 'off' \}/)
})

test('Desktop routed activation applies the first durable user message before hydration and navigation', async () => {
  const app = await readDesktopAppPage()
  const handlerStart = app.indexOf('const handleRoutedSessionResolved = useCallback')
  const handlerEnd = app.indexOf('const handleArchivePlanSession', handlerStart)
  const handler = app.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  const messageResult = handler.indexOf('applyDesktopV3RoutedStartResponse(response)')
  const hydrate = handler.indexOf('await selectAndHydrateDesktopV3Session(response.session_id)')
  const navigate = handler.indexOf('await handleNewSessionStarted(response.session_id)')
  assert.ok(messageResult >= 0 && hydrate > messageResult && navigate > hydrate)
})

test('Desktop ordinary startup cannot opt out of the Router-owned worktree', async () => {
  const pane = await readNewSessionPane()

  assert.match(pane, /never creates a direct dev-bound session/)
  assert.match(pane, /startPath="router"/)
  assert.match(pane, /desktopRoutedSessionMetadata\(\{ source: 'desktop-v3' \}\)/)
  assert.doesNotMatch(pane, /managed_worktree_requested|worktree_name|worktree_branch_name|startPath="direct"/)
})

test('V3 session workspace updates drive client route changes without workspace polling', async () => {
  const source = await readDesktopAppPage()
  const routeEffectStart = source.indexOf("if (!routeWorkspaceSlug || !routeSessionId)")
  const routeEffectEnd = source.indexOf('const handleSelectVideoSidebarSession', routeEffectStart)
  const routeEffect = source.slice(routeEffectStart, routeEffectEnd)

  assert.ok(routeEffectStart >= 0 && routeEffectEnd > routeEffectStart)
  assert.match(routeEffect, /sessionById\.get\(routeSessionId\)/)
  assert.match(routeEffect, /desktopRouteWorkspacePathForSession\(session/)
  assert.match(routeEffect, /navigate\(\{[\s\S]*to: '\/\$workspaceSlug\/\$sessionId'/)
  assert.doesNotMatch(routeEffect, /setInterval|refetchInterval|fetch\(/)
})

test('explicit New Session gestures still reset the local operation and navigate once', async () => {
  const source = await readDesktopAppPage()
  const handlerStart = source.indexOf('const handleStartNewSessionInWorkspace = useCallback')
  const handlerEnd = source.indexOf('const handleNewSessionStarted', handlerStart)
  const handler = source.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handler, /clearDesktopV3RoutedStartOperation\(\)/)
  assert.match(handler, /setNewSessionEpoch\(\(current\) => current \+ 1\)/)
  assert.match(handler, /dispatchDesktopV3Cache\(selectSession\(undefined\)\)/)
  assert.match(handler, /navigate\(\{ to: '\/\$workspaceSlug'/)
  assert.equal((handler.match(/navigate\(\{/g) ?? []).length, 1)
})
