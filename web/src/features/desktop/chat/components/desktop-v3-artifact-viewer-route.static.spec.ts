import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)

test('artifact viewer route remains authoritative for modal visibility and session changes', async () => {
  const pane = await readFile(paneURL, 'utf8')

  assert.match(pane, /if \(!artifactViewerLocation\) \{\s+dismissedArtifactViewerLocationKeyRef\.current = "";\s+setArtifactGalleryOpen\(false\);/)
  assert.match(pane, /setSessionArtifacts\(\[\]\);\s+setSidebarView\("plan"\);\s+setArtifactGalleryOpen\(false\);\s+setArtifactGalleryInitialKey\(""\);\s+setArtifactGalleryInitialCollectionId\(""\);/)
  assert.match(pane, /initialCollectionId=\{artifactGalleryInitialCollectionId\}\s+artifactHref=\{artifactViewerHref\}\s+collectionHref=\{artifactCollectionViewerHref\}\s+onArtifactNavigate=\{navigateArtifactViewer\}\s+onCollectionNavigate=\{navigateArtifactCollectionViewer\}/)
  assert.match(pane, /artifactViewerLocation\.collectionId && !artifactViewerLocation\.artifactId/)
  assert.match(pane, /artifactReviewPresentation === "embedded" \|\| artifact\.lineage\?\.iterationSectionId \|\| !routeWorkspaceSlug/)
})
