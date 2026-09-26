import type { NotificationAction } from '../types.js';

export interface StorageDriver {
  read(key: string): Promise<Uint8Array | null>;
  readString(key: string): Promise<string | null>;
  write(key: string, data: Uint8Array | string, contentType?: string): Promise<void>;
  delete(key: string): Promise<void>;
  list(prefix: string): Promise<string[]>;
  exists(key: string): Promise<boolean>;
}

export interface WorkerManifest {
  id: string;
  name: string;
  description?: string;
  version?: string;
  tags?: string[];
  createdAt: string;
  updatedAt: string;
}

export interface WorkerBaseContext {
  instructions?: string;
  tools?: Record<string, any>[];
  memory?: Record<string, any>;
  updatedAt?: string;
}

export type WorkerSessionStatus =
  | 'starting'
  | 'running'
  | 'completed'
  | 'failed'
  | 'paused';

export interface WorkerSessionState {
  sessionId: string;
  workerId: string;
  status: WorkerSessionStatus;
  progress?: number; // 0 to 100
  step?: string;
  error?: string;
  startedAt: string;
  updatedAt: string;
  completedAt?: string;
}

export interface DeliverableFileRef {
  name: string;
  path: string;
  sizeBytes: number;
  sha256: string;
  contentType?: string;
}

export type DeliverableReviewStatus =
  | 'pending_review'
  | 'accepted'
  | 'approved'
  | 'rejected'
  | 'published'
  | 'dismissed';

export interface DeliverableTelemetry {
  model?: string;
  thinking_level?: string;
  prompt_tokens?: number;
  candidate_tokens?: number;
  thinking_tokens?: number;
  total_tokens?: number;
  compute_duration_ms?: number;
  cost_usd?: number;
}

export interface DeliverableReview {
  decision: 'approved' | 'rejected' | 'published' | 'dismissed';
  target?: 'cloud' | 'local';
  reviewedBy?: string;
  reviewedAt?: string | number;
  note?: string;
  txId?: string;
}

export interface DeliverableManifest {
  id: string;
  workerId: string;
  sessionId: string;
  title: string;
  summary?: string;
  kind?: string;
  status: DeliverableReviewStatus;
  files: DeliverableFileRef[];
  actions?: NotificationAction[];
  payload?: Record<string, any>;
  telemetry?: DeliverableTelemetry;
  review?: DeliverableReview;
  sha256?: string;
  createdAt: string;
  updatedAt: string;
}

export interface PublishDeliverableOptions {
  id?: string;
  sessionId: string;
  title: string;
  summary?: string;
  kind?: string;
  telemetry?: DeliverableTelemetry;
  files?: Array<{
    name: string;
    content: string | Uint8Array;
    contentType?: string;
  }>;
  actions?: NotificationAction[];
  payload?: Record<string, any>;
}

export interface StorageHubConfig {
  bucket?: string;
  workerId: string;
  driver: StorageDriver;
  prefix?: string;
}

// ─────────────────────────────────────────────────────────────────────────────
// WORKER ACTOR SPECIFICATION (worker.json) & DURABLE JOB TELEMETRY
// ─────────────────────────────────────────────────────────────────────────────

export type WorkerActorStatus = 'pending_approval' | 'active' | 'paused' | 'disabled';

export interface WorkerActorCloudConfig {
  provider: 'gcp' | 'aws';
  project?: string;
  region?: string;
  runtime: 'cloud-run-job' | 'ecs-task' | 'local';
  job_name?: string;
  service_account?: string;
  image?: string;
  env?: Record<string, string>;
}

export interface WorkerActorSchedule {
  kind: 'cron' | 'interval' | 'trigger' | 'none';
  cron?: string;
  interval_seconds?: number;
  timezone?: string;
  enabled?: boolean;
}

export interface WorkerActorBrain {
  model: string;
  thinking_level?: string;
  instructions: string;
  tools?: Array<{ name: string; description?: string; parameters?: any }>;
  secrets?: Record<string, string>;
  memory?: Record<string, any>;
  workspace_path?: string;
}

export interface WorkerActorTask {
  id: string;
  title: string;
  type: 'automated' | 'one_off';
  prompt: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  priority?: number;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
  sessionId?: string;
  deliverableId?: string;
  telemetry?: DeliverableTelemetry;
  error?: string;
}

export interface WorkerJobExecutionTelemetry {
  model: string;
  thinking_level?: string;
  prompt_tokens: number;
  candidate_tokens: number;
  thinking_tokens?: number;
  total_tokens: number;
  compute_duration_ms: number;
  cost_usd: number;
}

export interface WorkerJobExecutionLog {
  jobId: string;
  workerId: string;
  taskId?: string;
  type: 'automation' | 'one_off';
  status: 'success' | 'failed';
  startedAt: string;
  completedAt: string;
  durationMs: number;
  telemetry: WorkerJobExecutionTelemetry;
  logs: Array<{
    timestamp: string;
    level: 'info' | 'warn' | 'error' | 'debug';
    message: string;
    data?: any;
  }>;
  deliverableId?: string;
  error?: string;
}

export interface WorkerActorSpec {
  $schema?: string;
  id: string;
  name: string;
  description?: string;
  version: string;
  status: WorkerActorStatus;
  cloud?: WorkerActorCloudConfig;
  schedule?: WorkerActorSchedule;
  brain: WorkerActorBrain;
  tasks?: WorkerActorTask[];
  createdAt: string;
  updatedAt: string;
}
