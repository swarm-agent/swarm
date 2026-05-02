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
    || pathname.startsWith('/ws')
    || pathname === '/healthz'
    || pathname === '/readyz'
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
