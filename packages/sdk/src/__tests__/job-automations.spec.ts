import assert from 'node:assert/strict';
import { test } from 'node:test';
import { JOB_REVIEWER_FIX_MAX_LINES, SwarmJobAutomationsNamespace, type JobAutomationStep } from '../job-automations.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: the SDK reaches workflows only under /v3/workflows and
// /v3/job-deliveries (never the older /v3/automations routes), sends the
// definition as given, and encodes ids and filters. Boundary:
// SwarmJobAutomationsNamespace over a transport fixture; the daemon's
// validation, delivery and re-run rules are covered by the Go tests.
test('workflows use only the workflow routes', async () => {
  const calls: Array<[string, string, unknown]> = [];
  const transport = {
    request: async (path: string, options: { method?: string; body?: unknown } = {}) => {
      calls.push([options.method ?? 'GET', path, options.body]);
      return { data: { ok: true, automations: [], stopped: false, automation: { id: 'jauto_1' }, created: true, deliveries: [], delivery: { delivery_id: 'jdel_1' }, rerun: true, stop: { stopped: true } } };
    },
  } as unknown as SwarmTransport;
  const ns = new SwarmJobAutomationsNamespace(transport);

  const set = await ns.set({ name: 'push', scope: { folder: '/srv/site' }, steps: [{ type: 'run', command: 'deliver/push.sh' }] });
  assert.equal(set.created, true);
  await ns.list();
  await ns.delete('jauto/1');
  await ns.setStopped(true);
  await ns.deliveries({ jobId: 'worker/w1/r1', limit: 5 });
  await ns.delivery('jdel_1');
  const rerun = await ns.rerun('jdel_1');
  assert.equal(rerun.rerun, true);

  assert.deepEqual(calls, [
    ['POST', '/v3/workflows', { name: 'push', scope: { folder: '/srv/site' }, steps: [{ type: 'run', command: 'deliver/push.sh' }] }],
    ['GET', '/v3/workflows', undefined],
    ['DELETE', '/v3/workflows/jauto%2F1', undefined],
    ['POST', '/v3/workflows/stop', { stopped: true }],
    ['GET', '/v3/job-deliveries?job_id=worker%2Fw1%2Fr1&limit=5', undefined],
    ['GET', '/v3/job-deliveries/jdel_1', undefined],
    ['POST', '/v3/job-deliveries/jdel_1/rerun', {}],
  ]);
  for (const [, path] of calls) {
    assert.ok(!path.startsWith('/v3/automations'), `reached the older automations API: ${path}`);
  }
});

// Purpose: a review loop (agent step, reopen step with max_rounds, `if` on a
// verdict) is sent to the daemon exactly as written; the daemon owns its
// validation. Boundary: SwarmJobAutomationsNamespace.set over a transport
// fixture.
test('a review loop definition is sent as given', async () => {
  const bodies: unknown[] = [];
  const transport = {
    request: async (_path: string, options: { body?: unknown } = {}) => {
      bodies.push(options.body);
      return { data: { ok: true, automation: { id: 'jauto_2' }, created: true } };
    },
  } as unknown as SwarmTransport;
  const loop = {
    name: 'review loop',
    scope: { folder: '/srv/site', worker: 'coder' },
    steps: [
      { id: 'review', type: 'agent' as const, worker: 'reviewer', brief: 'Review this job.' },
      { id: 'fix', type: 'reopen' as const, from: 'review', message: 'The reviewer blocked it.', max_rounds: 2, if: [{ step: 'review', verdict: 'block' as const }] },
      { id: 'push', type: 'run' as const, command: 'deliver/push.sh', if: [{ step: 'review', verdict: 'ok' as const }] },
    ],
  };
  await new SwarmJobAutomationsNamespace(transport).set(loop);
  assert.deepEqual(bodies, [loop]);
});

// Purpose: an agent step's `fixes` (reviewer workers only) is typed as the
// single value `security` and sent to the daemon as written; the daemon owns
// the reviewer-only check and the 80-line limit (JOB_REVIEWER_FIX_MAX_LINES).
// Boundary: the JobAutomationStep type (checked by the SDK typecheck) and
// SwarmJobAutomationsNamespace.set over a transport fixture.
test('a reviewer step with fixes is typed and sent as given', async () => {
  const bodies: unknown[] = [];
  const transport = {
    request: async (_path: string, options: { body?: unknown } = {}) => {
      bodies.push(options.body);
      return { data: { ok: true, automation: { id: 'jauto_3' }, created: true } };
    },
  } as unknown as SwarmTransport;
  const review: JobAutomationStep = { id: 'review', type: 'agent', worker: 'reviewer', brief: 'Review this job.', fixes: 'security' };
  // @ts-expect-error fixes has exactly one value
  const typo: JobAutomationStep = { id: 'review', type: 'agent', worker: 'reviewer', brief: 'Review this job.', fixes: 'all' };
  assert.equal(typo.fixes, 'all');
  await new SwarmJobAutomationsNamespace(transport).set({ name: 'review fixes', scope: { folder: '/srv/site' }, steps: [review] });
  assert.deepEqual(bodies, [{ name: 'review fixes', scope: { folder: '/srv/site' }, steps: [review] }]);
  assert.equal(JOB_REVIEWER_FIX_MAX_LINES, 80);
});

// Purpose: an agent step's `checkout` is typed as same, new or none and sent
// as written; the daemon owns which agent may use which.
// Boundary: the JobAutomationStep type (checked by the SDK typecheck) and
// SwarmJobAutomationsNamespace.set over a transport fixture.
test('an agent step checkout is typed and sent as given', async () => {
  const bodies: unknown[] = [];
  const transport = {
    request: async (_path: string, options: { body?: unknown } = {}) => {
      bodies.push(options.body);
      return { data: { ok: true, automation: { id: 'jauto_4' }, created: true } };
    },
  } as unknown as SwarmTransport;
  const review: JobAutomationStep = { id: 'review', type: 'agent', worker: 'reviewer', brief: 'Review the coder\'s work.', checkout: 'same' };
  // @ts-expect-error checkout is same, new or none
  const typo: JobAutomationStep = { id: 'review', type: 'agent', worker: 'reviewer', brief: 'Review it.', checkout: 'shared' };
  assert.equal(typo.checkout, 'shared');
  await new SwarmJobAutomationsNamespace(transport).set({ name: 'review in place', scope: { folder: '/srv/site', role: 'coder' }, steps: [review] });
  assert.deepEqual(bodies, [{ name: 'review in place', scope: { folder: '/srv/site', role: 'coder' }, steps: [review] }]);
});
