import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const routerSource = readFileSync(new URL('./router.tsx', import.meta.url), 'utf8')
const desktopSource = readFileSync(new URL('../features/desktop/layout/desktop-app-page.tsx', import.meta.url), 'utf8')

test('Video Studio has editor and canonical video routes separate from Video Tool', () => {
  assert.match(routerSource, /path: '\/\$workspaceSlug\/studio\/\$videoSessionId'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/video\/\$videoSessionId',[\s\S]*?component: VideoToolPage/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/tools\/video'[\s\S]*?to: '\/\$workspaceSlug\/studio'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/tools'[\s\S]*?to: '\/\$workspaceSlug\/studio'/)
  assert.doesNotMatch(routerSource, /component: SwarmToolsPage/)
  assert.match(routerSource, /WORKSPACE_RESERVED_ROUTE_SEGMENTS = new Set\(\[[^\]]*'video'/)
})

test('Video Studio session view preserves canonical V3 hydration without forcing video mode', () => {
  assert.match(desktopSource, /selectAndHydrateDesktopV3Session\(sessionId\)/)
  assert.match(desktopSource, /routeSessionHasVideoProject = routeSessionIsVideoStudio \|\| routeSessionVideoProjectQuery\.data === true/)
  assert.match(desktopSource, /to: '\/\$workspaceSlug\/studio\/\$videoSessionId'/)
  assert.match(desktopSource, /selectDesktopVideoStudioRows/)
  assert.doesNotMatch(desktopSource, /routeSessionIsVideoStudio \|\| videoStudioRoute/)
})
