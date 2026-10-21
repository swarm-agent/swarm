// The forwarding deadline is a total one: a machine that keeps sending bytes but
// never finishes its answer must not hold a call open (it once held one 15 min).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { post, callTimeoutMs } from '../server.mjs';

test('a machine that trickles bytes forever is cut off at the deadline', async () => {
  const server = createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    const drip = setInterval(() => res.write(' '), 50);
    req.on('close', () => clearInterval(drip));
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const url = new URL(`http://127.0.0.1:${server.address().port}/mcp`);
  const started = Date.now();
  await assert.rejects(post({ url, token: '' }, { jsonrpc: '2.0', id: 1, method: 'tools/call' }, { timeoutMs: 400 }), /did not answer in time/);
  assert.ok(Date.now() - started < 1500, `took ${Date.now() - started} ms`);
  server.closeAllConnections(); server.close();
});

test('a call that asks the machine to wait gets that wait plus 30 s', () => {
  assert.equal(callTimeoutMs({ wait_seconds: 110 }), 140_000);
  assert.equal(callTimeoutMs({ wait_seconds: 0 }), 330_000);
  assert.equal(callTimeoutMs({}), 330_000);
  assert.equal(callTimeoutMs({ wait_seconds: 9999 }), 330_000);
});
