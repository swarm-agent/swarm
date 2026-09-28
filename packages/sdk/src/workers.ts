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
  WorkerValidateResult,
  WorkerExportResult,
  ImportWorkerOptions,
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
   * Requires nonblank name, instructions, and idempotency_key.
   * Server assigns stable identity and initial revision 1.
   */
  async create(params: CreateWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Worker creation parameters are required');
    }
    if (!params.name || typeof params.name !== 'string' || params.name.trim() === '') {
      throw new SwarmValidationError('Worker name is required and cannot be blank');
    }
    if (!params.instructions || typeof params.instructions !== 'string' || params.instructions.trim() === '') {
      throw new SwarmValidationError('Worker instructions are required and cannot be blank');
    }
    if (!params.idempotency_key || typeof params.idempotency_key !== 'string' || params.idempotency_key.trim() === '') {
      throw new SwarmValidationError('Worker idempotency_key is required and cannot be blank');
    }

    const body: Record<string, unknown> = {
      name: params.name.trim(),
      instructions: params.instructions,
      idempotency_key: params.idempotency_key.trim(),
    };
    if (params.description !== undefined) body.description = params.description;
    if (params.requested_capabilities !== undefined) body.requested_capabilities = params.requested_capabilities;
    if (params.workspace_requirements !== undefined) body.workspace_requirements = params.workspace_requirements;
    if (params.automations !== undefined) body.automations = params.automations;
    if (params.metadata !== undefined) body.metadata = params.metadata;

    const res = await this.transport.request<{ worker?: WorkerRecord }>('/v3/workers', {
      method: 'POST',
      headers: {
        'Idempotency-Key': params.idempotency_key.trim(),
      },
      body,
    });

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Retrieves a durable worker by its server ID or attached automation ID.
   */
  async get(id: string): Promise<WorkerRecord | null> {
    if (!id || typeof id !== 'string' || id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    try {
      const res = await this.transport.request<{ worker?: WorkerRecord }>(
        `/v3/workers/${encodeURIComponent(id.trim())}`,
        { method: 'GET' }
      );
      if (
        !res.data ||
        typeof res.data !== 'object' ||
        !res.data.worker ||
        typeof res.data.worker !== 'object' ||
        typeof res.data.worker.id !== 'string' ||
        res.data.worker.id === ''
      ) {
        throw new SwarmValidationError('Malformed response envelope: expected worker record');
      }
      return res.data.worker;
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
    if (params?.limit !== undefined) {
      if (!Number.isSafeInteger(params.limit) || params.limit < 1 || params.limit > 100) {
        throw new SwarmValidationError('limit must be an integer between 1 and 100');
      }
      q.set('limit', params.limit.toString());
    }
    if (params?.cursor !== undefined) {
      if (typeof params.cursor !== 'string' || params.cursor.length > 1024) {
        throw new SwarmValidationError('cursor must be a valid string <= 1024 characters');
      }
      if (params.cursor.trim() !== '') {
        q.set('cursor', params.cursor.trim());
      }
    }
    if (params?.lifecycle_state) {
      q.set('lifecycle_state', params.lifecycle_state);
    }
    if (params?.include_deleted !== undefined) {
      q.set('include_deleted', params.include_deleted ? 'true' : 'false');
    }

    const queryStr = q.toString();
    const endpoint = queryStr ? `/v3/workers?${queryStr}` : '/v3/workers';

    const res = await this.transport.request<{ workers?: WorkerRecord[]; next_cursor?: string }>(endpoint, {
      method: 'GET',
    });

    if (!res.data || typeof res.data !== 'object' || !Array.isArray(res.data.workers)) {
      throw new SwarmValidationError('Malformed response envelope: expected workers array');
    }
    return {
      workers: res.data.workers,
      next_cursor: typeof res.data.next_cursor === 'string' && res.data.next_cursor.length > 0 ? res.data.next_cursor : undefined,
    };
  }

  /**
   * Updates a worker's definition with an explicit expected revision for optimistic concurrency.
   * Body carries expected_revision only (no query params, no local_bindings).
   */
  async update(id: string, expectedRevision: number, params: UpdateWorkerParams): Promise<WorkerRecord> {
    if (!id || typeof id !== 'string' || id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 1) {
      throw new SwarmValidationError('Explicit numeric expectedRevision >= 1 is required');
    }
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Update parameters are required');
    }

    const body: Record<string, unknown> = {
      expected_revision: expectedRevision,
    };
    if (params.name !== undefined) body.name = params.name;
    if (params.description !== undefined) body.description = params.description;
    if (params.instructions !== undefined) body.instructions = params.instructions;
    if (params.requested_capabilities !== undefined) body.requested_capabilities = params.requested_capabilities;
    if (params.workspace_requirements !== undefined) body.workspace_requirements = params.workspace_requirements;
    if (params.automations !== undefined) body.automations = params.automations;
    if (params.metadata !== undefined) body.metadata = params.metadata;
    if (params.change_summary !== undefined) body.change_summary = params.change_summary;

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(id.trim())}`,
      {
        method: 'PUT',
        body,
      }
    );

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Deletes a worker with explicit revision guard.
   */
  async delete(id: string, expectedRevision: number): Promise<boolean> {
    if (!id || typeof id !== 'string' || id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 1) {
      throw new SwarmValidationError('Explicit numeric expectedRevision >= 1 is required');
    }

    const res = await this.transport.request<{ ok?: boolean }>(
      `/v3/workers/${encodeURIComponent(id.trim())}?expected_revision=${expectedRevision}`,
      {
        method: 'DELETE',
      }
    );

    if (!res.data || typeof res.data !== 'object' || res.data.ok !== true) {
      throw new SwarmValidationError('Malformed response envelope: expected ok: true');
    }
    return true;
  }

  /**
   * Validates a portable worker definition document without persisting it.
   * Sends raw PortableWorkerDefinition body and expects { valid: true, worker: doc }.
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
    if (!payload || typeof payload !== 'object') {
      throw new SwarmValidationError('Portable worker definition must be a valid object');
    }

    const res = await this.transport.request<WorkerValidateResult>('/v3/workers/validate', {
      method: 'POST',
      body: payload,
    });

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      typeof res.data.valid !== 'boolean' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object'
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected valid boolean and worker definition');
    }
    return {
      valid: res.data.valid,
      worker: res.data.worker,
    };
  }

  /**
   * Exports a complete portable worker definition.
   * Returns { worker: doc }.
   */
  async export(id: string): Promise<WorkerExportResult> {
    if (!id || typeof id !== 'string' || id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    const res = await this.transport.request<WorkerExportResult>(
      `/v3/workers/${encodeURIComponent(id.trim())}/export`,
      { method: 'GET' }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker || typeof res.data.worker !== 'object') {
      throw new SwarmValidationError('Malformed response envelope: expected worker export document');
    }
    return {
      worker: res.data.worker,
    };
  }

  /**
   * Imports a portable worker definition.
   * Sends raw document body.
   * - If mode is 'new' (default when targetWorkerId omitted): requires idempotencyKey option, queries ?mode=new&idempotency_key=...
   * - If mode is 'update' (or targetWorkerId provided): requires targetWorkerId and expectedRevision, queries ?mode=update&worker_id=...&expected_revision=...
   */
  async import(
    definition: PortableWorkerDefinition | string,
    options?: ImportWorkerOptions
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
    if (!payload || typeof payload !== 'object') {
      throw new SwarmValidationError('Portable worker definition must be a valid object');
    }

    const mode = options?.mode ?? (options?.targetWorkerId ? 'update' : 'new');
    const q = new URLSearchParams();
    q.set('mode', mode);

    const headers: Record<string, string> = {};

    if (mode === 'update') {
      if (!options?.targetWorkerId || typeof options.targetWorkerId !== 'string' || options.targetWorkerId.trim() === '') {
        throw new SwarmValidationError('targetWorkerId is required when importing in update mode');
      }
      if (typeof options.expectedRevision !== 'number' || !Number.isSafeInteger(options.expectedRevision) || options.expectedRevision < 1) {
        throw new SwarmValidationError('Explicit numeric expectedRevision >= 1 is required when importing into an existing target worker');
      }
      q.set('worker_id', options.targetWorkerId.trim());
      q.set('expected_revision', options.expectedRevision.toString());
    } else if (mode === 'new') {
      if (!options?.idempotencyKey || typeof options.idempotencyKey !== 'string' || options.idempotencyKey.trim() === '') {
        throw new SwarmValidationError('idempotencyKey is required when importing a new worker');
      }
      q.set('idempotency_key', options.idempotencyKey.trim());
      headers['Idempotency-Key'] = options.idempotencyKey.trim();
    } else {
      throw new SwarmValidationError("mode must be either 'new' or 'update'");
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/import?${q.toString()}`,
      {
        method: 'POST',
        headers,
        body: payload,
      }
    );

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Attaches an automation definition to the worker.
   * Body carries { expected_worker_revision, automation }, no query param.
   */
  async attachAutomation(params: AttachWorkerAutomationParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Attachment parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_worker_revision) || params.expected_worker_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_worker_revision >= 1 is required');
    }
    if (!params.automation || typeof params.automation !== 'object') {
      throw new SwarmValidationError('Automation definition is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations`,
      {
        method: 'POST',
        body: {
          expected_worker_revision: params.expected_worker_revision,
          automation: params.automation,
        },
      }
    );

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Updates an existing attached automation definition on the worker.
   * Body carries { expected_worker_revision, automation }, no query param.
   */
  async updateAutomation(params: UpdateWorkerAutomationParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Update automation parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!params.automation_id || typeof params.automation_id !== 'string' || params.automation_id.trim() === '') {
      throw new SwarmValidationError('Automation ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_worker_revision) || params.expected_worker_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_worker_revision >= 1 is required');
    }
    if (!params.automation || typeof params.automation !== 'object') {
      throw new SwarmValidationError('Automation definition is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations/${encodeURIComponent(params.automation_id.trim())}`,
      {
        method: 'PUT',
        body: {
          expected_worker_revision: params.expected_worker_revision,
          automation: params.automation,
        },
      }
    );

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Detaches/removes an automation definition from the worker.
   */
  async removeAutomation(params: RemoveWorkerAutomationParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Remove automation parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!params.automation_id || typeof params.automation_id !== 'string' || params.automation_id.trim() === '') {
      throw new SwarmValidationError('Automation ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_worker_revision) || params.expected_worker_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_worker_revision >= 1 is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations/${encodeURIComponent(params.automation_id.trim())}?expected_worker_revision=${params.expected_worker_revision}`,
      {
        method: 'DELETE',
      }
    );

    if (
      !res.data ||
      typeof res.data !== 'object' ||
      !res.data.worker ||
      typeof res.data.worker !== 'object' ||
      typeof res.data.worker.id !== 'string' ||
      res.data.worker.id === ''
    ) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }
}
