import { describe, test } from 'node:test';
import assert from 'node:assert/strict';
import { MemoryStorageDriver } from '../storage/adapters/memory.js';
import { WorkerStorageHub } from '../storage/hub.js';
import { WorkerActorRunner, calculateTokenCostUSD } from '../storage/runner.js';
import type { WorkerActorSpec } from '../storage/types.js';

describe('WorkerActorRunner & Granular Telemetry', () => {
  const workerId = 'test-social-actor';

  test('calculates token cost accurately with micro-dollar precision', () => {
    // 1000 prompt tokens, 500 candidate tokens, 200 thinking tokens on gemini flash
    // prompt: 1000/1M * 0.15 = 0.000150
    // output: 700/1M * 0.60 = 0.000420
    // total = 0.000570
    const cost = calculateTokenCostUSD('gemini-3.8-flash', 1000, 500, 200);
    assert.equal(cost, 0.00057);
  });

  test('halts execution when worker is pending_approval ($0 spend / zero unapproved execution)', async () => {
    const driver = new MemoryStorageDriver();
    const hub = new WorkerStorageHub({ workerId, driver });
    const spec: WorkerActorSpec = {
      id: workerId,
      name: 'Test Worker',
      version: '1.0.0',
      status: 'pending_approval',
      brain: {
        model: 'gemini-3.8-flash',
        instructions: 'Test instructions',
      },
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
    await hub.saveWorkerActor(spec);

    const runner = new WorkerActorRunner({ workerId, driver });
    const result = await runner.runJob();

    assert.equal(result.status, 'halted_unapproved');
    assert.equal(result.reason, 'pending_approval');
    assert.equal(result.deliverable, undefined);

    // Verify no jobs were logged to storage
    const logs = await hub.listJobExecutionLogs();
    assert.equal(logs.length, 0);
  });

  test('executes scheduled automation when status is active and logs durable telemetry to storage', async () => {
    const driver = new MemoryStorageDriver();
    const hub = new WorkerStorageHub({ workerId, driver });
    const spec: WorkerActorSpec = {
      id: workerId,
      name: 'Active Social Specialist',
      version: '1.0.0',
      status: 'active',
      brain: {
        model: 'gemini-3.8-flash',
        thinking_level: 'low',
        instructions: 'Monitor engineering feed and draft technical tweets.',
      },
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
    await hub.saveWorkerActor(spec);

    const runner = new WorkerActorRunner({ workerId, driver });
    const result = await runner.runJob({ type: 'automation' });

    assert.equal(result.status, 'success');
    assert.equal(result.type, 'automation');
    assert.ok(result.telemetry);
    assert.ok(result.telemetry.total_tokens > 0);
    assert.ok(result.telemetry.cost_usd > 0);
    assert.ok(result.deliverable);
    assert.ok(result.deliverable.sha256);

    // Verify durable execution log in bucket
    const jobLogs = await hub.listJobExecutionLogs();
    assert.equal(jobLogs.length, 1);
    assert.equal(jobLogs[0].jobId, result.jobId);
    assert.equal(jobLogs[0].status, 'success');
    assert.equal(jobLogs[0].telemetry.total_tokens, result.telemetry.total_tokens);
    assert.equal(jobLogs[0].telemetry.cost_usd, result.telemetry.cost_usd);
    assert.equal(jobLogs[0].deliverableId, result.deliverable.id);
  });

  test('executes a specific one-off task and updates task state in worker.json', async () => {
    const driver = new MemoryStorageDriver();
    const hub = new WorkerStorageHub({ workerId, driver });
    const spec: WorkerActorSpec = {
      id: workerId,
      name: 'Task Worker',
      version: '1.0.0',
      status: 'active',
      brain: {
        model: 'gemini-3.8-flash',
        instructions: 'Execute assigned tasks',
      },
      tasks: [
        {
          id: 'task-launch-thread',
          title: 'Draft launch thread for Cloud Workers',
          type: 'one_off',
          prompt: 'Write 4 tweets announcing Cloud Run Jobs for Swarm.',
          status: 'pending',
          createdAt: new Date().toISOString(),
        },
      ],
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    };
    await hub.saveWorkerActor(spec);

    const runner = new WorkerActorRunner({ workerId, driver });
    const result = await runner.runJob({ taskId: 'task-launch-thread' });

    assert.equal(result.status, 'success');
    assert.equal(result.taskId, 'task-launch-thread');
    assert.equal(result.type, 'one_off');

    // Check worker.json state in bucket
    const updatedSpec = await hub.getWorkerActor();
    assert.ok(updatedSpec);
    const task = updatedSpec!.tasks?.find((t) => t.id === 'task-launch-thread');
    assert.ok(task);
    assert.equal(task!.status, 'completed');
    assert.equal(task!.deliverableId, result.deliverable!.id);
    assert.ok(task!.telemetry);
    assert.ok(task!.telemetry!.cost_usd > 0);
  });
});
