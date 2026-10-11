// Minimal MCP stdio client: newline-delimited JSON-RPC 2.0, the framing
// `swarmctl mcp` speaks (swarmd/cmd/swarmctl/mcp_bridge.go: one message per
// line, at most 1 MiB, replies may arrive in any order and are matched by id).
// Flow: initialize -> notifications/initialized -> tools/list -> tools/call.

import { spawn } from 'node:child_process'

export const MCP_PROTOCOL_VERSION = '2025-11-25'
export const MCP_MAX_MESSAGE_BYTES = 1 << 20

export function encodeMessage(message) {
  const line = JSON.stringify(message)
  if (line.includes('\n')) throw new Error('encoded MCP message contains a newline')
  if (Buffer.byteLength(line) > MCP_MAX_MESSAGE_BYTES) throw new Error('MCP message exceeds 1 MiB')
  return `${line}\n`
}

// LineDecoder splits a byte stream into complete lines (without the newline,
// CR stripped). Partial lines are kept until their newline arrives.
export class LineDecoder {
  constructor() {
    this.buffer = ''
  }

  push(chunk) {
    this.buffer += typeof chunk === 'string' ? chunk : chunk.toString('utf8')
    const lines = []
    let index
    while ((index = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, index).replace(/\r$/, '')
      this.buffer = this.buffer.slice(index + 1)
      if (line.trim() !== '') lines.push(line)
    }
    return lines
  }
}

export class McpError extends Error {
  constructor(message, { code, kind } = {}) {
    super(message)
    this.code = code
    this.kind = kind
  }
}

// toolPayload decodes a tools/call result: the JSON text block Swarm Control
// returns, or the error text when isError is set.
export function toolPayload(result) {
  const text = (result?.content ?? []).filter((c) => c?.type === 'text').map((c) => c.text).join('')
  if (result?.isError) return { isError: true, text, data: null }
  try {
    return { isError: false, text, data: JSON.parse(text) }
  } catch {
    return { isError: false, text, data: null }
  }
}

// McpClient talks to any process speaking MCP over stdio. Its stderr is kept
// in a bounded tail for diagnostics (the bridge prints only status there).
export class McpClient {
  constructor({ command, args = [], env, cwd, log = () => {} }) {
    this.command = command
    this.args = args
    this.env = env
    this.cwd = cwd
    this.log = log
    this.nextId = 1
    this.pending = new Map()
    this.decoder = new LineDecoder()
    this.stderrTail = ''
    this.exited = null
    this.serverInfo = null
    this.tools = []
  }

  start() {
    this.child = spawn(this.command, this.args, { env: this.env, cwd: this.cwd, stdio: ['pipe', 'pipe', 'pipe'], detached: true })
    this.exitPromise = new Promise((resolve) => {
      this.child.once('exit', resolve)
      this.child.once('error', resolve)
    })
    this.child.stdout.on('data', (chunk) => {
      for (const line of this.decoder.push(chunk)) this.onLine(line)
    })
    this.child.stderr.on('data', (chunk) => {
      this.stderrTail = (this.stderrTail + chunk.toString('utf8')).slice(-2000)
    })
    this.child.stdin.on('error', () => {})
    this.child.on('error', (err) => this.fail(new McpError(`MCP process failed to start: ${err.message}`, { kind: 'spawn' })))
    this.child.on('exit', (code, signal) => {
      this.exited = { code, signal }
      this.fail(new McpError(`MCP process exited (code ${code}, signal ${signal}): ${this.stderrTail.trim().slice(-300)}`, { kind: 'exit' }))
    })
  }

  onLine(line) {
    let message
    try {
      message = JSON.parse(line)
    } catch {
      this.log(`mcp: ignoring non-JSON stdout line (${line.length} bytes)`)
      return
    }
    if (message && Object.hasOwn(message, 'id') && this.pending.has(message.id)) {
      const waiter = this.pending.get(message.id)
      this.pending.delete(message.id)
      clearTimeout(waiter.timer)
      if (message.error) waiter.reject(new McpError(`JSON-RPC error ${message.error.code}: ${message.error.message}`, { code: message.error.code, kind: 'rpc' }))
      else waiter.resolve(message.result)
    }
  }

  fail(err) {
    for (const [id, waiter] of this.pending) {
      clearTimeout(waiter.timer)
      waiter.reject(err)
      this.pending.delete(id)
    }
  }

  send(message) {
    if (!this.child || this.exited) throw new McpError('MCP process is not running', { kind: 'exit' })
    this.child.stdin.write(encodeMessage(message))
  }

  request(method, params = {}, timeoutMs = 60000) {
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new McpError(`MCP ${method} timed out after ${timeoutMs} ms`, { kind: 'timeout' }))
      }, timeoutMs)
      this.pending.set(id, { resolve, reject, timer })
      try {
        this.send({ jsonrpc: '2.0', id, method, params })
      } catch (err) {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(err)
      }
    })
  }

  notify(method, params) {
    const message = { jsonrpc: '2.0', method }
    if (params !== undefined) message.params = params
    this.send(message)
  }

  async initialize(clientName = 'swarm-ai-path-e2e') {
    const result = await this.request('initialize', {
      protocolVersion: MCP_PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: clientName, version: '1' },
    }, 30000)
    this.serverInfo = result?.serverInfo ?? null
    this.notify('notifications/initialized')
    return result
  }

  async listTools() {
    const result = await this.request('tools/list', {}, 30000)
    this.tools = Array.isArray(result?.tools) ? result.tools : []
    return this.tools
  }

  // call runs one tool. Waits inside the tool (wait_seconds) are added to the
  // client timeout, so a long wait never looks like a hung call.
  async call(name, args = {}, timeoutMs) {
    const wait = Number(args.wait_seconds) > 0 ? Number(args.wait_seconds) * 1000 : 0
    const result = await this.request('tools/call', { name, arguments: args }, timeoutMs ?? wait + 60000)
    return toolPayload(result)
  }

  async close() {
    if (!this.child || this.exited) return
    const exited = this.exitPromise
    try {
      this.child.stdin.end()
    } catch {}
    const timer = setTimeout(() => {
      try {
        process.kill(-this.child.pid, 'SIGTERM')
      } catch {}
    }, 3000)
    const hard = setTimeout(() => {
      try {
        process.kill(-this.child.pid, 'SIGKILL')
      } catch {}
    }, 8000)
    await exited
    clearTimeout(timer)
    clearTimeout(hard)
  }

  kill() {
    if (this.child && !this.exited) {
      try {
        process.kill(-this.child.pid, 'SIGKILL')
      } catch {}
    }
  }
}
