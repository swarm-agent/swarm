import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { modelOptionsQueryOptions } from '../../queries/query-options'
import { agentModelSettingsQueryKey, agentModelSettingsQueryOptions } from '../settings/swarm/queries/get-agent-model-settings'
import { saveSwarmModelSlot, saveSystemAgentModelSettings } from '../settings/swarm/mutations/save-agent-model-settings'
import type { AgentModelAssignment, AgentModelSettings, AgentModelRole, SystemAgentModelName } from '../settings/swarm/types/agent-model-settings'
import { DirectModelEditor } from '../settings/models/components/swarm-model-assignment-settings'
import { toFlatModelOptions } from '../settings/models/components/models-settings-page'

// Presentation only: the daemon's role list and account settings remain authoritative.
const roleGuidance: Record<AgentModelRole['slot'], { role: string; description: string; bestFor: string }> = {
  action: { role: 'Default agent · Big features', description: 'Takes on larger features and end-to-end work, coordinating research and implementation when needed.', bestFor: 'Multi-step features, larger changes, and work that needs a general-purpose agent.' },
  plan: { role: 'Orchestrator · Planning', description: 'Plans work for you, breaks goals into tasks, and coordinates agents across your project.', bestFor: 'Turning a broad goal into a plan and orchestrating work across multiple tasks.' },
  coder: { role: 'Implementation · Small features', description: 'Implements focused changes in an isolated worktree and hands back code and tests for review.', bestFor: 'Deploying a small feature, fixing a bug, or implementing a well-scoped task.' },
  finder: { role: 'Research · Read-only', description: 'Explores code and researches questions, returning findings without changing your files.', bestFor: 'Locating code, understanding behavior, and gathering evidence before implementation.' },
  designer: { role: 'Design · Visual exploration', description: 'Creates UI and visual design iterations for you to review and refine.', bestFor: 'Exploring design directions, interface concepts, and visual alternatives.' },
  compact: { role: 'Context · Background utility', description: 'Condenses conversation context so important decisions stay available as work continues.', bestFor: 'Automatic context compaction and session titling, not feature implementation.' },
  router: { role: 'Routing · Background utility', description: 'Interprets requests and prepares structured routing decisions behind the scenes.', bestFor: 'Internal request routing, not a standalone coding or planning session.' },
}

function guidanceFor(role: AgentModelRole) {
  return roleGuidance[role.slot] ?? { role: 'System agent', description: 'A system role provided by your daemon.', bestFor: 'Configure the model used by this role for future executions.' }
}

function assignmentFor(settings: AgentModelSettings, role: AgentModelRole): AgentModelAssignment {
  return role.group === 'swarm' ? settings.swarm[role.slot as 'action' | 'plan'] : settings.systemAgents[role.slot as SystemAgentModelName]
}

export function OrchestrateAgents() {
  const client = useQueryClient()
  const query = useQuery(agentModelSettingsQueryOptions())
  const options = useQuery(modelOptionsQueryOptions())
  const [selected, setSelected] = useState<string | null>(null)
  const [drafts, setDrafts] = useState<Record<string, AgentModelAssignment>>({})
  const [statuses, setStatuses] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState<string | null>(null)
  // Derive presentation order without changing the account query cache.
  const roles = query.data?.roles ?? []
  const orderedRoles = [...roles.filter((item) => item.id === 'system-orchestrator'), ...roles.filter((item) => item.id !== 'system-orchestrator')]
  const role = orderedRoles.find((item) => item.id === selected) ?? orderedRoles[0]
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
    <header className="swarm-agents-header">
      <span className="swarm-agent-eyebrow">Your agent team</span>
      <h1>Agents</h1>
      <p>Find the right agent for the job. From a focused fix to a bigger feature, each role has a purpose.</p>
      <span className="swarm-agents-scope">Account-wide models · Future executions</span>
    </header>
    {(query.isPending || options.isPending) && <p role="status">Loading assignments and supported models…</p>}
    {(query.error || options.error) && <div role="alert"><p>{String(query.error || options.error)}</p><button type="button" onClick={() => { void query.refetch(); void options.refetch() }}>Retry</button></div>}
    {query.data && !query.data.roles?.length && <p role="alert">System roles are unconfigured or unavailable. Update the daemon and retry. <button type="button" onClick={() => void query.refetch()}>Retry</button></p>}
    {query.data?.roles && <div className="swarm-agents-layout">
      <nav aria-label="System roles" className="swarm-agent-list">
        {orderedRoles.map((item) => {
          const assigned = assignmentFor(query.data!, item)
          return <button key={item.id} type="button" aria-pressed={role?.id === item.id} onClick={() => setSelected(item.id)} className="swarm-agent-role">
            <span className="swarm-agent-card-heading"><strong className="swarm-agent-name">{item.label}</strong><span aria-hidden="true" className="swarm-agent-indicator">{role?.id === item.id ? '●' : '○'}</span></span>
            <span className="swarm-agent-specialty">{guidanceFor(item).role}</span>
            <span className="swarm-agent-description">{guidanceFor(item).description}</span>
            <span className="swarm-agent-model">{assigned?.provider && assigned?.model ? `${assigned.provider} / ${assigned.model}` : 'Unconfigured'}</span>
          </button>
        })}
      </nav>
      {role && settings && <div className="swarm-agent-editor min-w-0 space-y-4">
        <div className="swarm-agent-overview">
          <span className="swarm-agent-eyebrow">{guidanceFor(role).role}</span>
          <h2>{role.label}</h2>
          <p>{guidanceFor(role).description}</p>
          <div className="swarm-agent-use-case"><h3>Best for</h3><p>{guidanceFor(role).bestFor}</p></div>
        </div>
        <div className="swarm-agent-model-heading"><h3>Model assignment</h3><p>Changes apply account-wide to future executions.</p></div>
        {role.slot === 'plan' && <p className="swarm-agent-plan-note">Orchestrator and Plan share this assignment. Changes affect both.</p>}
        <DirectModelEditor label={role.label} value={drafts[role.id] ?? assignmentFor(settings, role)} modelOptions={toFlatModelOptions(options.data ?? [])} disabled={Boolean(saving) || !options.data?.length} onChange={(value) => { setDrafts((previous) => ({ ...previous, [role.id]: value })); setStatuses((previous) => ({ ...previous, [role.id]: '' })) }} />
        <button type="button" disabled={Boolean(saving) || !options.data?.length} onClick={() => void save()} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-2">{saving === role.id ? 'Saving…' : `Save ${role.label}`}</button>
        {statuses[role.id] && <p role="status">{statuses[role.id]}</p>}
      </div>}
    </div>}
  </section>
}
