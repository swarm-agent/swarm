import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)
const thumbnailURL = new URL('./desktop-v3-artifact-preview-thumbnail.tsx', import.meta.url)
const sidebarURL = new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url)

test('rich previews navigate directly while bounded text remains fetched', async () => {
  const [gallery, thumbnail, sidebar] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(thumbnailURL, 'utf8'),
    readFile(sidebarURL, 'utf8'),
  ])
  for (const source of [gallery, thumbnail, sidebar]) {
    assert.match(source, /desktopV3ArtifactDirectContentURL/)
    assert.match(source, /fetchDesktopV3ArtifactPreviewAccess/)
    assert.doesNotMatch(source, /fetchDesktopV3Artifact\(/)
    assert.doesNotMatch(source, /srcDoc=/)
  }
  for (const source of [thumbnail, sidebar]) assert.doesNotMatch(source, /URL\.createObjectURL\(/)
  assert.match(gallery, /triggerBlobDownload/)
  for (const source of [gallery, thumbnail]) assert.match(source, /fetchDesktopV3ArtifactTextPreview/)
  assert.match(gallery, /Retry preview/)
  assert.match(gallery, /Download instead/)
  assert.match(gallery, /could not decode or load this video/)
})

test('gallery preview visibility observes a late-mounted routed preview surface', async () => {
  const [gallery, thumbnail] = await Promise.all([
    readFile(galleryURL, 'utf8'),
    readFile(thumbnailURL, 'utf8'),
  ])
  assert.match(thumbnail, /const \[previewElement, setPreviewElement\] = useState<T \| null>\(null\)/)
  assert.match(thumbnail, /const previewRef = useCallback\(\(element: T \| null\) => setPreviewElement\(element\), \[\]\)/)
  assert.match(thumbnail, /observeArtifactPreview\(previewElement, setIntersecting\)/)
  assert.match(thumbnail, /\[enabled, previewElement\]/)
  assert.match(gallery, /animationPreviewRef\(node\)/)
  assert.doesNotMatch(gallery, /animationPreviewRef\.current = node/)
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
