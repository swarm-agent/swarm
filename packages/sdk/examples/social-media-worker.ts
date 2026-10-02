/** Read-only consumer, NOT a worker executor or publication demo.
 * Deploy/approve the real worker in Swarm Orchestrate first. Call from a trusted
 * same-container BFF using the private socket; never expose this SDK to a browser.
 * No generated content, synthetic progress, storage uploads or implicit approval.
 */
import type { SwarmClient } from '../src/client.js';

export async function observeSocialMediaWorker(
  client: Pick<SwarmClient, 'workers' | 'deliverables'>,
  { workerId, runId, deliverableId }: { workerId: string; runId: string; deliverableId?: string },
) {
  if (!workerId.trim() || !runId.trim()) throw new Error('Exact worker and run IDs are required.');
  const worker = await client.workers.get(workerId);
  if (!worker || worker.id !== workerId) throw new Error('Worker not found.');
  const run = await client.workers.getRun({ worker_id: workerId, run_id: runId });
  if (!run || run.worker_id !== workerId || run.id !== runId) throw new Error('Worker run not found or mismatched.');
  const deliverable = deliverableId ? await client.deliverables.get(deliverableId) : undefined;
  if (deliverable && (deliverable.id !== deliverableId || deliverable.worker_id !== workerId ||
    !run.session_id || deliverable.session_id !== run.session_id)) {
    throw new Error('Deliverable does not belong to the selected worker run session.');
  }
  return {
    worker, run, deliverable,
    // Preserve the backend receipt verbatim; published status alone is not proof.
    publicationReceipt: deliverable?.action_result ?? null,
  };
}
