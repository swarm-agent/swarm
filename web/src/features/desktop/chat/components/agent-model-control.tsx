import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronDown, Cpu, GitBranch, Lightbulb, Lock, Settings2, Zap, ZapOff } from 'lucide-react'
import type { AgentProfileRecord, ModelOptionRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { defaultModelThinking, displayModelName, effectiveContextWindow, formatContextWindow, formatModelPricing, modelServiceTierOptions, modelThinkingOptions, normalizeModelServiceTier, normalizeModelThinking, supportsModelServiceTier } from '../services/model-options'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { saveSystemAgentSettings } from '../../settings/swarm/mutations/save-system-agent-settings'
import { normalizeExplorerAgentSettings } from '../../settings/swarm/types/swarm-settings'
import { displayAgentName } from '../services/agent-display'

export type AgentModelControlProfilePatch = Partial<Pick<AgentProfileRecord,
  | 'defaultSessionMode'
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
  showTrigger?: boolean
  initialAgentName?: string
}

type DraftMode = 'single' | 'split'
const EXPLORER_AGENT_NAME = 'system-explorer'
const CLONE_AGENT_NAME = 'system-clone'
const SWARM_AGENT_NAME = 'swarm'

function isSystemUtility(name: string): boolean {
  return name === EXPLORER_AGENT_NAME
}

function isCompiledSystemAgent(name: string): boolean {
  return isSystemUtility(name) || name === CLONE_AGENT_NAME || name === SWARM_AGENT_NAME
}
export type ModelDraft = { provider: string; model: string; thinking: string; serviceTier: string }

function agentMode(profile: AgentProfileRecord): string {
  return (profile.mode || 'primary').trim().toLowerCase()
}

function agentLabel(profile: AgentProfileRecord): string {
  return displayAgentName(profile.name)
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
  if (!profile) return 'Single model'
  if (profile.modelMode === 'split' && isPlanCapableAgent(profile)) return 'Split plan/action models'
  return 'Single model'
}

function isPlanCapableAgent(profile: AgentProfileRecord | null): boolean {
  if (!profile) return false
  if (profile.exitPlanModeEnabled || profile.runtimeMode === 'plan_auto') return true
  if (profile.runtimeMode === 'read' || profile.runtimeMode === 'readwrite' || profile.executionSetting === 'read' || profile.executionSetting === 'readwrite') return false
  const tools = profile.toolContract?.tools ?? {}
  const planManage = tools.plan_manage ?? tools['plan-manage']
  const exitPlanMode = tools.exit_plan_mode ?? tools['exit-plan-mode']
  return Boolean(planManage?.enabled || exitPlanMode?.enabled)
}

function selectedDraftMode(profile: AgentProfileRecord | null): DraftMode {
  return profile?.modelMode === 'split' && isPlanCapableAgent(profile) ? 'split' : 'single'
}

function modelOptionFor(provider: string, model: string, modelOptions: ModelOptionRecord[]): ModelOptionRecord | null {
  return modelOptions.find((candidate) => candidate.provider === provider && candidate.model === model) ?? null
}

function normalizeDraftServiceTier(provider: string, value: string): string {
  return normalizeModelServiceTier(provider, value)
}

function modelSupportsServiceTier(provider: string, model: string, modelOptions: ModelOptionRecord[], tier = ''): boolean {
  const option = modelOptionFor(provider, model, modelOptions)
  return supportsModelServiceTier(provider, model, option ?? { serviceTiers: [], serviceTierMappings: [] }, tier)
}

function serviceTierOptionsForDraft(draft: ModelDraft, modelOptions: ModelOptionRecord[]) {
  const option = modelOptionFor(draft.provider, draft.model, modelOptions)
  return modelServiceTierOptions(draft.provider, draft.model, option ?? { serviceTiers: [], serviceTierMappings: [] })
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
  return normalizeModelThinking(value)
}

function thinkingOptionsForOption(option: ModelOptionRecord | null): string[] {
  return modelThinkingOptions(option)
}

function defaultThinkingForOption(option: ModelOptionRecord | null): string {
  return defaultModelThinking(option)
}

function normalizeDraftThinking(provider: string, model: string, modelOptions: ModelOptionRecord[], value: string): string {
  const option = modelOptionFor(provider, model, modelOptions)
  const options = thinkingOptionsForOption(option)
  const normalized = normalizeThinking(value)
  return options.includes(normalized) ? normalized : defaultThinkingForOption(option)
}

function buildPatch(mode: DraftMode, single: ModelDraft, plan: ModelDraft, auto: ModelDraft, modelOptions: ModelOptionRecord[]): AgentModelControlProfilePatch {
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
  showTrigger = true,
  initialAgentName = '',
}: AgentModelControlProps) {
  const queryClient = useQueryClient()
  const { data: uiSettings = {} } = useQuery(uiSettingsQueryOptions())
  const explorerSettings = normalizeExplorerAgentSettings(uiSettings)
  const explorerProfile = useMemo<AgentProfileRecord>(() => ({
    name: EXPLORER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled repository and web research subagent',
    provider: explorerSettings.provider,
    model: explorerSettings.model,
    thinking: explorerSettings.thinking,
    modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: explorerSettings.service_tier,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: { read: { enabled: true, bashPrefixes: [] }, search: { enabled: true, bashPrefixes: [] }, list: { enabled: true, bashPrefixes: [] }, websearch: { enabled: true, bashPrefixes: [] }, webfetch: { enabled: true, bashPrefixes: [] } } },
    enabled: true, protected: true, updatedAt: 0,
  }), [explorerSettings.model, explorerSettings.provider, explorerSettings.service_tier, explorerSettings.thinking])
  const cloneProfile = useMemo<AgentProfileRecord>(() => ({
    name: CLONE_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled task-only implementation subagent',
    provider: '', model: '', thinking: '', modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: '',
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null, toolContract: null,
    enabled: true, protected: true, updatedAt: 0,
  }), [])
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [primaryLayoutVariant, setPrimaryLayoutVariant] = useState(1)
  const [error, setError] = useState<string | null>(null)
  const selectableAgents = useMemo(() => [...agents.filter((agent) => agent.enabled !== false && agent.name !== 'explorer' && (!isCompiledSystemAgent(agent.name) || agent.name === SWARM_AGENT_NAME)), explorerProfile, cloneProfile], [agents, cloneProfile, explorerProfile])
  const activeProfile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? selectableAgents.find((agent) => agent.name === currentAgent) ?? null
  const [draftAgentName, setDraftAgentName] = useState(activeProfile?.name ?? selectedPrimaryAgent)
  const draftProfile = selectableAgents.find((agent) => agent.name === draftAgentName) ?? activeProfile
  const [draftSessionMode, setDraftSessionMode] = useState<DesktopSessionMode>(() => activeProfile?.defaultSessionMode ?? mode)
  const [draftMode, setDraftMode] = useState<DraftMode>(() => selectedDraftMode(activeProfile))
  const [singleDraft, setSingleDraft] = useState<ModelDraft>(() => singleDraftFromProfile(activeProfile, selectedModel, selectedServiceTier, selectedThinking))
  const [planDraft, setPlanDraft] = useState<ModelDraft>(() => splitDraftFromProfile(activeProfile, 'plan', selectedModel, selectedServiceTier, selectedThinking))
  const [autoDraft, setAutoDraft] = useState<ModelDraft>(() => splitDraftFromProfile(activeProfile, 'auto', selectedModel, selectedServiceTier, selectedThinking))
  const providers = useMemo(() => providerOptions(modelOptions), [modelOptions])
  const splitModeAllowed = isPlanCapableAgent(draftProfile)
  const effectiveDraftMode: DraftMode = draftMode === 'split' && !splitModeAllowed ? 'single' : draftMode
  const agentSections = useMemo(() => {
    const sections = [
      { label: 'Primary agents', profiles: selectableAgents.filter((agent) => agentMode(agent) === 'primary' && !isCompiledSystemAgent(agent.name)) },
      { label: 'Subagents', profiles: selectableAgents.filter((agent) => agentMode(agent) === 'subagent' && !isCompiledSystemAgent(agent.name)) },
      { label: 'System agents', profiles: selectableAgents.filter((agent) => isCompiledSystemAgent(agent.name)) },
      { label: 'Other agents', profiles: selectableAgents.filter((agent) => {
        const profileMode = agentMode(agent)
        return profileMode !== 'primary' && profileMode !== 'subagent' && !isCompiledSystemAgent(agent.name)
      }) },
    ]
    return sections.filter((section) => section.profiles.length > 0)
  }, [selectableAgents])
  const selectedModelLabel = selectedModel
    ? `${selectedModel.provider}/${displayModelName(selectedModel.provider, selectedModel.model, selectedModel.contextMode)}`
    : 'No resolved model'
  const normalizedSelectedThinking = selectedThinking.trim() || defaultThinkingForOption(selectedModel)
  const selectedServiceTierSupported = selectedModel ? supportsModelServiceTier(selectedModel.provider, selectedModel.model, selectedModel) : false
  const normalizedSelectedServiceTier = normalizeDraftServiceTier(selectedModel?.provider ?? '', selectedServiceTier)
  const selectedServiceTierLabel = normalizedSelectedServiceTier ? serviceTierLabel(selectedModel?.provider ?? '', selectedModel?.model ?? '', modelOptions, normalizedSelectedServiceTier) : 'standard'
  const SelectedServiceTierIcon = normalizedSelectedServiceTier ? Zap : ZapOff

  useEffect(() => {
    if (openSignal > 0) setOpen(true)
  }, [openSignal])

  useEffect(() => {
    if (!open) return
    const profile = selectableAgents.find((agent) => agent.name === initialAgentName)
      ?? selectableAgents.find((agent) => agent.name === selectedPrimaryAgent)
      ?? activeProfile
    setDraftAgentName(profile?.name ?? selectedPrimaryAgent)
    setDraftSessionMode(profile?.defaultSessionMode ?? mode)
    setDraftMode(selectedDraftMode(profile))
    setSingleDraft(singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking))
    setPlanDraft(splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking))
    setAutoDraft(splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking))
    setError(null)
  }, [activeProfile, initialAgentName, mode, open, selectableAgents, selectedModel, selectedPrimaryAgent, selectedServiceTier, selectedThinking])

  function chooseAgent(profile: AgentProfileRecord) {
    setDraftAgentName(profile.name)
    setDraftSessionMode(profile.defaultSessionMode)
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
    if (!profile || saving || busy || profile.name === CLONE_AGENT_NAME) return
    const normalizedDraftMode: DraftMode = draftMode === 'split' && !isPlanCapableAgent(profile) ? 'single' : draftMode
    const agentPatch = {
      ...buildPatch(normalizedDraftMode, singleDraft, planDraft, autoDraft, modelOptions),
      defaultSessionMode: draftSessionMode,
    }
    const action: AgentModelControlAction = normalizedDraftMode === 'single'
      ? { kind: 'single', agentPatch }
      : { kind: 'split', agentPatch }
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
      if (isSystemUtility(profile.name)) {
        const saved = await saveSystemAgentSettings({
          current: uiSettings,
          agent: 'explorer',
          settings: {
            provider: String(action.agentPatch.provider ?? '').trim(),
            model: String(action.agentPatch.model ?? '').trim(),
            thinking: String(action.agentPatch.thinking ?? '').trim(),
            service_tier: String(action.agentPatch.autoServiceTier ?? '').trim(),
          },
        })
        queryClient.setQueryData(uiSettingsQueryOptions().queryKey, saved)
      } else {
        await onConfirmAgentSettings?.({ agentName: profile.name, profile, action })
      }
      setOpen(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  const modal = open ? createPortal(
    <div className="fixed inset-0 z-[9999] flex items-end justify-center bg-black/50 p-3 sm:items-center" role="dialog" aria-modal="true" aria-label="Agent and model settings">
      <div className="flex max-h-[min(94vh,880px)] w-full max-w-6xl flex-col overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl">
        <div className="flex items-start justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4">
          <div className="min-w-0">
            <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent setup</div>
            <div className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]">{draftProfile ? agentLabel(draftProfile) : displayAgentName(currentAgent) || 'Agent'}</div>
            <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Changes are staged here and saved to the agent profile only when confirmed.</div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {draftProfile && agentMode(draftProfile) === 'primary' ? (
              <div role="group" aria-label="Primary agent control layout" className="flex items-center gap-0.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] p-0.5">
                <span className="px-1.5 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Layout</span>
                {[1, 2, 3, 4, 5].map((variant) => (
                  <button
                    key={variant}
                    type="button"
                    aria-label={`Show primary agent control layout ${variant}`}
                    aria-pressed={primaryLayoutVariant === variant}
                    onClick={() => setPrimaryLayoutVariant(variant)}
                    className={`h-6 w-6 rounded-md text-[11px] font-semibold transition ${primaryLayoutVariant === variant ? 'bg-[var(--app-primary)] text-[var(--app-primary-text)]' : 'text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}
                  >
                    {variant}
                  </button>
                ))}
              </div>
            ) : null}
            {onOpenAgentSettings ? (
              <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="rounded-lg border border-[var(--app-border)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                Close
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
            {draftProfile?.name === CLONE_AGENT_NAME ? (
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                <div className="font-semibold text-[var(--app-text)]">Compiled system agent</div>
                <div className="mt-1">Clone has no independent model controls. Every task launch inherits its parent session&apos;s provider, model, thinking, and service tier, while its identity, prompt, runtime, tools, worktree isolation, and commit handoff remain code-owned.</div>
              </div>
            ) : draftProfile && isSystemUtility(draftProfile.name) ? (
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                <div className="font-semibold text-[var(--app-text)]">Compiled system agent</div>
                <div className="mt-1">Explorer&apos;s provider, model, thinking level, and priority/service tier are configurable. Its identity, prompt, runtime, and tool contract remain code-owned.</div>
              </div>
            ) : draftProfile && agentMode(draftProfile) === 'primary' ? (
              <PrimaryAgentControlRow
                variant={primaryLayoutVariant}
                sessionMode={draftSessionMode}
                modelMode={effectiveDraftMode}
                splitModeAllowed={splitModeAllowed}
                onSessionModeChange={setDraftSessionMode}
                onModelModeChange={setDraftMode}
              />
            ) : <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              <div className="grid gap-4 lg:grid-cols-2">
                <div>
                  <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Default session mode</div>
                  <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Choose how new sessions start for this agent. Plan can still be toggled in the composer.</div>
                </div>
                <SessionModeChoices value={draftSessionMode} onChange={setDraftSessionMode} />
              </div>
              <div className="mt-3 text-[11px] text-[var(--app-text-subtle)]">Runtime: {runtimeLabel(draftProfile)}. Current action: {mode === 'plan' ? 'Plan' : 'Action'}.</div>
            </div>}

            {draftProfile && agentMode(draftProfile) === 'primary' ? (
              <div className="mt-2 text-[11px] text-[var(--app-text-subtle)]">Runtime: {runtimeLabel(draftProfile)}. Current action: {mode === 'plan' ? 'Plan' : 'Action'}.</div>
            ) : null}

            {!draftProfile || (!isSystemUtility(draftProfile.name) && agentMode(draftProfile) !== 'primary') ? <div className="mt-4 border-y border-[var(--app-border)] py-3">
              <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent model policy</div>
                  <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Use one model everywhere or separate plan and action models.</div>
                </div>
                <ModelPolicyChoices value={effectiveDraftMode} splitModeAllowed={splitModeAllowed} onChange={setDraftMode} />
              </div>
              {!splitModeAllowed ? <div className="mt-2 text-[11px] text-[var(--app-text-subtle)]">Split policy is available only for plan-capable agents.</div> : null}
            </div> : null}
            {!splitModeAllowed && draftProfile && agentMode(draftProfile) === 'primary' ? <div className="mt-2 text-[11px] text-[var(--app-text-subtle)]">Split policy is available only for plan-capable agents.</div> : null}
            {modelLocked && draftProfile?.name !== CLONE_AGENT_NAME && !isSystemUtility(draftProfile?.name ?? '') ? (
              <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                <span>{modelLockNotice || 'The current session is resolved from the selected agent profile.'}</span>
              </div>
            ) : null}

            {draftProfile?.name === CLONE_AGENT_NAME ? null : effectiveDraftMode === 'single' ? (
              <ModelDraftEditor title={draftProfile && isSystemUtility(draftProfile.name) ? 'Explorer model' : 'Single model'} draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('single', provider)} onModelChange={(model) => selectModel('single', model)} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
            ) : (
              <div className="mt-4 grid gap-3 lg:grid-cols-2">
                <ModelDraftEditor title="Plan model" draft={planDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('plan', provider)} onModelChange={(model) => selectModel('plan', model)} onThinkingChange={(thinking) => setPlanDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setPlanDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
                <ModelDraftEditor title="Action model" draft={autoDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('auto', provider)} onModelChange={(model) => selectModel('auto', model)} onThinkingChange={(thinking) => setAutoDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setAutoDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </div>
            )}
            {error ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          </div>
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4">
          <button type="button" onClick={() => setOpen(false)} className="rounded-lg border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">Cancel</button>
          <button type="button" disabled={busy || saving || !draftProfile || draftProfile.name === CLONE_AGENT_NAME} onClick={() => { void confirm() }} className="rounded-lg border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-[11px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-60">
            {saving || busy ? 'Saving…' : 'Confirm changes'}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  ) : null

  return (
    <div className={showTrigger ? 'inline-flex min-w-0 items-center' : 'contents'}>
      {showTrigger ? (
        <button
          type="button"
          onClick={() => setOpen(true)}
          title={triggerDetail ? `Open agent setup: ${triggerDetail}` : 'Open agent setup'}
          className="inline-flex min-w-0 items-center gap-1.5 rounded-full border border-transparent px-2 py-1 text-[11px] font-medium text-[var(--app-text-muted)] transition hover:border-[var(--app-border)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]"
        >
          <Settings2 size={13} className="shrink-0 text-[var(--app-text-subtle)]" />
          <span className="shrink-0 text-[var(--app-text)]">Setup</span>
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
      ) : null}
      {modal}
    </div>
  )
}

function SessionModeChoices({ value, onChange, className = '' }: { value: DesktopSessionMode; onChange: (value: DesktopSessionMode) => void; className?: string }) {
  return (
    <div role="group" aria-label="Default session mode" className={`grid shrink-0 grid-cols-2 gap-1 rounded-lg bg-transparent p-1 ${className}`}>
      <CompactChoice selected={value === 'plan'} label="Plan" onClick={() => onChange('plan')} />
      <CompactChoice selected={value === 'auto'} label="Action" onClick={() => onChange('auto')} />
    </div>
  )
}

function ModelPolicyChoices({ value, splitModeAllowed, onChange, className = '' }: { value: DraftMode; splitModeAllowed: boolean; onChange: (value: DraftMode) => void; className?: string }) {
  return (
    <div role="group" aria-label="Agent model policy" className={`grid shrink-0 grid-cols-2 gap-1 rounded-lg bg-transparent p-1 ${className}`}>
      <CompactChoice selected={value === 'single'} label="Single" onClick={() => onChange('single')} />
      <CompactChoice selected={value === 'split'} label="Split" onClick={() => { if (splitModeAllowed) onChange('split') }} disabled={!splitModeAllowed} />
    </div>
  )
}

function PrimaryAgentControlRow({
  variant,
  sessionMode,
  modelMode,
  splitModeAllowed,
  onSessionModeChange,
  onModelModeChange,
}: {
  variant: number
  sessionMode: DesktopSessionMode
  modelMode: DraftMode
  splitModeAllowed: boolean
  onSessionModeChange: (value: DesktopSessionMode) => void
  onModelModeChange: (value: DraftMode) => void
}) {
  const sessionChoices = <SessionModeChoices value={sessionMode} onChange={onSessionModeChange} className="min-w-[176px]" />
  const policyChoices = <ModelPolicyChoices value={modelMode} splitModeAllowed={splitModeAllowed} onChange={onModelModeChange} className="min-w-[176px]" />

  if (variant === 2) {
    return (
      <div data-primary-layout="2" className="flex min-w-[640px] items-center gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
        <span className="shrink-0 text-xs font-semibold text-[var(--app-text)]">New sessions</span>
        {sessionChoices}
        <span className="h-7 w-px bg-[var(--app-border)]" />
        <span className="shrink-0 text-xs font-semibold text-[var(--app-text)]">Models</span>
        {policyChoices}
        <span className="ml-auto text-[10px] uppercase tracking-wider text-[var(--app-text-subtle)]">Mode · Policy</span>
      </div>
    )
  }
  if (variant === 3) {
    return (
      <div data-primary-layout="3" className="flex min-w-[640px] items-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
        <div className="flex flex-1 items-center gap-2 border-r border-[var(--app-border)] pr-3">
          <span className="shrink-0 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Start</span>
          {sessionChoices}
        </div>
        <div className="flex flex-1 items-center justify-end gap-2 pl-3">
          <span className="shrink-0 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Model use</span>
          {policyChoices}
        </div>
      </div>
    )
  }
  if (variant === 4) {
    return (
      <div data-primary-layout="4" className="flex min-w-[640px] items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2">
        <span className="shrink-0 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Defaults</span>
        <span className="shrink-0 text-xs text-[var(--app-text-muted)]">Session</span>
        {sessionChoices}
        <span className="mx-1 text-[var(--app-text-subtle)]">/</span>
        <span className="shrink-0 text-xs text-[var(--app-text-muted)]">Model</span>
        {policyChoices}
      </div>
    )
  }
  if (variant === 5) {
    return (
      <div data-primary-layout="5" className="flex min-w-[640px] items-center gap-4 border-y border-[var(--app-border)] py-3">
        <div className="min-w-0 flex-1">
          <div className="text-xs font-semibold text-[var(--app-text)]">Primary agent defaults</div>
          <div className="text-[10px] text-[var(--app-text-subtle)]">Session start and model routing</div>
        </div>
        <span className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Session</span>
        {sessionChoices}
        <span className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Policy</span>
        {policyChoices}
      </div>
    )
  }
  return (
    <div data-primary-layout="1" className="flex min-w-[640px] items-center gap-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Default session mode</div>
        <div className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">How new sessions start</div>
      </div>
      {sessionChoices}
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent model policy</div>
        <div className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">One model or split by mode</div>
      </div>
      {policyChoices}
    </div>
  )
}

function CompactChoice({ selected, label, onClick, disabled = false }: { selected: boolean; label: string; onClick: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      disabled={disabled}
      className={`rounded-lg border px-4 py-2 text-sm font-semibold capitalize transition disabled:cursor-not-allowed disabled:opacity-45 ${selected ? 'border-[var(--app-primary)] bg-transparent text-[var(--app-primary)]' : 'border-transparent bg-transparent text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]'}`}
    >
      {label}
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
