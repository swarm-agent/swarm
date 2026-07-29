export type CodexSetupRecommendation = 'browser' | 'device'

export function codexSetupRecommendation(hostname?: string): CodexSetupRecommendation {
  const host = normalizeHostname(hostname ?? currentBrowserHostname())
  return isLoopbackHostname(host) ? 'browser' : 'device'
}

function currentBrowserHostname(): string {
  return typeof window === 'undefined' ? 'localhost' : window.location.hostname
}

function normalizeHostname(hostname: string): string {
  return String(hostname ?? '').trim().toLowerCase().replace(/^\[/, '').replace(/\]$/, '')
}

function isLoopbackHostname(hostname: string): boolean {
  return hostname === 'localhost'
    || hostname.endsWith('.localhost')
    || hostname === '::1'
    || hostname.startsWith('127.')
}
