import type { SwarmClient, WorkerSSHRegistration } from '../src/index.js';

/** Configuration only. Supply an already authenticated private SDK client and
 * an existing worker. No browser, SSH command, provider key or cloud helper. */
export async function proposeHeadlessPlacements(
  client: SwarmClient,
  workerId: string,
  targets: WorkerSSHRegistration[],
) {
  if (targets.length < 1 || targets.length > 10) throw new Error('Supply 1–10 explicit SSH targets');
  const worker = await client.workers.get(workerId);
  if (!worker) throw new Error('Worker not found');
  const context = await client.workers.control.getContext(worker.id);
  const proposals = [];
  for (const registration of targets) {
    const target = await client.workers.control.registerSSHTarget(worker.id, registration);
    proposals.push(await client.workers.control.proposeDeployment(worker.id, {
      worker_revision: worker.revision,
      context_revision: context.revision,
      target,
      lifecycle: 'on_demand',
      idempotency_key: `placement-${registration.idempotency_key}`,
    }));
  }
  // Present exact revisions/digests to the authorized human. Do not automatically
  // approve them or report online: every observed_state is currently unavailable.
  return proposals;
}
