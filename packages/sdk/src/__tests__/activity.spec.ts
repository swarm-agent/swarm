import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmActivityNamespace } from '../activity.js';
import { SwarmTransport } from '../transport.js';

// Purpose: the public iterator must preserve its cursor/filter/auth contract on
// reconnect and surface gaps. A loopback SSE peer tests actual HTTP parsing,
// not a mocked fetch or a provider/agent performance benchmark.
test('activity iterator resumes, surfaces gaps and keeps authentication', { timeout: 8000 }, async () => {
  const cursors: string[] = [];
  const server = http.createServer((req, res) => {
    const url = new URL(req.url!, 'http://localhost');
    cursors.push(url.searchParams.get('cursor')!);
    assert.equal(url.searchParams.get('project'), 'project');
    assert.equal(url.searchParams.get('worker'), 'worker');
    assert.equal(req.headers.authorization, 'Bearer test-token');
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    if (cursors.length === 1) res.end('event: activity\ndata: {"seq":1,"kind":"session.created"}\n\n');
    else if (cursors.length === 2) res.end('event: gap\ndata: {"from":2,"to":4,"cursor":"4"}\n\n');
    else res.end('event: activity\ndata: {"seq":5,"kind":"session.created"}\n\n');
  });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const address = server.address() as { port: number };
  try {
    const activity = new SwarmActivityNamespace(new SwarmTransport({ baseUrl: `http://127.0.0.1:${address.port}`, token: 'test-token' }));
    const events = [];
    for await (const event of activity.stream({ project: 'project', worker: 'worker' })) {
      events.push(event);
      if (events.length === 3) break;
    }
    assert.deepEqual(cursors, ['0', '1', '4']);
    assert.equal(events[1].kind, 'gap');
    assert.equal(events[2].kind, 'session.created');
  } finally { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); }
});

// Purpose: authorization errors must terminate rather than reconnect forever.
// The HTTP layer is the narrowest layer proving the iterator's retry decision.
test('activity iterator does not retry forbidden responses', { timeout: 3000 }, async () => {
  let requests = 0;
  const server = http.createServer((_req, res) => { requests++; res.writeHead(403); res.end(); });
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  try {
    const address = server.address() as { port: number };
    const activity = new SwarmActivityNamespace(new SwarmTransport({ baseUrl: `http://127.0.0.1:${address.port}` }));
    await assert.rejects(activity.stream().next(), /403/);
    assert.equal(requests, 1);
  } finally { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); }
});
