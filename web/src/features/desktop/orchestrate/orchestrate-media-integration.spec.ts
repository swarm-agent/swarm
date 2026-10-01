// Compact-form regression purpose: keep model/capability/default/prompt wiring while
// moving redundant copy to media-task-controls help. Updated form-only assertions
// follow the new contract; unrelated payload, pricing and completed-card checks remain.
// Authority: OrchestrateView New Task JSX and MediaTaskSelect/Default leaf markup.
// Layer: source wiring plus rendered leaves, not browser/pixel or live media proof.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { resolveImagePricing, resolveVideoPricing, resolveAudioPricing } from './OrchestrateView'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskSelect, MediaTaskDefault } from './media-task-controls'

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
  assert.ok(source.includes('attached_media: attachedMediaForTask'), 'Must pass attached_media in task deployment payload')
})

test('OrchestrateView and Task Router scale swarm variants up to 25', () => {
  // Invariant: UI allows scaling up to 25 variants for swarm generation,
  // facilitating high-iteration non-blocking batches.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')
  const viewerPath = path.join(__dirname, '../tools/media-library/media-viewer-modal.tsx')
  const viewerSource = fs.readFileSync(viewerPath, 'utf8')

  assert.ok(source.includes('[1, 2, 4, 5, 10, 25]'), 'Variant selector must support up to 25 variants')
  assert.ok(source.includes('onGenerate={handleMediaGenerate}'), 'OrchestrateView must pass the typed generation handler')
  assert.ok(viewerSource.includes('Swarm Iterations'), 'Viewer must offer Swarm Iterations action')
})

test('MediaViewerModal provides interactive Quick-Route panel with Fine-Tune, Iterations, and Video continuity', () => {
  // Invariant: MediaViewerModal must offer dedicated Quick Route actions for
  // Fine-Tuning ("change this to..."), Swarm Iterations, Keyframe-to-Video, and Next Scene continuation.
  // Stale 'In Planner' and hardcoded 'presetSuggestions' chips are removed.
  const viewerPath = path.join(__dirname, '../tools/media-library/media-viewer-modal.tsx')
  const viewerSource = fs.readFileSync(viewerPath, 'utf8')

  assert.ok(viewerSource.includes('Fine-Tune'), 'Must render Fine-Tune button')
  assert.ok(viewerSource.includes('Swarm Iterations'), 'Must render Swarm Iterations button')
  assert.ok(viewerSource.includes('To Video'), 'Must offer To Video conversion for image keyframes')
  assert.ok(viewerSource.includes('Next Scene'), 'Must offer Next Scene continuation for videos')
  assert.ok(viewerSource.includes('Generate revision'), 'Must label the generation action clearly')
  assert.ok(!viewerSource.includes('>In Planner</button>'), 'Stale In Planner button must be removed')
  assert.ok(!viewerSource.includes('presetSuggestions.map'), 'Stale hardcoded preset suggestion chips must be removed')
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
})

test('OrchestrateView displays worktree name immediately and implements accurate integration lifecycle', () => {
  // Invariant: Cards must show worktree name right away, render Code PR deliverable icons
  // rather than image icons for coder tasks, suppress diffs before changes exist,
  // show dirty files when pending commit, show not integrated when commits are unmerged,
  // show integrated when merged, and warn when behind or out of sync.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Worktree name shown right away
  assert.ok(source.includes('task.worktreeBranch || (task.worktreeName ? `agent/${task.worktreeName}` : \'agent/worktree\')'), 'Must display worktree branch/name right away')
  assert.ok(!source.includes('Branch: <span className="text-indigo-300 font-semibold">{task.worktreeBranch'), 'Must not display raw backwards Branch: main')

  // 2. Code PR spec and deliverables
  assert.ok(source.includes('Code PR Spec - for code tasks'), 'Must render dedicated Code PR spec for coder tasks')
  assert.ok(source.includes('Pending Acceptance • Code PR'), 'Must label code deliverables with Code PR')
  assert.ok(source.includes('GitPullRequest'), 'Must import and use GitPullRequest icon')

  // 3. Image spec restricted to image tasks
  assert.ok(source.includes("(task.agentType === 'image' || task.outcomeType === 'media_bundle') && task.aspectRatio"), 'Must restrict Aspect Ratio display to image tasks')

  // 4. Git status suppression for pending approval, clean states, and media tasks
  assert.ok(source.includes('(task.isDirty || hasUnintegrated)'), 'Must not display change bar until changes exist waiting to be committed')
  assert.ok(source.includes('!isMediaTask'), 'Must suppress worktree and git changes for media tasks')

  // 5. Out of sync warning
  assert.ok(source.includes('Out of Sync Warning:'), 'Must render out of sync warning banner')
  assert.ok(source.includes('task.syncWarning'), 'Must check task.syncWarning')

  // Compact integration states replace the verbose banners. Pending, error,
  // retry, verified completion and unavailable lineage are exercised on the real
  // MinimalTaskCard in task-integration-operation.browser.spec.ts.
})

test('OrchestrateView renders Image and Video model dropdowns with availability warnings and settings persistence', () => {
  // Invariant: Deploy Task Modal must display model selectors for Image and Video intents,
  // show warning banners if 0 models connected, persist user model choice to ui-settings,
  // and forward selected model in task creation and quick routing payloads.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Model options interface and state
  assert.ok(source.includes('TaskModalModelOption'), 'Must declare TaskModalModelOption interface')
  assert.ok(source.includes('imageModelOptions'), 'Must manage imageModelOptions state')
  assert.ok(source.includes('videoModelOptions'), 'Must manage videoModelOptions state')
  assert.ok(source.includes('selectedImageModel'), 'Must manage selectedImageModel state')
  assert.ok(source.includes('selectedVideoModel'), 'Must manage selectedVideoModel state')

  // 2. Fetch media catalog and default models on mount
  assert.ok(source.includes("'/v1/media/settings/catalog'"), 'Must query media catalog on mount')
  // Source guard only: behavioral shared-cache assertions live in video-default-state.spec.ts.
  assert.ok(source.includes('useVideoTaskDefault(isDeployModalOpen, videoModelOptions)'), 'Video defaults must use shared canonical settings')

  // 3. Dropdowns with aria-labels and availability warning states
  assert.ok(source.includes('aria-label="Image Model"'), 'Must render Image Model dropdown')
  assert.ok(source.includes('No image models connected. Add Google or OpenAI key in Settings.'), 'Must display warning when no image models connected')
  assert.ok(source.includes('aria-label="Video Model"'), 'Must render Video Model dropdown')
  assert.ok(source.includes('No video models connected. Connect Google or OpenRouter key in Settings.'), 'Must display warning when no video models connected')

  // 4. Video default persistence is delegated to the canonical shared-cache hook.
  assert.ok(source.includes('handleImageModelChange'), 'Must provide handleImageModelChange')
  assert.ok(source.includes('handleVideoModelChange'), 'Must provide handleVideoModelChange')
  assert.ok(source.includes('tools: { image: { default_model: newModel } }'), 'Must persist image model choice')
  assert.ok(source.includes('const handleSetVideoAsDefault = videoDefaults.save'), 'Must persist video choice through canonical settings')

  // 5. Task creation passes selected model
  assert.ok(source.includes('model: qualifiedModel'), 'Deploy payload must pass the selected provider-qualified model')
  assert.ok(source.includes('(selectedImageModel || undefined)'), 'Image selection must feed the qualified model')
  assert.ok(source.includes("model: model || (targetIntent === 'image'"), 'Quick route media must honor the explicitly selected model first')
})

test('OrchestrateView retains explicit intents in separate Coding and Media groups', () => {
  // Requirement: OrchestrateView must distinguish coding from media without losing
  // any intent. This narrow source-wiring check guards accidental label removal;
  // rendered grouping, icon removal and selection are proved in the browser test.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('>Coding</legend>'), 'Must label the coding group')
  assert.ok(source.includes('>Media</legend>'), 'Must label the separate media group')
  assert.ok(source.includes('<span>Small Feature/Fix</span>'), 'Must retain the explicit small-task intent')
  assert.ok(source.includes('<span>Big Feature</span>'), 'Must retain the explicit planning intent')
  assert.ok(!source.includes('<span>Code / Feature</span>'), 'Must eliminate old "Code / Feature" text')
  assert.ok(source.includes('<span>Image</span>'), 'Must label image tab as "Image"')
  assert.ok(source.includes('<span>Video</span>'), 'Must label video tab as "Video"')
  assert.ok(!source.includes('<span>Video Story</span>'), 'Must eliminate old "Video Story" tab label')
  assert.ok(source.includes('<span>Sounds</span>'), 'Must provide dedicated "Sounds" tab')
  assert.ok(source.includes('<span>Audit</span>'), 'Must label audit tab as "Audit"')
  assert.ok(!source.includes('<span>Audit / Finder</span>'), 'Must eliminate old "Audit / Finder" text')
})

test('OrchestrateView supports single video clips with independent 1-8 clip count and verified duration/aspect controls', () => {
  // Invariant: Video tab generates direct single video shots with native audio synthesis from 1 prompt,
  // supporting 1..8 independent clip counts and exact model generation options (aspect ratio, resolution, duration).
  // Source wiring only: explicit scenes assemble separately from Video Studio timeline/audio editing.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Compact rendering delegates explanatory prose and optional scenes to accessible help.
  const controls = fs.readFileSync(path.join(__dirname, 'media-task-controls.tsx'), 'utf8')
  assert.ok(source.includes('your prompt goes directly to the selected model'), 'Help must describe direct mode')
  assert.ok(controls.includes('Timeline editing and audio mixing are available in Video Studio'), 'Help must clarify Studio separation')
  assert.ok(source.includes('label="Clips" value={videoClipCount} values={[1, 2, 4, 8]}'), 'Must retain independent clip choices')
  assert.ok(source.includes('videoClipCount'), 'Must manage independent videoClipCount state')
  assert.ok(source.includes('supportedVideoDurations'), 'Must enforce model-supported durations')
  assert.ok(source.includes('supportedVideoResolutions'), 'Must enforce model-supported resolutions')
  assert.ok(source.includes('supportedVideoAspectRatios'), 'Must enforce model-supported aspect ratios')
  assert.ok(source.includes('<MediaTaskScenes value={videoScenePrompts} onChange={setVideoScenePrompts}'), 'Must retain controlled scenes for bounded multipart assembly')
  assert.ok(!source.includes('Add Dedicated Soundtrack Clip'), 'Must eliminate soundtrack clip controls from project modal')
})

test('OrchestrateView provides video resolution selection with model-supported transparency', () => {
  // Invariant: Video modal must allow switching between model-supported resolutions,
  // showing per-second or per-clip pricing rates extracted from catalog pricing so users know what they pay.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('values={supportedVideoResolutions} onChange={setVideoResolution}'), 'Must allow switching supported video resolution')
  assert.ok(source.includes('supportedVideoResolutions'), 'Must enforce model-supported resolutions')
  assert.ok(source.includes('resolveVideoPricing'), 'Must implement resolveVideoPricing helper')
  assert.ok(source.includes('<MediaTaskCost total={videoPricingInfo.totalPrice}'), 'Must display a consolidated video estimate')
  assert.ok(source.includes('videoPricingInfo.formattedSummary'), 'Must render formatted model pricing summary')
  assert.ok(source.includes("taskIntent === 'video'"), 'Must pass resolution in task payload')
})

test('OrchestrateView restricts video reference inputs to supported images and preserves prompt text', () => {
  // Invariant: Video reference inputs must strictly be supported image formats (.png, .jpg, .jpeg),
  // accepting at most 1 starting image. Text and documents stay directly in the prompt input without truncation.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('validateVideoAttachment'), 'Must validate video attachments with validateVideoAttachment')
  assert.ok(source.includes('Text stays in prompt for video task flow'), 'Must keep text/doc content in prompt input')
  assert.ok(source.includes('videoAttachmentError'), 'Must track and surface video attachment errors')
  assert.ok(source.includes("'image/png,image/jpeg,.png,.jpg,.jpeg'"), 'Must enforce supported video image extensions')
  assert.ok(source.includes('paste one PNG/JPEG image'), 'Help must retain the one-image input limit')
})

test('OrchestrateView provides dedicated Sounds tab with audio model dropdown, duration selector, and settings persistence', () => {
  // Invariant: Sounds tab must display Audio Model dropdown, duration buttons (15s, 30s, 60s, 120s),
  // persist default audio model to ui-settings, and pass sound intent and duration in task payload.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('aria-label="Audio Model"'), 'Must render Audio Model dropdown')
  assert.ok(source.includes('handleAudioModelChange'), 'Must provide handleAudioModelChange')
  assert.ok(source.includes('tools: { audio: { default_model: newModel } }'), 'Must persist audio model to ui-settings')
  assert.ok(source.includes('audioModelOptions'), 'Must manage audioModelOptions')
  assert.ok(source.includes('setSoundDuration'), 'Must manage soundDuration')
  assert.ok(source.includes('[15, 30, 60, 120]'), 'Must support 15s, 30s, 60s, 120s durations')
  assert.ok(source.includes("duration_seconds: taskIntent === 'sound' ? soundDuration"), 'Must pass duration_seconds in task payload')
})

test('OrchestrateView provides AI Prompt Enhancement toggle and Single Video Shot Spec without worktrees for media tasks', () => {
  // Invariant: Video tab must provide an explicit "Enhance prompt with AI" toggle (defaulting to false / direct),
  // passing enhance_prompt and video_type in task submission payload.
  // Single Video tasks render a dedicated Single Video Shot Spec (8s, model, resolution, native audio)
  // instead of multi-scene blueprints, and media tasks completely suppress worktree branches and git tracking badges.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. AI Prompt Enhancement toggle
  assert.ok(source.includes('enhanceVideoPrompt'), 'Must manage enhanceVideoPrompt state')
  assert.ok(source.includes('Enhance prompt'), 'Must render explicit enhancement choice')
  assert.ok(source.includes('Without enhancement, your prompt goes directly to the selected model'), 'Help must describe unchanged direct mode')
  assert.ok(source.includes('Enhancement uses Router to refine lighting and camera motion'), 'Help must describe Router enhancement')
  assert.ok(source.includes("video_type: taskIntent === 'video' ? (scenePrompts.length ? 'multipart' : 'single') : undefined"), 'Must pass video_type in task payload')
  assert.ok(source.includes(": taskIntent === 'video' ? enhanceVideoPrompt : undefined"), 'Video enhancement remains independent of the image opt-in branch; image POST behavior is exercised in image-task-prompt.spec.tsx')

  // 2. Single Video Shot Spec
  assert.ok(source.includes('Single Video Shot Spec'), 'Must render Single Video Shot Spec for single video tasks')
  assert.ok(source.includes('Direct Model Execution'), 'Must badge single video shot as Direct Model Execution')
  assert.ok(source.includes('Duration'), 'Must display Duration in single video spec')
  assert.ok(source.includes('8 Seconds'), 'Must display 8 Seconds in single video spec')
  assert.ok(source.includes('Model Native'), 'Must indicate Model Native audio synthesis')

  // 3. Media Task worktree suppression
  assert.ok(source.includes('const isMediaTask ='), 'Must define isMediaTask helper')
  assert.ok(source.includes('const isSingleVideo ='), 'Must define isSingleVideo helper')
  assert.ok(source.includes('!isMediaTask && (task.worktreeBranch || task.worktreeName)'), 'Must hide worktree badges for media tasks')
  assert.ok(source.includes('SINGLE VIDEO') || source.includes('Single Video'), 'Must display Single Video badge for video_clip outcome')
})

test('OrchestrateView enables natural task model override and in-menu option to change saved default for next time', () => {
  // Invariant: Changing the model in the modal naturally overrides for the current task without
  // silently forcing ui-settings, while providing an explicit option to change the default for next time
  // in the same menu, and showing which model is the saved default.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Default model tracking & resolution from catalog / settings
  assert.ok(source.includes('defaultImageModel'), 'Must manage defaultImageModel state')
  assert.ok(source.includes('defaultVideoModel'), 'Must manage defaultVideoModel state')
  assert.ok(source.includes('catalogRes?.default_image_model'), 'Must resolve default image model from catalog')
  assert.ok(source.includes('const defaultVideoModel = videoDefaults.defaultModel'), 'Must resolve default video model from catalog')

  // 2. Clear Default tagging in dropdowns
  assert.ok(source.includes("opt.id === defaultImageModel ? ' (Default)' : ''"), 'Must label default image model in dropdown')
  assert.ok(source.includes("opt.id === defaultVideoModel ? ' (Default)' : ''"), 'Must label default video model in dropdown')

  // 3. Task override and Change Default actions in the same menu
  // Purpose: the new contract permits ONE explicit save gesture, never a sticky checkbox.
  // Leaf rendering is the narrow observable layer; production selection callbacks are
  // exercised in media-task-controls.spec.tsx.
  const override = renderToStaticMarkup(React.createElement(MediaTaskDefault, { isDefault: false, onSave: () => {} }))
  assert.match(override, /This task only/)
  assert.match(override, /Set default/)
  assert.equal((override.match(/<button/g) || []).length, 1)
  assert.doesNotMatch(override, /checkbox/)
  assert.ok(source.includes('handleSetImageAsDefault'), 'Must provide handleSetImageAsDefault')
  assert.ok(source.includes('handleSetVideoAsDefault'), 'Must provide handleSetVideoAsDefault')
  assert.ok(source.includes('isDefault={selectedImageModel === defaultImageModel}'), 'Must show image default status')
  assert.ok(source.includes('isDefault={selectedVideoModel === defaultVideoModel}'), 'Must show video default status')
})

test('OrchestrateView provides 1k, 2k, and 4k image resolution options with per-resolution pricing transparency', () => {
  // Invariant: Image tab must provide 1K, 2K, and 4K resolution options, calculate per-variant
  // and per-resolution pricing from catalog pricing lines, and render an Estimated Model Cost summary.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('imageResolution'), 'Must manage imageResolution state')
  assert.ok(source.includes("['1k', '2k', '4k']"), 'Must offer 1k, 2k, and 4k resolution options')
  assert.ok(source.includes('resolveImagePricing'), 'Must implement resolveImagePricing helper')
  assert.ok(source.includes('imagePricingInfo'), 'Must compute imagePricingInfo')
  assert.ok(source.includes('imagePricingInfo.formattedSummary'), 'Must display formatted image pricing summary')
  assert.ok(source.includes('ratesByResolution'), 'Must provide rates by resolution')
  assert.ok(source.includes("taskIntent === 'image' ? imageResolution"), 'Must pass image resolution in task payload')
})

test('compact aspect selectors retain explicit image and catalog video ratio choices', () => {
  // Purpose: compacting removes decorative boxes, not supported ratio values.
  // Boundary: MediaTaskSelect and OrchestrateView capability wiring; narrow leaf markup
  // proves selectable values, not visual layout or upstream capability correctness.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Aspect ratio definitions with visual dimensional cues
  assert.ok(source.includes('IMAGE_ASPECT_RATIOS'), 'Must declare IMAGE_ASPECT_RATIOS with visual cues')
  assert.ok(source.includes('VIDEO_ASPECT_RATIOS'), 'Must declare VIDEO_ASPECT_RATIOS with visual cues')
  assert.ok(source.includes('Landscape'), 'Must include Landscape label')
  assert.ok(source.includes('Portrait'), 'Must include Portrait label')
  assert.ok(source.includes('Square'), 'Must include Square label')

  assert.ok(source.includes('values={supportedVideoAspectRatios} onChange={setVideoAspectRatio}'), 'Video ratios must remain catalog constrained')
  const values = ['16:9', '9:16', '1:1', '4:3']
  const html = renderToStaticMarkup(React.createElement(MediaTaskSelect, { label: 'Ratio', value: '9:16', values, onChange: () => {} }))
  for (const value of values) assert.ok(html.includes(`value="${value}"`))
  assert.match(html, /value="9:16" selected=""/)
  assert.match(html, /aria-label="Ratio"/)
})

test('resolveImagePricing dynamically scales total cost across variant count, resolution, and catalog lines', () => {
  // Invariant: Costs must be dynamic with user choices (variants and resolution), NOT static estimates for 1 image.
  // When variant count changes (e.g. 5x), total cost and resolution option tags must scale accordingly.

  // 1. Single image baseline
  const single1k = resolveImagePricing(undefined, '1k', 1)
  assert.equal(single1k.totalPrice, 0.03)
  assert.equal(single1k.ratePerImage, 0.03)
  assert.equal(single1k.ratesByResolution['1k'], '$0.03/img')
  assert.equal(single1k.ratesByResolution['2k'], '$0.06/img')
  assert.equal(single1k.ratesByResolution['4k'], '$0.12/img')
  assert.ok(single1k.formattedSummary.includes('$0.03 Total'))

  // 2. 5 Images scaling dynamically across all resolutions
  const fiveImages1k = resolveImagePricing(undefined, '1k', 5)
  assert.equal(fiveImages1k.totalPrice, 0.15)
  assert.equal(fiveImages1k.ratesByResolution['1k'], '$0.15 ($0.03/ea)')
  assert.equal(fiveImages1k.ratesByResolution['2k'], '$0.30 ($0.06/ea)')
  assert.equal(fiveImages1k.ratesByResolution['4k'], '$0.60 ($0.12/ea)')
  assert.equal(fiveImages1k.totalsByResolution['1k'], 0.15)
  assert.equal(fiveImages1k.totalsByResolution['2k'], 0.30)
  assert.equal(fiveImages1k.totalsByResolution['4k'], 0.60)
  assert.ok(fiveImages1k.formattedSummary.includes('$0.15 Total ($0.03/image × 5 images)'))

  // 3. 10 Images 2K resolution scaling
  const tenImages2k = resolveImagePricing(undefined, '2k', 10)
  assert.equal(tenImages2k.totalPrice, 0.60)
  assert.equal(tenImages2k.ratePerImage, 0.06)
  assert.equal(tenImages2k.ratesByResolution['1k'], '$0.30 ($0.03/ea)')
  assert.equal(tenImages2k.ratesByResolution['2k'], '$0.60 ($0.06/ea)')
  assert.equal(tenImages2k.ratesByResolution['4k'], '$1.20 ($0.12/ea)')
  assert.ok(tenImages2k.formattedSummary.includes('$0.60 Total ($0.06/image × 10 images)'))

  // 4. Custom model with verified billing lines
  const customModel = {
    id: 'flux-pro',
    label: 'Flux Pro',
    ready: true,
    pricing: {
      billing: {
        lines: [
          { conditions: { resolution: '1024x1024' }, price_usd: 0.05 },
          { conditions: { resolution: '2048x2048' }, price_usd: 0.10 },
        ],
      },
    },
  }
  const verifiedPricing = resolveImagePricing(customModel, '2k', 4)
  assert.equal(verifiedPricing.isVerified, true)
  assert.equal(verifiedPricing.ratePerImage, 0.10)
  assert.equal(verifiedPricing.totalPrice, 0.40)
  assert.equal(verifiedPricing.ratesByResolution['1k'], '$0.20 ($0.05/ea)')
  assert.equal(verifiedPricing.ratesByResolution['2k'], '$0.40 ($0.10/ea)')
  assert.ok(verifiedPricing.formattedSummary.includes('Verified catalog'))
})

test('resolveVideoPricing dynamically scales duration and clip count using verified catalog lines without guessing', () => {
  // Invariant: Video pricing must be derived strictly from verified catalog billing lines.
  // When catalog is missing or unverified, prices must be marked unavailable without guessing (.05/.08/.20).
  // Snapshot examples (Standard .40/.40/.60, Lite .05/.08, Fast .10/.12/.30) scale with duration and clip count.

  // 1. Undefined / unverified catalog returns unavailable without guessing
  const unpriced = resolveVideoPricing(undefined, '1080p', 8, 1)
  assert.equal(unpriced.isVerified, false)
  assert.equal(unpriced.totalPrice, undefined)
  assert.equal(unpriced.ratesByResolution['720p'], 'Unavailable')
  assert.equal(unpriced.ratesByResolution['1080p'], 'Unavailable')
  assert.equal(unpriced.ratesByResolution['4k'], 'Unavailable')
  assert.ok(unpriced.formattedSummary.includes('Pricing unavailable'))

  // 2. Veo Standard (.40/.40/.60 per second)
  const standardModel: TaskModalModelOption = {
    id: 'veo-3.1-generate-preview',
    label: 'Veo 3.1 Standard',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.40, conditions: { resolution: '720p' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.40, conditions: { resolution: '1080p' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.60, conditions: { resolution: '4k' } },
        ],
      },
    },
  }
  const std1080 = resolveVideoPricing(standardModel, '1080p', 8, 1)
  assert.equal(std1080.isVerified, true)
  assert.equal(std1080.ratePerSec, 0.40)
  assert.equal(std1080.rateForClip, 3.20)
  assert.equal(std1080.totalPrice, 3.20)
  assert.equal(std1080.ratesByResolution['720p'], '$3.20 ($0.40/s)')
  assert.equal(std1080.ratesByResolution['1080p'], '$3.20 ($0.40/s)')
  assert.equal(std1080.ratesByResolution['4k'], '$4.80 ($0.60/s)')
  assert.ok(std1080.formattedSummary.includes('$3.20 Total ($0.40/sec × 8s clip)'))

  // 3. Clip count scaling (e.g. 2 clips at 720p 8s = $6.40)
  const std2Clips = resolveVideoPricing(standardModel, '720p', 8, 2)
  assert.equal(std2Clips.totalPrice, 6.40)
  assert.ok(std2Clips.formattedSummary.includes('$6.40 Total ($0.40/sec × 8s × 2 clips)'))

  // 4. Veo Lite (.05/.08, no 4k)
  const liteModel: TaskModalModelOption = {
    id: 'veo-3.1-lite-generate-preview',
    label: 'Veo 3.1 Lite',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.08, conditions: { resolution: '1080p' } },
        ],
      },
    },
  }
  const lite1080 = resolveVideoPricing(liteModel, '1080p', 8, 1)
  assert.equal(lite1080.isVerified, true)
  assert.equal(lite1080.rateForClip, 0.64)
  assert.equal(lite1080.ratesByResolution['4k'], 'Unavailable')
  const lite4k = resolveVideoPricing(liteModel, '4k', 8, 1)
  assert.equal(lite4k.isVerified, false)
  assert.equal(lite4k.totalPrice, undefined)
})

test('resolveAudioPricing dynamically scales costs across duration choices', () => {
  // Invariant: Audio generation pricing calculates base rate for short clips and scaled rate for tracks.
  const shortClip = resolveAudioPricing(undefined, 30)
  assert.equal(shortClip.cost, 0.04)
  assert.ok(shortClip.formattedSummary.includes('$0.04 Total (30s audio track)'))

  const longTrack = resolveAudioPricing(undefined, 60)
  assert.equal(longTrack.cost, 0.08)
  assert.ok(longTrack.formattedSummary.includes('$0.08 Total (60s audio track)'))
})

test('OrchestrateView UI renders dynamic cost cues on resolution, variant, and clip count buttons', () => {
  // Invariant: The UI must not display static 1-image estimates across options;
  // resolution buttons must render dynamic totals, variant count buttons must display per-variant costs,
  // and video clip count buttons must display calculated batch costs.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Image Resolution buttons display dynamic totals and unit breakdown
  assert.ok(source.includes('${totalForRes.toFixed(2)}'), 'Image resolution button must display dynamic total for chosen variants')
  assert.ok(source.includes("imageVariants > 1 ? `${imageVariants}x at $${unitRate.toFixed(2)}/ea`"), 'Image resolution button must display variant count and per-unit rate')

  // 2. Image Variant buttons display calculated cost for active resolution
  assert.ok(source.includes('const costForV = (imagePricingInfo.ratePerImage * v).toFixed(2)'), 'Variant buttons must calculate price dynamically for active resolution')
  assert.ok(source.includes('${costForV}'), 'Variant buttons must display dynamic cost')

  // 3. Image Estimated Model Cost Banner shows dynamic total and choices
  assert.ok(source.includes('${imagePricingInfo.totalPrice.toFixed(2)} Total'), 'Banner badge must display dynamic total')

  // 4. Video Resolution buttons display duration-scaled cost
  assert.ok(source.includes('resolveVideoPricing(selectedVideoOption, res, estimateDuration, videoClipCount)'), 'Video resolution button must display duration and rate')

  // 5. Video Clip Count buttons display calculated dynamic cost
  assert.ok(source.includes('const costForC = videoPricingInfo.rateForClip !== undefined'), 'Clip count buttons must calculate cost dynamically')
  assert.ok(source.includes('videoClipCount === c'), 'Clip count button must track active selection')

  // 6. Sound Duration buttons and banner
  assert.ok(source.includes('const dCost = (d >= 60 ? 0.08 : 0.04).toFixed(2)'), 'Sound duration buttons must show cost')
  assert.ok(source.includes('audioPricingInfo.formattedSummary'), 'Must render audio pricing summary')
})

test('OrchestrateView wires typed onGenerate, generationJobs, and settings propagation end-to-end', () => {
  // Invariant: MediaViewerModal and HistoricalMediaLibrary receive typed onGenerate and generationJobs,
  // propagating aspectRatio, resolution, durationSeconds, model, and count to POST /v3/projects/{id}/tasks.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. MediaViewerModal wired with onGenerate and allGenerationJobs
  assert.ok(source.includes('onGenerate={handleMediaGenerate}'), 'Must pass handleMediaGenerate to MediaViewerModal')
  assert.ok(source.includes('generationJobs={allGenerationJobs}'), 'Must pass allGenerationJobs to MediaViewerModal')
  assert.ok(source.includes('initialQuickRouteMode={mediaViewerInitialMode}'), 'Must pass mediaViewerInitialMode')

  // 2. HistoricalMediaLibrary wired with onGenerate and allGenerationJobs
  assert.ok(source.includes('<HistoricalMediaLibrary'), 'Must render HistoricalMediaLibrary')

  // 3. handleMediaGenerate and handleQuickRouteMedia propagate actual settings
  assert.ok(source.includes('handleMediaGenerate = useCallback'), 'Must define handleMediaGenerate')
  assert.ok(source.includes('aspect_ratio: settings ? settings.aspectRatio'), 'Must propagate settings.aspectRatio to task API')
  assert.ok(source.includes('resolution: settings?.resolution'), 'Must propagate settings.resolution to task API')
  assert.ok(source.includes('duration_seconds: settings?.durationSeconds'), 'Must propagate settings.durationSeconds to task API')

  // 4. Source generation metadata preselection mapping
  assert.ok(source.includes('const aspectRatio = prov?.aspect_ratio || (d as any).aspectRatio || (d as any).aspect_ratio || d.videoAspect'), 'deliverableToMediaItem must map deliverable aspect ratio')
  assert.ok(source.includes('const resolution = prov?.resolution || (d as any).resolution || undefined'), 'deliverableToMediaItem must map deliverable resolution')
  assert.ok(source.includes('durationSeconds'), 'deliverableToMediaItem must map durationSeconds')
  assert.ok(source.includes('aspectRatio: (m as any).aspectRatio'), 'uploadedToMediaItem must map aspectRatio')
  assert.ok(source.includes('resolution: (m as any).resolution'), 'uploadedToMediaItem must map resolution')
  assert.ok(source.includes('durationSeconds: (m as any).durationSeconds'), 'uploadedToMediaItem must map durationSeconds')
})

test('Legacy quick action buttons open configured composer modal rather than running hardcoded suggestions silently', () => {
  // Invariant: Clicking Edit or Iterate on uploaded shelf or deliverable cards opens the MediaViewerModal
  // with preselected mode rather than silently firing arbitrary background auto-deployments.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // Uploaded media shelf
  assert.ok(source.includes("handleOpenUploadedInMediaCenter(m, 'fine_tune')"), 'Shelf Edit button must open configured composer in fine_tune mode')
  assert.ok(source.includes("handleOpenUploadedInMediaCenter(m, 'iterate')"), 'Shelf Iterate button must open configured composer in iterate mode')

  // Deliverable cards
  assert.ok(source.includes("handleOpenDeliverableInMediaCenter(d, tasks.find((t) => t.deliverables?.some((entry) => entry.id === d.id)), 'fine_tune')"), 'Card Edit button must open configured composer in fine_tune mode')
  assert.ok(source.includes("handleOpenDeliverableInMediaCenter(d, tasks.find((t) => t.deliverables?.some((entry) => entry.id === d.id)), 'iterate')"), 'Card Iterate button must open configured composer in iterate mode')
})

// Requirement: media uses the canonical event-driven project cache without
// restoring task polling. Authority: useDesktopProject/DesktopProjectsRuntime.
// Source wiring only; runtime coalescing is tested in desktop-projects.spec.ts.
test('OrchestrateView reconciles media through event-driven project state', () => {
  const source = fs.readFileSync(path.join(__dirname, 'OrchestrateView.tsx'), 'utf8')
  assert.ok(source.includes('useDesktopProject(selectedProjectId)'))
  assert.ok(source.includes('desktopProjects.invalidate(selectedProject.id)'))
  assert.ok(source.includes('job.taskId === t.id && job.sourceId === am.id'))
  assert.ok(!source.includes('const hasActiveTasks ='), 'Do not restore a task polling trigger')
  assert.ok(!source.includes('void poll()'), 'Do not restore task polling')
})
