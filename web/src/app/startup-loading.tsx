import { useLayoutEffect, type CSSProperties } from 'react'
import { SwarmMark } from '../components/ui/swarm-mark'

export function StartupLoading() {
  return <main className="swarm-startup-loading" role="status" aria-label="Loading Swarm" aria-live="polite">
    <SwarmMark className="swarm-startup-mark" />
    <span>Loading Swarm…</span>
  </main>
}

/** Cosmetic colors only: never a cache of project identity, authorization or data. */
export function useStartupPalette(style: CSSProperties, enabled: boolean) {
  useLayoutEffect(() => {
    if (!enabled) return
    const vars = style as Record<string, string>
    const palette = {
      background: vars['--swarm-background'],
      text: vars['--swarm-text'],
      accent: vars['--swarm-accent'],
    }
    if (!Object.values(palette).every(value => value && CSS.supports('color', value))) return
    for (const [key, value] of Object.entries(palette)) document.documentElement.style.setProperty(`--startup-${key}`, value)
    try {
      const route = window.location.pathname.match(/^\/projects\/[^/]+/)?.[0] || window.location.pathname
      sessionStorage.setItem('swarm:startup-palette', JSON.stringify({ route, ...palette }))
    } catch { /* Cosmetic cache is optional. */ }
  }, [style, enabled])
}
