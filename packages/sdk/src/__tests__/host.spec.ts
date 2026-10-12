import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';
import { SwarmConflictError, SwarmForbiddenError } from '../errors.js';
import { SwarmHostNamespace } from '../host.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: client.host must send host change requests, listings and the
// owner's decisions exactly as the daemon's /v1/host/changes routes expect
// (op, path and reason as JSON; the status filter as a query; approve and
// deny as POSTs on the record's own path with an optional reason), surface
// the machine's `supported` flag and an empty list without hiding either,
// and pass the daemon's refusals (no helper, not the owner) through as the
// typed errors apps branch on. Boundary: SwarmHostNamespace over the
// transport; a local HTTP stand-in is the narrowest layer that shows the
// wire format, not a live daemon.
test('SwarmHostNamespace: request, list, approve and deny on the wire', async () => {
  const seen: Array<{ method: string; path: string; body: unknown }> = [];
  const record = { id: 'hc_1', op: 'mount-workspace', path: '/home/person/app', reason: 'their app', status: 'pending', requested_by: 'token:setup', requested_at: 1 };
  const server = http.createServer((req, res) => {
    let raw = '';
    req.on('data', (chunk) => (raw += chunk));
    req.on('end', () => {
      seen.push({ method: req.method || '', path: req.url || '', body: raw ? JSON.parse(raw) : undefined });
      res.setHeader('Content-Type', 'application/json');
      const url = new URL(req.url || '/', 'http://127.0.0.1');
      if (req.method === 'POST' && url.pathname === '/v1/host/changes') {
        if (req.headers['x-no-helper']) {
          res.statusCode = 409;
          res.end(JSON.stringify({ ok: false, code: 'host_changes_unsupported', error: 'host changes are not supported on this machine' }));
          return;
        }
        res.statusCode = 201;
        res.end(JSON.stringify({ ok: true, change: record }));
        return;
      }
      if (req.method === 'GET' && url.pathname === '/v1/host/changes') {
        const status = url.searchParams.get('status');
        res.end(JSON.stringify({ ok: true, supported: status !== 'applied', changes: status === 'applied' ? null : [record] }));
        return;
      }
      if (req.method === 'POST' && url.pathname === '/v1/host/changes/hc_1/approve') {
        res.end(JSON.stringify({ ok: true, change: { ...record, status: 'applied', applied_at: 2 } }));
        return;
      }
      if (req.method === 'POST' && url.pathname === '/v1/host/changes/hc_1/deny') {
        res.statusCode = 403;
        res.end(JSON.stringify({ ok: false, error: 'deciding host changes requires the machine owner' }));
        return;
      }
      res.statusCode = 404;
      res.end(JSON.stringify({ ok: false, error: 'not found' }));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address() as { port: number };
  const client = new SwarmClient({ baseUrl: `http://127.0.0.1:${address.port}`, token: 'swk_test' });
  try {
    const requested = await client.host.request({ op: 'mount-workspace', path: '/home/person/app', reason: 'their app' });
    assert.equal(requested.id, 'hc_1');
    assert.equal(requested.status, 'pending');

    const all = await client.host.list();
    assert.equal(all.supported, true);
    assert.deepEqual(all.changes.map((c) => c.id), ['hc_1']);
    const applied = await client.host.list({ status: 'applied' });
    assert.deepEqual(applied, { supported: false, changes: [] });

    const approved = await client.host.approve('hc_1', { reason: 'yes' });
    assert.equal(approved.status, 'applied');
    await assert.rejects(client.host.deny('hc_1'), SwarmForbiddenError);
    await assert.rejects(client.host.approve('../auth/tokens'), /id is required/);

    assert.deepEqual(seen, [
      { method: 'POST', path: '/v1/host/changes', body: { op: 'mount-workspace', path: '/home/person/app', reason: 'their app' } },
      { method: 'GET', path: '/v1/host/changes', body: undefined },
      { method: 'GET', path: '/v1/host/changes?status=applied', body: undefined },
      { method: 'POST', path: '/v1/host/changes/hc_1/approve', body: { reason: 'yes' } },
      { method: 'POST', path: '/v1/host/changes/hc_1/deny', body: {} },
    ]);

    const noHelper = new SwarmClient({ baseUrl: `http://127.0.0.1:${address.port}`, token: 'swk_test', defaultHeaders: { 'x-no-helper': '1' } });
    await assert.rejects(
      noHelper.host.request({ op: 'mount-workspace', path: '/home/person/app', reason: 'their app' }),
      (err: unknown) => err instanceof SwarmConflictError && err.code === 'host_changes_unsupported',
    );
  } finally {
    server.close();
  }
});

test('SwarmHostNamespace: list tolerates a daemon that omits the changes array', async () => {
  const transport = { request: async () => ({ data: { ok: true, supported: true } }) } as unknown as SwarmTransport;
  assert.deepEqual(await new SwarmHostNamespace(transport).list(), { supported: true, changes: [] });
});
