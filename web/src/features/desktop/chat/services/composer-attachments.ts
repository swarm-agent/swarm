import type { ModelOptionRecord } from '../types/chat'
import type { DesktopV3MediaCapability, DesktopV3MediaCapabilityEntry, DesktopV3MediaReference } from '../../state/desktop-v3-cache-types'
import {
  DESKTOP_V3_MEDIA_STAGING_MAX_BYTES,
  DESKTOP_V3_MEDIA_STAGING_MAX_COUNT,
  stageDesktopV3Media,
  type DesktopV3MediaStagingRecord,
} from '../../session-v3/media-staging-api'

export const DESKTOP_COMPOSER_TEXT_FILE_MAX_BYTES = 1 << 20
export const DESKTOP_COMPOSER_TEXT_TOTAL_MAX_BYTES = 4 << 20
export const DESKTOP_COMPOSER_TEXT_FILE_MAX_COUNT = 8

const TEXT_FILE_TYPES = new Set([
  'bash', 'c', 'cc', 'cfg', 'conf', 'cpp', 'cs', 'css', 'csv', 'cxx', 'diff', 'env.example',
  'go', 'graphql', 'h', 'hpp', 'html', 'ini', 'java', 'js', 'json', 'jsx', 'kt', 'kts', 'log',
  'lua', 'md', 'mdx', 'mjs', 'php', 'properties', 'py', 'rb', 'rs', 'sh', 'sql', 'svelte',
  'swift', 'toml', 'ts', 'tsx', 'txt', 'vue', 'xml', 'yaml', 'yml', 'zsh',
])

const TEXT_MIME_TYPES = new Set([
  'application/graphql', 'application/javascript', 'application/json', 'application/sql',
  'application/toml', 'application/typescript', 'application/x-httpd-php', 'application/xml',
  'application/x-sh', 'application/x-yaml', 'text/markdown',
])

export type DesktopComposerFileAdmission =
  | { kind: 'media'; capability: DesktopV3MediaCapabilityEntry; mimeType: string; fileType?: string }
  | { kind: 'text'; fileType?: string }
  | { kind: 'rejected'; reason: string }

export function composerFileType(file: Pick<File, 'name'>): string | undefined {
  const name = file.name.trim().toLowerCase()
  const index = name.lastIndexOf('.')
  return index >= 0 && index < name.length - 1 ? name.slice(index + 1) : undefined
}

export function composerFileMIME(file: Pick<File, 'name' | 'type'>): string {
  const browserMIME = file.type.trim().toLowerCase()
  if (browserMIME) return browserMIME
  switch (composerFileType(file)) {
    case 'gif': return 'image/gif'
    case 'jpeg':
    case 'jpg': return 'image/jpeg'
    case 'png': return 'image/png'
    case 'webp': return 'image/webp'
    case 'md':
    case 'mdx': return 'text/markdown'
    case 'json': return 'application/json'
    case 'js':
    case 'mjs': return 'application/javascript'
    case 'ts': return 'application/typescript'
    case 'yaml':
    case 'yml': return 'application/x-yaml'
    case 'txt': return 'text/plain'
    default: return ''
  }
}

export function isComposerTextFile(file: Pick<File, 'name' | 'type'>): boolean {
  const mimeType = composerFileMIME(file)
  if (mimeType.startsWith('text/') || TEXT_MIME_TYPES.has(mimeType)) return true
  return TEXT_FILE_TYPES.has(composerFileType(file) ?? '')
}

export function modelMediaCapability(option: ModelOptionRecord | null | undefined): DesktopV3MediaCapability | null {
  const inputs = option?.media?.inputs ?? []
  const capabilities = inputs
    .filter((input) => input.state === 'supported' && (input.modality === 'image' || input.modality === 'file' || input.modality === 'pdf'))
    .map((input) => ({
      modality: input.modality,
      mime_types: input.mimeTypes,
      file_types: input.fileTypes,
      max_bytes: 20 << 20,
      max_count: 8,
      provenance: ['model_catalog'],
    }))
  if (capabilities.length === 0) return null
  return {
    status: 'available',
    contract_version: 1,
    provider: option?.provider,
    model: option?.model,
    provider_surface: option?.media?.providerSurface,
    credential_surface: option?.media?.credentialSurface,
    capabilities,
  }
}

export function admitComposerFile(
  file: Pick<File, 'name' | 'type' | 'size'>,
  capability: DesktopV3MediaCapability | null | undefined,
): DesktopComposerFileAdmission {
  const fileType = composerFileType(file)
  const mimeType = composerFileMIME(file)
  const media = capability?.status === 'available'
    ? capability.capabilities.find((candidate) => {
        const acceptsMIME = Boolean(mimeType && (candidate.mime_types ?? []).some((value) => value.toLowerCase() === mimeType))
        const acceptsFileType = Boolean(fileType && (candidate.file_types ?? []).some((value) => value.replace(/^\./, '').toLowerCase() === fileType))
        return acceptsMIME || acceptsFileType
      })
    : undefined
  if (media) {
    if (media.max_bytes > 0 && file.size > media.max_bytes) {
      return { kind: 'rejected', reason: `${file.name} exceeds the ${Math.ceil(media.max_bytes / (1024 * 1024))} MB attachment limit.` }
    }
    return { kind: 'media', capability: media, mimeType, fileType }
  }
  if (!isComposerTextFile(file)) {
    return { kind: 'rejected', reason: `${file.name} is not a supported image, Markdown, or code/text file.` }
  }
  if (file.size > DESKTOP_COMPOSER_TEXT_FILE_MAX_BYTES) {
    return { kind: 'rejected', reason: `${file.name} exceeds the 1 MB text-file limit.` }
  }
  return { kind: 'text', fileType }
}

export function appendComposerTextFile(draft: string, fileName: string, fileType: string | undefined, content: string): string {
  const safeName = fileName.trim() || 'attachment.txt'
  const language = (fileType ?? '').replace(/[^a-z0-9_+-]/gi, '')
  const prefix = draft.trimEnd()
  const block = `File: ${safeName}\n\n\`\`\`${language}\n${content.replace(/\r\n?/g, '\n')}\n\`\`\``
  return prefix ? `${prefix}\n\n${block}` : block
}

export interface DesktopComposerStagedAttachment {
  id: string
  stagingId: string
  idempotencyKey: string
  name: string
  mimeType: string
  fileType?: string
  modality: string
  size: number
  createdAt: number
  expiresAt: number
}

export interface StageDesktopComposerAttachmentsInput {
  files: readonly File[]
  routedClientRequestId: string
  existing?: readonly DesktopComposerStagedAttachment[]
  signal?: AbortSignal
  stage?: typeof stageDesktopV3Media
}

function preSessionModality(mimeType: string): string {
  const topLevel = mimeType.split('/', 1)[0]
  return topLevel === 'image' || topLevel === 'audio' || topLevel === 'video' ? topLevel : 'document'
}

function stagedAttachmentFromRecord(
  file: File,
  fileType: string | undefined,
  idempotencyKey: string,
  record: DesktopV3MediaStagingRecord,
): DesktopComposerStagedAttachment {
  return {
    id: crypto.randomUUID(),
    stagingId: record.id,
    idempotencyKey,
    name: record.file_name?.trim() || file.name.trim() || 'attachment',
    mimeType: record.detected_mime_type,
    fileType,
    modality: preSessionModality(record.detected_mime_type),
    size: record.size,
    createdAt: record.created_at,
    expiresAt: record.expires_at,
  }
}

/**
 * Stages routed-new-session files without creating hidden session or model state.
 * Keys derive from the routed operation identity and remain stable when this
 * function is retried with the same ordered file list.
 */
export async function stageDesktopComposerAttachments(
  input: StageDesktopComposerAttachmentsInput,
): Promise<DesktopComposerStagedAttachment[]> {
  const routedClientRequestId = input.routedClientRequestId.trim()
  if (!routedClientRequestId) throw new Error('Pre-session attachments require the routed client_request_id')
  const existing = [...(input.existing ?? [])]
  if (existing.length + input.files.length > DESKTOP_V3_MEDIA_STAGING_MAX_COUNT) {
    throw new Error(`A routed message supports at most ${DESKTOP_V3_MEDIA_STAGING_MAX_COUNT} staged attachments.`)
  }
  for (const file of input.files) {
    if (file.size > DESKTOP_V3_MEDIA_STAGING_MAX_BYTES) {
      throw new Error(`${file.name || 'Attachment'} exceeds the 20 MB staging limit.`)
    }
  }

  const stage = input.stage ?? stageDesktopV3Media
  const staged: DesktopComposerStagedAttachment[] = []
  for (const [index, file] of input.files.entries()) {
    if (file.size <= 0) throw new Error(`${file.name || 'Attachment'} is empty.`)
    const mimeType = composerFileMIME(file)
    if (!mimeType) throw new Error(`${file.name || 'Attachment'} has no supported MIME type.`)
    const fileType = composerFileType(file)
    const idempotencyKey = `${routedClientRequestId}:media:${existing.length + index}`
    const response = await stage({
      body: file,
      idempotencyKey,
      mimeType,
      fileName: file.name,
      signal: input.signal,
    })
    staged.push(stagedAttachmentFromRecord(file, fileType, idempotencyKey, response.staging))
  }
  return [...existing, ...staged]
}

export function desktopComposerStagedMediaInput(
  attachments: readonly DesktopComposerStagedAttachment[],
): Array<{ staging_id: string; modality: string; file_type?: string }> {
  if (attachments.length > DESKTOP_V3_MEDIA_STAGING_MAX_COUNT) {
    throw new Error(`A routed message supports at most ${DESKTOP_V3_MEDIA_STAGING_MAX_COUNT} staged attachments.`)
  }
  const seen = new Set<string>()
  return attachments.map((attachment) => {
    const stagingId = attachment.stagingId.trim()
    if (!stagingId || seen.has(stagingId)) throw new Error('Routed message contains an invalid or duplicate staging id')
    seen.add(stagingId)
    return {
      staging_id: stagingId,
      modality: attachment.modality.trim(),
      file_type: attachment.fileType?.trim() || undefined,
    }
  })
}

export function reconcileDesktopComposerStagedAttachments(
  staged: readonly DesktopComposerStagedAttachment[],
  firstMessage: { media?: readonly DesktopV3MediaReference[] | null } | null | undefined,
): DesktopV3MediaReference[] {
  const refs = [...(firstMessage?.media ?? [])]
  if (refs.length !== staged.length) {
    throw new Error('Routed session returned a different attachment count')
  }
  return refs.map((reference, index) => {
    const attachment = staged[index]
    if (!reference.asset_id?.trim() || reference.size !== attachment.size) {
      throw new Error(`Routed session returned mismatched attachment ${index + 1}`)
    }
    if (reference.modality.trim().toLowerCase() !== attachment.modality.trim().toLowerCase()) {
      throw new Error(`Routed session returned mismatched attachment modality ${index + 1}`)
    }
    if (reference.mime_type.trim().toLowerCase() !== attachment.mimeType.trim().toLowerCase()) {
      throw new Error(`Routed session returned mismatched attachment MIME type ${index + 1}`)
    }
    if (attachment.fileType && reference.file_type?.replace(/^\./, '').trim().toLowerCase() !== attachment.fileType.toLowerCase()) {
      throw new Error(`Routed session returned mismatched attachment file type ${index + 1}`)
    }
    return reference
  })
}
