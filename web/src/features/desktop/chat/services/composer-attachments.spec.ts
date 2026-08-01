import assert from 'node:assert/strict'
import test from 'node:test'
import {
  desktopComposerStagedMediaInput,
  reconcileDesktopComposerStagedAttachments,
  stageDesktopComposerAttachments,
  type DesktopComposerStagedAttachment,
} from './composer-attachments'
import type { StageDesktopV3MediaInput } from '../../session-v3/media-staging-api'

function file(parts: BlobPart[], name: string, type: string): File {
  return new File(parts, name, { type })
}

function staged(index: number, overrides: Partial<DesktopComposerStagedAttachment> = {}): DesktopComposerStagedAttachment {
  return {
    id: `local-${index}`,
    stagingId: `stg_${String(index).padStart(32, '0')}`,
    idempotencyKey: `desktop-v3-routed:stable:media:${index}`,
    name: `screen-${index}.png`,
    mimeType: 'image/png',
    fileType: 'png',
    modality: 'image',
    size: 3,
    createdAt: 10,
    expiresAt: 20,
    ...overrides,
  }
}

test('stageDesktopComposerAttachments derives stable upload identities from routed identity', async () => {
  const calls: StageDesktopV3MediaInput[] = []
  const stage = async (input: StageDesktopV3MediaInput) => {
    calls.push(input)
    return {
      ok: true as const,
      replayed: calls.length > 2,
      staging: {
        id: `stg_${String(calls.length).padStart(32, '0')}`,
        status: 'staged',
        consumable: true,
        declared_mime_type: input.mimeType,
        detected_mime_type: input.mimeType,
        file_name: input.fileName,
        size: input.body.size,
        created_at: 10,
        expires_at: 20,
      },
    }
  }
  const files = [file(['png'], 'screen.png', 'image/png'), file(['pdf'], 'brief.pdf', 'application/pdf')]
  await stageDesktopComposerAttachments({ files, routedClientRequestId: 'desktop-v3-routed:stable', stage })
  await stageDesktopComposerAttachments({ files, routedClientRequestId: 'desktop-v3-routed:stable', stage })
  assert.deepEqual(calls.map((call) => call.idempotencyKey), [
    'desktop-v3-routed:stable:media:0',
    'desktop-v3-routed:stable:media:1',
    'desktop-v3-routed:stable:media:0',
    'desktop-v3-routed:stable:media:1',
  ])
  assert.deepEqual(calls.map((call) => call.fileName), ['screen.png', 'brief.pdf', 'screen.png', 'brief.pdf'])
})

test('stageDesktopComposerAttachments enforces count and per-file byte bounds', async () => {
  const files = Array.from({ length: 9 }, (_, index) => file(['x'], `${index}.png`, 'image/png'))
  await assert.rejects(stageDesktopComposerAttachments({ files, routedClientRequestId: 'routed', stage: async () => { throw new Error('must not upload') } }), /at most 8/)

  await assert.rejects(stageDesktopComposerAttachments({
    files: [file([new Uint8Array((20 << 20) + 1)], 'large.png', 'image/png')],
    routedClientRequestId: 'routed',
    stage: async () => { throw new Error('must not upload') },
  }), /20 MB/)
})

test('staged refs become routed media and reconcile to canonical first-message refs', () => {
  const attachments = [staged(1), staged(2, { mimeType: 'application/pdf', fileType: 'pdf', modality: 'document', name: 'brief.pdf' })]
  assert.deepEqual(desktopComposerStagedMediaInput(attachments), [
    { staging_id: attachments[0].stagingId, modality: 'image', file_type: 'png' },
    { staging_id: attachments[1].stagingId, modality: 'document', file_type: 'pdf' },
  ])
  const refs = [
    { asset_id: 'asset-image', modality: 'image', mime_type: 'image/png', file_type: 'png', size: 3, digest_sha256: 'digest-1', contract_hash: 'contract' },
    { asset_id: 'asset-pdf', modality: 'document', mime_type: 'application/pdf', file_type: 'pdf', size: 3, digest_sha256: 'digest-2', contract_hash: 'contract' },
  ]
  assert.deepEqual(reconcileDesktopComposerStagedAttachments(attachments, { media: refs }), refs)
  assert.throws(() => reconcileDesktopComposerStagedAttachments(attachments, { media: refs.slice(0, 1) }), /different attachment count/)
  assert.throws(() => reconcileDesktopComposerStagedAttachments(attachments, { media: [{ ...refs[0], size: 4 }, refs[1]] }), /mismatched attachment 1/)
})
