import { useEffect, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { createPortal } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, GitBranch, Lightbulb, Lock, Settings2, Zap, ZapOff } from 'lucide-react'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileInput, ModelProfileRecord } from '../types/chat'
import { defaultModelThinking, displayModelName, effectiveContextWindow, formatContextWindow, formatModelPricing, modelOptionRouteLabel, modelOptionUpstreamFamily, modelServiceTierOptions, modelThinkingOptions, normalizeModelServiceTier, normalizeModelThinking, supportsModelServiceTier } from '../services/model-options'
import { displayAgentName } from '../services/agent-display'
import { agentModelSettingsQueryOptions, agentModelSettingsQueryKey } from '../../settings/swarm/queries/get-agent-model-settings'
import { saveSwarmAgentModelSettings, saveSystemAgentModelSettings } from '../../settings/swarm/mutations/save-agent-model-settings'
import type { AgentModelAssignment, AgentModelSettings, SystemAgentModelName } from '../../settings/swarm/types/agent-model-settings'

export type AgentModelControlProfilePatch = Partial<Pick<AgentProfileRecord,
  | 'provider'
  | 'model'
  | 'thinking'
>>

export type AgentModelControlConfirmInput = {
  agentName: string
  modelProfile: ModelProfileInput
  persistence: 'temporary' | 'create' | 'update' | 'create-copy'
  profileId: string
  makeDefault: boolean
}

interface AgentModelControlProps {
  currentAgent: string
  selectedPrimaryAgent: string
  agents: AgentProfileRecord[]
  /** Deprecated compatibility prop; model favorites no longer mutate session mode. */
  mode?: unknown
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
  modelProfiles?: ModelProfileRecord[]
  activeModelProfile?: ActiveModelProfileState
  createModelProfileSignal?: number
  busy?: boolean
  showTrigger?: boolean
  initialAgentName?: string
}

const COMPACT_AGENT_NAME = 'system-compact'
const FINDER_AGENT_NAME = 'system-finder'
const CODER_AGENT_NAME = 'system-coder'
const DESIGNER_AGENT_NAME = 'system-designer'
const ROUTER_AGENT_NAME = 'system-router'
const SWARM_AGENT_NAME = 'swarm'

function isSystemUtility(name: string): boolean {
  return name === COMPACT_AGENT_NAME || name === FINDER_AGENT_NAME || name === CODER_AGENT_NAME || name === DESIGNER_AGENT_NAME || name === ROUTER_AGENT_NAME
}

function isCompiledSystemAgent(name: string): boolean {
  return isSystemUtility(name) || name === SWARM_AGENT_NAME
}
export type ModelDraft = { provider: string; upstreamFamily?: string; model: string; thinking: string; serviceTier: string; contextMode: string }

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

function modelBehaviorLabel(_profile: AgentProfileRecord | null): string {
  return 'Single model'
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
    upstreamFamily: model ? modelOptionUpstreamFamily(model) : '',
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
    upstreamFamily: provider === fallback.provider ? fallback.upstreamFamily : '',
    model: hasExplicitSingleModel ? profile?.model.trim() || fallback.model : fallback.model,
    thinking: hasExplicitSingleModel ? profile?.thinking.trim() || fallback.thinking : fallback.thinking,
    serviceTier: fallback.serviceTier,
    contextMode: fallback.contextMode,
  }
}

export interface AgentModelProviderChoice {
  key: string
  label: string
  provider: string
  upstreamFamily: string
}

export function agentModelProviderChoices(modelOptions: ModelOptionRecord[]): AgentModelProviderChoice[] {
  const choices = new Map<string, AgentModelProviderChoice>()
  for (const option of modelOptions) {
    const provider = option.provider.trim()
    if (!provider) continue
    const upstreamFamily = modelOptionUpstreamFamily(option)
    const key = upstreamFamily ? `${provider}::upstream::${upstreamFamily}` : `${provider}::direct`
    choices.set(key, { key, label: modelOptionRouteLabel(option), provider, upstreamFamily })
  }
  return Array.from(choices.values()).sort((left, right) => left.label.localeCompare(right.label))
}

function providerChoiceKey(provider: string, upstreamFamily: string | undefined, model: string, modelOptions: ModelOptionRecord[]): string {
  const option = modelOptions.find((candidate) => candidate.provider === provider && candidate.model === model)
  const resolvedUpstreamFamily = upstreamFamily?.trim().toLowerCase() || (option ? modelOptionUpstreamFamily(option) : '')
  return resolvedUpstreamFamily ? `${provider}::upstream::${resolvedUpstreamFamily}` : `${provider}::direct`
}

function modelChoices(providerChoiceKey: string, modelOptions: ModelOptionRecord[]): ModelOptionRecord[] {
  const choice = agentModelProviderChoices(modelOptions).find((candidate) => candidate.key === providerChoiceKey)
  if (!choice) return []
  return modelOptions.filter((option) => option.provider === choice.provider && modelOptionUpstreamFamily(option) === choice.upstreamFamily)
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

function buildPatch(single: ModelDraft, modelOptions: ModelOptionRecord[]): AgentModelControlProfilePatch {
  return {
    provider: single.provider.trim(),
    model: single.model.trim(),
    thinking: normalizeDraftThinking(single.provider, single.model, modelOptions, single.thinking),
  }
}

function modelProfileInput(name: string, draft: ModelDraft, modelOptions: ModelOptionRecord[]): ModelProfileInput {
  const patch = buildPatch(draft, modelOptions)
  return {
    name,
    provider: patch.provider?.trim() ?? '',
    model: patch.model?.trim() ?? '',
    thinking: patch.thinking?.trim() ?? '',
    serviceTier: draft.serviceTier.trim(),
    contextMode: draft.contextMode.trim(),
  }
}

export function AgentModelControl({
  currentAgent,
  selectedPrimaryAgent,
  agents,
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
  modelProfiles = [],
  activeModelProfile,
  createModelProfileSignal = 0,
  busy = false,
  showTrigger = true,
  initialAgentName = '',
}: AgentModelControlProps) {
  const queryClient = useQueryClient()
  const agentModelSettingsQuery = useQuery(agentModelSettingsQueryOptions())
  const emptyAssignment: AgentModelAssignment = { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '' }
  const compactSettings = agentModelSettingsQuery.data?.systemAgents.compact ?? emptyAssignment
  const finderSettings = agentModelSettingsQuery.data?.systemAgents.finder ?? emptyAssignment
  const coderSettings = agentModelSettingsQuery.data?.systemAgents.coder ?? emptyAssignment
  const designerSettings = agentModelSettingsQuery.data?.systemAgents.designer ?? emptyAssignment
  const routerSettings = agentModelSettingsQuery.data?.systemAgents.router ?? emptyAssignment
  const coderSettingsEnabled = Boolean(coderSettings.provider && coderSettings.model)
  const compactProfile = useMemo<AgentProfileRecord>(() => ({
    name: COMPACT_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled tool-free context compaction and titling utility',
    provider: compactSettings.provider,
    model: compactSettings.model,
    thinking: compactSettings.thinking,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: {} },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [compactSettings.model, compactSettings.provider, compactSettings.serviceTier, compactSettings.thinking])
  const finderProfile = useMemo(() => ({
    name: FINDER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled repository and web research subagent',
    provider: finderSettings.provider,
    model: finderSettings.model,
    thinking: finderSettings.thinking,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: { read: { enabled: true, bashPrefixes: [] }, search: { enabled: true, bashPrefixes: [] }, list: { enabled: true, bashPrefixes: [] }, websearch: { enabled: true, bashPrefixes: [] }, webfetch: { enabled: true, bashPrefixes: [] } } },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [finderSettings.model, finderSettings.provider, finderSettings.serviceTier, finderSettings.thinking])
  const coderProfile = useMemo(() => ({
    name: CODER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled isolated implementation subagent',
    provider: coderSettingsEnabled ? coderSettings.provider : '', model: coderSettingsEnabled ? coderSettings.model : '', thinking: coderSettingsEnabled ? coderSettings.thinking : '',
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null, toolContract: null,
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [coderSettings.model, coderSettings.provider, coderSettings.serviceTier, coderSettings.thinking, coderSettingsEnabled])
  const routerProfile = useMemo(() => ({
    name: ROUTER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled Router model selection',
    provider: routerSettings.provider, model: routerSettings.model, thinking: routerSettings.thinking,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: {} },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [routerSettings.model, routerSettings.provider, routerSettings.serviceTier, routerSettings.thinking])
  const designerProfile = useMemo(() => ({
    name: DESIGNER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled same-checkout UI iteration subagent with reusable workspace outputs',
    provider: designerSettings.provider, model: designerSettings.model, thinking: designerSettings.thinking,
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: { read: { enabled: true, bashPrefixes: [] }, search: { enabled: true, bashPrefixes: [] }, find: { enabled: true, bashPrefixes: [] }, list: { enabled: true, bashPrefixes: [] }, write: { enabled: true, bashPrefixes: [] }, edit: { enabled: true, bashPrefixes: [] } } },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [designerSettings.model, designerSettings.provider, designerSettings.serviceTier, designerSettings.thinking])
  function modelDraftForProfile(profile: AgentProfileRecord | null): ModelDraft {
    const draft = singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking)
    if (!profile || !isSystemUtility(profile.name)) return draft
    const serviceTier = profile.name === COMPACT_AGENT_NAME
      ? compactSettings.serviceTier
      : profile.name === CODER_AGENT_NAME
        ? coderSettings.serviceTier
        : profile.name === DESIGNER_AGENT_NAME
          ? designerSettings.serviceTier
          : profile.name === ROUTER_AGENT_NAME
            ? routerSettings.serviceTier
            : finderSettings.serviceTier
    return { ...draft, serviceTier: normalizeDraftServiceTier(draft.provider, serviceTier) }
  }
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const initializedOpenRef = useRef(false)
  const selectableAgents = useMemo(() => [...agents.filter((agent) => agent.enabled !== false && agent.name !== 'finder' && !isCompiledSystemAgent(agent.name)), compactProfile, finderProfile, coderProfile, designerProfile, routerProfile], [agents, coderProfile, compactProfile, designerProfile, finderProfile, routerProfile])
  const activeProfile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? selectableAgents.find((agent) => agent.name === currentAgent) ?? null
  const [draftAgentName, setDraftAgentName] = useState(SWARM_AGENT_NAME)
  const draftProfile = draftAgentName === SWARM_AGENT_NAME ? null : selectableAgents.find((agent) => agent.name === draftAgentName) ?? activeProfile
  const [singleDraft, setSingleDraft] = useState<ModelDraft>(() => modelDraftForProfile(activeProfile))
  const [actionDraft, setActionDraft] = useState<ModelDraft>(() => modelDraftForProfile(activeProfile))
  const [planDraft, setPlanDraft] = useState<ModelDraft>(() => modelDraftForProfile(activeProfile))
  const [editingProfileId, setEditingProfileId] = useState('')
  const providers = useMemo(() => agentModelProviderChoices(modelOptions), [modelOptions])
  const agentSections = useMemo(() => {
    const item = (profile: AgentProfileRecord) => ({ name: profile.name, profile })
    const primaryProfiles = selectableAgents.filter((agent) => agentMode(agent) === 'primary' && !isCompiledSystemAgent(agent.name))
    const sections = [
      { label: 'Agents', items: [{ name: SWARM_AGENT_NAME, profile: null }, ...primaryProfiles.map(item)] },
      { label: 'Subagents', items: selectableAgents.filter((agent) => agentMode(agent) === 'subagent' && !isCompiledSystemAgent(agent.name)).map(item) },
      { label: 'System agents', items: selectableAgents.filter((agent) => isCompiledSystemAgent(agent.name)).map(item) },
      { label: 'Other agents', items: selectableAgents.filter((agent) => {
        const profileMode = agentMode(agent)
        return profileMode !== 'primary' && profileMode !== 'subagent' && !isCompiledSystemAgent(agent.name)
      }).map(item) },
    ]
    return sections.filter((section) => section.items.length > 0)
  }, [selectableAgents])
  const selectedModelLabel = selectedModel
    ? `${selectedModel.provider}/${displayModelName(selectedModel.provider, selectedModel.model, selectedModel.contextMode)}`
    : 'No resolved model'
  const normalizedSelectedThinking = selectedThinking.trim() || defaultThinkingForOption(selectedModel)
  const selectedServiceTierSupported = selectedModel ? supportsModelServiceTier(selectedModel.provider, selectedModel.model, selectedModel) : false
  const normalizedSelectedServiceTier = normalizeDraftServiceTier(selectedModel?.provider ?? '', selectedServiceTier)
  const selectedServiceTierLabel = normalizedSelectedServiceTier ? serviceTierLabel(selectedModel?.provider ?? '', selectedModel?.model ?? '', modelOptions, normalizedSelectedServiceTier) : 'standard'
  const SelectedServiceTierIcon = normalizedSelectedServiceTier ? Zap : ZapOff

  function initializeDrafts(profile: AgentProfileRecord | null) {
    const fallback = modelDraftForProfile(profile)
    const settings = agentModelSettingsQuery.data?.swarm
    const action = settings?.action ?? fallback
    const plan = settings?.plan ?? action
    setSingleDraft(fallback)
    setActionDraft(action)
    setPlanDraft(plan)
    setEditingProfileId(activeModelProfile?.source === 'saved' ? activeModelProfile.profileId : '')
  }

  useEffect(() => {
    if (openSignal > 0 || createModelProfileSignal > 0) setOpen(true)
  }, [createModelProfileSignal, openSignal])

  useEffect(() => {
    if (!open) {
      initializedOpenRef.current = false
      return
    }
    if (initializedOpenRef.current) return
    const requestedAgentName = initialAgentName.trim()
    const requestedProfile = selectableAgents.find((agent) => agent.name === requestedAgentName) ?? null
    const agentName = requestedAgentName === SWARM_AGENT_NAME || requestedProfile ? requestedAgentName : SWARM_AGENT_NAME
    const profile = agentName === SWARM_AGENT_NAME ? null : requestedProfile
    if (isCompiledSystemAgent(agentName) && agentModelSettingsQuery.isPending) return
    initializedOpenRef.current = true
    setDraftAgentName(agentName)
    initializeDrafts(profile)
    setError(null)
  }, [activeModelProfile, agentModelSettingsQuery.data, agentModelSettingsQuery.isPending, initialAgentName, modelProfiles, open, selectableAgents])

  function chooseAgent(name: string, profile: AgentProfileRecord | null) {
    setDraftAgentName(name)
    initializeDrafts(profile)
    setError(null)
  }

  function updateProvider(setDraft: Dispatch<SetStateAction<ModelDraft>>, providerChoice: string) {
    const choice = providers.find((candidate) => candidate.key === providerChoice)
    setDraft((current) => ({ ...current, provider: choice?.provider ?? '', upstreamFamily: choice?.upstreamFamily ?? '', model: '', thinking: '', serviceTier: '', contextMode: '' }))
  }

  function updateModel(setDraft: Dispatch<SetStateAction<ModelDraft>>, key: string) {
    setDraft((current) => {
      const option = modelOptions.find((candidate) => candidate.provider === current.provider && modelOptionUpstreamFamily(candidate) === (current.upstreamFamily ?? '') && modelOptionKey(candidate) === key) ?? null
      const model = option?.model ?? ''
      return {
        ...current,
        model,
        upstreamFamily: option ? modelOptionUpstreamFamily(option) : current.upstreamFamily,
        contextMode: option?.contextMode ?? '',
        thinking: normalizeDraftThinking(current.provider, model, modelOptions, current.thinking),
        serviceTier: modelSupportsServiceTier(current.provider, model, modelOptions, current.serviceTier) ? current.serviceTier : '',
      }
    })
  }

  function validateDraft(label: string, draft: ModelDraft) {
    const value = modelProfileInput(label, draft, modelOptions)
    if (!value.provider || !value.model || !value.thinking) throw new Error(`Choose provider, model, and thinking for ${label}.`)
    return value
  }

  async function saveSwarmModels() {
    const action = validateDraft('Swarm Action', actionDraft)
    const plan = validateDraft('Swarm Plan', planDraft)
    const saved = await saveSwarmAgentModelSettings({
      action: { provider: action.provider, model: action.model, thinking: action.thinking, serviceTier: action.serviceTier, contextMode: action.contextMode },
      plan: { provider: plan.provider, model: plan.model, thinking: plan.thinking, serviceTier: plan.serviceTier, contextMode: plan.contextMode },
    })
    queryClient.setQueryData<AgentModelSettings>(agentModelSettingsQueryKey, saved)
  }

  async function confirm() {
    const profile = draftProfile
    if ((draftAgentName !== SWARM_AGENT_NAME && !profile) || saving || busy) return
    setSaving(true)
    setError(null)
    try {
      if (draftAgentName === SWARM_AGENT_NAME) {
        await saveSwarmModels()
      } else if (profile && isSystemUtility(profile.name)) {
        const agentPatch = validateDraft(`${displayAgentName(profile.name)} model`, singleDraft)
        const agent: SystemAgentModelName = profile.name === COMPACT_AGENT_NAME ? 'compact' : profile.name === CODER_AGENT_NAME ? 'coder' : profile.name === DESIGNER_AGENT_NAME ? 'designer' : profile.name === ROUTER_AGENT_NAME ? 'router' : 'finder'
        const saved = await saveSystemAgentModelSettings({
          agent,
          assignment: {
            provider: agentPatch.provider,
            model: agentPatch.model,
            thinking: agentPatch.thinking,
            serviceTier: agentPatch.serviceTier,
            contextMode: agentPatch.contextMode,
          },
        })
        queryClient.setQueryData<AgentModelSettings>(agentModelSettingsQueryKey, saved)
      } else if (profile) {
        const input = validateDraft(`${displayAgentName(profile.name)} model`, singleDraft)
        await onConfirmAgentSettings?.({
          agentName: profile.name,
          modelProfile: input,
          persistence: editingProfileId ? 'update' : 'create',
          profileId: editingProfileId,
          makeDefault: false,
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
    <div className="fixed inset-0 z-[9999] flex items-stretch justify-center overflow-hidden bg-black/50 pt-[var(--app-safe-area-top)] pr-[var(--app-safe-area-right)] pb-[var(--app-safe-area-bottom)] pl-[var(--app-safe-area-left)] sm:items-center sm:pt-[calc(var(--app-safe-area-top)+0.75rem)] sm:pr-[calc(var(--app-safe-area-right)+0.75rem)] sm:pb-[calc(var(--app-safe-area-bottom)+0.75rem)] sm:pl-[calc(var(--app-safe-area-left)+0.75rem)]" role="dialog" aria-modal="true" aria-label="Agent and model settings">
      <div className="flex h-full max-h-full w-full max-w-6xl flex-col overflow-hidden bg-[var(--app-surface)] shadow-xl sm:h-auto sm:max-h-[min(94dvh,880px)] sm:rounded-xl sm:border sm:border-[var(--app-border)]">
        <div className="flex flex-col gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 sm:flex-row sm:items-start sm:justify-between sm:px-5 sm:py-4">
          <div className="min-w-0">
            <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent setup</div>
            <div className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]">{displayAgentName(draftAgentName) || 'Agent'}</div>
            <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Configure each agent directly. System-agent models are not saved profiles.</div>
          </div>
          <div className="grid w-full shrink-0 grid-cols-2 gap-2 sm:flex sm:w-auto sm:items-center">
            {onOpenAgentSettings && draftProfile && !isCompiledSystemAgent(draftProfile.name) ? (
              <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="inline-flex min-h-10 items-center justify-center gap-1.5 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1">
                <Settings2 size={12} /> Manage agent
              </button>
            ) : null}
            <button type="button" onClick={() => setOpen(false)} className="min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1">Close</button>
          </div>
        </div>

        <div aria-label="Agent setup sections" className="min-h-0 flex-1 overflow-y-auto min-[900px]:grid min-[900px]:grid-cols-[240px_minmax(0,1fr)] min-[900px]:overflow-hidden">
          <aside aria-label="Agents" className="flex min-h-0 flex-col border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] min-[900px]:border-b-0 min-[900px]:border-r">
            <div className="border-b border-[var(--app-border)] px-4 py-3">
              <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent</div>
              <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Select the agent to configure.</div>
            </div>
            <div className="max-h-44 space-y-3 overflow-y-auto p-3 min-[480px]:max-h-56 min-[900px]:max-h-none min-[900px]:flex-1">
              {agentSections.map((section) => (
                <section key={section.label}>
                  <div className="mb-1.5 px-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">{section.label}</div>
                  <div className="grid gap-1 min-[480px]:grid-cols-2 min-[900px]:grid-cols-1">
                    {section.items.map(({ name, profile }) => {
                      const selected = name === draftAgentName
                      const assignment = name === SWARM_AGENT_NAME
                        ? agentModelSettingsQuery.data?.swarm.action ?? null
                        : name === COMPACT_AGENT_NAME
                          ? compactSettings
                          : name === FINDER_AGENT_NAME
                            ? finderSettings
                            : name === CODER_AGENT_NAME
                              ? coderSettings
                              : name === DESIGNER_AGENT_NAME
                                ? designerSettings
                                : name === ROUTER_AGENT_NAME
                                  ? routerSettings
                                  : profile
                      const model = assignment?.model.trim() ?? ''
                      const thinking = normalizeThinking(assignment?.thinking ?? '')
                      const configuredServiceTier = name === SWARM_AGENT_NAME
                        ? agentModelSettingsQuery.data?.swarm.action.serviceTier ?? ''
                        : name === COMPACT_AGENT_NAME
                          ? compactSettings.serviceTier
                          : name === FINDER_AGENT_NAME
                            ? finderSettings.serviceTier
                            : name === CODER_AGENT_NAME
                              ? coderSettings.serviceTier
                              : name === DESIGNER_AGENT_NAME
                                ? designerSettings.serviceTier
                                : name === ROUTER_AGENT_NAME
                                  ? routerSettings.serviceTier
                                  : ''
                      const serviceTier = normalizeDraftServiceTier(assignment?.provider ?? '', configuredServiceTier)
                      const enabledDetails = [thinking !== 'off' ? thinking : '', serviceTier].filter(Boolean)
                      return (
                        <button
                          key={name}
                          type="button"
                          onClick={() => chooseAgent(name, profile)}
                          aria-pressed={selected}
                          className={`group flex w-full flex-col gap-1.5 rounded-lg border px-2.5 py-2.5 text-left text-xs transition ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface)] text-[var(--app-text)] shadow-sm' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}
                        >
                          <span className="w-full truncate font-semibold">{displayAgentName(name)}</span>
                          <span className="w-full truncate font-mono text-[10px] text-[var(--app-text-subtle)]" title={model || 'Model not configured'}>
                            {model || 'Model not configured'}
                          </span>
                          {enabledDetails.length > 0 ? (
                            <span className="w-full truncate text-[10px] text-[var(--app-text-muted)]">{enabledDetails.join(' · ')}</span>
                          ) : null}
                        </button>
                      )
                    })}
                  </div>
                </section>
              ))}
            </div>
          </aside>

          <section aria-label="Agent model settings" className="min-h-0 p-4 min-[900px]:overflow-y-auto min-[900px]:p-5">
            {draftAgentName === SWARM_AGENT_NAME ? (
              <div className="grid gap-4">
                <ModelDraftEditor title="Default Model" draft={actionDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => updateProvider(setActionDraft, provider)} onModelChange={(model) => updateModel(setActionDraft, model)} onThinkingChange={(thinking) => setActionDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setActionDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
                <ModelDraftEditor title="Plan Model" draft={planDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => updateProvider(setPlanDraft, provider)} onModelChange={(model) => updateModel(setPlanDraft, model)} onThinkingChange={(thinking) => setPlanDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setPlanDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </div>
            ) : (
              <>
                {draftProfile && isSystemUtility(draftProfile.name) ? (
                  <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                    <div className="font-semibold text-[var(--app-text)]">Compiled system agent</div>
                    <div className="mt-1">Configure this system agent’s model directly. Its identity, prompt, runtime, and tool contract remain code-owned.</div>
                  </div>
                ) : null}
                {modelLocked && modelLockNotice && draftProfile?.name !== CODER_AGENT_NAME && !isSystemUtility(draftProfile?.name ?? '') ? (
                  <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                    <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                    <span>{modelLockNotice}</span>
                  </div>
                ) : null}
                <ModelDraftEditor className="mt-4" title={`${draftProfile ? displayAgentName(draftProfile.name) : 'Agent'} model`} draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={(provider) => updateProvider(setSingleDraft, provider)} onModelChange={(model) => updateModel(setSingleDraft, model)} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
              </>
            )}
            {error ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          </section>
        </div>

        <div className="flex shrink-0 items-center justify-end gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3 sm:px-5 sm:py-4">
          <button type="button" onClick={() => setOpen(false)} className="min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1.5">Cancel</button>
          <button type="button" disabled={busy || saving || (draftAgentName !== SWARM_AGENT_NAME && !draftProfile)} onClick={() => { void confirm() }} className="min-h-10 rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary)] px-4 py-2 text-[11px] font-semibold text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] disabled:opacity-60 sm:min-h-0 sm:py-1.5">
            {saving || busy ? 'Saving…' : `Save ${draftAgentName === SWARM_AGENT_NAME ? 'Swarm models' : `${draftProfile ? displayAgentName(draftProfile.name) : 'agent'} model`}`}
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

function ModelDraftEditor({
  title,
  className = '',
  compact = false,
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
  className?: string
  compact?: boolean
  draft: ModelDraft
  providers: AgentModelProviderChoice[]
  modelOptions: ModelOptionRecord[]
  showServiceTier?: boolean
  onProviderChange: (provider: string) => void
  onModelChange: (model: string) => void
  onThinkingChange: (thinking: string) => void
  onServiceTierChange?: (serviceTier: string) => void
}) {
  const selectedProviderChoice = providerChoiceKey(draft.provider, draft.upstreamFamily, draft.model, modelOptions)
  const choices = modelChoices(selectedProviderChoice, modelOptions)
  const selectedOption = choices.find((option) => option.model === draft.model && option.contextMode === draft.contextMode) ?? null
  const serviceTierOptions = serviceTierOptionsForDraft(draft, modelOptions)
  const serviceTierSupported = serviceTierOptions.length > 1
  const normalizedServiceTier = serviceTierSupported ? normalizeDraftServiceTier(draft.provider, draft.serviceTier) : ''
  const thinkingOptions = thinkingOptionsForOption(selectedOption)
  const normalizedThinking = thinkingOptions.includes(normalizeThinking(draft.thinking)) ? normalizeThinking(draft.thinking) : defaultThinkingForOption(selectedOption)
  return (
    <div className={`rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 sm:p-4 ${className}`}>
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"><GitBranch size={14} />{title}</div>
      <div className={`grid gap-3 sm:grid-cols-2 ${compact ? '' : 'min-[1100px]:grid-cols-[minmax(130px,0.7fr)_minmax(220px,1.4fr)_minmax(130px,0.7fr)_minmax(130px,0.7fr)]'}`}>
        <SelectField label="Provider" value={selectedProviderChoice} onChange={onProviderChange} options={providers.map((provider) => ({ label: provider.label, value: provider.key }))} placeholder="Choose provider route" />
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
            const route = option.provider === 'openrouter' ? `${modelOptionRouteLabel(option)} · ` : ''
            const labelText = `${route}${displayModelName(option.provider, option.model, option.contextMode)}${meta ? ` — ${meta}` : ''}`
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
