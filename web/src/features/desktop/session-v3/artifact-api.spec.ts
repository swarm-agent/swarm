import test from 'node:test'
import assert from 'node:assert/strict'

import {
  appendDesktopV3ArtifactMessageSelection,
  appendDesktopV3ArtifactMessageSelections,
  DESKTOP_V3_ARTIFACT_MESSAGE_SELECTION_MAX_COUNT,
  desktopV3ArtifactMessageSelection,
  desktopV3ArtifactSelection,
  removeDesktopV3ArtifactMessageSelection,
  normalizeDesktopV3ArtifactCatalogEntry,
  desktopV3ArtifactSelectionEndpoint,
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
  progress: { total: 3, staging: 1, ready: 1, failed: 1, unavailable: 0 },
  lineage: {
    parent_session_id: 'session-1',
    source_session_id: 'child-1',
    source_collection_id: 'source-collection',
    source_variant_id: 'source-variant',
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
    progress: { total: 3, staging: 1, ready: 1, failed: 1, unavailable: 0 },
    lineage: {
      parentSessionId: 'session-1', sourceSessionId: 'child-1', sourceCollectionId: 'source-collection',
      sourceVariantId: 'source-variant', taskCallId: 'call-1', programId: 'program-1', programJobId: 'job-1',
      childSessionId: '', iterationId: '', iterationIndex: 2, runId: '', planId: '', checkpointId: '', attemptId: '',
    },
  })
})

test('artifact selection actions target the canonical variant selection route', () => {
  assert.equal(desktopV3ArtifactSelectionEndpoint('session-1', 'variant-1'), '/v3/sessions/session-1/artifacts/variant-1/selection')
})

test('artifact chips dedupe by opaque identity, update intent, and remove without touching bytes', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)
  const select = desktopV3ArtifactMessageSelection(entry, 'select')
  const use = desktopV3ArtifactMessageSelection(entry, 'use')
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
