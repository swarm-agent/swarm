import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('DesktopAppPage bootstraps from the initial route only while route selectors remain authoritative', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  const bootstrapCalls = source.match(/bootstrapDesktopV3Sidebar\(/g) ?? []

  assert.equal(bootstrapCalls.length, 1)
  assert.match(source, /const initialDesktopV3RouteSessionId = useRef\(routeSessionId\)/)
  assert.match(
    source,
    /useEffect\(\(\) => \{\s*(?:const stopPersistence = startDesktopV3PersistenceController\(\)\s*)?void bootstrapDesktopV3Sidebar\(\{ preferredSessionId: initialDesktopV3RouteSessionId\.current \}\)\s*(?:return stopPersistence\s*)?\}, \[\]\)/,
  )
  assert.doesNotMatch(
    source,
    /useEffect\(\(\) => \{\s*void bootstrapDesktopV3Sidebar\(\{ preferredSessionId: routeSessionId \}\)\s*\}, \[routeSessionId\]\)/,
  )
  assert.match(source, /const sessionId = routeSessionId \|\| state\.selectedSessionId/)
})
