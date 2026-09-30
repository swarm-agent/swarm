import { useEffect, useState } from 'react'
import { ModelPicker } from '../chat/components/model-picker'
import { fetchModelOptions } from '../chat/queries/chat-queries'
import type { ModelOptionRecord } from '../chat/types/chat'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { WorkerModelProfile, WorkerModelSelection } from '../state/desktop-workers-api'
import { getAgentModelSettings } from '../settings/swarm/queries/get-agent-model-settings'

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

/** Read-only account defaults/catalog; edits remain drafts until separate acceptance. */
export function WorkerModelPicker({ accountScopeId, profile, disabled, onChange }: { accountScopeId: string; profile?: WorkerModelProfile | null; disabled: boolean; onChange: (profile: WorkerModelProfile) => void }) {
  const [loaded, setLoaded] = useState<{ account: string; options: ModelOptionRecord[]; defaults: WorkerModelProfile }>()
  const [error, setError] = useState('')
  useEffect(() => {
    let alive = true
    setLoaded(undefined); setError('')
    if (getDesktopSessionIdentitySnapshot()?.accountScopeId !== accountScopeId) return
    void Promise.all([fetchModelOptions().catch(cause => {
      if (alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) setError(cause instanceof Error ? cause.message : 'Model catalog unavailable')
      return [] as ModelOptionRecord[]
    }), getAgentModelSettings()]).then(([options, settings]) => {
      const selection = (value: typeof settings.swarm.action): WorkerModelSelection => ({ provider: value.provider, model: value.model, thinking: value.thinking, service_tier: value.serviceTier, context_mode: value.contextMode })
      if (alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) setLoaded({ account: accountScopeId, options, defaults: { source: 'swarm_settings', use_account_default: true, action: selection(settings.swarm.action), plan: selection(settings.swarm.plan) } })
    }).catch(cause => { if (alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) setError(cause instanceof Error ? cause.message : 'Account model defaults unavailable') })
    return () => { alive = false }
  }, [accountScopeId])
  const data = loaded?.account === accountScopeId ? loaded : undefined
  const options = data?.options || []
  return <section aria-label="Worker models" className="space-y-3">
    {(['action', 'plan'] as const).map(slot => {
      const inherited = workerSlotInherited(profile, slot)
      const selection = inherited ? data?.defaults[slot] : profile?.[slot]
      const selected = options.find(option => option.provider === selection?.provider && option.model === selection?.model && option.contextMode === (selection?.context_mode || ''))
      return <section key={slot} aria-label={`${slot === 'action' ? 'Action' : 'Plan'} model`} className="space-y-1">
        <h4>{slot === 'action' ? 'Action' : 'Plan'} model</h4>
        <p>{inherited ? 'Account default (follows future changes)' : 'Explicit worker override'}: {selection ? `${selection.provider}/${selection.model}${selection.thinking ? ` · ${selection.thinking}` : ''}` : 'Loading account default…'}</p>
        <ModelPicker options={options} selectedKey={selected?.key || ''} disabled={disabled || !options.length} onSelect={key => {
          const option = options.find(item => item.key === key)
          if (!option) return
          const value = { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode }
          onChange(slot === 'action' ? selectWorkerActionModel(profile, value) : { ...profile, source: 'temporary', use_account_default: false, action: profile?.action || data!.defaults.action, action_use_account_default: workerSlotInherited(profile, 'action'), plan_use_account_default: false, plan: value })
        }} />
        <button type="button" disabled={disabled || inherited} onClick={() => onChange(resetWorkerModelSlot(profile, slot))}>Use account default for {slot === 'action' ? 'Action' : 'Plan'}</button>
      </section>
    })}
    <p>Plan is used only in explicit Plan mode. Overrides and resets affect this worker only after acceptance; account settings and admitted jobs are unchanged.</p>
    {profile?.resolution_warning && <p role="alert">{profile.resolution_warning}</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
