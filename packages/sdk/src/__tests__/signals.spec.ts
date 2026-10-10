import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';

// Purpose: monitors and reporters use client.signals to follow a machine's
// feed with a cursor and to report outside events. The SDK must send the
// cursor, kind prefixes and severity exactly as the daemon's /v3/signals
// expects, surface the gap flag (signals dropped after the cursor) instead of
// hiding it, and post reports as JSON. Layer: the SDK against a local HTTP
// stand-in, the narrowest layer that shows the wire format.
test('SwarmSignalsNamespace: list with cursor and filters, report', async () => {
  let listQuery: URLSearchParams | null = null;
  let reported: any = null;
  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '/', 'http://127.0.0.1');
    res.setHeader('Content-Type', 'application/json');
    if (req.method === 'GET' && url.pathname === '/v3/signals') {
      listQuery = url.searchParams;
      res.end(
        JSON.stringify({
          ok: true,
          signals: [{ seq: 8, id: 'sig_1', at: 1, kind: 'agent.blocked', severity: 'warning', source: 'swarmd', summary: 'waiting' }],
          next_after: 9,
          oldest_seq: 5,
          latest_seq: 9,
          gap: true,
        }),
      );
      return;
    }
    if (req.method === 'POST' && url.pathname === '/v3/signals') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        reported = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.end(JSON.stringify({ ok: true, signal: { seq: 10, id: 'sig_2', at: 2, kind: reported.kind, severity: 'critical', source: 'external:falco', summary: reported.summary } }));
      });
      return;
    }
    res.statusCode = 404;
    res.end('{}');
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const { port } = server.address() as { port: number };
  try {
    const client = new SwarmClient({ baseUrl: `http://127.0.0.1:${port}`, token: 'test_tok' });
    const page = await client.signals.list({ after: 3, limit: 50, kinds: ['agent', 'run.failed'], minSeverity: 'warning' });
    assert.equal(listQuery!.get('after'), '3');
    assert.equal(listQuery!.get('limit'), '50');
    assert.equal(listQuery!.get('kind'), 'agent,run.failed');
    assert.equal(listQuery!.get('min_severity'), 'warning');
    assert.equal(page.gap, true);
    assert.equal(page.next_after, 9);
    assert.equal(page.signals[0].kind, 'agent.blocked');

    const sig = await client.signals.report({ kind: 'external.falco', severity: 'critical', source: 'falco', summary: 'Terminal shell in container' });
    assert.deepEqual(reported, { kind: 'external.falco', severity: 'critical', source: 'falco', summary: 'Terminal shell in container' });
    assert.equal(sig.source, 'external:falco');
  } finally {
    server.close();
  }
});
