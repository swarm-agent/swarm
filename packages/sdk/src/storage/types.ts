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
  | 'rejected'
  | 'dismissed';

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
