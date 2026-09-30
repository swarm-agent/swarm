import { useContext, useEffect, useState } from 'react'
import { QueryClientContext } from '@tanstack/react-query'
import { Cpu, Workflow } from 'lucide-react'
import { ModelPicker } from '../chat/components/model-picker'
import { displayModelName, modelProviderLabel } from '../chat/services/model-options'
import { fetchModelOptions } from '../chat/queries/chat-queries'
import type { ModelOptionRecord } from '../chat/types/chat'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { WorkerModelProfile, WorkerModelSelection } from '../state/desktop-workers-api'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'
import { agentModelSettingsQueryKey, getAgentModelSettings } from '../settings/swarm/queries/get-agent-model-settings'

export function workerSlotInherited(profile: WorkerModelProfile | null | undefined, slot: 'action' | 'plan') {
  return !profile || !!profile.use_account_default || !!profile[`${slot}_use_account_default`]
}
export function selectWorkerActionModel(profile: WorkerModelProfile | null | undefined, action: WorkerModelSelection): WorkerModelProfile {
  return { ...profile, source: 'temporary', use_account_default: false, action_use_account_default: false, plan_use_account_default: workerSlotInherited(profile, 'plan'), action }
}
export function resetWorkerModelSlot(profile: WorkerModelProfile | null | undefined, slot: 'action' | 'plan'): WorkerModelProfile {
  return { ...profile, source: 'temporary', action: profile?.action || { provider: '', model: '' }, use_account_default: false,
    action_use_account_default: slot === 'action' || workerSlotInherited(profile, 'action'),
    plan_use_account_default: slot === 'plan' || workerSlotInherited(profile, 'plan') }
}
export function selectWorkerModelSlot(profile: WorkerModelProfile | null | undefined, slot: 'action' | 'plan', value: WorkerModelSelection, defaultAction: WorkerModelSelection): WorkerModelProfile {
  return slot === 'action' ? selectWorkerActionModel(profile, value) : {
    ...profile, source: 'temporary', use_account_default: false, action: profile?.action || defaultAction,
    action_use_account_default: workerSlotInherited(profile, 'action'), plan_use_account_default: false, plan: value,
  }
}
// Re-selecting the current model must not reset a saved thinking/tier/context policy.
export function workerModelOptionSelection(option: ModelOptionRecord, current?: WorkerModelSelection | null): WorkerModelSelection {
  if (current?.provider === option.provider && current.model === option.model && (current.context_mode || '') === option.contextMode) return { ...current }
  return { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode }
}
const control = 'rounded-lg px-2.5 py-1.5 text-xs transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--app-text)] disabled:opacity-40'
const muted = 'text-[var(--app-text-muted)]'

/** Reads canonical account defaults/catalog; never writes account settings. */
export function WorkerModelPicker({ accountScopeId, profile, disabled, onChange, mode = 'auto' }: { accountScopeId: string; profile?: WorkerModelProfile | null; disabled: boolean; onChange: (profile: WorkerModelProfile) => void; mode?: 'auto' | 'plan' }) {
  const client = useContext(QueryClientContext)
  const [loaded, setLoaded] = useState<{ account: string; options: ModelOptionRecord[]; defaults: WorkerModelProfile }>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [editing, setEditing] = useState<'action' | 'plan' | null>(null)
  const authorized = getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId
  useEffect(() => {
    let alive = true
    setLoaded(undefined); setError(''); setEditing(null)
    if (!authorized) return
    const defaults = (settings: AgentModelSettings): WorkerModelProfile => {
      const selection = (value: typeof settings.swarm.action): WorkerModelSelection => ({ provider: value.provider, model: value.model, thinking: value.thinking, service_tier: value.serviceTier, context_mode: value.contextMode })
      return { source: 'swarm_settings', use_account_default: true, action: selection(settings.swarm.action), plan: selection(settings.swarm.plan) }
    }
    const valid = () => alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId
    let updatedSettings: AgentModelSettings | undefined
    void Promise.all([fetchModelOptions().catch(cause => {
      if (valid()) setError(cause instanceof Error ? cause.message : 'Model catalog unavailable')
      return [] as ModelOptionRecord[]
    }), getAgentModelSettings()]).then(([options, settings]) => {
      if (valid()) setLoaded({ account: accountScopeId, options, defaults: defaults(updatedSettings || settings) })
    }).catch(cause => { if (valid()) setError(cause instanceof Error ? cause.message : 'Models unavailable') })
    // Settings editors publish canonical query updates. Follow those without polling.
    const unsubscribe = client?.getQueryCache().subscribe(event => {
      if (event.type !== 'updated' || event.query.queryKey.length !== 1 || event.query.queryKey[0] !== agentModelSettingsQueryKey[0]) return
      const settings = event.query.state.data as AgentModelSettings | undefined
      if (settings && valid()) {
        updatedSettings = settings
        setLoaded(previous => previous ? { ...previous, defaults: defaults(settings) } : previous)
      }
    })
    return () => { alive = false; unsubscribe?.() }
  }, [accountScopeId, authorized, retry, client])
  const data = authorized && loaded?.account === accountScopeId ? loaded : undefined
  const options = data?.options || []
  const blocked = disabled || !authorized
  return <section aria-label="Worker models" className="space-y-3">
    <div className="grid gap-3" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 230px), 1fr))' }}>
      {(['action', 'plan'] as const).map(slot => {
        const label = slot === 'action' ? 'Action' : 'Plan'
        const Icon = slot === 'action' ? Cpu : Workflow
        const inherited = workerSlotInherited(profile, slot)
        const selection = inherited ? data?.defaults[slot] : profile?.[slot]
        const selected = options.find(option => option.provider === selection?.provider && option.model === selection?.model && option.contextMode === (selection?.context_mode || ''))
        const pin = (value: WorkerModelSelection) => { if (!blocked && data) onChange(selectWorkerModelSlot(profile, slot, value, data.defaults.action)) }
        return <section key={slot} aria-label={`${label} model`} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h4 className="flex items-center gap-2 text-sm font-semibold"><Icon size={15} aria-hidden="true" />{label}</h4>
            <div role="group" aria-label={`${label} model source`} title="Default follows account settings. Custom pins this worker only." className="flex rounded-lg border border-[var(--app-border)] p-0.5">
              <button type="button" aria-pressed={inherited} disabled={blocked || inherited} className={`${control} ${inherited ? 'bg-[var(--app-border)] font-semibold' : muted}`} onClick={() => onChange(resetWorkerModelSlot(profile, slot))}>Default</button>
              <button type="button" aria-pressed={!inherited} disabled={blocked || !selection || !data} className={`${control} ${!inherited ? 'bg-[var(--app-border)] font-semibold' : muted}`} onClick={() => { if (inherited && selection) pin({ ...selection }); setEditing(slot) }}>Custom</button>
            </div>
          </div>
          <p className={`mt-1 text-xs ${muted}`}>{slot === 'action' ? 'Does the work' : mode === 'plan' ? 'Plans first' : 'Optional · used in Plan mode'}</p>
          <div className="my-4 min-h-[64px]">
            <p className="break-words text-lg font-semibold leading-snug [overflow-wrap:anywhere]" title={selection?.model}>{selection ? displayModelName(selection.provider, selection.model, selection.context_mode || '') : !inherited ? 'Model not configured' : error ? 'Model unavailable' : 'Loading model…'}</p>
            <p className={`mt-1 break-words text-xs ${muted}`}>{selection ? modelProviderLabel(selection.provider) : 'Account default'}</p>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <button type="button" aria-label={`Edit ${label} thinking`} aria-expanded={editing === slot} disabled={blocked || !selected?.thinkingOptions.length} onClick={() => setEditing(editing === slot ? null : slot)} className={`${control} border border-[var(--app-border)]`}>Thinking · {selection?.thinking || 'Default'}</button>
            {selection?.service_tier && <span className={muted}>Tier · {selection.service_tier}</span>}
            {selection?.context_mode && <span className={muted}>Context · {selection.context_mode}</span>}
          </div>
          <div className="mt-4 flex flex-wrap items-center justify-between gap-2">
            <span className={`text-[11px] ${muted}`}>{inherited ? 'Follows account' : 'Pinned to worker'}</span>
            {!inherited && <button type="button" aria-label={`Reset ${label} to account default`} disabled={blocked} className={`${control} ${muted}`} onClick={() => onChange(resetWorkerModelSlot(profile, slot))}>Reset</button>
            <button type="button" aria-label={`${inherited ? 'Customize' : 'Change'} ${label} model`} aria-expanded={editing === slot} disabled={blocked || !options.length} className={`${control} border border-[var(--app-border)] hover:bg-[var(--app-border)]`} onClick={() => setEditing(editing === slot ? null : slot)}>{editing === slot ? 'Done' : inherited ? 'Customize' : 'Change'}</button>
          </div>
          {editing === slot && <div className="mt-3 space-y-3 border-t border-[var(--app-border)] pt-3" aria-label={`${label} model editor`}>
            <ModelPicker options={options} selectedKey={selected?.key || ''} disabled={blocked || !options.length} onSelect={key => { const option = options.find(item => item.key === key); if (option) pin(workerModelOptionSelection(option, selection)) }} />
            {!!selected?.thinkingOptions.length && selection && <label className={`flex flex-wrap items-center gap-2 text-xs ${muted}`}>Thinking
              <select aria-label={`${label} thinking`} value={selection.thinking || ''} disabled={blocked} className="min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2 text-[var(--app-text)]" onChange={event => pin({ ...selection, thinking: event.target.value })}>
                {!selected.thinkingOptions.includes(selection.thinking || '') && <option value={selection.thinking || ''}>{selection.thinking || 'Default'}</option>}
                {selected.thinkingOptions.map(value => <option key={value} value={value}>{value}</option>)}
              </select>
            </label>}
          </div>}
        </section>
      })}
    </div>
    {authorized && !data && !error && <p role="status" className={`text-xs ${muted}`}>Loading account models…</p>}
    {!authorized && <p role="alert" className={`text-xs ${muted}`}>Reconnect to this account to change models.</p>}
    {profile?.resolution_warning && <p role="alert">{profile.resolution_warning}</p>}
    {error && <div role="alert" className="text-xs"><span>{error}</span> <button type="button" disabled={blocked} className={`${control} underline`} onClick={() => setRetry(value => value + 1)}>Retry</button></div>}
  </section>
}
