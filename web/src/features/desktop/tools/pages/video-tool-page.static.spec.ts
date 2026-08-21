import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const pageSourceUrl = new URL('./video-tool-page.tsx', import.meta.url)
const routerSourceUrl = new URL('../../../../app/router.tsx', import.meta.url)
const desktopPageSourceUrl = new URL('../../layout/desktop-app-page.tsx', import.meta.url)

test('Video Studio fills the app surface without an inset page wrapper', async () => {
  const source = await readFile(pageSourceUrl, 'utf8')

  assert.match(source, /<div className="absolute inset-0 flex min-h-0 flex-col overflow-hidden/)
  assert.match(source, /<main className="flex min-h-0 flex-1 flex-col overflow-y-auto overscroll-contain lg:flex-row lg:overflow-hidden">/)
  assert.match(source, /lg:hidden/)
  assert.doesNotMatch(source, /Coming soon for mobile/)
  assert.doesNotMatch(source, /mx-auto hidden h-full w-full max-w-none flex-col px-4 py-4/)
})

test('Video Studio tolerates legacy nullable timeline clip arrays', async () => {
  const source = await readFile(pageSourceUrl, 'utf8')
  assert.match(source, /currentRevision\?\.timeline\.clips\?\.some/)
  assert.match(source, /\(currentRevision\.timeline\.clips \?\? \[\]\)\.filter/)
})

test('Video Studio exposes automatic working changes and non-mutating history preview', async () => {
  const source = await readFile(pageSourceUrl, 'utf8')
  const proposalSource = await readFile(new URL('../video-studio/video-studio-surface.tsx', import.meta.url), 'utf8')

  assert.match(source, /replaceCachedVideoMedia/)
  assert.match(source, /replaceCachedImageMedia/)
  assert.match(source, /Previewing r\{previewRevision\.revision_number\}/)
  assert.match(source, /Restore this as a new version/)
  assert.match(source, /videoProjectProjectionSequence/)
  assert.match(proposalSource, /New change added/)
  assert.match(proposalSource, /Showing working change/)
  assert.match(proposalSource, /Restore prior section/)
  assert.match(proposalSource, /previewProposalId/)
  assert.match(proposalSource, /pending\[pending\.length - 1\]\.id/)
  assert.doesNotMatch(proposalSource, /const latestPending = pending\[0\]/)
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
  assert.match(source, /data-testid="video-studio-session-toggle"/)
  assert.match(source, /aria-label="Switch to session mode"/)
  assert.match(desktopSource, />Video sessions</)
  assert.match(desktopSource, /childLabel=""/)
  assert.match(desktopSource, /selectionGroup="video"/)
  assert.match(desktopSource, /archiveDesktopV3Sessions\(ids\)/)
  assert.doesNotMatch(desktopSource, /routeSessionIsVideoStudio \|\| videoStudioRoute/)
  assert.match(desktopSource, /routeSessionHasVideoProject = routeSessionIsVideoStudio \|\| routeSessionVideoProjectQuery\.data === true/)
  assert.match(desktopSource, /sessionVideoProjectPresenceKey\(routeSessionId, routeSessionVideoProjectSequence\)/)
  assert.match(desktopSource, /studioMode=\{routeSessionHasVideoProject \? 'session' : null\}/)
  assert.match(source, /videoThreadFromSessionProject/)
  assert.match(desktopSource, /to: '\/\$workspaceSlug\/studio\/\$videoSessionId'/)
})
