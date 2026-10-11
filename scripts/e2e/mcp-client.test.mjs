// Unit tests for the MCP stdio client framing used by the ai-path e2e.
// A tiny in-test server stands in for `swarmctl mcp`; it answers out of
// order, as the real bridge may. Run: node --test scripts/e2e/*.test.mjs

import assert from 'node:assert/strict'
import test from 'node:test'

import { encodeMessage, LineDecoder, McpClient, MCP_MAX_MESSAGE_BYTES, toolPayload } from './lib/mcp-client.mjs'

test('encodeMessage: one JSON object per line', () => {
  const line = encodeMessage({ jsonrpc: '2.0', id: 1, method: 'x', params: { text: 'a\nb' } })
  assert.ok(line.endsWith('\n'))
  assert.equal(line.indexOf('\n'), line.length - 1)
  assert.deepEqual(JSON.parse(line), { jsonrpc: '2.0', id: 1, method: 'x', params: { text: 'a\nb' } })
  assert.throws(() => encodeMessage({ big: 'x'.repeat(MCP_MAX_MESSAGE_BYTES) }), /1 MiB/)
})

test('LineDecoder: split, partial and CRLF lines', () => {
  const decoder = new LineDecoder()
  assert.deepEqual(decoder.push('{"a":1}\n{"b"'), ['{"a":1}'])
  assert.deepEqual(decoder.push(':2}\r\n\n'), ['{"b":2}'])
  assert.deepEqual(decoder.push(Buffer.from('{"c":3}')), [])
  assert.deepEqual(decoder.push('\n'), ['{"c":3}'])
})

test('toolPayload: JSON text, tool errors and plain text', () => {
  assert.deepEqual(toolPayload({ content: [{ type: 'text', text: '{"ok":true}' }], isError: false }), { isError: false, text: '{"ok":true}', data: { ok: true } })
  assert.deepEqual(toolPayload({ content: [{ type: 'text', text: 'no such session' }], isError: true }), { isError: true, text: 'no such session', data: null })
  assert.deepEqual(toolPayload({ content: [{ type: 'text', text: 'plain' }] }), { isError: false, text: 'plain', data: null })
})

// The fake server: initialize, tools/list, tools/call (echo), notifications
// recorded, replies to ids 3 and 4 swapped, errors for unknown methods.
const FAKE_SERVER = `
import readline from 'node:readline'
const rl = readline.createInterface({ input: process.stdin })
const notes = []
let held = null
const send = (m) => process.stdout.write(JSON.stringify(m) + '\\n')
process.stderr.write('fake bridge: speaking MCP on stdio\\n')
rl.on('line', (line) => {
  const m = JSON.parse(line)
  if (m.id === undefined) { notes.push(m.method); return }
  let reply
  if (m.method === 'initialize') reply = { jsonrpc: '2.0', id: m.id, result: { protocolVersion: m.params.protocolVersion, serverInfo: { name: 'swarm-control', version: 'test' } } }
  else if (m.method === 'tools/list') reply = { jsonrpc: '2.0', id: m.id, result: { tools: [{ name: 'swarm_echo' }], notes } }
  else if (m.method === 'tools/call') reply = { jsonrpc: '2.0', id: m.id, result: { content: [{ type: 'text', text: JSON.stringify({ echo: m.params.arguments, notes }) }], isError: false } }
  else reply = { jsonrpc: '2.0', id: m.id, error: { code: -32601, message: 'method not found' } }
  if (m.id === 3) { held = reply; return }
  send(reply)
  if (m.id === 4 && held) { send(held); held = null }
})
`

function fakeClient() {
  return new McpClient({ command: process.execPath, args: ['--input-type=module', '-e', FAKE_SERVER], env: { PATH: process.env.PATH } })
}

test('McpClient: handshake, out-of-order replies, errors and close', async () => {
  const client = fakeClient()
  client.start()
  try {
    const init = await client.initialize()
    assert.equal(init.serverInfo.name, 'swarm-control')
    assert.equal(init.protocolVersion, '2025-11-25')
    const tools = await client.listTools()
    assert.deepEqual(tools.map((t) => t.name), ['swarm_echo'])
    // Reply 3 is held until 4 arrives: matching must be by id, not order.
    const [three, four] = await Promise.all([client.call('swarm_echo', { n: 3 }), client.call('swarm_echo', { n: 4 })])
    assert.deepEqual(three.data.echo, { n: 3 })
    assert.deepEqual(four.data.echo, { n: 4 })
    assert.deepEqual(three.data.notes, ['notifications/initialized'])
    await assert.rejects(client.request('nope/method'), /-32601/)
    assert.match(client.stderrTail, /speaking MCP/)
  } finally {
    await client.close()
  }
  assert.ok(client.exited)
  await assert.rejects(client.request('tools/list'), /not running/)
})

test('McpClient: a dying server fails pending requests', async () => {
  const client = new McpClient({ command: process.execPath, args: ['-e', 'process.stdin.once("data", () => process.exit(3))'], env: { PATH: process.env.PATH } })
  client.start()
  await assert.rejects(client.request('initialize', {}, 5000), /exited \(code 3/)
})

test('McpClient: request timeout', async () => {
  const client = new McpClient({ command: process.execPath, args: ['-e', 'process.stdin.resume()'], env: { PATH: process.env.PATH } })
  client.start()
  try {
    await assert.rejects(client.request('initialize', {}, 200), /timed out/)
  } finally {
    client.kill()
  }
})
