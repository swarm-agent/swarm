import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function readDesktopAppPage(): Promise<string> {
  return readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
}

async function readNewSessionPane(): Promise<string> {
  return readFile(new URL('../chat/components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8')
}

test('Desktop new-session startup creates an ordinary Swarm session without routed activation', async () => {
  const [app, pane] = await Promise.all([readDesktopAppPage(), readNewSessionPane()])

  assert.match(pane, /createDesktopV3NewSessionOperation\(\{/)
  assert.match(pane, /startNewDesktopV3Session\(\{ operation \}\)/)
  assert.match(pane, /startDesktopV3CreateOnlySession\(\{ operation \}\)[\s\S]*getDesktopV3MediaCapability[\s\S]*uploadDesktopV3MediaAsset[\s\S]*appendFirstDesktopV3Message\(\{ operation \}\)/)
  assert.match(pane, /agentName: 'swarm'/)
  assert.match(pane, /worktree: \{ mode: 'off' \}/)
  assert.match(pane, /onSessionStarted: \(sessionId: string\)/)
  assert.match(app, /onSessionStarted=\{handleNewSessionStarted\}/)
  assert.doesNotMatch(pane, /postDesktopV3RoutedSessionStart|DesktopV3RoutedNewSessionController/)
  assert.doesNotMatch(app, /activateDesktopV3RoutedSession|validateDesktopV3RoutedActivationResponse|handleRoutedSessionResolved/)
})

test('Desktop direct startup keeps the user current checkout unless isolation is explicitly requested', async () => {
  const pane = await readNewSessionPane()

  assert.match(pane, /Worktree allocation is intentionally absent here/)
  assert.match(pane, /worktree: \{ mode: 'off' \}/)
  assert.match(pane, /sessionMetadata: \{ source: 'desktop-v3' \}/)
  assert.match(pane, /startPath="direct"/)
  assert.doesNotMatch(pane, /managed_worktree_requested|worktree_name|worktree_branch_name/)
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
