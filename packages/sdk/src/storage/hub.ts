import type {
  StorageDriver,
  StorageHubConfig,
  WorkerManifest,
  WorkerBaseContext,
  WorkerSessionState,
  DeliverableManifest,
  PublishDeliverableOptions,
  DeliverableFileRef,
} from './types.js';

export class WorkerStorageHub {
  private driver: StorageDriver;
  private workerId: string;
  private prefix: string;

  constructor(config: StorageHubConfig) {
    this.driver = config.driver;
    this.workerId = config.workerId;
    this.prefix = config.prefix ? config.prefix.replace(/\/+$/, '') + '/' : '';
  }

  private key(path: string): string {
    return `${this.prefix}${path.replace(/^\/+/, '')}`;
  }

  private async sha256Hex(data: string | Uint8Array): Promise<string> {
    const bytes = typeof data === 'string' ? new TextEncoder().encode(data) : data;
    const hashBuffer = await crypto.subtle.digest('SHA-256', bytes as any);
    return Array.from(new Uint8Array(hashBuffer))
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('');
  }

  async initWorker(definition: Partial<WorkerManifest> = {}): Promise<WorkerManifest> {
    const key = this.key(`workers/${this.workerId}/worker.json`);
    const now = new Date().toISOString();
    let existing: WorkerManifest | null = null;

    try {
      const raw = await this.driver.readString(key);
      if (raw) existing = JSON.parse(raw);
    } catch {
      // ignore parse error on new worker
    }

    const manifest: WorkerManifest = {
      id: this.workerId,
      name: definition.name || existing?.name || this.workerId,
      description: definition.description ?? existing?.description ?? '',
      version: definition.version || existing?.version || '1.0.0',
      tags: definition.tags || existing?.tags || [],
      createdAt: existing?.createdAt || now,
      updatedAt: now,
    };

    await this.driver.write(key, JSON.stringify(manifest, null, 2), 'application/json');
    return manifest;
  }

  async getWorkerManifest(): Promise<WorkerManifest | null> {
    const key = this.key(`workers/${this.workerId}/worker.json`);
    const raw = await this.driver.readString(key);
    if (!raw) return null;
    return JSON.parse(raw);
  }

  async loadBaseContext(): Promise<WorkerBaseContext> {
    const basePrefix = `workers/${this.workerId}/base`;
    const instructionsStr = await this.driver.readString(this.key(`${basePrefix}/instructions.md`));
    const toolsStr = await this.driver.readString(this.key(`${basePrefix}/tools.json`));
    const memoryStr = await this.driver.readString(this.key(`${basePrefix}/memory.json`));

    return {
      instructions: instructionsStr ?? undefined,
      tools: toolsStr ? JSON.parse(toolsStr) : undefined,
      memory: memoryStr ? JSON.parse(memoryStr) : undefined,
      updatedAt: new Date().toISOString(),
    };
  }

  async saveBaseContext(context: WorkerBaseContext): Promise<void> {
    const basePrefix = `workers/${this.workerId}/base`;
    if (context.instructions !== undefined) {
      await this.driver.write(this.key(`${basePrefix}/instructions.md`), context.instructions, 'text/markdown');
    }
    if (context.tools !== undefined) {
      await this.driver.write(this.key(`${basePrefix}/tools.json`), JSON.stringify(context.tools, null, 2), 'application/json');
    }
    if (context.memory !== undefined) {
      await this.driver.write(this.key(`${basePrefix}/memory.json`), JSON.stringify(context.memory, null, 2), 'application/json');
    }
  }

  async startSession(sessionId: string, initialConfig?: Record<string, any>): Promise<WorkerSessionState> {
    const sessionPrefix = `workers/${this.workerId}/sessions/${sessionId}`;
    const now = new Date().toISOString();

    if (initialConfig) {
      await this.driver.write(
        this.key(`${sessionPrefix}/run_config.json`),
        JSON.stringify(initialConfig, null, 2),
        'application/json'
      );
    }

    const state: WorkerSessionState = {
      sessionId,
      workerId: this.workerId,
      status: 'starting',
      progress: 0,
      step: 'Initialized session',
      startedAt: now,
      updatedAt: now,
    };

    await this.driver.write(
      this.key(`${sessionPrefix}/state.json`),
      JSON.stringify(state, null, 2),
      'application/json'
    );

    return state;
  }

  async updateSessionState(sessionId: string, updates: Partial<WorkerSessionState>): Promise<WorkerSessionState> {
    const sessionPrefix = `workers/${this.workerId}/sessions/${sessionId}`;
    const stateKey = this.key(`${sessionPrefix}/state.json`);
    const now = new Date().toISOString();

    let current: WorkerSessionState = {
      sessionId,
      workerId: this.workerId,
      status: 'running',
      startedAt: now,
      updatedAt: now,
    };

    try {
      const raw = await this.driver.readString(stateKey);
      if (raw) current = JSON.parse(raw);
    } catch {
      // ignore
    }

    const merged: WorkerSessionState = {
      ...current,
      ...updates,
      sessionId,
      workerId: this.workerId,
      updatedAt: now,
    };

    if (merged.status === 'completed' && !merged.completedAt) {
      merged.completedAt = now;
      if (merged.progress === undefined || merged.progress < 100) {
        merged.progress = 100;
      }
    }

    await this.driver.write(stateKey, JSON.stringify(merged, null, 2), 'application/json');
    return merged;
  }

  async appendTrace(sessionId: string, message: string, data?: any): Promise<void> {
    const traceKey = this.key(`workers/${this.workerId}/sessions/${sessionId}/trace.jsonl`);
    const entry = JSON.stringify({
      timestamp: new Date().toISOString(),
      message,
      ...(data ? { data } : {}),
    }) + '\n';

    const existing = await this.driver.readString(traceKey);
    const combined = existing ? existing + entry : entry;
    await this.driver.write(traceKey, combined, 'application/x-ndjson');
  }

  async publishDeliverable(options: PublishDeliverableOptions): Promise<DeliverableManifest> {
    const deliverableId = options.id || `deliv_${Date.now()}_${Math.random().toString(36).substring(2, 8)}`;
    const deliverablePrefix = `deliverables/${this.workerId}/${deliverableId}`;
    const now = new Date().toISOString();

    const fileRefs: DeliverableFileRef[] = [];
    let combinedFileDigests = '';

    if (options.files && options.files.length > 0) {
      for (const file of options.files) {
        const fileContent = typeof file.content === 'string'
          ? new TextEncoder().encode(file.content)
          : file.content;
        const fileSha256 = await this.sha256Hex(fileContent);
        const filePath = `${deliverablePrefix}/files/${file.name}`;

        await this.driver.write(
          this.key(filePath),
          fileContent,
          file.contentType || 'application/octet-stream'
        );

        fileRefs.push({
          name: file.name,
          path: filePath,
          sizeBytes: fileContent.byteLength,
          sha256: fileSha256,
          contentType: file.contentType,
        });

        combinedFileDigests += fileSha256;
      }
    }

    const aggregateSha256 = options.payload
      ? await this.sha256Hex(JSON.stringify(options.payload) + combinedFileDigests)
      : await this.sha256Hex(combinedFileDigests || deliverableId);

    const manifest: DeliverableManifest = {
      id: deliverableId,
      workerId: this.workerId,
      sessionId: options.sessionId,
      title: options.title,
      summary: options.summary,
      kind: options.kind || 'ai_deliverable',
      status: 'pending_review',
      files: fileRefs,
      actions: options.actions || [],
      payload: options.payload,
      sha256: aggregateSha256,
      createdAt: now,
      updatedAt: now,
    };

    await this.driver.write(
      this.key(`${deliverablePrefix}/manifest.json`),
      JSON.stringify(manifest, null, 2),
      'application/json'
    );

    // Automatically update the session state to completed
    await this.updateSessionState(options.sessionId, {
      status: 'completed',
      progress: 100,
      step: `Published deliverable: ${options.title}`,
    });

    return manifest;
  }

  async getSessionState(sessionId: string): Promise<WorkerSessionState | null> {
    const sessionPrefix = `workers/${this.workerId}/sessions/${sessionId}`;
    const stateKey = this.key(`${sessionPrefix}/state.json`);
    const raw = await this.driver.readString(stateKey);
    if (!raw) return null;
    return JSON.parse(raw);
  }

  async getDeliverableManifest(deliverableId: string): Promise<DeliverableManifest | null> {
    const deliverablePrefix = `deliverables/${this.workerId}/${deliverableId}`;
    const manifestKey = this.key(`${deliverablePrefix}/manifest.json`);
    const raw = await this.driver.readString(manifestKey);
    if (!raw) return null;
    return JSON.parse(raw);
  }

  async listDeliverables(): Promise<DeliverableManifest[]> {
    const prefix = this.key(`deliverables/${this.workerId}/`);
    const keys = await this.driver.list(prefix);
    const manifests: DeliverableManifest[] = [];

    for (const k of keys) {
      if (k.endsWith('/manifest.json')) {
        const raw = await this.driver.readString(k);
        if (raw) {
          try {
            manifests.push(JSON.parse(raw));
          } catch {
            // ignore
          }
        }
      }
    }

    return manifests.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }
}
