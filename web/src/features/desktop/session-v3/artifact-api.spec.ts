import test from 'node:test'
import assert from 'node:assert/strict'

import {
  desktopV3ArtifactSelection,
  normalizeDesktopV3ArtifactCatalogEntry,
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

test('artifact selection helper emits only opaque authority fields', () => {
  const entry = normalizeDesktopV3ArtifactCatalogEntry(managedCatalogWire)
  assert.ok(entry)
  assert.deepEqual(desktopV3ArtifactSelection(entry), {
    session_id: 'session-1', collection_id: 'collection-1', variant_id: 'variant-1', event_seq: 42,
  })
})

test('select and use helpers send the fixed selection request payload', async () => {
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

  assert.deepEqual(requests, [
    { url: '/v3/sessions/session-1/artifacts/select', method: 'POST', body: selection },
    { url: '/v3/sessions/session-1/artifacts/use', method: 'POST', body: selection },
  ])
})
