export const TAILSCALE_ROUTE_CLASSIFICATIONS = [
  'verified_swarm_desktop',
  'wrong_target',
  'funnel_enabled',
  'unsupported_handler',
  'invalid',
  'unconfigured',
  'incompatible',
  'unavailable',
] as const

export type TailscaleRouteClassification = (typeof TAILSCALE_ROUTE_CLASSIFICATIONS)[number]

export interface TailscaleSettingsRoute {
  origin: string
  authority: string
  proxy_target: string
  classification: TailscaleRouteClassification
  reason: string
  approved: boolean
  active: boolean
}

export interface TailscaleSettingsStatus {
  state: TailscaleRouteClassification
  approvals: string[]
  revision: number
  updated_at?: number
  routes: TailscaleSettingsRoute[]
  self_dns_name?: string
  effective_target?: string
  remediation?: string
  detection_error?: string
}

const CLASSIFICATION_SET = new Set<string>(TAILSCALE_ROUTE_CLASSIFICATIONS)

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function classificationValue(value: unknown): TailscaleRouteClassification {
  return typeof value === 'string' && CLASSIFICATION_SET.has(value)
    ? value as TailscaleRouteClassification
    : 'incompatible'
}

function routeValue(value: unknown): TailscaleSettingsRoute | null {
  if (!value || typeof value !== 'object') return null
  const wire = value as Record<string, unknown>
  return {
    origin: stringValue(wire.origin),
    authority: stringValue(wire.authority),
    proxy_target: stringValue(wire.proxy_target),
    classification: classificationValue(wire.classification),
    reason: stringValue(wire.reason),
    approved: wire.approved === true,
    active: wire.active === true,
  }
}

export function mapTailscaleSettingsStatus(value: unknown): TailscaleSettingsStatus {
  if (!value || typeof value !== 'object') throw new Error('Tailscale settings returned an invalid response')
  const wire = value as Record<string, unknown>
  const routes = Array.isArray(wire.routes) ? wire.routes.map(routeValue).filter((route): route is TailscaleSettingsRoute => route !== null) : []
  return {
    state: classificationValue(wire.state),
    approvals: Array.isArray(wire.approvals) ? wire.approvals.map(stringValue).filter(Boolean) : [],
    revision: typeof wire.revision === 'number' && Number.isFinite(wire.revision) ? wire.revision : 0,
    updated_at: typeof wire.updated_at === 'number' ? wire.updated_at : undefined,
    routes,
    self_dns_name: stringValue(wire.self_dns_name) || undefined,
    effective_target: stringValue(wire.effective_target) || undefined,
    remediation: stringValue(wire.remediation) || undefined,
    detection_error: stringValue(wire.detection_error) || undefined,
  }
}

export function normalizeTailscaleOriginInput(raw: string): string | null {
  try {
    const parsed = new URL(raw.trim())
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) return null
    if (parsed.pathname !== '/' || (parsed.port && parsed.port !== '443')) return null
    const host = parsed.hostname.toLowerCase().replace(/\.$/, '')
    if (!host || host === 'ts.net' || !host.endsWith('.ts.net')) return null
    return `https://${host}`
  } catch {
    return null
  }
}

export function verifiedRouteForInput(status: TailscaleSettingsStatus | null, raw: string): TailscaleSettingsRoute | null {
  const origin = normalizeTailscaleOriginInput(raw)
  if (!origin || !status) return null
  return status.routes.find((route) => route.origin === origin && route.classification === 'verified_swarm_desktop') ?? null
}
