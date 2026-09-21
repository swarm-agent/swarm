import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmAutomationsNamespace } from '../automations.js';
import { SwarmTransport } from '../transport.js';

test('SwarmAutomationsNamespace: trigger admits occurrence with runtime context payload', async () => {
  let receivedBody: any = null;

  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v3/automations/v2/trigger') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        receivedBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            occurrence: {
              id: 'occ_test_12345',
              session_id: 'av2-execution-occ_test_12345',
              state: 'running',
              due_at: 1789990500,
              admitted_at: 1789990500,
              accepted: {
                automation_id: receivedBody.worker_id,
                generation: 1,
              },
            },
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
      token: 'swk_trigger_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const automations = new SwarmAutomationsNamespace(transport);
    const result = await automations.trigger({
      workspace_id: 'ws_1',
      worker_id: 'auto_ci_repair',
      context: {
        event: 'github_push',
        commit: 'abc1234',
        branch: 'fix/ci',
      },
    });

    assert.equal(result.ok, true);
    assert.equal(result.occurrence.id, 'occ_test_12345');
    assert.equal(result.occurrence.accepted?.automation_id, 'auto_ci_repair');
    assert.equal(result.occurrence.state, 'running');

    assert.equal(receivedBody.workspace_id, 'ws_1');
    assert.equal(receivedBody.worker_id, 'auto_ci_repair');
    assert.equal(receivedBody.context.event, 'github_push');
    assert.equal(receivedBody.context.commit, 'abc1234');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmAutomationsNamespace: list, get, getProgress, delete and pause/resume controls', async () => {
  let requestedUrl = '';
  let controlAction = '';

  const server = http.createServer((req, res) => {
    requestedUrl = req.url || '';
    if (req.method === 'GET' && req.url?.startsWith('/v3/automations/v2?')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          records: [
            {
              automation_id: 'worker_unit_test',
              account_id: 'acct_1',
              workspace_id: 'ws_1',
              session_id: 'sess_1',
              generation: 1,
              enabled: true,
              cancelled: false,
            },
          ],
        })
      );
    } else if (req.method === 'GET' && req.url?.startsWith('/v3/automations/v2/progress?')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          record: { automation_id: 'worker_unit_test' },
          observed_at: 1789990000,
          timezone: 'UTC',
          forecast: [],
          forecast_is_admission: false,
          complete: false,
          occurrences: [],
        })
      );
    } else if (req.method === 'POST' && req.url === '/v3/automations/v2/control') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        controlAction = body.action;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
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
      token: 'swk_admin_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const automations = new SwarmAutomationsNamespace(transport);

    // List
    const list = await automations.list({ workspace_id: 'ws_1', archived_mode: 'exclude' });
    assert.equal(list.length, 1);
    assert.equal(list[0].automation_id, 'worker_unit_test');
    assert.ok(requestedUrl.includes('workspace_id=ws_1'));
    assert.ok(requestedUrl.includes('action=list'));

    // Progress
    const progress = await automations.getProgress({ workspace_id: 'ws_1', worker_id: 'worker_unit_test' });
    assert.equal(progress.record.automation_id, 'worker_unit_test');

    // Pause
    const paused = await automations.pause({ workspace_id: 'ws_1', session_id: 'sess_1', generation: 1 });
    assert.equal(paused, true);
    assert.equal(controlAction, 'pause');

    // Resume
    const resumed = await automations.resume({ workspace_id: 'ws_1', session_id: 'sess_1', generation: 1 });
    assert.equal(resumed, true);
    assert.equal(controlAction, 'resume');

    // Delete
    const deleted = await automations.delete({ workspace_id: 'ws_1', session_id: 'sess_1' });
    assert.equal(deleted, true);
    assert.equal(controlAction, 'delete_automation');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
