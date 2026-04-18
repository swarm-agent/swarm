import { useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react'
import { Badge } from '../../../../components/ui/badge'
import { Button } from '../../../../components/ui/button'
import { Card } from '../../../../components/ui/card'
import { Input } from '../../../../components/ui/input'
import { useDesktopStore } from '../../state/use-desktop-store'
import {
  fetchDesktopOnboardingStatus,
  saveDesktopOnboarding,
  submitSwarmEnrollment,
} from '../api'
import type { DesktopOnboardingStatus } from '../types'
import { startCodexOAuth } from '../../settings/mutations/start-codex-oauth'
import { getCodexOAuthStatus } from '../../settings/queries/get-codex-oauth-status'
import { upsertAuthCredential } from '../../settings/mutations/upsert-auth-credential'
import { verifyAuthCredential } from '../../settings/mutations/verify-auth-credential'
import type { AuthMethod, CodexOAuthSession, ProviderStatus, UpsertAuthCredentialInput } from '../../settings/types/auth'
import { upsertSwarmGroup } from '../../swarm/mutations/upsert-swarm-group'
import { suggestGroupNetworkName } from '../../swarm/services/group-network-name'

type OnboardingStep = 'identity' | 'security' | 'provider' | 'child-bootstrap'
type SecurityChoice = 'setup' | 'import' | 'skip' | null
type SwarmRole = 'master' | 'child'
type BootstrapMode = 'lan' | 'tailscale'

interface DesktopOnboardingGateProps {
  status: DesktopOnboardingStatus
  restart?: boolean
  onComplete: (status: DesktopOnboardingStatus) => void
}

function deriveInitialStep(status: DesktopOnboardingStatus): OnboardingStep {
  if (!status.config.swarmMode) {
    return 'identity'
  }
  if (status.config.swarmRole === 'child' && status.pairing.pairingState !== 'paired') {
    return 'child-bootstrap'
  }
  if (status.auth.credentialCount > 0) {
    return 'provider'
  }
  if (
    status.vault.enabled
    || status.config.swarmName
    || status.config.mode === 'tailscale'
    || status.config.child
  ) {
    return 'security'
  }
  return 'identity'
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

function suggestedTailscaleURL(status: DesktopOnboardingStatus, mode: BootstrapMode): string {
 if (mode === 'tailscale') {
    return status.config.tailscaleURL || status.network.tailscale.candidateURL || status.network.tailscale.tailnetURL || status.network.tailscale.dnsName || status.network.tailscale.ips[0] || ''
  }
  return ''
}

function suggestedAdvertiseHost(status: DesktopOnboardingStatus): string {
  return status.config.advertiseHost || status.network.lanAddresses[0] || ''
}

function defaultBootstrapMode(status: DesktopOnboardingStatus): BootstrapMode {
  if (status.config.mode === 'tailscale' || status.config.mode === 'lan') {
    return status.config.mode
  }
  if (status.network.tailscale.connected) {
    return 'tailscale'
  }
  return 'lan'
}

function defaultSwarmRole(status: DesktopOnboardingStatus): SwarmRole {
  return status.config.swarmRole === 'child' ? 'child' : 'master'
}

function parseAPIPort(value: string): number {
  if (!/^\d+$/.test(value.trim())) {
    throw new Error('Backend API port must be a whole number.')
  }
  const parsed = Number.parseInt(value.trim(), 10)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error('Backend API port must be between 1 and 65535.')
  }
  return parsed
}

function parseAdvertiseHost(value: string): string {
  const normalized = value.trim()
  if (!normalized) {
    throw new Error('LAN advertise host is required.')
  }
  if (normalized.includes('://')) {
    throw new Error('LAN advertise host must be a host or IP only, without http:// or https://.')
  }
  if (normalized.includes('/')) {
    throw new Error('LAN advertise host must not contain path separators.')
  }
  return normalized
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

function currentGroupNetworkName(status: DesktopOnboardingStatus): string {
  const currentGroupID = status.currentGroupID.trim()
  if (currentGroupID) {
    const current = status.groups.find((group) => group.group.id === currentGroupID)
    if (current?.group.networkName.trim()) {
      return current.group.networkName.trim()
    }
  }
  return status.groups[0]?.group.networkName?.trim() || ''
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
  const enableVault = useDesktopStore((state) => state.enableVault)
  const importVaultBundle = useDesktopStore((state) => state.importVaultBundle)

  const [status, setStatus] = useState(initialStatus)
  const [step, setStep] = useState<OnboardingStep>(() => (restart ? 'identity' : deriveInitialStep(initialStatus)))
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const [swarmName, setSwarmName] = useState(initialStatus.config.swarmName)
  const [swarmRole, setSwarmRole] = useState<SwarmRole>(() => defaultSwarmRole(initialStatus))
  const [groupName, setGroupName] = useState(() => suggestedGroupName(initialStatus, initialStatus.config.swarmName))
  const [groupNameTouched, setGroupNameTouched] = useState(() => currentGroupName(initialStatus) !== '')
  const [mode, setMode] = useState<BootstrapMode>(() => defaultBootstrapMode(initialStatus))
  const [apiPort, setAPIPort] = useState(() => String(initialStatus.config.port))
  const [advertiseHost, setAdvertiseHost] = useState(() => initialStatus.config.advertiseHost || suggestedAdvertiseHost(initialStatus))
  const [advertisePort, setAdvertisePort] = useState(() => String(initialStatus.config.advertisePort))
  const [tailscaleURL, setTailscaleURL] = useState(
    initialStatus.config.tailscaleURL || suggestedTailscaleURL(initialStatus, defaultBootstrapMode(initialStatus)),
  )
  const [inviteToken, setInviteToken] = useState('')

  const [securityChoice, setSecurityChoice] = useState<SecurityChoice>(initialStatus.vault.enabled ? 'setup' : null)
  const [vaultPassword, setVaultPassword] = useState('')
  const [vaultConfirm, setVaultConfirm] = useState('')
  const [importPassword, setImportPassword] = useState('')
  const [importVaultPassword, setImportVaultPassword] = useState('')
  const [importBundleName, setImportBundleName] = useState('')
  const [importBundleBytes, setImportBundleBytes] = useState<Uint8Array | null>(null)

  const providerOptions = useMemo(
    () => status.auth.providers.filter((provider) => provider.id !== '' && !provider.runReason.toLowerCase().includes('search-only provider')),
    [status.auth.providers],
  )
  const [providerID, setProviderID] = useState(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
  const [credentialValue, setCredentialValue] = useState('')
  const [oauthSession, setOAuthSession] = useState<CodexOAuthSession | null>(null)

  const fileInputRef = useRef<HTMLInputElement | null>(null)

  const selectedProvider = useMemo(
    () => providerOptions.find((provider) => provider.id === providerID) ?? providerOptions[0] ?? null,
    [providerID, providerOptions],
  )
  const manualMethods = useMemo(() => (selectedProvider ? apiCompatibleMethods(selectedProvider) : []), [selectedProvider])
  const selectedManualMethod = manualMethods[0] ?? null
  const providerAlreadyConnected = Boolean(selectedProvider && status.auth.activeProviders.includes(selectedProvider.id))
  const groupNetworkPreview = useMemo(
    () => currentGroupNetworkName(status) || suggestGroupNetworkName(groupName || suggestedGroupName(status, swarmName) || 'swarm-group'),
    [groupName, status, swarmName],
  )

  useEffect(() => {
    if (!status.auth.activeProviders.includes(providerID) && !providerOptions.some((provider) => provider.id === providerID)) {
      setProviderID(status.auth.activeProviders[0] || providerOptions[0]?.id || '')
    }
  }, [providerID, providerOptions, status.auth.activeProviders])

  useEffect(() => {
    if (mode === 'tailscale') {
      setTailscaleURL((current) => current || suggestedTailscaleURL(status, mode))
    }
  }, [mode, status])

  useEffect(() => {
    if (mode !== 'lan') {
      return
    }
    setAdvertiseHost((current) => current || suggestedAdvertiseHost(status))
    setAdvertisePort((current) => current || String(status.config.advertisePort || status.config.port))
  }, [mode, status])

  useEffect(() => {
    if (swarmRole !== 'master' || groupNameTouched) {
      return
    }
    setGroupName(suggestedGroupName(status, swarmName))
  }, [groupNameTouched, status, swarmName, swarmRole])

  useEffect(() => {
    if (!oauthSession?.sessionID || oauthSession.status === 'success' || oauthSession.status === 'error') {
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
  }, [oauthSession])

  const reloadStatus = async () => {
    const next = await fetchDesktopOnboardingStatus()
    setStatus(next)
    return next
  }

  const persistIdentity = async () => {
    const normalizedName = swarmName.trim()
    const normalizedGroupName = groupName.trim()
    const normalizedAPIPort = parseAPIPort(apiPort)
    const normalizedAdvertisePort = advertisePort.trim() ? parseAPIPort(advertisePort) : status.config.advertisePort || normalizedAPIPort
    const normalizedAdvertiseHost = advertiseHost.trim() ? parseAdvertiseHost(advertiseHost) : ''
    const normalizedTailscaleURL = tailscaleURL.trim()

    if (!normalizedName) {
      throw new Error('Swarm name is required.')
    }
    if (swarmRole === 'master' && !normalizedGroupName) {
      throw new Error('Group name is required for the first swarm group.')
    }
    if (mode === 'tailscale' && !normalizedTailscaleURL) {
      throw new Error('Choose or enter a Tailscale URL before continuing.')
    }

    const next = await saveDesktopOnboarding({
      swarmName: normalizedName,
      swarmMode: true,
      child: swarmRole === 'child',
      mode,
      port: normalizedAPIPort,
      advertiseHost: normalizedAdvertiseHost,
      advertisePort: normalizedAdvertisePort,
      tailscaleURL: normalizedTailscaleURL,
    })
    let refreshed = next
    if (swarmRole === 'master') {
      const currentGroupID = next.currentGroupID.trim() || next.groups[0]?.group.id?.trim() || ''
      await upsertSwarmGroup({
        groupID: currentGroupID || undefined,
        name: normalizedGroupName,
        setCurrent: true,
      })
      refreshed = await fetchDesktopOnboardingStatus()
    }
    setStatus(refreshed)
    setSwarmName(refreshed.config.swarmName)
    setSwarmRole(defaultSwarmRole(refreshed))
    setGroupName(currentGroupName(refreshed) || normalizedGroupName)
    setMode(refreshed.config.mode)
    setAPIPort(String(refreshed.config.port))
    setAdvertiseHost(refreshed.config.advertiseHost || suggestedAdvertiseHost(refreshed))
    setAdvertisePort(String(refreshed.config.advertisePort))
    setTailscaleURL(refreshed.config.tailscaleURL)
    return refreshed
  }

  const finalizeOnboarding = async () => {
    const next = await reloadStatus()
    setStatus(next)
    onComplete(next)
  }

  const handleIdentityContinue = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      const next = await persistIdentity()
      if (swarmRole === 'child') {
        setStep('child-bootstrap')
      } else {
        setStep('security')
      }
      setStatus(next)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save onboarding settings')
    } finally {
      setSubmitting(false)
    }
  }

  const handleChildBootstrapContinue = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      const token = inviteToken.trim()
      if (!token) {
        throw new Error('Invite token is required.')
      }
      const enrollment = await submitSwarmEnrollment({
        inviteToken: token,
        childSwarmID: status.pairing.swarmID,
        childName: swarmName.trim(),
        childRole: 'child',
        transportMode: mode,
        rendezvousTransports: status.pairing.rendezvousTransports,
      })
      const next = await reloadStatus()
      setNotice(`Enrollment requested. Status: ${enrollment.status}. Waiting for Primary approval.`)
      setStatus(next)
      if (next.auth.credentialCount > 0) {
        await finalizeOnboarding()
      } else {
        setStep('provider')
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to submit enrollment request')
    } finally {
      setSubmitting(false)
    }
  }

  const handleEnableVault = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      if (!vaultPassword.trim()) {
        throw new Error('Vault password is required.')
      }
      if (vaultPassword !== vaultConfirm) {
        throw new Error('Vault passwords do not match.')
      }
      await enableVault(vaultPassword)
      await reloadStatus()
      setVaultPassword('')
      setVaultConfirm('')
      setNotice('Vault enabled.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to enable vault')
    } finally {
      setSubmitting(false)
    }
  }

  const handleImportBundle = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      if (!importBundleBytes) {
        throw new Error('Choose a vault bundle first.')
      }
      if (!importPassword.trim()) {
        throw new Error('Bundle password is required.')
      }
      const result = await importVaultBundle(importPassword.trim(), importBundleBytes, importVaultPassword.trim())
      await reloadStatus()
      setImportPassword('')
      setImportVaultPassword('')
      setImportBundleName('')
      setImportBundleBytes(null)
      setNotice(`Imported ${result.imported} credential(s).`)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to import vault bundle')
    } finally {
      setSubmitting(false)
    }
  }

  const handleSecurityContinue = async () => {
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      if (securityChoice === null) {
        throw new Error('Choose how to handle credentials first.')
      }
      if (securityChoice === 'setup' && !status.vault.enabled) {
        throw new Error('Set up the vault before continuing.')
      }
      if (securityChoice === 'import' && !status.vault.enabled && status.auth.credentialCount === 0) {
        throw new Error('Import a vault bundle before continuing.')
      }
      setStep('provider')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to continue onboarding')
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
        throw new Error('Choose a provider with API key support, or skip for now.')
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

  const handleStartOAuth = async () => {
    if (!selectedProvider) {
      setError('Choose a provider first.')
      return
    }
    setSubmitting(true)
    setError(null)
    setNotice(null)
    try {
      const session = await startCodexOAuth({
        provider: selectedProvider.id,
        active: true,
        method: 'browser',
      })
      setOAuthSession(session)
      if (session.authURL && typeof window !== 'undefined') {
        window.open(session.authURL, '_blank', 'noopener,noreferrer')
      }
      setNotice('Finish sign-in in your browser. Swarm will continue when it sees the callback.')
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start browser sign-in')
    } finally {
      setSubmitting(false)
    }
  }

  const handleImportFileSelected = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file) {
      return
    }
    try {
      const raw = new Uint8Array(await file.arrayBuffer())
      if (raw.length === 0) {
        throw new Error('Selected bundle file is empty.')
      }
      setImportBundleName(file.name)
      setImportBundleBytes(raw)
      setNotice(`Selected ${file.name}.`)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to read bundle file')
    }
  }

  const title = step === 'identity'
    ? 'Name this Swarm'
    : step === 'child-bootstrap'
      ? 'Join a Primary Swarm'
      : step === 'security'
        ? 'Protect credentials'
        : 'Connect a provider'

  const subtitle = step === 'identity'
    ? 'Give this machine a clear name, then decide how other devices should reach it.'
    : step === 'child-bootstrap'
      ? 'Use a short-lived invite token from the Primary. Reachability can be LAN, Tailscale, or both.'
      : step === 'security'
        ? 'Store credentials in a local vault now, import one, or skip until later.'
        : 'Add a provider now so this Swarm is ready to run immediately, or skip for now.'

  return (
    <div className="absolute inset-0 flex items-center justify-center bg-[radial-gradient(circle_at_top,#1b2f45,transparent_52%),var(--app-bg)] px-6 py-8">
      <Card className="relative w-full max-w-2xl overflow-hidden border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]">
        <div className="grid gap-6 p-8">
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="live">{restart ? 'Swarm setup' : 'First launch'}</Badge>
              <span className="text-xs uppercase tracking-[0.2em] text-[var(--app-text-muted)]">
                {step === 'identity' ? 'Step 1 of 4' : step === 'child-bootstrap' ? 'Step 2 of 4' : step === 'security' ? 'Step 3 of 4' : 'Step 4 of 4'}
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
                  This is the label Swarm shows for this machine in discovery and networking screens.
                </p>
              </div>

              <div className="grid gap-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="text-sm font-semibold text-[var(--app-text)]">Swarm role</h2>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  {[
                    {
                      id: 'master',
                      title: 'Master Swarm',
                      text: 'Master is the default. This Swarm owns approval and desired pairing state for the group.',
                    },
                    {
                      id: 'child',
                      title: 'Child Swarm',
                      text: 'This Swarm requests secure pairing to a Primary over LAN, Tailscale, or mixed reachability.',
                    },
                  ].map((option) => (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => setSwarmRole(option.id as SwarmRole)}
                      className={`grid gap-2 rounded-2xl border px-4 py-4 text-left transition ${
                        swarmRole === option.id
                          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
                      }`}
                    >
                      <strong className="text-sm text-[var(--app-text)]">{option.title}</strong>
                      <span className="text-sm leading-6 text-[var(--app-text-muted)]">{option.text}</span>
                    </button>
                  ))}
                </div>
              </div>

              {swarmRole === 'master' ? (
                <div className="grid gap-2">
                  <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-group-name">
                    Swarm group name
                  </label>
                  <Input
                    id="desktop-onboarding-group-name"
                    value={groupName}
                    onChange={(event) => {
                      setGroupName(event.target.value)
                      setGroupNameTouched(true)
                    }}
                    placeholder={suggestedGroupName(status, swarmName) || 'Main Swarm Group'}
                  />
                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                    This names the current group that this machine hosts when Swarm Mode is turned on.
                  </p>
                  <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3">
                    <div className="text-[11px] uppercase tracking-[0.18em] text-[var(--app-text-muted)]">Group network</div>
                    <div className="mt-2 break-all text-sm font-medium text-[var(--app-text)]">{groupNetworkPreview}</div>
                    <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">
                      This network name is assigned from the group when the master is created and then reused for Podman and Docker local children.
                    </p>
                  </div>
                </div>
              ) : null}

              <div className="grid gap-3">
                <div className="flex flex-wrap items-center gap-2">
                  <h2 className="text-sm font-semibold text-[var(--app-text)]">Reachability</h2>
                  {status.network.tailscale.connected ? (
                    <Badge tone="live">
                      Tailscale detected: {status.network.tailscale.dnsName || status.network.tailscale.ips[0] || 'connected'}
                    </Badge>
                  ) : null}
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  {[
                    { id: 'lan', title: 'Use LAN', text: 'Use local network transport for nearby devices and containers.' },
                    { id: 'tailscale', title: 'Use Tailscale', text: 'Use the canonical saved Tailscale URL for tailnet reachability.' },
                  ].map((option) => (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => setMode(option.id as BootstrapMode)}
                      className={`grid gap-2 rounded-2xl border px-4 py-4 text-left transition ${
                        mode === option.id
                          ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
                      }`}
                    >
                      <strong className="text-sm text-[var(--app-text)]">{option.title}</strong>
                      <span className="text-sm leading-6 text-[var(--app-text-muted)]">{option.text}</span>
                    </button>
                  ))}
                </div>
              </div>

              {mode === 'tailscale' ? (
                <div className="grid gap-2">
                  <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-tailscale-url">
                    Tailscale URL
                  </label>
                  <Input
                    id="desktop-onboarding-tailscale-url"
                    value={tailscaleURL}
                    onChange={(event) => setTailscaleURL(event.target.value)}
                    placeholder={suggestedTailscaleURL(status, mode) || 'https://example.ts.net'}
                  />
                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                    This value is saved as `tailscale_url` and becomes the durable bootstrap endpoint.
                  </p>
                </div>
              ) : (
                <div className="grid gap-2">
                  <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-advertise-host">
                    LAN Advertise Host
                  </label>
                  <Input
                    id="desktop-onboarding-advertise-host"
                    value={advertiseHost}
                    onChange={(event) => setAdvertiseHost(event.target.value)}
                    placeholder={suggestedAdvertiseHost(status) || '192.168.1.20'}
                  />
                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                    Swarm auto-detects a LAN host for normal setups. Local Add Swarm containers also use this advertised host when they call back to the master, but the master backend must also be bound to a non-loopback `host` in `swarm.conf`. Change this only when you need to override the advertised LAN endpoint for containers or advanced networking. It is saved as `advertise_host` in `swarm.conf`.
                  </p>
                  <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-api-port">
                    Backend API Port
                  </label>
                  <Input
                    id="desktop-onboarding-api-port"
                    value={apiPort}
                    onChange={(event) => setAPIPort(event.target.value)}
                    inputMode="numeric"
                    placeholder="7781"
                  />
                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                    This is the port the backend listens on. Default is `7781`. Changing it requires a restart and is saved as `port` in `swarm.conf`.
                  </p>
                  <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-advertise-port">
                    LAN Advertise Port
                  </label>
                  <Input
                    id="desktop-onboarding-advertise-port"
                    value={advertisePort}
                    onChange={(event) => setAdvertisePort(event.target.value)}
                    inputMode="numeric"
                    placeholder="7781"
                  />
                  <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                    By default this matches the backend API port. Local Add Swarm containers use this advertised backend port, and new child containers continue from the next host port pair. Change it only when other machines or sibling containers should reach this Swarm on a different LAN port. It is saved as `advertise_port` in `swarm.conf`.
                  </p>
                  {status.network.lanAddresses.length > 0 ? (
                    <div className="text-sm leading-6 text-[var(--app-text-muted)]">
                      Detected LAN targets: {status.network.lanAddresses.map((address) => `${address}:${advertisePort.trim() || '7781'}`).join(', ')}
                    </div>
                  ) : null}
                </div>
              )}

              <div className="flex justify-end">
                <Button type="button" onClick={() => void handleIdentityContinue()} disabled={submitting}>
                  {submitting ? 'Saving…' : 'Continue'}
                </Button>
              </div>
            </div>
          ) : null}

          {step === 'child-bootstrap' ? (
            <div className="grid gap-6">
              <div className="grid gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <div className="text-sm text-[var(--app-text-muted)]">
                  <div><strong className="text-[var(--app-text)]">Pairing status:</strong> {status.pairing.pairingState || 'unpaired'}</div>
                  <div className="mt-2"><strong className="text-[var(--app-text)]">Reachability:</strong> {mode.toUpperCase()}</div>
                  <div className="mt-2">Transport is reachability only. Trust activates only after Primary approval.</div>
                </div>
                {status.pairing.rendezvousTransports.length > 0 ? (
                  <div className="flex flex-wrap gap-2">
                    {status.pairing.rendezvousTransports.map((transport) => (
                      <Badge key={`${transport.kind}-${transport.primary}`} tone="live">
                        {transport.kind}: {transport.primary || transport.all[0] || 'available'}
                      </Badge>
                    ))}
                  </div>
                ) : null}
              </div>

              <div className="grid gap-2">
                <label className="text-xs font-medium uppercase tracking-[0.18em] text-[var(--app-text-muted)]" htmlFor="desktop-onboarding-invite-token">
                  Invite token
                </label>
                <Input
                  id="desktop-onboarding-invite-token"
                  autoFocus
                  value={inviteToken}
                  onChange={(event) => setInviteToken(event.target.value)}
                  placeholder="paste invite token from Primary"
                />
                <p className="text-sm leading-6 text-[var(--app-text-muted)]">
                  Use a token created on the Primary. This works for LAN-only, Tailscale-only, or mixed paths.
                </p>
              </div>

              <div className="flex items-center justify-between gap-3">
                <Button type="button" variant="outline" onClick={() => setStep('identity')} disabled={submitting}>
                  Back
                </Button>
                <Button type="button" onClick={() => void handleChildBootstrapContinue()} disabled={submitting}>
                  {submitting ? 'Requesting…' : 'Request pairing'}
                </Button>
              </div>
            </div>
          ) : null}

          {step === 'security' ? (
            <div className="grid gap-6">
              <div className="grid gap-3 sm:grid-cols-3">
                {[
                  {
                    id: 'setup',
                    title: 'Set up vault now',
                    text: 'Encrypt saved provider credentials on this machine.',
                  },
                  {
                    id: 'import',
                    title: 'Import existing vault',
                    text: 'Bring in an exported Swarm vault bundle and keep your saved credentials.',
                  },
                  {
                    id: 'skip',
                    title: 'Skip for now',
                    text: 'Keep moving and add vault protection later from Settings.',
                  },
                ].map((option) => (
                  <button
                    key={option.id}
                    type="button"
                    onClick={() => {
                      setSecurityChoice(option.id as SecurityChoice)
                      setError(null)
                      setNotice(null)
                    }}
                    className={`grid gap-2 rounded-2xl border px-4 py-4 text-left transition ${
                      securityChoice === option.id
                        ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                        : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
                    }`}
                  >
                    <strong className="text-sm text-[var(--app-text)]">{option.title}</strong>
                    <span className="text-sm leading-6 text-[var(--app-text-muted)]">{option.text}</span>
                  </button>
                ))}
              </div>

              {securityChoice === 'setup' ? (
                <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                  <Input type="password" value={vaultPassword} onChange={(event) => setVaultPassword(event.target.value)} placeholder="Vault password" />
                  <Input type="password" value={vaultConfirm} onChange={(event) => setVaultConfirm(event.target.value)} placeholder="Confirm vault password" />
                  <div className="flex justify-end">
                    <Button type="button" onClick={() => void handleEnableVault()} disabled={submitting || status.vault.enabled}>
                      {status.vault.enabled ? 'Vault ready' : submitting ? 'Enabling…' : 'Enable vault'}
                    </Button>
                  </div>
                </div>
              ) : null}

              {securityChoice === 'import' ? (
                <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                  <input ref={fileInputRef} type="file" accept=".swarmvault,application/octet-stream" className="hidden" onChange={(event) => void handleImportFileSelected(event)} />
                  <div className="flex flex-wrap items-center gap-3">
                    <Button type="button" variant="outline" onClick={() => fileInputRef.current?.click()} disabled={submitting}>
                      {importBundleName ? 'Choose another file' : 'Choose vault bundle'}
                    </Button>
                    {importBundleName ? <span className="text-sm text-[var(--app-text-muted)]">{importBundleName}</span> : null}
                  </div>
                  <Input type="password" value={importPassword} onChange={(event) => setImportPassword(event.target.value)} placeholder="Bundle password" />
                  <Input type="password" value={importVaultPassword} onChange={(event) => setImportVaultPassword(event.target.value)} placeholder="Vault password (optional)" />
                  <div className="flex justify-end">
                    <Button type="button" onClick={() => void handleImportBundle()} disabled={submitting}>
                      {submitting ? 'Importing…' : 'Import vault'}
                    </Button>
                  </div>
                </div>
              ) : null}

              <div className="flex items-center justify-between gap-3">
                <Button type="button" variant="outline" onClick={() => setStep(swarmRole === 'child' ? 'child-bootstrap' : 'identity')} disabled={submitting}>
                  Back
                </Button>
                <Button type="button" onClick={() => void handleSecurityContinue()} disabled={submitting}>
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
                      onChange={(event) => setProviderID(event.target.value)}
                      className="h-11 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-sm text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]"
                    >
                      {providerOptions.map((provider) => (
                        <option key={provider.id} value={provider.id}>
                          {provider.id}
                        </option>
                      ))}
                    </select>
                  </div>

                  {!providerAlreadyConnected && selectedManualMethod ? (
                    <div className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                      <Input
                        type="password"
                        value={credentialValue}
                        onChange={(event) => setCredentialValue(event.target.value)}
                        placeholder={credentialLabel(selectedManualMethod)}
                      />
                      <div className="flex flex-wrap justify-end gap-3">
                        {supportsCodexOAuth(selectedProvider) ? (
                          <Button type="button" variant="outline" onClick={() => void handleStartOAuth()} disabled={submitting}>
                            Sign in with browser
                          </Button>
                        ) : null}
                        <Button type="button" onClick={() => void handleProviderSave()} disabled={submitting}>
                          {submitting ? 'Saving…' : 'Save provider'}
                        </Button>
                      </div>
                    </div>
                  ) : null}
                </>
              ) : (
                <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-4 text-sm leading-6 text-[var(--app-text-muted)]">
                  No providers are available yet. Finish onboarding and connect one later from Settings.
                </div>
              )}

              <div className="flex items-center justify-between gap-3">
                <Button type="button" variant="outline" onClick={() => setStep('security')} disabled={submitting}>
                  Back
                </Button>
                <Button type="button" onClick={() => void finalizeOnboarding()} disabled={submitting}>
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
