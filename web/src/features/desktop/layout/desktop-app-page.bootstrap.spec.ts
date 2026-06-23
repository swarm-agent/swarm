import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('DesktopAppPage leaves Desktop V3 runtime ownership to the root provider while route selectors remain authoritative', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.doesNotMatch(source, /bootstrapDesktopV3SidebarMetadataOnly\(/)
  assert.doesNotMatch(source, /retainDesktopV3RealtimeController\(/)
  assert.doesNotMatch(
    source,
    /useEffect\(\(\) => \{\s*void bootstrapDesktopV3Sidebar\(\{ preferredSessionId: routeSessionId \}\)\s*\}, \[routeSessionId\]\)/,
  )
  assert.match(source, /const sessionId = routeSessionId\.trim\(\)/)
  assert.match(
    source,
    /dispatchDesktopV3Cache\(selectSession\(sessionId\)\)\s*if \(isDesktopV3SessionTailReady\(getDesktopV3CacheSnapshot\(\), sessionId\)\) \{\s*return\s*\}\s*void requireDesktopV3RealtimeControllerReady\(\)\s*\.then\(\(controller\) => controller\.ensureSessionHistory\(sessionId\)\)/,
  )
  assert.match(source, /console\.error\('\[desktop-v3\] route-session fallback hydrate failed', error\)/)
})
