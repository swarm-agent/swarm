import { requestJson } from '../../../../../app/api'

export interface ImageProviderModelOption {
  id: string
  label: string
}

export interface ImageProviderOption {
  id: string
  label: string
  ready: boolean
  reason: string
  models: ImageProviderModelOption[]
}

function selectionID(model: string): string {
  switch (model.trim()) {
    case 'gpt-5.5':
      return 'codex-image-gen'
    case 'gemini-3.1-flash-image-preview':
      return 'gemini-nano-banana-2'
    case 'gemini-3-pro-image-preview':
      return 'gemini-nano-banana-pro'
    case 'gemini-2.5-flash-image':
      return 'gemini-nano-banana'
    default:
      return model.trim()
  }
}

function modelLabel(model: string): string {
  switch (selectionID(model)) {
    case 'codex-image-gen':
      return 'Codex Image Gen'
    case 'gemini-nano-banana-2':
      return 'Nano Banana 2'
    case 'gemini-nano-banana-pro':
      return 'Nano Banana Pro'
    case 'gemini-nano-banana':
      return 'Nano Banana'
    default:
      return model.trim()
  }
}

function parseProvider(value: unknown): ImageProviderOption | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const provider = value as Record<string, unknown>
  const id = typeof provider.id === 'string' ? provider.id.trim() : ''
  const label = typeof provider.label === 'string' ? provider.label.trim() : ''
  const models = Array.isArray(provider.models)
    ? provider.models
        .filter((model): model is string => typeof model === 'string' && model.trim() !== '')
        .map((model) => ({ id: selectionID(model), label: modelLabel(model) }))
    : []
  if (!id || !label || models.length === 0) return null
  return {
    id,
    label,
    ready: provider.ready === true,
    reason: typeof provider.reason === 'string' ? provider.reason.trim() : '',
    models,
  }
}

export async function getImageProviders(signal?: AbortSignal): Promise<ImageProviderOption[]> {
  const response = await requestJson<unknown>('/v1/image/providers', { signal })
  if (!response || typeof response !== 'object' || Array.isArray(response)) {
    throw new Error('Image provider response is malformed')
  }
  const providers = (response as Record<string, unknown>).providers
  if (!Array.isArray(providers)) {
    throw new Error('Image provider response is missing providers')
  }
  return providers.map(parseProvider).filter((provider): provider is ImageProviderOption => provider !== null)
}

export const imageProvidersQueryKey = ['image-generation-providers'] as const

export function imageProvidersQueryOptions() {
  return {
    queryKey: imageProvidersQueryKey,
    queryFn: ({ signal }: { signal?: AbortSignal }) => getImageProviders(signal),
    staleTime: 30_000,
  }
}
