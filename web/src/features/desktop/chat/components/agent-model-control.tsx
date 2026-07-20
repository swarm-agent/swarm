import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, GitBranch, Lightbulb, Lock, Plus, Settings2, Star, Zap, ZapOff } from 'lucide-react'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileInput, ModelProfileRecord } from '../types/chat'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import { defaultModelThinking, displayModelName, effectiveContextWindow, formatContextWindow, formatModelPricing, modelServiceTierOptions, modelThinkingOptions, normalizeModelServiceTier, normalizeModelThinking, supportsModelServiceTier } from '../services/model-options'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { saveSystemAgentSettings } from '../../settings/swarm/mutations/save-system-agent-settings'
import { normalizeCoderAgentSettings, normalizeCompactAgentSettings, normalizeExplorerAgentSettings } from '../../settings/swarm/types/swarm-settings'
import { displayAgentName } from '../services/agent-display'
import { canSwitchModelProfilePolicyGroup, modelProfilePolicyGroupLabel, modelProfilesInPolicyGroup, type ModelProfilePolicyGroup } from '../services/model-profile-groups'

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
  modelProfile: ModelProfileInput
  persistence: 'temporary' | 'create' | 'update' | 'create-copy'
  profileId: string
  makeDefault: boolean
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
  onSetDefaultModelProfile?: (profileId: string) => void | Promise<void>
  modelProfiles?: ModelProfileRecord[]
  activeModelProfile?: ActiveModelProfileState
  initialModelProfileId?: string | null
  createModelProfileSignal?: number
  busy?: boolean
  showTrigger?: boolean
  initialAgentName?: string
  thinkingTagsEnabled?: boolean
  onThinkingTagsToggle?: (enabled: boolean) => void
  thinkingTagsBusy?: boolean
  showCompactButton?: boolean
  onShowCompactButtonToggle?: (enabled: boolean) => void
  showCompactButtonBusy?: boolean
}

type DraftMode = 'single' | 'split'
const COMPACT_AGENT_NAME = 'system-compact'
const EXPLORER_AGENT_NAME = 'system-explorer'
const CODER_AGENT_NAME = 'system-coder'
const SWARM_AGENT_NAME = 'swarm'

function isSystemUtility(name: string): boolean {
  return name === COMPACT_AGENT_NAME || name === EXPLORER_AGENT_NAME || name === CODER_AGENT_NAME
}

function isCompiledSystemAgent(name: string): boolean {
  return isSystemUtility(name) || name === SWARM_AGENT_NAME
}
export type ModelDraft = { provider: string; model: string; thinking: string; serviceTier: string; contextMode: string }

export function resolveInitialModelProfileId(initialProfileId: string | null | undefined, activeProfile: ActiveModelProfileState | undefined, profiles: ModelProfileRecord[]): string {
  if (initialProfileId !== undefined && initialProfileId !== null) return initialProfileId
  if (activeProfile?.source === 'saved') return activeProfile.profileId
  return profiles.find((profile) => profile.isDefault)?.profileId ?? ''
}

export function modelProfileDraftIsCustomized(baseline: string, current: string): boolean {
  return Boolean(baseline && current !== baseline)
}

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

function modelBehaviorLabel(profile: AgentProfileRecord | null): string {
  if (!profile) return 'Single model'
  if (profile.modelMode === 'split' && isPlanCapableAgent(profile)) return 'Split plan/action models'
  return 'Single model'
}

function savedProfileModelLabels(profile: ModelProfileRecord): string[] {
  if (profile.modelMode === 'split') {
    return [
      `Plan · ${savedProfileSelectionLabel(profile.plan)}`,
      `Action · ${savedProfileSelectionLabel(profile.auto)}`,
    ]
  }
  return [`Single · ${savedProfileSelectionLabel(profile.single)}`]
}

function savedProfileSelectionLabel(selection: ModelProfileRecord['single']): string {
  if (!selection) return 'Unavailable selection'
  return [
    [selection.provider.trim(), selection.model.trim()].filter(Boolean).join('/') || 'Default model',
    selection.thinking.trim() ? `thinking ${selection.thinking.trim()}` : '',
    selection.serviceTier.trim(),
  ].filter(Boolean).join(' · ')
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

function modelOptionFor(provider: string, model: string, modelOptions: ModelOptionRecord[], contextMode = ''): ModelOptionRecord | null {
  return modelOptions.find((candidate) => candidate.provider === provider && candidate.model === model && candidate.contextMode === contextMode) ?? modelOptions.find((candidate) => candidate.provider === provider && candidate.model === model) ?? null
}

function normalizeDraftServiceTier(provider: string, value: string): string {
  return normalizeModelServiceTier(provider, value)
}

function modelSupportsServiceTier(provider: string, model: string, modelOptions: ModelOptionRecord[], tier = ''): boolean {
  const option = modelOptionFor(provider, model, modelOptions)
  return supportsModelServiceTier(provider, model, option ?? { serviceTiers: [], serviceTierMappings: [] }, tier)
}

function serviceTierOptionsForDraft(draft: ModelDraft, modelOptions: ModelOptionRecord[]) {
  const option = modelOptionFor(draft.provider, draft.model, modelOptions, draft.contextMode)
  return modelServiceTierOptions(draft.provider, draft.model, option ?? { serviceTiers: [], serviceTierMappings: [] })
}

function serviceTierLabel(provider: string, model: string, modelOptions: ModelOptionRecord[], value: string): string {
  const normalized = normalizeDraftServiceTier(provider, value)
  const options = serviceTierOptionsForDraft({ provider, model, thinking: '', serviceTier: normalized, contextMode: '' }, modelOptions)
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
    contextMode: model?.contextMode ?? '',
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
    contextMode: fallback.contextMode,
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
      contextMode: fallback.contextMode,
    }
  }
  const provider = profile?.autoProvider.trim() || fallback.provider
  return {
    provider,
    model: profile?.autoModel.trim() || fallback.model,
    thinking: profile?.autoThinking.trim() || fallback.thinking,
    serviceTier: normalizeDraftServiceTier(provider, profile?.autoServiceTier ?? ''),
    contextMode: fallback.contextMode,
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
  onSetDefaultModelProfile,
  modelProfiles = [],
  activeModelProfile,
  initialModelProfileId,
  createModelProfileSignal = 0,
  busy = false,
  showTrigger = true,
  initialAgentName = '',
  thinkingTagsEnabled,
  onThinkingTagsToggle,
  thinkingTagsBusy = false,
  showCompactButton = false,
  onShowCompactButtonToggle,
  showCompactButtonBusy = false,
}: AgentModelControlProps) {
  const queryClient = useQueryClient()
  const { data: uiSettings = {} } = useQuery(uiSettingsQueryOptions())
  const compactSettings = normalizeCompactAgentSettings(uiSettings)
  const explorerSettings = normalizeExplorerAgentSettings(uiSettings)
  const coderSettings = normalizeCoderAgentSettings(uiSettings)
  const coderSettingsEnabled = Boolean(coderSettings.provider && coderSettings.model)
  const compactProfile = useMemo<AgentProfileRecord>(() => ({
    name: COMPACT_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled tool-free context compaction and titling utility',
    provider: compactSettings.provider,
    model: compactSettings.model,
    thinking: compactSettings.thinking,
    modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: compactSettings.service_tier,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: {} },
    enabled: true, protected: true, updatedAt: 0,
  }), [compactSettings.model, compactSettings.provider, compactSettings.service_tier, compactSettings.thinking])
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
  const coderProfile = useMemo<AgentProfileRecord>(() => ({
    name: CODER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled isolated implementation subagent',
    provider: coderSettingsEnabled ? coderSettings.provider : '', model: coderSettingsEnabled ? coderSettings.model : '', thinking: coderSettingsEnabled ? coderSettings.thinking : '', modelMode: 'single',
    planProvider: '', planModel: '', planThinking: '', planServiceTier: '',
    autoProvider: '', autoModel: '', autoThinking: '', autoServiceTier: coderSettingsEnabled ? coderSettings.service_tier : '',
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null, toolContract: null,
    enabled: true, protected: true, updatedAt: 0,
  }), [coderSettings.model, coderSettings.provider, coderSettings.service_tier, coderSettings.thinking, coderSettingsEnabled])
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [draftProfileName, setDraftProfileName] = useState('')
  const [draftMakeDefault, setDraftMakeDefault] = useState(false)
  const [defaultingProfileId, setDefaultingProfileId] = useState('')
  const [editingProfileId, setEditingProfileId] = useState('')
  const [baseline, setBaseline] = useState('')
  const initializedOpenRef = useRef(false)
  const selectableAgents = useMemo(() => [...agents.filter((agent) => agent.enabled !== false && agent.name !== 'explorer' && (!isCompiledSystemAgent(agent.name) || agent.name === SWARM_AGENT_NAME)), compactProfile, explorerProfile, coderProfile], [agents, coderProfile, compactProfile, explorerProfile])
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
  const visibleModelProfiles = modelProfilesInPolicyGroup(modelProfiles, effectiveDraftMode)
  const profileGroupSwitchable = canSwitchModelProfilePolicyGroup(draftProfile)
  const agentSections = useMemo(() => {
    const swarmProfile = selectableAgents.find((agent) => agent.name === SWARM_AGENT_NAME)
    const primaryProfiles = selectableAgents.filter((agent) => agentMode(agent) === 'primary' && !isCompiledSystemAgent(agent.name))
    const sections = [
      { label: 'Agents', profiles: [...(swarmProfile ? [swarmProfile] : []), ...primaryProfiles] },
      { label: 'Subagents', profiles: selectableAgents.filter((agent) => agentMode(agent) === 'subagent' && !isCompiledSystemAgent(agent.name)) },
      { label: 'System agents', profiles: selectableAgents.filter((agent) => isCompiledSystemAgent(agent.name) && agent.name !== SWARM_AGENT_NAME) },
      { label: 'Other agents', profiles: selectableAgents.filter((agent) => {
        const profileMode = agentMode(agent)
        return profileMode !== 'primary' && profileMode !== 'subagent' && !isCompiledSystemAgent(agent.name)
      }) },
    ]
    return sections.filter((section) => section.profiles.length > 0)
  }, [selectableAgents])
  const displayedModelProfileId = editingProfileId
    || (activeModelProfile?.source === 'saved' ? activeModelProfile.profileId : '')
    || modelProfiles.find((profile) => profile.isDefault)?.profileId
    || ''
  const selectedModelLabel = selectedModel
    ? `${selectedModel.provider}/${displayModelName(selectedModel.provider, selectedModel.model, selectedModel.contextMode)}`
    : 'No resolved model'
  const normalizedSelectedThinking = selectedThinking.trim() || defaultThinkingForOption(selectedModel)
  const selectedServiceTierSupported = selectedModel ? supportsModelServiceTier(selectedModel.provider, selectedModel.model, selectedModel) : false
  const normalizedSelectedServiceTier = normalizeDraftServiceTier(selectedModel?.provider ?? '', selectedServiceTier)
  const selectedServiceTierLabel = normalizedSelectedServiceTier ? serviceTierLabel(selectedModel?.provider ?? '', selectedModel?.model ?? '', modelOptions, normalizedSelectedServiceTier) : 'standard'
  const SelectedServiceTierIcon = normalizedSelectedServiceTier ? Zap : ZapOff

  useEffect(() => {
    if (openSignal > 0 || createModelProfileSignal > 0) setOpen(true)
  }, [createModelProfileSignal, openSignal])

  useEffect(() => {
    if (!open) {
      initializedOpenRef.current = false
      return
    }
    if (initializedOpenRef.current) return
    initializedOpenRef.current = true
    const profile = selectableAgents.find((agent) => agent.name === initialAgentName)
      ?? selectableAgents.find((agent) => agent.name === selectedPrimaryAgent)
      ?? activeProfile
    const requestedProfileId = resolveInitialModelProfileId(initialModelProfileId, activeModelProfile, modelProfiles)
    const saved = requestedProfileId ? modelProfiles.find((candidate) => candidate.profileId === requestedProfileId) ?? null : null
    const nextMode: DraftMode = saved?.modelMode ?? selectedDraftMode(profile)
    const single = saved?.single ? { ...saved.single } : singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking)
    const plan = saved?.plan ? { ...saved.plan } : splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking)
    const auto = saved?.auto ? { ...saved.auto } : splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking)
    const name = saved?.name ?? (activeModelProfile?.source === 'saved' ? `${activeModelProfile.name} copy` : '')
    const makeDefault = saved ? false : modelProfiles.length === 0
    setDraftAgentName(profile?.name ?? selectedPrimaryAgent)
    setDraftSessionMode(profile?.defaultSessionMode ?? mode)
    setDraftMode(nextMode)
    setSingleDraft(single)
    setPlanDraft(plan)
    setAutoDraft(auto)
    setDraftProfileName(name)
    setDraftMakeDefault(makeDefault)
    setEditingProfileId(saved?.profileId ?? '')
    setBaseline(JSON.stringify({ name, makeDefault, sessionMode: profile?.defaultSessionMode ?? mode, mode: nextMode, single, plan, auto }))
    setError(null)
  }, [activeModelProfile, activeProfile, initialAgentName, initialModelProfileId, mode, modelProfiles, open, selectableAgents, selectedModel, selectedPrimaryAgent, selectedServiceTier, selectedThinking])

  function chooseAgent(profile: AgentProfileRecord) {
    if (customized && !window.confirm('Discard the unsaved profile changes and switch agents?')) return
    const nextMode = selectedDraftMode(profile)
    const single = singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking)
    const plan = splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking)
    const auto = splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking)
    setDraftAgentName(profile.name)
    setDraftSessionMode(profile.defaultSessionMode)
    setDraftMode(nextMode)
    setSingleDraft(single)
    setPlanDraft(plan)
    setAutoDraft(auto)
    setDraftProfileName('')
    setDraftMakeDefault(modelProfiles.length === 0)
    setEditingProfileId('')
    setBaseline(JSON.stringify({ name: '', makeDefault: modelProfiles.length === 0, sessionMode: profile.defaultSessionMode, mode: nextMode, single, plan, auto }))
    setError(null)
  }

  async function makeModelProfileDefault(profile: ModelProfileRecord) {
    if (saving || busy || profile.isDefault || !onSetDefaultModelProfile) return
    setDefaultingProfileId(profile.profileId)
    setError(null)
    try {
      await onSetDefaultModelProfile(profile.profileId)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setDefaultingProfileId('')
    }
  }

  function chooseModelProfile(saved: ModelProfileRecord | null) {
    if (customized && !window.confirm('Discard the unsaved changes and switch profiles?')) return
    const profile = draftProfile ?? activeProfile
    const nextMode: DraftMode = saved?.modelMode ?? selectedDraftMode(profile)
    const single = saved?.single ? { ...saved.single } : singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking)
    const plan = saved?.plan ? { ...saved.plan } : splitDraftFromProfile(profile, 'plan', selectedModel, selectedServiceTier, selectedThinking)
    const auto = saved?.auto ? { ...saved.auto } : splitDraftFromProfile(profile, 'auto', selectedModel, selectedServiceTier, selectedThinking)
    const name = saved?.name ?? ''
    const makeDefault = saved ? false : modelProfiles.length === 0
    const sessionMode = profile?.defaultSessionMode ?? mode
    setDraftSessionMode(sessionMode)
    setDraftMode(nextMode)
    setSingleDraft(single)
    setPlanDraft(plan)
    setAutoDraft(auto)
    setDraftProfileName(name)
    setDraftMakeDefault(makeDefault)
    setEditingProfileId(saved?.profileId ?? '')
    setBaseline(JSON.stringify({ name, makeDefault, sessionMode, mode: nextMode, single, plan, auto }))
    setError(null)
  }

  function selectProvider(target: 'single' | 'plan' | 'auto', provider: string) {
    const update = (current: ModelDraft): ModelDraft => ({ ...current, provider, model: '', thinking: '', serviceTier: '', contextMode: '' })
    if (target === 'single') setSingleDraft(update)
    else if (target === 'plan') setPlanDraft(update)
    else setAutoDraft(update)
  }

  function selectModel(target: 'single' | 'plan' | 'auto', key: string) {
    const update = (current: ModelDraft): ModelDraft => {
      const option = modelOptions.find((candidate) => candidate.provider === current.provider && modelOptionKey(candidate) === key) ?? null
      const model = option?.model ?? ''
      return {
        ...current,
        model,
        contextMode: option?.contextMode ?? '',
        thinking: normalizeDraftThinking(current.provider, model, modelOptions, current.thinking),
        serviceTier: modelSupportsServiceTier(current.provider, model, modelOptions, current.serviceTier) ? current.serviceTier : '',
      }
    }
    if (target === 'single') setSingleDraft(update)
    else if (target === 'plan') setPlanDraft(update)
    else setAutoDraft(update)
  }

  const currentDraftSignature = JSON.stringify({ name: draftProfileName.trim(), makeDefault: draftMakeDefault, sessionMode: draftSessionMode, mode: effectiveDraftMode, single: singleDraft, plan: planDraft, auto: autoDraft })
  const customized = modelProfileDraftIsCustomized(baseline, currentDraftSignature)
  const editingModelProfile = modelProfiles.find((profile) => profile.profileId === editingProfileId) ?? null

  async function confirm(persistence: AgentModelControlConfirmInput['persistence']) {
    const profile = draftProfile
    if (!profile || saving || busy) return
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
          agent: profile.name === COMPACT_AGENT_NAME ? 'compact' : profile.name === CODER_AGENT_NAME ? 'coder' : 'explorer',
          settings: {
            provider: String(action.agentPatch.provider ?? '').trim(),
            model: String(action.agentPatch.model ?? '').trim(),
            thinking: String(action.agentPatch.thinking ?? '').trim(),
            service_tier: String(action.agentPatch.autoServiceTier ?? '').trim(),
          },
        })
        queryClient.setQueryData(uiSettingsQueryOptions().queryKey, saved)
      } else {
        const toSelection = (draft: ModelDraft) => ({ provider: draft.provider.trim(), model: draft.model.trim(), thinking: draft.thinking.trim(), serviceTier: draft.serviceTier.trim(), contextMode: draft.contextMode.trim() })
        const profileName = draftProfileName.trim() || (persistence === 'temporary' ? 'Temporary/customized' : '')
        if (persistence !== 'temporary' && !profileName) {
          throw new Error('Enter a profile name before saving.')
        }
        await onConfirmAgentSettings?.({
          agentName: profile.name,
          profile,
          action,
          modelProfile: normalizedDraftMode === 'single'
            ? { name: profileName, modelMode: 'single', single: toSelection(singleDraft), plan: null, auto: null }
            : { name: profileName, modelMode: 'split', single: null, plan: toSelection(planDraft), auto: toSelection(autoDraft) },
          persistence,
          profileId: editingProfileId,
          makeDefault: draftMakeDefault,
        })
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
            <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Model setup is separate from agent identity. Choose explicitly whether to use it temporarily or save a named profile.</div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {onOpenAgentSettings ? (
              <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="inline-flex items-center gap-1.5 rounded-lg border border-[var(--app-border)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">
                <Settings2 size={12} /> Manage agent
              </button>
            ) : null}
            <button type="button" onClick={() => setOpen(false)} className="rounded-lg border border-[var(--app-border)] px-3 py-1 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">Close</button>
          </div>
        </div>

        <div className="grid min-h-0 flex-1 min-[780px]:grid-cols-[280px_minmax(0,1fr)]">
          <div className="min-h-0 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] min-[780px]:border-b-0 min-[780px]:border-r">
            <div className="border-b border-[var(--app-border)] px-4 py-3">
              <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent</div>
              <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Profiles use the current agent. Switch only when needed.</div>
            </div>
            <div className="max-h-56 space-y-3 overflow-y-auto p-3 min-[780px]:max-h-[660px]">
              {agentSections.map((section) => (
                <section key={section.label}>
                  <div className="mb-1.5 px-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{section.label}</div>
                  <div className="grid gap-1">
                    {section.profiles.map((profile) => {
                      const selected = profile.name === draftAgentName
                      return (
                        <button key={profile.name} type="button" onClick={() => chooseAgent(profile)} aria-pressed={selected} className={`group flex w-full items-center gap-2 rounded-lg border px-2.5 py-2 text-left text-xs transition ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface)] text-[var(--app-text)] shadow-sm' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}>
                          <span className="min-w-0 flex-1 truncate font-semibold">{agentLabel(profile)}</span>
                          <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">{agentModeLabel(profile)} · {modelBehaviorLabel(profile)}</span>
                        </button>
                      )
                    })}
                  </div>
                </section>
              ))}
            </div>
          </div>

          <div className="min-h-0 overflow-y-auto p-5">
            {!draftProfile || !isSystemUtility(draftProfile.name) || visibleModelProfiles.length > 0 ? <section aria-label="Saved model profiles" className="mb-4">
              <div className="mb-2 flex items-end justify-between gap-3">
                <div>
                  <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{modelProfilePolicyGroupLabel(effectiveDraftMode)} profiles</div>
                  <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Profiles are grouped by model policy to keep the current setup clear.</div>
                </div>
                {!draftProfile || !isSystemUtility(draftProfile.name) ? <button type="button" onClick={() => chooseModelProfile(null)} className="inline-flex shrink-0 items-center gap-1.5 rounded-lg border border-[var(--app-border)] px-2.5 py-1.5 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]"><Plus size={12} />New profile</button> : null}
              </div>
              {profileGroupSwitchable ? <SetupProfileGroupSwitch value={effectiveDraftMode} onChange={setDraftMode} /> : null}
              {visibleModelProfiles.length ? (
                <div className="grid gap-2">
                  {visibleModelProfiles.map((profile) => {
                    const selected = displayedModelProfileId === profile.profileId
                    const settingDefault = defaultingProfileId === profile.profileId
                    return <div key={profile.profileId} className={`flex min-w-0 items-center rounded-lg border bg-[var(--app-surface)] transition ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface-subtle)] shadow-sm' : 'border-[var(--app-border)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'}`}>
                      <button type="button" onClick={() => chooseModelProfile(profile)} aria-pressed={selected} className="flex min-w-0 flex-1 items-center gap-3 px-3 py-2.5 text-left">
                        <span className="min-w-0 flex-1 truncate text-sm font-semibold leading-5 text-[var(--app-text)]">{profile.name}</span>
                        <span className="hidden min-w-0 flex-[2] items-center gap-2 text-xs leading-4 text-[var(--app-text-subtle)] sm:flex">
                          {savedProfileModelLabels(profile).map((label) => <span key={label} className="min-w-0 flex-1 truncate">{label}</span>)}
                        </span>
                      </button>
                      {!draftProfile || !isSystemUtility(draftProfile.name) ? <button
                        type="button"
                        disabled={busy || saving || settingDefault || profile.isDefault || !onSetDefaultModelProfile}
                        onClick={() => { void makeModelProfileDefault(profile) }}
                        aria-label={profile.isDefault ? `${profile.name} is the account default` : `Make ${profile.name} the account default`}
                        aria-pressed={profile.isDefault}
                        title={profile.isDefault ? 'Account default' : 'Make account default'}
                        className={`mr-1.5 rounded-md p-1.5 transition disabled:cursor-default ${profile.isDefault ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-subtle)] hover:bg-[var(--app-surface)] hover:text-[var(--app-primary)] disabled:opacity-50'}`}
                      >
                        <Star size={14} fill={profile.isDefault ? 'currentColor' : 'none'} />
                      </button> : null}
                    </div>
                  })}
                </div>
              ) : <button type="button" onClick={() => chooseModelProfile(null)} className="w-full rounded-xl border border-dashed border-[var(--app-border)] px-4 py-4 text-left text-xs text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]">No {modelProfilePolicyGroupLabel(effectiveDraftMode).toLowerCase()} profiles yet. Create one in this group.</button>}
            </section> : null}

            {!draftProfile || !isSystemUtility(draftProfile.name) ? (
              <div className="mb-4 grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <label className="grid gap-1.5 text-xs font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">
                  Profile name
                  <input value={draftProfileName} onChange={(event) => setDraftProfileName(event.target.value)} placeholder="Name this model setup" className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 py-2 text-sm font-normal normal-case tracking-normal text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
                </label>
                {editingModelProfile ? <div className="text-[11px] text-[var(--app-text-muted)]">{editingModelProfile.isDefault ? 'Editing your account default profile. Saving updates it everywhere; continuing for this chat only leaves it unchanged.' : 'Editing a saved profile. Saving updates it everywhere; continuing for this chat only leaves it unchanged.'}</div> : null}
                {customized ? <div className="text-[11px] font-semibold text-[var(--app-warning)]">Unsaved changes — choose whether to update the saved profile or use this draft only in the current chat.</div> : null}
              </div>
            ) : null}
            {draftProfile && isSystemUtility(draftProfile.name) ? (
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                <div className="font-semibold text-[var(--app-text)]">Compiled system agent</div>
                <div className="mt-1">{draftProfile.name === COMPACT_AGENT_NAME ? 'Compact' : draftProfile.name === CODER_AGENT_NAME ? 'Coder' : 'Explorer'} uses its independently configured single-model selection when set, otherwise it inherits the active account model. Its identity, prompt, runtime, and tool contract remain code-owned.</div>
              </div>
            ) : draftProfile && agentMode(draftProfile) === 'primary' ? (
              <PrimaryAgentControlRow
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
            </div>}

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
            {modelLocked && modelLockNotice && draftProfile?.name !== CODER_AGENT_NAME && !isSystemUtility(draftProfile?.name ?? '') ? (
              <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                <span>{modelLockNotice}</span>
              </div>
            ) : null}

            {effectiveDraftMode === 'single' ? (
              <ModelDraftEditor title={draftProfile && isSystemUtility(draftProfile.name) ? `${displayAgentName(draftProfile.name)} model` : 'Single model'} draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('single', provider)} onModelChange={(model) => selectModel('single', model)} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
            ) : (
              <div className="mt-4 grid gap-3">
                <ModelDraftEditor title="Plan model" draft={planDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('plan', provider)} onModelChange={(model) => selectModel('plan', model)} onThinkingChange={(thinking) => setPlanDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setPlanDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
                <ModelDraftEditor title="Action model" draft={autoDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => selectProvider('auto', provider)} onModelChange={(model) => selectModel('auto', model)} onThinkingChange={(thinking) => setAutoDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setAutoDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </div>
            )}
            {(thinkingTagsEnabled !== undefined && onThinkingTagsToggle) || onShowCompactButtonToggle ? (
              <div className="mt-4 grid gap-2 border-t border-[var(--app-border)] pt-4 sm:grid-cols-2">
                {thinkingTagsEnabled !== undefined && onThinkingTagsToggle ? <PreferenceSwitch label="Show thinking responses" checked={thinkingTagsEnabled} busy={thinkingTagsBusy} onToggle={onThinkingTagsToggle} /> : null}
                {onShowCompactButtonToggle ? <PreferenceSwitch label="Show compact button" checked={showCompactButton} busy={showCompactButtonBusy} onToggle={onShowCompactButtonToggle} /> : null}
              </div>
            ) : null}
            {error ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4">
          <button type="button" onClick={() => setOpen(false)} className="rounded-lg border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]">Cancel</button>
          <button type="button" disabled={busy || saving || !draftProfile || isSystemUtility(draftProfile.name)} onClick={() => { void confirm('temporary') }} className="rounded-lg border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-60">Continue for this chat only</button>
          {editingProfileId && (!draftProfile || !isSystemUtility(draftProfile.name)) ? <button type="button" disabled={busy || saving || !customized} onClick={() => { void confirm('create-copy') }} className="rounded-lg border border-[var(--app-border)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-60">Save as new</button> : null}
          <button type="button" disabled={busy || saving || !draftProfile || (isSystemUtility(draftProfile.name) ? !singleDraft.provider || !singleDraft.model || !singleDraft.thinking : !draftProfileName.trim() || Boolean(editingProfileId && !customized))} onClick={() => { void confirm(editingProfileId ? 'update' : 'create') }} className="rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary)] px-3 py-1.5 text-[11px] font-semibold text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] disabled:opacity-60">
            {saving || busy ? 'Saving…' : draftProfile && isSystemUtility(draftProfile.name) ? `Save ${displayAgentName(draftProfile.name)} model` : editingProfileId ? customized ? 'Save and apply' : 'Saved profile in use' : 'Create profile and apply'}
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

function SetupProfileGroupSwitch({ value, onChange }: { value: ModelProfilePolicyGroup; onChange: (value: ModelProfilePolicyGroup) => void }) {
  return (
    <div role="group" aria-label="Profile policy type" className="mb-2 grid max-w-sm grid-cols-2 gap-1 rounded-lg border border-[var(--app-border)] p-1">
      {(['split', 'single'] as const).map((group) => <CompactChoice key={group} selected={value === group} label={modelProfilePolicyGroupLabel(group)} onClick={() => onChange(group)} />)}
    </div>
  )
}

function PreferenceSwitch({ label, checked, busy, onToggle }: { label: string; checked: boolean; busy: boolean; onToggle: (enabled: boolean) => void }) {
  return (
    <div className="flex min-h-14 items-center justify-between gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
      <span className="min-w-0 font-medium">{label}</span>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        aria-label={label}
        disabled={busy}
        onClick={() => onToggle(!checked)}
        className={`relative h-6 w-11 shrink-0 rounded-full border transition disabled:cursor-not-allowed disabled:opacity-60 ${checked ? 'border-[var(--app-primary)] bg-[var(--app-primary)]' : 'border-[var(--app-border-strong)] bg-[var(--app-surface)]'}`}
      >
        <span className={`absolute left-1 top-1 h-4 w-4 rounded-full shadow-sm transition-transform ${checked ? 'translate-x-5 bg-[var(--app-primary-text)]' : 'translate-x-0 bg-[var(--app-text-muted)]'}`} />
      </button>
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
  sessionMode,
  modelMode,
  splitModeAllowed,
  onSessionModeChange,
  onModelModeChange,
}: {
  sessionMode: DesktopSessionMode
  modelMode: DraftMode
  splitModeAllowed: boolean
  onSessionModeChange: (value: DesktopSessionMode) => void
  onModelModeChange: (value: DraftMode) => void
}) {
  return (
    <div className="flex min-w-[640px] items-center gap-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3">
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Default session mode</div>
        <div className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">How new sessions start</div>
      </div>
      <SessionModeChoices value={sessionMode} onChange={onSessionModeChange} className="min-w-[176px]" />
      <div aria-hidden="true" className="h-8 w-px shrink-0 bg-[var(--app-border)]" />
      <div className="min-w-0 flex-1">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent model policy</div>
        <div className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">One model or split by mode</div>
      </div>
      <ModelPolicyChoices value={modelMode} splitModeAllowed={splitModeAllowed} onChange={onModelModeChange} className="min-w-[176px]" />
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
  const selectedOption = choices.find((option) => option.model === draft.model && option.contextMode === draft.contextMode) ?? null
  const serviceTierOptions = serviceTierOptionsForDraft(draft, modelOptions)
  const serviceTierSupported = serviceTierOptions.length > 1
  const normalizedServiceTier = serviceTierSupported ? normalizeDraftServiceTier(draft.provider, draft.serviceTier) : ''
  const thinkingOptions = thinkingOptionsForOption(selectedOption)
  const normalizedThinking = thinkingOptions.includes(normalizeThinking(draft.thinking)) ? normalizeThinking(draft.thinking) : defaultThinkingForOption(selectedOption)
  return (
    <div className="mt-4 rounded-xl border border-[var(--app-border)] p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"><GitBranch size={14} />{title}</div>
      <div className="grid gap-3 md:grid-cols-[minmax(130px,0.7fr)_minmax(220px,1.4fr)_minmax(130px,0.7fr)_minmax(130px,0.7fr)]">
        <SelectField label="Provider" value={draft.provider} onChange={onProviderChange} options={providers.map((provider) => ({ label: provider, value: provider }))} placeholder="Choose provider" />
        <ModelSelectField label="Model" value={selectedOption ? modelOptionKey(selectedOption) : ''} onChange={onModelChange} options={choices} placeholder="Choose model" disabled={!draft.provider.trim()} />
        <SelectField label="Thinking" value={normalizedThinking} onChange={onThinkingChange} options={thinkingOptions.map((option) => ({ label: option, value: option }))} disabled={!selectedOption || thinkingOptions.length <= 1} />
        {showServiceTier ? <SelectField label="Service tier" value={normalizedServiceTier} onChange={(value) => onServiceTierChange?.(normalizeDraftServiceTier(draft.provider, value))} options={serviceTierOptions} disabled={!serviceTierSupported} /> : <div />}
      </div>
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
            return <option key={`model:${modelOptionKey(option)}`} value={modelOptionKey(option)}>{labelText}</option>
          })}
        </select>
        <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
      </span>
    </label>
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
