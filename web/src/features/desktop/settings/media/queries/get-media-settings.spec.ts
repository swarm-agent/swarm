import assert from 'node:assert/strict'
import test from 'node:test'

import {
  cancelVideoTranscription,
  pollVideoTranscriptionJob,
  readVideoTranscript,
  startVideoTranscription,
  truncateVideoFocusNotes,
  videoFocusNotesByteLength,
} from './get-media-settings'
import { formatTimelineRange, transcriptSegmentDetails } from '../components/video-transcript-presentation'

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
    if (path.endsWith('/cancel')) {
      return Response.json({ job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: 'cancelled' } })
    }
    if (path.endsWith('/read')) {
      return Response.json({ transcript: { ref: 'transcript_1', text: 'Timeline', segments: [], metadata: {}, validation: { state: 'validated' } } })
    }
    return Response.json({ session_id: 'session_1', job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: 'queued' } })
  }) as typeof fetch

  await startVideoTranscription('/workspace', ['videosrc_1', 'videosrc_2'], 'Watch the cursor')
  await cancelVideoTranscription('/workspace', 'session_1', 'trjob_1')
  await readVideoTranscript('/workspace', 'session_1', 'transcript_1')

  assert.deepEqual(requests.map((request) => request.path), ['/v1/workspace/video/transcribe', '/v1/workspace/video/transcribe/cancel', '/v1/workspace/video/transcribe/read'])
  assert.deepEqual(requests[0].body, { workspace_path: '/workspace', video_refs: ['videosrc_1', 'videosrc_2'], focus_notes: 'Watch the cursor' })
  assert.deepEqual(requests[1].body, { workspace_path: '/workspace', session_id: 'session_1', job_ref: 'trjob_1' })
  assert.deepEqual(requests[2].body, { workspace_path: '/workspace', session_id: 'session_1', transcript_ref: 'transcript_1' })
  assert.equal(JSON.stringify(requests).includes('file_uri'), false)
  assert.equal(JSON.stringify(requests).includes('api_key'), false)
})

test('focus notes use the backend byte limit without splitting unicode', () => {
  const value = `${'a'.repeat(498)}éextra`
  assert.equal(value.length < 500, false)
  assert.equal(videoFocusNotesByteLength(value), 505)
  const truncated = truncateVideoFocusNotes(value)
  assert.equal(truncated, `${'a'.repeat(498)}é`)
  assert.equal(videoFocusNotesByteLength(truncated), 500)
})

test('bounded polling reports updates and stops at ready', async () => {
  const statuses = ['uploading', 'processing', 'ready'] as const
  const updates: string[] = []
  let requestCount = 0
  globalThis.fetch = (async () => Response.json({
    job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: statuses[requestCount++] },
  })) as typeof fetch

  const result = await pollVideoTranscriptionJob({
    workspacePath: '/workspace',
    sessionID: 'session_1',
    jobRef: 'trjob_1',
    maxAttempts: 3,
    intervalMs: 0,
    wait: async () => {},
    onUpdate: (job) => updates.push(job.status),
  })

  assert.equal(result.status, 'ready')
  assert.deepEqual(updates, ['uploading', 'processing', 'ready'])
  assert.equal(requestCount, 3)
})

test('bounded polling returns failed and cancelled terminal jobs without another request', async () => {
  for (const status of ['failed', 'cancelled'] as const) {
    let requestCount = 0
    globalThis.fetch = (async () => {
      requestCount += 1
      return Response.json({ job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status } })
    }) as typeof fetch
    const result = await pollVideoTranscriptionJob({ workspacePath: '/workspace', sessionID: 'session_1', jobRef: 'trjob_1', wait: async () => {} })
    assert.equal(result.status, status)
    assert.equal(requestCount, 1)
  }
})

test('visual-only segments render useful timeline details without speech or audio', () => {
  const details = transcriptSegmentDetails({
    start_ms: 2_000,
    end_ms: 7_500,
    visual: 'A cursor opens Media settings.',
    on_screen_text: 'Transcribe video',
    text: '[Visual] A cursor opens Media settings.',
  })

  assert.equal(formatTimelineRange(2_000, 7_500), '00:02–00:07')
  assert.deepEqual(details, [
    { label: 'Visual', value: 'A cursor opens Media settings.' },
    { label: 'On-screen text', value: 'Transcribe video' },
  ])
})

test('polling surfaces bounded-window and API errors', async () => {
  globalThis.fetch = (async () => Response.json({ job: { ref: 'trjob_1', transcript_ref: 'transcript_1', status: 'processing' } })) as typeof fetch
  await assert.rejects(
    pollVideoTranscriptionJob({ workspacePath: '/workspace', sessionID: 'session_1', jobRef: 'trjob_1', maxAttempts: 1, wait: async () => {} }),
    /bounded polling window/,
  )

  globalThis.fetch = (async () => new Response(JSON.stringify({ error: 'job is outside workspace scope' }), { status: 400, headers: { 'Content-Type': 'application/json' } })) as typeof fetch
  await assert.rejects(
    pollVideoTranscriptionJob({ workspacePath: '/workspace', sessionID: 'session_1', jobRef: 'trjob_other', maxAttempts: 1, wait: async () => {} }),
  )
})
