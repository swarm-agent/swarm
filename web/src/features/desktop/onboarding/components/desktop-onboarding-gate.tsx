import { useEffect, useMemo, useState } from 'react'
import { queryClient } from '../../../../app/query-client'
import { draftModelQueryOptions, modelOptionsQueryOptions, agentStateQueryOptions } from '../../../queries/query-options'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import {
  fetchDesktopOnboardingStatus,
  saveDesktopOnboarding,
} from '../api'
import type { DesktopOnboardingStatus } from '../types'
import { startCodexOAuth } from '../../settings/mutations/start-codex-oauth'
import { getCodexOAuthStatus } from '../../settings/queries/get-codex-oauth-status'
import { completeCodexOAuth } from '../../settings/mutations/complete-codex-oauth'
import { upsertAuthCredential } from '../../settings/mutations/upsert-auth-credential'
import { verifyAuthCredential } from '../../settings/mutations/verify-auth-credential'
import type { AuthMethod, CodexOAuthSession, ProviderStatus, StartCodexOAuthInput, UpsertAuthCredentialInput } from '../../settings/types/auth'
import { upsertSwarmGroup } from '../../swarm/mutations/upsert-swarm-group'

type OnboardingStep = 'identity' | 'provider'
type CodexOAuthMode = StartCodexOAuthInput['method']

interface DesktopOnboardingGateProps {
  status: DesktopOnboardingStatus
  restart?: boolean
  onComplete: (status: DesktopOnboardingStatus) => void
}

function deriveInitialStep(status: DesktopOnboardingStatus): OnboardingStep {
  return status.config.swarmName ? 'provider' : 'identity'
}

function apiCompatibleMethods(provider: ProviderStatus): AuthMethod[] {
  return provider.authMethods.filter((method) => method.credentialType === 'api' || method.credentialType === 'access_token' || method.credentialType === 'token')
}

function supportsCodexOAuth(provider: ProviderStatus | null): boolean {
  if (!provider || provider.id !== 'codex') {
    return false
  }
  return provider.authMethods.some((method) => method.credentialType === 'oauth' || method.id === 'oauth')
}

function credentialLabel(method: AuthMethod | null): string {
  if (!method) {
    return 'Credential'
  }
  if (method.credentialType === 'api') {
    return 'API key'
  }
  return 'Access token'
}

function currentGroupName(status: DesktopOnboardingStatus): string {
  const currentGroupID = status.currentGroupID.trim()
  if (currentGroupID) {
    const current = status.groups.find((group) => group.group.id === currentGroupID)
    if (current?.group.name.trim()) {
      return current.group.name.trim()
    }
  }
  const firstGroupName = status.groups[0]?.group.name?.trim() || ''
  return firstGroupName
}

async function refreshAuthDependentQueries(): Promise<void> {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: draftModelQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: modelOptionsQueryOptions().queryKey }),
    queryClient.invalidateQueries({ queryKey: agentStateQueryOptions().queryKey }),
  ])
}

function suggestedGroupName(status: DesktopOnboardingStatus, swarmName: string): string {
  const existing = currentGroupName(status)
  if (existing) {
    return existing
  }
  const normalizedName = swarmName.trim()
  return normalizedName ? `${normalizedName} Group` : ''
}

export function DesktopOnboardingGate({ status: initialStatus, restart = false, onComplete }: DesktopOnboardingGateProps) {
  const [status, setStatus] = useState(initialStatus)
  const [step, setStep] = useState<OnboardingStep>(() => (restart ? 'identity' : deriveInitialStep(initialStatus)))
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const [swarmName, setSwarmName] = useState(initialStatus.config.swarmName)

  const providerOptions = useMemo(
    () => status.auth.providers.filter((provider) => provider.id !== '' && !provider.runReason.toLowerCase().includes('search-only provider')),
    [status.auth.providers],
  )
  const [providerID, setProviderID] = useState(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
  const [credentialValue, setCredentialValue] = useState('')
  const [codexOAuthMode, setCodexOAuthMode] = useState<CodexOAuthMode>('browser')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)
  const [callbackInput, setCallbackInput] = useState('')

  const selectedProvider = useMemo(
    () => providerOptions.find((provider) => provider.id === providerID) ?? providerOptions[0] ?? null,
    [providerID, providerOptions],
  )
  const manualMethods = useMemo(() => (selectedProvider ? apiCompatibleMethods(selectedProvider) : []), [selectedProvider])
  const selectedManualMethod = manualMethods[0] ?? null
  const providerAlreadyConnected = Boolean(selectedProvider && status.auth.activeProviders.includes(selectedProvider.id))
  const canStartOAuth = supportsCodexOAuth(selectedProvider)
  const canQuickAuthenticate = Boolean(selectedManualMethod || canStartOAuth)

  useEffect(() => {
    if (!status.auth.activeProviders.includes(providerID) && !providerOptions.some((provider) => provider.id === providerID)) {
      setProviderID(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
    }
  }, [providerID, providerOptions, status.auth.activeProviders])

  useEffect(() => {
    if (codexOAuthMode !== 'browser' || !oauthSession?.sessionID || oauthSession.status === 'success' || oauthSession.status === 'error') {
      return
    }

    const timer = window.setInterval(() => {
      void getCodexOAuthStatus(oauthSession.sessionID)
        .then((next) => {
          setOAuthSession(next)
          if (next.status === 'success') {
            setNotice('Provider connected.')
            setError(null)
            void finalizeOnboarding()
          }
        })
        .catch((err) => {
          setError(err instanceof Error ? err.message : 'Failed to refresh OAuth status')
        })
    }, 1500)

    return () => {
      window.clearInterval(timer)
    }
  }, [codexOAuthMode, oauthSession])

  const reloadStatus = async () => {
    const next = await fetchDesktopOnboardingStatus()
    setStatus(next)
    return next
  }

  const persistIdentity = async () => {
    const normalizedName = swarmName.trim()
    const normalizedGroupName = suggestedGroupName(status, normalizedName).trim()
    if (!normalizedName) {
      throw new Error('Swarm name is required.')
    }
    if (!normalizedGroupName) {
      throw new Error('Group name is required for the first swarm group.')
    }
    const next = await saveDesktopOnboarding({
      swarmName: normalizedName,
      swarmMode: true,
      child: false,
    })
    let refreshed = next
    const currentGroupID = next.currentGroupID.trim() || next.groups[0]?.group.id?.trim() || ''
    await upsertSwarmGroup({
      groupID: currentGroupID || undefined,
      name: normalizedGroupName,
      setCurrent: true,
    })
    refreshed = await fetchDesktopOnboardingStatus()
    setStatus(refreshed)
    setSwarmName(refreshed.config.swarmName)
    return refreshed
  }

  const finalizeOnboarding = async () => {
    const next = await reloadStatus()
    await refreshAuthDependentQueries()
    setStatus(next)
    onComplete(next)
  }

  const handleIdentityContinue = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      const next = await persistIdentity()
      setStep('provider')
      setStatus(next)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save onboarding settings')
    } finally {
      setSubmitting(false)
    }
  }

  const handleProviderSave = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      if (!selectedProvider || !selectedManualMethod) {
        throw new Error('Choose a provider with API key support, use browser sign-in, or skip for now.')
      }
      if (!credentialValue.trim()) {
        throw new Error(`${credentialLabel(selectedManualMethod)} is required.`)
      }

      const payload: UpsertAuthCredentialInput = {
        provider: selectedProvider.id,
        type: selectedManualMethod.credentialType,
        active: true,
      }
      if (selectedManualMethod.credentialType === 'api') {
        payload.api_key = credentialValue.trim()
      } else {
        payload.access_token = credentialValue.trim()
      }

      const saved = await upsertAuthCredential(payload)
      const verification = await verifyAuthCredential({ provider: saved.provider, id: saved.id })
      if (!verification.connected) {
        throw new Error(verification.message || 'Credential saved, but verification failed.')
      }

      setCredentialValue('')
      setNotice('Provider connected.')
      await reloadStatus()
      await finalizeOnboarding()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save provider credential')
    } finally {
      setSubmitting(false)
    }
  }

  const handleStartOAuth = async (method: CodexOAuthMode) => {
    if (!selectedProvider) {
      setError('Choose a provider first.')
      return
    }
    setSubmitting(true)
    setError(null)
    setNotice(null)
    setCallbackInput('')
    try {
      const session = await startCodexOAuth({
        provider: selectedProvider.id,
        active: true,
        method,
      })
      setCodexOAuthMode(method)
      setOAuthSession(session)
      if (method === 'browser' && session.authURL && typeof window !== 'undefined') {
        window.open(session.authURL, '_blank', 'noopener,noreferrer')
      }
      setNotice(method === 'browser'
        ? 'Finish local sign-in in your browser. Swarm will continue when it sees the callback.'
        : 'Open the remote auth URL anywhere, then paste the callback URL or code here.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start Codex sign-in')
    } finally {
      setSubmitting(false)
    }
  }

  const handleCompleteOAuth = async () => {
    if (!oauthSession?.sessionID) {
      setError('Start remote sign-in first.')
      return
    }
    if (!callbackInput.trim()) {
      setError('Paste the callback URL, query string, or authorization code.')
      return
    }

    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      const session = await completeCodexOAuth({
        session_id: oauthSession.sessionID,
        callback_input: callbackInput.trim(),
      })
      setOAuthSession(session)
      if (session.status !== 'success') {
        throw new Error(session.error || 'OAuth completion did not succeed.')
      }
      setCredentialValue('')
      setCallbackInput('')
      setNotice('Provider connected.')
      await reloadStatus()
      await finalizeOnboarding()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete remote sign-in')
    } finally {
      setSubmitting(false)
    }
  }

  const title = step === 'identity' ? 'Name this Swarm' : 'Connect a provider'

  const subtitle = step === 'identity'
    ? 'Give this machine a clear name. Provider sign-in is next.'
    : 'Add an API key or sign in now so this Swarm is ready to run immediately.'

  return (
    <div className="absolute inset-0 flex items-center justify-center bg-[radial-gradient(circle_at_top,#1b2f45,transparent_52%),var(--app-bg)] px-6 py-8">
      <Card className="relative w-full max-w-2xl overflow-hidden border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        <div className="grid gap-6 p-8">
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="rounded-full border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-2.5 py-1 text-xs font-medium text-[var(--app-success)]">{restart ? 'Swarm setup' : 'First launch'}</span>
              <span className="text-xs uppercase tracking-[0.2em] text-[var(--app-text-muted)]">
                {step === 'identity' ? 'Step 1 of 2' : 'Step 2 of 2'}
              </span>
            </div>
            <div className="grid gap-1">
              <h1 className="text-2xl font-semibold tracking-tight text-[var(--app-text)]">{title}</h1>
              <p className="max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">{subtitle}</p>
            </div>
          </div>

          {error ? (
            <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">
              {error}
            </div>
          ) : null}

          {notice ? (
            <div className="rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">
              {notice}
            </div>
          ) : null}

          {step === 'identity' ? (
            <div className="grid gap-6">
              <div className="grid gap-2">
                <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-swarm-name">
                  Device / Swarm name
                </label>
                <Input
                  id="desktop-onboarding-swarm-name"
                  autoFocus
                  value={swarmName}
                  onChange={(event) => setSwarmName(event.target.value)}
                  placeholder="my-device"
                />
                <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                  This is the label Swarm shows for this machine in discovery and launcher screens.
                </p>
              </div>

              <div className="flex justify-end">
                <Button type="button" onClick={() => void handleIdentityContinue()} disabled={submitting}>
                  {submitting ? 'Saving…' : 'Continue'}
                </Button>
              </div>
            </div>
          ) : null}

          {step === 'provider' ? (
            <div className="grid gap-6">
              {providerOptions.length > 0 ? (
                <>
                  <div className="grid gap-2">
                    <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-provider">
                      Provider
                    </label>
                    <select
                      id="desktop-onboarding-provider"
                      value={providerID}
                      onChange={(event) => {
                        setProviderID(event.target.value)
                        setCredentialValue('')
                        setCallbackInput('')
                        setOAuthSession(null)
                        setError(null)
                        setNotice(null)
                      }}
                      className="h-11 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]"
                    >
                      {providerOptions.map((provider) => (
                        <option key={provider.id} value={provider.id}>
                          {provider.id}
                        </option>
                      ))}
                    </select>
                  </div>

                  {providerAlreadyConnected ? (
                    <div className="rounded-2xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-4 text-sm leading-6 text-[var(--app-success)]">
                      {selectedProvider?.id || 'Selected provider'} is already connected.
                    </div>
                  ) : canQuickAuthenticate ? (
                    <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                      {selectedManualMethod ? (
                        <>
                          <label className="grid gap-2">
                            <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
                              {credentialLabel(selectedManualMethod)}
                            </span>
                            <Input
                              type="password"
                              autoComplete="off"
                              value={credentialValue}
                              onChange={(event) => setCredentialValue(event.target.value)}
                              placeholder={credentialLabel(selectedManualMethod)}
                            />
                          </label>
                          {selectedManualMethod.description ? (
                            <p className="text-sm leading-6 text-[var(--app-text-muted)]">{selectedManualMethod.description}</p>
                          ) : null}
                        </>
                      ) : null}

                      {canStartOAuth && oauthSession ? (
                        <div className="grid gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm leading-6 text-[var(--app-text-muted)]">
                          <div>
                            {codexOAuthMode === 'browser' ? 'Local browser sign-in' : 'Remote browser sign-in'} status: <span className="font-medium text-[var(--app-text)]">{oauthSession.status || 'waiting'}</span>
                            {oauthSession.error ? <div className="text-[var(--app-danger)]">{oauthSession.error}</div> : null}
                          </div>
                          {oauthSession.authURL ? (
                            <label className="grid gap-2">
                              <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Auth URL</span>
                              <textarea readOnly value={oauthSession.authURL} className="min-h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)] outline-none" />
                            </label>
                          ) : null}
                          {codexOAuthMode === 'manual' ? (
                            <>
                              <label className="grid gap-2">
                                <span className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Callback URL or code</span>
                                <textarea value={callbackInput} onChange={(event) => setCallbackInput(event.target.value)} placeholder="Paste the callback URL, query string, or authorization code" className="min-h-24 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
                              </label>
                              <div className="flex justify-end">
                                <Button type="button" onClick={() => void handleCompleteOAuth()} disabled={submitting || !oauthSession.sessionID}>
                                  {submitting ? 'Completing…' : 'Complete remote sign-in'}
                                </Button>
                              </div>
                            </>
                          ) : null}
                        </div>
                      ) : null}

                      <div className="flex flex-wrap justify-end gap-3">
                        {canStartOAuth ? (
                          <>
                            <Button type="button" variant="outline" onClick={() => void handleStartOAuth('browser')} disabled={submitting}>
                              Local browser sign-in
                            </Button>
                            <Button type="button" variant="outline" onClick={() => void handleStartOAuth('manual')} disabled={submitting}>
                              Remote browser sign-in
                            </Button>
                          </>
                        ) : null}
                        {selectedManualMethod ? (
                          <Button type="button" onClick={() => void handleProviderSave()} disabled={submitting}>
                            {submitting ? 'Saving…' : 'Save provider'}
                          </Button>
                        ) : null}
                      </div>
                    </div>
                  ) : (
                    <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                      The selected provider does not expose a quick auth method here. Finish onboarding and connect it from Settings.
                    </div>
                  )}
                </>
              ) : (
                <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                  No providers are available yet. Finish onboarding and connect one later from Settings.
                </div>
              )}

              <div className="flex items-center justify-between gap-3">
                <Button type="button" variant="outline" onClick={() => setStep('identity')} disabled={submitting}>
                  Back
                </Button>
                <Button type="button" variant={providerAlreadyConnected || providerOptions.length === 0 ? 'primary' : 'outline'} onClick={() => void finalizeOnboarding()} disabled={submitting}>
                  {submitting ? 'Finishing…' : providerAlreadyConnected || providerOptions.length === 0 ? 'Open launcher' : 'Skip for now'}
                </Button>
              </div>
            </div>
          ) : null}
        </div>
      </Card>
    </div>
  )
}
