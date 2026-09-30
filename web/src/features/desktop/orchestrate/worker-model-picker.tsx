import { useEffect, useState } from 'react'
import { ModelPicker } from '../chat/components/model-picker'
import { fetchModelOptions } from '../chat/queries/chat-queries'
import type { ModelOptionRecord } from '../chat/types/chat'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { WorkerModelProfile } from '../state/desktop-workers-api'

export function selectWorkerActionModel(profile: WorkerModelProfile | null | undefined, action: WorkerModelProfile['action']): WorkerModelProfile {
  return { ...profile, source: 'temporary', use_account_default: false, action }
}

/** Read-only catalog access; selections are draft state until explicit acceptance. */
export function WorkerModelPicker({ accountScopeId, profile, disabled, onChange }: { accountScopeId: string; profile?: WorkerModelProfile | null; disabled: boolean; onChange: (profile: WorkerModelProfile) => void }) {
  const [options, setOptions] = useState<ModelOptionRecord[]>([])
  const [error, setError] = useState('')
  useEffect(() => {
    let alive = true
    setOptions([])
    setError('')
    if (getDesktopSessionIdentitySnapshot()?.accountScopeId !== accountScopeId) return
    void fetchModelOptions().then(result => {
      if (alive && getDesktopSessionIdentitySnapshot()?.accountScopeId === accountScopeId) setOptions(result)
    }).catch(cause => { if (alive) setError(cause instanceof Error ? cause.message : 'Model catalog unavailable') })
    return () => { alive = false }
  }, [accountScopeId])
  const selected = options.find(option => option.provider === profile?.action.provider && option.model === profile?.action.model && option.contextMode === (profile?.action.context_mode || ''))
  return <section aria-label="Worker model" className="space-y-1">
    <p>Action model: {profile ? `${profile.action.provider}/${profile.action.model}${profile.action.thinking ? ` · ${profile.action.thinking}` : ''}` : 'Swarm Default / account action settings; resolved on acceptance'}{profile?.plan && JSON.stringify(profile.plan) !== JSON.stringify(profile.action) ? ` · Planning: ${profile.plan.provider}/${profile.plan.model}` : ''}</p>
    <ModelPicker options={options} selectedKey={selected?.key || ''} disabled={disabled || !options.length} onSelect={key => {
      const option = options.find(item => item.key === key)
      if (!option) return
      const selection = { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode }
      onChange(selectWorkerActionModel(profile, selection))
    }} />
    {profile && <><p>Optional Plan model (used only in explicit Plan mode)</p><ModelPicker options={options} selectedKey={options.find(option => option.provider === profile.plan?.provider && option.model === profile.plan?.model && option.contextMode === (profile.plan?.context_mode || ''))?.key || ''} disabled={disabled || !options.length} onSelect={key => {
      const option = options.find(item => item.key === key)
      if (option) onChange({ ...profile, source: 'temporary', use_account_default: false, plan: { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode } })
    }} /></>}
    {profile && <p>Model source: {profile.source === 'swarm_settings' || profile.use_account_default ? 'Swarm Default / account action settings' : 'Worker-specific selection'}</p>}
    <p className="text-slate-400">Action model only; the optional Plan model is preserved. Swarm is the default execution mode.</p>
    {profile?.resolution_warning && <p role="alert">{profile.resolution_warning}</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
