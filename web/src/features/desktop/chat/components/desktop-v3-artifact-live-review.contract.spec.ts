import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const realtimeURL = new URL('../../realtime/v3-client-effect-runner.ts', import.meta.url)
const refreshURL = new URL('../../session-v3/artifact-catalog-refresh.ts', import.meta.url)
const artifactAPIURL = new URL('../../session-v3/artifact-api.ts', import.meta.url)
const runtimeAssetsURL = new URL('../../session-v3/artifact-animation-runtime-assets.ts', import.meta.url)
const viteConfigURL = new URL('../../../../../vite.config.ts', import.meta.url)
const thirdPartyNoticesURL = new URL('../../../../../../THIRD_PARTY_NOTICES.md', import.meta.url)

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

test('animation previews use immutable local runtimes and keep network access disabled', async () => {
  const [gallery, artifactAPI, runtimeAssets, viteConfig, thirdPartyNotices] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(artifactAPIURL, 'utf8'),
    readFile(runtimeAssetsURL, 'utf8'),
    readFile(viteConfigURL, 'utf8'),
    readFile(thirdPartyNoticesURL, 'utf8'),
  ])
  assert.match(artifactAPI, /normalizeDesktopV3ArtifactAnimationProfile/)
  assert.match(artifactAPI, /connect-src 'none'/)
  assert.match(artifactAPI, /Animation runtime assets must use the reviewed local runtime path/)
  assert.match(artifactAPI, /Animation runtime assets must be same-install URLs/)
  assert.match(artifactAPI, /Secure animation preview nonce generation is unavailable/)
  assert.match(artifactAPI, /querySelectorAll\('script'\).*script\.setAttribute\('nonce', scriptNonce\)/)
  assert.doesNotMatch(artifactAPI, /querySelectorAll\('script:not\(\[nonce\]\)'\)/)
  assert.match(runtimeAssets, /swarm-animation-runtime/)
  assert.match(runtimeAssets, /three\.module\.js/)
  assert.match(runtimeAssets, /dotlottie-player\.wasm/)
  assert.match(runtimeAssets, /rive\.wasm/)
  assert.match(viteConfig, /THIRD_PARTY_NOTICES\.md/)
  assert.match(viteConfig, /Access-Control-Allow-Origin/)
  assert.match(viteConfig, /Cross-Origin-Resource-Policy', 'cross-origin'/)
  for (const runtime of ['@lottiefiles/dotlottie-web', '@rive-app/canvas', 'three']) {
    assert.match(thirdPartyNotices, new RegExp(runtime.replaceAll('.', '\\.')))
  }
  assert.match(thirdPartyNotices, /Assets must be\s+created by the user, owned by the user, or supplied under terms/)
  assert.match(gallery, /desktopV3ArtifactLocalRuntimeAssets\(selected\.animationProfile\)/)
  assert.match(gallery, /formatDesktopV3ArtifactAnimationProfile\(selected\?\.animationProfile\)/)
  assert.match(gallery, /selected\.animationProfile\.profileId === 'final_render'/)
  assert.match(gallery, /const selectedAnimationActive = animationPreviewVisible/)
  assert.doesNotMatch(gallery, /selectedAnimationActive = animationPreviewVisible &&[\s\S]*previewMotionAllowed/)
  assert.match(gallery, /selectedAnimationActive && selected\.mediaType === 'text\/html'/)
  assert.match(gallery, /selectedVideoProfileCompatible = !selected\?\.animationProfile \|\| selected\.animationProfile\.profileId === 'final_render'/)
})

test('artifact previews fit the available viewport and offer explicit fullscreen viewing', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /data-artifact-preview-surface/)
  assert.match(gallery, /selected\.mediaType\.startsWith\('image\/'\).*\? 'overflow-hidden' : 'overflow-auto'/s)
  assert.match(gallery, /className="grid size-full min-h-0 place-items-center"/)
  assert.match(gallery, /className="size-full rounded-lg border.*object-contain shadow-sm"/)
  assert.match(gallery, /selected\.sourceRef[\s\S]*fetchDesktopV3ArtifactDownload\(selected, controller\.signal\)/)
  assert.match(gallery, /previewSurface\.requestFullscreen\(\)/)
  assert.match(gallery, /document\.exitFullscreen\(\)/)
  assert.match(gallery, /aria-label="View artifact fullscreen"/)
  assert.match(gallery, /aria-label="Exit fullscreen artifact preview"/)
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
