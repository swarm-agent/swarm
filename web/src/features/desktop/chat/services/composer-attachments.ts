import type { ModelOptionRecord } from '../types/chat'
import type { DesktopV3MediaCapability, DesktopV3MediaCapabilityEntry } from '../../state/desktop-v3-cache-types'

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
