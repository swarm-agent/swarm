import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const librarySourceUrl = new URL('./historical-media-library.tsx', import.meta.url)
const modalSourceUrl = new URL('./media-viewer-modal.tsx', import.meta.url)
const gridSourceUrl = new URL('./media-grid-view.tsx', import.meta.url)
const listSourceUrl = new URL('./media-list-view.tsx', import.meta.url)
const iterationSourceUrl = new URL('./media-iteration-groups-view.tsx', import.meta.url)
const videoPageSourceUrl = new URL('../pages/video-tool-page.tsx', import.meta.url)

test('HistoricalMediaLibrary provides explorer toolbar with type filters, grouping, and view switchers', () => {
  const source = readFileSync(librarySourceUrl, 'utf8')

  // Type filter tabs with item counts
  assert.match(source, /All Media/)
  assert.match(source, /Images/)
  assert.match(source, /Videos/)
  assert.match(source, /Audio/)
  assert.match(source, /Animations/)

  // Grouping options: Date & Day vs Iteration Groups
  assert.match(source, /Group by:/)
  assert.match(source, /Date & Day/)
  assert.match(source, /Iteration Groups/)

  // View modes: Grid / Thumbnails vs List
  assert.match(source, /Thumbnails view/)
  assert.match(source, /List view/)

  // Search input
  assert.match(source, /Search by name, session, prompt\.\.\./)
  assert.match(source, /Clear search/)

  // Sort dropdown
  assert.match(source, /Newest First/)
  assert.match(source, /Oldest First/)
  assert.match(source, /Name \(A-Z\)/)
  assert.match(source, /By Type/)

  // Refresh capability
  assert.match(source, /Refresh media catalog/)
})

test('MediaViewerModal provides rich playback and inspection for all media types', () => {
  const source = readFileSync(modalSourceUrl, 'utf8')

  // Image zoom controls
  assert.match(source, /Zoom in/)
  assert.match(source, /Zoom out/)
  assert.match(source, /Reset zoom/)

  // Video playback
  assert.match(source, /<video[\s\S]*?controls[\s\S]*?autoPlay/)

  // Audio playback
  assert.match(source, /<audio[\s\S]*?controls[\s\S]*?autoPlay/)

  // Animation sandbox
  assert.match(source, /<iframe[\s\S]*?sandbox="allow-scripts"/)

  // Actions
  assert.match(source, /Copy link|Copy direct URL/)
  assert.match(source, /Download/)
  assert.match(source, /Open session/)

  // Keyboard navigation
  assert.match(source, /ArrowLeft/)
  assert.match(source, /ArrowRight/)
  assert.match(source, /Escape/)

  // Metadata pane
  assert.match(source, /Metadata & Details/)
  assert.match(source, /Artifact ID/)
  assert.match(source, /Source Session/)
  assert.match(source, /Created \/ Generated/)
  assert.match(source, /Iteration Group/)
})

test('MediaGridView renders desktop file-manager style thumbnails with type badges and date headers', () => {
  const source = readFileSync(gridSourceUrl, 'utf8')

  assert.match(source, /date-header-/)
  assert.match(source, /MediaThumbnailCard/)
  assert.match(source, /typeSummary/)
  assert.match(source, /aspectClass/)
})

test('MediaListView renders tabular file-manager rows with metadata columns', () => {
  const source = readFileSync(listSourceUrl, 'utf8')

  assert.match(source, /<table/)
  assert.match(source, /Dimensions \/ Info/)
  assert.match(source, /Iteration Group/)
  assert.match(source, /Date Generated/)
  assert.match(source, /onSelectItem/)
})

test('MediaIterationGroupsView displays grouped wave/chain iteration cards', () => {
  const source = readFileSync(iterationSourceUrl, 'utf8')

  assert.match(source, /group\.itemCount/)
  assert.match(source, /group\.title/)
  assert.match(source, /variant|variants/)
  assert.match(source, /View all/)
})

test('VideoToolPage integrates HistoricalMediaLibrary under Studio with navigation tabs', () => {
  const source = readFileSync(videoPageSourceUrl, 'utf8')

  // Header tabs: Video Editor vs Media Library
  assert.match(source, /aria-label="Studio views"/)
  assert.match(source, /Video Editor/)
  assert.match(source, /Media Library/)

  // State & search param sync
  assert.match(source, /studioSearch\.view === 'media' \? 'media' : 'editor'/)
  assert.match(source, /activeStudioTab === 'media'/)
  assert.match(source, /<HistoricalMediaLibrary/)
})

test('DesktopAppPage has Media navigation button below Studio in sidebar', () => {
  const appPageSourceUrl = new URL('../../layout/desktop-app-page.tsx', import.meta.url)
  const appPageSource = readFileSync(appPageSourceUrl, 'utf8')

  assert.match(appPageSource, /aria-label="Open Media"/)
  assert.match(appPageSource, /title="Media"/)
  assert.match(appPageSource, /data-testid="sidebar-media-btn"/)
  assert.match(appPageSource, /search:\s*\{\s*view:\s*'media'\s*\}/)

  const studioIdx = appPageSource.indexOf('aria-label="Open Studio"')
  const mediaIdx = appPageSource.indexOf('aria-label="Open Media"')
  const environmentsIdx = appPageSource.indexOf('aria-label="Open Environments"')
  assert.ok(studioIdx !== -1 && mediaIdx !== -1 && environmentsIdx !== -1)
  assert.ok(studioIdx < mediaIdx && mediaIdx < environmentsIdx, 'expected Media to be below Studio and above Environments')
})
