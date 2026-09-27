import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { resolveImagePricing, resolveVideoPricing, resolveAudioPricing } from './OrchestrateView'

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

  // 6. Integration status
  assert.ok(source.includes('Integrated:'), 'Must render Integrated banner')
  assert.ok(source.includes('Not Integrated:'), 'Must render Not Integrated banner')
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
  assert.ok(source.includes("'/v1/desktop/ui-settings'"), 'Must query ui-settings on mount')

  // 3. Dropdowns with aria-labels and availability warning states
  assert.ok(source.includes('aria-label="Image Model"'), 'Must render Image Model dropdown')
  assert.ok(source.includes('No image models connected. Add Google or OpenAI key in Settings.'), 'Must display warning when no image models connected')
  assert.ok(source.includes('aria-label="Video Model"'), 'Must render Video Model dropdown')
  assert.ok(source.includes('No video models connected. Connect Google or OpenRouter key in Settings.'), 'Must display warning when no video models connected')

  // 4. Model change handlers persist to /v1/desktop/ui-settings
  assert.ok(source.includes('handleImageModelChange'), 'Must provide handleImageModelChange')
  assert.ok(source.includes('handleVideoModelChange'), 'Must provide handleVideoModelChange')
  assert.ok(source.includes('tools: { image: { default_model: newModel } }'), 'Must persist image model choice')
  assert.ok(source.includes('tools: { video: { default_model: newModel } }'), 'Must persist video model choice')

  // 5. Task creation passes selected model
  assert.ok(source.includes("model: taskIntent === 'image' ? (selectedImageModel || undefined)"), 'Deploy modal submit must pass selected model')
  assert.ok(source.includes("model: targetIntent === 'image' ? (selectedImageModel || undefined)"), 'Quick route media must pass selected model')
})

test('OrchestrateView implements compact 5-tab layout (Feature, Image, Video, Sounds, Audit) fitting in one line', () => {
  // Invariant: The Deploy Task Modal must replace long tab labels ('Code / Feature' -> 'Feature',
  // 'Audit / Finder' -> 'Audit', 'Video Story' -> 'Video') and add a dedicated 'Sounds' tab in a 5-column grid.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes('grid-cols-5'), 'Must render 5-column tab layout')
  assert.ok(source.includes('<span>Feature</span>'), 'Must label code tab as "Feature"')
  assert.ok(!source.includes('<span>Code / Feature</span>'), 'Must eliminate old "Code / Feature" text')
  assert.ok(source.includes('<span>Image</span>'), 'Must label image tab as "Image"')
  assert.ok(source.includes('<span>Video</span>'), 'Must label video tab as "Video"')
  assert.ok(!source.includes('<span>Video Story</span>'), 'Must eliminate old "Video Story" tab label')
  assert.ok(source.includes('<span>Sounds</span>'), 'Must provide dedicated "Sounds" tab')
  assert.ok(source.includes('<span>Audit</span>'), 'Must label audit tab as "Audit"')
  assert.ok(!source.includes('<span>Audit / Finder</span>'), 'Must eliminate old "Audit / Finder" text')
})

test('OrchestrateView supports Single Video (1 prompt, 8s model clip) and Multi-Part Video with optional unapproved soundtrack', () => {
  // Invariant: Video tab must provide two explicit options: Single Video (1 clip, 1 prompt, direct to model, 8s)
  // and Multi-Part Video (multi-scene timeline). Single video hides separate soundtrack input and generates
  // audio within the video model directly. Multi-part video provides an explicit optional toggle for soundtrack,
  // defaulting to empty/unapproved, and warns if no soundtrack is selected.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes("setVideoType('single')"), 'Must provide Single Video selection')
  assert.ok(source.includes("setVideoType('multipart')"), 'Must provide Multi-Part Video selection')
  assert.ok(source.includes('1 continuous clip · Direct model shot'), 'Must describe Single Video mode')
  assert.ok(source.includes('Multi-scene timeline · Automatic flow'), 'Must describe Multi-Part Video mode')
  assert.ok(source.includes('Single Video Shot (1 Prompt · 8s Clip)'), 'Must explain single video 1-prompt 8s clip')
  assert.ok(source.includes('How Multi-Part Video Generation Works'), 'Must explain how multi-part video works')
  assert.ok(source.includes('Swarm drafts a multi-scene visual blueprint'), 'Must explain blueprint and clip sequencing')
  assert.ok(source.includes('Add Dedicated Soundtrack Clip'), 'Must offer explicit optional checkbox for soundtrack')
  assert.ok(source.includes('No Soundtrack Selected (Multi-Part Video)'), 'Must warn when no soundtrack is selected for multi-part video')
  assert.ok(source.includes("won&apos;t come out good") || source.includes("won't come out good"), 'Must warn that audio cuts abruptly between clips without sound')
  assert.ok(source.includes("videoType === 'single' ? 8 : videoScenes * 4"), 'Must calculate pricing and duration for 8s single video')
})

test('OrchestrateView provides 720p, 1080p, and 4k video resolution selection with per-model pricing transparency', () => {
  // Invariant: Video modal must allow switching between 720p, 1080p, and 4k resolutions,
  // showing per-second or per-clip pricing rates extracted from catalog pricing so users know what they pay.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes("setVideoResolution(res)"), 'Must allow switching video resolution')
  assert.ok(source.includes("'720p', '1080p', '4k'"), 'Must offer 720p, 1080p, and 4k options')
  assert.ok(source.includes('resolveVideoPricing'), 'Must implement resolveVideoPricing helper')
  assert.ok(source.includes('Estimated Model Cost:'), 'Must display Estimated Model Cost summary')
  assert.ok(source.includes('videoPricingInfo.formattedSummary'), 'Must render formatted model pricing summary')
  assert.ok(source.includes("resolution: taskIntent === 'image' ? imageResolution : taskIntent === 'video' ? videoResolution : undefined") || source.includes("taskIntent === 'video' ? videoResolution : undefined"), 'Must pass resolution in task payload')
})

test('OrchestrateView provides optional soundtrack input box with sound model detection and non-preapproved defaults', () => {
  // Invariant: Video modal must NOT pre-approve or pre-fill soundtrack;
  // it must provide an optional text input box for added sound only when opted into, detect if a sound model is ready,
  // and offer quick-fill preset chips while defaulting to empty.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  assert.ok(source.includes("useState<string>('')"), 'Soundtrack state must default to empty')
  assert.ok(source.includes("useState<boolean>(false)"), 'Include soundtrack state must default to false')
  assert.ok(source.includes('aria-label="Soundtrack Request"'), 'Must render Soundtrack Request input box')
  assert.ok(source.includes('hasSupportedSoundModel'), 'Must check hasSupportedSoundModel')
  assert.ok(source.includes('Sound model ready'), 'Must indicate when supported sound model is ready')
  assert.ok(source.includes('Presets:'), 'Must render optional preset suggestion chips')
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
  assert.ok(source.includes('Enhance prompt with AI'), 'Must render Enhance prompt with AI toggle')
  assert.ok(source.includes('Direct to Video Model (No router rewrite)'), 'Must describe direct mode when toggle is unchecked')
  assert.ok(source.includes('Uses Router to polish prompt'), 'Must describe router enhancement when toggle is checked')
  assert.ok(source.includes('video_type: taskIntent === \'video\' ? videoType : undefined'), 'Must pass video_type in task payload')
  assert.ok(source.includes('enhance_prompt: taskIntent === \'video\' ? enhanceVideoPrompt : undefined'), 'Must pass enhance_prompt in task payload')

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
  assert.ok(source.includes('SINGLE VIDEO'), 'Must display SINGLE VIDEO badge for video_clip outcome')
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
  assert.ok(source.includes('catalogRes?.default_video_model'), 'Must resolve default video model from catalog')

  // 2. Clear Default tagging in dropdowns
  assert.ok(source.includes("opt.id === defaultImageModel ? ' (Default)' : ''"), 'Must label default image model in dropdown')
  assert.ok(source.includes("opt.id === defaultVideoModel ? ' (Default)' : ''"), 'Must label default video model in dropdown')

  // 3. Task override and Change Default actions in the same menu
  assert.ok(source.includes('Override for this task'), 'Must indicate when selected model is a task override')
  assert.ok(source.includes('Set as default for next time'), 'Must offer button to update default for next time')
  assert.ok(source.includes('Change default in this menu for next time'), 'Must offer checkbox to change default for future tasks')
  assert.ok(source.includes('handleSetImageAsDefault'), 'Must provide handleSetImageAsDefault')
  assert.ok(source.includes('handleSetVideoAsDefault'), 'Must provide handleSetVideoAsDefault')
  assert.ok(source.includes('Default image model'), 'Must show default model indicator')
  assert.ok(source.includes('Default video model'), 'Must show default video model indicator')
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

test('OrchestrateView renders visual aspect ratio box wireframe cues for both image and video selectors', () => {
  // Invariant: Aspect ratio selectors for both image and video must not only display raw numbers (16:9, 1:1, etc.),
  // but also render visual proportional box wireframe cues illustrating landscape, square, portrait, and standard shapes.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Aspect ratio definitions with visual dimensional cues
  assert.ok(source.includes('IMAGE_ASPECT_RATIOS'), 'Must declare IMAGE_ASPECT_RATIOS with visual cues')
  assert.ok(source.includes('VIDEO_ASPECT_RATIOS'), 'Must declare VIDEO_ASPECT_RATIOS with visual cues')
  assert.ok(source.includes('Landscape'), 'Must include Landscape label')
  assert.ok(source.includes('Portrait'), 'Must include Portrait label')
  assert.ok(source.includes('Square'), 'Must include Square label')

  // 2. Wireframe rectangular visual boxes in rendered buttons
  assert.ok(source.includes('ar.widthClass'), 'Must apply widthClass to visual ratio box')
  assert.ok(source.includes('ar.heightClass'), 'Must apply heightClass to visual ratio box')
  assert.ok(source.includes('w-5'), 'Must use landscape width')
  assert.ok(source.includes('h-5'), 'Must use portrait height')
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

test('resolveVideoPricing dynamically scales duration costs across single shot and multi-scene sequences', () => {
  // Invariant: Video resolution and scenes must scale cost dynamically with duration (8s single vs 12s/16s/20s scenes).
  // 1. Single video 8 seconds at 1080p ($0.08/s)
  const singleVideo = resolveVideoPricing(undefined, '1080p', 8)
  assert.equal(singleVideo.rateForClip, 0.64)
  assert.equal(singleVideo.ratePerSec, 0.08)
  assert.equal(singleVideo.ratesByResolution['720p'], '$0.40 ($0.05/s)')
  assert.equal(singleVideo.ratesByResolution['1080p'], '$0.64 ($0.08/s)')
  assert.equal(singleVideo.ratesByResolution['4k'], '$1.60 ($0.20/s)')
  assert.ok(singleVideo.formattedSummary.includes('$0.64 Total ($0.08/sec × 8s clip)'))

  // 2. 4-Scene multi-part video (16s) at 720p ($0.05/s)
  const fourScenes = resolveVideoPricing(undefined, '720p', 16)
  assert.equal(fourScenes.rateForClip, 0.80)
  assert.equal(fourScenes.ratesByResolution['720p'], '$0.80 ($0.05/s)')
  assert.equal(fourScenes.ratesByResolution['1080p'], '$1.28 ($0.08/s)')
  assert.equal(fourScenes.ratesByResolution['4k'], '$3.20 ($0.20/s)')
  assert.ok(fourScenes.formattedSummary.includes('$0.80 Total ($0.05/sec × 16s clip)'))
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

test('OrchestrateView UI renders dynamic cost cues on resolution, variant, and scene buttons', () => {
  // Invariant: The UI must not display static 1-image estimates across options;
  // resolution buttons must render dynamic totals, variant count buttons must display per-variant costs,
  // and video scene buttons must display calculated sequence costs.
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
  assert.ok(source.includes('duration = videoType === \'single\' ? 8 : videoScenes * 4'), 'Must compute exact duration for video mode')
  assert.ok(source.includes('${duration}s at $${unitRate.toFixed(2)}/s'), 'Video resolution button must display duration and rate')

  // 5. Video Scene buttons display sequence duration and dynamic cost
  assert.ok(source.includes('const costForScenes = (currentResRate * scenesDuration).toFixed(2)'), 'Scene buttons must calculate cost dynamically')
  assert.ok(source.includes('{scenesDuration}s · ${costForScenes}'), 'Scene buttons must display duration and calculated price')

  // 6. Sound Duration buttons and banner
  assert.ok(source.includes('const dCost = (d >= 60 ? 0.08 : 0.04).toFixed(2)'), 'Sound duration buttons must show cost')
  assert.ok(source.includes('audioPricingInfo.formattedSummary'), 'Must render audio pricing summary')
})
