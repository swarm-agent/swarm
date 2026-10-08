import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmPermissionsNamespace } from '../permissions.js';
import { SwarmTransport } from '../transport.js';

test('SwarmPermissionsNamespace: getPolicy, setBypass, setBashProfile, addRule, removeRule, resetPolicy, explain', async () => {
  let bypassValue: boolean | null = null;
  let bashProfileValue: string | null = null;
  let addedRule: any = null;
  let removedRuleId: string | null = null;
  let resetCalled = false;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'GET' && url.pathname === '/v1/permissions') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          policy: {
            version: 1,
            bash_profile: 'strict',
            rules: [{ id: 'rule_1', kind: 'tool', decision: 'allow', tool: 'search' }],
            updated_at: 1000,
          },
        })
      );
    } else if (req.method === 'POST' && url.pathname === '/v1/permissions/bypass') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        bypassValue = body.enabled;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, bypass_permissions: bypassValue }));
      });
    } else if (req.method === 'POST' && url.pathname === '/v1/permissions/bash-profile') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        bashProfileValue = body.bash_profile;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, bash_profile: bashProfileValue }));
      });
    } else if (req.method === 'POST' && url.pathname === '/v1/permissions') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        addedRule = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            rule: { id: 'rule_new', ...addedRule, created_at: 2000, updated_at: 2000 },
          })
        );
      });
    } else if (req.method === 'DELETE' && url.pathname.startsWith('/v1/permissions/')) {
      removedRuleId = url.pathname.replace('/v1/permissions/', '');
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, removed: true }));
    } else if (req.method === 'POST' && url.pathname === '/v1/permissions/reset') {
      resetCalled = true;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          policy: { version: 1, bash_profile: 'strict', rules: [], updated_at: 3000 },
        })
      );
    } else if (req.method === 'GET' && url.pathname === '/v1/permissions/explain') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          explain: {
            decision: 'allow',
            source: 'rule',
            reason: 'explicit allow rule',
            tool_name: url.searchParams.get('tool'),
          },
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const perms = new SwarmPermissionsNamespace(transport);

    const policy = await perms.getPolicy();
    assert.equal(policy.version, 1);
    assert.equal(policy.bash_profile, 'strict');
    assert.equal(policy.rules?.length, 1);

    const bypassed = await perms.setBypass(true);
    assert.equal(bypassed, true);
    assert.equal(bypassValue, true);

    const profile = await perms.setBashProfile('permissive');
    assert.equal(profile, 'permissive');
    assert.equal(bashProfileValue, 'permissive');

    const rule = await perms.addRule({ kind: 'tool', decision: 'allow', tool: 'search' });
    assert.equal(rule.id, 'rule_new');
    assert.equal(addedRule.tool, 'search');

    const removed = await perms.removeRule('rule_new');
    assert.equal(removed, true);
    assert.equal(removedRuleId, 'rule_new');

    const reset = await perms.resetPolicy();
    assert.equal(resetCalled, true);
    assert.equal(reset.rules?.length, 0);

    const explain = await perms.explain('auto', 'search');
    assert.equal(explain.decision, 'allow');
    assert.equal(explain.tool_name, 'search');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmPermissionsNamespace: listSessionPending, listSessionAll, resolve, and resolveAll', async () => {
  let resolvedAction: string | null = null;
  let resolvedReason: string | null = null;
  let resolvedAllAction: string | null = null;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'GET' && url.pathname === '/v3/sessions/sess_test_1/permissions') {
      const status = url.searchParams.get('status');
      res.writeHead(200, { 'Content-Type': 'application/json' });
      if (status === 'pending') {
        res.end(
          JSON.stringify({
            ok: true,
            count: 1,
            permissions: [
              {
                id: 'perm_pending_1',
                session_id: 'sess_test_1',
                run_id: 'run_1',
                tool_name: 'bash',
                requirement: 'bash',
                mode: 'auto',
                status: 'pending',
                created_at: 1000,
                updated_at: 1000,
              },
            ],
          })
        );
      } else {
        res.end(
          JSON.stringify({
            ok: true,
            count: 2,
            permissions: [
              {
                id: 'perm_pending_1',
                session_id: 'sess_test_1',
                run_id: 'run_1',
                tool_name: 'bash',
                status: 'pending',
                created_at: 1000,
                updated_at: 1000,
              },
              {
                id: 'perm_resolved_2',
                session_id: 'sess_test_1',
                run_id: 'run_1',
                tool_name: 'read',
                status: 'approved',
                created_at: 900,
                updated_at: 950,
              },
            ],
          })
        );
      }
    } else if (req.method === 'POST' && url.pathname === '/v3/sessions/sess_test_1/permissions/perm_pending_1/resolve') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        resolvedAction = body.action;
        resolvedReason = body.reason;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            session_id: 'sess_test_1',
            permission: {
              id: 'perm_pending_1',
              session_id: 'sess_test_1',
              run_id: 'run_1',
              tool_name: 'bash',
              status: 'approved',
              decision: body.action,
              reason: body.reason,
            },
          })
        );
      });
    } else if (req.method === 'POST' && url.pathname === '/v3/sessions/sess_test_1/permissions/resolve_all') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        resolvedAllAction = body.action;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            session_id: 'sess_test_1',
            count: 1,
            resolved: [
              {
                id: 'perm_pending_1',
                session_id: 'sess_test_1',
                tool_name: 'bash',
                status: 'approved',
              },
            ],
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const perms = new SwarmPermissionsNamespace(transport);

    const pending = await perms.listSessionPending('sess_test_1');
    assert.equal(pending.length, 1);
    assert.equal(pending[0].id, 'perm_pending_1');

    const all = await perms.listSessionAll('sess_test_1');
    assert.equal(all.length, 2);

    const resolveRes = await perms.resolve('sess_test_1', 'perm_pending_1', 'allow_once', { reason: 'User confirmed' });
    assert.equal(resolveRes.ok, true);
    assert.equal(resolvedAction, 'allow_once');
    assert.equal(resolvedReason, 'User confirmed');

    const resolveAllRes = await perms.resolveAll('sess_test_1', 'allow_once', { reason: 'Approve all' });
    assert.equal(resolveAllRes.ok, true);
    assert.equal(resolveAllRes.count, 1);
    assert.equal(resolvedAllAction, 'allow_once');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
