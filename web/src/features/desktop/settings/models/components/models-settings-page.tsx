import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Boxes } from 'lucide-react'
import { Card } from '../../../../../components/ui/card'
import { modelOptionsQueryOptions, modelProfilesQueryOptions } from '../../../../queries/query-options'
import {
  createModelProfile,
  deleteModelProfile,
  invalidateModelProfiles,
  reorderModelProfiles,
  setDefaultModelProfile,
  updateModelProfile,
} from '../../../chat/queries/model-profile-queries'
import type { ModelOptionRecord, ModelProfileInput, ModelProfileRecord } from '../../../chat/types/chat'
import {
  ModelFavoritesSettings,
  type FlatModelFavorite,
  type FlatModelFavoriteInput,
  type FlatModelOption,
} from '../../model-favorites/components/model-favorites-settings'
import { saveSwarmAgentModelSettings } from '../../swarm/mutations/save-agent-model-settings'
import { agentModelSettingsQueryOptions, agentModelSettingsQueryKey } from '../../swarm/queries/get-agent-model-settings'
import type { AgentModelSettings } from '../../swarm/types/agent-model-settings'
import {
  SwarmModelAssignmentSettings,
  type SwarmModelAssignmentSaveInput,
} from './swarm-model-assignment-settings'

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}

export function toFlatModelFavorite(profile: ModelProfileRecord): FlatModelFavorite {
  return {
    id: profile.profileId,
    name: profile.name,
    provider: profile.provider,
    model: profile.model,
    thinking: profile.thinking,
    serviceTier: profile.serviceTier,
    contextMode: profile.contextMode,
    isDefault: profile.isDefault,
  }
}

export function toModelProfileInput(favorite: FlatModelFavoriteInput): ModelProfileInput {
  return {
    name: favorite.name,
    provider: favorite.provider,
    model: favorite.model,
    thinking: favorite.thinking,
    serviceTier: favorite.serviceTier,
    contextMode: favorite.contextMode,
  }
}

function unique(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean)))
}

export function toFlatModelOptions(options: ModelOptionRecord[]): FlatModelOption[] {
  const byModel = new Map<string, FlatModelOption>()
  for (const option of options) {
    const key = `${option.provider.trim()}\u0000${option.model.trim()}`
    const current = byModel.get(key)
    const contextModes = unique([
      ...(current?.contextModeOptions ?? []),
      option.contextMode,
      ...option.contextModes.map((mode) => mode.mode),
    ])
    byModel.set(key, {
      provider: option.provider,
      model: option.model,
      label: current?.label || option.label,
      thinkingOptions: unique([...(current?.thinkingOptions ?? []), ...option.thinkingOptions]),
      serviceTierOptions: unique([...(current?.serviceTierOptions ?? []), ...option.serviceTiers]),
      contextModeOptions: contextModes,
    })
  }
  return Array.from(byModel.values())
}

export function ModelsSettingsPage() {
  const queryClient = useQueryClient()
  const optionsQuery = useQuery(modelOptionsQueryOptions())
  const profilesQuery = useQuery(modelProfilesQueryOptions())
  const settingsQuery = useQuery(agentModelSettingsQueryOptions())

  const favoritesMutation = useMutation({
    mutationFn: async (operation: () => Promise<unknown>) => {
      await operation()
      await invalidateModelProfiles(queryClient)
    },
  })
  const settingsMutation = useMutation({
    mutationFn: saveSwarmAgentModelSettings,
    onSuccess: (settings) => queryClient.setQueryData<AgentModelSettings>(agentModelSettingsQueryKey, settings),
  })

  const profiles = profilesQuery.data?.profiles ?? []
  const favorites = profiles.map(toFlatModelFavorite)
  const modelOptions = toFlatModelOptions(optionsQuery.data ?? [])
  const loadErrors = [
    optionsQuery.error ? `Model choices are unavailable: ${errorMessage(optionsQuery.error, 'request failed')}` : '',
    profilesQuery.error ? `Model favorites are unavailable: ${errorMessage(profilesQuery.error, 'request failed')}` : '',
    settingsQuery.error ? `Swarm models are unavailable: ${errorMessage(settingsQuery.error, 'request failed')}` : '',
  ].filter(Boolean)
  const favoriteError = favoritesMutation.error
    ? errorMessage(favoritesMutation.error, 'The model favorite request failed.')
    : loadErrors.filter((message) => !message.startsWith('Swarm models')).join(' ')
  const assignmentError = settingsMutation.error
    ? errorMessage(settingsMutation.error, 'The Swarm model request failed.')
    : loadErrors.filter((message) => message.startsWith('Swarm models')).join(' ')
  const settings = settingsQuery.data
  const busy = profilesQuery.isPending || optionsQuery.isPending || favoritesMutation.isPending

  const runFavoriteOperation = (operation: () => Promise<unknown>) => favoritesMutation.mutateAsync(operation).then(() => undefined)
  const saveAssignments = (input: SwarmModelAssignmentSaveInput) => {
    settingsMutation.mutate(input)
  }

  return (
    <div className="grid gap-8">
      <header className="flex items-center gap-3">
        <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
          <Boxes size={18} />
        </div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Models</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Manage reusable favorites and configure Swarm’s Action and Plan models directly.</p>
        </div>
      </header>

      <Card className="p-5">
        <ModelFavoritesSettings
          favorites={favorites}
          modelOptions={modelOptions}
          busy={busy}
          error={favoriteError || null}
          onCreate={(favorite) => runFavoriteOperation(() => createModelProfile(toModelProfileInput(favorite)))}
          onUpdate={(id, favorite) => runFavoriteOperation(() => updateModelProfile(id, toModelProfileInput(favorite)))}
          onDelete={(id) => runFavoriteOperation(() => deleteModelProfile(id))}
          onReorder={(ids) => runFavoriteOperation(() => reorderModelProfiles(ids))}
          onSetDefault={(id) => runFavoriteOperation(() => setDefaultModelProfile(id))}
        />
      </Card>

      <Card className="p-5">
        {settings ? (
          <SwarmModelAssignmentSettings
            modelOptions={modelOptions}
            action={settings.swarm.action}
            plan={settings.swarm.plan}
            saving={settingsMutation.isPending}
            error={assignmentError || null}
            onSave={saveAssignments}
          />
        ) : (
          <section aria-labelledby="swarm-model-assignments-title" className="space-y-3">
            <h2 id="swarm-model-assignments-title" className="text-lg font-semibold text-[var(--app-text)]">Swarm models</h2>
            {assignmentError ? <div role="alert" className="text-sm text-[var(--app-danger)]">{assignmentError}</div> : <p className="text-sm text-[var(--app-text-muted)]">Loading Swarm models…</p>}
          </section>
        )}
      </Card>
    </div>
  )
}
