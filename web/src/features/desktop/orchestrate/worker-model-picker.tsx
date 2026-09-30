import { useEffect, useState } from 'react'
import { ModelPicker } from '../chat/components/model-picker'
import { fetchModelOptions } from '../chat/queries/chat-queries'
import type { ModelOptionRecord } from '../chat/types/chat'
import { getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { WorkerModelProfile } from '../state/desktop-workers-api'

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
    <p>Model: {profile ? `${profile.action.provider}/${profile.action.model}${profile.action.thinking ? ` · ${profile.action.thinking}` : ''}` : 'Not initialized; canonical default will be saved on acceptance'}{profile?.plan && JSON.stringify(profile.plan) !== JSON.stringify(profile.action) ? ` · Planning: ${profile.plan.provider}/${profile.plan.model}` : ''}</p>
    <ModelPicker options={options} selectedKey={selected?.key || ''} disabled={disabled || !options.length} onSelect={key => {
      const option = options.find(item => item.key === key)
      if (!option) return
      const selection = { provider: option.provider, model: option.model, thinking: option.defaultThinking, service_tier: option.defaultServiceTier, context_mode: option.contextMode }
      onChange({ source: 'temporary', action: selection, plan: { ...selection } })
    }} />
    <p className="text-slate-400">A changed model applies to both planning and action for this worker only.</p>
    {profile?.resolution_warning && <p role="alert">{profile.resolution_warning}</p>}
    {error && <p role="alert">{error}</p>}
  </section>
}
