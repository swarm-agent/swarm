import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Button } from '../../../../../components/ui/button'
import { Select } from '../../../../../components/ui/select'
import type { SwarmModelSelection, SwarmModelSettingsInput } from '../../swarm/types/model-settings'

export interface SwarmDirectModelOption {
  provider: string
  model: string
  label?: string
  thinkingOptions?: string[]
  serviceTierOptions?: string[]
  contextModeOptions?: string[]
}

export type SwarmModelAssignmentSaveInput = SwarmModelSettingsInput

export interface SwarmModelAssignmentSettingsProps {
  modelOptions: readonly SwarmDirectModelOption[]
  action: SwarmModelSelection
  plan: SwarmModelSelection
  saving: boolean
  error?: string | null
  onSave: (input: SwarmModelAssignmentSaveInput) => void
}

type AssignmentValidationResult =
  | { value: SwarmModelAssignmentSaveInput; error: null }
  | { value: null; error: string }

function normalizeSelection(selection: SwarmModelSelection): SwarmModelSelection {
  return {
    provider: selection.provider.trim(),
    model: selection.model.trim(),
    thinking: selection.thinking.trim(),
    serviceTier: selection.serviceTier.trim(),
    contextMode: selection.contextMode.trim().toLowerCase(),
  }
}

export function buildSwarmModelAssignmentSaveInput(input: SwarmModelSettingsInput): AssignmentValidationResult {
  const action = normalizeSelection(input.action)
  const plan = normalizeSelection(input.plan)
  if (!action.provider || !action.model || !action.thinking) {
    return { value: null, error: 'Choose provider, model, and thinking for Action.' }
  }
  if (!plan.provider || !plan.model || !plan.thinking) {
    return { value: null, error: 'Choose provider, model, and thinking for Plan.' }
  }
  return { value: { action, plan }, error: null }
}

function modelKey(provider: string, model: string): string {
  return `${encodeURIComponent(provider)}:${encodeURIComponent(model)}`
}

function DirectModelEditor({
  label,
  value,
  modelOptions,
  disabled,
  onChange,
}: {
  label: string
  value: SwarmModelSelection
  modelOptions: readonly SwarmDirectModelOption[]
  disabled: boolean
  onChange: (value: SwarmModelSelection) => void
}) {
  const providers = Array.from(new Set(modelOptions.map((option) => option.provider.trim()).filter(Boolean)))
  const choices = modelOptions.filter((option) => option.provider === value.provider)
  const selected = choices.find((option) => option.model === value.model) ?? null
  const thinkingOptions = Array.from(new Set([...(selected?.thinkingOptions ?? []), value.thinking].map((item) => item.trim()).filter(Boolean)))
  const serviceTierOptions = Array.from(new Set(['', ...(selected?.serviceTierOptions ?? []), value.serviceTier].map((item) => item.trim())))
  const contextModeOptions = Array.from(new Set(['', ...(selected?.contextModeOptions ?? []), value.contextMode].map((item) => item.trim().toLowerCase())))
  const fieldClass = 'grid gap-1.5 text-xs font-medium text-[var(--app-text-muted)]'

  return (
    <fieldset className="grid gap-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
      <legend className="px-1 text-sm font-semibold text-[var(--app-text)]">{label}</legend>
      <div className="grid gap-4 md:grid-cols-2">
        <label className={fieldClass}>
          Provider
          <Select value={value.provider} disabled={disabled} onChange={(event) => onChange({ provider: event.target.value, model: '', thinking: '', serviceTier: '', contextMode: '' })}>
            <option value="">Choose a provider</option>
            {providers.map((provider) => <option key={provider} value={provider}>{provider}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Model
          <Select
            value={modelKey(value.provider, value.model)}
            disabled={disabled || !value.provider}
            onChange={(event) => {
              const option = choices.find((candidate) => modelKey(candidate.provider, candidate.model) === event.target.value)
              onChange({
                provider: option?.provider ?? value.provider,
                model: option?.model ?? '',
                thinking: option?.thinkingOptions?.[0] ?? '',
                serviceTier: option?.serviceTierOptions?.[0] ?? '',
                contextMode: option?.contextModeOptions?.[0] ?? '',
              })
            }}
          >
            <option value={modelKey(value.provider, '')}>Choose a model</option>
            {choices.map((option) => <option key={modelKey(option.provider, option.model)} value={modelKey(option.provider, option.model)}>{option.label?.trim() || option.model}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Thinking
          <Select value={value.thinking} disabled={disabled || !selected} onChange={(event) => onChange({ ...value, thinking: event.target.value })}>
            <option value="">Choose thinking</option>
            {thinkingOptions.map((thinking) => <option key={thinking} value={thinking}>{thinking}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Service tier
          <Select value={value.serviceTier} disabled={disabled || !selected} onChange={(event) => onChange({ ...value, serviceTier: event.target.value })}>
            {serviceTierOptions.map((tier) => <option key={tier || 'standard'} value={tier}>{tier || 'Standard'}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Context mode
          <Select value={value.contextMode} disabled={disabled || !selected} onChange={(event) => onChange({ ...value, contextMode: event.target.value })}>
            {contextModeOptions.map((mode) => <option key={mode || 'default'} value={mode}>{mode || 'Default'}</option>)}
          </Select>
        </label>
      </div>
    </fieldset>
  )
}

export function SwarmModelAssignmentSettings({ modelOptions, action, plan, saving, error, onSave }: SwarmModelAssignmentSettingsProps) {
  const [draftAction, setDraftAction] = useState(() => normalizeSelection(action))
  const [draftPlan, setDraftPlan] = useState(() => normalizeSelection(plan))
  const [validationError, setValidationError] = useState<string | null>(null)
  const normalizedOptions = useMemo(() => modelOptions.filter((option) => option.provider.trim() && option.model.trim()), [modelOptions])

  useEffect(() => {
    setDraftAction(normalizeSelection(action))
    setDraftPlan(normalizeSelection(plan))
    setValidationError(null)
  }, [action, plan])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const result = buildSwarmModelAssignmentSaveInput({ action: draftAction, plan: draftPlan })
    setValidationError(result.error)
    if (result.value) onSave(result.value)
  }

  const disabled = saving || normalizedOptions.length === 0
  return (
    <section aria-labelledby="swarm-model-assignments-title" className="space-y-6">
      <div>
        <h2 id="swarm-model-assignments-title" className="text-lg font-semibold text-[var(--app-text)]">Swarm models</h2>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">Configure the canonical Action and Plan models directly. These settings do not create or assign model favorites.</p>
      </div>
      {validationError || error ? <div role="alert" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{validationError || error}</div> : null}
      <form className="space-y-5" onSubmit={submit}>
        <DirectModelEditor label="Action model" value={draftAction} modelOptions={normalizedOptions} disabled={disabled} onChange={(value) => { setDraftAction(value); setValidationError(null) }} />
        <DirectModelEditor label="Plan model" value={draftPlan} modelOptions={normalizedOptions} disabled={disabled} onChange={(value) => { setDraftPlan(value); setValidationError(null) }} />
        <div className="flex justify-end"><Button type="submit" variant="primary" disabled={disabled}>{saving ? 'Saving…' : 'Save Swarm models'}</Button></div>
      </form>
    </section>
  )
}
