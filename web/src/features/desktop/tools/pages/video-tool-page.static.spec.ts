import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const pageSourceUrl = new URL('./video-tool-page.tsx', import.meta.url)
const routerSourceUrl = new URL('../../../../app/router.tsx', import.meta.url)
const desktopPageSourceUrl = new URL('../../layout/desktop-app-page.tsx', import.meta.url)

test('Video Studio fills the app surface without an inset page wrapper', async () => {
  const source = await readFile(pageSourceUrl, 'utf8')

  assert.match(source, /<div className="hidden h-full w-full flex-col lg:flex">/)
  assert.match(source, /<main className="flex min-h-0 flex-1 overflow-hidden">/)
  assert.doesNotMatch(source, /mx-auto hidden h-full w-full max-w-none flex-col px-4 py-4/)
})

test('Video Studio keeps a route-backed video selection and exposes session mode', async () => {
  const [source, routerSource, desktopSource] = await Promise.all([
    readFile(pageSourceUrl, 'utf8'),
    readFile(routerSourceUrl, 'utf8'),
    readFile(desktopPageSourceUrl, 'utf8'),
  ])

  assert.match(routerSource, /path: '\/\$workspaceSlug\/studio\/\$videoSessionId'/)
  assert.match(source, /VIDEO_STUDIO_LAST_SESSION_STORAGE_KEY/)
  assert.match(source, /to: '\/\$workspaceSlug\/studio\/\$videoSessionId'/)
  assert.match(source, /to: '\/\$workspaceSlug\/\$sessionId'/)
  assert.match(source, /label: 'Open session mode'/)
  assert.doesNotMatch(desktopSource, /routeSessionIsVideoStudio \|\| videoStudioRoute/)
  assert.doesNotMatch(desktopSource, /to: '\/\$workspaceSlug\/video\/\$videoSessionId',[\s\S]*replace: true/)
  assert.match(desktopSource, />Video mode</)
})
