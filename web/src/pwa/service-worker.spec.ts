// Purpose: icon-only service-worker fetch ownership must not intercept navigation,
// runtime/auth or app chunks; update activation must never auto-reload a session.
// Authority: public/sw.js + service-worker-registration.ts. Execute the worker
// and registration in isolated VM contexts, asserting calls and negative effects.
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'
import { transform } from 'esbuild'

const worker = await readFile(new URL('../../public/sw.js', import.meta.url), 'utf8')

test('worker only intercepts icons and preserves push and safe notification routing', async () => {
  const handlers: Record<string, (event: any) => void> = {}
  const notices: any[] = []
  const opened: string[] = []
  let cacheReads = 0
  const cached = { icon: true }
  runInNewContext(worker, {
    URL, Request,
    self: {
      location: { origin: 'https://desktop.invalid' },
      addEventListener: (name: string, callback: any) => { handlers[name] = callback },
      registration: { showNotification: (...args: any[]) => { notices.push(args) } },
      clients: { matchAll: async () => [], openWindow: (url: string) => { opened.push(url) } },
    },
    caches: { open: async () => { cacheReads++; return { match: async () => cached } } },
    fetch: () => { throw new Error('unexpected network request') },
  })
  for (const [path, mode, method] of [
    ['/', 'navigate', 'GET'], ['/workspace/session', 'navigate', 'GET'], ['/favicon.svg', 'navigate', 'GET'],
    ['/v3/sync/bootstrap', 'cors', 'GET'], ['/v1/auth/session', 'cors', 'POST'],
    ['/assets/app.js', 'cors', 'GET'], ['/assets/app.css', 'cors', 'GET'],
    ['https://external.invalid/favicon.svg', 'cors', 'GET'],
  ]) {
    handlers.fetch({ request: { url: new URL(path, 'https://desktop.invalid').href, mode, method }, respondWith: () => assert.fail(`intercepted ${path}`) })
  }
  assert.equal(cacheReads, 0)
  let response: any
  handlers.fetch({ request: { url: 'https://desktop.invalid/favicon.svg', mode: 'cors', method: 'GET' }, respondWith: (value: any) => { response = value } })
  assert.equal(await response, cached)
  assert.equal(cacheReads, 1)
  let pending: Promise<void> | undefined
  handlers.push({ data: { json: () => ({ title: 'Ready', body: 'Done', url: 'https://external.invalid/' }) }, waitUntil: (value: any) => { pending = value } })
  await pending
  assert.equal(notices[0][0], 'Ready')
  assert.equal(notices[0][1].data.url, '/')
  assert.equal(notices[0][1].icon, '/pwa-icon-192.png')
  let closed = false
  handlers.notificationclick({ notification: { close: () => { closed = true }, data: { url: '/workspace/session' } }, waitUntil: (value: any) => { pending = value } })
  await pending
  assert.equal(closed, true)
  assert.deepEqual(opened, ['https://desktop.invalid/workspace/session'])
})

test('production worker updates never reload on controller changes', async () => {
  const source = await readFile(new URL('./service-worker-registration.ts', import.meta.url), 'utf8')
  const { code } = await transform(source, { loader: 'ts', format: 'cjs', define: { 'import.meta.env.PROD': 'true', 'import.meta.env.DEV': 'false' } })
  const windowHandlers: Record<string, () => void> = {}
  const workerHandlers: Record<string, () => void> = {}
  const exports: any = {}
  let registrations = 0
  let updates = 0
  runInNewContext(code, {
    module: exports,
    window: { isSecureContext: true, location: { reload: () => assert.fail('automatic reload') }, addEventListener: (name: string, callback: any) => { windowHandlers[name] = callback } },
    document: { addEventListener() {}, visibilityState: 'visible' },
    navigator: { serviceWorker: {
      controller: {},
      addEventListener: (name: string, callback: any) => { workerHandlers[name] = callback },
      register: async (url: string, options: any) => {
        assert.equal(url, '/sw.js'); assert.equal(options.updateViaCache, 'none'); registrations++
        return { update: async () => { updates++ } }
      },
    } },
  })
  exports.exports.setupServiceWorker()
  windowHandlers.load()
  await new Promise(resolve => setImmediate(resolve))
  workerHandlers.controllerchange?.()
  workerHandlers.controllerchange?.()
  assert.equal(registrations, 1)
  assert.equal(updates, 1)
})
