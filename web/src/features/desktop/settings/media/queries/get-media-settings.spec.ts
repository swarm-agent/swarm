import assert from 'node:assert/strict'
import test from 'node:test'

import { getVideoTranscriptionStatus, readVideoTranscript, startVideoTranscription } from './get-media-settings'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('direct video transcription clients send only workspace and opaque authority', async () => {
  const requests: Array<{ path: string; body: unknown }> = []
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const request = new Request(input, init)
    const path = new URL(request.url).pathname
    requests.push({ path, body: JSON.parse(await request.text()) })
    if (path.endsWith('/status')) {
      return new Response(JSON.stringify({ job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: 'ready' } }), { headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/read')) {
      return new Response(JSON.stringify({ transcript: { ref: 'transcript_1', text: 'Timeline', segments: [], metadata: {}, validation: { state: 'validated' } } }), { headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ session_id: 'session_1', job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: 'queued' } }), { headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  await startVideoTranscription('/workspace', 'videosrc_1', 'Watch the cursor')
  await getVideoTranscriptionStatus('/workspace', 'session_1', 'trjob_1')
  await readVideoTranscript('/workspace', 'session_1', 'transcript_1')

  assert.deepEqual(requests.map((request) => request.path), ['/v1/workspace/video/transcribe', '/v1/workspace/video/transcribe/status', '/v1/workspace/video/transcribe/read'])
  assert.deepEqual(requests[0].body, { workspace_path: '/workspace', video_ref: 'videosrc_1', focus_notes: 'Watch the cursor' })
  assert.deepEqual(requests[1].body, { workspace_path: '/workspace', session_id: 'session_1', job_ref: 'trjob_1' })
  assert.deepEqual(requests[2].body, { workspace_path: '/workspace', session_id: 'session_1', transcript_ref: 'transcript_1' })
  assert.equal(JSON.stringify(requests).includes('file_uri'), false)
  assert.equal(JSON.stringify(requests).includes('api_key'), false)
})
