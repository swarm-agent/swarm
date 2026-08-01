import { apiFetch, readErrorMessage } from '../../../app/api'

export const DESKTOP_V3_MEDIA_STAGING_PATH = '/v3/media-staging' as const
export const DESKTOP_V3_MEDIA_STAGING_MAX_BYTES = 20 << 20
export const DESKTOP_V3_MEDIA_STAGING_MAX_COUNT = 8
export const DESKTOP_V3_MEDIA_STAGING_MAX_TTL_SECONDS = 24 * 60 * 60

export interface DesktopV3MediaStagingRecord {
  id: string
  status: string
  consumable: boolean
  declared_mime_type: string
  detected_mime_type: string
  file_name?: string
  size: number
  created_at: number
  expires_at: number
}

export interface DesktopV3MediaStagingResponse {
  ok: true
  replayed: boolean
  staging: DesktopV3MediaStagingRecord
}

export interface StageDesktopV3MediaInput {
  body: Blob
  idempotencyKey: string
  mimeType: string
  fileName?: string
  ttlSeconds?: number
  signal?: AbortSignal
}

function boundedHeader(value: string, name: string, maximum: number, required: boolean): string {
  const normalized = value.trim()
  if (required && !normalized) throw new Error(`${name} is required`)
  if (normalized.length > maximum) throw new Error(`${name} exceeds ${maximum} characters`)
  if (/\r|\n|\0/.test(normalized)) throw new Error(`${name} contains invalid characters`)
  return normalized
}

function normalizedMIMEType(value: string): string {
  const normalized = boundedHeader(value, 'Media staging Content-Type', 256, true).toLowerCase()
  if (!/^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/.test(normalized)) {
    throw new Error('Media staging requires a valid MIME type without parameters')
  }
  return normalized
}

function validatedStagingResponse(value: unknown): DesktopV3MediaStagingResponse {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Media staging returned an invalid response')
  }
  const response = value as Partial<DesktopV3MediaStagingResponse>
  const staging = response.staging
  if (response.ok !== true || typeof response.replayed !== 'boolean' || !staging || typeof staging !== 'object') {
    throw new Error('Media staging returned an invalid response')
  }
  if (!/^stg_[0-9a-f]{32}$/.test(staging.id ?? '')) throw new Error('Media staging returned an invalid staging id')
  if (staging.status !== 'staged' || staging.consumable !== true) {
    throw new Error('Media staging returned a non-consumable record')
  }
  if (!staging.declared_mime_type?.trim() || !staging.detected_mime_type?.trim()) {
    throw new Error('Media staging returned incomplete MIME metadata')
  }
  if (!Number.isSafeInteger(staging.size) || staging.size <= 0 || staging.size > DESKTOP_V3_MEDIA_STAGING_MAX_BYTES) {
    throw new Error('Media staging returned an invalid size')
  }
  if (!Number.isSafeInteger(staging.created_at) || !Number.isSafeInteger(staging.expires_at) || staging.expires_at <= staging.created_at) {
    throw new Error('Media staging returned invalid expiry metadata')
  }
  return response as DesktopV3MediaStagingResponse
}

/** Uploads one account-scoped pre-session asset without creating a session. */
export async function stageDesktopV3Media(input: StageDesktopV3MediaInput): Promise<DesktopV3MediaStagingResponse> {
  const idempotencyKey = boundedHeader(input.idempotencyKey, 'Media staging idempotency key', 256, true)
  const mimeType = normalizedMIMEType(input.mimeType)
  const fileName = boundedHeader(input.fileName ?? '', 'Media staging filename', 512, false)
  if (input.body.size <= 0) throw new Error('Media staging does not accept empty files')
  if (input.body.size > DESKTOP_V3_MEDIA_STAGING_MAX_BYTES) {
    throw new Error('Media staging file exceeds the 20 MB upload limit')
  }
  if (input.ttlSeconds !== undefined && (!Number.isSafeInteger(input.ttlSeconds) || input.ttlSeconds <= 0 || input.ttlSeconds > DESKTOP_V3_MEDIA_STAGING_MAX_TTL_SECONDS)) {
    throw new Error('Media staging TTL must be a positive integer no greater than 86400 seconds')
  }

  const headers = new Headers({
    'Content-Type': mimeType,
    'Idempotency-Key': idempotencyKey,
  })
  if (fileName) headers.set('X-Swarm-Media-Filename', fileName)
  if (input.ttlSeconds !== undefined) headers.set('X-Swarm-Media-TTL-Seconds', String(input.ttlSeconds))

  const response = await apiFetch(DESKTOP_V3_MEDIA_STAGING_PATH, {
    method: 'POST',
    headers,
    body: input.body,
    signal: input.signal,
  })
  if (!response.ok) throw new Error(await readErrorMessage(response))
  return validatedStagingResponse(await response.json())
}
