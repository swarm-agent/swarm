import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmWorkersNamespace } from '../workers.js';
import { SwarmTransport } from '../transport.js';
import { SwarmValidationError } from '../errors.js';
import type {
  WorkerRecord,
  PortableWorkerDefinition,
  CreateWorkerParams,
  WorkerAutomationDefinition,
  WorkerAutomationInput,
} from '../types.js';

// Invariant: SDK provides typed /v3/workers client matching backend authority.
// Threat: Wire protocol divergence, lost cursor pagination, lack of revision CAS guards, malformed envelope acceptance.
// Boundary: SwarmWorkersNamespace create, get, list, update, delete, validate, export, import, automations.

function sampleWorkerRecord(overrides?: Partial<WorkerRecord>): WorkerRecord {
  return {
    id: 'worker_1234567890abcdef',
    account_scope_id: 'acct_main',
    name: 'DevOps Specialist',
    description: 'Specialist for infrastructure tasks',
    instructions: '# DevOps Specialist\nExecute infrastructure scripts safely.',
    lifecycle_state: 'idle',
    revision: 1,
    requested_capabilities: [{ type: 'tool', name: 'bash', required: true }],
    workspace_requirements: [{ role: 'primary', required: true }],
    automations: [],
    created_at: 1789990000000,
    updated_at: 1789990000000,
    ...overrides,
  };
}

function samplePortableDefinition(): PortableWorkerDefinition {
  return {
    schema_version: 1,
    name: 'DevOps Specialist',
    description: 'Specialist for infrastructure tasks',
    instructions: '# DevOps Specialist\nExecute infrastructure scripts safely.',
    capabilities: [{ type: 'tool', name: 'bash', required: true }],
    workspace_requirements: [{ role: 'primary', required: true }],
    automations: [
      {
        name: 'Daily Check',
        activation_mode: 'manual',
        enabled: true,
        plan: {
          title: 'Daily Check Plan',
          checkpoints: [
            {
              id: 'cp-1',
              title: 'Check',
              status: 'pending',
              subtasks: [{ id: 'sub-1', title: 'Ping server', status: 'pending' }],
            },
          ],
        },
      },
    ],
  };
}

test('SwarmWorkersNamespace: create worker sends required idempotency_key, excludes caller id/bindings, parses envelope', async () => {
  let receivedMethod = '';
  let receivedPath = '';
  let receivedHeaders: http.IncomingHttpHeaders = {};
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    receivedMethod = req.method || '';
    receivedPath = req.url || '';
    receivedHeaders = req.headers;
    if (req.method === 'POST' && req.url === '/v3/workers') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(201, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            worker: sampleWorkerRecord({
              name: receivedBody.name,
              instructions: receivedBody.instructions,
            }),
          })
        );
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    // Validation: missing idempotency_key rejected
    await assert.rejects(async () => {
      await workers.create({
        name: 'DevOps Specialist',
        instructions: 'Do work',
        idempotency_key: '',
      });
    }, SwarmValidationError);

    // Validation: missing name rejected
    await assert.rejects(async () => {
      await workers.create({
        name: '   ',
        instructions: 'Do work',
        idempotency_key: 'idemp-1',
      });
    }, SwarmValidationError);

    const params: CreateWorkerParams = {
      name: 'DevOps Specialist',
      instructions: '# DevOps Specialist\nExecute infrastructure scripts safely.',
      idempotency_key: 'idemp-1234',
    };

    const record = await workers.create(params);
    assert.equal(receivedMethod, 'POST');
    assert.equal(receivedPath, '/v3/workers');
    assert.equal(receivedHeaders['idempotency-key'], 'idemp-1234');
    assert.equal(receivedBody.name, 'DevOps Specialist');
    assert.equal(receivedBody.idempotency_key, 'idemp-1234');
    assert.equal(receivedBody.id, undefined);
    assert.equal(receivedBody.local_bindings, undefined);
    assert.equal(record.id, 'worker_1234567890abcdef');
    assert.equal(record.lifecycle_state, 'idle');
    assert.equal(record.revision, 1);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: create worker rejects malformed response envelope', async () => {
  const server = http.createServer((_req, res) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: true })); // missing worker field
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    await assert.rejects(async () => {
      await workers.create({
        name: 'DevOps Specialist',
        instructions: 'Do work',
        idempotency_key: 'idemp-1234',
      });
    }, SwarmValidationError);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: get worker returns record or null on 404, rejects blank id', async () => {
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v3/workers/worker_existing') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ worker: sampleWorkerRecord({ id: 'worker_existing' }) }));
    } else if (req.method === 'GET' && req.url === '/v3/workers/worker_nonexistent') {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'worker not found' }));
    } else {
      res.writeHead(500);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    const found = await workers.get('worker_existing');
    assert.ok(found);
    assert.equal(found?.id, 'worker_existing');

    const notFound = await workers.get('worker_nonexistent');
    assert.equal(notFound, null);

    await assert.rejects(async () => {
      await workers.get('   ');
    }, SwarmValidationError);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: list workers uses cursor parameter and returns no total_count', async () => {
  let requestedUrl = '';
  const server = http.createServer((req, res) => {
    requestedUrl = req.url || '';
    if (req.method === 'GET' && req.url?.startsWith('/v3/workers')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          workers: [sampleWorkerRecord({ id: 'w1' }), sampleWorkerRecord({ id: 'w2' })],
          next_cursor: 'cursor_offset_2',
        })
      );
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    const res = await workers.list({
      limit: 2,
      cursor: 'cursor_offset_1',
      lifecycle_state: 'idle',
      include_deleted: true,
    });

    assert.ok(requestedUrl.includes('limit=2'));
    assert.ok(requestedUrl.includes('cursor=cursor_offset_1'));
    assert.ok(!requestedUrl.includes('after='));
    assert.ok(requestedUrl.includes('lifecycle_state=idle'));
    assert.ok(requestedUrl.includes('include_deleted=true'));

    assert.equal(res.workers.length, 2);
    assert.equal(res.next_cursor, 'cursor_offset_2');
    assert.equal((res as any).total_count, undefined);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: update worker sends expected_revision in body only and no local_bindings', async () => {
  let receivedMethod = '';
  let receivedPath = '';
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    receivedMethod = req.method || '';
    receivedPath = req.url || '';
    if (req.method === 'PUT' && req.url === '/v3/workers/worker_target') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        if (receivedBody.expected_revision === 2) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              worker: sampleWorkerRecord({
                id: 'worker_target',
                revision: 3,
                name: receivedBody.name,
              }),
            })
          );
        } else {
          res.writeHead(409, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: 'revision conflict' }));
        }
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    // Reject non-numeric or non-safe-integer revision
    await assert.rejects(async () => {
      await workers.update('worker_target', 0, { name: 'New Name' });
    }, SwarmValidationError);

    await assert.rejects(async () => {
      await workers.update('worker_target', 1.5, { name: 'New Name' });
    }, SwarmValidationError);

    const updated = await workers.update('worker_target', 2, {
      name: 'Updated DevOps Specialist',
      change_summary: 'renamed',
    });
    assert.equal(receivedMethod, 'PUT');
    assert.equal(receivedPath, '/v3/workers/worker_target');
    assert.ok(!receivedPath.includes('expected_revision'));
    assert.equal(receivedBody.expected_revision, 2);
    assert.equal(receivedBody.local_bindings, undefined);
    assert.equal(receivedBody.name, 'Updated DevOps Specialist');
    assert.equal(updated.revision, 3);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Invariant: Worker deletion is supported over canonical API with safe stop barrier and expected_revision CAS.
test('SwarmWorkersNamespace: worker deletion sends expected_revision and validates { ok: true, deleted: true }', async () => {
  assert.equal('delete' in SwarmWorkersNamespace.prototype, true);
  let receivedMethod = '';
  let receivedUrl = '';
  const server = http.createServer((req, res) => {
    receivedMethod = req.method || '';
    receivedUrl = req.url || '';
    if (req.method === 'DELETE' && req.url === '/v3/workers/w1?expected_revision=2') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, deleted: true }));
    } else {
      res.writeHead(400);
      res.end();
    }
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;
  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 't', defaultHeaders: {}, timeoutMs: 5000 });
    const workers = new SwarmWorkersNamespace(transport);
    const result = await workers.delete({ worker_id: 'w1', expected_revision: 2 });
    assert.deepEqual(result, { ok: true, deleted: true });
    assert.equal(receivedMethod, 'DELETE');
    assert.equal(receivedUrl, '/v3/workers/w1?expected_revision=2');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: validate sends raw PortableWorkerDefinition and parses { valid: true, worker: doc }', async () => {
  const portableDef = samplePortableDefinition();
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v3/workers/validate') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        if (receivedBody.schema_version === 1 && receivedBody.name) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ valid: true, worker: receivedBody }));
        } else {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: 'invalid schema' }));
        }
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    const valRes = await workers.validate(portableDef);
    assert.equal(valRes.valid, true);
    assert.equal(valRes.worker.name, 'DevOps Specialist');
    assert.equal(receivedBody.schema_version, 1);
    assert.equal(receivedBody.definition, undefined); // verified raw body, not wrapped
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: export worker returns { worker: doc } and rejects malformed envelope', async () => {
  const portableDef = samplePortableDefinition();

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v3/workers/worker_1/export') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ worker: portableDef }));
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    await assert.rejects(async () => {
      await workers.export('   ');
    }, SwarmValidationError);

    const expRes = await workers.export('worker_1');
    assert.equal(expRes.worker.name, 'DevOps Specialist');
    assert.equal(expRes.worker.schema_version, 1);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: import sends raw doc body with mode and idempotency query parameters', async () => {
  const portableDef = samplePortableDefinition();
  let receivedUrl = '';
  let receivedHeaders: http.IncomingHttpHeaders = {};
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    receivedUrl = req.url || '';
    receivedHeaders = req.headers;
    if (req.method === 'POST' && req.url?.startsWith('/v3/workers/import')) {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        const u = new URL(req.url!, 'http://127.0.0.1');
        const mode = u.searchParams.get('mode');
        const workerId = u.searchParams.get('worker_id') || 'worker_imported_new';
        const rev = u.searchParams.get('expected_revision');
        res.writeHead(mode === 'new' ? 201 : 200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            worker: sampleWorkerRecord({
              id: workerId,
              revision: rev ? Number(rev) + 1 : 1,
              name: receivedBody.name,
            }),
          })
        );
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    // 1. New import requires idempotencyKey option
    await assert.rejects(async () => {
      await workers.import(portableDef, {} as any);
    }, SwarmValidationError);

    // 2. New import sends raw body, query mode=new&idempotency_key=...
    const importedNew = await workers.import(portableDef, {
      mode: 'new',
      idempotencyKey: 'idemp-import-1',
    });
    assert.equal(importedNew.id, 'worker_imported_new');
    assert.equal(importedNew.revision, 1);
    assert.ok(receivedUrl.includes('mode=new'));
    assert.ok(receivedUrl.includes('idempotency_key=idemp-import-1'));
    assert.equal(receivedHeaders['idempotency-key'], 'idemp-import-1');
    assert.equal(receivedBody.name, 'DevOps Specialist');
    assert.equal(receivedBody.definition, undefined); // verified raw body

    // 3. Update import requires targetWorkerId and expectedRevision >= 1
    await assert.rejects(async () => {
      await workers.import(portableDef, {
        mode: 'update',
        targetWorkerId: '',
        expectedRevision: 1,
      });
    }, SwarmValidationError);

    await assert.rejects(async () => {
      await workers.import(portableDef, {
        mode: 'update',
        targetWorkerId: 'worker_target',
        expectedRevision: 0,
      });
    }, SwarmValidationError);

    const importedUpdate = await workers.import(portableDef, {
      mode: 'update',
      targetWorkerId: 'worker_target',
      expectedRevision: 4,
    });
    assert.equal(importedUpdate.id, 'worker_target');
    assert.equal(importedUpdate.revision, 5);
    assert.ok(receivedUrl.includes('mode=update'));
    assert.ok(receivedUrl.includes('worker_id=worker_target'));
    assert.ok(receivedUrl.includes('expected_revision=4'));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: attach and update send body {expected_worker_revision,automation} without query params', async () => {
  let lastMethod = '';
  let lastUrl = '';
  let lastBody: any = null;

  const server = http.createServer((req, res) => {
    lastMethod = req.method || '';
    lastUrl = req.url || '';
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      if (chunks.length > 0) {
        lastBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ worker: sampleWorkerRecord({ revision: 2 }) }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_test_auth',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const workers = new SwarmWorkersNamespace(transport);

    const autoInput: WorkerAutomationInput = {
      name: 'Hourly Heartbeat',
      activation_mode: 'interval',
      schedule: { kind: 'interval', interval_seconds: 3600 },
      enabled: true,
      plan_document: {
        title: 'Heartbeat Plan',
        checkpoints: [{ id: 'cp-1', title: 'Heartbeat', status: 'pending' }],
      },
    };

    // 1. Attach automation
    await workers.attachAutomation({
      worker_id: 'worker_1',
      expected_worker_revision: 1,
      automation: autoInput,
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/worker_1/automations');
    assert.ok(!lastUrl.includes('expected_worker_revision'));
    assert.equal(lastBody.expected_worker_revision, 1);
    assert.equal(lastBody.automation.name, 'Hourly Heartbeat');

    // 2. Update automation
    await workers.updateAutomation({
      worker_id: 'worker_1',
      automation_id: 'wauto_abc123',
      expected_worker_revision: 2,
      automation: autoInput,
    });
    assert.equal(lastMethod, 'PUT');
    assert.equal(lastUrl, '/v3/workers/worker_1/automations/wauto_abc123');
    assert.ok(!lastUrl.includes('expected_worker_revision'));
    assert.equal(lastBody.expected_worker_revision, 2);
    assert.equal(lastBody.automation.name, 'Hourly Heartbeat');

    // 3. Remove automation sends expected_worker_revision query
    await workers.removeAutomation({
      worker_id: 'worker_1',
      automation_id: 'wauto_abc123',
      expected_worker_revision: 3,
    });
    assert.equal(lastMethod, 'DELETE');
    assert.ok(lastUrl.includes('/v3/workers/worker_1/automations/wauto_abc123'));
    assert.ok(lastUrl.includes('expected_worker_revision=3'));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Requirement: unsupported authority fields fail locally rather than disappearing during serialization.
// Boundary: worker SDK request validation; no transport should be invoked on rejection.
test('SwarmWorkersNamespace: rejects caller authority and invalid revision without transport', async () => {
  let calls = 0;
  const transport = { request: async () => { calls++; throw new Error('unexpected transport'); } } as unknown as SwarmTransport;
  const workers = new SwarmWorkersNamespace(transport);
  await assert.rejects(workers.create({ name: 'Worker', idempotency_key: 'key', id: 'forged' } as any), SwarmValidationError);
  await assert.rejects(workers.update('worker', 1, { local_bindings: { primary: 'foreign' } } as any), SwarmValidationError);
  for (const revision of [NaN, Infinity, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
    await assert.rejects(workers.update('worker', revision, { name: 'Updated' }), SwarmValidationError);
  }
  assert.equal(calls, 0);
});

test('SwarmWorkersNamespace: activate, deploy, pause, resume, archive lifecycle methods send correct endpoints and CAS bodies', async () => {
  let lastMethod = '';
  let lastUrl = '';
  let lastBody: any = null;

  const server = http.createServer((req, res) => {
    lastMethod = req.method || '';
    lastUrl = req.url || '';
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      if (chunks.length > 0) {
        lastBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        worker: sampleWorkerRecord({
          id: 'w_life',
          revision: 2,
          lifecycle_state: lastUrl.includes('/pause') ? 'paused' : lastUrl.includes('/archive') ? 'archived' : 'active',
          local_bindings: lastBody?.local_bindings,
        }),
      }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 'swk_test', defaultHeaders: {}, timeoutMs: 5000 });
    const workers = new SwarmWorkersNamespace(transport);

    // 1. Activate
    const act = await workers.activate({
      worker_id: 'w_life',
      expected_revision: 1,
      local_bindings: { primary: 'ws_abc' },
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_life/activate');
    assert.equal(lastBody.expected_revision, 1);
    assert.deepEqual(lastBody.local_bindings, { primary: 'ws_abc' });
    assert.equal(act.lifecycle_state, 'active');

    // 2. Deploy alias
    const dep = await workers.deploy({
      worker_id: 'w_life',
      expected_revision: 2,
      local_bindings: { primary: 'ws_abc' },
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_life/activate');
    assert.equal(dep.lifecycle_state, 'active');

    // 3. Pause
    const paused = await workers.pause({ worker_id: 'w_life', expected_revision: 2 });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_life/pause');
    assert.equal(lastBody.expected_revision, 2);
    assert.equal(paused.lifecycle_state, 'paused');

    // 4. Resume
    const resumed = await workers.resume({ worker_id: 'w_life', expected_revision: 3 });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_life/resume');
    assert.equal(lastBody.expected_revision, 3);
    assert.equal(resumed.lifecycle_state, 'active');

    // 5. Archive
    const archived = await workers.archive({ worker_id: 'w_life', expected_revision: 4 });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_life/archive');
    assert.equal(lastBody.expected_revision, 4);
    assert.equal(archived.lifecycle_state, 'archived');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: enableAutomation and disableAutomation send expected_worker_revision in body', async () => {
  let lastMethod = '';
  let lastUrl = '';
  let lastBody: any = null;

  const server = http.createServer((req, res) => {
    lastMethod = req.method || '';
    lastUrl = req.url || '';
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      lastBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ worker: sampleWorkerRecord({ id: 'w1', revision: 3 }) }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 'swk_test', defaultHeaders: {}, timeoutMs: 5000 });
    const workers = new SwarmWorkersNamespace(transport);

    await workers.enableAutomation({ worker_id: 'w1', automation_id: 'a1', expected_worker_revision: 2 });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w1/automations/a1/enable');
    assert.equal(lastBody.expected_worker_revision, 2);

    await workers.disableAutomation({ worker_id: 'w1', automation_id: 'a1', expected_worker_revision: 3 });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w1/automations/a1/disable');
    assert.equal(lastBody.expected_worker_revision, 3);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: directRequest, testRun, trigger dispatch and parse WorkerRunRecord envelopes', async () => {
  let lastMethod = '';
  let lastUrl = '';
  let lastHeaders: http.IncomingHttpHeaders = {};
  let lastBody: any = null;

  const server = http.createServer((req, res) => {
    lastMethod = req.method || '';
    lastUrl = req.url || '';
    lastHeaders = req.headers;
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      lastBody = chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : {};
      res.writeHead(201, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        ok: true,
        run: {
          id: 'wrun_test_1',
          account_scope_id: 'acct_1',
          worker_id: 'w_exec',
          worker_revision: 1,
          request_source: lastUrl.includes('/test') ? 'test_run' : lastUrl.includes('/trigger') ? 'trigger' : 'direct',
          status: 'admitted',
          created_at: Date.now(),
        },
      }));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 'swk_test', defaultHeaders: {}, timeoutMs: 5000 });
    const workers = new SwarmWorkersNamespace(transport);

    // Direct request validation
    await assert.rejects(workers.directRequest({ worker_id: 'w_exec' }), SwarmValidationError);

    // Direct request
    const directRun = await workers.directRequest({
      worker_id: 'w_exec',
      prompt: 'Execute query',
      idempotency_key: 'idemp_dir',
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_exec/direct');
    assert.equal(lastHeaders['idempotency-key'], 'idemp_dir');
    assert.equal(lastBody.prompt, 'Execute query');
    assert.equal(directRun.request_source, 'direct');

    // Test run
    const testRun = await workers.testRun({
      worker_id: 'w_exec',
      automation_id: 'auto_ping',
      prompt: 'Ping test',
      idempotency_key: 'idemp_test',
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_exec/test');
    assert.equal(lastHeaders['idempotency-key'], 'idemp_test');
    assert.equal(lastBody.automation_id, 'auto_ping');
    assert.equal(testRun.request_source, 'test_run');

    // Worker trigger
    const trigRun = await workers.trigger({
      worker_id: 'w_exec',
      payload: { event: 'alert' },
      idempotency_key: 'idemp_trig',
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_exec/trigger');
    assert.equal(lastHeaders['idempotency-key'], 'idemp_trig');
    assert.deepEqual(lastBody.payload, { event: 'alert' });
    assert.equal(trigRun.request_source, 'trigger');

    // Automation trigger
    await workers.trigger({
      worker_id: 'w_exec',
      automation_id: 'auto_webhook',
      payload: { event: 'webhook' },
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/w_exec/automations/auto_webhook/trigger');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: mintTriggerToken, listRuns, getRun, cancelRun, getHistory, getRevision work as specified', async () => {
  let lastUrl = '';
  let lastMethod = '';

  const server = http.createServer((req, res) => {
    lastUrl = req.url || '';
    lastMethod = req.method || '';
    if (req.method === 'POST' && req.url === '/v3/workers/w1/token') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, token: 'swk_scoped_tok', worker_id: 'w1', scopes: ['workers:trigger'] }));
      return;
    }
    if (req.method === 'GET' && req.url?.startsWith('/v3/workers/w1/runs')) {
      if (req.url === '/v3/workers/w1/runs/r_404') {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'not found' }));
        return;
      }
      if (req.url === '/v3/workers/w1/runs/r1') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ run: { id: 'r1', worker_id: 'w1', status: 'admitted', created_at: 1000 } }));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ runs: [{ id: 'r1', worker_id: 'w1', status: 'admitted', created_at: 1000 }], next_cursor: 'cur2' }));
      return;
    }
    if (req.method === 'POST' && req.url === '/v3/workers/w1/runs/r1/cancel') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ run: { id: 'r1', worker_id: 'w1', status: 'cancelled', error: 'run cancelled', created_at: 1000 } }));
      return;
    }
    if (req.method === 'GET' && req.url?.startsWith('/v3/workers/w1/history')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ history: [{ worker_id: 'w1', revision: 1, committed_at: 1000 }], next_cursor: '' }));
      return;
    }
    if (req.method === 'GET' && req.url === '/v3/workers/w1/revisions/1') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ revision: { worker_id: 'w1', revision: 1, committed_at: 1000 } }));
      return;
    }
    res.writeHead(404);
    res.end();
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 'swk_test', defaultHeaders: {}, timeoutMs: 5000 });
    const workers = new SwarmWorkersNamespace(transport);

    // 1. Mint trigger token
    const tokenResult = await workers.mintTriggerToken({ worker_id: 'w1', name: 'Hook Token' });
    assert.equal(tokenResult.token, 'swk_scoped_tok');
    assert.equal(tokenResult.worker_id, 'w1');

    // 2. List runs
    const runsResult = await workers.listRuns({ worker_id: 'w1', limit: 10, cursor: 'cur1' });
    assert.equal(runsResult.runs.length, 1);
    assert.equal(runsResult.next_cursor, 'cur2');
    assert.ok(lastUrl.includes('limit=10'));
    assert.ok(lastUrl.includes('cursor=cur1'));

    // 3. Get run (existing)
    const run = await workers.getRun({ worker_id: 'w1', run_id: 'r1' });
    assert.ok(run);
    assert.equal(run.id, 'r1');

    // 4. Get run (404 -> null)
    const notFoundRun = await workers.getRun({ worker_id: 'w1', run_id: 'r_404' });
    assert.equal(notFoundRun, null);

    // 5. Cancel run
    const cancelled = await workers.cancelRun({ worker_id: 'w1', run_id: 'r1' });
    assert.equal(cancelled.status, 'cancelled');

    // 6. History
    const historyResult = await workers.getHistory({ worker_id: 'w1' });
    assert.equal(historyResult.history.length, 1);

    // 7. Revision
    const revRecord = await workers.getRevision({ worker_id: 'w1', revision: 1 });
    assert.ok(revRecord);
    assert.equal(revRecord.revision, 1);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
