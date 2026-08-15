import assert from 'node:assert/strict'
import test from 'node:test'

import { browseDesktopVideoSource, DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT } from './video-source-attachments'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('browseDesktopVideoSource uses the shared bounded opaque video contract', async () => {
  let request: Request | undefined
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    request = new Request(input, init)
    return new Response(JSON.stringify({
      root_path: '/registered/source',
      relative_path: '.',
      directories: [{ name: 'nested', relative_path: 'nested' }],
      clips: [
        { ref: 'videosrc_valid', name: 'clip.mp4', mime_type: 'video/mp4', size_bytes: 42, source_fingerprint: 'fingerprint' },
        { ref: 'videosrc_audio', name: 'audio.wav', mime_type: 'audio/wav', size_bytes: 42, source_fingerprint: 'fingerprint' },
        { ref: '', name: 'missing-ref.mp4', mime_type: 'video/mp4', size_bytes: 42, source_fingerprint: 'fingerprint' },
      ],
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch

  const result = await browseDesktopVideoSource('/workspace', '/registered/source')

  assert.equal(DESKTOP_VIDEO_ATTACHMENT_MAX_COUNT, 8)
  assert.deepEqual(result.clips, [
    { ref: 'videosrc_valid', name: 'clip.mp4', mime_type: 'video/mp4', size_bytes: 42, source_fingerprint: 'fingerprint' },
  ])
  assert.equal(request?.method, 'POST')
  assert.equal(new URL(request?.url ?? 'http://invalid').pathname, '/v1/workspace/video/scan')
  assert.deepEqual(JSON.parse(await request!.text()), {
    workspace_path: '/workspace',
    root_path: '/registered/source',
    relative_path: '.',
  })
})

