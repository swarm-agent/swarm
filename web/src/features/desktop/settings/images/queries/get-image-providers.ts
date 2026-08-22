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
  return model.trim() === 'gpt-5.5' ? 'codex-image-gen' : model.trim()
}

function modelLabel(model: string): string {
  return selectionID(model) === 'codex-image-gen' ? 'Codex Image Gen' : model.trim()
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
