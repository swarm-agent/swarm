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

test('Video Studio reviews nested selective iterations in the sidebar and keeps the center player review-free', async () => {
  const source = await readFile(pageSourceUrl, 'utf8')
  const proposalSource = await readFile(new URL('../video-studio/video-studio-surface.tsx', import.meta.url), 'utf8')

  assert.match(source, /replaceCachedVideoMedia/)
  assert.match(source, /replaceCachedImageMedia/)
  assert.match(source, /<VideoIterationSidebar/)
  assert.match(source, /layoutClassName="flex max-h-\[48dvh\]/)
  assert.match(source, /childrenClassName="mt-3 min-h-0 flex-1 overflow-y-auto"/)
  assert.match(source, /compactSelectedSession=\{Boolean\(selectedThread\)\}/)
  assert.match(source, />Video workspace<\/p>/)
  assert.doesNotMatch(source, />Current movie<\/p>/)
  assert.match(source, /Previewing r\{previewRevision\.revision_number\}/)
  assert.match(source, /Restore as new version/)
  assert.match(source, /videoProjectProjectionSequence/)
  assert.match(source, /selectionKind: 'iteration'/)
  assert.match(source, /iterationContext=\{studioComposerContext\?\.iteration\}/)
  assert.doesNotMatch(source, /<VideoProposalReview/)
  assert.doesNotMatch(source, /aria-label="Kept video plan"/)

  assert.match(proposalSource, /aria-label="Video iterations"/)
  assert.match(proposalSource, /buildVideoIterationTimeline/)
  assert.match(proposalSource, /setPreviewId\(newestPendingIterationId\)/)
  assert.match(proposalSource, /proposal\.accepted_operation_ids/)
  assert.match(proposalSource, /Enable \$\{change\.label\}/)
  assert.match(proposalSource, /props\.onFocusChange\(change\.clipId, change\.startMs\)/)
  assert.match(proposalSource, /Attach \$\{change\.label\} to AI composer/)
  assert.match(proposalSource, /Confirm enabled changes/)
  assert.match(proposalSource, /selectedOperationIds: enabledIds/)
  assert.match(proposalSource, /video_iteration_parent_revision_id/)
  assert.match(proposalSource, /video_iteration_candidate_revision_id/)
  assert.match(proposalSource, /video_iteration_change_id/)
  assert.match(proposalSource, /video_iteration_range_start_ms/)
  assert.match(proposalSource, /video_iteration_range_end_ms/)
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
