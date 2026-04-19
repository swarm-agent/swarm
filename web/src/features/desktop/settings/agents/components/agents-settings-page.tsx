import { useEffect, useMemo, useState, type ChangeEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Plus, RotateCcw, Settings2, Trash2 } from 'lucide-react'
import { requestJson } from '../../../../../app/api'
import { modelOptionsQueryOptions, agentStateQueryOptions } from '../../../../queries/query-options'
import { resetAgentDefaults, restoreAgentDefaults } from '../../../../desktop/chat/queries/chat-queries'
import type { AgentProfileRecord, AgentStateRecord } from '../../../chat/types/chat'

interface AgentFormState {
  name: string
  mode: string
  description: string
  provider: string
  model: string
  thinking: string
  prompt: string
  executionSetting: 'read' | 'readwrite' | ''
  exitPlanModeEnabled: boolean
  toolScopePreset: string
  toolScopeAllowTools: string
  toolScopeDenyTools: string
  toolScopeBashPrefixes: string
  toolScopeInheritPolicy: boolean
  enabled: boolean
}

const NEW_AGENT_KEY = '__new__'
const THINKING_OPTIONS = [
  { value: '', label: 'Default' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'X-High' },
]

function emptyAgentForm(): AgentFormState {
  return {
    name: '',
    mode: 'primary',
    description: '',
    provider: '',
    model: '',
    thinking: '',
    prompt: '',
    executionSetting: 'readwrite',
    exitPlanModeEnabled: false,
    toolScopePreset: '',
    toolScopeAllowTools: '',
    toolScopeDenyTools: '',
    toolScopeBashPrefixes: '',
    toolScopeInheritPolicy: false,
    enabled: true,
  }
}

function agentRuntimeSummary(profile: AgentProfileRecord): string {
  if (profile.exitPlanModeEnabled) {
    return 'plan -> auto'
  }
  return profile.executionSetting || 'unset'
}

function profileToForm(profile: AgentProfileRecord | null | undefined): AgentFormState {
  if (!profile) {
    return emptyAgentForm()
  }
  return {
    name: profile.name,
    mode: profile.mode || 'primary',
    description: profile.description,
    provider: profile.provider,
    model: profile.model,
    thinking: profile.thinking,
    prompt: profile.prompt,
    executionSetting: profile.executionSetting,
    exitPlanModeEnabled: profile.exitPlanModeEnabled,
    toolScopePreset: profile.toolScope?.preset ?? '',
    toolScopeAllowTools: (profile.toolScope?.allowTools ?? []).join(', '),
    toolScopeDenyTools: (profile.toolScope?.denyTools ?? []).join(', '),
    toolScopeBashPrefixes: (profile.toolScope?.bashPrefixes ?? []).join(', '),
    toolScopeInheritPolicy: Boolean(profile.toolScope?.inheritPolicy),
    enabled: profile.enabled,
  }
}

async function upsertAgent(input: AgentFormState): Promise<string> {
  const toolContractTools: Record<string, { enabled?: boolean, bash_prefixes?: string[] }> = {}
  for (const value of input.toolScopeAllowTools.split(',').map((entry) => entry.trim()).filter(Boolean)) {
    toolContractTools[value] = { enabled: true }
  }
  for (const value of input.toolScopeDenyTools.split(',').map((entry) => entry.trim()).filter(Boolean)) {
    toolContractTools[value] = { enabled: false }
  }
  const bashPrefixes = input.toolScopeBashPrefixes.split(',').map((value) => value.trim()).filter(Boolean)
  if (bashPrefixes.length > 0) {
    toolContractTools.bash = { enabled: true, bash_prefixes: bashPrefixes }
  }

  const response = await requestJson<{ profile?: { name?: string } }>(`/v2/agents/${encodeURIComponent(input.name.trim())}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      mode: input.mode,
      description: input.description.trim(),
      provider: input.provider,
      model: input.model,
      thinking: input.thinking,
      prompt: input.prompt,
      execution_setting: input.exitPlanModeEnabled ? '' : input.executionSetting,
      exit_plan_mode_enabled: input.exitPlanModeEnabled,
      tool_contract: {
        preset: input.toolScopePreset.trim() || undefined,
        tools: Object.keys(toolContractTools).length > 0 ? toolContractTools : undefined,
        inherit_policy: input.toolScopeInheritPolicy,
      },
      enabled: input.enabled,
    }),
  })
  return String(response.profile?.name ?? input.name).trim()
}

async function activatePrimaryAgent(name: string): Promise<void> {
  await requestJson('/v2/agents/active/primary', {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ name: name.trim() }),
  })
}

async function deleteAgent(name: string): Promise<void> {
  await requestJson(`/v2/agents/${encodeURIComponent(name.trim())}`, {
    method: 'DELETE',
  })
}

function actionButtonClassName(intent: 'primary' | 'secondary' | 'danger'): string {
  if (intent === 'primary') {
    return 'inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-transparent bg-[var(--app-primary)] px-4 py-2 text-sm font-medium text-[var(--app-primary-text)] shadow-sm transition-colors hover:bg-[var(--app-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50'
  }
  if (intent === 'danger') {
    return 'inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[var(--app-danger)]/25 bg-[var(--app-danger)]/10 px-4 py-2 text-sm font-medium text-[var(--app-danger)] shadow-sm transition-colors hover:bg-[var(--app-danger)]/18 disabled:cursor-not-allowed disabled:opacity-50'
  }
  return 'inline-flex min-h-10 items-center justify-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-2 text-sm font-medium text-[var(--app-text)] shadow-sm transition-colors hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50'
}

async function restoreDefaults(): Promise<AgentStateRecord> {
  return restoreAgentDefaults()
}

function PromptEditor({ form, onSavePrompt, busy, disabled }: { form: AgentFormState, onSavePrompt: (prompt: string) => Promise<void>, busy: boolean, disabled: boolean }) {
  const [isEditing, setIsEditing] = useState(false)
  const [currentPrompt, setCurrentPrompt] = useState(form.prompt)

  useEffect(() => {
    if (!isEditing) {
      setCurrentPrompt(form.prompt)
    }
  }, [form.prompt, isEditing])

  useEffect(() => {
    setIsEditing(false)
  }, [form.name])

  const hasChanges = currentPrompt !== form.prompt

  const handleSavePrompt = async () => {
    await onSavePrompt(currentPrompt)
    setIsEditing(false)
  }

  if (!isEditing) {
    return (
      <div 
        className="w-full cursor-pointer rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)]"
        onClick={() => {
          if (!disabled) setIsEditing(true)
        }}
      >
        {form.prompt ? (
          <div className="line-clamp-3 whitespace-pre-wrap font-mono text-sm opacity-80 text-[var(--app-text)]">{form.prompt}</div>
        ) : (
          <div className="font-mono text-sm text-[var(--app-text-muted)] italic">No system prompt set. Click to edit.</div>
        )}
      </div>
    )
  }

  return (
    <div className="flex w-full flex-col gap-3 rounded-xl border border-[var(--app-border-strong)] bg-[var(--app-bg)] p-4 shadow-sm">
      <textarea
        value={currentPrompt}
        onChange={(e) => setCurrentPrompt(e.target.value)}
        disabled={busy}
        placeholder="System prompt / instructions for this agent"
        className="min-h-[240px] w-full resize-y bg-transparent font-mono text-sm leading-relaxed text-[var(--app-text)] outline-none placeholder:text-[var(--app-text-muted)]"
      />
      <div className="flex items-center justify-end gap-2 border-t border-[var(--app-border)] pt-3">
        <button
          type="button"
          onClick={() => {
            setCurrentPrompt(form.prompt)
            setIsEditing(false)
          }}
          disabled={busy}
          className="rounded-md px-3 py-1.5 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)]"
        >
          Cancel
        </button>
        {hasChanges && (
          <button
            type="button"
            onClick={() => void handleSavePrompt()}
            disabled={busy}
            className="rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-medium text-[var(--app-primary)] transition-colors hover:bg-[var(--app-surface-subtle)] disabled:opacity-50"
          >
            {busy ? 'Saving…' : 'Save Prompt'}
          </button>
        )}
      </div>
    </div>
  )
}

export function AgentsSettingsPage() {
  const queryClient = useQueryClient()
  const { data: agentState, isLoading, isFetching, refetch: refetchAgentState } = useQuery(agentStateQueryOptions())
  const { data: modelOptions = [] } = useQuery(modelOptionsQueryOptions())

  useEffect(() => {
    void refetchAgentState()
  }, [refetchAgentState])

  const profiles = agentState?.profiles ?? []
  const activePrimary = agentState?.activePrimary?.trim() || 'swarm'

  const [viewMode, setViewMode] = useState<'list' | 'edit'>('list')
  const [selectedKey, setSelectedKey] = useState<string>('')
  const [form, setForm] = useState<AgentFormState>(emptyAgentForm())
  const [saving, setSaving] = useState(false)
  const [status, setStatus] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (profiles.length === 0) {
      // If there are literally no profiles, default to new
      setSelectedKey(NEW_AGENT_KEY)
      setForm(emptyAgentForm())
      return
    }
    const hasSelected = selectedKey !== '' && selectedKey !== NEW_AGENT_KEY && profiles.some((profile) => profile.name === selectedKey)
    if (hasSelected) {
      return
    }
    const nextSelected = profiles.some((profile) => profile.name === activePrimary) ? activePrimary : profiles[0].name
    setSelectedKey(nextSelected)
  }, [activePrimary, profiles, selectedKey])

  const selectedProfile = useMemo(
    () => (selectedKey && selectedKey !== NEW_AGENT_KEY ? profiles.find((profile) => profile.name === selectedKey) ?? null : null),
    [profiles, selectedKey],
  )

  useEffect(() => {
    if (selectedKey === NEW_AGENT_KEY) {
      setForm(emptyAgentForm())
      return
    }
    setForm(profileToForm(selectedProfile))
  }, [selectedKey, selectedProfile])

  const providerOptions = useMemo(() => {
    const values = new Set<string>()
    for (const option of modelOptions) {
      if (option.provider.trim() !== '') {
        values.add(option.provider.trim())
      }
    }
    if (selectedProfile?.provider?.trim()) {
      values.add(selectedProfile.provider.trim())
    }
    return Array.from(values).sort((left, right) => left.localeCompare(right))
  }, [modelOptions, selectedProfile?.provider])

  const modelChoices = useMemo(() => {
    if (!form.provider.trim()) {
      return []
    }
    const values = new Set<string>()
    for (const option of modelOptions) {
      if (option.provider === form.provider && option.model.trim() !== '') {
        values.add(option.model.trim())
      }
    }
    if (selectedProfile?.provider === form.provider && selectedProfile.model.trim() !== '') {
      values.add(selectedProfile.model.trim())
    }
    return Array.from(values).sort((left, right) => left.localeCompare(right))
  }, [form.provider, modelOptions, selectedProfile?.model, selectedProfile?.provider])

  useEffect(() => {
    if (!form.provider.trim() && form.model !== '') {
      setForm((current) => ({ ...current, model: '' }))
      return
    }
    if (form.model && modelChoices.length > 0 && !modelChoices.includes(form.model)) {
      setForm((current) => ({ ...current, model: '' }))
    }
  }, [form.model, form.provider, modelChoices])

  const agentStateQueryKey = agentStateQueryOptions().queryKey

  const applyAgentState = (nextState: AgentStateRecord) => {
    queryClient.setQueryData(agentStateQueryKey, nextState)
    return nextState
  }

  const refreshAgents = async () => {
    return queryClient.fetchQuery(agentStateQueryOptions())
  }

  const handleSelectProfile = (name: string) => {
    setSelectedKey(name)
    setStatus(null)
    setError(null)
    setViewMode('edit')
  }

  const handleCreateNew = () => {
    setSelectedKey(NEW_AGENT_KEY)
    setStatus(null)
    setError(null)
    setViewMode('edit')
  }

  const handleBackToList = () => {
    setViewMode('list')
    setStatus(null)
    setError(null)
  }

  const handleSaveWithPrompt = async (newPrompt: string) => {
    const trimmedName = form.name.trim()
    if (!trimmedName) {
      setError('Agent name is required.')
      return
    }
    if (!form.mode.trim()) {
      setError('Agent mode is required.')
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const savedName = await upsertAgent({
        ...form,
        name: trimmedName,
        description: form.description.trim(),
        provider: form.provider.trim(),
        model: form.provider.trim() ? form.model.trim() : '',
        thinking: form.thinking.trim(),
        prompt: newPrompt,
      })
      await refreshAgents()
      setSelectedKey(savedName || trimmedName)
      setForm((current) => ({ ...current, prompt: newPrompt }))
      setStatus(`Saved prompt for agent ${savedName || trimmedName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save agent prompt')
    } finally {
      setSaving(false)
    }
  }

  const handleSave = async () => {
    const trimmedName = form.name.trim()
    if (!trimmedName) {
      setError('Agent name is required.')
      return
    }
    if (!form.mode.trim()) {
      setError('Agent mode is required.')
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const savedName = await upsertAgent({
        ...form,
        name: trimmedName,
        description: form.description.trim(),
        provider: form.provider.trim(),
        model: form.provider.trim() ? form.model.trim() : '',
        thinking: form.thinking.trim(),
      })
      await refreshAgents()
      setSelectedKey(savedName || trimmedName)
      setStatus(`Saved agent ${savedName || trimmedName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save agent')
    } finally {
      setSaving(false)
    }
  }

  const handleActivate = async () => {
    const targetName = selectedProfile?.name?.trim() || form.name.trim()
    if (!targetName) {
      setError('Choose a primary agent first.')
      return
    }
    if ((selectedProfile?.mode || form.mode).trim().toLowerCase() !== 'primary') {
      setError('Only primary agents can be activated.')
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      await activatePrimaryAgent(targetName)
      await refreshAgents()
      setSelectedKey(targetName)
      setStatus(`Activated primary agent ${targetName}.`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to activate agent')
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async () => {
    const targetName = selectedProfile?.name?.trim()
    if (!targetName) {
      setError('Choose an existing agent to delete.')
      return
    }
    if (selectedProfile?.protected || targetName.toLowerCase() === 'memory') {
      setError('memory cannot be deleted because it is used for session titles.')
      return
    }
    if (!window.confirm(`Delete agent ${targetName}?`)) {
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      await deleteAgent(targetName)
      const nextState = await refreshAgents()
      applyAgentState(nextState)
      setSelectedKey('')
      setStatus(`Deleted agent ${targetName}.`)
      setViewMode('list')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete agent')
    } finally {
      setSaving(false)
    }
  }

  const handleRestoreDefaults = async () => {
    if (!window.confirm('Restore built-in agents and default assignments? This keeps custom agents and tools.')) {
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const nextState = await restoreDefaults()
      applyAgentState(nextState)
      setSelectedKey(nextState.activePrimary || nextState.profiles[0]?.name || '')
      setViewMode('list')
      setStatus('Restored default agents.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to restore default agents')
    } finally {
      setSaving(false)
    }
  }

  const handleResetDefaults = async () => {
    const confirmed = window.confirm(
      'Delete all custom agents and custom tools, then reset agent state to the built-in defaults? This cannot be undone.',
    )
    if (!confirmed) {
      return
    }
    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const nextState = await resetAgentDefaults()
      applyAgentState(nextState)
      setSelectedKey(nextState.activePrimary || nextState.profiles[0]?.name || '')
      setViewMode('list')
      setStatus('Reset agents and tools to defaults.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to reset agents to defaults')
    } finally {
      setSaving(false)
    }
  }

  const selectedMode = (selectedProfile?.mode || form.mode).trim().toLowerCase()
  const canActivate = Boolean(selectedProfile?.name) && selectedMode === 'primary' && selectedProfile?.name !== activePrimary
  const canDelete = Boolean(selectedProfile?.name) && !Boolean(selectedProfile?.protected)
  const busy = saving || isFetching

  const primaryAgents = profiles.filter(p => (p.mode || 'primary').toLowerCase() === 'primary')
  const subAgents = profiles.filter(p => (p.mode || 'primary').toLowerCase() === 'subagent')
  const backgroundAgents = profiles.filter(p => (p.mode || 'primary').toLowerCase() === 'background')

  if (viewMode === 'list') {
    return (
      <div className="flex h-full flex-col">
        <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h1 className="text-xl font-semibold text-[var(--app-text)]">Agents</h1>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">Manage desktop and TUI agent profiles.</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <button 
              type="button" 
              onClick={handleCreateNew} 
              disabled={busy}
              className={actionButtonClassName('primary')}
            >
              <Plus size={16} />
              New agent
            </button>
            <button
              type="button"
              onClick={() => void handleRestoreDefaults()}
              disabled={busy}
              className={actionButtonClassName('secondary')}
            >
              <RotateCcw size={16} />
              Restore defaults
            </button>
            <button
              type="button"
              onClick={() => void handleResetDefaults()}
              disabled={busy}
              className={actionButtonClassName('danger')}
            >
              <Trash2 size={16} />
              Delete all & reset
            </button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto pb-12 pr-2">
          {error ? <div className="mb-6 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
          {status ? <div className="mb-6 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">{status}</div> : null}

          <div className="grid grid-cols-1 gap-8 lg:grid-cols-3 lg:gap-12">
            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">Primary Agents</h3>
              <div className="flex flex-col gap-3">
                {primaryAgents.map((profile) => {
                  const isActive = profile.name === activePrimary
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 flex w-full items-center justify-between gap-2">
                        <span className="truncate font-semibold text-[var(--app-text)]">{profile.name}</span>
                        {isActive && <span className="h-2 w-2 shrink-0 rounded-full bg-[var(--app-success)]" title="Active Primary" />}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
            </div>

            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">Subagents</h3>
              <div className="flex flex-col gap-3">
                {subAgents.map((profile) => {
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 w-full truncate font-semibold text-[var(--app-text)]">
                        {profile.name}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
            </div>

            <div className="flex flex-col gap-4">
              <h3 className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)] m-0">Background Agents</h3>
              <div className="flex flex-col gap-3">
                {backgroundAgents.map((profile) => {
                  return (
                    <button
                      key={profile.name}
                      onClick={() => handleSelectProfile(profile.name)}
                      className="group relative flex flex-col items-start overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition-colors hover:border-[var(--app-primary)] hover:bg-[var(--app-bg)] shadow-sm"
                    >
                      <div className="mb-0.5 w-full truncate font-semibold text-[var(--app-text)]">
                        {profile.name}
                      </div>
                      <span className="w-full truncate text-xs font-medium text-[var(--app-text-muted)]">
                        {agentRuntimeSummary(profile)}
                      </span>
                      {profile.description && (
                        <span className="mt-1.5 line-clamp-1 w-full text-xs text-[var(--app-text-muted)] opacity-80">
                          {profile.description}
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
            </div>
          </div>
        </div>
      </div>
    )
  }

  // Edit View
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 flex flex-wrap items-center justify-between gap-4">
        <button 
          onClick={handleBackToList} 
          className="flex items-center gap-2 text-sm font-medium text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)]"
        >
          <span>←</span> Back to Agents
        </button>
        <div className="flex items-center gap-4 text-sm text-[var(--app-text-muted)]">
          <div><span className="font-medium text-[var(--app-text)]">Active primary:</span> {activePrimary}</div>
          <div><span className="font-medium text-[var(--app-text)]">Status:</span> {busy ? 'Refreshing…' : isLoading ? 'Loading…' : 'Ready'}</div>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto pb-12 pr-2">
        {error ? <div className="mb-6 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
        {status ? <div className="mb-6 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">{status}</div> : null}
        <div className="mb-6 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">
          memory cannot be deleted because it is used for session titles. Other utility agents can be removed or replaced. Use Restore defaults if you want the built-ins back.
        </div>

        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] shadow-sm">
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 rounded-t-xl">
            <h4 className="text-sm font-semibold text-[var(--app-text)]">{selectedProfile ? `Edit ${selectedProfile.name}` : 'New agent'}</h4>
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" onClick={() => void handleActivate()} disabled={!canActivate || busy} className="rounded-md border border-[var(--app-border)] bg-[var(--app-surface-elevated)] px-3 py-1.5 text-xs font-medium text-[var(--app-text)] transition-colors hover:bg-[var(--app-surface-hover)] disabled:opacity-50 disabled:cursor-not-allowed">
                Make active primary
              </button>
              {canDelete && (
                <button type="button" onClick={() => void handleDelete()} disabled={!canDelete || busy} className="rounded-md border border-transparent bg-[var(--app-danger)]/10 px-3 py-1.5 text-xs font-medium text-[var(--app-danger)] transition-colors hover:bg-[var(--app-danger)]/20 disabled:opacity-50 disabled:cursor-not-allowed">
                  Delete
                </button>
              )}
              <button type="button" onClick={() => void handleSave()} disabled={busy} className="rounded-md border border-[var(--app-primary)] bg-transparent px-3 py-1.5 text-xs font-medium text-[var(--app-primary)] transition-colors hover:bg-[var(--app-surface-subtle)] disabled:opacity-50 disabled:cursor-not-allowed">
                {saving ? 'Saving…' : 'Save agent'}
              </button>
            </div>
          </div>

          <div className="p-0">
            <div className="border-b border-[var(--app-border)]">
              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Name</label>
                <input
                  type="text"
                  value={form.name}
                  onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, name: event.target.value }))}
                  disabled={busy}
                  placeholder="agent-name"
                  autoComplete="off"
                  className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 placeholder:text-[var(--app-text-muted)]"
                />
              </div>
              
              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Description</label>
                <input
                  type="text"
                  value={form.description}
                  onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, description: event.target.value }))}
                  disabled={busy}
                  placeholder="What this agent is for"
                  autoComplete="off"
                  className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 placeholder:text-[var(--app-text-muted)]"
                />
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Mode</label>
                <div className="relative w-full">
                  <select
                    value={form.mode}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) => setForm((current) => ({ ...current, mode: event.target.value }))}
                    disabled={busy}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    <option value="primary">Primary</option>
                    <option value="subagent">Subagent</option>
                    <option value="background">Background</option>
                  </select>
                  <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                </div>
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Provider</label>
                <div className="relative w-full">
                  <select
                    value={form.provider}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) => {
                      const provider = event.target.value
                      setForm((current) => ({
                        ...current,
                        provider,
                        model: provider === current.provider ? current.model : '',
                      }))
                    }}
                    disabled={busy}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    <option value="">Inherit</option>
                    {providerOptions.map((provider) => (
                      <option key={provider} value={provider}>{provider}</option>
                    ))}
                  </select>
                  <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                </div>
              </div>

              <div className="flex items-center border-b border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Model</label>
                <div className="relative w-full">
                  <select
                    value={form.model}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) => setForm((current) => ({ ...current, model: event.target.value }))}
                    disabled={busy || !form.provider.trim()}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    <option value="">Inherit</option>
                    {modelChoices.map((model) => (
                      <option key={model} value={model}>{model}</option>
                    ))}
                  </select>
                  <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                </div>
              </div>

              <div className="flex items-center px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Thinking</label>
                <div className="relative w-full">
                  <select
                    value={form.thinking}
                    onChange={(event: ChangeEvent<HTMLSelectElement>) => setForm((current) => ({ ...current, thinking: event.target.value }))}
                    disabled={busy}
                    className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                  >
                    {THINKING_OPTIONS.map((option) => (
                      <option key={option.label} value={option.value}>{option.label}</option>
                    ))}
                  </select>
                  <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                </div>
              </div>

              <div className="flex items-center border-t border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Execution</label>
                <div className="w-full">
                  <div className="relative">
                    <select
                      value={form.executionSetting}
                      onChange={(event: ChangeEvent<HTMLSelectElement>) => setForm((current) => ({ ...current, executionSetting: event.target.value as 'read' | 'readwrite' | '' }))}
                      disabled={busy || form.exitPlanModeEnabled}
                      className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 pr-8 text-sm font-medium text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)] disabled:cursor-not-allowed disabled:opacity-50 cursor-pointer"
                    >
                      <option value="">unset</option>
                      <option value="read">read</option>
                      <option value="readwrite">readwrite</option>
                    </select>
                    <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                  </div>
                  <p className="mt-2 text-xs text-[var(--app-text-muted)]">
                    {form.exitPlanModeEnabled
                      ? 'Plan-mode agents use the plan -> auto contract; execution_setting is ignored and cleared.'
                      : 'Required when plan mode is off. Tool scope can only narrow this baseline.'}
                  </p>
                </div>
              </div>

              <div className="flex items-center border-t border-[var(--app-border)] px-4 py-3">
                <label className="w-1/4 shrink-0 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Plan mode</label>
                <label className="inline-flex items-center gap-2 text-sm text-[var(--app-text)]">
                  <input
                    type="checkbox"
                    checked={form.exitPlanModeEnabled}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({
                      ...current,
                      exitPlanModeEnabled: event.target.checked,
                      executionSetting: event.target.checked ? '' : current.executionSetting,
                    }))}
                    disabled={busy}
                  />
                  Enable plan → approval → execute flow
                </label>
              </div>

              <div className="border-t border-[var(--app-border)] px-4 py-4">
                <div className="mb-3 flex items-center gap-2 text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                  <Settings2 size={14} /> Advanced tool scope
                </div>
                <p className="mb-4 text-xs text-[var(--app-text-muted)]">
                  {form.exitPlanModeEnabled
                    ? 'Optional narrowing overlay for the plan/auto runtime contract. It can only remove capability; it never expands it.'
                    : 'Optional narrowing overlay. This can only remove capability from the base execution setting; it never expands it.'}
                </p>
                <div className="grid grid-cols-1 gap-3">
                  <input
                    type="text"
                    value={form.toolScopePreset}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, toolScopePreset: event.target.value }))}
                    disabled={busy}
                    placeholder="Preset (optional)"
                    autoComplete="off"
                    className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
                  />
                  <input
                    type="text"
                    value={form.toolScopeAllowTools}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, toolScopeAllowTools: event.target.value }))}
                    disabled={busy}
                    placeholder="Allow tools (comma-separated)"
                    autoComplete="off"
                    className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
                  />
                  <input
                    type="text"
                    value={form.toolScopeDenyTools}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, toolScopeDenyTools: event.target.value }))}
                    disabled={busy}
                    placeholder="Deny tools (comma-separated)"
                    autoComplete="off"
                    className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
                  />
                  <input
                    type="text"
                    value={form.toolScopeBashPrefixes}
                    onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, toolScopeBashPrefixes: event.target.value }))}
                    disabled={busy}
                    placeholder="Bash prefixes (comma-separated)"
                    autoComplete="off"
                    className="w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-1.5 text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
                  />
                  <label className="inline-flex items-center gap-2 text-sm text-[var(--app-text)]">
                    <input
                      type="checkbox"
                      checked={form.toolScopeInheritPolicy}
                      onChange={(event: ChangeEvent<HTMLInputElement>) => setForm((current) => ({ ...current, toolScopeInheritPolicy: event.target.checked }))}
                      disabled={busy}
                    />
                    Apply stored permission policy in addition to this overlay
                  </label>
                </div>
              </div>
            </div>

            <div className="flex flex-col gap-3 px-4 py-4 transition-colors">
              <label className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">
                System Prompt
              </label>
              <PromptEditor
                form={form}
                onSavePrompt={handleSaveWithPrompt}
                busy={busy}
                disabled={busy}
              />
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
