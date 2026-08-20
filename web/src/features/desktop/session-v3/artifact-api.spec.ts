import test from 'node:test'
import assert from 'node:assert/strict'

import {
  appendDesktopV3ArtifactMessageSelection,
  appendDesktopV3ArtifactMessageSelections,
  DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT,
  desktopV3ArtifactCatalogEntryForKey,
  desktopV3ArtifactCatalogEntryForViewerLocation,
  desktopV3ArtifactCatalogEntryKey,
  desktopV3ArtifactCollectionViewerHref,
  desktopV3ArtifactCollectionViewerSearch,
  desktopV3ArtifactDirectContentURL,
  desktopV3ArtifactDownloadName,
  desktopV3ArtifactRequiresBundle,
  desktopV3ArtifactViewerHref,
  desktopV3ArtifactViewerLocation,
  desktopV3ArtifactViewerSearch,
  desktopV3ArtifactMessageSelection,
  desktopV3ArtifactSelection,
  formatDesktopV3ArtifactAnimationProfile,
  formatDesktopV3ArtifactOutputRequirements,
  normalizeDesktopV3ArtifactAnimationProfile,
  normalizeDesktopV3ArtifactOutputRequirements,
  removeDesktopV3ArtifactMessageSelection,
  normalizeDesktopV3ArtifactCatalogEntry,
  desktopV3ArtifactSelectionEndpoint,
  fetchDesktopV3ArtifactPreviewAccess,
  fetchDesktopV3ArtifactCatalogResult,
  preflightDesktopV3ArtifactDirectContent,
  revealDesktopV3Artifact,
  revealDesktopV3ArtifactCollection,
  selectDesktopV3Artifact,
  useDesktopV3Artifact,
} from './artifact-api'

const managedCatalogWire = {
  artifact_id: 'variant-1',
  collection_id: 'collection-1',
  session_id: 'session-1',
  label: 'Homepage',
  media_type: 'text/html',
  kind: 'html',
  status: 'ready',
  failure_code: '',
  previewable: true,
  selected: true,
  category: 'visual',
  updated_at: 100,
  event_seq: 42,
  animation_profile: {
    profile_id: 'spatial_3d',
    registry_version: '2026-08-16.v1',
    runtime_kind: 'three_webgl',
    runtime_package: 'three',
    runtime_version: '0.185.1',
    heavy: true,
    budgets: {
      max_simultaneous_live_previews: 1,
      max_webgl_contexts: 1,
      max_device_pixel_ratio: 1.5,
      max_canvas_pixels: 2073600,
      max_particles: 2000,
      max_draw_calls_per_frame: 200,
      pause_when_offscreen: true,
      stop_when_document_hidden: true,
      reduced_motion_behavior: 'static_first_frame',
      network_allowed: false,
    },
  },
  output_requirements: {
    preset_id: 'twitter_header',
    width: 1500,
    height: 500,
    aspect_ratio: '3:1',
    orientation: 'landscape',
    resolution_source: 'preset',
    registry_version: '2026-08-01',
  },
  progress: { total: 3, staging: 1, ready: 1, failed: 1, unavailable: 0 },
  lineage: {
    parent_session_id: 'session-1',
    source_session_id: 'child-1',
    source_collection_id: 'source-collection',
    source_variant_id: 'source-variant',
    source_event_seq: 41,
    task_call_id: 'call-1',
    program_id: 'program-1',
    program_job_id: 'job-1',
    iteration_index: 2,
  },
}

test('artifact catalog normalizes managed collection, progress, selection, lineage, status, and event sequence', () => {
  assert.deepEqual(normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire), {
    artifactId: 'variant-1',
    collectionId: 'collection-1',
    sessionId: 'session-1',
    sessionTitle: '',
    workspacePath: '',
    workspaceName: '',
    planId: '',
    planTitle: '',
    checkpointId: '',
    checkpointTitle: '',
    label: 'Homepage',
    description: '',
    collectionName: '',
    collectionDescription: '',
    filename: '',
    mediaType: 'text/html',
    kind: 'html',
    status: 'ready',
    failureCode: '',
    previewable: true,
    selected: true,
    category: 'visual',
    updatedAt: 100,
    eventSeq: 42,
    outputRequirements: {
      presetId: 'twitter_header', width: 1500, height: 500, aspectRatio: '3:1', orientation: 'landscape',
      resolutionSource: 'preset', registryVersion: '2026-08-01',
    },
    animationProfile: {
      profileId: 'spatial_3d', registryVersion: '2026-08-16.v1', runtimeKind: 'three_webgl',
      runtimePackage: 'three', runtimeVersion: '0.185.1', secondaryRuntimePackage: '', secondaryRuntimeVersion: '',
      heavy: true, importedPlaybackOnly: false, editableSourceRequired: false,
      budgets: { maxSimultaneousLivePreviews: 1, maxWebGLContexts: 1, maxDevicePixelRatio: 1.5, maxCanvasPixels: 2073600, maxParticles: 2000, maxDrawCallsPerFrame: 200, pauseWhenOffscreen: true, stopWhenDocumentHidden: true, reducedMotionBehavior: 'static_first_frame', networkAllowed: false },
    },
    progress: { total: 3, staging: 1, ready: 1, failed: 1, unavailable: 0 },
    lineage: {
      parentSessionId: 'session-1', sourceSessionId: 'child-1', sourceCollectionId: 'source-collection',
      sourceVariantId: 'source-variant', sourceEventSeq: 41, taskCallId: 'call-1', programId: 'program-1', programJobId: 'job-1',
      childSessionId: '', iterationGroupId: '', iterationGroup: '', iterationId: '', iterationIndex: 2,
      iterationLabel: '', iterationTheme: '', runId: '', planId: '', checkpointId: '', attemptId: '',
    },
  })
})

test('artifact animation profiles preserve only the exact server-owned runtime contract', () => {
  const profile = normalizeDesktopV3ArtifactAnimationProfile(managedCatalogWire.animation_profile)
  assert.ok(profile)
  assert.equal(profile.profileId, 'spatial_3d')
  assert.equal(profile.runtimePackage, 'three')
  assert.equal(profile.runtimeVersion, '0.185.1')
  assert.equal(profile.budgets.networkAllowed, false)
  assert.equal(formatDesktopV3ArtifactAnimationProfile(profile), 'Three.js 3D')
  assert.equal(normalizeDesktopV3ArtifactAnimationProfile({ ...managedCatalogWire.animation_profile, runtime_version: 'latest' }), null)
  assert.equal(normalizeDesktopV3ArtifactAnimationProfile({ ...managedCatalogWire.animation_profile, budgets: { ...managedCatalogWire.animation_profile.budgets, network_allowed: true } }), null)
  assert.equal(normalizeDesktopV3ArtifactAnimationProfile({ ...managedCatalogWire.animation_profile, budgets: { ...managedCatalogWire.animation_profile.budgets, max_simultaneous_live_previews: 0 } }), null)
  assert.equal(normalizeDesktopV3ArtifactAnimationProfile({ ...managedCatalogWire.animation_profile, budgets: { ...managedCatalogWire.animation_profile.budgets, max_webgl_contexts: -1 } }), null)
})

test('artifact output requirements normalize and format the canonical requested target', () => {
  const requirements = normalizeDesktopV3ArtifactOutputRequirements(managedCatalogWire.output_requirements)
  assert.deepEqual(requirements, {
    presetId: 'twitter_header', width: 1500, height: 500, aspectRatio: '3:1', orientation: 'landscape',
    resolutionSource: 'preset', registryVersion: '2026-08-01',
  })
  assert.equal(formatDesktopV3ArtifactOutputRequirements(requirements), 'X header · 1500 × 500 · 3:1')
  assert.equal(formatDesktopV3ArtifactOutputRequirements({
    ...requirements,
    presetId: 'x_video_landscape',
    width: 1920,
    height: 1080,
    aspectRatio: '16:9',
  }), 'Landscape video · 1920 × 1080 · 16:9')
  assert.equal(formatDesktopV3ArtifactOutputRequirements({
    ...requirements,
    presetId: 'landscape_video',
    width: 1920,
    height: 1080,
    aspectRatio: '16:9',
  }), 'Landscape video · 1920 × 1080 · 16:9')
  assert.equal(formatDesktopV3ArtifactOutputRequirements({
    ...requirements,
    presetId: 'portrait_video',
    width: 1080,
    height: 1920,
    aspectRatio: '9:16',
    orientation: 'portrait',
  }), 'Portrait video · 1080 × 1920 · 9:16')
})

test('historical artifacts omit requirements and malformed nested requirements fail closed', () => {
  const historical = normalizeDesktopV3ArtifactCatalogEntry({ ...managedCatalogWire, output_requirements: undefined })
  assert.ok(historical)
  assert.equal(historical.outputRequirements, undefined)
  assert.equal(formatDesktopV3ArtifactOutputRequirements(historical.outputRequirements), '')

  const explicitDimensions = normalizeDesktopV3ArtifactOutputRequirements({
    ...managedCatalogWire.output_requirements,
    preset_id: '',
    resolution_source: 'dimensions',
  })
  assert.ok(explicitDimensions)
  assert.equal(formatDesktopV3ArtifactOutputRequirements(explicitDimensions), '1500 × 500 · 3:1')

  const malformedCases = [
    { ...managedCatalogWire.output_requirements, width: '1500' },
    { ...managedCatalogWire.output_requirements, width: -1 },
    { ...managedCatalogWire.output_requirements, aspect_ratio: '1500:500' },
    { ...managedCatalogWire.output_requirements, orientation: 'portrait' },
    { ...managedCatalogWire.output_requirements, registry_version: '' },
  ]
  for (const malformed of malformedCases) {
    const entry = normalizeDesktopV3ArtifactCatalogEntry({ ...managedCatalogWire, output_requirements: malformed })
    assert.ok(entry)
    assert.equal(entry.outputRequirements, undefined)
  }
})

test('artifact selection actions target the canonical variant selection route', () => {
  assert.equal(desktopV3ArtifactSelectionEndpoint('session-1', 'variant-1'), '/v3/sessions/session-1/artifacts/variant-1/selection')
})

test('rich artifact previews use direct browser URLs instead of blob hydration', () => {
  assert.equal(desktopV3ArtifactDirectContentURL({ sessionId: 'session one', artifactId: 'video/1', sourceRef: '' }), '/v3/sessions/session%20one/artifacts/video%2F1')
  assert.equal(
    desktopV3ArtifactDirectContentURL({ sessionId: 'session one', artifactId: 'ignored', sourceRef: 'source ref' }),
    '/v3/sessions/session%20one/video/sources/media?source_ref=source+ref',
  )
})

test('protected direct previews preflight with HEAD before browser assignment', async () => {
  const originalFetch = globalThis.fetch
  let method = ''
  globalThis.fetch = (async (_input, init) => {
    method = init?.method ?? ''
    return new Response(null, { status: 200 })
  }) as typeof fetch
  try {
    const url = await preflightDesktopV3ArtifactDirectContent({ sessionId: 'session-1', artifactId: 'variant-1', sourceRef: '' })
    assert.equal(method, 'HEAD')
    assert.equal(url, '/v3/sessions/session-1/artifacts/variant-1')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('artifact catalog carries server-authoritative local reveal availability', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async () => new Response(JSON.stringify({
    ok: true,
    artifacts: [managedCatalogWire],
    local_reveal_available: false,
  }), { headers: { 'Content-Type': 'application/json' } })) as typeof fetch
  try {
    const result = await fetchDesktopV3ArtifactCatalogResult()
    assert.equal(result.localRevealAvailable, false)
    assert.equal(result.artifacts[0]?.localRevealAvailable, false)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('artifact reveal actions use authenticated variant and collection routes', async () => {
  const originalFetch = globalThis.fetch
  const requests: Array<{ url: string; method: string }> = []
  globalThis.fetch = (async (input, init) => {
    requests.push({ url: String(input), method: init?.method ?? '' })
    return new Response(JSON.stringify({ ok: true, method: 'freedesktop-file-manager-show-folders', display_location: '/home/user/.cache/swarm/artifacts/output' }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    const artifactResult = await revealDesktopV3Artifact('session-1', 'variant-1')
    const collectionResult = await revealDesktopV3ArtifactCollection('session-1', 'collection-1')
    assert.equal(artifactResult.method, 'freedesktop-file-manager-show-folders')
    assert.equal(collectionResult.displayLocation, '/home/user/.cache/swarm/artifacts/output')
    assert.deepEqual(requests, [
      { url: '/v3/sessions/session-1/artifacts/variant-1/reveal', method: 'POST' },
      { url: '/v3/sessions/session-1/artifacts/collections/collection-1/reveal', method: 'POST' },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('HTML preview access accepts only the canonical opaque runtime contract', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async () => new Response(JSON.stringify({
    ok: true,
    preview_url: '/v3/sessions/session-1/artifacts/variant-1/content/access/token/__swarm_artifact_entry__.html',
    expires_at: 1_900_000_000,
    media_type: 'text/html; charset=utf-8',
    sandbox: 'allow-scripts',
    opaque_origin: true,
  }), { headers: { 'Content-Type': 'application/json' } })) as typeof fetch
  try {
    const access = await fetchDesktopV3ArtifactPreviewAccess('session-1', 'variant-1')
    assert.equal(access.url, '/v3/sessions/session-1/artifacts/variant-1/content/access/token/__swarm_artifact_entry__.html')
    assert.equal(access.opaqueOrigin, true)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('artifact downloads preserve native image and video files and bundle only packages', () => {
  const image = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    artifact_id: 'image-var-1',
    filename: 'campaign.png',
    media_type: 'image/png',
    kind: 'image',
  })
  const video = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    artifact_id: 'video-var-1',
    filename: 'launch.mp4',
    media_type: 'video/mp4',
    kind: 'video',
  })
  const artifactPackage = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    artifact_id: 'package-var-1',
    filename: 'site.html',
    label: 'Interactive site',
    media_type: 'application/zip',
    kind: 'html',
  })
  assert.ok(image)
  assert.ok(video)
  assert.ok(artifactPackage)
  assert.equal(desktopV3ArtifactRequiresBundle(image), false)
  assert.equal(desktopV3ArtifactRequiresBundle(video), false)
  assert.equal(desktopV3ArtifactRequiresBundle(artifactPackage), true)
  assert.equal(desktopV3ArtifactDownloadName(image), 'campaign.png')
  assert.equal(desktopV3ArtifactDownloadName(video), 'launch.mp4')
  assert.equal(desktopV3ArtifactDownloadName(artifactPackage), 'Interactive site.zip')
  assert.equal(desktopV3ArtifactDownloadName({ ...image, filename: '', label: 'Campaign hero' }), 'Campaign hero.png')
})

test('artifact catalog normalizes video mp4 entry with visual category and video kind', () => {
  const videoWire = {
    ...managedCatalogWire,
    artifact_id: 'video-var-1',
    label: 'Final Video Render',
    filename: 'render.mp4',
    media_type: 'video/mp4',
    kind: 'video',
    output_requirements: {
      preset_id: 'landscape_video',
      width: 1920,
      height: 1080,
      aspect_ratio: '16:9',
      orientation: 'landscape',
      resolution_source: 'preset',
      registry_version: '2026-08-14.v2',
    },
  }
  const entry = normalizeDesktopV3ArtifactCatalogEntry(videoWire)
  assert.ok(entry)
  assert.equal(entry.category, 'visual')
  assert.equal(entry.kind, 'video')
  assert.equal(entry.mediaType, 'video/mp4')
  assert.equal(entry.previewable, true)
  assert.equal(formatDesktopV3ArtifactOutputRequirements(entry.outputRequirements), 'Landscape video · 1920 × 1080 · 16:9')
})

test('artifact chips preserve exact iteration identity and describe the selected variant', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    label: 'Managed iteration group',
    description: 'Iteration Swarm group · 3 iterations',
    collection_name: 'Managed iteration group',
    collection_description: 'Iteration Swarm group · 3 iterations',
    lineage: { ...managedCatalogWire.lineage, iteration_label: 'Motion Study', iteration_theme: 'motion' },
  })
  assert.ok(entry)
  const select = desktopV3ArtifactMessageSelection(entry, 'select')
  const use = desktopV3ArtifactMessageSelection(entry, 'use')
  assert.equal(select.variant_id, 'variant-1')
  assert.equal(select.label, 'Iteration 2: Motion Study')
  assert.equal(select.description, 'Managed iteration group · Iteration 2 of 3')
  assert.deepEqual(appendDesktopV3ArtifactMessageSelection([select], use), [use])
  assert.deepEqual(removeDesktopV3ArtifactMessageSelection([use], use), [])
  assert.equal(JSON.stringify(use).includes('content'), false)
})

test('artifact chips enforce bounded batches and keep use intent singular per collection', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)
  const other = { ...desktopV3ArtifactMessageSelection(entry, 'select'), variant_id: 'variant-2', label: 'Homepage alt' }
  const batched = appendDesktopV3ArtifactMessageSelections([], [desktopV3ArtifactMessageSelection(entry, 'select'), other])
  assert.equal(batched.length, 2)
  const usedFirst = appendDesktopV3ArtifactMessageSelections(batched, [desktopV3ArtifactMessageSelection(entry, 'use')])
  assert.deepEqual(usedFirst, [desktopV3ArtifactMessageSelection(entry, 'use'), other])
  assert.deepEqual(
    appendDesktopV3ArtifactMessageSelections(usedFirst, [{ ...other, action: 'use' }]),
    [desktopV3ArtifactMessageSelection(entry, 'select'), { ...other, action: 'use' }],
  )

  const full = Array.from({ length: DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT }, (_, index) => ({
    ...desktopV3ArtifactMessageSelection(entry, 'select'),
    variant_id: `variant-${index}`,
    label: `Variant ${index}`,
  }))
  assert.throws(
    () => appendDesktopV3ArtifactMessageSelections(full, [{ ...other, variant_id: 'overflow' }]),
    /at most 16 artifact selections/,
  )
  assert.throws(
    () => appendDesktopV3ArtifactMessageSelections([], [{ ...other, label: '' }]),
    /complete opaque selection/,
  )
})

test('artifact viewer keys resolve the exact iteration when variant ids repeat across collections', () => {
  const first = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  const second = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    session_id: 'session-2',
    collection_id: 'collection-2',
    label: 'Homepage iteration 2',
  })
  assert.ok(first)
  assert.ok(second)

  const artifacts = [first, second]
  const firstKey = desktopV3ArtifactCatalogEntryKey(first)
  const secondKey = desktopV3ArtifactCatalogEntryKey(second)

  assert.notEqual(firstKey, secondKey)
  assert.equal(desktopV3ArtifactCatalogEntryForKey(artifacts, firstKey), first)
  assert.equal(desktopV3ArtifactCatalogEntryForKey(artifacts, secondKey), second)
})

test('artifact viewer URLs encode exact session, collection, and variant identity', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)

  const second = normalizeDesktopV3ArtifactCatalogEntry({ ...managedCatalogWire, artifact_id: 'variant-2', lineage: { iteration_group_id: 'group-1', iteration_group: 'navigation remix', iteration_id: 'iteration-2', iteration_index: 2, iteration_label: 'Navigation Remix', iteration_theme: 'navigation' } })
  assert.ok(second)
  assert.notEqual(desktopV3ArtifactViewerHref('my workspace', entry), desktopV3ArtifactViewerHref('my workspace', second))
  assert.equal(second.lineage?.iterationGroupId, 'group-1')
  assert.equal(second.lineage?.iterationGroup, 'navigation remix')
  assert.equal(second.lineage?.iterationIndex, 2)
  assert.equal(second.lineage?.iterationLabel, 'Navigation Remix')

  assert.deepEqual(desktopV3ArtifactViewerSearch(entry), { artifactSession: 'session-1', collection: 'collection-1', artifact: 'variant-1' })
  assert.equal(
    desktopV3ArtifactViewerHref('my workspace', entry),
    '/my%20workspace/session-1?artifactSession=session-1&collection=collection-1&artifact=variant-1',
  )
  const location = desktopV3ArtifactViewerLocation('session-1', { artifactSession: 'session-1', artifact: 'variant-1', collection: 'collection-1' })
  assert.deepEqual(location, { sessionId: 'session-1', collectionId: 'collection-1', artifactId: 'variant-1' })
  assert.equal(location && desktopV3ArtifactCatalogEntryForViewerLocation([entry], location), entry)
})

test('artifact collection URLs preserve collection-level viewer state', () => {
  const first = normalizeDesktopV3ArtifactCatalogEntry({ ...managedCatalogWire, selected: false, status: 'staging' })
  const selected = normalizeDesktopV3ArtifactCatalogEntry({ ...managedCatalogWire, artifact_id: 'variant-2', selected: true })
  assert.ok(first)
  assert.ok(selected)

  const target = { sessionId: 'session-1', collectionId: 'collection-1' }
  assert.deepEqual(desktopV3ArtifactCollectionViewerSearch(target), { artifactSession: 'session-1', collection: 'collection-1' })
  assert.equal(
    desktopV3ArtifactCollectionViewerHref('my workspace', target),
    '/my%20workspace/session-1?artifactSession=session-1&collection=collection-1',
  )
  const location = desktopV3ArtifactViewerLocation('session-1', desktopV3ArtifactCollectionViewerSearch(target))
  assert.deepEqual(location, { sessionId: 'session-1', collectionId: 'collection-1' })
  assert.equal(location && desktopV3ArtifactCatalogEntryForViewerLocation([first, selected], location), undefined)
})

test('artifact viewer resolves delegated entries against parent-session URLs', () => {
  const delegated = normalizeDesktopV3ArtifactCatalogEntry({
    ...managedCatalogWire,
    session_id: 'child-1',
    lineage: { ...managedCatalogWire.lineage, parent_session_id: 'session-1' },
  })
  assert.ok(delegated)
  const location = desktopV3ArtifactViewerLocation('session-1', { artifactSession: 'session-1', artifact: 'variant-1', collection: 'collection-1' })
  assert.ok(location)
  assert.equal(desktopV3ArtifactCatalogEntryForViewerLocation([delegated], location), delegated)
})

test('artifact viewer location rejects incomplete URLs, mismatched collection pairs, and cross-session resolution', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)
  assert.equal(desktopV3ArtifactViewerLocation('session-1', {}), null)
  assert.equal(desktopV3ArtifactViewerLocation('session-2', { artifactSession: 'session-1', artifact: 'variant-1', collection: 'collection-1' }), null)
  const mismatched = desktopV3ArtifactViewerLocation('session-1', { artifactSession: 'session-1', artifact: 'variant-1', collection: 'collection-2' })
  assert.ok(mismatched)
  assert.equal(desktopV3ArtifactCatalogEntryForViewerLocation([entry], mismatched), undefined)
})

test('artifact selection helper emits only opaque authority fields', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)
  assert.deepEqual(desktopV3ArtifactSelection(entry), {
    session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 42,
  })
})

test('select and use helpers send canonical variant selection payloads', async () => {
  const originalFetch = globalThis.fetch
  const requests: Array<{ url: string; method: string; body: unknown }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), method: init?.method ?? '', body: JSON.parse(String(init?.body)) })
    return new Response(JSON.stringify({
      ok: true,
      selection: { session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 42 },
    }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const selection = { session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 42 }
  try {
    await selectDesktopV3Artifact(selection)
    await useDesktopV3Artifact(selection)
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(requests.map((request) => ({ ...request, body: { ...(request.body as Record<string, unknown>), client_request_id: '<operation-id>' } })), [
    { url: '/v3/sessions/session-1/artifacts/variant-1/selection', method: 'POST', body: { client_request_id: '<operation-id>', event_seq: 42, action: 'select' } },
    { url: '/v3/sessions/session-1/artifacts/variant-1/selection', method: 'POST', body: { client_request_id: '<operation-id>', event_seq: 42, action: 'use' } },
  ])
})
