import assert from 'node:assert/strict'
import test from 'node:test'
import {
  DESKTOP_V3_MEDIA_STAGING_MAX_BYTES,
  stageDesktopV3Media,
} from './media-staging-api'

function stagingResponse(overrides: Record<string, unknown> = {}) {
  return {
    ok: true,
    replayed: false,
    staging: {
      id: `stg_${'a'.repeat(32)}`,
      status: 'staged',
      consumable: true,
      declared_mime_type: 'image/png',
      detected_mime_type: 'image/png',
      file_name: 'screen.png',
      size: 3,
      created_at: 10,
      expires_at: 20,
      ...overrides,
    },
  }
}

test('stageDesktopV3Media posts the strict account-scoped pre-session upload shape', async () => {
  const originalFetch = globalThis.fetch
  let captured: { input: RequestInfo | URL; init?: RequestInit } | undefined
  globalThis.fetch = async (input, init) => {
    captured = { input, init }
    return Response.json(stagingResponse(), { status: 201 })
  }
  try {
    const body = new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' })
    const result = await stageDesktopV3Media({
      body,
      idempotencyKey: 'desktop-v3-routed:operation:media:0',
      mimeType: 'image/png',
      fileName: 'screen.png',
      ttlSeconds: 3600,
    })
    assert.equal(captured?.input, '/v3/media-staging')
    assert.equal(captured?.init?.method, 'POST')
    assert.equal(captured?.init?.body, body)
    const headers = new Headers(captured?.init?.headers)
    assert.equal(headers.get('Idempotency-Key'), 'desktop-v3-routed:operation:media:0')
    assert.equal(headers.get('Content-Type'), 'image/png')
    assert.equal(headers.get('X-Swarm-Media-Filename'), 'screen.png')
    assert.equal(headers.get('X-Swarm-Media-TTL-Seconds'), '3600')
    assert.equal(result.staging.id, `stg_${'a'.repeat(32)}`)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('stageDesktopV3Media rejects bounded invalid input before transport', async () => {
  const originalFetch = globalThis.fetch
  let calls = 0
  globalThis.fetch = async () => {
    calls += 1
    return Response.json(stagingResponse())
  }
  try {
    await assert.rejects(stageDesktopV3Media({ body: new Blob(['x']), idempotencyKey: '', mimeType: 'image/png' }), /idempotency key is required/)
    await assert.rejects(stageDesktopV3Media({ body: new Blob(['x']), idempotencyKey: 'key', mimeType: 'bad' }), /valid MIME type/)
    await assert.rejects(stageDesktopV3Media({ body: new Blob([]), idempotencyKey: 'key', mimeType: 'image/png' }), /empty files/)
    await assert.rejects(stageDesktopV3Media({ body: new Blob([new Uint8Array(DESKTOP_V3_MEDIA_STAGING_MAX_BYTES + 1)]), idempotencyKey: 'key', mimeType: 'image/png' }), /20 MB/)
    assert.equal(calls, 0)
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('stageDesktopV3Media surfaces API errors and rejects non-consumable responses', async () => {
  const originalFetch = globalThis.fetch
  try {
    globalThis.fetch = async () => Response.json({ error: 'media staging account quota exceeded' }, { status: 429 })
    await assert.rejects(stageDesktopV3Media({ body: new Blob(['x']), idempotencyKey: 'key', mimeType: 'image/png' }), /quota exceeded/)

    globalThis.fetch = async () => Response.json(stagingResponse({ consumable: false }))
    await assert.rejects(stageDesktopV3Media({ body: new Blob(['x']), idempotencyKey: 'key', mimeType: 'image/png' }), /non-consumable/)
  } finally {
    globalThis.fetch = originalFetch
  }
})
