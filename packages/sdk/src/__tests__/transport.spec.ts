import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import {
  SwarmApiError,
  SwarmAuthError,
  SwarmConflictError,
  SwarmForbiddenError,
  SwarmNotFoundError,
  SwarmTimeoutError,
} from '../errors.js';
import { SwarmTransport } from '../transport.js';

test('SwarmTransport: serializes headers, token and JSON body', async () => {
  let receivedHeaders: http.IncomingHttpHeaders | null = null;
  let receivedBody = '';

  const server = http.createServer((req, res) => {
    receivedHeaders = req.headers;
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      receivedBody = Buffer.concat(chunks).toString('utf8');
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, echo: JSON.parse(receivedBody) }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_secret_token',
      defaultHeaders: { 'X-Custom-App': 'SwarmTest' },
      timeoutMs: 5000,
    });

    const res = await transport.request<{ ok: boolean; echo: any }>('/v1/echo', {
      method: 'POST',
      body: { greeting: 'hello world', num: 42 },
    });

    assert.equal(res.status, 200);
    assert.equal(res.data.ok, true);
    assert.equal(res.data.echo.greeting, 'hello world');
    assert.equal(res.data.echo.num, 42);
    assert.equal(receivedHeaders?.['authorization'], 'Bearer swk_test_secret_token');
    assert.equal(receivedHeaders?.['x-custom-app'], 'SwarmTest');
    assert.equal(receivedHeaders?.['content-type'], 'application/json');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmTransport: classifies HTTP error status codes into typed Swarm errors', async () => {
  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');
    const status = parseInt(url.searchParams.get('status') || '200', 10);
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: false, error: `Simulated error ${status}`, code: `ERR_${status}` }));
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    // 401 Unauthorized
    await assert.rejects(
      async () => transport.request('/test?status=401'),
      (err: any) => {
        assert.ok(err instanceof SwarmAuthError);
        assert.equal(err.status, 401);
        assert.equal(err.message, 'Simulated error 401');
        assert.equal(err.code, 'ERR_401');
        return true;
      }
    );

    // 403 Forbidden
    await assert.rejects(
      async () => transport.request('/test?status=403'),
      (err: any) => {
        assert.ok(err instanceof SwarmForbiddenError);
        assert.equal(err.status, 403);
        assert.equal(err.message, 'Simulated error 403');
        return true;
      }
    );

    // 404 Not Found
    await assert.rejects(
      async () => transport.request('/test?status=404'),
      (err: any) => {
        assert.ok(err instanceof SwarmNotFoundError);
        assert.equal(err.status, 404);
        assert.equal(err.message, 'Simulated error 404');
        return true;
      }
    );

    // 409 Conflict
    await assert.rejects(
      async () => transport.request('/test?status=409'),
      (err: any) => {
        assert.ok(err instanceof SwarmConflictError);
        assert.equal(err.status, 409);
        assert.equal(err.message, 'Simulated error 409');
        return true;
      }
    );

    // 500 Internal Server Error
    await assert.rejects(
      async () => transport.request('/test?status=500'),
      (err: any) => {
        assert.ok(err instanceof SwarmApiError);
        assert.equal(err.status, 500);
        assert.equal(err.message, 'Simulated error 500');
        return true;
      }
    );
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmTransport: enforces request timeout and throws SwarmTimeoutError', async () => {
  const server = http.createServer((_req, res) => {
    // Intentionally delay response to trigger timeout
    setTimeout(() => {
      res.writeHead(200);
      res.end('ok');
    }, 500);
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      defaultHeaders: {},
      timeoutMs: 100, // 100ms timeout
    });

    await assert.rejects(
      async () => transport.request('/slow'),
      (err: any) => {
        assert.ok(err instanceof SwarmTimeoutError);
        assert.equal(err.timeoutMs, 100);
        return true;
      }
    );
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
