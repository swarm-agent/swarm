import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { MemoryStorageDriver } from '../storage/adapters/memory.js';
import { WorkerStorageHub } from '../storage/hub.js';
import { WorkerCloudDeployer } from '../deploy/worker-cloud-deployer.js';
import type { WorkerActorSpec } from '../storage/types.js';

describe('WorkerCloudDeployer', () => {
  test('validates worker spec and rejects invalid IDs or empty instructions', () => {
    const invalidSpec: any = {
      id: 'Invalid_Id_With_Uppercase',
      name: 'Test',
      brain: { model: 'gemini-3.8-flash', instructions: '' },
    };
    const res = WorkerCloudDeployer.validate(invalidSpec);
    assert.equal(res.valid, false);
    assert.ok(res.errors.some((e) => e.includes('Invalid worker id')));
    assert.ok(res.errors.some((e) => e.includes('instructions cannot be empty')));
  });

  test('creates default spec with cloud configuration and status pending_approval', () => {
    const spec = WorkerCloudDeployer.createDefaultSpec({
      id: 'social-specialist',
      name: 'Social Media Specialist',
      instructions: 'Draft high-impact technical tweets.',
      gcpProject: 'swarm-social-20260926',
      cronSchedule: '0 14 * * 1-5',
    });

    assert.equal(spec.id, 'social-specialist');
    assert.equal(spec.status, 'pending_approval');
    assert.equal(spec.cloud?.provider, 'gcp');
    assert.equal(spec.cloud?.runtime, 'cloud-run-job');
    assert.equal(spec.cloud?.project, 'swarm-social-20260926');
    assert.equal(spec.schedule?.kind, 'cron');
    assert.equal(spec.schedule?.cron, '0 14 * * 1-5');

    const validation = WorkerCloudDeployer.validate(spec);
    assert.equal(validation.valid, true);
  });

  test('uploads validated worker.json to bucket storage hub', async () => {
    const driver = new MemoryStorageDriver();
    const hub = new WorkerStorageHub({ workerId: 'social-specialist', driver });
    const spec = WorkerCloudDeployer.createDefaultSpec({
      id: 'social-specialist',
      name: 'Social Media Specialist',
      instructions: 'Draft tweets.',
    });

    await WorkerCloudDeployer.uploadToBucket(spec, hub);

    const saved = await hub.getWorkerActor();
    assert.ok(saved);
    assert.equal(saved.id, 'social-specialist');
    assert.equal(saved.status, 'pending_approval');
  });

  test('generates valid Cloud Run Job and Cloud Scheduler deployment commands', () => {
    const spec = WorkerCloudDeployer.createDefaultSpec({
      id: 'social-specialist',
      name: 'Social Media Specialist',
      instructions: 'Draft tweets.',
      gcpProject: 'swarm-social-20260926',
      cronSchedule: '0 14 * * 1-5',
    });

    const cmds = WorkerCloudDeployer.generateDeployCommands(spec, 'swarm-social-20260926-hub');
    assert.ok(cmds.jobDeployCommand.includes('gcloud run jobs deploy worker-social-specialist'));
    assert.ok(cmds.jobDeployCommand.includes('--project=swarm-social-20260926'));
    assert.ok(cmds.jobDeployCommand.includes('--set-env-vars=GCS_BUCKET=swarm-social-20260926-hub,WORKER_ID=social-specialist,SWARM_DISABLE_MINT_REPORT=1'));
    assert.ok(cmds.jobDeployCommand.includes('--max-retries=0'));

    assert.ok(cmds.schedulerCommand);
    assert.ok(cmds.schedulerCommand.includes('gcloud scheduler jobs create http worker-social-specialist-cron'));
    assert.ok(cmds.schedulerCommand.includes('--schedule=0 14 * * 1-5'));
  });

  test('generates task execution command for one-off tasks', () => {
    const spec = WorkerCloudDeployer.createDefaultSpec({
      id: 'social-specialist',
      name: 'Social Media Specialist',
      instructions: 'Draft tweets.',
      gcpProject: 'swarm-social-20260926',
    });

    const execCmd = WorkerCloudDeployer.generateTaskExecuteCommand(spec, 'task-init-01');
    assert.equal(
      execCmd.executeCommand,
      'gcloud run jobs execute worker-social-specialist --project=swarm-social-20260926 --region=us-central1 --args=--task-id=task-init-01'
    );
  });
});
