import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createSwarmClient } from '../client.js';
import { createDeployManifest, generateOptimizedDockerfile, loadDeployManifest, serializeDeployManifest } from '../deploy/index.js';

// Purpose: the generator is the narrowest boundary that can prevent a second,
// incompatible daemon build or Dockerfile injection; a valid pin is not release proof.
test('canonical image only; rejects mutable images and legacy build overrides', () => {
  const image = `ghcr.io/swarm-agent/swarm-headless@sha256:${'a'.repeat(64)}`;
  assert.equal(generateOptimizedDockerfile({ runtimeImage: image }).split('\n')[1], `FROM ${image}`);
  for (const runtimeImage of [undefined, 'swarm:latest', image + '\nRUN false', image.replace('swarm-agent', 'other')]) {
    assert.throws(() => generateOptimizedDockerfile({ runtimeImage }), /runtimeImage/);
  }
  for (const override of [{ port: 18080 }, { goVersion: '1.24' }, { source: { type: 'github' as const } }]) {
    assert.throws(() => generateOptimizedDockerfile({ runtimeImage: image, ...override }), /unsupported/);
  }
});

// Purpose: all public recipe entrypoints must reject retired deployment paths,
// including serialized plans. Namespace-level tests prove no artifact writes occur.
test('unsupported providers fail before writing or executing even a saved plan', async () => {
  const client = createSwarmClient();
  const dir = await mkdtemp(join(tmpdir(), 'sdk-deploy-'));
  try {
    for (const target of ['gcp-cloud-run', 'gcp-compute-engine'] as const) {
      const manifest = createDeployManifest({ target });
      assert.deepEqual(loadDeployManifest(serializeDeployManifest(manifest), { PROJECT_ID: '${PROJECT_ID}' }), manifest);
      assert.equal(client.deploy.validate(manifest).valid, false);
      for (const method of ['plan', 'generateArtifacts', 'generateCommands'] as const) {
        assert.throws(() => client.deploy[method](manifest), /unsupported/);
      }
      const plan = { manifest, target, summary: '', artifacts: { 'must-not-exist': 'bad' }, commands: [], recommendations: [], warnings: [] };
      await assert.rejects(client.deploy.writeArtifacts(dir, plan), /unsupported/);
      await assert.rejects(client.deploy.execute(plan, { outputDir: dir, dryRun: true }), /unsupported/);
      assert.deepEqual(await readdir(dir), []);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});
