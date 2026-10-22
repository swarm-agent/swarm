// Purpose: the setup app starts when its unit names it through a symlink, as a
// fresh install does (/opt/swarm/current), not only through its real path.
// Before this, Node's main-module check failed silently and the app exited 0.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, symlinkSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { isMainModule } from '../server.mjs';

test('the setup app recognizes itself through a symlinked release', () => {
  const real = fileURLToPath(new URL('../server.mjs', import.meta.url));
  const dir = mkdtempSync(join(process.env.TMPDIR || tmpdir(), 'main-module-'));
  try {
    const link = join(dir, 'current');
    symlinkSync(dirname(real), link);
    const url = pathToFileURL(real).href;
    assert.equal(isMainModule(real, url), true);
    assert.equal(isMainModule(join(link, 'server.mjs'), url), true);
    assert.equal(isMainModule(join(dir, 'nothing.mjs'), url), false);
    assert.equal(isMainModule(undefined, url), false);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
