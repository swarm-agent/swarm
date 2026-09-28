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
} from '../types.js';

// Invariant: SDK provides typed /v3/workers client matching backend authority.
// Threat: Protocol divergence, lost pagination, silent mutations, lack of revision CAS guards.
// Boundary: SwarmWorkersNamespace create, get, list, update, validate, export, import, automations.

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

test('SwarmWorkersNamespace: create worker sends correct envelope and parses response', async () => {
  let receivedMethod = '';
  let receivedPath = '';
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    receivedMethod = req.method || '';
    receivedPath = req.url || '';
    if (req.method === 'POST' && req.url === '/v3/workers') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
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

    const params: CreateWorkerParams = {
      name: 'DevOps Specialist',
      instructions: '# DevOps Specialist\nExecute infrastructure scripts safely.',
      idempotency_key: 'idemp-1234',
    };

    const record = await workers.create(params);
    assert.equal(receivedMethod, 'POST');
    assert.equal(receivedPath, '/v3/workers');
    assert.equal(receivedBody.name, 'DevOps Specialist');
    assert.equal(receivedBody.idempotency_key, 'idemp-1234');
    assert.equal(record.id, 'worker_1234567890abcdef');
    assert.equal(record.lifecycle_state, 'idle');
    assert.equal(record.revision, 1);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: get worker returns record or null on 404', async () => {
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v3/workers/worker_existing') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, worker: sampleWorkerRecord({ id: 'worker_existing' }) }));
    } else if (req.method === 'GET' && req.url === '/v3/workers/worker_nonexistent') {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: false, error: 'worker not found' }));
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
      await workers.get('');
    }, SwarmValidationError);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: list workers serializes params and preserves next_cursor and total_count', async () => {
  let requestedUrl = '';
  const server = http.createServer((req, res) => {
    requestedUrl = req.url || '';
    if (req.method === 'GET' && req.url?.startsWith('/v3/workers')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          workers: [sampleWorkerRecord({ id: 'w1' }), sampleWorkerRecord({ id: 'w2' })],
          next_cursor: 'cursor_offset_2',
          total_count: 5,
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
      after: 'prev_cursor',
      lifecycle_state: 'idle',
      include_deleted: true,
    });

    assert.ok(requestedUrl.includes('limit=2'));
    assert.ok(requestedUrl.includes('after=prev_cursor'));
    assert.ok(requestedUrl.includes('lifecycle_state=idle'));
    assert.ok(requestedUrl.includes('include_deleted=true'));

    assert.equal(res.workers.length, 2);
    assert.equal(res.next_cursor, 'cursor_offset_2');
    assert.equal(res.total_count, 5);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: update worker requires explicit revision CAS guard', async () => {
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
              ok: true,
              worker: sampleWorkerRecord({
                id: 'worker_target',
                revision: 3,
                name: receivedBody.name,
              }),
            })
          );
        } else {
          res.writeHead(409, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: 'revision conflict' }));
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

    // Reject non-numeric revision
    await assert.rejects(async () => {
      await workers.update('worker_target', 0, { name: 'New Name' });
    }, SwarmValidationError);

    const updated = await workers.update('worker_target', 2, {
      name: 'Updated DevOps Specialist',
      change_summary: 'renamed',
    });
    assert.equal(receivedMethod, 'PUT');
    assert.equal(receivedPath, '/v3/workers/worker_target');
    assert.equal(receivedBody.expected_revision, 2);
    assert.equal(receivedBody.name, 'Updated DevOps Specialist');
    assert.equal(updated.revision, 3);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: validate, export and import round-trip', async () => {
  const portableDef = samplePortableDefinition();
  let importBody: any = null;

  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v3/workers/validate') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        if (body.schema_version === 1 && body.name) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: true, definition: body }));
        } else {
          res.writeHead(400, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: false, error: 'invalid schema' }));
        }
      });
    } else if (req.method === 'GET' && req.url === '/v3/workers/worker_1/export') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          worker_id: 'worker_1',
          definition: portableDef,
          raw_json: JSON.stringify(portableDef, null, 2),
        })
      );
    } else if (req.method === 'POST' && req.url === '/v3/workers/import') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        importBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            worker: sampleWorkerRecord({
              id: importBody.target_worker_id || 'worker_newly_imported',
              revision: importBody.expected_revision ? importBody.expected_revision + 1 : 1,
              name: importBody.definition.name,
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

    // 1. Validate
    const valRes = await workers.validate(portableDef);
    assert.equal(valRes.ok, true);
    assert.equal(valRes.definition?.name, 'DevOps Specialist');

    // 2. Export
    const expRes = await workers.export('worker_1');
    assert.equal(expRes.ok, true);
    assert.equal(expRes.worker_id, 'worker_1');
    assert.equal(expRes.definition.name, 'DevOps Specialist');
    assert.ok(expRes.raw_json.includes('DevOps Specialist'));

    // 3. Import as new identity
    const importedNew = await workers.import(portableDef);
    assert.equal(importedNew.id, 'worker_newly_imported');
    assert.equal(importBody.target_worker_id, undefined);

    // 4. Import as update to existing target requires explicit revision
    await assert.rejects(async () => {
      await workers.import(portableDef, { targetWorkerId: 'worker_existing' });
    }, SwarmValidationError);

    const importedUpdate = await workers.import(portableDef, {
      targetWorkerId: 'worker_existing',
      expectedRevision: 3,
    });
    assert.equal(importedUpdate.id, 'worker_existing');
    assert.equal(importedUpdate.revision, 4);
    assert.equal(importBody.target_worker_id, 'worker_existing');
    assert.equal(importBody.expected_revision, 3);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmWorkersNamespace: attach, update, and remove automation definitions', async () => {
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
      res.end(JSON.stringify({ ok: true, worker: sampleWorkerRecord({ revision: 2 }) }));
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

    const autoDef: WorkerAutomationDefinition = {
      id: 'wauto_abc123',
      worker_id: 'worker_1',
      name: 'Hourly Heartbeat',
      activation_mode: 'interval',
      schedule: { kind: 'interval', interval_seconds: 3600 },
      enabled: true,
      plan_document: {
        title: 'Heartbeat Plan',
        checkpoints: [{ id: 'cp-1', title: 'Heartbeat', status: 'pending' }],
      },
      revision: 1,
      created_at: 1789990000000,
      updated_at: 1789990000000,
    };

    // Attach
    await workers.attachAutomation({
      worker_id: 'worker_1',
      expected_worker_revision: 1,
      automation: autoDef,
    });
    assert.equal(lastMethod, 'POST');
    assert.equal(lastUrl, '/v3/workers/worker_1/automations');
    assert.equal(lastBody.expected_worker_revision, 1);
    assert.equal(lastBody.automation.id, 'wauto_abc123');

    // Update
    await workers.updateAutomation({
      worker_id: 'worker_1',
      automation_id: 'wauto_abc123',
      expected_worker_revision: 2,
      automation: autoDef,
    });
    assert.equal(lastMethod, 'PUT');
    assert.equal(lastUrl, '/v3/workers/worker_1/automations/wauto_abc123');
    assert.equal(lastBody.expected_worker_revision, 2);

    // Remove
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
