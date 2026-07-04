import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bot, Check, ChevronDown, Cpu, ExternalLink, GitBranch, Lightbulb, Lock, Zap, ZapOff } from 'lucide-react'
import type { AgentProfileRecord, ModelOptionRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { displayModelName, effectiveContextWindow, formatContextWindow, formatModelPricing, modelServiceTierOptions, normalizeModelServiceTier, supportsModelServiceTier } from '../services/model-options'

export type AgentModelControlProfilePatch = Partial<Pick<AgentProfileRecord,
  | 'modelMode'
  | 'provider'
  | 'model'
  | 'thinking'
  | 'planProvider'
  | 'planModel'
  | 'planThinking'
  | 'planServiceTier'
  | 'autoProvider'
  | 'autoModel'
  | 'autoThinking'
  | 'autoServiceTier'
>>

export type AgentModelControlAction =
  | { kind: 'default'; agentPatch: AgentModelControlProfilePatch; defaultPreference: ModelDraft }
  | { kind: 'single'; agentPatch: AgentModelControlProfilePatch }
  | { kind: 'split'; agentPatch: AgentModelControlProfilePatch }

export type AgentModelControlConfirmInput = {
  agentName: string
  profile: AgentProfileRecord
  action: AgentModelControlAction
}

interface AgentModelControlProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  mode: DesktopSessionMode
  selectedModel: ModelOptionRecord | null
  selectedServiceTier?: string
  selectedThinking?: string
  modelOptions: ModelOptionRecord[]
  modelLocked?: boolean
  modelLockNotice?: string
  triggerDetail?: string
  openSignal?: number
  onOpenAgentSettings?: () => void
  onConfirmAgentSettings?: (input: AgentModelControlConfirmInput) => void | Promise<void>
  busy?: boolean
}

const FALLBACK_THINKING_OPTIONS = ['off', 'low', 'medium', 'high', 'xhigh']
type DraftMode = 'default' | 'single' | 'split'
export type ModelDraft = { provider: string; model: string; thinking: string; serviceTier: string }

function agentMode(profile: AgentProfileRecord): string {
  return (profile.mode || 'primary').trim().toLowerCase()
}

function agentLabel(profile: AgentProfileRecord): string {
  return profile.name === 'swarm' ? 'Swarm' : profile.name
}

function agentModeLabel(profile: AgentProfileRecord): string {
  switch (agentMode(profile)) {
    case 'primary': return 'Primary'
    case 'subagent': return 'Subagent'
    case 'background': return 'Background'
    default: return profile.mode || 'Agent'
  }
}

function runtimeLabel(profile: AgentProfileRecord | null): string {
  const raw = profile?.exitPlanModeEnabled ? 'plan_auto' : profile?.runtimeMode || profile?.executionSetting || ''
  switch (raw) {
    case 'plan_auto': return 'starts in plan'
    case 'read': return 'read-only'
    case 'readwrite': return 'auto-capable'
    default: return 'runtime default'
  }
}

function modelBehaviorLabel(profile: AgentProfileRecord | null): string {
  if (!profile) return 'Default model'
  if (profile.modelMode === 'split') return 'Split plan/auto models'
  if (profile.provider.trim() || profile.model.trim()) return 'Single model lock'
  return 'Default model'
}

function selectedDraftMode(profile: AgentProfileRecord | null): DraftMode {
  if (!profile) return 'default'
  if (profile.modelMode === 'split') return 'split'
  if (profile.provider.trim() || profile.model.trim()) return 'single'
  return 'default'
}

function modelOptionFor(provider: string, model: string, modelOptions: ModelOptionRecord[]): ModelOptionRecord | null {
  return modelOptions.find((candidate) => candidate.provider === provider && candidate.model === model) ?? null
}

function normalizeDraftServiceTier(provider: string, value: string): string {
  return normalizeModelServiceTier(provider, value)
}

function modelSupportsServiceTier(provider: string, model: string, modelOptions: ModelOptionRecord[], tier = ''): boolean {
  const option = modelOptionFor(provider, model, modelOptions)
  return supportsModelServiceTier(provider, model, option?.serviceTiers ?? [], tier)
}

function serviceTierOptionsForDraft(draft: ModelDraft, modelOptions: ModelOptionRecord[]) {
  const option = modelOptionFor(draft.provider, draft.model, modelOptions)
  return modelServiceTierOptions(draft.provider, draft.model, option?.serviceTiers ?? [])
}

function serviceTierLabel(provider: string, model: string, modelOptions: ModelOptionRecord[], value: string): string {
  const normalized = normalizeDraftServiceTier(provider, value)
  const options = serviceTierOptionsForDraft({ provider, model, thinking: '', serviceTier: normalized }, modelOptions)
  return options.find((option) => option.value === normalized)?.label ?? (normalized || 'Off / standard')
}

function defaultDraftFromModel(model: ModelOptionRecord | null, selectedServiceTier = '', selectedThinking = ''): ModelDraft {
  const provider = model?.provider ?? ''
  const modelID = model?.model ?? ''
  const requestedServiceTier = normalizeDraftServiceTier(provider, selectedServiceTier)
  const fallbackServiceTier = normalizeDraftServiceTier(provider, model?.defaultServiceTier ?? '')
  const resolvedServiceTier = requestedServiceTier || fallbackServiceTier
  return {
    provider,
    model: modelID,
    thinking: selectedThinking.trim() || defaultThinkingForOption(model),
    serviceTier: modelSupportsServiceTier(provider, modelID, model ? [model] : [], resolvedServiceTier) ? resolvedServiceTier : '',
  }
}

function singleDraftFromProfile(profile: AgentProfileRecord | null, selectedModel: ModelOptionRecord | null, selectedServiceTier = '', selectedThinking = ''): ModelDraft {
  const fallback = defaultDraftFromModel(selectedModel, selectedServiceTier, selectedThinking)
  const hasExplicitSingleModel = Boolean(profile?.provider.trim() || profile?.model.trim())
  const provider = hasExplicitSingleModel ? profile?.provider.trim() || fallback.provider : fallback.provider
  return {
    provider,
    model: hasExplicitSingleModel ? profile?.model.trim() || fallback.model : fallback.model,
    thinking: hasExplicitSingleModel ? profile?.thinking.trim() || fallback.thinking : fallback.thinking,
    serviceTier: hasExplicitSingleModel ? normalizeDraftServiceTier(provider, profile?.autoServiceTier ?? '') : fallback.serviceTier,
  }
}

function splitDraftFromProfile(profile: AgentProfileRecord | null, prefix: 'plan' | 'auto', selectedModel: ModelOptionRecord | null, selectedServiceTier = '', selectedThinking = ''): ModelDraft {
  const fallback = defaultDraftFromModel(selectedModel, selectedServiceTier, selectedThinking)
  if (prefix === 'plan') {
    const provider = profile?.planProvider.trim() || fallback.provider
    return {
      provider,
      model: profile?.planModel.trim() || fallback.model,
      thinking: profile?.planThinking.trim() || fallback.thinking,
      serviceTier: normalizeDraftServiceTier(provider, profile?.planServiceTier ?? ''),
    }
  }
  const provider = profile?.autoProvider.trim() || fallback.provider
  return {
    provider,
    model: profile?.autoModel.trim() || fallback.model,
    thinking: profile?.autoThinking.trim() || fallback.thinking,
    serviceTier: normalizeDraftServiceTier(provider, profile?.autoServiceTier ?? ''),
  }
}

function providerOptions(modelOptions: ModelOptionRecord[]): string[] {
  return Array.from(new Set(modelOptions.map((option) => option.provider.trim()).filter(Boolean))).sort((left, right) => left.localeCompare(right))
}

function modelChoices(provider: string, modelOptions: ModelOptionRecord[]): ModelOptionRecord[] {
  const normalized = provider.trim()
  return modelOptions.filter((option) => option.provider === normalized)
}

function modelOptionKey(option: ModelOptionRecord): string {
  return `${option.model}::${option.contextMode.trim().toLowerCase()}`
}

function modelContextLabel(option: ModelOptionRecord): string {
  const contextWindow = effectiveContextWindow(option.provider, option.model, option.contextMode, option.contextWindow)
  return contextWindow > 0 ? formatContextWindow(contextWindow) : ''
}

function normalizeThinking(value: string): string {
  return value.trim().toLowerCase() || 'off'
}

function thinkingOptionsForOption(option: ModelOptionRecord | null): string[] {
  const seen = new Set<string>()
  const source = option?.thinkingOptions?.length ? option.thinkingOptions : FALLBACK_THINKING_OPTIONS
  const out: string[] = []
  for (const item of source) {
    const normalized = normalizeThinking(item)
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    out.push(normalized)
  }
  return out.length > 0 ? out : FALLBACK_THINKING_OPTIONS
}

function defaultThinkingForOption(option: ModelOptionRecord | null): string {
  const options = thinkingOptionsForOption(option)
  const declaredDefault = normalizeThinking(option?.defaultThinking ?? '')
  if (options.includes(declaredDefault)) return declaredDefault
  const favoriteDefault = normalizeThinking(option?.thinking ?? '')
  if (options.includes(favoriteDefault)) return favoriteDefault
  if (options.includes('off')) return 'off'
  return options[0] ?? 'off'
}

function normalizeDraftThinking(provider: string, model: string, modelOptions: ModelOptionRecord[], value: string): string {
  const option = modelOptionFor(provider, model, modelOptions)
  const options = thinkingOptionsForOption(option)
  const normalized = normalizeThinking(value)
  return options.includes(normalized) ? normalized : defaultThinkingForOption(option)
}

function buildPatch(mode: DraftMode, single: ModelDraft, plan: ModelDraft, auto: ModelDraft, modelOptions: ModelOptionRecord[]): AgentModelControlProfilePatch {
  if (mode === 'default') {
    return {
      modelMode: 'single',
      provider: '',
      model: '',
      thinking: '',
      planProvider: '',
      planModel: '',
      planThinking: '',
      planServiceTier: '',
      autoProvider: '',
      autoModel: '',
      autoThinking: '',
      autoServiceTier: '',
    }
  }
  if (mode === 'single') {
    return {
      modelMode: 'single',
      provider: single.provider.trim(),
      model: single.model.trim(),
      thinking: normalizeDraftThinking(single.provider, single.model, modelOptions, single.thinking),
      planProvider: '',
      planModel: '',
      planThinking: '',
      planServiceTier: '',
      autoProvider: '',
      autoModel: '',
      autoThinking: '',
      autoServiceTier: modelSupportsServiceTier(single.provider, single.model, modelOptions, single.serviceTier) ? normalizeDraftServiceTier(single.provider, single.serviceTier) : '',
    }
  }
  return {
    modelMode: 'split',
    provider: '',
    model: '',
    thinking: '',
    planProvider: plan.provider.trim(),
    planModel: plan.model.trim(),
    planThinking: normalizeDraftThinking(plan.provider, plan.model, modelOptions, plan.thinking),
    planServiceTier: modelSupportsServiceTier(plan.provider, plan.model, modelOptions, plan.serviceTier) ? normalizeDraftServiceTier(plan.provider, plan.serviceTier) : '',
    autoProvider: auto.provider.trim(),
    autoModel: auto.model.trim(),
    autoThinking: normalizeDraftThinking(auto.provider, auto.model, modelOptions, auto.thinking),
    autoServiceTier: modelSupportsServiceTier(auto.provider, auto.model, modelOptions, auto.serviceTier) ? normalizeDraftServiceTier(auto.provider, auto.serviceTier) : '',
  }
}

export function AgentModelControl({
  currentAgent,
  selectedPrimaryAgent,
  agents,
  mode,
  selectedModel,
  selectedServiceTier = '',
  selectedThinking = '',
  modelOptions,
  modelLocked = false,
  modelLockNotice = '',
  triggerDetail = '',
  openSignal = 0,
  onOpenAgentSettings,
  onConfirmAgentSettings,
  busy = false,
}: AgentModelControlProps) {
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const selectableAgents = useMemo(() => agents.filter((agent) => agent.enabled !== false), [agents])
  const activeProfile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? selectableAgents.find((agent) => agent.name === currentAgent) ?? null
  const [draftAgentName, setDraftAgentName] = useState(activeProfile?.name ?? selectedPrimaryAgent)
  const draftProfile = selectableAgents.find((agent) => agent.name === draftAgentName) ?? activeProfile
  const [draftMode, setDraftMode] = useState<DraftMode>(() => selectedDraftMode(activeProfile))
  const [singleDraft, setSingleDraft] = useState<ModelDraft>(() => singleDraftFromProfile(activeProfile, selectedModel, selectedServiceTier, selectedThinking))
  const [planDraft, setPlanDraft] = useState<ModelDraft>(() => splitDraftFromProfile(activeProfile, 'plan', selectedModel, selectedServiceTier, selectedThinking))
  const [autoDraft, setAutoDraft] = useState<ModelDraft>(() => splitDraftFromProfile(activeProfile, 'auto', selectedModel, selectedServiceTier, selectedThinking))
  const providers = useMemo(() => providerOptions(modelOptions), [modelOptions])
  const agentSections = useMemo(() => {
    const sections = [
      { label: 'Primary agents', profiles: selectableAgents.filter((agent) => agentMode(agent) === 'primary') },
      { label: 'Subagents', profiles: selectableAgents.filter((agent) => agentMode(agent) === 'subagent') },
      { label: 'Other agents', profiles: selectableAgents.filter((agent) => {
        const profileMode = agentMode(agent)
        return profileMode !== 'primary' && profileMode !== 'subagent'
      }) },
    ]
    return sections.filter((section) => section.profiles.length > 0)
  }, [selectableAgents])
  const pricingLabel = selectedModel ? formatModelPricing(selectedModel.pricing) : ''
  const selectedModelLabel = selectedModel
    ? `${selectedModel.provider}/${displayModelName(selectedModel.provider, selectedModel.model, selectedModel.contextMode)}`
    : 'Default model'
  const normalizedSelectedThinking = selectedThinking.trim() || defaultThinkingForOption(selectedModel)
  const selectedServiceTierSupported = selectedModel ? supportsModelServiceTier(selectedModel.provider, selectedModel.model, selectedModel.serviceTiers) : false
  const normalizedSelectedServiceTier = normalizeDraftServiceTier(selectedModel?.provider ?? '', selectedServiceTier)
  const selectedServiceTierLabel = normalizedSelectedServiceTier ? serviceTierLabel(selectedModel?.provider ?? '', selectedModel?.model ?? '', modelOptions, normalizedSelectedServiceTier) : 'standard'
  const SelectedServiceTierIcon = normalizedSelectedServiceTier ? Zap : ZapOff

  useEffect(() => {
    if (openSignal > 0) setOpen(true)
  }, [openSignal])

  useEffect(() => {
    if (!open) return
    const profile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? activeProfile
    setDraftAgentName(profile?.name ?? selectedPrimaryAgent)
    setDraftMode(selectedDraftMode(profile))
    setSingleDraft(singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking))
    setPlanDraft(splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking))
    setAutoDraft(splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking))
    setError(null)
  }, [activeProfile, open, selectableAgents, selectedModel, selectedPrimaryAgent, selectedServiceTier, selectedThinking])

  function chooseAgent(profile: AgentProfileRecord) {
    setDraftAgentName(profile.name)
    setDraftMode(selectedDraftMode(profile))
    setSingleDraft(singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking))
    setPlanDraft(splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking))
    setAutoDraft(splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking))
    setError(null)
  }

  function selectProvider(target: 'single' | 'plan' | 'auto', provider: string) {
    const update = (current: ModelDraft): ModelDraft => ({ ...current, provider, model: '', serviceTier: '' })
    if (target === 'single') setSingleDraft(update)
    else if (target === 'plan') setPlanDraft(update)
    else setAutoDraft(update)
  }

  function selectModel(target: 'single' | 'plan' | 'auto', model: string) {
    const update = (current: ModelDraft): ModelDraft => ({ ...current, model, serviceTier: modelSupportsServiceTier(current.provider, model, modelOptions, current.serviceTier) ? current.serviceTier : '' })
    if (target === 'single') setSingleDraft(update)
    else if (target === 'plan') setPlanDraft(update)
    else setAutoDraft(update)
  }

  async function confirm() {
    const profile = draftProfile
    if (!profile || saving || busy) return
    const agentPatch = buildPatch(draftMode, singleDraft, planDraft, autoDraft, modelOptions)
    const action: AgentModelControlAction = draftMode === 'default'
      ? {
        kind: 'default',
        agentPatch,
        defaultPreference: {
          ...singleDraft,
          provider: singleDraft.provider.trim(),
          model: singleDraft.model.trim(),
          thinking: normalizeDraftThinking(singleDraft.provider, singleDraft.model, modelOptions, singleDraft.thinking),
          serviceTier: modelSupportsServiceTier(singleDraft.provider, singleDraft.model, modelOptions, singleDraft.serviceTier) ? normalizeDraftServiceTier(singleDraft.provider, singleDraft.serviceTier) : '',
        },
      }
      : draftMode === 'single'
        ? { kind: 'single', agentPatch }
        : { kind: 'split', agentPatch }
    if (action.kind === 'default' && (!action.defaultPreference.provider || !action.defaultPreference.model || !action.defaultPreference.thinking)) {
      setError('Choose provider, model, and thinking for your default model settings.')
      return
    }
    if (action.kind === 'single' && (!action.agentPatch.provider || !action.agentPatch.model || !action.agentPatch.thinking)) {
      setError('Choose provider, model, and thinking for the single-model lock.')
      return
    }
    if (action.kind === 'split' && (!action.agentPatch.planProvider || !action.agentPatch.planModel || !action.agentPatch.planThinking || !action.agentPatch.autoProvider || !action.agentPatch.autoModel || !action.agentPatch.autoThinking)) {
      setError('Choose provider, model, and thinking for both plan and auto split settings.')
      return
    }
    setSaving(true)
    setError(null)
    try {
      await onConfirmAgentSettings?.({ agentName: profile.name, profile, action })
      setOpen(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const modal = open ? createPortal(
    <div className="fixed inset-0 z-[9999] flex items-end justify-center bg-black/50 p-3 sm:items-center" role="dialog" aria-modal="true" aria-label="Agent and model settings">
      <div className="flex max-h-[min(94vh,880px)] w-full max-w-6xl flex-col overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl">
        <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4">
          <div className="min-w-0">
            <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent setup</div>
            <div className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]">{draftProfile?.name || currentAgent || 'Agent'}</div>
            <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Changes are staged here and saved to the agent profile only when confirmed.</div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {onOpenAgentSettings ? (
              <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="inline-flex items-center gap-1 rounded-lg border border-[var(--app-border)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                Agents <ExternalLink size={12} />
              </button>
            ) : null}
            <button type="button" onClick={() => setOpen(false)} className="rounded-lg border border-[var(--app-border)] px-3 py-1 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">Close</button>
          </div>
        </div>

        <div className="grid min-h-0 flex-1 min-[780px]:grid-cols-[280px_minmax(0,1fr)]">
          <div className="min-h-0 border-b border-[var(--app-border)] min-[780px]:border-b-0 min-[780px]:border-r">
            <div className="border-b border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Choose agent</div>
            <div className="max-h-56 overflow-y-auto py-1 min-[780px]:max-h-[660px]">
              {agentSections.map((section, sectionIndex) => (
                <div key={section.label} className={sectionIndex === 0 ? '' : 'mt-1 border-t border-[var(--app-border)] pt-1'}>
                  <div className="px-3 pb-1 pt-2 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{section.label}</div>
                  {section.profiles.map((profile) => {
                    const selected = profile.name === draftAgentName
                    return (
                      <button key={profile.name} type="button" onClick={() => chooseAgent(profile)} className={`flex w-full items-start gap-2 px-3 py-2.5 text-left text-sm transition ${selected ? 'bg-[var(--app-surface-subtle)] text-[var(--app-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}>
                        {selected ? <Check size={14} className="mt-0.5 shrink-0 text-[var(--app-primary)]" /> : <span className="mt-0.5 w-[14px] shrink-0" />}
                        <Bot size={14} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate font-medium">{agentLabel(profile)}</span>
                          <span className="mt-0.5 block truncate text-[11px] text-[var(--app-text-subtle)]">{modelBehaviorLabel(profile)} · {agentModeLabel(profile)}</span>
                        </span>
                      </button>
                    )
                  })}
                </div>
              ))}
            </div>
          </div>

          <div className="min-h-0 overflow-y-auto p-5">
            <div className="grid gap-3 sm:grid-cols-3">
              <SummaryCard label="Session mode" value={mode} />
              <SummaryCard label="Runtime" value={runtimeLabel(draftProfile)} />
              <SummaryCard label="Current resolved model" value={selectedModelLabel} detail={pricingLabel} />
            </div>

            <div className="mt-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
              <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent model policy</div>
              <div className="mt-3 grid gap-2 sm:grid-cols-3">
                <ModeButton selected={draftMode === 'default'} title="Default" description="Show your current defaults here. Editing them changes the defaults used by future default-mode agents and conversations." onClick={() => { setDraftMode('default'); setSingleDraft(defaultDraftFromModel(selectedModel, selectedServiceTier, selectedThinking)) }} />
                <ModeButton selected={draftMode === 'single'} title="Single" description="Lock this agent to one model." onClick={() => setDraftMode('single')} />
                <ModeButton selected={draftMode === 'split'} title="Split" description="Use separate plan and auto models." onClick={() => setDraftMode('split')} />
              </div>
              {modelLocked ? (
                <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                  <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                  <span>{modelLockNotice || 'The current session is resolved from the selected agent profile.'}</span>
                </div>
              ) : null}
            </div>

            {draftMode === 'single' ? (
              <ModelDraftEditor title="Single model" draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('single', provider)} onModelChange={(model) => selectModel('single', model)} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
            ) : draftMode === 'split' ? (
              <div className="mt-4 grid gap-3 lg:grid-cols-2">
                <ModelDraftEditor title="Plan model" draft={planDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('plan', provider)} onModelChange={(model) => selectModel('plan', model)} onThinkingChange={(thinking) => setPlanDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setPlanDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
                <ModelDraftEditor title="Auto model" draft={autoDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('auto', provider)} onModelChange={(model) => selectModel('auto', model)} onThinkingChange={(thinking) => setAutoDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setAutoDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </div>
            ) : (
              <div className="mt-4 grid gap-3">
                <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 text-sm text-[var(--app-text-muted)]">
                  This agent is using your defaults. The fields below show those defaults; changing them updates your future default model settings instead of locking this agent.
                </div>
                <ModelDraftEditor title="Default model settings" draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('single', provider)} onModelChange={(model) => selectModel('single', model)} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </div>
            )}
            {error ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          </div>
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4">
          <button type="button" onClick={() => setOpen(false)} className="rounded-lg border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">Cancel</button>
          <button type="button" disabled={busy || saving || !draftProfile} onClick={() => { void confirm() }} className="rounded-lg bg-[var(--app-primary)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-60">
            {saving || busy ? 'Saving…' : 'Confirm changes'}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  ) : null

  return (
    <div className="inline-flex min-w-0 items-center">
      <button
        type="button"
        onClick={() => setOpen(true)}
        title={triggerDetail ? `Open defaults for ${currentAgent || selectedPrimaryAgent || 'Agent'}: ${triggerDetail}` : 'Open agent and model setup'}
        className="inline-flex min-w-0 items-center gap-1.5 rounded-full border border-transparent px-2 py-1 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
      >
        <Bot size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="max-w-[110px] truncate text-[var(--app-text)]">{currentAgent || selectedPrimaryAgent || 'Agent'}</span>
        <span className="hidden min-w-0 max-w-[260px] items-center gap-1 truncate text-[var(--app-text-muted)] min-[1120px]:inline-flex">
          {selectedModel ? (
            <>
              <span className="truncate">{selectedModelLabel}</span>
              <Lightbulb size={12} className="shrink-0 text-[var(--app-text-subtle)]" />
              <span>{normalizedSelectedThinking}</span>
              {selectedServiceTierSupported ? <SelectedServiceTierIcon size={12} className="shrink-0 text-[var(--app-text-subtle)]" /> : null}
              {selectedServiceTierSupported ? <span>{selectedServiceTierLabel}</span> : null}
            </>
          ) : (triggerDetail || modelBehaviorLabel(activeProfile))}
        </span>
        <ChevronDown size={12} className="shrink-0" />
      </button>
      {modal}
    </div>
  )
}

function SummaryCard({ label, value, detail = '' }: { label: string; value: string; detail?: string }) {
  return (
    <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-[11px]">
      <div className="font-semibold text-[var(--app-text-subtle)]">{label}</div>
      <div className="mt-1 break-words text-[var(--app-text)]">{value || '—'}</div>
      {detail ? <div className="mt-1 text-[var(--app-text-muted)]">{detail}</div> : null}
    </div>
  )
}

function ModeButton({ selected, title, description, onClick }: { selected: boolean; title: string; description: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={`rounded-xl border px-3 py-2 text-left transition ${selected ? 'border-[var(--app-border-accent)] bg-[var(--app-primary-soft)] text-[var(--app-text)]' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}>
      <span className="flex items-center gap-2 text-sm font-semibold">{selected ? <Check size={14} className="text-[var(--app-primary)]" /> : null}{title}</span>
      <span className="mt-1 block text-[11px] leading-4">{description}</span>
    </button>
  )
}

function ModelDraftEditor({
  title,
  draft,
  providers,
  modelOptions,
  showServiceTier = false,
  onProviderChange,
  onModelChange,
  onThinkingChange,
  onServiceTierChange,
}: {
  title: string
  draft: ModelDraft
  providers: string[]
  modelOptions: ModelOptionRecord[]
  showServiceTier?: boolean
  onProviderChange: (provider: string) => void
  onModelChange: (model: string) => void
  onThinkingChange: (thinking: string) => void
  onServiceTierChange?: (serviceTier: string) => void
}) {
  const choices = modelChoices(draft.provider, modelOptions)
  const selectedOption = choices.find((option) => option.model === draft.model) ?? null
  const serviceTierOptions = serviceTierOptionsForDraft(draft, modelOptions)
  const serviceTierSupported = serviceTierOptions.length > 1
  const normalizedServiceTier = serviceTierSupported ? normalizeDraftServiceTier(draft.provider, draft.serviceTier) : ''
  const thinkingOptions = thinkingOptionsForOption(selectedOption)
  const normalizedThinking = thinkingOptions.includes(normalizeThinking(draft.thinking)) ? normalizeThinking(draft.thinking) : defaultThinkingForOption(selectedOption)
  return (
    <div className="mt-4 rounded-xl border border-[var(--app-border)] p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"><GitBranch size={14} />{title}</div>
      <div className="grid gap-3 lg:grid-cols-[180px_minmax(0,1fr)]">
        <SelectField label="Provider" value={draft.provider} onChange={onProviderChange} options={providers.map((provider) => ({ label: provider, value: provider }))} placeholder="Choose provider" />
        <ModelSelectField label="Model" value={draft.model} onChange={onModelChange} options={choices} placeholder="Choose model" disabled={!draft.provider.trim()} />
        <SelectField label="Thinking" value={normalizedThinking} onChange={onThinkingChange} options={thinkingOptions.map((option) => ({ label: option, value: option }))} />
        {showServiceTier ? <SelectField label="Service tier" value={normalizedServiceTier} onChange={(value) => onServiceTierChange?.(normalizeDraftServiceTier(draft.provider, value))} options={serviceTierOptions} disabled={!serviceTierSupported} /> : null}
      </div>
      {selectedOption ? <ModelInfoPanel option={selectedOption} /> : null}
    </div>
  )
}

function ModelSelectField({ label, value, options, placeholder = '', disabled = false, onChange }: { label: string; value: string; options: ModelOptionRecord[]; placeholder?: string; disabled?: boolean; onChange: (value: string) => void }) {
  return (
    <label className="grid gap-1.5 text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)] lg:col-span-1">
      {label}
      <span className="relative">
        <select value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 pr-8 text-sm normal-case tracking-normal text-[var(--app-text)] outline-none transition hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50">
          {placeholder ? <option value="" disabled>{placeholder}</option> : null}
          {options.map((option) => {
            const contextLabel = modelContextLabel(option)
            const pricingLabel = formatModelPricing(option.pricing)
            const meta = [contextLabel ? `Context ${contextLabel}` : '', pricingLabel].filter(Boolean).join(' · ')
            const labelText = `${displayModelName(option.provider, option.model, option.contextMode)}${meta ? ` — ${meta}` : ''}`
            return <option key={`model:${modelOptionKey(option)}`} value={option.model}>{labelText}</option>
          })}
        </select>
        <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
      </span>
    </label>
  )
}

function ModelInfoPanel({ option }: { option: ModelOptionRecord }) {
  const contextLabel = modelContextLabel(option)
  const pricingLabel = formatModelPricing(option.pricing)
  const serviceTiers = option.serviceTiers.map((tier) => tier.trim()).filter(Boolean)
  const details = [
    { label: 'Provider', value: option.provider },
    { label: 'Context', value: contextLabel || 'Unknown' },
    { label: 'Price', value: pricingLabel || 'Not listed' },
    { label: 'Thinking', value: option.thinking || 'default' },
    serviceTiers.length > 0 ? { label: 'Tiers', value: serviceTiers.join(', ') } : null,
  ].filter(Boolean) as Array<{ label: string; value: string }>
  return (
    <div className="mt-4 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
      <div className="flex items-start gap-2">
        <Cpu size={14} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold text-[var(--app-text)]">{displayModelName(option.provider, option.model, option.contextMode)}</div>
          <div className="mt-1 break-words text-[11px] text-[var(--app-text-muted)]">{option.label || option.model}</div>
        </div>
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {details.map((detail) => (
          <div key={detail.label} className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-2">
            <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{detail.label}</div>
            <div className="mt-1 break-words text-[11px] text-[var(--app-text)]">{detail.value}</div>
          </div>
        ))}
      </div>
    </div>
  )
}

function SelectField({ label, value, options, placeholder = '', disabled = false, onChange }: { label: string; value: string; options: Array<{ label: string; value: string }>; placeholder?: string; disabled?: boolean; onChange: (value: string) => void }) {
  return (
    <label className="grid gap-1.5 text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
      {label}
      <span className="relative">
        <select value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 pr-8 text-sm normal-case tracking-normal text-[var(--app-text)] outline-none transition hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50">
          {placeholder ? <option value="" disabled>{placeholder}</option> : null}
          {options.map((option) => <option key={`${label}:${option.value || 'empty'}`} value={option.value}>{option.label}</option>)}
        </select>
        <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
      </span>
    </label>
  )
}
