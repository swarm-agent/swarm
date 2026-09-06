function unregisterDevelopmentServiceWorkers(): void {
  window.addEventListener('load', () => {
    navigator.serviceWorker.getRegistrations()
      .then((registrations) => Promise.all(registrations.map((registration) => registration.unregister())))
      .catch(() => undefined)
  })
}

function registerProductionServiceWorker(): void {
  // The worker owns icons and push only, not app bundles. A controller update
  // needs no page reload and must not interrupt a session or loop on startup.
  window.addEventListener('load', () => {
    let updateInFlight: Promise<void> | undefined
    let registration: ServiceWorkerRegistration | undefined

    const checkForUpdate = (): void => {
      if (!registration || updateInFlight) return
      updateInFlight = registration.update()
        .then(() => undefined)
        .catch(() => undefined)
        .finally(() => {
          updateInFlight = undefined
        })
    }

    navigator.serviceWorker.register('/sw.js', {
      scope: '/',
      updateViaCache: 'none',
    }).then((registered) => {
      registration = registered
      checkForUpdate()
    }).catch(() => undefined)

    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') checkForUpdate()
    })
    window.addEventListener('focus', checkForUpdate)
    window.addEventListener('online', checkForUpdate)
  })
}

export function setupServiceWorker(): void {
  if (!('serviceWorker' in navigator)) return

  if (import.meta.env.PROD && window.isSecureContext) {
    registerProductionServiceWorker()
  } else if (import.meta.env.DEV) {
    unregisterDevelopmentServiceWorkers()
  }
}
