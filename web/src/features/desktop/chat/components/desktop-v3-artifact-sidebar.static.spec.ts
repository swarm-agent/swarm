import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

const paneURL = new URL('./desktop-v3-existing-conversation-pane.tsx', import.meta.url)
const sidebarURL = new URL('./desktop-v3-artifact-sidebar.tsx', import.meta.url)
const galleryURL = new URL('./desktop-v3-artifact-gallery.tsx', import.meta.url)

test('conversation sidebar toggles plan and session artifacts only when artifacts exist', async () => {
  const pane = await readFile(paneURL, 'utf8')

  assert.match(pane, /showPlanSidebar && hasSessionArtifacts && !pendingPlanDocument/)
  assert.match(pane, /data-session-sidebar-toggle/)
  assert.match(pane, /aria-label="Show plan sidebar"/)
  assert.match(pane, /aria-label=\{`Show \$\{sessionArtifacts\.length\} session artifacts`\}/)
  assert.match(pane, /activeSidebarView === "artifacts"/)
  assert.match(pane, /desktopV3NextSessionSidebarView/)
  assert.match(pane, /prioritizePlan: Boolean\(pendingPlanDocument\) \|\| \(showPlanSidebar && !previousHasPlan\)/)
  assert.match(pane, /activeSidebarView = pendingPlanDocument[\s\S]*\? "plan"/)
})

test('pending plan sidechat takes over the sidebar and opens as a mobile sheet', async () => {
  const pane = await readFile(paneURL, 'utf8')

  assert.match(pane, /setPlanAgentMobileOpen\(Boolean\(pendingPlanPermission\?\.id\)\)/)
  assert.match(pane, /pendingPlanDocument[\s\S]*\? "contents min-\[1300px\]:flex/)
  assert.match(pane, /pendingPlanDocument && pendingPlanPermission[\s\S]*<DesktopPlanAgentSidecar/)
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
  assert.match(sidebar, /className="size-full object-contain"/)
  assert.match(sidebar, /className="relative grid h-20 place-items-center overflow-hidden"/)
  assert.doesNotMatch(sidebar, /aspect-\[16\/9\]/)
  assert.match(sidebar, /href=\{artifactHref\(artifact\)\}/)
  assert.match(sidebar, /desktopV3ArtifactSidebarGroups\(artifacts\)/)
  assert.match(sidebar, /data-artifact-collection-group/)
  assert.match(sidebar, /Iteration Swarm generating/)
  assert.match(sidebar, /collectionHref\(representative\)/)
  assert.match(sidebar, /event\.preventDefault\(\)/)
  assert.match(sidebar, /onOpenArtifact\(artifact\)/)
  assert.match(sidebar, /onAddToChat/)
  assert.match(sidebar, /Add \$\{artifact\.label\} to chat/)
  assert.match(sidebar, /MessageSquarePlus/)
  assert.match(sidebar, /formatDesktopV3ArtifactOutputRequirements\(representative\.outputRequirements\)/)
  assert.match(sidebar, /data-artifact-output-requirements/)
  assert.match(gallery, /formatDesktopV3ArtifactOutputRequirements\(selected\?\.outputRequirements\)/)
  assert.match(gallery, /formatDesktopV3ArtifactOutputRequirements\(group\.entries\[0\]\?\.outputRequirements\)/)
  assert.match(gallery, /Requested output target; not measured binary dimensions/)
  assert.match(pane, /useParams\(\{ strict: false \}\)/)
  assert.match(pane, /typeof routeParams\.workspaceSlug === "string"/)
  assert.match(pane, /desktopV3ArtifactViewerHref\(routeWorkspaceSlug/)
  assert.match(pane, /sessionId: normalizedSessionId/)
  assert.match(pane, /desktopV3ArtifactCollectionViewerHref\(routeWorkspaceSlug/)
  assert.match(pane, /desktopV3ArtifactViewerSearch\(\{ \.\.\.artifact, sessionId: normalizedSessionId \}\)/)
  assert.match(pane, /desktopV3ArtifactCollectionViewerSearch/)
  assert.match(pane, /desktopV3ArtifactCatalogEntryForViewerLocation\(sessionArtifacts, artifactViewerLocation\)/)
  assert.match(pane, /initialArtifactKey=\{artifactGalleryInitialKey\}/)
  assert.match(gallery, /desktopV3ArtifactCatalogEntryForKey\(artifacts, initialArtifactKey\)/)
  assert.match(gallery, /Group URL/)
})
