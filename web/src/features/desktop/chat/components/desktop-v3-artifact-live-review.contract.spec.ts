import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const realtimeURL = new URL('../../realtime/v3-client-effect-runner.ts', import.meta.url)
const refreshURL = new URL('../../session-v3/artifact-catalog-refresh.ts', import.meta.url)

test('live artifact review presents multi-variant progress and refreshes from durable artifact events', async () => {
  const [gallery, realtime, refresh] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(realtimeURL, 'utf8'),
    readFile(refreshURL, 'utf8'),
  ])

  assert.match(gallery, /collectionGroups\(visibleArtifacts\)/)
  assert.match(gallery, /Collection variants/)
  assert.match(gallery, /group\.progress\.staging/)
  assert.match(gallery, /group\.progress\.ready/)
  assert.match(gallery, /group\.progress\.failed \+ group\.progress\.unavailable/)
  assert.match(gallery, /This variant is still generating/)
  assert.match(gallery, /The live review surface will refresh when it is ready/)
  assert.match(gallery, /useDesktopV3OpenArtifactCatalogRefresh\(open, refreshCatalog\)/)

  for (const eventType of [
    'session.artifact.created',
    'session.artifact.updated',
    'session.artifact.finalized',
    'session.artifact.failed',
    'session.artifact.unavailable',
    'session.artifact.selected',
  ]) {
    assert.match(realtime, new RegExp(eventType.replaceAll('.', '\\.')))
  }
  assert.match(realtime, /effects: \[\{ type: 'refresh_artifacts' \}\]/)
  assert.match(realtime, /refreshArtifacts: refreshOpenDesktopV3ArtifactCatalogs/)
  assert.match(refresh, /Promise\.allSettled\(active\.map\(\(listener\) => listener\(\)\)\)/)
  assert.match(refresh, /if \(this\.pendingDrain\) return this\.pendingDrain/)
})

test('review navigation and durable selection stay collection-scoped', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /const selectedVariants = selectedGroup\?\.entries \?\? \[\]/)
  assert.match(gallery, /const nextIndex = \(selectedVariantIndex \+ offset \+ selectedVariants\.length\) % selectedVariants\.length/)
  assert.match(gallery, /event\.key === 'ArrowLeft' \|\| event\.key === 'ArrowRight'/)
  assert.match(gallery, /aria-label="Previous artifact"/)
  assert.match(gallery, /aria-label="Next artifact"/)
  assert.match(gallery, /canonicalSelection = await useDesktopV3Artifact\(desktopV3ArtifactSelection\(selected\)\)/)
  assert.match(gallery, /setDurableSelectedId\(artifactSelectionKey\(selected\)\)/)
  assert.match(gallery, /await onSelectionPersisted\?\.\(\)/)
  assert.match(gallery, /data-artifact-selected-design/)
})
