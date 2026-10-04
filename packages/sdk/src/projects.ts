import {
  SwarmApiError,
  SwarmTimeoutError,
} from './errors.js';
import type { SwarmTransport } from './transport.js';
import type {
  ApproveProjectTaskResult,
  ClearProjectContextResult,
  CompleteProjectTaskResult,
  CreateProjectParams,
  CreateProjectTaskParams,
  CreateProjectTaskResult,
  DeleteProjectMediaResult,
  DeleteProjectResult,
  DeleteProjectTaskResult,
  DeployProjectTaskProgramResult,
  GetProjectTaskProgramResult,
  GetProjectTaskResult,
  IntegrateProjectTaskResult,
  ListProjectsParams,
  PreviewProjectTaskParams,
  ProjectRecord,
  ProjectTaskApprovalGuards,
  ProjectTaskMediaRef,
  ProjectTaskModelPreviewResult,
  ProjectTaskPreviewResult,
  ProjectTaskRecord,
  ProjectWorkspaceRef,
  RedeployProjectTaskJobResult,
  RefineProjectTaskParams,
  RefineProjectTaskResult,
  RejectProjectTaskResult,
  ReopenProjectTaskResult,
  RequestOptions,
  SessionRecord,
  SyncStreamParams,
  SyncStreamResult,
  SynthesizeProjectContextParams,
  UpdateProjectParams,
  UpdateProjectTaskParams,
} from './types.js';

export class SwarmProjectsNamespace {
  private readonly transport: SwarmTransport;

  constructor(transport: SwarmTransport) {
    this.transport = transport;
  }

  // ==========================================================================
  // Project CRUD Operations
  // ==========================================================================

  /**
   * Lists projects across the active account.
   */
  async list(params: ListProjectsParams = {}, options?: RequestOptions): Promise<ProjectRecord[]> {
    const q = new URLSearchParams();
    if (typeof params.limit === 'number' && params.limit > 0) {
      q.set('limit', String(params.limit));
    }
    const queryStr = q.toString() ? `?${q.toString()}` : '';
    const res = await this.transport.request<{ projects?: ProjectRecord[]; count?: number } | ProjectRecord[]>(
      `/v3/projects${queryStr}`,
      { method: 'GET', ...options }
    );
    if (Array.isArray(res.data)) {
      return res.data;
    }
    if (res.data && typeof res.data === 'object' && 'projects' in res.data && Array.isArray(res.data.projects)) {
      return res.data.projects;
    }
    return [];
  }

  /**
   * Creates a new project in the active account.
   */
  async create(params: CreateProjectParams, options?: RequestOptions): Promise<ProjectRecord> {
    const res = await this.transport.request<{ project?: ProjectRecord } | ProjectRecord>(
      '/v3/projects',
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'project' in data && data.project) {
      return data.project;
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return data as ProjectRecord;
    }
    throw new SwarmApiError('Malformed response: missing project record', { status: res.status, details: data });
  }

  /**
   * Retrieves a single project by ID.
   */
  async get(projectId: string, options?: RequestOptions): Promise<ProjectRecord> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ project?: ProjectRecord } | ProjectRecord>(
      `/v3/projects/${id}`,
      { method: 'GET', ...options }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'project' in data && data.project) {
      return data.project;
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return data as ProjectRecord;
    }
    throw new SwarmApiError('Malformed response: missing project record', { status: res.status, details: data });
  }

  /**
   * Ensures a project exists by name. If found, returns the existing record;
   * otherwise creates a new project with the specified name and optional workspace binding.
   */
  async ensureProject(
    name: string,
    options: { description?: string; workspacePath?: string; workspaceId?: string } = {},
    reqOptions?: RequestOptions
  ): Promise<ProjectRecord> {
    const trimmed = name.trim();
    const existingList = await this.list({ limit: 50 }, reqOptions);
    const existing = existingList.find((p) => p.name === trimmed);
    if (existing) return existing;

    const workspaces: ProjectWorkspaceRef[] | undefined = options.workspacePath
      ? [{ path: options.workspacePath, workspace_id: options.workspaceId }]
      : undefined;

    return this.create(
      {
        name: trimmed,
        description: options.description ?? `Project ${trimmed}`,
        workspaces,
      },
      reqOptions
    );
  }

  /**
   * Retrieves an active orchestrator session for this project, or creates one if none exists.
   */
  async getOrchestratorSession(
    projectId: string,
    options: { title?: string; mode?: 'auto' | 'plan'; agentName?: string } = {},
    reqOptions?: RequestOptions
  ): Promise<SessionRecord> {
    const sessions = await this.listConversations(projectId, {}, reqOptions);
    const active = sessions.find((s) => !s.archived_at && !s.archived);
    if (active) return active;
    return this.createConversation(projectId, options, reqOptions);
  }

  /** Project orchestrator conversations, not delegated task sessions or workspace chats. */
  async listConversations(
    projectId: string,
    params: { limit?: number } = {},
    reqOptions?: RequestOptions
  ): Promise<SessionRecord[]> {
    if (!projectId.trim()) throw new Error('projectId is required');
    const q = new URLSearchParams();
    if (params.limit) q.set('limit', String(params.limit));
    const suffix = q.size ? `?${q}` : '';
    const res = await this.transport.request<any>(
      `/v3/projects/${encodeURIComponent(projectId.trim())}/sessions${suffix}`,
      { method: 'GET', ...reqOptions }
    );
    const items = Array.isArray(res.data) ? res.data : res.data?.sessions;
    if (!Array.isArray(items)) throw new SwarmApiError('Malformed conversation list', { status: res.status });
    return items.map((item) => {
      const session = item.session ? { ...item.session, projection: item.projection } : item;
      if (!session?.id) throw new SwarmApiError('Conversation is missing session identity', { status: res.status });
      return session;
    });
  }

  /** Create an independent project conversation without archiving existing conversations.
   * Models/context resolve on the daemon. Project conversations never bind a workspace.
   * Reuse clientRequestId when retrying an uncertain creation result.
   */
  async createConversation(
    projectId: string,
    options: { title?: string; mode?: 'auto' | 'plan'; agentName?: string; clientRequestId?: string } = {},
    reqOptions?: RequestOptions
  ): Promise<SessionRecord> {
    const id = projectId.trim();
    if (!id) throw new Error('projectId is required');
    if (options.agentName && options.agentName !== 'system-orchestrator') {
      throw new Error('Project conversations require system-orchestrator; use chat.createSession for standalone chat');
    }
    const encId = encodeURIComponent(id);
    const createRes = await this.transport.request<{ ok: boolean; session?: SessionRecord } | SessionRecord>(
      `/v3/projects/${encId}/sessions`,
      {
        method: 'POST',
        body: {
          client_request_id: options.clientRequestId ?? `proj-orch-${globalThis.crypto.randomUUID()}`,
          agent_name: options.agentName ?? 'system-orchestrator',
          title: options.title ?? 'Orchestrator AI',
          mode: options.mode ?? 'auto',
        },
        ...reqOptions,
      }
    );
    const data = createRes.data;
    if (data && typeof data === 'object' && 'session' in data && data.session) {
      return data.session;
    }
    if (data && 'id' in data && data.id) return data as SessionRecord;
    throw new SwarmApiError('Malformed conversation creation response', { status: createRes.status });
  }

  /**
   * Updates an existing project's metadata, context, or bound workspaces.
   */
  async update(projectId: string, params: UpdateProjectParams, options?: RequestOptions): Promise<ProjectRecord> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ project?: ProjectRecord } | ProjectRecord>(
      `/v3/projects/${id}`,
      {
        method: 'PATCH',
        body: params,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'project' in data && data.project) {
      return data.project;
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return data as ProjectRecord;
    }
    throw new SwarmApiError('Malformed response: missing project record', { status: res.status, details: data });
  }

  /**
   * Deletes a project by ID.
   */
  async delete(projectId: string, options?: RequestOptions): Promise<DeleteProjectResult> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<DeleteProjectResult>(
      `/v3/projects/${id}`,
      { method: 'DELETE', ...options }
    );
    return res.data ?? { status: 'deleted', id: projectId.trim() };
  }

  /**
   * Synthesizes Markdown project context from project name and linked workspaces.
   */
  async synthesizeContext(params: SynthesizeProjectContextParams, options?: RequestOptions): Promise<string> {
    const res = await this.transport.request<{ project_context?: string }>(
      '/v3/projects/synthesize-context',
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    return res.data?.project_context ?? '';
  }

  /**
   * Clears orchestrator context for a project, archiving old session and provisioning replacement.
   */
  async clearContext(projectId: string, options?: RequestOptions): Promise<ClearProjectContextResult> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<ClearProjectContextResult>(
      `/v3/projects/${id}/orchestrator:clear-context`,
      { method: 'POST', ...options }
    );
    return res.data ?? { status: 'cleared' };
  }

  // ==========================================================================
  // Project Media Sub-resource
  // ==========================================================================

  /**
   * Lists media items attached to a project.
   */
  async listMedia(projectId: string, options?: RequestOptions): Promise<ProjectTaskMediaRef[]> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ media?: ProjectTaskMediaRef[]; count?: number } | ProjectTaskMediaRef[]>(
      `/v3/projects/${id}/media`,
      { method: 'GET', ...options }
    );
    if (Array.isArray(res.data)) {
      return res.data;
    }
    if (res.data && typeof res.data === 'object' && 'media' in res.data && Array.isArray(res.data.media)) {
      return res.data.media;
    }
    return [];
  }

  /**
   * Uploads or attaches a media item to a project.
   */
  async uploadMedia(projectId: string, media: Partial<ProjectTaskMediaRef>, options?: RequestOptions): Promise<ProjectTaskMediaRef> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ media?: ProjectTaskMediaRef } | ProjectTaskMediaRef>(
      `/v3/projects/${id}/media`,
      {
        method: 'POST',
        body: media,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'media' in data && data.media) {
      return data.media;
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return data as ProjectTaskMediaRef;
    }
    throw new SwarmApiError('Malformed response: missing media record', { status: res.status, details: data });
  }

  /**
   * Removes an attached media item from a project.
   */
  async deleteMedia(projectId: string, mediaId: string, options?: RequestOptions): Promise<DeleteProjectMediaResult> {
    const pId = encodeURIComponent(projectId.trim());
    const mId = encodeURIComponent(mediaId.trim());
    const res = await this.transport.request<DeleteProjectMediaResult>(
      `/v3/projects/${pId}/media/${mId}`,
      { method: 'DELETE', ...options }
    );
    return res.data ?? { removed: true, media_id: mediaId.trim(), uploaded_media: [] };
  }

  // ==========================================================================
  // Project Tasks CRUD & Previews
  // ==========================================================================

  /**
   * Lists tasks associated with a project.
   */
  async listTasks(projectId: string, options?: RequestOptions, view: 'active' | 'archived' = 'active'): Promise<ProjectTaskRecord[]> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ tasks?: ProjectTaskRecord[]; count?: number } | ProjectTaskRecord[]>(
      `/v3/projects/${id}/tasks${view === 'archived' ? '?view=archived' : ''}`,
      { method: 'GET', ...options }
    );
    if (Array.isArray(res.data)) {
      return res.data;
    }
    if (res.data && typeof res.data === 'object' && 'tasks' in res.data && Array.isArray(res.data.tasks)) {
      return res.data.tasks;
    }
    return [];
  }

  /**
   * Creates a new autonomous task inside a project.
   */
  async createTask(projectId: string, params: CreateProjectTaskParams, options?: RequestOptions): Promise<CreateProjectTaskResult> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<{ task?: ProjectTaskRecord; model_preview?: any } | ProjectTaskRecord>(
      `/v3/projects/${id}/tasks`,
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        task: data.task,
        model_preview: data.model_preview,
      };
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return {
        task: data as ProjectTaskRecord,
      };
    }
    throw new SwarmApiError('Malformed response: missing task record', { status: res.status, details: data });
  }

  /**
   * Retrieves a single task by ID within a project, including model preview.
   */
  async getTask(projectId: string, taskId: string, options?: RequestOptions): Promise<GetProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<{ task?: ProjectTaskRecord; model_preview?: any } | ProjectTaskRecord>(
      `/v3/projects/${pId}/tasks/${tId}`,
      { method: 'GET', ...options }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        task: data.task,
        model_preview: data.model_preview,
      };
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return {
        task: data as ProjectTaskRecord,
      };
    }
    throw new SwarmApiError('Malformed response: missing task record', { status: res.status, details: data });
  }

  /**
   * Updates an existing pending or queued task in a project.
   */
  async updateTask(
    projectId: string,
    taskId: string,
    params: UpdateProjectTaskParams,
    options?: RequestOptions
  ): Promise<ProjectTaskRecord> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<{ task?: ProjectTaskRecord } | ProjectTaskRecord>(
      `/v3/projects/${pId}/tasks/${tId}`,
      {
        method: 'PATCH',
        body: params,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return data.task;
    }
    if (data && typeof data === 'object' && 'id' in data) {
      return data as ProjectTaskRecord;
    }
    throw new SwarmApiError('Malformed response: missing task record', { status: res.status, details: data });
  }

  /** Archives a task only at the supplied revision. */
  async archiveTask(projectId: string, taskId: string, revision: number, options?: RequestOptions): Promise<ProjectTaskRecord> {
    const path = `/v3/projects/${encodeURIComponent(projectId.trim())}/tasks/${encodeURIComponent(taskId.trim())}/archive`;
    const res = await this.transport.request<{ task: ProjectTaskRecord }>(path, { method: 'POST', body: { revision }, ...options });
    if (!res.data?.task) throw new SwarmApiError('Malformed response: missing archived task', { status: res.status, details: res.data });
    return res.data.task;
  }

  /** Deletes only an archived, unlaunched task at the supplied revision. */
  async deleteTask(projectId: string, taskId: string, revision: number, options?: RequestOptions): Promise<DeleteProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<DeleteProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}?revision=${encodeURIComponent(String(revision))}`,
      { method: 'DELETE', ...options }
    );
    return res.data ?? { status: 'deleted', task_id: taskId.trim() };
  }

  /**
   * Previews how a task will be routed, planned, and modeled before creating it.
   */
  async previewTask(
    projectId: string,
    params: PreviewProjectTaskParams,
    options?: RequestOptions
  ): Promise<ProjectTaskPreviewResult> {
    const id = encodeURIComponent(projectId.trim());
    const res = await this.transport.request<ProjectTaskPreviewResult>(
      `/v3/projects/${id}/tasks:preview`,
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    return res.data;
  }

  /**
   * Retrieves the authoritative model preview for an existing task.
   */
  async getTaskModelPreview(
    projectId: string,
    taskId: string,
    options?: RequestOptions
  ): Promise<ProjectTaskModelPreviewResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<ProjectTaskModelPreviewResult>(
      `/v3/projects/${pId}/tasks/${tId}/model-preview`,
      { method: 'GET', ...options }
    );
    return res.data;
  }

  // ==========================================================================
  // Task Plan Lifecycle: Approval, Rejection, Reopening, Refinement, Completion
  // ==========================================================================

  /**
   * Approves a task session with optional exact revision guards.
   * If the task is bound to a structured plan, exact session_id, plan_id, and definition_revision
   * guards are enforced by the backend to prevent stale approvals.
   */
  async approveTask(
    projectId: string,
    taskId: string,
    guards?: ProjectTaskApprovalGuards,
    options?: RequestOptions
  ): Promise<ApproveProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<ApproveProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/approve`,
      {
        method: 'POST',
        body: guards ?? {},
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        status: (data.status as any) ?? 'approved',
        task: data.task,
      };
    }
    throw new SwarmApiError('Malformed response: missing task in approve response', {
      status: res.status,
      details: data,
    });
  }

  /**
   * Convenient alias for approveTask.
   */
  async acceptTask(
    projectId: string,
    taskId: string,
    guards?: ProjectTaskApprovalGuards,
    options?: RequestOptions
  ): Promise<ApproveProjectTaskResult> {
    return this.approveTask(projectId, taskId, guards, options);
  }

  /**
   * Rejects a task and marks its bound plan as rejected.
   */
  async rejectTask(projectId: string, taskId: string, guards?: ProjectTaskApprovalGuards, options?: RequestOptions): Promise<RejectProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<RejectProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/reject`,
      { method: 'POST', body: guards ?? {}, ...options }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        status: (data.status as any) ?? 'rejected',
        task: data.task,
      };
    }
    throw new SwarmApiError('Malformed response: missing task in reject response', {
      status: res.status,
      details: data,
    });
  }

  /**
   * Reopens a completed or needs_review task with optional feedback instructions.
   */
  async reopenTask(
    projectId: string,
    taskId: string,
    params?: ProjectTaskApprovalGuards & { feedback?: string },
    options?: RequestOptions
  ): Promise<ReopenProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<ReopenProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/reopen`,
      {
        method: 'POST',
        body: params ?? {},
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        status: (data.status as any) ?? 'reopened',
        task: data.task,
      };
    }
    throw new SwarmApiError('Malformed response: missing task in reopen response', {
      status: res.status,
      details: data,
    });
  }

  /**
   * Marks a task as accepted and completed by the user.
   */
  async completeTask(projectId: string, taskId: string, options?: RequestOptions): Promise<CompleteProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<CompleteProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/complete`,
      { method: 'POST', ...options }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        status: (data.status as any) ?? 'completed',
        task: data.task,
      };
    }
    throw new SwarmApiError('Malformed response: missing task in complete response', {
      status: res.status,
      details: data,
    });
  }

  /**
   * Refines a task plan based on user feedback directives or error recovery strategy.
   */
  async refineTask(
    projectId: string,
    taskId: string,
    params: RefineProjectTaskParams,
    options?: RequestOptions
  ): Promise<RefineProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<RefineProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/refine`,
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    const data = res.data;
    if (data && typeof data === 'object' && 'task' in data && data.task) {
      return {
        status: (data.status as any) ?? 'refined',
        task: data.task,
      };
    }
    throw new SwarmApiError('Malformed response: missing task in refine response', {
      status: res.status,
      details: data,
    });
  }

  // ==========================================================================
  // Task Integration & Task Program Execution
  // ==========================================================================

  /**
   * Integrates task commits into the parent workspace repository.
   */
  async integrateTask(
    projectId: string,
    taskId: string,
    options?: RequestOptions
  ): Promise<IntegrateProjectTaskResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<IntegrateProjectTaskResult>(
      `/v3/projects/${pId}/tasks/${tId}/integrate`,
      { method: 'POST', ...options }
    );
    return res.data;
  }

  /**
   * Deploys a task program for staged multi-agent execution.
   */
  async deployTaskProgram(
    projectId: string,
    taskId: string,
    options?: RequestOptions
  ): Promise<DeployProjectTaskProgramResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<DeployProjectTaskProgramResult>(
      `/v3/projects/${pId}/tasks/${tId}/program:deploy`,
      { method: 'POST', ...options }
    );
    return res.data;
  }

  /**
   * Redeploys a failed or rejected job within a deployed task program.
   */
  async redeployTaskProgramJob(
    projectId: string,
    taskId: string,
    params: { job_id: string; feedback?: string },
    options?: RequestOptions
  ): Promise<RedeployProjectTaskJobResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<RedeployProjectTaskJobResult>(
      `/v3/projects/${pId}/tasks/${tId}/program:redeploy-job`,
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    return res.data;
  }

  /**
   * Inspects the durable TaskProgramRecord for a deployed task program.
   */
  async getTaskProgram(
    projectId: string,
    taskId: string,
    options?: RequestOptions
  ): Promise<GetProjectTaskProgramResult> {
    const pId = encodeURIComponent(projectId.trim());
    const tId = encodeURIComponent(taskId.trim());
    const res = await this.transport.request<GetProjectTaskProgramResult>(
      `/v3/projects/${pId}/tasks/${tId}/program`,
      { method: 'GET', ...options }
    );
    return res.data;
  }

  // ==========================================================================
  // Sync Stream, Event Replay, and Polling Helpers
  // ==========================================================================

  /**
   * Streams outbox events after an opaque endpoint cursor via HTTP POST /v3/sync/stream.
   * Cursors remain strictly opaque strings.
   */
  async syncStream<TEvent = any>(params: SyncStreamParams, options?: RequestOptions): Promise<SyncStreamResult<TEvent>> {
    const res = await this.transport.request<SyncStreamResult<TEvent>>(
      '/v3/sync/stream',
      {
        method: 'POST',
        body: params,
        ...options,
      }
    );
    return res.data;
  }

  /**
   * Replays events starting after an opaque endpoint cursor.
   */
  async replayEvents<TEvent = any>(
    cursor: string,
    params: Omit<SyncStreamParams, 'endpoint_cursor'> = {},
    options?: RequestOptions
  ): Promise<SyncStreamResult<TEvent>> {
    return this.syncStream<TEvent>(
      {
        ...params,
        endpoint_cursor: cursor,
      },
      options
    );
  }

  /**
   * Polls a task until a predicate condition is satisfied, or times out/aborts.
   */
  async waitForTask(
    projectId: string,
    taskId: string,
    predicate: (task: ProjectTaskRecord) => boolean,
    options: { timeoutMs?: number; pollIntervalMs?: number; signal?: AbortSignal } = {}
  ): Promise<ProjectTaskRecord> {
    const timeoutMs = options.timeoutMs ?? 60_000;
    const pollIntervalMs = options.pollIntervalMs ?? 500;
    const deadline = Date.now() + timeoutMs;

    while (Date.now() < deadline) {
      if (options.signal?.aborted) {
        throw new SwarmApiError('Request aborted by caller', { status: 0 });
      }
      const { task } = await this.getTask(projectId, taskId, {
        signal: options.signal,
        timeoutMs: options.timeoutMs,
      });
      if (predicate(task)) {
        return task;
      }
      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(resolve, pollIntervalMs);
        if (options.signal) {
          options.signal.addEventListener(
            'abort',
            () => {
              clearTimeout(timer);
              reject(new SwarmApiError('Request aborted by caller', { status: 0 }));
            },
            { once: true }
          );
        }
      });
    }
    throw new SwarmTimeoutError(`Task did not satisfy condition within ${timeoutMs}ms`, timeoutMs);
  }

  /**
   * Async generator yielding sync stream events starting after an opaque cursor,
   * respecting cancellation via AbortSignal.
   */
  async *streamEvents<TEvent = any>(
    initialCursor: string,
    params: Omit<SyncStreamParams, 'endpoint_cursor'> = {},
    options: { pollIntervalMs?: number; signal?: AbortSignal; timeoutMs?: number } = {}
  ): AsyncIterableIterator<TEvent> {
    let cursor = initialCursor;
    const pollIntervalMs = options.pollIntervalMs ?? 500;

    while (!options.signal?.aborted) {
      const res = await this.syncStream<TEvent>(
        { ...params, endpoint_cursor: cursor },
        { signal: options.signal, timeoutMs: options.timeoutMs }
      );
      cursor = res.endpoint_cursor;
      for (const event of res.events || []) {
        yield event;
      }
      if (!res.has_more) {
        if (options.signal?.aborted) break;
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, pollIntervalMs);
          if (options.signal) {
            options.signal.addEventListener(
              'abort',
              () => {
                clearTimeout(timer);
                resolve();
              },
              { once: true }
            );
          }
        });
      }
    }
  }
}
