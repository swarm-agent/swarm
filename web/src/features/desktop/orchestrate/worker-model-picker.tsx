import { useContext, useEffect, useRef, useState } from 'react'
import { QueryClientContext } from '@tanstack/react-query'
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
  if (current?.provider === option.provider && current.model === option.model) return { ...current }
  return { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode }
}
export function workerCatalogPrice(option: ModelOptionRecord): string {
  const price = option.pricing
  if (price?.is_free) return 'Catalog: free'
  const input = price?.input_price_per_million_tokens, output = price?.output_price_per_million_tokens
  return typeof input === 'number' && typeof output === 'number'
    ? `Catalog estimate: ${price?.currency || 'USD'} ${input} input / ${output} output per million tokens`
    : 'Catalog price unknown · not a billing quote'
}
const control = 'rounded-lg px-2.5 py-1.5 text-xs transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--app-text)] disabled:opacity-40'
const muted = 'text-[var(--app-text-muted)]'

/** Reads canonical account defaults/catalog; never writes account settings. */
export function WorkerModelPicker({ accountScopeId, profile, disabled, onChange, mode = 'auto' }: { accountScopeId: string; profile?: WorkerModelProfile | null; disabled: boolean; onChange: (profile: WorkerModelProfile) => void; mode?: 'auto' | 'plan' }) {
  const client = useContext(QueryClientContext)
  const triggers = useRef<Partial<Record<'action' | 'plan', HTMLButtonElement>>>({})
  const profileOwner = useRef({ account: accountScopeId, profile })
  if (profileOwner.current.profile !== profile) profileOwner.current = { account: accountScopeId, profile }
  const safeProfile = profileOwner.current.account === accountScopeId ? profile : undefined
  const [loaded, setLoaded] = useState<{ account: string; options: ModelOptionRecord[]; defaults: WorkerModelProfile }>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const [editing, setEditing] = useState<'action' | 'plan' | null>(null)
  const [search, setSearch] = useState('')
  const authorized = getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId
  useEffect(() => {
    let alive = true
    const controller = new AbortController()
    setLoaded(undefined); setError(''); setEditing(null); setSearch('')
    if (!authorized) return
    const defaults = (settings: AgentModelSettings): WorkerModelProfile => {
      const selection = (value: typeof settings.swarm.action): WorkerModelSelection => ({ provider: value.provider, model: value.model, thinking: value.thinking, service_tier: value.serviceTier, context_mode: value.contextMode })
      return { source: 'swarm_settings', use_account_default: true, action: selection(settings.swarm.action), plan: selection(settings.swarm.plan) }
    }
    const valid = () => alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId
    let updatedSettings: AgentModelSettings | undefined
    void Promise.all([fetchModelOptions(controller.signal).catch(cause => {
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
    return () => { alive = false; controller.abort(); unsubscribe?.() }
  }, [accountScopeId, authorized, retry, client])
  const data = authorized && loaded?.account === accountScopeId ? loaded : undefined
  const options = data?.options || []
  const blocked = disabled || !authorized
  const renderSlot = (slot: 'action' | 'plan') => {
    const label = slot === 'action' ? 'Execution' : 'Planning'
    const inherited = workerSlotInherited(safeProfile, slot)
    const selection = inherited ? data?.defaults[slot] : safeProfile?.[slot]
    const selected = options.find(option => option.provider === selection?.provider && option.model === selection?.model && option.contextMode === (selection?.context_mode || ''))
      || options.find(option => option.provider === selection?.provider && option.model === selection?.model && !option.contextMode)
    const pin = (value: WorkerModelSelection) => { if (!blocked && data && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) onChange(selectWorkerModelSlot(safeProfile, slot, value, data.defaults.action)) }
    const close = () => { setEditing(null); setSearch(''); triggers.current[slot]?.focus() }
    return <section aria-label={`${label} model`} className="min-w-0 space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <strong>{label} model</strong><span title={selection?.model} className="min-w-0 flex-1 break-words">{selection ? displayModelName(selection.provider, selection.model, selection.context_mode || '') : error ? 'Unavailable' : data ? 'Not configured' : 'Loading…'} · {inherited ? 'Account default' : 'Worker override'}</span>
        <button ref={node => { if (node) triggers.current[slot] = node }} type="button" className={`${control} border border-[var(--app-border)]`} disabled={blocked || !data} aria-expanded={editing === slot} aria-label={`Change ${label} model`} onClick={() => { if (editing === slot) close(); else { setEditing(slot); setSearch('') } }}>Choose</button>
      </div>
      {editing === slot && <div aria-label={`${label} model chooser`} className="space-y-2 rounded-lg border border-[var(--app-border)] p-3" onKeyDown={event => {
          if (event.key === 'Escape') { event.preventDefault(); close(); return }
          if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
          if (event.target instanceof HTMLInputElement && !['ArrowDown', 'ArrowUp'].includes(event.key)) return
          const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>('button[data-model-option]:not(:disabled)')]
          if (!buttons.length) return
          event.preventDefault()
          const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
          const index = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : event.key === 'ArrowDown' ? (current + 1) % buttons.length : (current < 0 ? buttons.length - 1 : (current + buttons.length - 1) % buttons.length)
          buttons[index].focus()
        }}>
        <label className="block text-xs">Search provider or model<input autoFocus aria-label={`Search ${label} models`} value={search} onChange={event => setSearch(event.target.value)} className="mt-1 w-full min-w-0 rounded border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2" /></label>
        <button data-model-option type="button" className={`${control} w-full text-left`} disabled={blocked} aria-pressed={inherited} onClick={() => { if (getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) onChange(resetWorkerModelSlot(safeProfile, slot)); close() }}>Account default · follows account settings</button>
        <div className="max-h-64 space-y-1 overflow-y-auto" role="group" aria-label={`${label} catalog models`}>
          {options.filter(option => `${option.provider} ${option.model} ${option.label}`.toLowerCase().includes(search.toLowerCase().trim())).map(option => <button data-model-option type="button" key={option.key} disabled={blocked} aria-pressed={!inherited && selected?.key === option.key} className={`${control} block w-full text-left hover:bg-[var(--app-border)]`} onClick={() => { pin(workerModelOptionSelection(option, selection)); close() }}>
            <span className="block break-words">{option.label} · {modelProviderLabel(option.provider)}</span><span className={`block ${muted}`}>{workerCatalogPrice(option)}</span>
          </button>)}
          {!options.length && <p>No catalog models available.</p>}
          {!!options.length && !options.some(option => `${option.provider} ${option.model} ${option.label}`.toLowerCase().includes(search.toLowerCase().trim())) && <p>No matching models.</p>}
        </div>
        <button type="button" className={control} onClick={close}>Close chooser</button>
      </div>}
      {selection && <p className={`text-xs ${muted}`}>Thinking · {selection.thinking || 'Default'}{selection.service_tier ? ` · Tier · ${selection.service_tier}` : ''}{selection.context_mode ? ` · Context · ${selection.context_mode}` : ''}</p>}
      {selected && selection && <details className="text-xs"><summary className={`cursor-pointer ${muted}`}>Advanced · thinking, tier and context</summary>
        <div className="mt-2 flex flex-wrap gap-3">
          {!!selected.thinkingOptions.length && <label>Thinking<select aria-label={`${label} thinking`} disabled={blocked} value={selection.thinking || ''} onChange={e => pin({ ...selection, thinking: e.target.value })}><option value="">Default</option>{selection.thinking && !selected.thinkingOptions.includes(selection.thinking) && <option value={selection.thinking}>Saved · {selection.thinking} (unsupported)</option>}{selected.thinkingOptions.map(value => <option key={value}>{value}</option>)}</select></label>}
          {!!selected.serviceTiers.length && <label>Tier<select aria-label={`${label} tier`} disabled={blocked} value={selection.service_tier || ''} onChange={e => pin({ ...selection, service_tier: e.target.value })}><option value="">Default</option>{selection.service_tier && !selected.serviceTiers.includes(selection.service_tier) && <option value={selection.service_tier}>Saved · {selection.service_tier} (unsupported)</option>}{selected.serviceTiers.map(value => <option key={value}>{value}</option>)}</select></label>}
          {!!selected.contextModes.length && <label>Context<select aria-label={`${label} context`} disabled={blocked} value={selection.context_mode || ''} onChange={e => { const option = options.find(item => item.provider === selected.provider && item.model === selected.model && item.contextMode === e.target.value); if (option) pin({ ...workerModelOptionSelection(option, selection), context_mode: option.contextMode }) }}><option value="">Default</option>{selection.context_mode && !selected.contextModes.some(value => value.mode === selection.context_mode) && <option value={selection.context_mode}>Saved · {selection.context_mode} (unsupported)</option>}{selected.contextModes.filter(value => !value.default).map(value => <option key={value.mode} value={value.mode}>{value.label || value.mode}</option>)}</select></label>}
        </div>
      </details>}
    </section>
  }
  return <section aria-label="Worker models" className="min-w-0 space-y-3">
    {renderSlot('action')}
    <details open={mode === 'plan' ? true : undefined}><summary className={`cursor-pointer text-xs ${muted}`}>Planning model · {mode === 'plan' ? 'plans first' : 'only used in Plan mode'}</summary><div className="mt-2">{renderSlot('plan')}</div></details>
    <p className={`text-xs ${muted}`}>Worker-only draft. Review and save below; admitted runs retain their captured model.</p>
    {authorized && !data && !error && <p role="status" className={`text-xs ${muted}`}>Loading account models…</p>}
    {!authorized && <p role="alert" className={`text-xs ${muted}`}>Reconnect to this account to change models.</p>}
    {safeProfile?.resolution_warning && <p role="alert">{safeProfile.resolution_warning}</p>}
    {error && <div role="alert" className="text-xs"><span>{error}</span> <button type="button" disabled={blocked} className={`${control} underline`} onClick={() => setRetry(value => value + 1)}>Retry</button></div>}
  </section>
}
