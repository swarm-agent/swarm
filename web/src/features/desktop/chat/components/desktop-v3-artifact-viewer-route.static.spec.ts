import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)

test('artifact viewer route remains authoritative for modal visibility and session changes', async () => {
  const pane = await readFile(paneURL, 'utf8')

  assert.match(pane, /if \(!artifactViewerLocation\) \{\s+dismissedArtifactViewerLocationKeyRef\.current = "";\s+setArtifactGalleryOpen\(false\);/)
  assert.match(pane, /setSessionArtifacts\(\[\]\);\s+setSidebarView\("plan"\);\s+setArtifactGalleryOpen\(false\);\s+setArtifactGalleryInitialKey\(""\);\s+setArtifactGalleryInitialCollectionId\(""\);/)
  assert.match(pane, /initialCollectionId=\{artifactGalleryInitialCollectionId\}\s+artifactHref=\{artifactViewerHref\}\s+collectionHref=\{artifactCollectionViewerHref\}\s+onArtifactNavigate=\{navigateArtifactViewer\}\s+onCollectionNavigate=\{navigateArtifactCollectionViewer\}/)
  assert.match(pane, /artifactViewerLocation\.collectionId && !artifactViewerLocation\.artifactId/)
  assert.match(pane, /const navigateArtifactViewer = useCallback[\s\S]*if \(artifactReviewPresentation === "embedded" \|\| !routeWorkspaceSlug\) return;[\s\S]*desktopV3ArtifactViewerSearch\(artifact\)/)
  assert.doesNotMatch(pane, /artifactReviewPresentation === "embedded" \|\| artifact\.lineage\?\.iterationSectionId/)
})

test('Artifact Studio branch changes keep the exact variant in the deep link', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /const selectStudioArtifact = \(artifact:[\s\S]*setStudioActiveBranchId\(artifactSelectionKey\(artifact\)\);?\s+selectArtifact\(artifact\)/)
  assert.doesNotMatch(gallery, /selectStudioArtifact[\s\S]{0,300}selectArtifact\(artifact, false\)/)
})

test('Artifact Studio resolves an active complete head when a deep link has no in-memory branch choice', async () => {
  const gallery = await readFile(galleryURL, 'utf8')

  assert.match(gallery, /const activeIterationAlternative =[\s\S]*\?\? selectedIterationAlternative\s+\?\? iterationSectionAlternatives\.at\(-1\)/)
  assert.match(gallery, /const targetSectionId = selected \? desktopV3ArtifactStudioSectionLineage\(artifacts, selected\)\?\.iterationSectionId : ''/)
})
