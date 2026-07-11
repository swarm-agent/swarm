const SWARM_PWA_CACHE = 'swarm-pwa-shell-v2'
const SHELL_ASSETS = [
  '/favicon.svg',
  '/apple-touch-icon.png',
  '/pwa-icon-192.png',
  '/pwa-icon-512.png',
]

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SWARM_PWA_CACHE).then((cache) => cache.addAll(SHELL_ASSETS)).catch(() => undefined),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(
      keys.filter((key) => key !== SWARM_PWA_CACHE).map((key) => caches.delete(key)),
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

  if (request.mode === 'navigate') {
    event.respondWith(networkFirst(request, { cacheResponse: false, fallbackToCache: false }))
    return
  }

  if (isStaticShellAsset(url.pathname) || url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(request))
  }
})

function isRuntimeEndpoint(pathname) {
  return pathname.startsWith('/v1/')
    || pathname.startsWith('/v2/')
    || pathname.startsWith('/v3/')
    || pathname.startsWith('/ws')
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

async function networkFirst(request, options = {}) {
  const cacheResponse = options.cacheResponse !== false
  const fallbackToCache = options.fallbackToCache !== false

  try {
    const response = await fetch(request)
    if (cacheResponse && response && response.ok) {
      const cache = await caches.open(SWARM_PWA_CACHE)
      await cache.put(request, response.clone())
    }
    return response
  } catch (error) {
    if (fallbackToCache) {
      const cached = await caches.match(request)
      if (cached) {
        return cached
      }
    }
    throw error
  }
}

async function cacheFirst(request) {
  const cached = await caches.match(request)
  if (cached) {
    return cached
  }
  const response = await fetch(request)
  if (response && response.ok) {
    const cache = await caches.open(SWARM_PWA_CACHE)
    await cache.put(request, response.clone())
  }
  return response
}
