import { requestJson } from '../../../../app/api'

export type ImageAsset = {
  id: string
  name: string
  path: string
  extension: string
  sizeBytes: number
  modifiedAt: number
  url?: string
}

export type ImageAssetWire = {
  id?: string
  name?: string
  path?: string
  extension?: string
  size_bytes?: number
  sizeBytes?: number
  modified_at?: number
  modifiedAt?: number
  url?: string
}

export type ImageThreadRecord = {
  id: string
  title: string
  workspacePath: string
  workspaceName: string
  imageFolders: string[]
  imageAssets: ImageAsset[]
  imageAssetOrder: string[]
  metadata?: Record<string, unknown>
  createdAt: number
  updatedAt: number
}

export type ImageThreadWire = {
  id?: string
  title?: string
  workspace_path?: string
  workspace_name?: string
  image_folders?: string[]
  image_assets?: ImageAssetWire[]
  image_asset_order?: string[]
  metadata?: Record<string, unknown>
  created_at?: number
  updated_at?: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((entry) => String(entry ?? '').trim()).filter(Boolean)
    : []
}

export function imageAssetURL(threadId: string, assetId: string): string {
  const search = new URLSearchParams({ thread_id: threadId, asset_id: assetId })
  return `/v1/image/assets?${search.toString()}`
}

function mapImageAssets(value: unknown): ImageAsset[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .map((entry): ImageAsset | null => {
      if (!isRecord(entry)) return null
      const id = String(entry.id ?? '').trim()
      const name = String(entry.name ?? '').trim()
      const path = String(entry.path ?? '').trim()
      if (!id || !name || !path) return null
      return {
        id,
        name,
        path,
        extension: String(entry.extension ?? '').trim(),
        sizeBytes: typeof entry.size_bytes === 'number'
          ? entry.size_bytes
          : typeof entry.sizeBytes === 'number'
            ? entry.sizeBytes
            : 0,
        modifiedAt: typeof entry.modified_at === 'number'
          ? entry.modified_at
          : typeof entry.modifiedAt === 'number'
            ? entry.modifiedAt
            : 0,
        url: typeof entry.url === 'string' && entry.url.trim() ? entry.url.trim() : undefined,
      }
    })
    .filter((entry): entry is ImageAsset => Boolean(entry))
}

export function mapImageThread(wire: ImageThreadWire): ImageThreadRecord | null {
  const id = String(wire.id ?? '').trim()
  const workspacePath = String(wire.workspace_path ?? '').trim()
  if (!id || !workspacePath) {
    return null
  }
  return {
    id,
    title: String(wire.title ?? '').trim(),
    workspacePath,
    workspaceName: String(wire.workspace_name ?? '').trim(),
    imageFolders: stringArray(wire.image_folders),
    imageAssets: mapImageAssets(wire.image_assets).map((asset) => ({ ...asset, url: asset.url ?? imageAssetURL(id, asset.id) })),
    imageAssetOrder: stringArray(wire.image_asset_order),
    metadata: isRecord(wire.metadata) ? wire.metadata : undefined,
    createdAt: typeof wire.created_at === 'number' ? wire.created_at : 0,
    updatedAt: typeof wire.updated_at === 'number' ? wire.updated_at : 0,
  }
}

export async function fetchImageThreads(workspacePath: string): Promise<ImageThreadRecord[]> {
  const search = new URLSearchParams({ workspace_path: workspacePath })
  const response = await requestJson<{ threads?: ImageThreadWire[] }>(`/v1/workspace/image/threads?${search.toString()}`)
  return (response.threads ?? [])
    .map(mapImageThread)
    .filter((thread): thread is ImageThreadRecord => Boolean(thread))
}

export async function fetchImageThread(threadId: string): Promise<ImageThreadRecord> {
  const encoded = encodeURIComponent(threadId.trim())
  const response = await requestJson<{ thread?: ImageThreadWire }>(`/v1/workspace/image/threads/${encoded}`)
  const thread = mapImageThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Image thread not found')
  }
  return thread
}

export async function createImageThread(input: {
  title: string
  workspacePath: string
  workspaceName: string
}): Promise<ImageThreadRecord> {
  const response = await requestJson<{ thread?: ImageThreadWire }>('/v1/workspace/image/threads', {
    method: 'POST',
    body: JSON.stringify({
      title: input.title,
      workspace_path: input.workspacePath,
      workspace_name: input.workspaceName,
      image_folders: [],
      image_assets: [],
      image_asset_order: [],
      metadata: {
        tool_kind: 'image',
        session_schema_version: 1,
        storage_area: 'app_managed_workspace_bucket/tools/image/sessions',
      },
    }),
  })
  const thread = mapImageThread(response.thread ?? {})
  if (!thread) {
    throw new Error('Image thread create returned no thread')
  }
  return thread
}
