import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createBoundary } from '../boundary.mjs';

// Purpose: the reusable BFF must fail closed on public/origin-confused config;
// this pure boundary test is narrower than starting a daemon or container.
test('application configuration accepts only exact loopback origins', () => {
  for (const origin of ['http://0.0.0.0:8443', 'https://example.com', 'http://localhost:8443', 'http://127.0.0.1:8443/path', 'http://user@127.0.0.1:8443']) {
    assert.throws(() => createBoundary(origin, 'private-test-secret'), /exact HTTP/);
  }
  const boundary = createBoundary('http://127.0.0.1:8443', 'private-test-secret');
  assert.throws(() => boundary.check({ headers: { host: 'example.com', origin: 'http://127.0.0.1:8443' } }), /Invalid host/);
  assert.throws(() => boundary.authenticate({ headers: {} }), /Sign in/);
  const { id, session } = boundary.login('private-test-secret');
  assert.throws(() => boundary.authenticate({ headers: { cookie: `swapp-loopback=${id}` } }), /CSRF/);
  assert.equal(boundary.authenticate({ headers: { cookie: `swapp-loopback=${id}`, 'x-csrf-token': session.csrf } }), session);
});
