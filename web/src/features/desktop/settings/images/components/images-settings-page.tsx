import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Image } from 'lucide-react'
import { Card } from '../../../../../components/ui/card'
import { Select } from '../../../../../components/ui/select'
import { imageProvidersQueryOptions } from '../queries/get-image-providers'
import { saveImageDefaultModel } from '../../swarm/mutations/save-image-default-model'
import { getUISettings } from '../../swarm/queries/get-ui-settings'
import { normalizeImageDefaultModel, type UISettingsWire } from '../../swarm/types/swarm-settings'

const uiSettingsQueryKey = ['ui-settings'] as const

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}

export function ImagesSettingsPage() {
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({ queryKey: uiSettingsQueryKey, queryFn: getUISettings, staleTime: 30_000 })
  const providersQuery = useQuery(imageProvidersQueryOptions())
  const saveMutation = useMutation({
    mutationFn: (defaultModel: string) => saveImageDefaultModel({ current: settingsQuery.data ?? {}, defaultModel }),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })

  const providers = providersQuery.data ?? []
  const modelOptions = providers.flatMap((provider) => provider.models.map((model) => ({ ...model, provider })))
  const configuredModel = normalizeImageDefaultModel(settingsQuery.data)
  const firstReadyModel = modelOptions.find((option) => option.provider.ready)?.id || ''
  const selectedModel = configuredModel || firstReadyModel
  const selectedProvider = modelOptions.find((option) => option.id === selectedModel)?.provider
  const loadError = settingsQuery.error || providersQuery.error
  const error = saveMutation.error
    ? errorMessage(saveMutation.error, 'The image model could not be saved.')
    : loadError
      ? errorMessage(loadError, 'Image settings are unavailable.')
      : null

  return (
    <div className="grid gap-6">
      <header className="flex items-center gap-3">
        <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
          <Image size={18} />
        </div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Images</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Choose the account model Swarm uses for AI-generated images.</p>
        </div>
      </header>

      <Card className="p-5">
        <section aria-labelledby="default-image-model-title" className="space-y-4">
          <div>
            <h3 id="default-image-model-title" className="text-lg font-semibold text-[var(--app-text)]">Default image model</h3>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">
              This model is used by managed image generation and image Iteration Swarms.
            </p>
          </div>

          {settingsQuery.isPending || providersQuery.isPending ? (
            <p className="text-sm text-[var(--app-text-muted)]">Loading image models…</p>
          ) : error && modelOptions.length === 0 ? (
            <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div>
          ) : (
            <div className="space-y-3">
              <label className="block space-y-2">
                <span className="text-sm font-medium text-[var(--app-text)]">Image model</span>
                <Select
                  value={selectedModel}
                  disabled={saveMutation.isPending || modelOptions.length === 0}
                  onChange={(event) => saveMutation.mutate(event.target.value)}
                >
                  {providers.map((provider) => (
                    <optgroup key={provider.id} label={provider.label}>
                      {provider.models.map((model) => (
                        <option key={model.id} value={model.id} disabled={!provider.ready}>{model.label}</option>
                      ))}
                    </optgroup>
                  ))}
                </Select>
              </label>
              {selectedProvider && !selectedProvider.ready ? (
                <p className="text-sm text-[var(--app-warning)]">
                  {selectedProvider.reason || `${selectedProvider.label} needs authentication before it can generate images.`}
                </p>
              ) : null}
              {error ? <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div> : null}
              {saveMutation.isSuccess ? <p className="text-sm text-[var(--app-success)]">Image model saved.</p> : null}
              <p className="border-t border-[var(--app-border)] pt-3 text-xs text-[var(--app-text-subtle)]">
                Changes save automatically and apply to future image generations.
              </p>
            </div>
          )}
        </section>
      </Card>
    </div>
  )
}
