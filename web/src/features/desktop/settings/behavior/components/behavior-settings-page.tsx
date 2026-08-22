import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SlidersHorizontal } from 'lucide-react'
import { Card } from '../../../../../components/ui/card'
import { ArtifactLibrarySettingsSection } from './artifact-library-settings'
import { PlanContextGuardSettingsSection } from './plan-context-guard-settings'
import { TaskContextSettingsSection } from './task-context-settings'
import { saveArtifactLibrarySettings } from '../../swarm/mutations/save-artifact-library-settings'
import { savePlanContextGuardSettings } from '../../swarm/mutations/save-plan-context-guard-settings'
import { saveTaskContextSettings } from '../../swarm/mutations/save-task-context-settings'
import { getUISettings } from '../../swarm/queries/get-ui-settings'
import { normalizeArtifactLibrarySettings, normalizePlanContextGuardSettings, normalizeTaskContextSettings, type UISettingsWire } from '../../swarm/types/swarm-settings'

const uiSettingsQueryKey = ['ui-settings'] as const

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}

export function BehaviorSettingsPage() {
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({ queryKey: uiSettingsQueryKey, queryFn: getUISettings, staleTime: 30_000 })
  const artifactLibraryMutation = useMutation({
    mutationFn: (value: Parameters<typeof saveArtifactLibrarySettings>[1]) => saveArtifactLibrarySettings(settingsQuery.data ?? {}, value),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const guardMutation = useMutation({
    mutationFn: (value: Parameters<typeof savePlanContextGuardSettings>[1]) => savePlanContextGuardSettings(settingsQuery.data ?? {}, value),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const taskContextMutation = useMutation({
    mutationFn: (value: Parameters<typeof saveTaskContextSettings>[1]) => saveTaskContextSettings(settingsQuery.data ?? {}, value),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const error = guardMutation.error
    ? errorMessage(guardMutation.error, 'The Plan context guard request failed.')
    : settingsQuery.error
      ? errorMessage(settingsQuery.error, 'Behavior settings are unavailable.')
      : null

  return (
    <div className="grid gap-6">
      <header className="flex items-center gap-3">
        <div className="grid h-10 w-10 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)]">
          <SlidersHorizontal size={18} />
        </div>
        <div>
          <h2 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">Behavior</h2>
          <p className="text-sm text-[var(--app-text-muted)]">Control how Swarm behaves while working.</p>
        </div>
      </header>

      <Card className="p-5">
        {settingsQuery.data ? (
          <ArtifactLibrarySettingsSection
            value={normalizeArtifactLibrarySettings(settingsQuery.data)}
            saving={artifactLibraryMutation.isPending}
            error={artifactLibraryMutation.error ? errorMessage(artifactLibraryMutation.error, 'The artifact working-copy request failed.') : null}
            onSave={(value) => artifactLibraryMutation.mutate(value)}
          />
        ) : error ? (
          <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div>
        ) : (
          <p className="text-sm text-[var(--app-text-muted)]">Loading behavior settings…</p>
        )}
      </Card>

      <Card className="p-5">
        {settingsQuery.data ? (
          <TaskContextSettingsSection
            value={normalizeTaskContextSettings(settingsQuery.data)}
            saving={taskContextMutation.isPending}
            error={taskContextMutation.error ? errorMessage(taskContextMutation.error, 'The Task context request failed.') : null}
            onSave={(value) => taskContextMutation.mutate(value)}
          />
        ) : error ? (
          <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div>
        ) : (
          <p className="text-sm text-[var(--app-text-muted)]">Loading behavior settings…</p>
        )}
      </Card>

      <Card className="p-5">
        {settingsQuery.data ? (
          <PlanContextGuardSettingsSection
            value={normalizePlanContextGuardSettings(settingsQuery.data)}
            saving={guardMutation.isPending}
            error={error}
            onSave={(value) => guardMutation.mutate(value)}
          />
        ) : error ? (
          <div role="alert" className="text-sm text-[var(--app-danger)]">{error}</div>
        ) : (
          <p className="text-sm text-[var(--app-text-muted)]">Loading behavior settings…</p>
        )}
      </Card>
    </div>
  )
}
