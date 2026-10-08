import type { SwarmTransport } from './transport.js';
import { SwarmWorkerControlNamespace } from './worker-control.js';
import type {
  WorkerRecord,
  WorkerRevisionRecord,
  WorkerRunRecord,
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
  ActivateWorkerParams,
  PauseWorkerParams,
  ResumeWorkerParams,
  ArchiveWorkerParams,
  DeleteWorkerParams,
  SetWorkerAutomationEnabledParams,
  DirectWorkerRequestParams,
  TestWorkerRunParams,
  TriggerWorkerParams,
  MintWorkerTriggerTokenParams,
  MintWorkerTriggerTokenResult,
  ListWorkerRunsParams,
  ListWorkerRunsResult,
  GetWorkerRunParams,
  CancelWorkerRunParams,
} from './types.js';
import { SwarmValidationError } from './errors.js';

/**
 * SwarmWorkersNamespace manages durable, account-owned workers, their revisions,
 * attached automations, and portable import/export definitions over canonical /v3/workers APIs.
 *
 * Distinct from legacy SwarmAutomationsNamespace which targets /v3/automations/v2.
 */
export class SwarmWorkersNamespace {
  readonly control: SwarmWorkerControlNamespace;
  private transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
    this.control = new SwarmWorkerControlNamespace(transport);
  }

  /**
   * Creates a new durable worker in idle state.
   * Requires nonblank name and idempotency_key; instructions may be empty.
   * Server assigns stable identity and initial revision 1.
   */
  async create(params: CreateWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Worker creation parameters are required');
    }
    if (!params.name || typeof params.name !== 'string' || params.name.trim() === '') {
      throw new SwarmValidationError('Worker name is required and cannot be blank');
    }
    if (params.instructions !== undefined && typeof params.instructions !== 'string') {
      throw new SwarmValidationError('Worker instructions must be a string');
    }
    if ('id' in params || 'local_bindings' in params) {
      throw new SwarmValidationError('Worker identity and local bindings are server-owned');
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
    if (res.data.next_cursor !== undefined && typeof res.data.next_cursor !== 'string') throw new SwarmValidationError('Malformed response cursor');
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

    if ('local_bindings' in params) throw new SwarmValidationError('Local bindings require approved activation');
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

  /**
   * Activates a stored worker with approved local workspace bindings.
   * Transitions worker from idle/paused to active state.
   */
  async activate(params: ActivateWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Activate worker parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_revision) || params.expected_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_revision >= 1 is required');
    }
    if (!params.local_bindings || typeof params.local_bindings !== 'object') {
      throw new SwarmValidationError('Approved local_bindings map is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/activate`,
      {
        method: 'POST',
        body: {
          expected_revision: params.expected_revision,
          local_bindings: params.local_bindings,
          ...(params.activate === undefined ? {} : { activate: params.activate }),
        },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Local deployment alias for activate: validates and activates with approved bindings.
   */
  async deploy(params: ActivateWorkerParams): Promise<WorkerRecord> {
    return this.activate(params);
  }

  /**
   * Pauses an active worker, closing admission and invalidating queued work.
   */
  async pause(params: PauseWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Pause worker parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_revision) || params.expected_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_revision >= 1 is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/pause`,
      {
        method: 'POST',
        body: { expected_revision: params.expected_revision },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Resumes a paused worker without replaying cancelled work.
   */
  async resume(params: ResumeWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Resume worker parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_revision) || params.expected_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_revision >= 1 is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/resume`,
      {
        method: 'POST',
        body: { expected_revision: params.expected_revision },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Archives a worker, stopping future work and disabling automations.
   * Rejects if there are active running executions.
   */
  async archive(params: ArchiveWorkerParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Archive worker parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_revision) || params.expected_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_revision >= 1 is required');
    }

    const res = await this.transport.request<{ worker?: WorkerRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/archive`,
      {
        method: 'POST',
        body: { expected_revision: params.expected_revision },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Deletes a worker with safe stop barrier enforcement (tombstone).
   * Rejects if there are active running executions.
   */
  async delete(params: DeleteWorkerParams): Promise<{ ok: boolean; deleted: boolean }> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Delete worker parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.expected_revision) || params.expected_revision < 1) {
      throw new SwarmValidationError('Explicit numeric expected_revision >= 1 is required');
    }

    const res = await this.transport.request<{ ok?: boolean; deleted?: boolean }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}?expected_revision=${params.expected_revision}`,
      { method: 'DELETE' }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.ok) {
      throw new SwarmValidationError('Malformed response envelope: expected { ok: true, deleted: true }');
    }
    return { ok: true, deleted: true };
  }

  /**
   * Enables a specific attached automation on the worker.
   */
  async enableAutomation(params: SetWorkerAutomationEnabledParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Enable automation parameters are required');
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
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations/${encodeURIComponent(params.automation_id.trim())}/enable`,
      {
        method: 'POST',
        body: { expected_worker_revision: params.expected_worker_revision },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Disables a specific attached automation on the worker without stopping other jobs.
   */
  async disableAutomation(params: SetWorkerAutomationEnabledParams): Promise<WorkerRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Disable automation parameters are required');
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
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations/${encodeURIComponent(params.automation_id.trim())}/disable`,
      {
        method: 'POST',
        body: { expected_worker_revision: params.expected_worker_revision },
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.worker) {
      throw new SwarmValidationError('Malformed response envelope: expected worker record');
    }
    return res.data.worker;
  }

  /**
   * Dispatches a direct execution request to the worker without creating a persistent automation.
   */
  async directRequest(params: DirectWorkerRequestParams): Promise<WorkerRunRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Direct request parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    const hasPrompt = typeof params.prompt === 'string' && params.prompt.trim() !== '';
    const hasInput = params.input && typeof params.input === 'object' && Object.keys(params.input).length > 0;
    if (!hasPrompt && !hasInput) {
      throw new SwarmValidationError('Prompt or input is required for direct request');
    }

    const headers: Record<string, string> = {};
    if (params.idempotency_key && typeof params.idempotency_key === 'string' && params.idempotency_key.trim() !== '') {
      headers['Idempotency-Key'] = params.idempotency_key.trim();
    }
    const body: Record<string, unknown> = {};
    if (hasPrompt) body.prompt = params.prompt!.trim();
    if (params.input) body.input = params.input;
    if (params.idempotency_key) body.idempotency_key = params.idempotency_key.trim();

    const res = await this.transport.request<{ ok?: boolean; run?: WorkerRunRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/direct`,
      {
        method: 'POST',
        headers,
        body,
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.run) {
      throw new SwarmValidationError('Malformed response envelope: expected worker run record');
    }
    return res.data.run;
  }

  /**
   * Dispatches an explicitly labelled test run. Does not enable schedules.
   */
  async testRun(params: TestWorkerRunParams): Promise<WorkerRunRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Test run parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }

    const headers: Record<string, string> = {};
    if (params.idempotency_key && typeof params.idempotency_key === 'string' && params.idempotency_key.trim() !== '') {
      headers['Idempotency-Key'] = params.idempotency_key.trim();
    }
    const body: Record<string, unknown> = {};
    if (params.automation_id && typeof params.automation_id === 'string' && params.automation_id.trim() !== '') {
      body.automation_id = params.automation_id.trim();
    }
    if (params.prompt && typeof params.prompt === 'string' && params.prompt.trim() !== '') {
      body.prompt = params.prompt.trim();
    }
    if (params.input && typeof params.input === 'object') {
      body.input = params.input;
    }
    if (params.idempotency_key) body.idempotency_key = params.idempotency_key.trim();

    const res = await this.transport.request<{ ok?: boolean; run?: WorkerRunRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/test`,
      {
        method: 'POST',
        headers,
        body,
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.run) {
      throw new SwarmValidationError('Malformed response envelope: expected worker run record');
    }
    return res.data.run;
  }

  /**
   * Dispatches an authenticated external trigger to the worker or specific automation.
   */
  async trigger(params: TriggerWorkerParams): Promise<WorkerRunRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Trigger parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }

    const headers: Record<string, string> = {};
    if (params.idempotency_key && typeof params.idempotency_key === 'string' && params.idempotency_key.trim() !== '') {
      headers['Idempotency-Key'] = params.idempotency_key.trim();
    }
    const body: Record<string, unknown> = {};
    if (params.automation_id && typeof params.automation_id === 'string' && params.automation_id.trim() !== '') {
      body.automation_id = params.automation_id.trim();
    }
    if (params.payload && typeof params.payload === 'object') {
      body.payload = params.payload;
    }
    if (params.idempotency_key) body.idempotency_key = params.idempotency_key.trim();

    const url = params.automation_id
      ? `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/automations/${encodeURIComponent(params.automation_id.trim())}/trigger`
      : `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/trigger`;

    const res = await this.transport.request<{ ok?: boolean; run?: WorkerRunRecord }>(
      url,
      {
        method: 'POST',
        headers,
        body,
      }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.run) {
      throw new SwarmValidationError('Malformed response envelope: expected worker run record');
    }
    return res.data.run;
  }

  /**
   * Mints an authenticated scoped trigger token for this worker.
   */
  async mintTriggerToken(params: MintWorkerTriggerTokenParams): Promise<MintWorkerTriggerTokenResult> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Mint trigger token parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }

    const body: Record<string, unknown> = {};
    if (params.name) body.name = params.name.trim();
    if (params.save_to_secrets !== undefined) body.save_to_secrets = params.save_to_secrets;

    const res = await this.transport.request<MintWorkerTriggerTokenResult>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/token`,
      {
        method: 'POST',
        body,
      }
    );
    if (!res.data || typeof res.data !== 'object' || typeof res.data.token !== 'string' || res.data.token === '') {
      throw new SwarmValidationError('Malformed response envelope: expected trigger token result');
    }
    return res.data;
  }

  /**
   * Lists historical and active execution runs for a worker with pagination.
   */
  async listRuns(params: ListWorkerRunsParams): Promise<ListWorkerRunsResult> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('List runs parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }

    const q = new URLSearchParams();
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor !== undefined && params.cursor.trim() !== '') q.set('cursor', params.cursor.trim());
    const qs = q.toString() ? `?${q.toString()}` : '';

    const res = await this.transport.request<{ runs?: WorkerRunRecord[]; next_cursor?: string }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/runs${qs}`,
      { method: 'GET' }
    );
    if (!res.data || typeof res.data !== 'object' || !Array.isArray(res.data.runs)) {
      throw new SwarmValidationError('Malformed response envelope: expected { runs: WorkerRunRecord[] }');
    }
    return {
      runs: res.data.runs,
      next_cursor: res.data.next_cursor,
    };
  }

  /**
   * Retrieves a single worker execution run by ID.
   */
  async getRun(params: GetWorkerRunParams): Promise<WorkerRunRecord | null> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Get run parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!params.run_id || typeof params.run_id !== 'string' || params.run_id.trim() === '') {
      throw new SwarmValidationError('Run ID is required and cannot be blank');
    }

    try {
      const res = await this.transport.request<{ run?: WorkerRunRecord }>(
        `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/runs/${encodeURIComponent(params.run_id.trim())}`,
        { method: 'GET' }
      );
      if (!res.data || typeof res.data !== 'object' || !res.data.run) {
        throw new SwarmValidationError('Malformed response envelope: expected { run: WorkerRunRecord }');
      }
      return res.data.run;
    } catch (err: any) {
      if (err?.status === 404 || err?.statusCode === 404) {
        return null;
      }
      throw err;
    }
  }

  /**
   * Cancels an active or admitted execution run.
   */
  async cancelRun(params: CancelWorkerRunParams): Promise<WorkerRunRecord> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Cancel run parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!params.run_id || typeof params.run_id !== 'string' || params.run_id.trim() === '') {
      throw new SwarmValidationError('Run ID is required and cannot be blank');
    }

    const res = await this.transport.request<{ ok?: boolean; run?: WorkerRunRecord }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/runs/${encodeURIComponent(params.run_id.trim())}/cancel`,
      { method: 'POST' }
    );
    if (!res.data || typeof res.data !== 'object' || !res.data.run) {
      throw new SwarmValidationError('Malformed response envelope: expected { run: WorkerRunRecord }');
    }
    return res.data.run;
  }

  /**
   * Retrieves revision history snapshots for a worker.
   */
  async getHistory(params: { worker_id: string; limit?: number; cursor?: string }): Promise<{ history: WorkerRevisionRecord[]; next_cursor?: string }> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('History parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    const q = new URLSearchParams();
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor !== undefined && params.cursor.trim() !== '') q.set('cursor', params.cursor.trim());
    const qs = q.toString() ? `?${q.toString()}` : '';

    const res = await this.transport.request<{ revisions?: WorkerRevisionRecord[]; next_cursor?: string }>(
      `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/history${qs}`,
      { method: 'GET' }
    );
    if (!res.data || typeof res.data !== 'object' || !Array.isArray(res.data.revisions)) {
      throw new SwarmValidationError('Malformed response envelope: expected { revisions: WorkerRevisionRecord[] }');
    }
    return {
      history: res.data.revisions,
      next_cursor: res.data.next_cursor,
    };
  }

  /**
   * Retrieves a specific pinned revision snapshot of a worker.
   */
  async getRevision(params: { worker_id: string; revision: number }): Promise<WorkerRevisionRecord | null> {
    if (!params || typeof params !== 'object') {
      throw new SwarmValidationError('Revision parameters are required');
    }
    if (!params.worker_id || typeof params.worker_id !== 'string' || params.worker_id.trim() === '') {
      throw new SwarmValidationError('Worker ID is required and cannot be blank');
    }
    if (!Number.isSafeInteger(params.revision) || params.revision < 1) {
      throw new SwarmValidationError('Explicit numeric revision >= 1 is required');
    }

    try {
      const res = await this.transport.request<{ revision?: WorkerRevisionRecord }>(
        `/v3/workers/${encodeURIComponent(params.worker_id.trim())}/revisions/${params.revision}`,
        { method: 'GET' }
      );
      if (!res.data || typeof res.data !== 'object' || !res.data.revision) {
        throw new SwarmValidationError('Malformed response envelope: expected { revision: WorkerRevisionRecord }');
      }
      return res.data.revision;
    } catch (err: any) {
      if (err?.status === 404 || err?.statusCode === 404) {
        return null;
      }
      throw err;
    }
  }
}
