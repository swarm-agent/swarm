import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const thumbnailURL = new URL('./desktop-v3-artifact-preview-thumbnail.tsx', import.meta.url)

test('rich previews navigate directly while bounded text remains fetched', async () => {
  const [gallery, thumbnail] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(thumbnailURL, 'utf8'),
  ])
  for (const source of [gallery, thumbnail]) {
    assert.match(source, /desktopV3ArtifactDirectContentURL/)
    assert.match(source, /fetchDesktopV3ArtifactPreviewAccess/)
    assert.match(source, /fetchDesktopV3ArtifactTextPreview/)
    assert.doesNotMatch(source, /fetchDesktopV3Artifact\(/)
    assert.doesNotMatch(source, /URL\.createObjectURL\(/)
    assert.doesNotMatch(source, /srcDoc=/)
  }
  assert.match(gallery, /Retry preview/)
  assert.match(gallery, /Download instead/)
  assert.match(gallery, /could not decode or load this video/)
})

test('HTML execution uses one production iframe URL and preserves nested srcdoc compatibility', async () => {
  const gallery = await readFile(galleryURL, 'utf8')
  assert.match(gallery, /selected\.mediaType === 'text\/html' && previewURL/)
  assert.match(gallery, /src=\{previewURL\} sandbox="allow-scripts"/)
  assert.doesNotMatch(gallery, /blob\.text\(\)/)
  assert.doesNotMatch(gallery, /buildDesktopV3ArtifactSandboxDocument/)
  // The server-owned runtime CSP admits nested data/blob frames; React must not
  // parse or rewrite a Canvas player that itself contains a srcdoc iframe.
  assert.doesNotMatch(gallery, /DOMParser/)
})
