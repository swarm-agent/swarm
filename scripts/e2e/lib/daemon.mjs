// One private swarmd per run: its own run directory, state, ports and owner
// socket. Nothing here reads or writes the host's real Swarm (no fixed ports,
// no /var/lib/swarmd, no inherited SWARM_* environment).

import { spawn } from 'node:child_process'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import path from 'node:path'

// Environment passed through from the caller; everything else is dropped so
// no provider key, Swarm socket or token in the caller's environment reaches
// the test daemon or its clients.
const PASS_ENV = ['PATH', 'LANG', 'LC_ALL', 'TZ', 'TERM']

export function baseEnv(runDir, binDir) {
  const env = {}
  for (const name of PASS_ENV) if (process.env[name]) env[name] = process.env[name]
  const home = path.join(runDir, 'home')
  Object.assign(env, {
    HOME: home,
    USER: process.env.USER || 'e2e',
    LD_LIBRARY_PATH: binDir,
    SWARM_DISABLE_MINT_REPORT: '1',
    XDG_CONFIG_HOME: path.join(home, '.config'),
    XDG_DATA_HOME: path.join(home, '.local', 'share'),
    XDG_STATE_HOME: path.join(home, '.local', 'state'),
    XDG_CACHE_HOME: path.join(home, '.cache'),
  })
  return env
}

export function daemonEnv(runDir, binDir) {
  return {
    ...baseEnv(runDir, binDir),
    STATE_DIRECTORY: path.join(runDir, 'data'),
    CACHE_DIRECTORY: path.join(runDir, 'cache'),
    RUNTIME_DIRECTORY: path.join(runDir, 'run'),
    CONFIGURATION_DIRECTORY: path.join(runDir, 'cfg'),
    LOGS_DIRECTORY: path.join(runDir, 'log'),
  }
}

export function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer()
    server.unref()
    server.on('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      server.close(() => resolve(port))
    })
  })
}

const SCENARIO_ABBR = { onboard: 'onb', chat: 'cht', tasks: 'tsk', sdk: 'sdk', restart: 'rst' }

// createRunDir makes <workDir>/sw-<abbr>-XXXXXX (0700). The short name keeps
// the owner socket path inside the local key loader's accepted pattern when
// workDir is /tmp.
export function createRunDir(workDir, scenario) {
  const parent = fs.realpathSync(workDir)
  const runDir = fs.mkdtempSync(path.join(parent, `sw-${SCENARIO_ABBR[scenario] || 'run'}-`))
  fs.chmodSync(runDir, 0o700)
  for (const dir of ['cfg', 'data', 'cache', 'run', 'log', 'home']) fs.mkdirSync(path.join(runDir, dir), { mode: 0o700 })
  return runDir
}

// removeRunDir deletes a run directory this process created, and only that:
// the path must be a direct sw-* child of the work dir.
export function removeRunDir(runDir, workDir) {
  const parent = fs.realpathSync(workDir)
  if (typeof runDir !== 'string' || runDir === '' || path.dirname(runDir) !== parent || !/^sw-[a-z]{3}-[A-Za-z0-9]{6}$/.test(path.basename(runDir))) {
    throw new Error('refusing to remove a path that is not this run directory')
  }
  fs.rmSync(runDir, { recursive: true, force: true, maxRetries: 3 })
}

export function writeStartupConfig(runDir, port, peerPort) {
  const text = [
    `port = ${port}`,
    `peer_transport_port = ${peerPort}`,
    'desktop_port = 0',
    'desktop_onboarding_complete = false',
    '',
  ].join('\n')
  fs.writeFileSync(path.join(runDir, 'cfg', 'swarm.conf'), text, { mode: 0o600 })
}

// ownerRequest sends one JSON request over the owner socket. It returns
// {status, body} and never throws on HTTP status.
export function ownerRequest(socketPath, method, route, body, timeoutMs = 30000) {
  return new Promise((resolve, reject) => {
    const data = body === undefined ? null : Buffer.from(JSON.stringify(body))
    const req = http.request({
      socketPath,
      method,
      path: route,
      headers: { 'Content-Type': 'application/json', ...(data ? { 'Content-Length': data.length } : {}) },
      timeout: timeoutMs,
    }, (res) => {
      const chunks = []
      res.on('data', (c) => chunks.push(c))
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8')
        let parsed = null
        try {
          parsed = text ? JSON.parse(text) : null
        } catch {}
        resolve({ status: res.statusCode, body: parsed, text })
      })
    })
    req.on('timeout', () => req.destroy(new Error(`owner socket ${method} ${route} timed out`)))
    req.on('error', reject)
    if (data) req.write(data)
    req.end()
  })
}

function httpGetStatus(url, timeoutMs = 2000) {
  return new Promise((resolve) => {
    const req = http.get(url, { timeout: timeoutMs }, (res) => {
      res.resume()
      resolve(res.statusCode)
    })
    req.on('timeout', () => req.destroy())
    req.on('error', () => resolve(0))
  })
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export class Daemon {
  constructor({ binDir, runDir, log = () => {}, sandbox = 'off' }) {
    this.sandbox = sandbox
    this.binDir = binDir
    this.runDir = runDir
    this.log = log
    this.socket = path.join(runDir, 'data', 'local-transport', 'api.sock')
    this.child = null
    this.exit = null
    this.starts = 0
  }

  get apiUrl() {
    return `http://127.0.0.1:${this.port}`
  }

  async start({ readyTimeoutMs = 90000, reusePorts = false } = {}) {
    if (!reusePorts || !this.port) {
      this.port = await freePort()
      this.peerPort = await freePort()
      while (this.peerPort === this.port) this.peerPort = await freePort()
    }
    // A restart keeps the config the daemon itself updated (owner and Swarm
    // name, onboarding state); only a first start writes it.
    if (!reusePorts || this.starts === 0) writeStartupConfig(this.runDir, this.port, this.peerPort)
    this.starts += 1
    const logPath = path.join(this.runDir, 'log', `swarmd-${this.starts}.out`)
    const out = fs.openSync(logPath, 'a', 0o600)
    const r = this.runDir
    const args = [
      '--listen', `127.0.0.1:${this.port}`,
      '--desktop-port', '0',
      '--data-dir', path.join(r, 'data'),
      '--db-path', path.join(r, 'data', 'swarmd.pebble'),
      '--lock-path', path.join(r, 'run', 'swarmd.lock'),
      '--cwd', path.join(r, 'home'),
      // Agent commands run on this host for the test: no shared Docker
      // sandbox network or image with any other Swarm on the machine.
      '--sandbox', this.sandbox,
    ]
    this.exit = null
    this.child = spawn(path.join(this.binDir, 'swarmd'), args, {
      env: daemonEnv(r, this.binDir),
      cwd: path.join(r, 'home'),
      stdio: ['ignore', out, out],
      detached: true,
    })
    fs.closeSync(out)
    this.logPath = logPath
    this.exited = new Promise((resolve) => {
      this.child.once('exit', (code, signal) => {
        this.exit = { code, signal }
        resolve(this.exit)
      })
      this.child.once('error', (err) => {
        this.exit = { code: null, signal: null, error: err.message }
        resolve(this.exit)
      })
    })
    const deadline = Date.now() + readyTimeoutMs
    while (Date.now() < deadline) {
      if (this.exit) throw new Error(`swarmd exited before ready (${JSON.stringify(this.exit)}): ${this.logTail()}`)
      if ((await httpGetStatus(`${this.apiUrl}/readyz`)) === 200 && fs.existsSync(this.socket)) return
      await sleep(200)
    }
    throw new Error(`swarmd not ready within ${readyTimeoutMs} ms: ${this.logTail()}`)
  }

  logTail(bytes = 400) {
    try {
      const text = fs.readFileSync(this.logPath, 'utf8')
      return text.slice(-bytes).replace(/\s+/g, ' ')
    } catch {
      return ''
    }
  }

  // stop sends SIGTERM to the daemon's process group (the daemon and every
  // child it started), waits, then SIGKILLs what is left.
  async stop({ graceMs = 20000 } = {}) {
    if (!this.child || this.exit) return this.exit
    const pid = this.child.pid
    try {
      process.kill(-pid, 'SIGTERM')
    } catch {}
    const timer = new Promise((resolve) => setTimeout(() => resolve(null), graceMs))
    const result = await Promise.race([this.exited, timer])
    if (!result) {
      try {
        process.kill(-pid, 'SIGKILL')
      } catch {}
      await this.exited
      this.exit.killed = true
    }
    // Children that outlived the daemon (same group) go too.
    try {
      process.kill(-pid, 'SIGKILL')
    } catch {}
    return this.exit
  }

  killNow() {
    if (this.child && !this.exit) {
      try {
        process.kill(-this.child.pid, 'SIGKILL')
      } catch {}
    }
  }

  request(method, route, body, timeoutMs) {
    return ownerRequest(this.socket, method, route, body, timeoutMs)
  }
}
