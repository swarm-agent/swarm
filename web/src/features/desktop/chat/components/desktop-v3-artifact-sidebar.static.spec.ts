import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const sidebarURL = new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url)
const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)

test('conversation sidebar toggles plan and session artifacts only when artifacts exist', async () => {
  const pane = await readFile(paneURL, 'utf8')

  assert.match(pane, /showPlanSidebar && hasSessionArtifacts/)
  assert.match(pane, /data-session-sidebar-toggle/)
  assert.match(pane, /aria-label="Show plan sidebar"/)
  assert.match(pane, /aria-label=\{`Show \$\{sessionArtifacts\.length\} session artifacts`\}/)
  assert.match(pane, /activeSidebarView === "artifacts"/)
  assert.match(pane, /desktopV3NextSessionSidebarView/)
})

test('artifact sidebar renders authorized thumbnail previews and opens full gallery selection', async () => {
  const [pane, sidebar, gallery] = await Promise.all([
    readFile(paneURL, 'utf8'),
    readFile(sidebarURL, 'utf8'),
    readFile(galleryURL, 'utf8'),
  ])

  assert.match(sidebar, /data-artifact-thumbnail-rail/)
  assert.match(sidebar, /fetchDesktopV3ArtifactPreviewToken/)
  assert.match(sidebar, /sandbox="allow-scripts"/)
  assert.match(sidebar, /referrerPolicy="no-referrer"/)
  assert.match(sidebar, /absolute left-0 top-0 size-\[400%\] origin-top-left scale-25/)
  assert.match(sidebar, /onOpenArtifact\(desktopV3ArtifactCatalogEntryKey\(artifact\)\)/)
  assert.match(pane, /initialArtifactKey=\{artifactGalleryInitialKey\}/)
  assert.match(gallery, /desktopV3ArtifactCatalogEntryForKey\(artifacts, initialArtifactKey\)/)
})
