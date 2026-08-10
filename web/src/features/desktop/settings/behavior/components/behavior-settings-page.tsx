import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { SlidersHorizontal } from 'lucide-react'
import { Card } from '../../../../../components/ui/card'
import { PlanContextGuardSettingsSection } from './plan-context-guard-settings'
import { SwarmModeSettingsSection } from './swarm-mode-settings'
import { saveMaxSwarmAgents } from '../../swarm/mutations/save-max-swarm-agents'
import { savePlanContextGuardSettings } from '../../swarm/mutations/save-plan-context-guard-settings'
import { getUISettings } from '../../swarm/queries/get-ui-settings'
import { normalizePlanContextGuardSettings, normalizeSwarmModeSettings, type UISettingsWire } from '../../swarm/types/swarm-settings'

const uiSettingsQueryKey = ['ui-settings'] as const

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() ? error.message : fallback
}

export function BehaviorSettingsPage() {
  const queryClient = useQueryClient()
  const settingsQuery = useQuery({ queryKey: uiSettingsQueryKey, queryFn: getUISettings, staleTime: 30_000 })
  const guardMutation = useMutation({
    mutationFn: (value: Parameters<typeof savePlanContextGuardSettings>[1]) => savePlanContextGuardSettings(settingsQuery.data ?? {}, value),
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const swarmModeMutation = useMutation({
    mutationFn: saveMaxSwarmAgents,
    onSuccess: (settings) => queryClient.setQueryData<UISettingsWire>(uiSettingsQueryKey, settings),
  })
  const loadError = settingsQuery.error ? errorMessage(settingsQuery.error, 'Behavior settings are unavailable.') : null
  const guardError = guardMutation.error ? errorMessage(guardMutation.error, 'The Plan context guard request failed.') : null
  const swarmModeError = swarmModeMutation.error ? errorMessage(swarmModeMutation.error, 'The Swarm mode request failed.') : null

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

      {settingsQuery.data ? (
        <>
          <Card className="p-5">
            <SwarmModeSettingsSection
              value={normalizeSwarmModeSettings(settingsQuery.data)}
              saving={swarmModeMutation.isPending}
              error={swarmModeError}
              onSave={(maxAgents) => swarmModeMutation.mutate(maxAgents)}
            />
          </Card>
          <Card className="p-5">
            <PlanContextGuardSettingsSection
              value={normalizePlanContextGuardSettings(settingsQuery.data)}
              saving={guardMutation.isPending}
              error={guardError}
              onSave={(value) => guardMutation.mutate(value)}
            />
          </Card>
        </>
      ) : loadError ? (
        <Card className="p-5"><div role="alert" className="text-sm text-[var(--app-danger)]">{loadError}</div></Card>
      ) : (
        <Card className="p-5"><p className="text-sm text-[var(--app-text-muted)]">Loading behavior settings…</p></Card>
      )}
    </div>
  )
}
