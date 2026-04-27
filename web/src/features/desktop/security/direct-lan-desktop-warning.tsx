import { ShieldAlert } from 'lucide-react'
import { Card } from '../../../components/ui/card'

export interface DirectLANDesktopWarning {
  host: string
}

export function getDirectLANDesktopWarning(): DirectLANDesktopWarning | null {
  if (typeof window === 'undefined') {
    return null
  }
  const host = normalizeBrowserHost(window.location.hostname)
  if (!host || isLoopbackBrowserHost(host)) {
    return null
  }
  if (window.location.protocol === 'https:' && isTailscaleHost(host)) {
    return null
  }
  if (!isPrivateBrowserHost(host) && window.location.protocol !== 'http:') {
    return null
  }
  return { host }
}

export function DirectLANDesktopWarningScreen({ warning }: { warning: DirectLANDesktopWarning }) {
  return (
    <div className="absolute inset-0 flex items-center justify-center bg-[var(--app-bg)] px-6 text-[var(--app-text)]">
      <Card className="w-full max-w-2xl border-[var(--app-warning-border)] bg-[var(--app-surface)] p-6 shadow-[var(--shadow-panel)]">
        <div className="flex items-start gap-3">
          <div className="grid h-10 w-10 shrink-0 place-items-center rounded-lg border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning)]">
            <ShieldAlert size={22} />
          </div>
          <div className="min-w-0">
            <div className="text-lg font-semibold">Direct LAN desktop is disabled for this MVP</div>
            <p className="mt-2 text-sm leading-6 text-[var(--app-text-muted)]">
              This browser opened Swarm through the network address <span className="font-mono text-[var(--app-text)]">{warning.host}</span>.
              Swarm does not yet have a safe LAN desktop pairing flow, so desktop API auth will reject this access path.
            </p>
            <p className="mt-3 text-sm leading-6 text-[var(--app-text-muted)]">
              Keep <span className="font-mono text-[var(--app-text)]">host = 127.0.0.1</span> in <span className="font-mono text-[var(--app-text)]">swarm.conf</span>,
              then connect from another device with an SSH tunnel to the desktop port, or use Tailscale.
              Tailscale is usually the lower-friction secure option.
            </p>
            <p className="mt-3 text-xs leading-5 text-[var(--app-text-subtle)]">
              A browser "Not Secure" warning means the page is plain HTTP. That warning is separate from Swarm auth; direct private-LAN HTTP is not the supported secure path for launch.
            </p>
          </div>
        </div>
      </Card>
    </div>
  )
}

function normalizeBrowserHost(host: string): string {
  return String(host ?? '').trim().toLowerCase().replace(/^\[/, '').replace(/\]$/, '')
}

function isLoopbackBrowserHost(host: string): boolean {
  return host === 'localhost' || host === '::1' || host.startsWith('127.')
}

function isPrivateBrowserHost(host: string): boolean {
  if (host === '::1') {
    return false
  }
  if (host.startsWith('fc') || host.startsWith('fd') || host.startsWith('fe80:')) {
    return true
  }
  const parts = host.split('.')
  if (parts.length !== 4) {
    return false
  }
  const octets = parts.map((part) => Number.parseInt(part, 10))
  if (octets.some((octet, index) => !Number.isFinite(octet) || String(octet) !== parts[index] || octet < 0 || octet > 255)) {
    return false
  }
  const a = octets[0] ?? -1
  const b = octets[1] ?? -1
  return a === 10
    || (a === 172 && b >= 16 && b <= 31)
    || (a === 192 && b === 168)
    || (a === 169 && b === 254)
}

function isTailscaleHost(host: string): boolean {
  return host.endsWith('.ts.net')
}
