import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createBoundary } from '../boundary.mjs';

// Purpose: the reusable BFF must fail closed on public/origin-confused config;
// this pure boundary test is narrower than starting a daemon or container.
test('application configuration accepts only exact loopback origins', () => {
  for (const origin of ['http://0.0.0.0:8443', 'https://example.com', 'http://localhost:8443', 'http://127.0.0.1:8443/path', 'http://user@127.0.0.1:8443']) {
    assert.throws(() => createBoundary(origin), /exact HTTP/);
  }
  const boundary = createBoundary('http://127.0.0.1:8443');
  assert.throws(() => boundary.check({ headers: { host: 'example.com', origin: 'http://127.0.0.1:8443' } }), /Invalid host/);
  assert.throws(() => boundary.authenticate({ headers: {} }), /Sign in/);
  const { id, session } = boundary.open('owner');
  assert.throws(() => boundary.authenticate({ headers: { cookie: `swapp-loopback=${id}` } }), /CSRF/);
  assert.equal(boundary.authenticate({ headers: { cookie: `swapp-loopback=${id}`, 'x-csrf-token': session.csrf } }), session);
});

// Purpose: a Tailscale Serve origin is the only non-loopback origin accepted;
// near-miss names, ports, paths and plain HTTP stay rejected.
test('application configuration accepts exact Tailscale Serve origins', () => {
  for (const origin of ['http://swarm.tail1234.ts.net', 'https://swarm.tail1234.ts.net:8443', 'https://swarm.tail1234.ts.net/',
    'https://ts.net', 'https://tail1234.ts.net.example.com', 'https://user@swarm.tail1234.ts.net', 'https://-x.tail1234.ts.net']) {
    assert.throws(() => createBoundary(origin), /exact HTTP/);
  }
  const origin = 'https://swarm.tail1234.ts.net';
  const boundary = createBoundary(origin);
  assert.throws(() => boundary.check({ headers: { host: '127.0.0.1:8443', origin } }), /Invalid host/);
  assert.throws(() => boundary.check({ method: 'POST', headers: { host: 'swarm.tail1234.ts.net', origin: 'https://other.tail1234.ts.net' } }), /Invalid origin/);
  boundary.check({ method: 'POST', headers: { host: 'swarm.tail1234.ts.net', origin } });
  const { id } = boundary.open('owner');
  assert.match(boundary.cookie(id), /^__Host-swapp=[a-f0-9]{64}; Path=\/; Secure; HttpOnly; SameSite=Strict/);
});
