import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

test('DesktopAppPage bootstraps from the initial route only while route selectors remain authoritative', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')

  const bootstrapCalls = source.match(/bootstrapDesktopV3SidebarMetadataOnly\(/g) ?? []

  assert.equal(bootstrapCalls.length, 1)
  assert.match(source, /const initialDesktopV3PreferredSessionId = useRef<string \| null \| undefined>\(routeSessionId \|\| \(workspaceMatch \? null : undefined\)\)/)
  assert.match(source, /preferredSessionId: initialDesktopV3PreferredSessionId\.current/)
  assert.doesNotMatch(
    source,
    /useEffect\(\(\) => \{\s*void bootstrapDesktopV3Sidebar\(\{ preferredSessionId: routeSessionId \}\)\s*\}, \[routeSessionId\]\)/,
  )
  assert.match(source, /const sessionId = routeSessionId \|\| state\.selectedSessionId/)
})
