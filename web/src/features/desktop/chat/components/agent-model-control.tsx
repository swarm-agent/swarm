import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronUp, GitBranch, GripVertical, Lightbulb, Lock, Plus, Settings2, Star, Trash2, Zap, ZapOff } from 'lucide-react'
import type { ActiveModelProfileState, AgentProfileRecord, ModelOptionRecord, ModelProfileInput, ModelProfileRecord } from '../types/chat'
import { defaultModelThinking, displayModelName, effectiveContextWindow, formatContextWindow, formatModelPricing, modelServiceTierOptions, modelThinkingOptions, normalizeModelServiceTier, normalizeModelThinking, supportsModelServiceTier } from '../services/model-options'
import { uiSettingsQueryOptions } from '../../../queries/query-options'
import { saveSystemAgentSettings } from '../../settings/swarm/mutations/save-system-agent-settings'
import { normalizeCoderAgentSettings, normalizeCompactAgentSettings, normalizeDesignerAgentSettings, normalizeFinderAgentSettings, normalizeRouterAgentSettings } from '../../settings/swarm/types/swarm-settings'
import { displayAgentName } from '../services/agent-display'

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
  onSetDefaultModelProfile?: (profileId: string) => void | Promise<void>
  onDeleteModelProfile?: (profileId: string) => void | Promise<void>
  onReorderModelProfiles?: (profileIds: string[]) => void | Promise<void>
  modelProfiles?: ModelProfileRecord[]
  activeModelProfile?: ActiveModelProfileState
  initialModelProfileId?: string | null
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

function modelBehaviorLabel(_profile: AgentProfileRecord | null): string {
  return 'Single model'
}

function savedFavoriteModelLabel(profile: ModelProfileRecord): string {
  return [profile.provider.trim(), profile.model.trim()].filter(Boolean).join('/') || 'Unavailable model'
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
    serviceTier: fallback.serviceTier,
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

function buildPatch(single: ModelDraft, modelOptions: ModelOptionRecord[]): AgentModelControlProfilePatch {
  return {
    provider: single.provider.trim(),
    model: single.model.trim(),
    thinking: normalizeDraftThinking(single.provider, single.model, modelOptions, single.thinking),
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
  onSetDefaultModelProfile,
  onDeleteModelProfile,
  onReorderModelProfiles,
  modelProfiles = [],
  activeModelProfile,
  initialModelProfileId,
  createModelProfileSignal = 0,
  busy = false,
  showTrigger = true,
  initialAgentName = '',
}: AgentModelControlProps) {
  const queryClient = useQueryClient()
  const { data: uiSettings = {} } = useQuery(uiSettingsQueryOptions())
  const compactSettings = normalizeCompactAgentSettings(uiSettings)
  const finderSettings = normalizeFinderAgentSettings(uiSettings)
  const coderSettings = normalizeCoderAgentSettings(uiSettings)
  const designerSettings = normalizeDesignerAgentSettings(uiSettings)
  const routerSettings = normalizeRouterAgentSettings(uiSettings)
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
  } as AgentProfileRecord), [compactSettings.model, compactSettings.provider, compactSettings.service_tier, compactSettings.thinking])
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
  } as AgentProfileRecord), [finderSettings.model, finderSettings.provider, finderSettings.service_tier, finderSettings.thinking])
  const coderProfile = useMemo(() => ({
    name: CODER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled isolated implementation subagent',
    provider: coderSettingsEnabled ? coderSettings.provider : '', model: coderSettingsEnabled ? coderSettings.model : '', thinking: coderSettingsEnabled ? coderSettings.thinking : '',
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null, toolContract: null,
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [coderSettings.model, coderSettings.provider, coderSettings.service_tier, coderSettings.thinking, coderSettingsEnabled])
  const routerProfile = useMemo(() => ({
    name: ROUTER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled Router model selection',
    provider: routerSettings.provider, model: routerSettings.model, thinking: routerSettings.thinking,
    prompt: '', runtimeMode: 'read', defaultSessionMode: 'auto', executionSetting: 'read',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: {} },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [routerSettings.model, routerSettings.provider, routerSettings.service_tier, routerSettings.thinking])
  const designerProfile = useMemo(() => ({
    name: DESIGNER_AGENT_NAME,
    mode: 'subagent',
    description: 'Compiled same-checkout UI iteration subagent with reusable workspace outputs',
    provider: designerSettings.provider, model: designerSettings.model, thinking: designerSettings.thinking,
    prompt: '', runtimeMode: 'readwrite', defaultSessionMode: 'auto', executionSetting: 'readwrite',
    exitPlanModeEnabled: false, toolScope: null,
    toolContract: { preset: 'custom', inheritPolicy: false, tools: { read: { enabled: true, bashPrefixes: [] }, search: { enabled: true, bashPrefixes: [] }, find: { enabled: true, bashPrefixes: [] }, list: { enabled: true, bashPrefixes: [] }, write: { enabled: true, bashPrefixes: [] }, edit: { enabled: true, bashPrefixes: [] } } },
    enabled: true, protected: true, updatedAt: 0,
  } as AgentProfileRecord), [designerSettings.model, designerSettings.provider, designerSettings.service_tier, designerSettings.thinking])
  function modelDraftForProfile(profile: AgentProfileRecord | null): ModelDraft {
    const draft = singleDraftFromProfile(profile, selectedModel, selectedServiceTier, selectedThinking)
    if (!profile || !isSystemUtility(profile.name)) return draft
    const serviceTier = profile.name === COMPACT_AGENT_NAME
      ? compactSettings.service_tier
      : profile.name === CODER_AGENT_NAME
        ? coderSettings.service_tier
        : profile.name === DESIGNER_AGENT_NAME
          ? designerSettings.service_tier
          : profile.name === ROUTER_AGENT_NAME
            ? routerSettings.service_tier
            : finderSettings.service_tier
    return { ...draft, serviceTier: normalizeDraftServiceTier(draft.provider, serviceTier) }
  }
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [draftProfileName, setDraftProfileName] = useState('')
  const [draftMakeDefault, setDraftMakeDefault] = useState(false)
  const [defaultingProfileId, setDefaultingProfileId] = useState('')
  const [reorderingProfileId, setReorderingProfileId] = useState('')
  const [draggedProfileId, setDraggedProfileId] = useState('')
  const [editingProfileId, setEditingProfileId] = useState('')
  const [baseline, setBaseline] = useState('')
  const [profileNameFocusSignal, setProfileNameFocusSignal] = useState(0)
  const initializedOpenRef = useRef(false)
  const profileNameInputRef = useRef<HTMLInputElement | null>(null)
  const selectableAgents = useMemo(() => [...agents.filter((agent) => agent.enabled !== false && agent.name !== 'finder' && (!isCompiledSystemAgent(agent.name) || agent.name === SWARM_AGENT_NAME)), compactProfile, finderProfile, coderProfile, designerProfile, routerProfile], [agents, coderProfile, compactProfile, designerProfile, finderProfile, routerProfile])
  const activeProfile = selectableAgents.find((agent) => agent.name === selectedPrimaryAgent) ?? selectableAgents.find((agent) => agent.name === currentAgent) ?? null
  const [draftAgentName, setDraftAgentName] = useState(activeProfile?.name ?? selectedPrimaryAgent)
  const draftProfile = selectableAgents.find((agent) => agent.name === draftAgentName) ?? activeProfile
  const [singleDraft, setSingleDraft] = useState<ModelDraft>(() => modelDraftForProfile(activeProfile))
  const providers = useMemo(() => providerOptions(modelOptions), [modelOptions])
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
  const compatibleModelProfiles = modelProfiles
  const displayedModelProfileId = editingProfileId
  const displayedModelProfile = compatibleModelProfiles.find((profile) => profile.profileId === displayedModelProfileId) ?? null
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
    if (createModelProfileSignal <= 0) return
    setOpen(true)
    setProfileNameFocusSignal((current) => current + 1)
  }, [createModelProfileSignal])

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
    const requestedSaved = requestedProfileId ? modelProfiles.find((candidate) => candidate.profileId === requestedProfileId) ?? null : null
    const saved = requestedSaved
    const single = saved ? { provider: saved.provider, model: saved.model, thinking: saved.thinking, serviceTier: saved.serviceTier, contextMode: saved.contextMode } : modelDraftForProfile(profile)
    const name = saved?.name ?? (initialModelProfileId === '' ? '' : activeModelProfile?.source === 'saved' ? `${activeModelProfile.name} copy` : '')
    const makeDefault = saved ? false : modelProfiles.length === 0
    setDraftAgentName(profile?.name ?? selectedPrimaryAgent)
    setSingleDraft(single)
    setDraftProfileName(name)
    setDraftMakeDefault(makeDefault)
    setEditingProfileId(saved?.profileId ?? '')
    setBaseline(JSON.stringify({ name, makeDefault, single }))
    setError(null)
  }, [activeModelProfile, activeProfile, initialAgentName, initialModelProfileId, modelProfiles, open, selectableAgents, selectedModel, selectedPrimaryAgent, selectedServiceTier, selectedThinking])

  useEffect(() => {
    if (!open || profileNameFocusSignal <= 0) return
    const input = profileNameInputRef.current
    if (!input) return
    input.focus()
    input.setSelectionRange(0, 0)
  }, [open, profileNameFocusSignal])

  function chooseAgent(profile: AgentProfileRecord) {
    if (customized && !window.confirm('Discard the unsaved profile changes and switch agents?')) return
    const single = modelDraftForProfile(profile)
    setDraftAgentName(profile.name)
    setSingleDraft(single)
    setDraftProfileName('')
    setDraftMakeDefault(modelProfiles.length === 0)
    setEditingProfileId('')
    setBaseline(JSON.stringify({ name: '', makeDefault: modelProfiles.length === 0, single }))
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

  async function removeModelProfile(profile: ModelProfileRecord) {
    if (saving || busy || !onDeleteModelProfile) return
    if (!window.confirm(`Delete profile “${profile.name}”?`)) return
    setSaving(true)
    setError(null)
    try {
      await onDeleteModelProfile(profile.profileId)
      setOpen(false)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setSaving(false)
    }
  }

  async function persistModelProfileOrder(profileId: string, ordered: ModelProfileRecord[]) {
    if (!onReorderModelProfiles || saving || busy) return
    setReorderingProfileId(profileId)
    setError(null)
    try {
      await onReorderModelProfiles(ordered.map((profile) => profile.profileId))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setReorderingProfileId('')
    }
  }

  function moveModelProfileByOffset(profileId: string, offset: -1 | 1) {
    const visibleIndex = compatibleModelProfiles.findIndex((profile) => profile.profileId === profileId)
    const target = compatibleModelProfiles[visibleIndex + offset]
    if (visibleIndex < 0 || !target) return
    const ordered = [...modelProfiles]
    const from = ordered.findIndex((profile) => profile.profileId === profileId)
    const to = ordered.findIndex((profile) => profile.profileId === target.profileId)
    ;[ordered[from], ordered[to]] = [ordered[to], ordered[from]]
    void persistModelProfileOrder(profileId, ordered)
  }

  function dropModelProfile(targetProfileId: string) {
    if (!draggedProfileId || draggedProfileId === targetProfileId || !onReorderModelProfiles) return
    const ordered = [...modelProfiles]
    const from = ordered.findIndex((profile) => profile.profileId === draggedProfileId)
    const to = ordered.findIndex((profile) => profile.profileId === targetProfileId)
    if (from < 0 || to < 0) return
    const [moved] = ordered.splice(from, 1)
    ordered.splice(to, 0, moved)
    setDraggedProfileId('')
    void persistModelProfileOrder(draggedProfileId, ordered)
  }

  function chooseModelProfile(saved: ModelProfileRecord | null): boolean {
    if (customized && !window.confirm('Discard the unsaved changes and switch profiles?')) return false
    const profile = draftProfile ?? activeProfile
    const single = saved ? { provider: saved.provider, model: saved.model, thinking: saved.thinking, serviceTier: saved.serviceTier, contextMode: saved.contextMode } : modelDraftForProfile(profile)
    const name = saved?.name ?? ''
    const makeDefault = saved ? false : modelProfiles.length === 0
    setSingleDraft(single)
    setDraftProfileName(name)
    setDraftMakeDefault(makeDefault)
    setEditingProfileId(saved?.profileId ?? '')
    setBaseline(JSON.stringify({ name, makeDefault, single }))
    setError(null)
    return true
  }

  function createNewModelProfile() {
    if (!chooseModelProfile(null)) return
    setProfileNameFocusSignal((current) => current + 1)
  }

  function selectProvider(provider: string) {
    setSingleDraft((current) => ({ ...current, provider, model: '', thinking: '', serviceTier: '', contextMode: '' }))
  }

  function selectModel(key: string) {
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
    setSingleDraft(update)
  }

  const currentDraftSignature = JSON.stringify({ name: draftProfileName.trim(), makeDefault: draftMakeDefault, single: singleDraft })
  const customized = modelProfileDraftIsCustomized(baseline, currentDraftSignature)
  const editingModelProfile = modelProfiles.find((profile) => profile.profileId === editingProfileId) ?? null

  async function confirm(persistence: AgentModelControlConfirmInput['persistence']) {
    const profile = draftProfile
    if (!profile || saving || busy) return
    const agentPatch = buildPatch(singleDraft, modelOptions)
    if (!agentPatch.provider || !agentPatch.model || !agentPatch.thinking) {
      setError('Choose provider, model, and thinking for the flat favorite.')
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (isSystemUtility(profile.name)) {
        const saved = await saveSystemAgentSettings({
          current: uiSettings,
          agent: profile.name === COMPACT_AGENT_NAME ? 'compact' : profile.name === CODER_AGENT_NAME ? 'coder' : profile.name === DESIGNER_AGENT_NAME ? 'designer' : profile.name === ROUTER_AGENT_NAME ? 'router' : 'finder',
          settings: {
            provider: String(agentPatch.provider ?? '').trim(),
            model: String(agentPatch.model ?? '').trim(),
            thinking: String(agentPatch.thinking ?? '').trim(),
            service_tier: singleDraft.serviceTier.trim(),
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
          modelProfile: { name: profileName, ...toSelection(singleDraft) },
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
    <div className="fixed inset-0 z-[9999] flex items-stretch justify-center bg-black/50 sm:items-center sm:p-3" role="dialog" aria-modal="true" aria-label="Agent and model settings">
      <div className="flex h-[100dvh] max-h-[100dvh] w-full max-w-6xl flex-col overflow-hidden bg-[var(--app-surface)] shadow-xl sm:h-auto sm:max-h-[min(94dvh,880px)] sm:rounded-xl sm:border sm:border-[var(--app-border)]">
        <div className="flex flex-col gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 sm:flex-row sm:items-start sm:justify-between sm:px-5 sm:py-4">
          <div className="min-w-0">
            <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Agent setup</div>
            <div className="mt-1 truncate text-sm font-semibold text-[var(--app-text)]">{draftProfile ? agentLabel(draftProfile) : displayAgentName(currentAgent) || 'Agent'}</div>
            <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Model setup is separate from agent identity. Choose explicitly whether to use it temporarily or save a named profile.</div>
          </div>
          <div className="grid w-full shrink-0 grid-cols-2 gap-2 sm:flex sm:w-auto sm:items-center">
            {onOpenAgentSettings ? (
              <button type="button" onClick={() => { setOpen(false); onOpenAgentSettings() }} className="inline-flex min-h-10 items-center justify-center gap-1.5 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1">
                <Settings2 size={12} /> Manage agent
              </button>
            ) : null}
            <button type="button" onClick={() => setOpen(false)} className={`min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1 ${onOpenAgentSettings ? '' : 'col-span-2'}`}>Close</button>
          </div>
        </div>

        <div aria-label="Agent setup sections" className="min-h-0 flex-1 overflow-y-auto min-[900px]:grid min-[900px]:grid-cols-[240px_280px_minmax(0,1fr)] min-[900px]:overflow-hidden">
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
                    {section.profiles.map((profile) => {
                      const selected = profile.name === draftAgentName
                      return <button key={profile.name} type="button" onClick={() => chooseAgent(profile)} aria-pressed={selected} className={`group flex w-full items-center rounded-lg border px-2.5 py-2 text-left text-xs transition ${selected ? 'border-[var(--app-primary)] bg-[var(--app-surface)] text-[var(--app-text)] shadow-sm' : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]'}`}><span className="min-w-0 flex-1 truncate font-semibold">{agentLabel(profile)}</span></button>
                    })}
                  </div>
                </section>
              ))}
            </div>
          </aside>

          <section aria-label="Saved model profiles" className="flex min-h-0 flex-col border-b border-[var(--app-border)] bg-[var(--app-surface)] min-[900px]:border-b-0 min-[900px]:border-r">
            <div className="flex items-center justify-between gap-2 border-b border-[var(--app-border)] px-4 py-3">
              <div>
                <div className="text-[11px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">Saved profiles</div>
                <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">Choose a model preset.</div>
              </div>
              {!draftProfile || !isSystemUtility(draftProfile.name) ? <button type="button" onClick={createNewModelProfile} className="inline-flex shrink-0 items-center gap-1 rounded-md px-2 py-1.5 text-[11px] font-semibold text-[var(--app-primary)] hover:bg-[var(--app-surface-hover)]"><Plus size={12} />New</button> : null}
            </div>
            <div className="grid max-h-52 content-start gap-2 overflow-y-auto p-3 min-[900px]:max-h-none min-[900px]:flex-1">
              {compatibleModelProfiles.length > 0 ? compatibleModelProfiles.map((profile) => {
                const selected = displayedModelProfileId === profile.profileId
                const profileIndex = compatibleModelProfiles.findIndex((candidate) => candidate.profileId === profile.profileId)
                const reordering = reorderingProfileId === profile.profileId
                return <div key={profile.profileId} draggable={Boolean(onReorderModelProfiles) && !busy && !saving} onDragStart={() => setDraggedProfileId(profile.profileId)} onDragEnd={() => setDraggedProfileId('')} onDragOver={(event) => { if (draggedProfileId) event.preventDefault() }} onDrop={() => dropModelProfile(profile.profileId)} className={`group flex min-w-0 items-center rounded-lg border bg-[var(--app-surface)] transition ${selected ? 'border-[var(--app-primary)] shadow-sm' : 'border-[var(--app-border)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'}`}>
                  {onReorderModelProfiles ? <span className="ml-1 cursor-grab p-1 text-[var(--app-text-subtle)]" title="Drag to reorder"><GripVertical size={14} /></span> : null}
                  <button type="button" onClick={() => chooseModelProfile(profile)} aria-pressed={selected} className="min-w-0 flex-1 px-2 py-2.5 text-left">
                    <span className="block truncate text-sm font-semibold leading-5 text-[var(--app-text)]">{profile.name}</span>
                    <span className="mt-1 grid gap-0.5 text-[10px] leading-4 text-[var(--app-text-subtle)]">
                      <span className="block truncate">{savedFavoriteModelLabel(profile)}</span>
                    </span>
                  </button>
                  {onSetDefaultModelProfile ? <button type="button" disabled={busy || saving || defaultingProfileId === profile.profileId || profile.isDefault} onClick={() => { void makeModelProfileDefault(profile) }} aria-label={profile.isDefault ? `${profile.name} is the account default` : `Make ${profile.name} the account default`} aria-pressed={profile.isDefault} title={profile.isDefault ? 'Account default' : 'Make account default'} className={`shrink-0 rounded-md p-1.5 transition disabled:cursor-default ${profile.isDefault ? 'text-[var(--app-primary)] opacity-100' : 'text-[var(--app-text-subtle)] opacity-0 hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100 disabled:opacity-50'}`}><Star size={14} fill={profile.isDefault ? 'currentColor' : 'none'} /></button> : null}
                  {onReorderModelProfiles ? <span className="grid shrink-0 pr-1">
                    <button type="button" disabled={busy || saving || reordering || profileIndex <= 0} onClick={() => moveModelProfileByOffset(profile.profileId, -1)} aria-label={`Move ${profile.name} up`} title="Move up" className="rounded p-0.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-25"><ChevronUp size={12} /></button>
                    <button type="button" disabled={busy || saving || reordering || profileIndex < 0 || profileIndex === compatibleModelProfiles.length - 1} onClick={() => moveModelProfileByOffset(profile.profileId, 1)} aria-label={`Move ${profile.name} down`} title="Move down" className="rounded p-0.5 text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-25"><ChevronDown size={12} /></button>
                  </span> : null}
                </div>
              }) : <button type="button" onClick={createNewModelProfile} className="rounded-lg border border-dashed border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3 text-left text-xs text-[var(--app-text-muted)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]">No compatible saved profiles yet. Create your first profile.</button>}
            </div>
          </section>

          <section aria-label="Selected profile settings" className="min-h-0 p-4 min-[900px]:overflow-y-auto min-[900px]:p-5">
            <div className="mb-3 flex flex-col gap-2 border-b border-[var(--app-border)] pb-3 sm:flex-row sm:items-end">
              <div className="min-w-0 flex-1">
                <label className="block text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-subtle)]">
                  Profile settings
                  {!draftProfile || !isSystemUtility(draftProfile.name) ? (
                    <input ref={profileNameInputRef} aria-label="Profile name" value={draftProfileName} onChange={(event) => setDraftProfileName(event.target.value)} placeholder="Profile name" className="mt-1 block w-full rounded-md border border-[var(--app-border)] bg-[var(--app-bg)] px-2.5 py-1.5 text-sm font-semibold normal-case tracking-normal text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
                  ) : (
                    <span className="mt-1 block truncate text-sm font-semibold normal-case tracking-normal text-[var(--app-text)]">{displayedModelProfile?.name || draftProfileName || 'New profile'}</span>
                  )}
                </label>
              </div>
              {displayedModelProfile && (!draftProfile || !isSystemUtility(draftProfile.name)) ? (
                <div className="flex shrink-0 items-center gap-1 self-end">
                  <button type="button" disabled={busy || saving || defaultingProfileId === displayedModelProfile.profileId || displayedModelProfile.isDefault || !onSetDefaultModelProfile} onClick={() => { void makeModelProfileDefault(displayedModelProfile) }} aria-label={displayedModelProfile.isDefault ? `${displayedModelProfile.name} is the account default` : `Make ${displayedModelProfile.name} the account default`} aria-pressed={displayedModelProfile.isDefault} title={displayedModelProfile.isDefault ? 'Account default' : 'Make account default'} className={`shrink-0 rounded-md p-1.5 transition disabled:cursor-default ${displayedModelProfile.isDefault ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-subtle)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] disabled:opacity-50'}`}><Star size={14} fill={displayedModelProfile.isDefault ? 'currentColor' : 'none'} /></button>
                  {onDeleteModelProfile ? <button type="button" disabled={busy || saving} onClick={() => { void removeModelProfile(displayedModelProfile) }} aria-label={`Delete ${displayedModelProfile.name}`} title="Delete profile" className="shrink-0 rounded-md p-1.5 text-[var(--app-text-subtle)] transition hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] disabled:opacity-50"><Trash2 size={14} /></button> : null}
                </div>
              ) : null}
            </div>
            {editingModelProfile ? <div className="mb-3 text-[11px] text-[var(--app-text-muted)]">{editingModelProfile.isDefault ? 'Editing your account default profile. Saving updates it everywhere; continuing for this chat only leaves it unchanged.' : 'Editing a saved profile. Saving updates it everywhere; continuing for this chat only leaves it unchanged.'}</div> : null}
            {customized ? <div className="mb-3 text-[11px] font-semibold text-[var(--app-warning)]">Unsaved changes — choose whether to update the saved profile or use this draft only in the current chat.</div> : null}
            {draftProfile && isSystemUtility(draftProfile.name) ? (
              <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
                <div className="font-semibold text-[var(--app-text)]">Compiled system agent</div>
                <div className="mt-1">{draftProfile.name === COMPACT_AGENT_NAME ? 'Compact' : draftProfile.name === CODER_AGENT_NAME ? 'Coder' : draftProfile.name === DESIGNER_AGENT_NAME ? 'Designer' : draftProfile.name === ROUTER_AGENT_NAME ? 'Router' : 'Finder'} uses its independently configured single-model selection when set, otherwise it inherits the active account model. Its identity, prompt, runtime, and tool contract remain code-owned.</div>
              </div>
            ) : null}

            {modelLocked && modelLockNotice && draftProfile?.name !== CODER_AGENT_NAME && !isSystemUtility(draftProfile?.name ?? '') ? (
              <div className="mt-3 flex gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-[11px] text-[var(--app-text-muted)]">
                <Lock size={13} className="mt-0.5 shrink-0 text-[var(--app-text-subtle)]" />
                <span>{modelLockNotice}</span>
              </div>
            ) : null}

            <ModelDraftEditor className="mt-4" title={draftProfile && isSystemUtility(draftProfile.name) ? `${displayAgentName(draftProfile.name)} model` : 'Favorite model'} draft={singleDraft} providers={providers} modelOptions={modelOptions} onProviderChange={selectProvider} onModelChange={selectModel} onThinkingChange={(thinking) => setSingleDraft((current) => ({ ...current, thinking }))} onServiceTierChange={(serviceTier) => setSingleDraft((current) => ({ ...current, serviceTier }))} showServiceTier />
            {error ? <div className="mt-3 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          </section>
        </div>

        <div className="grid shrink-0 grid-cols-2 gap-2 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3 pb-[calc(0.75rem+env(safe-area-inset-bottom))] sm:flex sm:flex-wrap sm:items-center sm:justify-end sm:px-5 sm:py-4">
          <button type="button" onClick={() => setOpen(false)} className="min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] sm:min-h-0 sm:py-1.5">Cancel</button>
          <button type="button" disabled={busy || saving || !draftProfile || isSystemUtility(draftProfile.name)} onClick={() => { void confirm('temporary') }} className="min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-60 sm:min-h-0 sm:py-1.5">Continue for this chat only</button>
          {editingProfileId && (!draftProfile || !isSystemUtility(draftProfile.name)) ? <button type="button" disabled={busy || saving || !customized} onClick={() => { void confirm('create-copy') }} className="min-h-10 rounded-lg border border-[var(--app-border)] px-3 py-2 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-60 sm:min-h-0 sm:py-1.5">Save as new</button> : null}
          <button type="button" disabled={busy || saving || !draftProfile || (isSystemUtility(draftProfile.name) ? !singleDraft.provider || !singleDraft.model || !singleDraft.thinking : !draftProfileName.trim() || Boolean(editingProfileId && !customized))} onClick={() => { void confirm(editingProfileId ? 'update' : 'create') }} className="col-span-2 min-h-10 rounded-lg border border-[var(--app-primary)] bg-[var(--app-primary)] px-3 py-2 text-[11px] font-semibold text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] disabled:opacity-60 sm:min-h-0 sm:py-1.5">
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
    <div className={`rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 sm:p-4 ${className}`}>
      <div className="mb-3 flex items-center gap-2 text-sm font-semibold text-[var(--app-text)]"><GitBranch size={14} />{title}</div>
      <div className={`grid gap-3 sm:grid-cols-2 ${compact ? '' : 'min-[1100px]:grid-cols-[minmax(130px,0.7fr)_minmax(220px,1.4fr)_minmax(130px,0.7fr)_minmax(130px,0.7fr)]'}`}>
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
