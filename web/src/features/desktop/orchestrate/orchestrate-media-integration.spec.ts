import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

test('OrchestrateView integrates studio Media Center and removes tiny accept modal', () => {
  // Invariant: Deliverable thumbnails click to studio MediaViewerModal,
  // the tiny modal with "Accept Deliverable" is completely removed,
  // and Media Center integration is available from sidebar and deliverables view.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Verify "Accept Deliverable" button was eliminated
  assert.equal(source.includes('Accept Deliverable'), false, 'Deprecated "Accept Deliverable" button must be removed')

  // Verify MediaViewerModal & HistoricalMediaLibrary are integrated
  assert.ok(source.includes('<MediaViewerModal'), 'OrchestrateView must render studio MediaViewerModal')
  assert.ok(source.includes('<HistoricalMediaLibrary'), 'OrchestrateView must render full HistoricalMediaLibrary')
  assert.ok(source.includes('Media Studio & Library'), 'Sidebar must include Media Studio & Library entry')
  assert.ok(source.includes('Open Media Studio'), 'Deliverables tab must offer button to open Media Studio')
})

test('OrchestrateView provides Uploaded Media Shelf with upload, paste doc, and remove capabilities', () => {
  // Invariant: Uploaded media persists in an uploaded media shelf,
  // supporting file uploads, pasted docs/specs, tagging, and individual removal.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('Uploaded Media'), 'Must render Uploaded Media shelf')
  assert.ok(source.includes('handleFileUpload'), 'Must provide handleFileUpload')
  assert.ok(source.includes('handleDeleteUploadedMedia'), 'Must provide handleDeleteUploadedMedia')
  assert.ok(source.includes('handleAddPastedDoc'), 'Must provide handleAddPastedDoc')
  assert.ok(source.includes('Paste Document / Markdown Spec'), 'Must provide Paste Doc modal')
  assert.ok(source.includes('/v3/projects/${selectedProject.id}/media'), 'Must integrate with backend project media API')
})

test('OrchestrateView supports tagging media and forwarding to tasks', () => {
  // Invariant: Media items can be tagged from deliverables, viewer, or uploaded shelf,
  // rendering as attached media chips and forwarding attached_media to task creation.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('toggleTagDeliverable'), 'Must support toggleTagDeliverable')
  assert.ok(source.includes('toggleTagMediaRef'), 'Must support toggleTagMediaRef')
  assert.ok(source.includes('Attached Media'), 'Must render Attached Media bar in deploy modal')
  assert.ok(source.includes('attached_media: taggedMedia'), 'Must pass attached_media in task deployment payload')
})

test('OrchestrateView and Task Router scale swarm variants up to 25', () => {
  // Invariant: UI allows scaling up to 25 variants for swarm generation,
  // facilitating high-iteration non-blocking batches.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')
  const viewerPath = path.join(__dirname, '../tools/media-library/media-viewer-modal.tsx')
  const viewerSource = fs.readFileSync(viewerPath, 'utf8')

  assert.ok(source.includes('[1, 2, 4, 5, 10, 25]'), 'Variant selector must support up to 25 variants')
  assert.ok(source.includes('onIterateSwarm'), 'OrchestrateView must pass onIterateSwarm handler')
  assert.ok(viewerSource.includes('Swarm Iterations'), 'Viewer must offer Swarm Iterations action')
})

test('MediaViewerModal provides interactive Quick-Route panel with Fine-Tune, Iterations, and Video continuity', () => {
  // Invariant: MediaViewerModal must offer dedicated Quick Route actions for
  // Fine-Tuning ("change this to..."), Swarm Iterations, Keyframe-to-Video, and Next Scene continuation.
  const viewerPath = path.join(__dirname, '../tools/media-library/media-viewer-modal.tsx')
  const viewerSource = fs.readFileSync(viewerPath, 'utf8')

  assert.ok(viewerSource.includes('Fine-Tune'), 'Must render Fine-Tune button')
  assert.ok(viewerSource.includes('Swarm Iterations'), 'Must render Swarm Iterations button')
  assert.ok(viewerSource.includes('To Video'), 'Must offer To Video conversion for image keyframes')
  assert.ok(viewerSource.includes('Next Scene'), 'Must offer Next Scene continuation for videos')
  assert.ok(viewerSource.includes('Route & Run Now'), 'Must provide 1-click Route & Run Now button')
  assert.ok(viewerSource.includes('In Planner'), 'Must provide In Planner button')
  assert.ok(viewerSource.includes('presetSuggestions'), 'Must provide quick preset chips')
})

test('OrchestrateView handles quick media routing for fine-tuning and iterations with instant deploy', () => {
  // Invariant: OrchestrateView must implement handleQuickRouteMedia supporting
  // fine_tune, iterate, to_video, next_scene, and 1-click auto-deploy without manual asking each time.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('handleQuickRouteMedia'), 'Must implement handleQuickRouteMedia')
  assert.ok(source.includes("action === 'fine_tune'"), 'Must handle fine_tune action')
  assert.ok(source.includes("action === 'iterate'"), 'Must handle iterate action')
  assert.ok(source.includes("action === 'to_video'"), 'Must handle to_video action')
  assert.ok(source.includes("action === 'next_scene'"), 'Must handle next_scene action')
  assert.ok(source.includes('auto_approve: true'), 'Must support auto_approve for 1-click execution')
  assert.ok(source.includes('handleOpenUploadedInMediaCenter'), 'Must support opening uploaded media in studio modal')
})

test('OrchestrateView safely formats media deliverables with parseSafeDate and safeIsoDayKey without RangeError', () => {
  // Invariant: Media deliverable date handling must safely parse relative strings like "Just now",
  // invalid date strings, and numeric timestamps without throwing RangeError: Invalid time value on toISOString.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('parseSafeDate'), 'Must implement parseSafeDate helper')
  assert.ok(source.includes('safeIsoDayKey'), 'Must implement safeIsoDayKey helper')
  assert.ok(!source.includes("'/v3/automations/v2?action=list'"), 'Must not send invalid ?action=list query to automations endpoint')
  assert.ok(source.includes("'/v3/automations/v2'"), 'Must query /v3/automations/v2 cleanly')
})
