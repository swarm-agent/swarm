import type { SwarmTransport } from './transport.js';
import type {
  WorkerRecord,
  PortableWorkerDefinition,
  CreateWorkerParams,
  UpdateWorkerParams,
  ListWorkersParams,
  ListWorkersResult,
  AttachWorkerAutomationParams,
  UpdateWorkerAutomationParams,
  RemoveWorkerAutomationParams,
  WorkerAutomationDefinition,
  WorkerValidateResult,
  WorkerExportResult,
} from './types.js';
import { SwarmValidationError } from './errors.js';

/**
 * SwarmWorkersNamespace manages durable, account-owned workers, their revisions,
 * attached automations, and portable import/export definitions over canonical /v3/workers APIs.
 *
 * Distinct from legacy SwarmAutomationsNamespace which targets /v3/automations/v2.
 */
export class SwarmWorkersNamespace {
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  /**
   * Creates a new durable worker in idle state.
   */
  async create(params: CreateWorkerParams): Promise<WorkerRecord> {
    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>('/v3/workers', {
      method: 'POST',
      body: {
        id: params.id,
        name: params.name,
        description: params.description,
        instructions: params.instructions,
        requested_capabilities: params.requested_capabilities,
        workspace_requirements: params.workspace_requirements,
        local_bindings: params.local_bindings,
        automations: params.automations,
        metadata: params.metadata,
        idempotency_key: params.idempotency_key,
      },
    });
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }

  /**
   * Retrieves a durable worker by its server ID or attached automation ID.
   */
  async get(id: string): Promise<WorkerRecord | null> {
    if (!id || typeof id !== 'string') {
      throw new SwarmValidationError('Worker ID is required');
    }
    try {
      const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>(
        `/v3/workers/${encodeURIComponent(id)}`,
        { method: 'GET' }
      );
      return res.data.worker ?? (res.data as unknown as WorkerRecord);
    } catch (err: any) {
      if (err?.status === 404) {
        return null;
      }
      throw err;
    }
  }

  /**
   * Lists durable workers with pagination, lifecycle filters, and cursor support.
   */
  async list(params?: ListWorkersParams): Promise<ListWorkersResult> {
    const q = new URLSearchParams();
    if (params?.limit) q.set('limit', params.limit.toString());
    if (params?.after) q.set('after', params.after);
    if (params?.lifecycle_state) q.set('lifecycle_state', params.lifecycle_state);
    if (params?.include_deleted !== undefined) q.set('include_deleted', params.include_deleted ? 'true' : 'false');

    const queryStr = q.toString();
    const endpoint = queryStr ? `/v3/workers?${queryStr}` : '/v3/workers';

    const res = await this.transport.request<ListWorkersResult>(endpoint, {
      method: 'GET',
    });
    return {
      workers: res.data.workers ?? [],
      next_cursor: res.data.next_cursor,
      total_count: res.data.total_count ?? 0,
    };
  }

  /**
   * Updates a worker's definition with an explicit expected revision for optimistic concurrency.
   */
  async update(id: string, expectedRevision: number, params: UpdateWorkerParams): Promise<WorkerRecord> {
    if (!id || typeof id !== 'string') {
      throw new SwarmValidationError('Worker ID is required');
    }
    if (typeof expectedRevision !== 'number' || isNaN(expectedRevision) || expectedRevision < 1) {
      throw new SwarmValidationError('Explicit numeric expectedRevision >= 1 is required');
    }

    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(id)}`,
      {
        method: 'PUT',
        body: {
          expected_revision: expectedRevision,
          name: params.name,
          description: params.description,
          instructions: params.instructions,
          requested_capabilities: params.requested_capabilities,
          workspace_requirements: params.workspace_requirements,
          local_bindings: params.local_bindings,
          automations: params.automations,
          metadata: params.metadata,
          change_summary: params.change_summary,
        },
      }
    );
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }

  /**
   * Validates a portable worker definition document without persisting it.
   */
  async validate(definition: PortableWorkerDefinition | string): Promise<WorkerValidateResult> {
    let payload: unknown;
    if (typeof definition === 'string') {
      try {
        payload = JSON.parse(definition);
      } catch (err: any) {
        throw new SwarmValidationError(`Invalid JSON in portable worker definition: ${err.message}`);
      }
    } else {
      payload = definition;
    }

    const res = await this.transport.request<WorkerValidateResult>('/v3/workers/validate', {
      method: 'POST',
      body: payload,
    });
    return res.data;
  }

  /**
   * Exports a complete portable worker definition as indented JSON.
   */
  async export(id: string): Promise<WorkerExportResult> {
    if (!id || typeof id !== 'string') {
      throw new SwarmValidationError('Worker ID is required');
    }
    const res = await this.transport.request<WorkerExportResult>(
      `/v3/workers/${encodeURIComponent(id)}/export`,
      { method: 'GET' }
    );
    return res.data;
  }

  /**
   * Imports a portable worker definition.
   * If targetWorkerId is omitted, imports as a new idle worker with a freshly generated identity.
   * If targetWorkerId is provided, requires expectedRevision and strictly updates that existing worker.
   */
  async import(
    definition: PortableWorkerDefinition | string,
    options?: { targetWorkerId?: string; expectedRevision?: number }
  ): Promise<WorkerRecord> {
    let payload: unknown;
    if (typeof definition === 'string') {
      try {
        payload = JSON.parse(definition);
      } catch (err: any) {
        throw new SwarmValidationError(`Invalid JSON in portable worker definition: ${err.message}`);
      }
    } else {
      payload = definition;
    }

    if (options?.targetWorkerId) {
      if (typeof options.expectedRevision !== 'number' || isNaN(options.expectedRevision) || options.expectedRevision < 1) {
        throw new SwarmValidationError('Explicit expectedRevision >= 1 is required when importing into an existing target worker');
      }
    }

    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>('/v3/workers/import', {
      method: 'POST',
      body: {
        target_worker_id: options?.targetWorkerId,
        expected_revision: options?.expectedRevision,
        definition: payload,
      },
    });
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }

  /**
   * Attaches an automation definition with an executable plan to the worker.
   */
  async attachAutomation(params: AttachWorkerAutomationParams): Promise<WorkerRecord> {
    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id)}/automations`,
      {
        method: 'POST',
        body: {
          expected_worker_revision: params.expected_worker_revision,
          automation: params.automation,
        },
      }
    );
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }

  /**
   * Updates an existing attached automation definition on the worker.
   */
  async updateAutomation(params: UpdateWorkerAutomationParams): Promise<WorkerRecord> {
    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id)}/automations/${encodeURIComponent(params.automation_id)}`,
      {
        method: 'PUT',
        body: {
          expected_worker_revision: params.expected_worker_revision,
          automation: params.automation,
        },
      }
    );
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }

  /**
   * Detaches/removes an automation definition from the worker.
   */
  async removeAutomation(params: RemoveWorkerAutomationParams): Promise<WorkerRecord> {
    const q = new URLSearchParams();
    q.set('expected_worker_revision', params.expected_worker_revision.toString());

    const res = await this.transport.request<{ ok?: boolean; worker: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id)}/automations/${encodeURIComponent(params.automation_id)}?${q.toString()}`,
      {
        method: 'DELETE',
      }
    );
    return res.data.worker ?? (res.data as unknown as WorkerRecord);
  }
}
