import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { modelOptionsQueryOptions } from '../../queries/query-options'
import { agentModelSettingsQueryKey, agentModelSettingsQueryOptions } from '../settings/swarm/queries/get-agent-model-settings'
import { saveSwarmModelSlot, saveSystemAgentModelSettings } from '../settings/swarm/mutations/save-agent-model-settings'
import type { AgentModelAssignment, AgentModelSettings, AgentModelRole, SystemAgentModelName } from '../settings/swarm/types/agent-model-settings'
import { DirectModelEditor } from '../settings/models/components/swarm-model-assignment-settings'
import { toFlatModelOptions } from '../settings/models/components/models-settings-page'

function assignmentFor(settings: AgentModelSettings, role: AgentModelRole): AgentModelAssignment {
  return role.group === 'swarm' ? settings.swarm[role.slot as 'action' | 'plan'] : settings.systemAgents[role.slot as SystemAgentModelName]
}

export function OrchestrateAgents() {
  const client = useQueryClient()
  const query = useQuery(agentModelSettingsQueryOptions())
  const options = useQuery(modelOptionsQueryOptions())
  const [selected, setSelected] = useState('swarm')
  const [drafts, setDrafts] = useState<Record<string, AgentModelAssignment>>({})
  const [statuses, setStatuses] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState<string | null>(null)
  const role = query.data?.roles?.find((item) => item.id === selected) ?? query.data?.roles?.[0]
  const settings = query.data
  const save = async () => {
    if (!role || !settings || saving) return
    const target = role
    const value = drafts[target.id] ?? assignmentFor(settings, target)
    setSaving(target.id)
    setStatuses((previous) => ({ ...previous, [target.id]: 'Saving…' }))
    try {
      const result = target.group === 'swarm'
        ? await saveSwarmModelSlot(target.slot as 'action' | 'plan', value)
        : await saveSystemAgentModelSettings({ agent: target.slot as SystemAgentModelName, assignment: value })
      client.setQueryData(agentModelSettingsQueryKey, result)
      setStatuses((previous) => ({ ...previous, [target.id]: 'Saved. Applies account-wide to future executions.' }))
    } catch (error) {
      setStatuses((previous) => ({ ...previous, [target.id]: error instanceof Error ? error.message : 'Save failed. Retry.' }))
    } finally { setSaving(null) }
  }
  return <section className="min-h-0 min-w-0 overflow-y-auto space-y-5 p-4 text-[var(--app-text)]">
    <header><h1 className="text-xl font-semibold">Agents</h1><p className="text-sm text-[var(--app-text-muted)]">Account-wide compiled system roles — not workers or running sessions.</p></header>
    {(query.isPending || options.isPending) && <p role="status">Loading assignments and supported models…</p>}
    {(query.error || options.error) && <div role="alert"><p>{String(query.error || options.error)}</p><button type="button" onClick={() => { void query.refetch(); void options.refetch() }}>Retry</button></div>}
    {query.data && !query.data.roles?.length && <p role="alert">System roles are unconfigured or unavailable. Update the daemon and retry. <button type="button" onClick={() => void query.refetch()}>Retry</button></p>}
    {query.data?.roles && <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(140px,200px)_minmax(0,1fr)]">
      <nav aria-label="System roles" className="flex flex-wrap gap-2 lg:flex-col">
        {query.data.roles.map((item) => {
          const assigned = assignmentFor(query.data!, item)
          return <button key={item.id} type="button" aria-pressed={role?.id === item.id} onClick={() => setSelected(item.id)} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 text-left">
            <strong className="block">{item.label}</strong><span className="block break-words text-xs text-[var(--app-text-muted)]">{assigned?.provider && assigned?.model ? `${assigned.provider} / ${assigned.model}` : 'Unconfigured'}</span>
          </button>
        })}
      </nav>
      {role && settings && <div className="min-w-0 space-y-4">
        {role.slot === 'plan' && <p className="text-sm">Orchestrator and Plan share this account-wide assignment. Changing it affects both; this does not create a separate Orchestrator model slot.</p>}
        <DirectModelEditor label={role.label} value={drafts[role.id] ?? assignmentFor(settings, role)} modelOptions={toFlatModelOptions(options.data ?? [])} disabled={Boolean(saving) || !options.data?.length} onChange={(value) => { setDrafts((previous) => ({ ...previous, [role.id]: value })); setStatuses((previous) => ({ ...previous, [role.id]: '' })) }} />
        <button type="button" disabled={Boolean(saving) || !options.data?.length} onClick={() => void save()} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2">{saving === role.id ? 'Saving…' : `Save ${role.label}`}</button>
        {statuses[role.id] && <p role="status">{statuses[role.id]}</p>}
      </div>}
    </div>}
  </section>
}
