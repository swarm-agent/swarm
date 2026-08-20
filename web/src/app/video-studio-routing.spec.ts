import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const routerSource = readFileSync(new URL('./router.tsx', import.meta.url), 'utf8')
const desktopSource = readFileSync(new URL('../features/desktop/layout/desktop-app-page.tsx', import.meta.url), 'utf8')

test('Video Studio has a session-ID route separate from Video Tool', () => {
  assert.match(routerSource, /path: '\/\$workspaceSlug\/video\/\$videoSessionId'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/tools\/video'/)
  assert.match(routerSource, /WORKSPACE_RESERVED_ROUTE_SEGMENTS = new Set\(\[[^\]]*'video'/)
})

test('Video Studio route preserves canonical V3 hydration and workspace return navigation', () => {
  assert.match(desktopSource, /selectAndHydrateDesktopV3Session\(sessionId\)/)
  assert.match(desktopSource, /videoStudioRoute \? \(/)
  assert.match(desktopSource, /Back to Workspace/)
  assert.match(desktopSource, /selectDesktopVideoStudioRows/)
})
