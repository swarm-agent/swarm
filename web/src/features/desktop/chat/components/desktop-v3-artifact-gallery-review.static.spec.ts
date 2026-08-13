import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)

test('artifact gallery is a live collection review surface distinct from plan execution', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /data-artifact-review-surface/)
  assert.match(gallery, /data-artifact-collection-sidebar/)
  assert.match(gallery, /Live collections/)
  assert.match(gallery, /Collection progress/)
  assert.match(gallery, /generating/)
  assert.match(gallery, /failed/)
  assert.match(gallery, /Search collections, variants, sessions, or types/)
  assert.match(gallery, /aria-label="Previous artifact"/)
  assert.match(gallery, /aria-label="Next artifact"/)
  assert.doesNotMatch(gallery, /DesktopPlanExecutionSidebar/)
})

test('gallery chat actions emit opaque references and persist use before callback', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /export interface DesktopV3ArtifactChatSelection/)
  assert.match(gallery, /selection: DesktopV3ArtifactSelection/)
  assert.match(gallery, /onAddToChat\(\{ label: selected\.label, selection: desktopV3ArtifactSelection\(selected\) \}\)/)
  assert.match(gallery, /canonicalSelection = await useDesktopV3Artifact\(desktopV3ArtifactSelection\(selected\)\)/)
  assert.match(gallery, /await onUseThisDesign\(\{ label: selected\.label, selection: canonicalSelection \}\)/)
  assert.match(gallery, /data-artifact-selected-design/)
  assert.match(gallery, /Add to chat<\/button>/)
  assert.match(gallery, /Use this design<\/button>/)
  assert.doesNotMatch(gallery, /arrayBuffer\(\)/)
  assert.doesNotMatch(gallery, /onAddToChat\(\{[^}]*content:/)
})

test('open catalog consumes realtime refresh demand and keeps previews sandboxed', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /useDesktopV3OpenArtifactCatalogRefresh\(open, refreshCatalog\)/)
  assert.match(gallery, /setArtifacts\(await fetchDesktopV3ArtifactCatalog\(\)\)/)
  assert.match(gallery, /sandbox="allow-scripts"/)
  assert.match(gallery, /sandbox=""/)
  assert.match(gallery, /referrerPolicy="no-referrer"/)
  assert.doesNotMatch(gallery, /allow-same-origin/)
})
