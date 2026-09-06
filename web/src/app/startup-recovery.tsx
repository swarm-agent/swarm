import { Suspense, useEffect, type ComponentType, type ReactNode } from 'react'

declare global {
  interface Window {
    __swarmStartup?: { started(): void; ready(): void; fail(): void }
  }
}

// An effect proves the gate or route actually committed, not merely that React
// mounted its provider. A suspended route must keep the HTML watchdog alive.
function StartupReady() {
  useEffect(() => { window.__swarmStartup?.ready() }, [])
  return null
}

export function StartupScreen({ children }: { children: ReactNode }) {
  return <Suspense fallback={null}>{children}<StartupReady /></Suspense>
}

export function withStartupScreen(Component: ComponentType) {
  return function StartupRouteScreen() {
    return <StartupScreen><Component /></StartupScreen>
  }
}

export function StartupRouteError() {
  return (
    <StartupScreen>
      <main style={{ padding: 24, color: 'var(--app-text)', background: 'var(--app-bg)', minHeight: '100vh' }}>
        <h1>Unable to open this screen</h1>
        <p>The app encountered an error while opening this route. Reload to try again.</p>
        <button type="button" onClick={() => window.location.reload()}>Reload page</button>
        <p>Reloading does not clear your sessions or credentials.</p>
      </main>
    </StartupScreen>
  )
}
