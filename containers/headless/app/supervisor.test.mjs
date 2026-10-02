import assert from 'node:assert/strict';
import { test } from 'node:test';
import { EventEmitter } from 'node:events';
import { supervise } from './supervisor.mjs';

const idle = [process.execPath, '-e', 'setInterval(() => {}, 1000)'];
const exit = code => [process.execPath, '-e', `process.exit(${code})`];
// Purpose: supervisor lifecycle only (not an AI workload). Real bounded child
// processes exercise readiness gating, child failure and TERM/INT teardown.
// Injected readiness is not evidence of a running/healthy daemon.
test('app is not started before readiness; startup deadline tears down daemon', { timeout: 4000 }, async () => {
  assert.equal(await supervise({ daemon: idle, app: exit(42), probe: async () => false, startupMs: 50, graceMs: 100 }), 1);
});
test('app failure and clean daemon exit are container failures', { timeout: 4000 }, async () => {
  assert.equal(await supervise({ daemon: idle, app: exit(42), probe: async () => true, graceMs: 100 }), 42);
  assert.equal(await supervise({ daemon: exit(0), app: idle, probe: async () => false, graceMs: 100 }), 1);
});
test('missing executable fails and cleans up sibling', { timeout: 4000 }, async () => {
  assert.equal(await supervise({ daemon: idle, app: ['/nonexistent-swarm-test-command'], probe: async () => true, graceMs: 100 }), 1);
});
for (const [signal, code] of [['SIGTERM', 143], ['SIGINT', 130]]) {
  test(`${signal} is forwarded with bounded shutdown`, { timeout: 4000 }, async () => {
    const signals = new EventEmitter();
    const result = supervise({ daemon: idle, app: idle, probe: async () => { signals.emit(signal); return true; }, graceMs: 100, signals });
    assert.equal(await result, code);
    assert.equal(signals.listenerCount(signal), 0);
  });
}

// Purpose: a child that handles TERM but refuses to exit must not hang the
// supervisor. Child readiness is synchronized over a temporary file, not timing.
test('TERM-resistant child is killed after grace deadline', { timeout: 4000 }, async () => {
  const { mkdtemp, access, rm } = await import('node:fs/promises');
  const { tmpdir } = await import('node:os');
  const { join } = await import('node:path');
  const dir = await mkdtemp(join(tmpdir(), 'supervisor-'));
  const marker = join(dir, 'ready');
  try {
    const daemon = [process.execPath, '-e', `process.on('SIGTERM', () => {}); require('node:fs').writeFileSync(process.argv[1], 'ready'); setInterval(() => {}, 1000)`, marker];
    assert.equal(await supervise({ daemon, app: exit(42), probe: async () => { try { await access(marker); return true; } catch { return false; } }, graceMs: 50, startupMs: 2000 }), 42);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
