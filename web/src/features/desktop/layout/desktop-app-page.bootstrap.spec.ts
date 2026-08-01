import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('DesktopAppPage leaves Desktop V3 bootstrap ownership to the root provider and uses the canonical route hydrator', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const routeSelectionStart = source.indexOf('const sessionId = routeSessionId.trim()')
  const routeSelectionEnd = source.indexOf('useEffect(() => {', routeSelectionStart + 1)
  const routeSelection = source.slice(routeSelectionStart, routeSelectionEnd)
  const activationStart = source.indexOf('export async function activateDesktopV3RoutedSession')
  const activationEnd = source.indexOf('function desktopRunIntentFromV3', activationStart)
  const activation = source.slice(activationStart, activationEnd)

  assert.ok(routeSelectionStart >= 0 && routeSelectionEnd > routeSelectionStart)
  assert.ok(activationStart >= 0 && activationEnd > activationStart)
  assert.doesNotMatch(source, /bootstrapDesktopV3SidebarMetadataOnly\(/)
  assert.doesNotMatch(source, /retainDesktopV3RealtimeController\(/)
  assert.doesNotMatch(source, /bootstrapDesktopV3Sidebar\(/)
  assert.doesNotMatch(routeSelection, /requireDesktopV3RealtimeControllerReady|ensureSessionConnected/)
  assert.doesNotMatch(source, /route-session fallback hydrate failed/)
  assert.match(routeSelection, /selectAndHydrateDesktopV3Session\(sessionId\)/)
  assert.doesNotMatch(routeSelection, /dispatchDesktopV3Cache\(selectSession\(sessionId\)\)/)
  assert.match(activation, /const actions = \[[\s\S]*selectSession\(sessionId\)[\s\S]*deps\.commitSnapshot\(previousState, nextState, \[\.\.\.actions\]\)/)
  assert.match(source, /Startup history is delivered by the single \/v3\/sync\/bootstrap transaction/)
})
