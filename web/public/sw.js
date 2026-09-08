const SWARM_PWA_CACHE_PREFIX = 'swarm-pwa-shell-'
const SWARM_PWA_CACHE = `${SWARM_PWA_CACHE_PREFIX}__SWARM_PWA_BUILD_ID__`
const SHELL_ASSETS = [
  '/favicon.svg',
  '/apple-touch-icon.png',
  '/pwa-icon-192.png',
  '/pwa-icon-512.png',
]

self.addEventListener('install', (event) => {
  event.waitUntil((async () => {
    const cache = await caches.open(SWARM_PWA_CACHE)
    await cache.addAll(SHELL_ASSETS.map((asset) => new Request(asset, { cache: 'reload' })))
    await self.skipWaiting()
  })())
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(
      keys
        .filter((key) => key.startsWith(SWARM_PWA_CACHE_PREFIX) && key !== SWARM_PWA_CACHE)
        .map((key) => caches.delete(key)),
    )).then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const request = event.request
  if (request.method !== 'GET') {
    return
  }

  const url = new URL(request.url)
  if (url.origin !== self.location.origin || isRuntimeEndpoint(url.pathname)) {
    return
  }

  // HTML and app chunks use normal browser/server cache semantics. Intercept
  // only the explicit icon allowlist, never navigation (even to an icon URL).
  if (request.mode === 'navigate') return

  if (isStaticShellAsset(url.pathname)) {
    event.respondWith(cacheFirst(request))
  }
})

function isRuntimeEndpoint(pathname) {
  return pathname === '/v1' || pathname.startsWith('/v1/')
    || pathname === '/v2' || pathname.startsWith('/v2/')
    || pathname === '/v3' || pathname.startsWith('/v3/')
    || pathname === '/desktop' || pathname.startsWith('/desktop/')
    || pathname === '/ws' || pathname.startsWith('/ws/')
    || pathname === '/healthz'
    || pathname === '/readyz'
}

self.addEventListener('push', (event) => {
  event.waitUntil((async () => {
    let payload = {}
    try {
      payload = event.data ? event.data.json() : {}
    } catch {
      payload = { body: event.data ? event.data.text() : '' }
    }
    const title = typeof payload.title === 'string' && payload.title.trim() ? payload.title.trim() : 'Swarm'
    const body = typeof payload.body === 'string' ? payload.body : ''
    const tag = typeof payload.tag === 'string' ? payload.tag : undefined
    const url = safeSwarmRoute(payload.url)
    await self.registration.showNotification(title, {
      body,
      tag,
      icon: '/pwa-icon-192.png',
      badge: '/pwa-icon-192.png',
      data: { url },
    })
  })())
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const route = safeSwarmRoute(event.notification.data && event.notification.data.url)
  event.waitUntil((async () => {
    const targetURL = new URL(route, self.location.origin).href
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const client of windows) {
      if (new URL(client.url).origin !== self.location.origin) continue
      if ('navigate' in client) await client.navigate(targetURL)
      return client.focus()
    }
    return self.clients.openWindow(targetURL)
  })())
})

function safeSwarmRoute(value) {
  try {
    const url = new URL(typeof value === 'string' && value.trim() ? value : '/', self.location.origin)
    if (url.origin !== self.location.origin || !url.pathname.startsWith('/')) return '/'
    if (url.protocol !== 'https:' && url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') return '/'
    return `${url.pathname}${url.search}${url.hash}`
  } catch {
    return '/'
  }
}

function isStaticShellAsset(pathname) {
  return SHELL_ASSETS.includes(pathname)
}

async function cacheFirst(request) {
  const cache = await caches.open(SWARM_PWA_CACHE)
  const cached = await cache.match(request)
  if (cached) {
    return cached
  }
  const response = await fetch(request)
  if (response && response.ok) {
    await cache.put(request, response.clone())
  }
  return response
}
