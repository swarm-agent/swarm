import assert from 'node:assert/strict';
import { test } from 'node:test';
import { WorkerCloudDeployer } from '../deploy/worker-cloud-deployer.js';
import type { WorkerActorSpec } from '../storage/types.js';
import { WorkerStorageHub } from '../storage/hub.js';
import { MemoryStorageDriver } from '../storage/adapters/memory.js';

// Purpose: legacy Cloud Run actors must not bypass canonical Orchestrator approval.
// The public helper boundary proves refusal with no bucket writes or generated commands.
test('cloud worker helpers reject without side effects', async () => {
  const spec = {} as WorkerActorSpec;
  const driver = new MemoryStorageDriver();
  const hub = new WorkerStorageHub({ workerId: 'example', driver });
  let writes = 0;
  hub.saveWorkerActor = async () => { writes++; };
  assert.equal(WorkerCloudDeployer.validate(spec).valid, false);
  assert.throws(() => WorkerCloudDeployer.createDefaultSpec({ id: 'example', name: 'Example', instructions: 'Example' }), /Orchestrator/);
  assert.throws(() => WorkerCloudDeployer.generateDeployCommands(spec, 'bucket'), /unsupported/);
  assert.throws(() => WorkerCloudDeployer.generateTaskExecuteCommand(spec, 'task'), /unsupported/);
  await assert.rejects(WorkerCloudDeployer.uploadToBucket(spec, hub), /unsupported/);
  assert.equal(writes, 0);
});
