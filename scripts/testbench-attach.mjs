#!/usr/bin/env node
// Shared attach-only client. Bootstrap and all requests stay on the supplied
// Desktop origin; never infer daemon ports, follow redirects, or load .env.
import { createHash } from 'node:crypto'
import { pathToFileURL } from 'node:url'

export function desktopOrigin(value) {
  const url = new URL(value)
  if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || !url.port || url.username || url.password || url.search || url.hash || url.pathname !== '/') {
    throw new Error('attach requires an explicit http://127.0.0.1:port/ Desktop root')
  }
  return url.origin
}

export class AttachClient {
  constructor(url, { requestMs = 10000, stageMs = 600000, maxBytes = 1048576, signal } = {}) {
    this.origin = desktopOrigin(url)
    if (!Number.isInteger(requestMs) || requestMs < 1 || requestMs > 15000 ||
        !Number.isInteger(stageMs) || stageMs < 1 || stageMs > 600000 ||
        !Number.isInteger(maxBytes) || maxBytes < 1 || maxBytes > 1048576) throw new Error('invalid attach bounds')
    this.requestMs = requestMs
    this.deadline = performance.now() + stageMs
    this.maxBytes = maxBytes
    this.token = ''
    this.signal = signal
  }

  async get(route) {
    if (!['/v1/auth/desktop/session', '/v1/swarm/topology', '/v1/agent-model-settings'].includes(route)) throw new Error('unreviewed attach read')
    return this.request('GET', route)
  }

  // Subclasses must authorize their own exact owned-session routes before calling.
  async request(method, route, body) {
    if (!route.startsWith('/') || route.startsWith('//') || route.includes('..') || route.includes('\\') || /[\r\n#]/.test(route)) throw new Error('invalid attach route')
    const readRoutes = ['/v1/auth/desktop/session', '/v1/swarm/topology', '/v1/agent-model-settings', '/v1/workspace/current']
    const mutationRoutes = ['/v1/workspace/folders/create', '/v1/workspace/repository/setup', '/v1/workspace/add', '/v3/sessions', '/v3/sync/hydrate']
    const ownedRoute = /^\/v3\/sessions\/[a-zA-Z0-9_-]+\/(repositories\?limit=20(?:&cursor=[a-zA-Z0-9_%.-]+)?|permissions\?status=pending&limit=20|messages|run\/stop)$/.test(route)
    const permissionResolve = /^\/v3\/sessions\/[a-zA-Z0-9_-]+\/permissions\/[a-zA-Z0-9_-]+\/resolve$/.test(route)
    const memoryRoute = route === '/v1/memory' || method === 'GET' && /^\/v1\/memory\?session_id=[a-zA-Z0-9_-]*$/.test(route)
    const memoryOperation = this.memoryTrial === true && memoryRoute && (method === 'GET' || method === 'POST' && ['remember', 'forget', 'settings', 'restore', 'run_now', 'cancel', 'approve'].includes(body?.action))
    const statusRoute = route.startsWith('/v1/workspace/git/status?session_id=')
    if (!memoryOperation && !(method === 'GET' && (readRoutes.includes(route) || statusRoute || ownedRoute && !route.endsWith('/messages') && !route.endsWith('/run/stop'))) &&
        !(method === 'POST' && (permissionResolve || mutationRoutes.includes(route) || ownedRoute && (route.endsWith('/messages') || route.endsWith('/run/stop'))))) throw new Error('unreviewed attach operation')
    if (permissionResolve && (body?.action !== 'allow_once' || Object.keys(body).some(key => !['action', 'reason'].includes(key)))) throw new Error('only exact allow-once permission resolution is permitted')
    if (route === '/v1/workspace/add' && body?.make_current !== false) throw new Error('attach cannot change shared selection')
    const remaining = Math.floor(this.deadline - performance.now())
    if (remaining <= 0) throw new Error('attach stage deadline exceeded')
    const headers = { Accept: 'application/json', Origin: this.origin, Referer: `${this.origin}/app`, 'Sec-Fetch-Site': 'same-origin' }
    if (this.token) {
      headers['X-Swarm-Token'] = this.token
      headers.Cookie = `swarm_desktop_session=${this.token}`
    }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const response = await fetch(`${this.origin}${route}`, {
      method, headers, body: body === undefined ? undefined : JSON.stringify(body),
      redirect: 'error', signal: AbortSignal.any([AbortSignal.timeout(Math.min(memoryOperation && method === 'POST' && body?.action === 'run_now' ? 150000 : this.requestMs, remaining)), ...(this.signal ? [this.signal] : [])]),
    })
    if (!response.ok) {
      // Only fixed memory diagnostics are safe to expose; never print provider bodies.
      if (memoryOperation) {
        const reader = response.body?.getReader()
        const chunk = await reader?.read()
        await reader?.cancel()
        const text = chunk?.value?.length <= 2048 ? new TextDecoder().decode(chunk.value).trim() : ''
        const safe = ['memory provider unavailable', 'memory budget exceeded', 'memory provider output rejected', 'memory provider returned invalid JSON', 'memory provider returned trailing output']
        if (safe.includes(text)) throw new Error(text)
      } else await response.body?.cancel()
      throw new Error(`attach read returned HTTP ${response.status}`)
    }
    const chunks = []
    let length = 0
    for await (const chunk of response.body) {
      length += chunk.length
      if (length > this.maxBytes) throw new Error('attach response limit exceeded')
      chunks.push(chunk)
    }
    // Do not include response bodies in errors: even bootstrap failures may carry secrets.
    try { return JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch { throw new Error('attach read returned invalid JSON') }
  }

  async inspect(expectedRuntime = '') {
    const auth = await this.get('/v1/auth/desktop/session')
    if (typeof auth.token !== 'string' || !auth.token || /[\r\n;]/.test(auth.token)) throw new Error('desktop bootstrap returned no usable credential')
    this.token = auth.token
    const topology = await this.get('/v1/swarm/topology')
    const self = (topology.runtimes || []).filter(item => item.relationship === 'self')
    if (self.length !== 1 || !self[0].swarm_id || self[0].status !== 'online') throw new Error('exact online self runtime is unavailable')
    if (expectedRuntime && self[0].swarm_id !== expectedRuntime) throw new Error('attach runtime identity mismatch')
    const settings = (await this.get('/v1/agent-model-settings')).agent_model_settings
    if (!settings?.swarm?.action?.model || !settings?.swarm?.plan?.model) throw new Error('canonical model settings missing')
    const digest = value => createHash('sha256').update(JSON.stringify(value)).digest('hex')
    const after = (await this.get('/v1/agent-model-settings')).agent_model_settings
    if (digest(after) !== digest(settings)) throw new Error('shared model settings changed during inspection')
    return { authenticated: true, runtime_id: self[0].swarm_id, binding_count: topology.workspace_bindings?.length || 0,
      settings_sha256: digest(settings), settings_unchanged: true,
      // Build/lane provenance is a separate broker read, not inferred from HTTP 200.
      candidate_build: 'requires independent broker evidence' }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const client = new AttachClient(process.argv[2])
    const evidence = await client.inspect(process.env.SWARM_ATTACH_EXPECTED_RUNTIME || '')
    if (process.env.SWARM_ATTACH_EXPECTED_SETTINGS && evidence.settings_sha256 !== process.env.SWARM_ATTACH_EXPECTED_SETTINGS) throw new Error('shared model settings differ from connection preflight')
    console.log(JSON.stringify(evidence))
  } catch (error) {
    console.error(`attach-only: ${error.message}`)
    process.exitCode = 1
  }
}
