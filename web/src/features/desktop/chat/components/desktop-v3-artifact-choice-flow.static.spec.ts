import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const composerURL = new URL('./desktop-v3-agentic-composer.tsx', import.meta.url)
const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const apiURL = new URL('../../session-v3/artifact-api.ts', import.meta.url)

test('gallery exposes accessible bounded multi-select without persisting Add to chat', async () => {
  const [gallery, api] = await Promise.all([readFile(galleryURL, 'utf8'), readFile(apiURL, 'utf8')])

  assert.match(gallery, /chatSelectedIds/)
  assert.match(gallery, /data-artifact-chat-selection/)
  assert.match(gallery, /aria-pressed=\{chatSelected\}/)
  assert.match(gallery, /DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT/)
  assert.match(gallery, /pendingChatArtifacts\.map/)
  assert.match(gallery, /Add \{pendingChatArtifacts\.length > 1/)
  assert.match(gallery, /This does not change the durable selected design/)
  assert.match(gallery, /Select this iteration/)
  assert.match(gallery, /selectedIsQueuedForChat/)
  const addAction = gallery.slice(gallery.indexOf('const emitAddToChat'), gallery.indexOf('const persistAndUseDesign'))
  assert.doesNotMatch(addAction, /useDesktopV3Artifact/)
  assert.match(api, /appendDesktopV3ArtifactMessageSelections/)
})

test('session sidebar and final handoff galleries feed the active existing composer', async () => {
  const [pane, composer] = await Promise.all([readFile(paneURL, 'utf8'), readFile(composerURL, 'utf8')])

  assert.match(pane, /queueGalleryArtifactSelections/)
  assert.match(pane, /queueGalleryArtifactSelections\(\[artifactSelectionRequest\]\)/)
  assert.match(pane, /requestSelections = appendDesktopV3ArtifactMessageSelections\(\[\], selections\)/)
  assert.match(pane, /artifactSelectionRequest=\{galleryArtifactSelectionRequest\}/)
  assert.match(pane, /requestAnimationFrame/)
  assert.match(pane, /onAddToChat=\{\(artifacts\) =>/)
  assert.match(pane, /onUseThisDesign=\{\(\{ label, selection \}\) =>/)
  assert.match(pane, /onSelectionPersisted=\{refreshSessionArtifacts\}/)
  assert.match(pane, /artifactCatalog=\{sessionArtifacts\}/)
  assert.match(pane, /entry\.artifactId === artifact\.artifactId/)
  assert.match(pane, /onAddToChat=\{\(artifacts\) => queueGalleryArtifactSelections/)
  assert.match(composer, /appendDesktopV3ArtifactMessageSelections/)
  assert.match(composer, /Array\.isArray\(artifactSelectionRequest\)/)
  assert.match(composer, /handledArtifactSelectionRequestRef/)
  assert.match(composer, /data-testid="desktop-composer-artifact-chip"/)
})

test('sent and pending user messages render their Designer iteration selections', async () => {
  const [pane, flow, state] = await Promise.all([
    readFile(paneURL, 'utf8'),
    readFile(new URL('../../session-v3/existing-session-flow.ts', import.meta.url), 'utf8'),
    readFile(new URL('../../state/desktop-v3-cache-types.ts', import.meta.url), 'utf8'),
  ])

  assert.match(pane, /artifactSelections=\{message\.artifact_selections\}/)
  assert.match(pane, /artifactSelections=\{message\.artifactSelections\}/)
  assert.match(pane, /data-testid="desktop-user-message-artifact-selections"/)
  assert.match(flow, /artifactSelections: operation\.request\.artifact_selections/)
  assert.match(state, /artifactSelections\?: DesktopV3ArtifactSelectionReference\[\]/)
})

test('Use this design remains a singular canonical action while message refs preserve intent', async () => {
  const [gallery, api] = await Promise.all([readFile(galleryURL, 'utf8'), readFile(apiURL, 'utf8')])

  assert.match(gallery, /canonicalSelection = await useDesktopV3Artifact/)
  assert.match(gallery, /await onUseThisDesign\(\{ label: selected\.label, selection: canonicalSelection \}\)/)
  assert.match(api, /normalized\.action === 'use'/)
  assert.match(api, /selection\.collection_id === normalized\.collection_id/)
  assert.match(api, /selection\.action === 'use'/)
})
