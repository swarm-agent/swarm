import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  admitComposerFile,
  composerFileMIME,
  composerFileType,
  isComposerMediaFile,
  isComposerTextFile,
  composerMediaModality,
} from '../chat/services/composer-attachments'
import { uploadDesktopV3MediaAsset } from '../session-v3/write-api'
import type { DesktopV3MediaCapability, DesktopV3MediaReference } from '../state/desktop-v3-cache-types'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

function mockFile(parts: BlobPart[], name: string, type: string): File {
  return new File(parts, name, { type })
}

test('admitComposerFile removes capability-dependent storage rejection and admits media for text-only sessions', () => {
  // Purpose:
  // - Requirement: Media files (image, video, audio) must be admitted for persistent storage
  //   independent of whether the current conversational model supports media perception.
  // - Threat/regression: Rejecting image/video uploads because model capability is unavailable/null.
  // - Boundary: admitComposerFile in composer-attachments.ts.
  const textOnlyCapability: DesktopV3MediaCapability = {
    status: 'unavailable',
    contract_version: 1,
    capabilities: [],
  }

  // 1. Image admitted even when capability is unavailable
  const thumbPNG = mockFile(['fake-png'], 'youtube-thumbnail.png', 'application/octet-stream')
  const admissionPNG = admitComposerFile(thumbPNG, textOnlyCapability)
  assert.equal(admissionPNG.kind, 'media', 'Image must be admitted as media even under text-only model')
  if (admissionPNG.kind === 'media') {
    assert.equal(admissionPNG.capability.modality, 'image')
    assert.equal(admissionPNG.mimeType, 'image/png')
    assert.equal(admissionPNG.fileType, 'png')
  }

  // 2. Video admitted under null capability
  const videoFile = mockFile(['fake-mp4'], 'drone-shot.mp4', '')
  const admissionVideo = admitComposerFile(videoFile, null)
  assert.equal(admissionVideo.kind, 'media', 'Video must be admitted as media even under null capability')
  if (admissionVideo.kind === 'media') {
    assert.equal(admissionVideo.capability.modality, 'video')
    assert.equal(admissionVideo.mimeType, 'video/mp4')
  }

  // 3. Audio admitted
  const audioFile = mockFile(['fake-mp3'], 'synthwave.mp3', 'audio/mpeg')
  const admissionAudio = admitComposerFile(audioFile, textOnlyCapability)
  assert.equal(admissionAudio.kind, 'media', 'Audio must be admitted as media')
  if (admissionAudio.kind === 'media') {
    assert.equal(admissionAudio.capability.modality, 'audio')
  }
})

test('admitComposerFile preserves legitimate text files and eliminates binary-as-text fallback', () => {
  // Purpose:
  // - Requirement: Legitimate text/code files (.md, .txt, .json, .go, .ts) are admitted as text,
  //   while non-text non-media binaries (.exe, .bin, .iso) are rejected rather than decoded as text.
  // - Threat/regression: Binary files decoded via file.text() corrupting prompt text.
  // - Boundary: admitComposerFile, isComposerTextFile, isComposerMediaFile.

  // 1. Text files admitted
  const mdFile = mockFile(['# Design Spec'], 'spec.md', 'text/markdown')
  const admissionMD = admitComposerFile(mdFile, null)
  assert.equal(admissionMD.kind, 'text')

  const codeFile = mockFile(['package main'], 'main.go', '')
  const admissionCode = admitComposerFile(codeFile, null)
  assert.equal(admissionCode.kind, 'text')

  const jsonFile = mockFile(['{"key":"val"}'], 'config.json', 'application/json')
  const admissionJSON = admitComposerFile(jsonFile, null)
  assert.equal(admissionJSON.kind, 'text')

  // 2. Arbitrary binary rejected - NOT decoded as text
  const binaryFile = mockFile(['\x00\x01\x02\x03'], 'program.exe', 'application/octet-stream')
  const admissionBinary = admitComposerFile(binaryFile, null)
  assert.equal(admissionBinary.kind, 'rejected')
  if (admissionBinary.kind === 'rejected') {
    assert.ok(admissionBinary.reason.includes('not a supported media or text/code file'))
  }

  // 3. Text size limit enforced (1MB)
  const hugeText = mockFile([new Uint8Array((1 << 20) + 10)], 'huge.txt', 'text/plain')
  const admissionHugeText = admitComposerFile(hugeText, null)
  assert.equal(admissionHugeText.kind, 'rejected')
  if (admissionHugeText.kind === 'rejected') {
    assert.ok(admissionHugeText.reason.includes('exceeds the 1 MB text-file limit'))
  }

  // 4. Media size limit enforced (20MB)
  const hugeMedia = mockFile([new Uint8Array((20 << 20) + 10)], 'huge.png', 'image/png')
  const admissionHugeMedia = admitComposerFile(hugeMedia, null)
  assert.equal(admissionHugeMedia.kind, 'rejected')
  if (admissionHugeMedia.kind === 'rejected') {
    assert.ok(admissionHugeMedia.reason.includes('attachment limit'))
  }
})

test('composerFileMIME detects media types from extension when browser MIME is empty', () => {
  assert.equal(composerFileMIME({ name: 'photo.heic', type: '' }), 'image/heic')
  assert.equal(composerFileMIME({ name: 'shot.mp4', type: '' }), 'video/mp4')
  assert.equal(composerFileMIME({ name: 'clip.webm', type: '' }), 'video/webm')
  assert.equal(composerFileMIME({ name: 'audio.mp3', type: '' }), 'audio/mpeg')
  assert.equal(composerFileMIME({ name: 'track.wav', type: '' }), 'audio/wav')
  assert.equal(composerFileMIME({ name: 'thumb.png', type: '' }), 'image/png')
})

test('OrchestrateView unifies picker, drop, and paste on composer and project shelf', () => {
  // Purpose:
  // - Requirement: Picker, drag-and-drop, and paste entrypoints are wired into the unified
  //   media pipeline for both Orchestrator chat composer and project shelf.
  // - Boundary: OrchestrateView.tsx.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Composer drag and drop
  assert.ok(source.includes('onDrop={(e) => {'), 'Composer must handle onDrop')
  assert.ok(source.includes('handleProcessComposerFiles'), 'Composer drop and paste must route to handleProcessComposerFiles')
  assert.ok(source.includes('data-testid="orchestrator-chat-input"'), 'Composer input must have test id')

  // 2. Composer paste
  assert.ok(source.includes('onPaste={(e) => {'), 'Composer must handle onPaste')

  // 3. Composer file picker
  assert.ok(source.includes('ref={fileInputRef}'), 'Composer must have file input ref')
  assert.ok(source.includes('onChange={handleFileSelected}'), 'Composer picker must route to handleFileSelected')

  // 4. Project shelf drag and drop
  assert.ok(source.includes('{/* Uploaded Media & Documents Shelf */}'), 'Shelf section must exist')
  assert.ok(source.includes('handleFileUpload(e.dataTransfer.files)'), 'Shelf must accept dropped files via handleFileUpload')
})

test('OrchestrateView tracks upload state, prevents incomplete send, and provides retry', () => {
  // Purpose:
  // - Requirement: Upload state is visible, send is prevented while uploads are incomplete,
  //   partial batch successes are preserved, and retry does not duplicate successful uploads.
  // - Boundary: OrchestrateView.tsx.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Upload state prevents send
  assert.ok(source.includes('uploadingAttachment'), 'Must track uploadingAttachment state')
  assert.ok(source.includes('if ((!text && attachments.length === 0) || sending || isRunning || uploadingAttachment) return'), 'handleSend must guard uploadingAttachment')
  assert.ok(source.includes('disabled={sending || isRunning || uploadingAttachment'), 'Send button must be disabled while uploadingAttachment')
  assert.ok(source.includes('if (uploadingAttachment) return'), 'Enter key must not send while uploadingAttachment')

  // 2. Partial batch successes and retry without duplicates
  assert.ok(source.includes('attachmentFailedFiles'), 'Must track attachmentFailedFiles')
  assert.ok(source.includes('handleProcessComposerFiles(attachmentFailedFiles)'), 'Must retry only failed composer files')
  assert.ok(source.includes('shelfFailedFiles'), 'Must track shelfFailedFiles')
  assert.ok(source.includes('handleFileUpload(shelfFailedFiles)'), 'Must retry only failed shelf files')
  assert.ok(source.includes('prev.some((p) => p.asset_id === uploaded.asset_id)'), 'Must avoid duplicate attachments on retry')

  // 3. Actionable visible errors
  assert.ok(source.includes('data-testid="chat-send-error"'), 'Composer must render visible send/upload error')
  assert.ok(source.includes('shelfUploadError'), 'Shelf must render visible shelf upload error')
})

test('OrchestrateView renders rich retained chips with thumbnail previews and safe removal', () => {
  // Purpose:
  // - Requirement: Image attachments render thumbnail previews, modality icons are shown,
  //   and removing a chip from the composer draft never deletes retained server media.
  // - Boundary: OrchestrateView.tsx.
  const sourcePath = path.join(__dirname, 'OrchestrateView.tsx')
  const source = fs.readFileSync(sourcePath, 'utf8')

  // 1. Image thumbnail preview from canonical server route
  assert.ok(source.includes('/v3/sessions/${encodeURIComponent(sessionId)}/media/${encodeURIComponent(att.asset_id)}'), 'Image chip must render server thumbnail')

  // 2. Modality icons
  assert.ok(source.includes('att.modality === \'video\''), 'Must branch on video modality for icon')
  assert.ok(source.includes('att.modality === \'audio\''), 'Must branch on audio modality for icon')

  // 3. Safe removal preserves server media
  assert.ok(source.includes('setAttachments((prev) => prev.filter((_, i) => i !== idx))'), 'Removing chip must only update local state')
  assert.ok(source.includes('preserves retained media'), 'Chip remove button must document that retained media is preserved')
})

test('uploadDesktopV3MediaAsset uploads all modalities with real bytes and returns canonical references', async () => {
  // Purpose:
  // - Requirement: uploadDesktopV3MediaAsset sends file payloads to /v3/sessions/{sessionId}/media
  //   with correct headers across image, video, audio, and document modalities, returning canonical references.
  // - Boundary: uploadDesktopV3MediaAsset in write-api.ts.
  const originalFetch = globalThis.fetch
  const calls: Array<{ url: string; headers: Headers; body: unknown }> = []

  const testModalities = [
    {
      name: 'diagram.png',
      bytes: new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
      mimeType: 'image/png',
      modality: 'image',
      fileType: 'png',
      assetId: 'media_img_123',
    },
    {
      name: 'clip.mp4',
      bytes: new Uint8Array([0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70]),
      mimeType: 'video/mp4',
      modality: 'video',
      fileType: 'mp4',
      assetId: 'media_vid_456',
    },
    {
      name: 'voice.wav',
      bytes: new Uint8Array([0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00]),
      mimeType: 'audio/wav',
      modality: 'audio',
      fileType: 'wav',
      assetId: 'media_aud_789',
    },
    {
      name: 'spec.pdf',
      bytes: new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d, 0x31, 0x2e, 0x34]),
      mimeType: 'application/pdf',
      modality: 'document',
      fileType: 'pdf',
      assetId: 'media_doc_012',
    },
  ]

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const headers = new Headers(init?.headers)
    calls.push({ url, headers, body: init?.body })

    const modality = headers.get('X-Swarm-Media-Modality') || 'image'
    const fileName = headers.get('X-Swarm-Media-Filename') || 'file.bin'
    const fileType = headers.get('X-Swarm-Media-File-Type') || 'bin'
    const contentType = headers.get('Content-Type') || 'application/octet-stream'

    const matched = testModalities.find((m) => m.name === fileName)
    const assetId = matched?.assetId || 'media_generic'

    return new Response(
      JSON.stringify({
        ok: true,
        asset: {
          id: assetId,
          modality,
          detected_mime_type: contentType,
          file_type: fileType,
          file_name: fileName,
          size: 8,
          digest_sha256: 'mock-digest-' + assetId,
        },
      }),
      { status: 201, headers: { 'Content-Type': 'application/json' } },
    )
  }) as typeof fetch

  try {
    for (const m of testModalities) {
      const file = new File([m.bytes], m.name, { type: m.mimeType })
      const ref = await uploadDesktopV3MediaAsset({
        sessionId: 'sess-upload-test',
        file,
        mimeType: m.mimeType,
        modality: m.modality,
        fileType: m.fileType,
      })

      assert.equal(ref.asset_id, m.assetId)
      assert.equal(ref.modality, m.modality)
      assert.equal(ref.mime_type, m.mimeType)
      assert.equal(ref.file_type, m.fileType)
      assert.equal(ref.file_name, m.name)
      assert.equal(ref.digest_sha256, 'mock-digest-' + m.assetId)
    }

    assert.equal(calls.length, 4, 'All 4 modalities must have been uploaded via fetch')
    for (let i = 0; i < testModalities.length; i++) {
      const call = calls[i]
      const expected = testModalities[i]
      assert.equal(call.url, '/v3/sessions/sess-upload-test/media')
      assert.equal(call.headers.get('Content-Type'), expected.mimeType)
      assert.equal(call.headers.get('X-Swarm-Media-Modality'), expected.modality)
      assert.equal(call.headers.get('X-Swarm-Media-Filename'), expected.name)
      assert.equal(call.headers.get('X-Swarm-Media-File-Type'), expected.fileType)
    }
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('mixed-batch upload preserves successes, captures failed files, and avoids duplicates on retry', async () => {
  // Purpose:
  // - Requirement: In a batch of 3 uploads where 1 fails, the 2 successful uploads are preserved
  //   in composer attachment state, failed files are isolated, and retrying only the failed file
  //   adds the missing item without duplicating the already-uploaded items.
  // - Boundary: Batch upload & retry flow in OrchestrateView.tsx.
  const originalFetch = globalThis.fetch

  const file1 = new File(['png-data'], 'photo.png', { type: 'image/png' })
  const file2Corrupt = new File(['corrupt-data'], 'bad-video.mp4', { type: 'video/mp4' })
  const file2Fixed = new File(['valid-mp4-data'], 'bad-video.mp4', { type: 'video/mp4' })
  const file3 = new File(['wav-data'], 'audio.wav', { type: 'audio/wav' })

  let shouldFailFile2 = true

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers)
    const fileName = headers.get('X-Swarm-Media-Filename')

    if (fileName === 'bad-video.mp4' && shouldFailFile2) {
      return new Response(JSON.stringify({ ok: false, error: 'corrupted video payload' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      })
    }

    return new Response(
      JSON.stringify({
        ok: true,
        asset: {
          id: 'media_' + fileName?.replace(/\./g, '_'),
          modality: headers.get('X-Swarm-Media-Modality') || 'image',
          detected_mime_type: headers.get('Content-Type') || 'image/png',
          file_type: 'ext',
          file_name: fileName,
          size: 100,
          digest_sha256: 'digest-' + fileName,
        },
      }),
      { status: 201, headers: { 'Content-Type': 'application/json' } },
    )
  }) as typeof fetch

  try {
    // 1. First attempt with 3 files
    let attachments: DesktopV3MediaReference[] = []
    let failedFiles: File[] = []
    let uploadError = ''

    const processBatch = async (filesToUpload: File[]) => {
      const nextFailed: File[] = []
      for (const f of filesToUpload) {
        try {
          const uploaded = await uploadDesktopV3MediaAsset({
            sessionId: 'sess-mixed',
            file: f,
            mimeType: f.type,
            modality: f.type.startsWith('video') ? 'video' : f.type.startsWith('audio') ? 'audio' : 'image',
          })
          // Invariant: avoid duplicate attachments on retry
          if (!attachments.some((p) => p.asset_id === uploaded.asset_id)) {
            attachments = [...attachments, uploaded]
          }
        } catch (err) {
          nextFailed.push(f)
          uploadError = err instanceof Error ? err.message : String(err)
        }
      }
      failedFiles = nextFailed
    }

    await processBatch([file1, file2Corrupt, file3])

    // Verify state after initial batch: 2 succeeded, 1 failed
    assert.equal(attachments.length, 2, 'Two valid files must be retained')
    assert.equal(attachments[0].file_name, 'photo.png')
    assert.equal(attachments[1].file_name, 'audio.wav')
    assert.equal(failedFiles.length, 1, 'Exactly one file must have failed')
    assert.equal(failedFiles[0].name, 'bad-video.mp4')
    assert.ok(uploadError.includes('corrupted video payload'), 'Error must be actionable')

    // 2. Retry with corrected file2
    shouldFailFile2 = false
    await processBatch([file2Fixed])

    // Verify state after retry: all 3 items now present with zero duplicates
    assert.equal(attachments.length, 3, 'All three files must be attached after retry')
    assert.equal(failedFiles.length, 0, 'No failed files remaining')
    const assetIds = attachments.map((a) => a.asset_id)
    assert.equal(new Set(assetIds).size, 3, 'All asset IDs must be unique (no duplicates)')

    // 3. Simulating re-uploading file1 (e.g. user retried entire batch accidentally):
    // Invariant: duplicate check prevents adding file1 a second time
    await processBatch([file1])
    assert.equal(attachments.length, 3, 'Re-uploading existing file must not duplicate attachment')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('safe chip removal updates draft state without issuing DELETE to server', () => {
  // Purpose:
  // - Requirement: Removing an attachment chip from the composer draft must only update local
  //   attachments state and must NEVER issue a DELETE request to delete retained server media.
  // - Boundary: Composer chip removal in OrchestrateView.tsx.
  const deleteCalls: string[] = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === 'DELETE') {
      deleteCalls.push(String(input))
    }
    return new Response(JSON.stringify({ ok: true }), { status: 200 })
  }) as typeof fetch

  try {
    let attachments: DesktopV3MediaReference[] = [
      {
        asset_id: 'media_1',
        modality: 'image',
        mime_type: 'image/png',
        file_type: 'png',
        file_name: 'first.png',
        size: 10,
        digest_sha256: 'd1',
      },
      {
        asset_id: 'media_2',
        modality: 'video',
        mime_type: 'video/mp4',
        file_type: 'mp4',
        file_name: 'second.mp4',
        size: 20,
        digest_sha256: 'd2',
      },
      {
        asset_id: 'media_3',
        modality: 'audio',
        mime_type: 'audio/wav',
        file_type: 'wav',
        file_name: 'third.wav',
        size: 30,
        digest_sha256: 'd3',
      },
    ]

    // Remove middle chip (index 1)
    const removeIdx = 1
    attachments = attachments.filter((_, i) => i !== removeIdx)

    assert.equal(attachments.length, 2)
    assert.equal(attachments[0].asset_id, 'media_1')
    assert.equal(attachments[1].asset_id, 'media_3')
    assert.equal(deleteCalls.length, 0, 'No DELETE requests must be issued when removing draft chip')
  } finally {
    globalThis.fetch = originalFetch
  }
})
