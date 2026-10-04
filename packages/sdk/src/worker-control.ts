import type { SwarmTransport } from './transport.js';
import type { WorkerRunRecord } from './types.js';
import { SwarmValidationError } from './errors.js';

export interface WorkerSSHRegistration {
  workspace_id: string; name: string; host: string; user: string; port: number; idempotency_key: string;
}
export interface WorkerTargetReference {
  kind: 'ssh' | 'gcp'; workspace_id?: string; reference_id: string;
  reference_digest?: string; capacity: number;
}
export interface WorkerContextUpdate {
  expected_revision: number; text: string; provenance: string;
  references?: { kind: 'artifact' | 'checkpoint'; reference: string }[];
}
export interface WorkerContextRecord {
  worker_id: string; revision: number; text: string; provenance: string;
  references?: WorkerContextUpdate['references']; published_by: string; published_at: number;
}
export interface WorkerDeploymentRequest {
  worker_revision: number; context_revision: number; target: WorkerTargetReference;
  lifecycle: 'persistent' | 'on_demand'; idempotency_key: string;
}
export interface WorkerDeploymentRecord {
  id: string; account_scope_id: string; worker_id: string; worker_revision: number;
  context_revision: number; target: WorkerTargetReference; lifecycle: 'persistent' | 'on_demand';
  revision: number; generation: number; desired_state: 'stopped' | 'running'; observed_state: 'unavailable';
  approval_state: 'pending' | 'approved'; approval_digest: string; approved_by?: string;
  cleanup_scope: 'owned_runtime'; cleanup_state: 'not_allocated'; active_job_id?: string;
  created_at: number; updated_at: number;
}
export interface WorkerRunPlacement {
  deployment_id: string; deployment_revision: number; target: WorkerTargetReference;
  context_revision: number; generation: number; attempt_id: string; attempt_number: number;
  state: 'pending_adapter' | 'cancelled_before_dispatch';
}
export interface WorkerCommandRequest {
  expected_revision: number; generation: number; kind: 'stop' | 'start'; idempotency_key: string;
}
export interface WorkerCommandRecord {
  id: string; deployment_id: string; generation: number; kind: 'stop'; status: 'pending';
  requested_by: string; created_at: number;
}
export interface QueueWorkerDeploymentJob {
  worker_revision: number; deployment_revision: number; context_revision: number;
  idempotency_key: string; input: { prompt: string; [key: string]: unknown };
}
const id = (value: string): string => {
  if (typeof value !== 'string' || !/^[a-zA-Z0-9_.-]{3,128}$/.test(value)) throw new SwarmValidationError('Invalid worker control identity');
  return encodeURIComponent(value);
};
const revision = (value: number, zero = false): void => {
  if (!Number.isSafeInteger(value) || value < (zero ? 0 : 1)) throw new SwarmValidationError('Exact revision is required');
};
const key = (value: string): void => {
  if (typeof value !== 'string' || !value.trim() || value !== value.trim() || value.length > 256 || /[/\\\r\n\0]/.test(value)) throw new SwarmValidationError('Stable idempotency key is required');
};
/** Durable configuration/queued intent only. No SSH, cloud credentials or remote executor. */
export class SwarmWorkerControlNamespace {
  constructor(private transport: SwarmTransport) {}
  private path(worker: string, tail: string): string { return `/v3/workers/${id(worker)}/${tail}`; }
  private async call<T>(path: string, method: string, field: string, body?: unknown, array = false): Promise<T> {
    const result = await this.transport.request<Record<string, unknown>>(path, { method, ...(body === undefined ? {} : { body }) });
    const value = result.data?.[field];
    if (array ? !Array.isArray(value) : !value || typeof value !== 'object' || Array.isArray(value)) throw new SwarmValidationError(`Malformed worker control envelope: ${field}`);
    return value as T;
  }
  registerSSHTarget(worker: string, request: WorkerSSHRegistration): Promise<WorkerTargetReference> {
    key(request.idempotency_key);
    return this.call(this.path(worker, 'ssh-targets'), 'POST', 'target', request);
  }
  resolveTarget(worker: string, target: WorkerTargetReference): Promise<WorkerTargetReference> {
    if (!['ssh', 'gcp'].includes(target.kind) || !Number.isInteger(target.capacity) || target.capacity < 1 || target.capacity > 100) throw new SwarmValidationError('Invalid target capability');
    return this.call(this.path(worker, 'target-reference'), 'POST', 'target', target);
  }
  getContext(worker: string, atRevision?: number): Promise<WorkerContextRecord> {
    if (atRevision !== undefined) revision(atRevision);
    return this.call(this.path(worker, `context${atRevision === undefined ? '' : `?revision=${atRevision}`}`), 'GET', 'context');
  }
  updateContext(worker: string, request: WorkerContextUpdate): Promise<WorkerContextRecord> {
    revision(request.expected_revision, true);
    if (!request.provenance.trim()) throw new SwarmValidationError('Context provenance is required');
    return this.call(this.path(worker, 'context'), 'PUT', 'context', request);
  }
  proposeDeployment(worker: string, request: WorkerDeploymentRequest): Promise<WorkerDeploymentRecord> {
    revision(request.worker_revision); revision(request.context_revision, true); key(request.idempotency_key);
    if (!['persistent', 'on_demand'].includes(request.lifecycle)) throw new SwarmValidationError('Explicit lifecycle is required');
    return this.call(this.path(worker, 'deployments'), 'POST', 'deployment', request);
  }
  listDeployments(worker: string): Promise<WorkerDeploymentRecord[]> { return this.call(this.path(worker, 'deployments'), 'GET', 'deployments', undefined, true); }
  getDeployment(worker: string, deployment: string): Promise<WorkerDeploymentRecord> { return this.call(this.path(worker, `deployments/${id(deployment)}`), 'GET', 'deployment'); }
  /** Explicit user approval of the exact digest; does not start a runtime. */
  approveDeployment(worker: string, deployment: string, expectedRevision: number, approvalDigest: string): Promise<WorkerDeploymentRecord> {
    revision(expectedRevision); if (!approvalDigest.trim()) throw new SwarmValidationError('Approval digest required');
    return this.call(this.path(worker, `deployments/${id(deployment)}/approve`), 'POST', 'deployment', { expected_revision: expectedRevision, approval_digest: approvalDigest });
  }
  queueJob(worker: string, deployment: string, request: QueueWorkerDeploymentJob): Promise<WorkerRunRecord> {
    revision(request.worker_revision); revision(request.deployment_revision); revision(request.context_revision, true); key(request.idempotency_key);
    return this.call(this.path(worker, `deployments/${id(deployment)}/jobs`), 'POST', 'run', request);
  }
  /** Start rejects with service unavailable. Stop is durable pending intent, not acknowledgement. */
  command(worker: string, deployment: string, request: WorkerCommandRequest): Promise<WorkerCommandRecord> {
    revision(request.expected_revision); revision(request.generation); key(request.idempotency_key);
    return this.call(this.path(worker, `deployments/${id(deployment)}/commands`), 'POST', 'command', request);
  }
  commands(worker: string, deployment: string): Promise<WorkerCommandRecord[]> { return this.call(this.path(worker, `deployments/${id(deployment)}/commands`), 'GET', 'commands', undefined, true); }
}
