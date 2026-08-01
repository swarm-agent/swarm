import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { Select } from '../../../../../components/ui/select'

export interface FlatModelFavorite {
  id: string
  name: string
  provider: string
  model: string
  thinking?: string
  serviceTier?: string
  contextMode?: string
  isDefault?: boolean
}

export type SwarmModelAssignmentSaveInput =
  | {
      actionFavoriteId: string
      planEnabled: false
      planFavoriteId?: never
    }
  | {
      actionFavoriteId: string
      planEnabled: true
      planFavoriteId: string
    }

export interface SwarmModelAssignmentSettingsProps {
  favorites: readonly FlatModelFavorite[]
  actionFavoriteId: string
  planEnabled: boolean
  planFavoriteId?: string
  saving: boolean
  error?: string | null
  onSave: (input: SwarmModelAssignmentSaveInput) => void
}

type AssignmentValidationResult =
  | { value: SwarmModelAssignmentSaveInput; error: null }
  | { value: null; error: string }

export function buildSwarmModelAssignmentSaveInput(input: {
  favoriteIds: readonly string[]
  actionFavoriteId: string
  planEnabled: boolean
  planFavoriteId?: string
}): AssignmentValidationResult {
  const favoriteIds = new Set(input.favoriteIds.map((id) => id.trim()).filter(Boolean))
  const actionFavoriteId = input.actionFavoriteId.trim()
  const planFavoriteId = input.planFavoriteId?.trim() ?? ''

  if (!actionFavoriteId || !favoriteIds.has(actionFavoriteId)) {
    return { value: null, error: 'Choose an Action favorite.' }
  }
  if (!input.planEnabled) {
    return { value: { actionFavoriteId, planEnabled: false }, error: null }
  }
  if (!planFavoriteId || !favoriteIds.has(planFavoriteId)) {
    return { value: null, error: 'Choose a Plan favorite.' }
  }
  return { value: { actionFavoriteId, planEnabled: true, planFavoriteId }, error: null }
}

function favoriteLabel(favorite: FlatModelFavorite): string {
  const name = favorite.name.trim() || favorite.model.trim()
  const model = favorite.model.trim()
  const provider = favorite.provider.trim()
  const details = [provider, model].filter(Boolean).join(' · ')
  const defaultLabel = favorite.isDefault ? ' (Default)' : ''
  return `${name}${defaultLabel}${details && details !== name ? ` — ${details}` : ''}`
}

export function SwarmModelAssignmentSettings({
  favorites,
  actionFavoriteId,
  planEnabled,
  planFavoriteId,
  saving,
  error,
  onSave,
}: SwarmModelAssignmentSettingsProps) {
  const normalizedFavorites = useMemo(() => favorites
    .map((favorite) => ({ ...favorite, id: favorite.id.trim() }))
    .filter((favorite) => favorite.id !== ''), [favorites])
  const [draftActionFavoriteId, setDraftActionFavoriteId] = useState(actionFavoriteId.trim())
  const [draftPlanEnabled, setDraftPlanEnabled] = useState(planEnabled)
  const [draftPlanFavoriteId, setDraftPlanFavoriteId] = useState(planFavoriteId?.trim() ?? '')
  const [validationError, setValidationError] = useState<string | null>(null)

  useEffect(() => {
    setDraftActionFavoriteId(actionFavoriteId.trim())
    setDraftPlanEnabled(planEnabled)
    setDraftPlanFavoriteId(planFavoriteId?.trim() ?? '')
    setValidationError(null)
  }, [actionFavoriteId, planEnabled, planFavoriteId])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const result = buildSwarmModelAssignmentSaveInput({
      favoriteIds: normalizedFavorites.map((favorite) => favorite.id),
      actionFavoriteId: draftActionFavoriteId,
      planEnabled: draftPlanEnabled,
      planFavoriteId: draftPlanFavoriteId,
    })
    setValidationError(result.error)
    if (result.value) onSave(result.value)
  }

  const disabled = saving || normalizedFavorites.length === 0
  const visibleError = validationError || error

  return (
    <section aria-labelledby="swarm-model-assignments-title" className="space-y-6">
      <div>
        <h2 id="swarm-model-assignments-title" className="text-lg font-semibold text-[var(--app-text)]">
          Swarm model assignments
        </h2>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Action is required for regular work. Enable Plan only when planning should use a dedicated favorite.
        </p>
      </div>

      {visibleError ? (
        <div role="alert" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
          {visibleError}
        </div>
      ) : null}

      {normalizedFavorites.length === 0 ? (
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
          Create a model favorite before assigning Action or Plan.
        </div>
      ) : null}

      <form className="space-y-5" onSubmit={submit}>
        <div className="space-y-2">
          <label htmlFor="swarm-action-favorite" className="text-sm font-medium text-[var(--app-text)]">
            Action <span className="text-[var(--app-danger)]">Required</span>
          </label>
          <Select
            id="swarm-action-favorite"
            aria-describedby="swarm-action-favorite-help"
            value={draftActionFavoriteId}
            disabled={disabled}
            onChange={(event) => {
              setDraftActionFavoriteId(event.target.value)
              setValidationError(null)
            }}
          >
            <option value="">Choose an Action favorite</option>
            {normalizedFavorites.map((favorite) => (
              <option key={favorite.id} value={favorite.id}>{favoriteLabel(favorite)}</option>
            ))}
          </Select>
          <p id="swarm-action-favorite-help" className="text-xs text-[var(--app-text-muted)]">
            Used for regular Swarm work and as the account default assignment.
          </p>
        </div>

        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <label className="flex cursor-pointer items-start gap-3">
            <input
              type="checkbox"
              className="mt-1 size-4 accent-[var(--app-primary)]"
              checked={draftPlanEnabled}
              disabled={disabled}
              onChange={(event) => {
                setDraftPlanEnabled(event.target.checked)
                setValidationError(null)
              }}
            />
            <span>
              <span className="block text-sm font-medium text-[var(--app-text)]">Enable Plan assignment</span>
              <span className="mt-1 block text-xs text-[var(--app-text-muted)]">
                Use a dedicated favorite for planning. Leave disabled to use Action without a Plan assignment.
              </span>
            </span>
          </label>

          {draftPlanEnabled ? (
            <div className="mt-4 space-y-2 border-t border-[var(--app-border)] pt-4">
              <label htmlFor="swarm-plan-favorite" className="text-sm font-medium text-[var(--app-text)]">
                Plan <span className="text-[var(--app-danger)]">Required when enabled</span>
              </label>
              <Select
                id="swarm-plan-favorite"
                aria-describedby="swarm-plan-favorite-help"
                value={draftPlanFavoriteId}
                disabled={disabled}
                onChange={(event) => {
                  setDraftPlanFavoriteId(event.target.value)
                  setValidationError(null)
                }}
              >
                <option value="">Choose a Plan favorite</option>
                {normalizedFavorites.map((favorite) => (
                  <option key={favorite.id} value={favorite.id}>
                    {favoriteLabel(favorite)}
                  </option>
                ))}
              </Select>
              <p id="swarm-plan-favorite-help" className="text-xs text-[var(--app-text-muted)]">
                Plan may reuse Action or select another flat favorite.
              </p>
            </div>
          ) : null}
        </div>

        <div className="flex justify-end">
          <Button type="submit" variant="primary" disabled={disabled}>
            {saving ? 'Saving…' : 'Save assignments'}
          </Button>
        </div>
      </form>
    </section>
  )
}
