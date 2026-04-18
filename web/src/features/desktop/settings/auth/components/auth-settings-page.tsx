import { useEffect, useMemo, useState, useCallback, useRef } from 'react'
import type { ChangeEvent } from 'react'
import { Badge } from '../../../../../components/ui/badge'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { Plus, Check, LogIn, Trash2, Key, ChevronDown } from 'lucide-react'
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
import { listProviders } from '../../queries/list-providers'
import type { AuthCredential, AuthMethod, CodexOAuthSession, ProviderStatus, StartCodexOAuthInput, UpsertAuthCredentialInput } from '../../types/auth'
import { createPortal } from 'react-dom'

type OAuthIntent = 'browser' | 'manual'

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
  const [oauthIntent, setOAuthIntent] = useState<OAuthIntent>('browser')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)
  const [callbackInput, setCallbackInput] = useState('')

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
    if (!composerOpen || oauthIntent !== 'browser' || !oauthSession?.sessionID) {
      return
    }
    if (oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    const timer = window.setInterval(() => {
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then(async (next) => {
          setOAuthSession(next)
          if (next.status === 'success') {
            setStatus('Credential is now active.')
            setComposerOpen(false)
            setFormError(null)
            setCallbackInput('')
            setAPIKey('')
            setLabel('')
            await load()
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

  const runRowAction = async (credential: AuthCredential, action: () => Promise<unknown>, doneMessage: string) => {
    setBusyCredentialID(credential.id)
    setError(null)
    setStatus(null)
    try {
      await action()
      await load()
      setStatus(doneMessage)
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
    try {
      const session = await startCodexOAuth(payload)
      setOAuthIntent(intent)
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
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">Securely managed LLM provider keys.</p>
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
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Button className={authButtonAccentClass} onClick={() => void startOAuth('browser')} disabled={saving}>
                    <LogIn size={16} className="mr-2" /> Browser login
                  </Button>
                  <Button className={authButtonNeutralClass} onClick={() => void startOAuth('manual')} disabled={saving}>
                    <Key size={16} className="mr-2" /> Manual login
                  </Button>
                </div>

                {oauthSession ? (
                  <>
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge tone={oauthSession.status === 'success' ? 'live' : oauthSession.status === 'error' ? 'danger' : 'warning'}>
                        {oauthSession.status || 'waiting'}
                      </Badge>
                      {oauthSession.error ? <span className="text-sm text-[var(--app-danger)]">{oauthSession.error}</span> : null}
                    </div>
                    {oauthSession.authURL ? (
                      <label className="grid gap-2">
                        <span className="text-sm font-medium text-[var(--app-text)]">Auth URL</span>
                        <Textarea value={oauthSession.authURL} readOnly className="min-h-[96px] bg-[var(--app-surface-subtle)]" />
                      </label>
                    ) : null}
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
                  const isOnlyOne = sortedCredentials.length === 1
                  const title = formatCredentialTitle(credential)
                  const showProviderInMeta = hasCustomLabel(credential)

                  return (
                    <div key={`${credential.provider}:${credential.id}`} className="flex flex-col sm:flex-row sm:items-center justify-between px-4 py-3 border-b border-[var(--app-border)] last:border-0 bg-[var(--app-bg)] hover:bg-[var(--app-surface-subtle)] transition-colors group">
                      <div className="flex items-center gap-4 mb-3 sm:mb-0">
                        <div className="w-8 h-8 rounded-lg bg-[var(--app-surface-elevated)] border border-[var(--app-border)] flex flex-shrink-0 items-center justify-center">
                          <Key size={16} className="text-[var(--app-text)] opacity-70" />
                        </div>
                        <div>
                          <div className="text-sm font-medium text-[var(--app-text)] leading-tight">{title}</div>
                          <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)] mt-0.5">
                            {showProviderInMeta ? (
                              <>
                                <span>{credential.provider}</span>
                                <span>•</span>
                              </>
                            ) : null}
                            <span className="font-mono uppercase opacity-70">{credential.authType || '—'}</span>
                            <span>•</span>
                            <span className="font-mono">{credential.last4 ? `••••${credential.last4}` : credential.id.slice(0, 8)}</span>
                          </div>
                        </div>
                      </div>

                      <div className="flex items-center gap-4 sm:justify-end ml-12 sm:ml-0">
                        <div className="flex items-center gap-1.5 w-20">
                          <div className={cn('h-1.5 w-1.5 rounded-full', credential.active ? 'bg-[var(--app-success)] shadow-[0_0_8px_var(--app-success)] opacity-80' : 'bg-[var(--app-border-strong)]')} />
                          <span className={cn("text-xs font-semibold", credential.active ? 'text-[var(--app-text)]' : 'text-[var(--app-text-muted)]')}>{statusLabel}</span>
                        </div>
                        
                        <div className="flex items-center gap-1">
                          <button
                            onClick={() => !credential.active && confirmMakeActive(credential) && runRowAction(credential, () => setActiveAuthCredential({ provider: credential.provider, id: credential.id }), 'Active.')}
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
                            onClick={() => !isOnlyOne && runRowAction(credential, () => deleteAuthCredential({ provider: credential.provider, id: credential.id }), 'Deleted.')}
                            disabled={isBusy || isOnlyOne}
                            className={cn(
                              "h-7 w-7 flex items-center justify-center rounded-md transition-all", 
                              isOnlyOne ? "opacity-10 cursor-not-allowed text-[var(--app-text-muted)]" : "text-[var(--app-text-muted)] hover:text-[var(--app-danger)] hover:bg-[var(--app-danger-bg)]"
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
      </div>
    </div>
  )
}
