import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  MemoryStorageDriver,
  S3StorageDriver,
  WorkerStorageHub,
} from '../storage/index.js';

test('MemoryStorageDriver: basic operations', async () => {
  const driver = new MemoryStorageDriver();

  assert.equal(await driver.exists('test/foo.txt'), false);
  await driver.write('test/foo.txt', 'hello world');
  assert.equal(await driver.exists('test/foo.txt'), true);

  const text = await driver.readString('test/foo.txt');
  assert.equal(text, 'hello world');

  await driver.write('test/bar.json', JSON.stringify({ a: 1 }));
  await driver.write('other/file.txt', 'other');

  const list = await driver.list('test/');
  assert.deepEqual(list, ['test/bar.json', 'test/foo.txt']);

  await driver.delete('test/foo.txt');
  assert.equal(await driver.exists('test/foo.txt'), false);
});

test('WorkerStorageHub: full lifecycle with base context, session traces and deliverables', async () => {
  const driver = new MemoryStorageDriver();
  const hub = new WorkerStorageHub({
    workerId: 'worker-auditor-1',
    driver,
  });

  // 1. Init worker manifest
  const manifest = await hub.initWorker({
    name: 'Code Auditor Agent',
    description: 'Audits PRs and executes benchmarks',
  });
  assert.equal(manifest.id, 'worker-auditor-1');
  assert.equal(manifest.name, 'Code Auditor Agent');
  assert.ok(manifest.createdAt);

  const readManifest = await hub.getWorkerManifest();
  assert.equal(readManifest?.name, 'Code Auditor Agent');

  // 2. Base context (source of truth)
  await hub.saveBaseContext({
    instructions: '# PR Review Guidelines\nCheck for security vulnerabilities.',
    tools: [{ name: 'git_diff', description: 'Inspects diffs' }],
    memory: { preferredLanguage: 'typescript' },
  });

  const baseContext = await hub.loadBaseContext();
  assert.ok(baseContext.instructions?.includes('PR Review Guidelines'));
  assert.equal(baseContext.tools?.[0].name, 'git_diff');
  assert.equal(baseContext.memory?.preferredLanguage, 'typescript');

  // 3. Start Session
  const session = await hub.startSession('sess-100', { prompt: 'Audit commit 89f2a' });
  assert.equal(session.sessionId, 'sess-100');
  assert.equal(session.status, 'starting');
  assert.equal(session.progress, 0);

  // 4. Update progress & traces
  await hub.appendTrace('sess-100', 'Analyzing AST trees');
  await hub.appendTrace('sess-100', 'Running vulnerability checks', { checksRun: 15 });

  const updatedSession = await hub.updateSessionState('sess-100', {
    status: 'running',
    progress: 50,
    step: 'Running AST inspection',
  });
  assert.equal(updatedSession.status, 'running');
  assert.equal(updatedSession.progress, 50);

  // 5. Publish Deliverable
  const deliverable = await hub.publishDeliverable({
    sessionId: 'sess-100',
    title: 'Security Audit & Remediation Patch',
    summary: 'Found 1 vulnerability and generated patch',
    files: [
      {
        name: 'patch.diff',
        content: '--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n- unsafe\n+ safe',
        contentType: 'text/x-diff',
      },
    ],
    actions: [
      {
        id: 'apply_patch',
        label: 'Apply Patch to Worktree',
        action_type: 'post',
        endpoint: '/v3/deliverables/deliv-patch/apply',
        variant: 'primary',
      },
    ],
  });

  assert.equal(deliverable.title, 'Security Audit & Remediation Patch');
  assert.equal(deliverable.status, 'pending_review');
  assert.equal(deliverable.files.length, 1);
  assert.equal(deliverable.files[0].name, 'patch.diff');
  assert.ok(deliverable.files[0].sha256.length === 64);
  assert.ok(deliverable.sha256 && deliverable.sha256.length === 64);

  // Deliverable file should be written in storage driver
  const savedFile = await driver.readString(deliverable.files[0].path);
  assert.ok(savedFile?.includes('+ safe'));

  // Session state should automatically be completed
  const finalSessionStateRaw = await driver.readString('workers/worker-auditor-1/sessions/sess-100/state.json');
  assert.ok(finalSessionStateRaw);
  const finalSessionState = JSON.parse(finalSessionStateRaw);
  assert.equal(finalSessionState.status, 'completed');
  assert.equal(finalSessionState.progress, 100);

  // List deliverables
  const deliverables = await hub.listDeliverables();
  assert.equal(deliverables.length, 1);
  assert.equal(deliverables[0].id, deliverable.id);
});

test('S3StorageDriver: URL calculation and credential signing', async () => {
  const driver = new S3StorageDriver({
    bucket: 'test-bucket',
    region: 'us-west-2',
    accessKeyId: 'AKIAEXAMPLE',
    secretAccessKey: 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY',
  });

  assert.ok(driver);
});

test('SwarmStorageNamespace: API client methods', async () => {
  const mockCalls: Array<{ path: string; method?: string; body?: any }> = [];

  const fakeFetch = async (url: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const urlStr = url.toString();
    const pathname = new URL(urlStr).pathname;
    const method = init?.method || 'GET';
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    mockCalls.push({ path: pathname, method, body });

    if (pathname === '/v1/storage/buckets' && method === 'GET') {
      return new Response(JSON.stringify({ buckets: [{ id: 'bkt_1', name: 'Main S3' }] }), { status: 200 });
    }
    if (pathname === '/v1/storage/buckets' && method === 'POST') {
      return new Response(JSON.stringify({ bucket: { id: 'bkt_new', ...body } }), { status: 201 });
    }
    if (pathname === '/v1/storage/buckets/bkt_1' && method === 'DELETE') {
      return new Response(JSON.stringify({ deleted: true }), { status: 200 });
    }
    if (pathname === '/v1/storage/buckets/bkt_1/scan' && method === 'POST') {
      return new Response(JSON.stringify({ scan_summary: { bucket_id: 'bkt_1', workers_found: 2, deliverables_new: 1, sessions_found: 3, scanned_at: Date.now() } }), { status: 200 });
    }
    if (pathname === '/v1/storage/workers' && method === 'GET') {
      return new Response(JSON.stringify({ workers: [{ worker_id: 'w1', name: 'Tester', imported: false }] }), { status: 200 });
    }
    if (pathname === '/v1/storage/workers/w1/import' && method === 'POST') {
      return new Response(JSON.stringify({ worker: { worker_id: 'w1', name: 'Tester', imported: true } }), { status: 200 });
    }
    if (pathname === '/v1/storage/deliverables' && method === 'GET') {
      return new Response(JSON.stringify({ deliverables: [{ deliverable_id: 'deliv_1', title: 'Audit' }] }), { status: 200 });
    }
    if (pathname === '/v1/storage/deliverables/deliv_1/import' && method === 'POST') {
      return new Response(JSON.stringify({ deliverable: { deliverable_id: 'deliv_1', status: 'accepted' }, target_dir: '/workspace' }), { status: 200 });
    }
    if (pathname === '/v1/storage/proposals' && method === 'POST') {
      return new Response(JSON.stringify({ proposal: { id: 'bkt_prop_1', status: 'pending_approval', ...body } }), { status: 201 });
    }
    if (pathname === '/v1/storage/proposals' && method === 'GET') {
      return new Response(JSON.stringify({ proposals: [{ id: 'bkt_prop_1', status: 'pending_approval', bucket_name: 'prop-bucket' }], count: 1 }), { status: 200 });
    }
    if (pathname === '/v1/storage/buckets/bkt_prop_1/accept-canonical' && method === 'POST') {
      return new Response(JSON.stringify({ bucket: { id: 'bkt_prop_1', canonical: true, status: 'active' } }), { status: 200 });
    }
    if (pathname === '/v1/storage/canonical' && method === 'GET') {
      return new Response(JSON.stringify({ bucket: { id: 'bkt_prop_1', canonical: true, status: 'active' }, configured: true }), { status: 200 });
    }
    if (pathname === '/v1/storage/buckets/bkt_prop_1/reject' && method === 'POST') {
      return new Response(JSON.stringify({ rejected: true }), { status: 200 });
    }

    return new Response('Not Found', { status: 404 });
  };

  const originalFetch = globalThis.fetch;
  globalThis.fetch = fakeFetch;

  try {
    const { SwarmClient } = await import('../client.js');
    const client = new SwarmClient({ baseUrl: 'http://127.0.0.1:18080' });

    // 1. List Buckets
    const buckets = await client.storage.listBuckets();
    assert.equal(buckets.length, 1);
    assert.equal(buckets[0].id, 'bkt_1');

    // 2. Register Bucket
    const newBucket = await client.storage.registerBucket({ name: 'Backup Hub', bucket_name: 'swarm-backups' });
    assert.equal(newBucket.id, 'bkt_new');
    assert.equal(newBucket.name, 'Backup Hub');

    // 3. Scan Bucket
    const scan = await client.storage.scanBucket('bkt_1');
    assert.equal(scan.workers_found, 2);
    assert.equal(scan.deliverables_new, 1);

    // 4. List Workers & Import
    const workers = await client.storage.listWorkers();
    assert.equal(workers.length, 1);
    const importedWorker = await client.storage.importWorker('w1');
    assert.equal(importedWorker.imported, true);

    // 5. List Deliverables & Import
    const deliverables = await client.storage.listDeliverables();
    assert.equal(deliverables.length, 1);
    const importRes = await client.storage.importDeliverable('deliv_1', '/workspace');
    assert.equal(importRes.deliverable.status, 'accepted');
    assert.equal(importRes.targetDir, '/workspace');

    // 6. Delete Bucket
    const deleted = await client.storage.deleteBucket('bkt_1');
    assert.equal(deleted, true);

    // 7. Propose Cloud Connection via AI worker
    const prop = await client.cloud.proposeConnection({
      provider: 'gcs',
      bucket_name: 'prop-bucket',
      proposed_by: 'social-worker',
      proposal_reason: 'Campaign assets',
    });
    assert.equal(prop.id, 'bkt_prop_1');
    assert.equal(prop.status, 'pending_approval');

    // 8. List Proposals
    const proposals = await client.storage.listProposals();
    assert.equal(proposals.length, 1);
    assert.equal(proposals[0].id, 'bkt_prop_1');

    // 9. Accept as Canonical
    const accepted = await client.storage.setCanonicalBucket('bkt_prop_1');
    assert.equal(accepted.canonical, true);
    assert.equal(accepted.status, 'active');

    // 10. Query Canonical
    const canonical = await client.cloud.getCanonicalBucket();
    assert.ok(canonical);
    assert.equal(canonical?.id, 'bkt_prop_1');
    assert.equal(canonical?.canonical, true);

    // 11. Reject Proposal
    const rejected = await client.storage.rejectProposal('bkt_prop_1');
    assert.equal(rejected, true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
