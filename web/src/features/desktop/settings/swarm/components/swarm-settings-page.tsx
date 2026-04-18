import { useEffect, useState, type ChangeEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import {
  fetchDesktopOnboardingStatus,
  saveDesktopOnboarding,
} from '../../../onboarding/api'
import type { DesktopOnboardingStatus } from '../../../onboarding/types'
import { ContainerProfilesPanel } from '../../../containers/components/container-profiles-panel'
import { saveSwarmSettings } from '../mutations/save-swarm-settings'
import { getUISettings } from '../queries/get-ui-settings'
import { DEFAULT_SWARM_NAME, normalizeDefaultNewSessionMode, normalizeSwarmName, normalizeSwarmSettings } from '../types/swarm-settings'
import type { UISettingsWire } from '../types/swarm-settings'
import { Input } from '../../../../../components/ui/input'
import { Button } from '../../../../../components/ui/button'

function suggestedTailscaleURL(status: DesktopOnboardingStatus | null): string {
  if (!status) {
    return ''
  }
  return status.config.tailscaleURL || status.network.tailscale.candidateURL || status.network.tailscale.tailnetURL || status.network.tailscale.dnsName || status.network.tailscale.ips[0] || ''
}

function parseTransportPort(value: string): number {
  if (!/^\d+$/.test(value.trim())) {
    throw new Error('Peer transport port must be a whole number.')
  }
  const parsed = Number.parseInt(value.trim(), 10)
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error('Peer transport port must be between 1 and 65535.')
  }
  return parsed
}

function suggestedAdvertiseHost(status: DesktopOnboardingStatus | null): string {
  if (!status) {
    return ''
  }
  return status.config.advertiseHost || status.network.lanAddresses[0] || ''
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

function isLoopbackHost(value: string | null | undefined): boolean {
  const normalized = String(value ?? '').trim().toLowerCase()
  return normalized === '127.0.0.1' || normalized === 'localhost' || normalized === '::1' || normalized === '[::1]'
}

export function SwarmSettingsPage() {
  const queryClient = useQueryClient()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [status, setStatus] = useState<string | null>(null)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const [uiSettings, setUISettings] = useState<UISettingsWire | null>(null)
  const [onboardingStatus, setOnboardingStatus] = useState<DesktopOnboardingStatus | null>(null)
  const [name, setName] = useState(DEFAULT_SWARM_NAME)
  const [defaultNewSessionMode, setDefaultNewSessionMode] = useState<'auto' | 'plan'>('auto')
  const [swarmMode, setSwarmMode] = useState(false)
  const [swarmRole, setSwarmRole] = useState<'master' | 'child'>('master')
  const [mode, setMode] = useState<'lan' | 'tailscale'>('lan')
  const [apiPort, setAPIPort] = useState('7781')
  const [advertiseHost, setAdvertiseHost] = useState('')
  const [advertisePort, setAdvertisePort] = useState('7781')
  const [tailscaleURL, setTailscaleURL] = useState('')
  const [localTransportPort, setLocalTransportPort] = useState('7790')
  const [peerTransportPort, setPeerTransportPort] = useState('7791')

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setStatus(null)

    void Promise.all([getUISettings(), fetchDesktopOnboardingStatus()])
      .then(([uiPayload, onboardingPayload]) => {
        if (cancelled) {
          return
        }
        const nextIdentity = normalizeSwarmSettings(uiPayload)
        const nextRole = onboardingPayload.config.swarmRole === 'child' ? 'child' : 'master'
        const nextMode = onboardingPayload.config.mode
        setUISettings(uiPayload)
        setOnboardingStatus(onboardingPayload)
        setName(nextIdentity.name)
        setDefaultNewSessionMode(nextIdentity.defaultNewSessionMode)
        setSwarmMode(onboardingPayload.config.swarmMode)
        setSwarmRole(nextRole)
        setMode(nextMode)
        setAPIPort(String(onboardingPayload.config.port))
        setAdvertiseHost(onboardingPayload.config.advertiseHost || suggestedAdvertiseHost(onboardingPayload))
        setAdvertisePort(String(onboardingPayload.config.advertisePort))
        setTailscaleURL(onboardingPayload.config.tailscaleURL || suggestedTailscaleURL(onboardingPayload))
        setLocalTransportPort(String(onboardingPayload.config.localTransportPort || 7790))
        setPeerTransportPort(String(onboardingPayload.config.peerTransportPort || 7791))
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load swarm settings')
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    if (mode === 'tailscale') {
      setTailscaleURL((current) => current || suggestedTailscaleURL(onboardingStatus))
    }
  }, [mode, onboardingStatus])

  useEffect(() => {
    if (mode !== 'lan') {
      return
    }
    setAdvertiseHost((current) => current || suggestedAdvertiseHost(onboardingStatus))
    setAdvertisePort((current) => current || String(onboardingStatus?.config.advertisePort || onboardingStatus?.config.port || 7781))
  }, [mode, onboardingStatus])

  const submit = async () => {
    if (!uiSettings || !onboardingStatus) {
      setError('Swarm settings are not loaded yet.')
      return
    }

    const normalizedName = normalizeSwarmName(name)
    const normalizedAPIPort = swarmMode ? parseAPIPort(apiPort) : onboardingStatus.config.port
    const normalizedAdvertisePort = swarmMode
      ? (advertisePort.trim() ? parseAPIPort(advertisePort) : onboardingStatus.config.advertisePort || normalizedAPIPort)
      : onboardingStatus.config.advertisePort
    const normalizedAdvertiseHost = swarmMode && advertiseHost.trim() ? parseAdvertiseHost(advertiseHost) : ''
    const normalizedTailscaleURL = swarmMode ? tailscaleURL.trim() : onboardingStatus.config.tailscaleURL
    const normalizedLocalTransportPort = parseTransportPort(localTransportPort)
    const normalizedPeerTransportPort = swarmMode ? parseTransportPort(peerTransportPort) : onboardingStatus.config.peerTransportPort

    if (swarmMode && mode === 'tailscale' && !normalizedTailscaleURL) {
      setError('Choose or enter a Tailscale URL.')
      return
    }

    setSaving(true)
    setError(null)
    setStatus(null)
    try {
      const nextSwarm = await saveSwarmSettings({
        current: uiSettings,
        name: normalizedName,
        defaultNewSessionMode,
      })
      const nextOnboarding = await saveDesktopOnboarding({
        swarmName: normalizedName,
        swarmMode,
        child: swarmMode ? swarmRole === 'child' : false,
        ...(swarmMode ? {
          mode,
          port: normalizedAPIPort,
          advertiseHost: normalizedAdvertiseHost,
          advertisePort: normalizedAdvertisePort,
          tailscaleURL: normalizedTailscaleURL,
          localTransportPort: normalizedLocalTransportPort,
          peerTransportPort: normalizedPeerTransportPort,
        } : {}),
      })
      const nextPayload: UISettingsWire = {
        ...uiSettings,
        chat: {
          ...(uiSettings.chat ?? {}),
          default_new_session_mode: normalizeDefaultNewSessionMode(defaultNewSessionMode),
        },
        swarm: {
          ...(uiSettings.swarm ?? {}),
          name: nextSwarm.name,
        },
        updated_at: nextSwarm.updatedAt,
      }
      setUISettings(nextPayload)
      setOnboardingStatus(nextOnboarding)
      setName(nextSwarm.name)
      setDefaultNewSessionMode(nextSwarm.defaultNewSessionMode)
      setSwarmMode(nextOnboarding.config.swarmMode)
      setSwarmRole(nextOnboarding.config.swarmRole === 'child' ? 'child' : 'master')
      setMode(nextOnboarding.config.mode || mode)
      setAPIPort(String(nextOnboarding.config.port))
      setAdvertiseHost(nextOnboarding.config.advertiseHost || suggestedAdvertiseHost(nextOnboarding))
      setAdvertisePort(String(nextOnboarding.config.advertisePort))
      setTailscaleURL(nextOnboarding.config.tailscaleURL || '')
      setLocalTransportPort(String(nextOnboarding.config.localTransportPort || 7790))
      setPeerTransportPort(String(nextOnboarding.config.peerTransportPort || 7791))
      setStatus(
        nextOnboarding.config.restartRequired
          ? `Saved swarm settings for ${nextSwarm.name}. Restart required: ${nextOnboarding.config.restartReason || 'transport settings changed'}.`
          : (swarmMode ? `Saved swarm settings for ${nextSwarm.name}.` : `Saved ${nextSwarm.name} for standalone local use.`),
      )
      queryClient.setQueryData(['ui-settings', 'swarm'], nextSwarm)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save swarm settings')
    } finally {
      setSaving(false)
    }
  }

  const peerTransportServeCommand = `tailscale serve --bg http://127.0.0.1:${peerTransportPort.trim() || '7791'}`
  const localOnlyBind = isLoopbackHost(onboardingStatus?.config.host)

  const handleCopyPeerTransportCommand = async () => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(peerTransportServeCommand)
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="mb-8">
        <h1 className="text-xl font-semibold text-[var(--app-text)]">Swarm Identity</h1>
        <p className="mt-1 text-sm text-[var(--app-text-muted)]">
          Manage how your local instance connects to the broader network.
        </p>
      </div>

      {error ? <div className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-4 py-3 text-sm text-[var(--app-danger)]">{error}</div> : null}
      {status ? <div className="mb-4 rounded-xl border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-4 py-3 text-sm text-[var(--app-success)]">{status}</div> : null}

      <div className="space-y-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Swarm Mode</label>
          <div className="grid gap-3 sm:grid-cols-2 max-w-3xl">
            <button
              type="button"
              onClick={() => setSwarmMode(false)}
              disabled={loading || saving}
              className={`grid gap-2 rounded-2xl border px-4 py-4 text-left transition ${
                !swarmMode
                  ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                  : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
              }`}
            >
              <strong className="text-sm text-[var(--app-text)]">Standalone</strong>
              <span className="text-sm leading-6 text-[var(--app-text-muted)]">
                Default local use. `swarm` and `swarm --desktop` just run normally and this node does not participate in shared transport.
              </span>
            </button>
            <button
              type="button"
              onClick={() => setSwarmMode(true)}
              disabled={loading || saving}
              className={`grid gap-2 rounded-2xl border px-4 py-4 text-left transition ${
                swarmMode
                  ? 'border-[var(--app-primary)] bg-[color-mix(in_oklab,var(--app-primary)_12%,var(--app-surface))]'
                  : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] hover:border-[var(--app-border-strong)]'
              }`}
            >
              <strong className="text-sm text-[var(--app-text)]">Swarm Mode On</strong>
              <span className="text-sm leading-6 text-[var(--app-text-muted)]">
                Opt into master/child role, LAN or Tailscale reachability, and durable advertised endpoints.
              </span>
            </button>
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Device Name</label>
          <Input
            type="text"
            value={name}
            onChange={(event: ChangeEvent<HTMLInputElement>) => setName(event.target.value)}
            disabled={loading || saving}
            placeholder={DEFAULT_SWARM_NAME}
            autoComplete="off"
            className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
          />
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Default new chat mode</label>
          <select
            value={defaultNewSessionMode}
            onChange={(event) => setDefaultNewSessionMode(event.target.value as 'auto' | 'plan')}
            disabled={loading || saving}
            className="w-full max-w-md h-10 px-3 rounded-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
          >
            <option value="auto">Auto</option>
            <option value="plan">Plan</option>
          </select>
          <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
            This only changes the default for new chats. Existing chats can still enter or exit plan mode independently.
          </p>
        </div>

        {swarmMode ? (
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Swarm role</label>
          <select
            value={swarmRole}
            onChange={(e) => setSwarmRole(e.target.value as 'master' | 'child')}
            disabled={loading || saving}
            className="w-full max-w-md h-10 px-3 rounded-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
          >
            <option value="master">Master Swarm</option>
            <option value="child">Child Swarm</option>
          </select>
          <p className="max-w-md text-sm text-[var(--app-text-muted)]">
            LAN-only, Tailscale-only, and mixed transport are all supported. Transport is not trust.
          </p>
        </div>
        ) : (
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
            Swarm Mode is currently off. This machine stays standalone and local-only until you turn it on.
          </div>
        )}

        {swarmMode ? (
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-[var(--app-text)]">Reachability</label>
          <select
            value={mode}
            onChange={(e) => setMode(e.target.value as 'lan' | 'tailscale')}
            disabled={loading || saving}
            className="w-full max-w-md h-10 px-3 rounded-md bg-[var(--app-surface-subtle)] border border-[var(--app-border)] text-sm text-[var(--app-text)] outline-none transition-colors hover:border-[var(--app-border-strong)] focus:border-[var(--app-primary)] focus:ring-1 focus:ring-[var(--app-primary)]"
          >
            <option value="lan">Use LAN</option>
            <option value="tailscale">Use Tailscale</option>
          </select>
        </div>
        ) : null}

        {swarmMode && mode === 'lan' ? (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-[var(--app-text)]">LAN advertise host</label>
            <Input
              type="text"
              value={advertiseHost}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setAdvertiseHost(event.target.value)}
              disabled={loading || saving}
              placeholder={suggestedAdvertiseHost(onboardingStatus) || '192.168.1.20'}
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
              Swarm auto-detects a LAN host for normal setups. Local Add Swarm containers also use this advertised host when they call back to the master, but the master backend must also be bound to a non-loopback `host` in `swarm.conf`. Change this only when you need to override the advertised LAN endpoint for containers or advanced networking. It is saved as `advertise_host` in `swarm.conf`.
            </p>
            <label className="text-sm font-medium text-[var(--app-text)]">Backend API port</label>
            <Input
              type="text"
              value={apiPort}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setAPIPort(event.target.value)}
              inputMode="numeric"
              disabled={loading || saving}
              placeholder="7781"
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
              This is the backend bind port. Default is `7781`. Changing it requires a restart and is saved as `port` in `swarm.conf`.
            </p>
            <label className="text-sm font-medium text-[var(--app-text)]">LAN advertise port</label>
            <Input
              type="text"
              value={advertisePort}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setAdvertisePort(event.target.value)}
              inputMode="numeric"
              disabled={loading || saving}
              placeholder="7781"
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
              By default this matches the backend API port. Local Add Swarm containers use this advertised backend port, and new child containers continue from the next host port pair. Change it only when other machines or sibling containers should reach this Swarm on a different LAN port. It is saved as `advertise_port` in `swarm.conf`.
            </p>
            {onboardingStatus?.network.lanAddresses.length ? (
              <div className="max-w-xl text-sm text-[var(--app-text-muted)]">
                Detected LAN targets: {onboardingStatus.network.lanAddresses.map((address) => `${address}:${advertisePort.trim() || '7781'}`).join(', ')}
              </div>
            ) : null}
          </div>
        ) : null}

        {swarmMode ? (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-[var(--app-text)]">
              Local child transport port
            </label>
            <Input
              type="text"
              value={localTransportPort}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setLocalTransportPort(event.target.value)}
              inputMode="numeric"
              disabled={loading || saving}
              placeholder="7790"
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
              This dedicated peer transport is what local child containers use when the main backend stays on localhost. It is saved as `local_transport_port` in `swarm.conf` and changing it requires a restart.
            </p>
            <div className="max-w-xl text-sm text-[var(--app-text-muted)]">
              {localOnlyBind
                ? (onboardingStatus?.config.localTransportActive ? 'Status: active for local child containers.' : 'Status: required because the main backend is localhost-only, but not active yet.')
                : 'Status: not required while the main backend is already network reachable.'}
            </div>
            {onboardingStatus?.config.localTransportWarning ? (
              <div className="max-w-2xl rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">
                {onboardingStatus.config.localTransportWarning}
              </div>
            ) : null}
          </div>
        ) : null}

        {swarmMode && mode === 'tailscale' ? (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-[var(--app-text)]">
              Tailscale URL
            </label>
            <Input
              type="text"
              value={tailscaleURL}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setTailscaleURL(event.target.value)}
              disabled={loading || saving}
              placeholder={suggestedTailscaleURL(onboardingStatus) || 'https://example.ts.net'}
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <label className="text-sm font-medium text-[var(--app-text)]">
              Peer transport port
            </label>
            <Input
              type="text"
              value={peerTransportPort}
              onChange={(event: ChangeEvent<HTMLInputElement>) => setPeerTransportPort(event.target.value)}
              inputMode="numeric"
              disabled={loading || saving}
              placeholder="7791"
              className="w-full max-w-md bg-[var(--app-surface-subtle)] border-[var(--app-border)] text-[var(--app-text)]"
            />
            <p className="max-w-xl text-sm text-[var(--app-text-muted)]">
              This is the dedicated peer transport listener that Tailscale Serve or SSH can forward to when you want remote swarm transport without hosting the desktop UI. It is saved as `peer_transport_port` in `swarm.conf` and changing it requires a restart.
            </p>
            <div className="max-w-2xl rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
              <div><strong className="text-[var(--app-text)]">How to enable the transport</strong></div>
              <ol className="mt-2 list-decimal space-y-1 pl-5">
                <li>Turn on Tailscale transport and save these settings.</li>
                <li>Restart Swarm.</li>
                <li>Run the command below.</li>
                <li>Confirm with <code className="rounded bg-[var(--app-surface)] px-1 py-0.5">tailscale serve status</code>.</li>
              </ol>
              <div className="mt-3 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
                <div className="break-all font-mono text-xs text-[var(--app-text)]">{peerTransportServeCommand}</div>
                <div className="mt-3 flex items-center gap-2">
                  <Button type="button" variant="outline" size="sm" onClick={() => void handleCopyPeerTransportCommand()}>
                    {copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Copy failed' : 'Copy command'}
                  </Button>
                </div>
              </div>
              <p className="mt-2 text-xs">
                Hosting the full Swarm UI over your tailnet is a separate Tailscale Serve choice and is shown live on the Swarm dashboard.
              </p>
            </div>
          </div>
        ) : null}

        {onboardingStatus ? (
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm text-[var(--app-text-muted)]">
            <div><strong className="text-[var(--app-text)]">Swarm mode:</strong> {onboardingStatus.config.swarmMode ? 'enabled' : 'standalone/local only'}</div>
            <div><strong className="text-[var(--app-text)]">Swarm ID:</strong> {onboardingStatus.config.swarmID || 'not assigned yet'}</div>
            {onboardingStatus.config.swarmMode ? (
              <>
                <div className="mt-1"><strong className="text-[var(--app-text)]">Bootstrap mode:</strong> {onboardingStatus.config.mode}</div>
                <div className="mt-1"><strong className="text-[var(--app-text)]">Backend API port:</strong> {onboardingStatus.config.port}</div>
                <div className="mt-1"><strong className="text-[var(--app-text)]">Local child transport:</strong> {onboardingStatus.config.localTransportPort}{onboardingStatus.config.localTransportActive ? ' (active)' : localOnlyBind ? ' (inactive)' : ' (not needed)'}</div>
              </>
            ) : null}
            {onboardingStatus.config.swarmMode && onboardingStatus.config.advertiseHost ? (
              <div className="mt-1"><strong className="text-[var(--app-text)]">LAN advertise endpoint:</strong> {onboardingStatus.config.advertiseHost}:{onboardingStatus.config.advertisePort}</div>
            ) : null}
            {onboardingStatus.config.swarmMode && onboardingStatus.config.mode === 'tailscale' && onboardingStatus.config.tailscaleURL ? (
              <>
                <div className="mt-1 break-all"><strong className="text-[var(--app-text)]">Tailscale URL:</strong> {onboardingStatus.config.tailscaleURL}</div>
                <div className="mt-1"><strong className="text-[var(--app-text)]">Peer transport port:</strong> {onboardingStatus.config.peerTransportPort}</div>
              </>
            ) : null}
          </div>
        ) : null}

        <div className="mt-8 pt-6 border-t border-[var(--app-border)] flex items-center justify-between">
          <div className="flex items-center gap-2 text-[var(--app-text-muted)] text-sm">
            {!swarmMode ? 'Standalone local use' : onboardingStatus?.network.tailscale.connected ? 'Tailscale Connected' : mode === 'tailscale' ? 'Tailscale configured' : 'LAN Ready'}
          </div>
          <Button
            className="border border-[var(--app-primary)] bg-transparent text-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)] hover:border-[var(--app-primary)] active:bg-[var(--app-surface-hover)]"
            onClick={() => void submit()}
            disabled={loading || saving}
          >
            {saving ? 'Saving…' : loading ? 'Loading…' : swarmMode ? 'Save Swarm Mode' : 'Save Standalone Mode'}
          </Button>
        </div>

        <div className="pt-6 border-t border-[var(--app-border)]">
          <ContainerProfilesPanel variant="compact" />
        </div>
      </div>
    </div>
  )
}
