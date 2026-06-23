import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('DesktopAppPage leaves Desktop V3 runtime ownership to the root provider while route selectors only select sessions', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.doesNotMatch(source, /bootstrapDesktopV3SidebarMetadataOnly\(/)
  assert.doesNotMatch(source, /retainDesktopV3RealtimeController\(/)
  assert.doesNotMatch(source, /bootstrapDesktopV3Sidebar\(/)
  assert.doesNotMatch(source, /requireDesktopV3RealtimeControllerReady/)
  assert.doesNotMatch(source, /ensureSessionHistory/)
  assert.doesNotMatch(source, /route-session fallback hydrate failed/)
  assert.match(source, /const sessionId = routeSessionId\.trim\(\)/)
  assert.match(source, /dispatchDesktopV3Cache\(selectSession\(sessionId\)\)/)
  assert.match(source, /Startup history is delivered by the single \/v3\/sync\/bootstrap transaction/)
})
