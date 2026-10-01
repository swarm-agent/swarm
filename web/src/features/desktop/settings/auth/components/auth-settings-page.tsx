import { useEffect, useMemo, useState, useCallback, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import type { ChangeEvent } from 'react'
import { queryClient } from '../../../../../app/query-client'
import { agentStateQueryOptions, draftModelQueryOptions, modelOptionsQueryOptions } from '../../../../queries/query-options'
import type { AgentProfileRecord } from '../../../../desktop/chat/types/chat'
import { Badge } from '../../../../../components/ui/badge'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { Plus, Check, LogIn, Trash2, Key, ChevronDown, Copy } from 'lucide-react'
import { Textarea } from '../../../../../components/ui/textarea'
import { cn } from '../../../../../lib/cn'
import { completeCodexOAuth } from '../../mutations/complete-codex-oauth'
import { deleteAuthCredential } from '../../mutations/delete-auth-credential'
import { setActiveAuthCredential } from '../../mutations/set-active-auth-credential'
import { startCodexOAuth } from '../../mutations/start-codex-oauth'
import { upsertAuthCredential } from '../../mutations/upsert-auth-credential'
import { verifyAuthCredential } from '../../mutations/verify-auth-credential'
import { getCodexOAuthStatus } from '../../queries/get-codex-oauth-status'
import { listAuthCredentials } from '../../queries/list-auth-credentials'
import { listScopedTokens } from '../../queries/list-scoped-tokens'
import { revokeScopedToken } from '../../mutations/revoke-scoped-token'
import { createScopedToken } from '../../mutations/create-scoped-token'
import { listProviders } from '../../queries/list-providers'
import type { AuthCredential, AuthMethod, CodexOAuthSession, ProviderStatus, StartCodexOAuthInput, UpsertAuthCredentialInput } from '../../types/auth'
import { createPortal } from 'react-dom'
import { CodexDeviceCode } from './codex-device-code'
import { codexSetupRecommendation } from '../codex-setup-recommendation'

type OAuthIntent = StartCodexOAuthInput['method']

const fallbackMethod: AuthMethod = {
  id: 'api',
  label: 'API key',
  credentialType: 'api',
  description: '',
}

const authButtonAccentClass = 'border border-[var(--app-primary)] bg-transparent text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)] hover:border-[var(--app-primary)] active:bg-[var(--app-surface-hover)]'
const authButtonNeutralClass = 'border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'

const authNoticeClass = 'rounded-xl border px-3 py-2 text-sm'
const authDangerNoticeClass = `${authNoticeClass} border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]`

function methodKey(method: AuthMethod): string {
  return `${method.id}:${method.credentialType}`
}

function formatLabel(record: AuthCredential): string {
  return record.label || record.id || '—'
}

function hasCustomLabel(record: AuthCredential): boolean {
  const label = record.label.trim()
  const id = record.id.trim()
  return label !== '' && label.localeCompare(id, undefined, { sensitivity: 'accent' }) !== 0
}

function formatCredentialTitle(record: AuthCredential): string {
  if (hasCustomLabel(record)) {
    return record.label.trim()
  }
  return record.provider || formatLabel(record)
}

function confirmMakeActive(credential: AuthCredential): boolean {
  if (typeof window === 'undefined') {
    return true
  }

  return window.confirm(`Make "${formatLabel(credential)}" the active credential for ${credential.provider}?`)
}

function buildDeleteCredentialWarning(credential: AuthCredential, agentProfiles: AgentProfileRecord[]): string {
  const affectedAgents = agentProfiles
    .filter((profile) => String(profile.provider ?? '').trim().toLowerCase() === credential.provider.trim().toLowerCase())
    .map((profile) => profile.name.trim())
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right))

  const lines = [`Delete "${formatLabel(credential)}" for ${credential.provider}?`]
  if (affectedAgents.length > 0) {
    lines.push('')
    lines.push(`This will reset these agents to Inherit:`)
    for (const name of affectedAgents) {
      lines.push(`- ${name}`)
    }
    lines.push('')
    lines.push('To reassign them, go to /agents.')
  }
  lines.push('')
  lines.push('If this was the last usable auth for that provider, the default model may also be cleared.')
  return lines.join('\n')
}

function sortProviders(records: ProviderStatus[]): ProviderStatus[] {
  return [...records].sort((a, b) => a.id.localeCompare(b.id))
}

function sortCredentials(records: AuthCredential[]): AuthCredential[] {
  return [...records].sort((a, b) => {
    const providerOrder = a.provider.localeCompare(b.provider)
    if (providerOrder !== 0) {
      return providerOrder
    }
    const labelOrder = formatLabel(a).localeCompare(formatLabel(b))
    if (labelOrder !== 0) {
      return labelOrder
    }
    return a.id.localeCompare(b.id)
  })
}

// --- Custom Glassmorphic Dropdown (Half Width) ---
interface ModernSelectProps {
  value: string
  options: { id: string; label?: string }[]
  onChange: (value: string) => void
  placeholder?: string
}

async function refreshAuthDependentQueries(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: draftModelQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: modelOptionsQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: ['auth-credentials'] }),
  ])
}

function ModernSelect({ value, options, onChange, placeholder }: ModernSelectProps) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const dropdownRef = useRef<HTMLDivElement | null>(null)
  const [position, setPosition] = useState<{ top: number; left: number; width: number } | null>(null)

  const selectedOption = options.find(o => o.id === value)

  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return
    const rect = triggerRef.current.getBoundingClientRect()
    setPosition({
      top: rect.bottom + 8,
      left: rect.left,
      width: rect.width
    })
  }, [])

  useEffect(() => {
    if (open) {
      updatePosition()
      window.addEventListener('resize', updatePosition)
      window.addEventListener('scroll', updatePosition, true)
    }
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [open, updatePosition])

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node) && !triggerRef.current?.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  return (
    <>
      <button ref={triggerRef} onClick={() => setOpen(!open)} className="flex items-center justify-between w-full h-11 px-4 text-sm transition-all outline-none bg-[var(--app-surface)] border border-[var(--app-border)] text-[var(--app-text)] rounded-xl hover:border-[var(--app-border-strong)]">
        <span className="truncate">{selectedOption?.label || selectedOption?.id || placeholder}</span>
        <ChevronDown size={16} className={cn("transition-transform duration-200 text-[var(--app-text-muted)]", open && "rotate-180")} />
      </button>
      {open && position && createPortal(
        <div ref={dropdownRef} className="fixed z-[9999] overflow-hidden flex flex-col transition-all duration-200 animate-in fade-in zoom-in-95 bg-[var(--app-surface-elevated)] border border-[var(--app-border-strong)] rounded-2xl shadow-[var(--shadow-panel)]" style={{ top: position.top, left: position.left, width: position.width }}>
          <div className="max-h-[300px] overflow-y-auto py-1">
            {options.map(opt => (
              <button key={opt.id} onClick={() => { onChange(opt.id); setOpen(false) }} className="flex items-center w-full px-4 py-3 text-sm transition-colors text-left text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]">
                {opt.label || opt.id}
                {opt.id === value && <Check size={14} className="ml-auto text-[var(--app-primary)]" />}
              </button>
            ))}
          </div>
        </div>,
        document.body
      )}
    </>
  )
}

export function AuthSettingsPage() {
  const [providers, setProviders] = useState<ProviderStatus[]>([])
  const [credentials, setCredentials] = useState<AuthCredential[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [busyCredentialID, setBusyCredentialID] = useState<string | null>(null)

  const [composerOpen, setComposerOpen] = useState(false)
  const [providerID, setProviderID] = useState('')
  const [selectedMethodKey, setSelectedMethodKey] = useState('')
  const [label, setLabel] = useState('')
  const [apiKey, setAPIKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [oauthIntent, setOAuthIntent] = useState<OAuthIntent>('device')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)
  const [callbackInput, setCallbackInput] = useState('')
  const { data: agentState } = useQuery(agentStateQueryOptions())

  const load = async () => {
    setLoading(true)
    setError(null)

    try {
      const [providerRecords, credentialRecords] = await Promise.all([listProviders(), listAuthCredentials('', '', 200)])
      const sortedProviders = sortProviders(providerRecords)
      const sortedCredentials = sortCredentials(credentialRecords.records)
      setProviders(sortedProviders)
      setCredentials(sortedCredentials)
      setProviderID((current) => current || sortedProviders[0]?.id || '')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load auth settings')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const providerOptions = useMemo(() => sortProviders(providers), [providers])
  const selectedProvider = useMemo(
    () => providerOptions.find((provider) => provider.id === providerID) ?? providerOptions[0] ?? null,
    [providerID, providerOptions],
  )
  const availableMethods = useMemo(() => {
    if (!selectedProvider) {
      return [fallbackMethod]
    }
    return selectedProvider.authMethods.length > 0 ? selectedProvider.authMethods : [fallbackMethod]
  }, [selectedProvider])
  const selectedMethod = useMemo(
    () => availableMethods.find((method) => methodKey(method) === selectedMethodKey) ?? availableMethods[0] ?? fallbackMethod,
    [availableMethods, selectedMethodKey],
  )
  const sortedCredentials = useMemo(() => sortCredentials(credentials), [credentials])
  const recommendedCodexSetup = codexSetupRecommendation()

  useEffect(() => {
    if (!selectedProvider && providerOptions.length > 0) {
      setProviderID(providerOptions[0].id)
    }
  }, [providerOptions, selectedProvider])

  useEffect(() => {
    const next = availableMethods[0]
    if (!next) {
      setSelectedMethodKey('')
      return
    }
    if (!availableMethods.some((method) => methodKey(method) === selectedMethodKey)) {
      setSelectedMethodKey(methodKey(next))
    }
  }, [availableMethods, selectedMethodKey])

  useEffect(() => {
    if (!composerOpen || (oauthIntent !== 'browser' && oauthIntent !== 'device') || !oauthSession?.sessionID) {
      return
    }
    if (oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    const timer = window.setInterval(() => {
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then(async (next) => {
          setOAuthSession(next)
          if (next.status === 'error') {
            setFormError(next.error || 'Codex sign-in failed.')
          }
          if (next.status === 'success') {
            setStatus('Credential is now active.')
            setComposerOpen(false)
            setFormError(null)
            setCallbackInput('')
            setAPIKey('')
            setLabel('')
            await load()
            await refreshAuthDependentQueries()
          }
        })
        .catch((err) => {
          setFormError(err instanceof Error ? err.message : 'Failed to refresh OAuth status')
        })
    }, 1500)

    return () => {
      window.clearInterval(timer)
    }
  }, [composerOpen, oauthIntent, oauthSession])

  const resetComposer = () => {
    setComposerOpen(false)
    setLabel('')
    setAPIKey('')
    setCallbackInput('')
    setOAuthSession(null)
    setFormError(null)
    setSaving(false)
  }

  const openComposer = () => {
    setStatus(null)
    setFormError(null)
    setComposerOpen(true)
    setProviderID((current) => current || providerOptions[0]?.id || '')
  }

  const runRowAction = async (credential: AuthCredential, action: () => Promise<string>, doneMessage: string) => {
    setBusyCredentialID(credential.id)
    setError(null)
    setStatus(null)
    try {
      const nextMessage = await action()
      await load()
      await refreshAuthDependentQueries()
      setStatus(nextMessage || doneMessage)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Auth action failed')
    } finally {
      setBusyCredentialID(null)
    }
  }

  const saveManualCredential = async () => {
    if (!selectedProvider) {
      setFormError('Choose a provider first.')
      return
    }

    const payload: UpsertAuthCredentialInput = {
      provider: selectedProvider.id,
      type: selectedMethod.credentialType,
      label: label.trim(),
      active: true,
    }

    if (selectedMethod.credentialType === 'api') {
      if (!apiKey.trim()) {
        setFormError('API key is required.')
        return
      }
      payload.api_key = apiKey.trim()
    }

    setSaving(true)
    setFormError(null)
    setStatus(null)
    try {
      const saved = await upsertAuthCredential(payload)
      await load()
      const verification = await verifyAuthCredential({ provider: saved.provider, id: saved.id })
      if (!verification.connected) {
        setFormError(verification.message || 'Credential saved, but verification failed.')
        return
      }
      await refreshAuthDependentQueries()
      resetComposer()
      setStatus(hasCustomLabel(saved) ? `Credential "${saved.label.trim()}" is active.` : 'Credential is active.')
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Failed to save credential')
    } finally {
      setSaving(false)
    }
  }

  const startOAuth = async (intent: OAuthIntent) => {
    if (!selectedProvider) {
      setFormError('Choose a provider first.')
      return
    }

    const payload: StartCodexOAuthInput = {
      provider: selectedProvider.id,
      label: label.trim(),
      active: true,
      method: intent,
    }

    setSaving(true)
    setFormError(null)
    setStatus(null)
    setOAuthIntent(intent)
    setOAuthSession(null)
    try {
      const session = await startCodexOAuth(payload)
      setOAuthSession(session)
      if (intent === 'browser' && session.authURL && typeof window !== 'undefined') {
        window.open(session.authURL, '_blank', 'noopener,noreferrer')
      }
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Failed to start OAuth')
    } finally {
      setSaving(false)
    }
  }

  const finishOAuth = async () => {
    if (!oauthSession?.sessionID) {
      setFormError('Start login first.')
      return
    }
    if (!callbackInput.trim()) {
      setFormError('Paste the callback URL, query string, or code.')
      return
    }

    setSaving(true)
    setFormError(null)
    try {
      const session = await completeCodexOAuth({
        session_id: oauthSession.sessionID,
        callback_input: callbackInput.trim(),
      })
      setOAuthSession(session)
      if (session.status !== 'success') {
        setFormError(session.error || 'OAuth completion did not succeed.')
        return
      }
      await load()
      await refreshAuthDependentQueries()
      resetComposer()
      setStatus('Credential added and selected.')
    } catch (err) {
      setFormError(err instanceof Error ? err.message : 'Failed to complete OAuth')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-8 flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Vault Credentials</h1>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Securely managed provider credentials. Adding and activating an Exa API key opts in to Exa-hosted web search and page retrieval: queries and selected URLs are sent to Exa, results return to the agent/model context, and your browser profile is not used.</p>
        </div>
        <div className="flex items-center gap-2 w-full sm:w-auto">
          <div className="w-full sm:w-36">
            <ModernSelect 
              value={providerID} 
              options={providerOptions.map(p => ({ id: p.id }))} 
              onChange={setProviderID} 
              placeholder="Select Provider"
            />
          </div>
          <Button 
            className="h-11 border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)] hover:bg-[var(--app-surface-subtle)]"
            onClick={composerOpen ? resetComposer : openComposer} 
            disabled={loading || providerOptions.length === 0}
          >
            {composerOpen ? 'Cancel' : <Plus size={18} />}
          </Button>
        </div>
      </div>

      <div className="space-y-6">
        {error ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
        {status ? <div className="rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-sm text-[var(--app-success)]">{status}</div> : null}

        {composerOpen && selectedProvider ? (
          <div className="grid gap-4 p-5 transition-all duration-300 rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)]">
            <div className="grid gap-4 md:grid-cols-[minmax(0,140px)_minmax(0,1fr)]">
              <label className="grid gap-2">
                <span className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Provider</span>
                <ModernSelect 
                  value={selectedProvider.id} 
                  options={providerOptions.map(p => ({ id: p.id }))} 
                  onChange={setProviderID} 
                />
              </label>
              {availableMethods.length > 1 ? (
                <label className="grid gap-2">
                  <span className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Method</span>
                  <ModernSelect 
                    value={methodKey(selectedMethod)} 
                    options={availableMethods.map(m => ({ id: methodKey(m), label: m.label }))} 
                    onChange={setSelectedMethodKey} 
                  />
                </label>
              ) : (
                <div className="grid gap-2">
                  <span className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Method</span>
                  <div className="flex h-11 items-center px-4 text-sm bg-[var(--app-surface)] border border-[var(--app-border)] text-[var(--app-text)] rounded-xl">
                    {selectedMethod.label}
                  </div>
                </div>
              )}
            </div>

            <label className="grid gap-2">
              <span className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">Label</span>
              <Input 
                value={label} 
                onChange={(event: ChangeEvent<HTMLInputElement>) => setLabel(event.target.value)} 
                placeholder={`${selectedProvider.id} credential`} 
                autoComplete="off" 
                className="bg-[var(--app-surface)] border-[var(--app-border)] text-[var(--app-text)]"
              />
            </label>

            {selectedMethod.description ? <p className="m-0 text-sm text-[var(--app-text-muted)] italic">{selectedMethod.description}</p> : null}

            {selectedMethod.credentialType === 'api' ? (
              <label className="grid gap-2">
                <span className="text-xs font-bold uppercase tracking-widest text-[var(--app-text-muted)]">API key</span>
                <Input 
                  value={apiKey} 
                  onChange={(event: ChangeEvent<HTMLInputElement>) => setAPIKey(event.target.value)} 
                  type="password" 
                  autoComplete="off" 
                  className="bg-[var(--app-surface)] border-[var(--app-border)] text-[var(--app-text)]"
                />
              </label>
            ) : null}

            {selectedMethod.credentialType === 'oauth' ? (
              <div className="grid gap-3 p-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
                <div className="grid gap-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <Button className={recommendedCodexSetup === 'browser' ? authButtonAccentClass : authButtonNeutralClass} onClick={() => void startOAuth('browser')} disabled={saving}>
                      <LogIn size={16} className="mr-2" /> Local Setup
                    </Button>
                    {recommendedCodexSetup === 'browser' ? <Badge tone="live">Recommended on this device</Badge> : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button className={recommendedCodexSetup === 'device' ? authButtonAccentClass : authButtonNeutralClass} onClick={() => void startOAuth('device')} disabled={saving}>
                      Sign in with device code
                    </Button>
                    {recommendedCodexSetup === 'device' ? <Badge tone="live">Recommended for remote setup</Badge> : null}
                  </div>
                  <p className="text-sm text-[var(--app-text-muted)]">Device code uses a short one-time code without a localhost callback. Device sign-in will not silently switch methods if policy disables it.</p>
                  <div className="flex flex-wrap gap-2">
                    <Button className={authButtonNeutralClass} onClick={() => void startOAuth('manual')} disabled={saving}>
                      <Key size={16} className="mr-2" /> Manual callback fallback
                    </Button>
                  </div>
                </div>

                {oauthSession ? (
                  <>
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={oauthSession.status === 'success' ? 'live' : oauthSession.status === 'error' ? 'danger' : 'warning'}>
                        {oauthSession.status || 'waiting'}
                      </Badge>
                      {oauthSession.error ? <span className="text-sm text-[var(--app-danger)]">{oauthSession.error}</span> : null}
                    </div>
                    {oauthIntent === 'device' ? <CodexDeviceCode session={oauthSession} disabled={saving} /> : null}
                    {oauthIntent !== 'device' && oauthSession.authURL ? (
                      <label className="grid gap-2">
                        <span className="text-sm font-medium text-[var(--app-text)]">Auth URL</span>
                        <Textarea value={oauthSession.authURL} readOnly className="min-h-[96px] bg-[var(--app-surface-subtle)]" />
                      </label>
                    ) : null}
                    {oauthIntent === 'manual' ? (
                      <>
                        <label className="grid gap-2">
                          <span className="text-sm font-medium text-[var(--app-text)]">Callback input</span>
                          <Textarea value={callbackInput} onChange={(event: ChangeEvent<HTMLTextAreaElement>) => setCallbackInput(event.target.value)} placeholder="Paste the callback URL, query string, or authorization code" className="bg-[var(--app-surface-subtle)]" />
                        </label>
                        <div>
                          <Button className={authButtonAccentClass} onClick={() => void finishOAuth()} disabled={saving || !oauthSession.sessionID}>
                            <Check size={16} className="mr-2" /> {saving ? 'Waiting…' : 'Complete login'}
                          </Button>
                        </div>
                      </>
                    ) : null}
                  </>
                ) : null}
              </div>
            ) : null}

            {selectedMethod.credentialType !== 'oauth' ? (
              <div className="pt-2">
                <Button 
                  className={authButtonAccentClass} 
                  onClick={() => void saveManualCredential()} 
                  disabled={saving}
                >
                  <Check size={18} className="mr-2" /> {saving ? 'Saving…' : 'Save and make active'}
                </Button>
              </div>
            ) : null}

            {formError ? <p className={authDangerNoticeClass}>{formError}</p> : null}
          </div>
        ) : null}

        {loading ? <p className="text-sm text-[var(--app-text-muted)] animate-pulse">Loading auth settings…</p> : null}

        {!loading && (
          <div className="overflow-hidden rounded-xl border border-[var(--app-border)] transition-colors duration-300">
            <div className="px-4 py-2 bg-[var(--app-surface-subtle)] border-b border-[var(--app-border)] text-xs font-medium text-[var(--app-text-muted)] uppercase tracking-wider">
              Saved Credentials
            </div>
            
            {sortedCredentials.length === 0 ? (
              <div className="px-4 py-12 text-center text-[var(--app-text-muted)] bg-[var(--app-bg)]">
                <div className="flex flex-col items-center gap-2 opacity-50">
                  <Key size={32} />
                  <span className="font-medium">No credentials saved yet.</span>
                </div>
              </div>
            ) : (
              <div className="flex flex-col">
                {sortedCredentials.map((credential) => {
                  const isBusy = busyCredentialID === credential.id
                  const statusLabel = credential.active ? 'Active' : 'Inactive'
                  const title = formatCredentialTitle(credential)
                  const showProviderInMeta = hasCustomLabel(credential)

                  return (
                    <div key={`${credential.provider}:${credential.id}`} className="grid gap-3 px-4 py-4 border-b border-[var(--app-border)] last:border-0 bg-[var(--app-bg)] hover:bg-[var(--app-surface-subtle)] transition-colors group sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:py-3">
                      <div className="grid min-w-0 grid-cols-[2rem_minmax(0,1fr)] items-center gap-3 sm:gap-4">
                        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-elevated)]">
                          <Key size={16} className="text-[var(--app-text)] opacity-70" />
                        </div>
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium leading-tight text-[var(--app-text)]">{title}</div>
                          <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--app-text-muted)]">
                            {showProviderInMeta ? (
                              <>
                                <span className="max-w-full truncate">{credential.provider}</span>
                                <span>•</span>
                              </>
                            ) : null}
                            <span className="font-mono uppercase opacity-70">{credential.authType || '—'}</span>
                            <span>•</span>
                            <span className="font-mono">{credential.last4 ? `••••${credential.last4}` : credential.id.slice(0, 8)}</span>
                          </div>
                        </div>
                      </div>

                      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:flex sm:justify-end sm:gap-4 sm:border-0 sm:bg-transparent sm:p-0">
                        <div className="flex min-w-0 items-center gap-1.5">
                          <div className={cn('h-1.5 w-1.5 rounded-full', credential.active ? 'bg-[var(--app-success)] shadow-[0_0_8px_var(--app-success)] opacity-80' : 'bg-[var(--app-border-strong)]')} />
                          <span className={cn("text-xs font-semibold", credential.active ? 'text-[var(--app-text)]' : 'text-[var(--app-text-muted)]')}>{statusLabel}</span>
                        </div>
                        
                        <div className="flex items-center gap-1">
                          <button
                            onClick={() => !credential.active && confirmMakeActive(credential) && runRowAction(credential, async () => {
                              await setActiveAuthCredential({ provider: credential.provider, id: credential.id })
                              return 'Active.'
                            }, 'Active.')}
                            disabled={isBusy || credential.active}
                            className={cn(
                              "h-7 w-7 flex items-center justify-center rounded-md transition-all", 
                              credential.active ? "text-[var(--app-success)] bg-[var(--app-success)]/10 cursor-default" : "text-[var(--app-text-muted)] hover:text-[var(--app-primary)] hover:bg-[var(--app-surface-elevated)]"
                            )}
                            title={credential.active ? "Active" : "Set Active"}
                          >
                            <Check size={14} strokeWidth={3} />
                          </button>
                          <button
                            onClick={() => {
                              const agentProfiles = (agentState?.profiles ?? []) as AgentProfileRecord[]
                              if (typeof window !== 'undefined' && !window.confirm(buildDeleteCredentialWarning(credential, agentProfiles))) {
                                return
                              }
                              void runRowAction(credential, async () => {
                                const result = await deleteAuthCredential({ provider: credential.provider, id: credential.id })
                                const extras: string[] = []
                                if (result.cleanup.clearedGlobalPreference) {
                                  extras.push('default model cleared')
                                }
                                if (result.cleanup.resetAgents.length > 0) {
                                  extras.push(`agents reset to inherit: ${result.cleanup.resetAgents.join(', ')}; reassign in /agents`)
                                }
                                return extras.length > 0 ? `Deleted. ${extras.join('. ')}.` : 'Deleted.'
                              }, 'Deleted.')
                            }}
                            disabled={isBusy}
                            className={cn(
                              "h-7 w-7 flex items-center justify-center rounded-md transition-all",
                              "text-[var(--app-text-muted)] hover:text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]",
                              isBusy && "opacity-50 cursor-not-allowed"
                            )}
                            title="Delete"
                          >
                            <Trash2 size={14} />
                          </button>
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        )}

        <ScopedTokensSection />
      </div>
    </div>
  )
}

function ScopedTokensSection() {
  const [copiedTokenId, setCopiedTokenId] = useState<string | null>(null)
  const [mintOpen, setMintOpen] = useState(false)
  const [mintName, setMintName] = useState('')
  const [mintWorkerId, setMintWorkerId] = useState('')
  const [mintWorkerName, setMintWorkerName] = useState('')
  const [mintExpires, setMintExpires] = useState<'0' | '2592000' | '7776000' | '31536000'>('2592000')
  const [mintResult, setMintResult] = useState<{ token: string; name: string } | null>(null)
  const [minting, setMinting] = useState(false)
  const [mintError, setMintError] = useState<string | null>(null)
  const [revokingId, setRevokingId] = useState<string | null>(null)

  const tokensQuery = useQuery({
    queryKey: ['scoped-tokens'],
    queryFn: () => listScopedTokens(),
  })

  const tokens = tokensQuery.data ?? []

  const handleCopy = useCallback(async (text: string, id: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopiedTokenId(id)
      setTimeout(() => setCopiedTokenId((curr) => (curr === id ? null : curr)), 2000)
    } catch {
      // ignore
    }
  }, [])

  const handleMint = async (e: React.FormEvent) => {
    e.preventDefault()
    const name = mintName.trim()
    if (!name) return
    setMinting(true)
    setMintError(null)
    try {
      const expiresInSeconds = parseInt(mintExpires, 10) || undefined
      const res = await createScopedToken({
        name,
        scopes: ['automations:trigger'],
        worker_id: mintWorkerId.trim() || undefined,
        worker_name: mintWorkerName.trim() || undefined,
        expires_in_seconds: expiresInSeconds,
      })
      setMintResult({ token: res.token, name: res.record.name })
      setMintName('')
      setMintWorkerId('')
      setMintWorkerName('')
      setMintOpen(false)
      void queryClient.invalidateQueries({ queryKey: ['scoped-tokens'] })
    } catch (err: any) {
      setMintError(err?.message || 'Failed to mint token')
    } finally {
      setMinting(false)
    }
  }

  const handleRevoke = async (tokenId: string, name: string) => {
    if (typeof window !== 'undefined' && !window.confirm(`Revoke token "${name}"? External scripts using this key will immediately be denied.`)) {
      return
    }
    setRevokingId(tokenId)
    try {
      await revokeScopedToken(tokenId)
      void queryClient.invalidateQueries({ queryKey: ['scoped-tokens'] })
    } finally {
      setRevokingId(null)
    }
  }

  return (
    <div className="mt-8 border-t border-[var(--app-border)] pt-8">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 mb-4">
        <div>
          <h3 className="text-base font-semibold text-[var(--app-text)] flex items-center gap-2">
            <Key size={16} className="text-[var(--app-primary)]" />
            Worker Trigger & Deploy Tokens
          </h3>
          <p className="text-xs text-[var(--app-text-muted)] mt-1">
            Scoped API keys for external scripts, webhooks, or SDK automation triggers. See which worker each key is tied to.
          </p>
        </div>
        <Button
          onClick={() => { setMintOpen(true); setMintResult(null); }}
          className="h-8 text-xs shrink-0 flex items-center gap-1.5"
        >
          <Plus size={14} />
          Mint Token
        </Button>
      </div>

      {mintResult && (
        <div className="mb-4 rounded-xl border border-[var(--app-success-border,var(--app-border))] bg-[var(--app-success-bg,var(--app-surface-subtle))] p-4 text-xs">
          <div className="font-semibold text-[var(--app-success,var(--app-text))] flex items-center justify-between">
            <span>New Token Minted: {mintResult.name}</span>
            <button
              type="button"
              onClick={() => handleCopy(mintResult.token, 'newly-minted')}
              className="flex items-center gap-1 font-mono font-normal text-[var(--app-primary)] hover:underline"
            >
              {copiedTokenId === 'newly-minted' ? <Check size={13} /> : <Copy size={13} />}
              {copiedTokenId === 'newly-minted' ? 'Copied' : 'Copy Key'}
            </button>
          </div>
          <p className="text-[var(--app-text-muted)] mt-1">
            Save this key now. For your security, it will not be displayed again.
          </p>
          <div className="mt-2 flex items-center gap-2 rounded bg-[var(--app-surface)] p-2 font-mono text-[11px] select-all break-all border border-[var(--app-border)]">
            <span className="flex-1">{mintResult.token}</span>
          </div>
        </div>
      )}

      {mintOpen && (
        <form onSubmit={handleMint} className="mb-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-xs space-y-3">
          <div className="font-semibold text-sm">Mint New Scoped Token</div>
          {mintError && (
            <div className="text-[var(--app-danger)]">{mintError}</div>
          )}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label className="block text-[11px] font-medium text-[var(--app-text-muted)] mb-1">
                Token Name *
              </label>
              <Input
                value={mintName}
                onChange={(e) => setMintName(e.target.value)}
                placeholder="e.g. GitHub Actions Trigger"
                required
                className="h-8 text-xs"
              />
            </div>
            <div>
              <label className="block text-[11px] font-medium text-[var(--app-text-muted)] mb-1">
                Expiration
              </label>
              <select
                value={mintExpires}
                onChange={(e: any) => setMintExpires(e.target.value)}
                className="h-8 w-full rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-xs text-[var(--app-text)]"
              >
                <option value="2592000">30 days</option>
                <option value="7776000">90 days</option>
                <option value="31536000">1 year</option>
                <option value="0">Never expires</option>
              </select>
            </div>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label className="block text-[11px] font-medium text-[var(--app-text-muted)] mb-1">
                Tied Worker ID (optional)
              </label>
              <Input
                value={mintWorkerId}
                onChange={(e) => setMintWorkerId(e.target.value)}
                placeholder="e.g. av2_917413f8ec6cd78da065d321515f4058"
                className="h-8 text-xs font-mono"
              />
            </div>
            <div>
              <label className="block text-[11px] font-medium text-[var(--app-text-muted)] mb-1">
                Worker Display Name (optional)
              </label>
              <Input
                value={mintWorkerName}
                onChange={(e) => setMintWorkerName(e.target.value)}
                placeholder="e.g. Mailbox Notifier"
                className="h-8 text-xs"
              />
            </div>
          </div>
          <div className="flex justify-end gap-2 pt-1">
            <Button
              type="button"
              variant="outline"
              onClick={() => setMintOpen(false)}
              className="h-8 text-xs"
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={minting || !mintName.trim()}
              className="h-8 text-xs"
            >
              {minting ? 'Minting...' : 'Mint Scoped Key'}
            </Button>
          </div>
        </form>
      )}

      {tokensQuery.isLoading ? (
        <div className="py-6 text-center text-xs text-[var(--app-text-muted)]">Loading tokens...</div>
      ) : tokens.length === 0 ? (
        <div className="rounded-xl border border-dashed border-[var(--app-border)] p-6 text-center text-xs text-[var(--app-text-muted)]">
          No scoped deploy tokens minted yet. Mint a key to trigger Worker V2 executions externally.
        </div>
      ) : (
        <div className="space-y-2">
          {tokens.map((token) => {
            const isRevoked = token.revoked === true
            const isRevoking = revokingId === token.id
            const expiresDate = token.expires_at ? new Date(token.expires_at).toLocaleDateString() : 'Never'
            const isExpired = token.expires_at ? token.expires_at < Date.now() : false
            const isCopied = copiedTokenId === token.id

            return (
              <div
                key={token.id}
                className={cn(
                  'rounded-xl border p-3.5 transition-all text-xs',
                  isRevoked || isExpired
                    ? 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]/40 opacity-60'
                    : 'border-[var(--app-border)] bg-[var(--app-surface)] shadow-xs'
                )}
              >
                <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-semibold text-[var(--app-text)]">{token.name}</span>
                      <span className="font-mono text-[11px] text-[var(--app-text-muted)] bg-[var(--app-surface-subtle)] px-1.5 py-0.5 rounded border border-[var(--app-border)]">
                        {token.token_hint}
                      </span>
                      {isRevoked ? (
                        <Badge tone="danger" className="text-[10px] px-1.5 py-0">Revoked</Badge>
                      ) : isExpired ? (
                        <Badge tone="warning" className="text-[10px] px-1.5 py-0">Expired</Badge>
                      ) : (
                        <Badge tone="live" className="text-[10px] px-1.5 py-0 text-[var(--app-success)] border-[var(--app-success)]/40">Active</Badge>
                      )}
                    </div>
                    <div className="mt-1.5 flex items-center gap-3 text-[11px] text-[var(--app-text-muted)] flex-wrap">
                      <span>
                        Tied Job: <span className="font-medium text-[var(--app-text)]">{token.worker_name || token.worker_id || 'All / Unbound'}</span>
                      </span>
                      <span>•</span>
                      <span>Scopes: <span className="font-mono text-[10px]">{token.scopes.join(', ')}</span></span>
                      <span>•</span>
                      <span>Expires: {expiresDate}</span>
                    </div>
                  </div>
                  {!isRevoked && (
                    <div className="flex items-center gap-1.5 shrink-0 self-end sm:self-center">
                      <button
                        type="button"
                        onClick={() => handleCopy(token.token_hint, token.id)}
                        className="h-7 px-2 rounded-md border border-[var(--app-border)] text-[11px] text-[var(--app-text-muted)] hover:text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] transition-colors flex items-center gap-1"
                        title="Copy token hint"
                      >
                        {isCopied ? <Check size={12} className="text-[var(--app-success)]" /> : <Copy size={12} />}
                        {isCopied ? 'Copied' : 'Hint'}
                      </button>
                      <button
                        type="button"
                        onClick={() => handleRevoke(token.id, token.name)}
                        disabled={isRevoking}
                        className="h-7 px-2 rounded-md border border-[var(--app-danger-border,var(--app-border))] text-[11px] text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)] transition-colors flex items-center gap-1 disabled:opacity-50"
                        title="Revoke Token"
                      >
                        <Trash2 size={12} />
                        {isRevoking ? 'Revoking...' : 'Revoke'}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
