import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const routerSource = readFileSync(new URL('./router.tsx', import.meta.url), 'utf8')
const desktopSource = readFileSync(new URL('../features/desktop/layout/desktop-app-page.tsx', import.meta.url), 'utf8')

test('Video Studio session-ID route owns the canonical editor surface', () => {
  assert.match(routerSource, /path: '\/\$workspaceSlug\/video\/\$videoSessionId',[\s\S]*?component: VideoToolPage/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/tools\/video'/)
  assert.match(routerSource, /WORKSPACE_RESERVED_ROUTE_SEGMENTS = new Set\(\[[^\]]*'video'/)
})

test('Desktop navigation sends Video Studio sessions to the canonical editor route', () => {
  assert.match(desktopSource, /selectAndHydrateDesktopV3Session\(sessionId\)/)
  assert.match(desktopSource, /to="\/\$workspaceSlug\/video\/\$videoSessionId"/)
  assert.match(desktopSource, /selectDesktopVideoStudioRows/)
})
