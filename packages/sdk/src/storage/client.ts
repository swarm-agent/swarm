import type { SwarmTransport } from '../transport.js';

export interface StorageBucketRecord {
  id: string;
  account_scope_id?: string;
  name: string;
  provider: 's3' | 'gcs' | 'mock' | 'local' | string;
  bucket_name: string;
  endpoint?: string;
  region?: string;
  prefix?: string;
  access_key_id?: string;
  secret_access_key?: string;
  enabled: boolean;
  canonical?: boolean;
  status?: 'active' | 'pending_approval' | 'rejected' | string;
  proposed_by?: string;
  proposal_reason?: string;
  created_at?: number;
  updated_at?: number;
}

export interface StorageDiscoveredWorkerRecord {
  worker_id: string;
  bucket_id: string;
  account_scope_id?: string;
  name: string;
  description?: string;
  version?: string;
  tags?: string[];
  has_base_context: boolean;
  base_context?: Record<string, any>;
  sessions_count?: number;
  last_session_id?: string;
  last_status?: string;
  last_step?: string;
  last_progress?: number;
  last_activity_at?: number;
  imported: boolean;
  imported_at?: number;
  discovered_at?: number;
  updated_at?: number;
}

export interface StorageDiscoveredDeliverableRecord {
  deliverable_id: string;
  worker_id: string;
  session_id: string;
  bucket_id: string;
  account_scope_id?: string;
  title: string;
  summary?: string;
  kind?: string;
  status: 'pending_review' | 'accepted' | 'rejected' | string;
  sha256?: string;
  files?: Array<{
    name: string;
    path: string;
    size_bytes: number;
    sha256: string;
    content_type?: string;
  }>;
  actions?: Array<{
    id: string;
    label: string;
    action_type?: string;
    endpoint: string;
    variant?: string;
  }>;
  payload?: Record<string, any>;
  created_at?: number;
  updated_at?: number;
  imported_at?: number;
}

export interface CloudConnectionProposal {
  provider: 's3' | 'gcs' | 'r2' | 'minio' | 'local' | string;
  bucket_name: string;
  name?: string;
  endpoint?: string;
  region?: string;
  prefix?: string;
  access_key_id?: string;
  secret_access_key?: string;
  proposed_by?: string;
  proposal_reason?: string;
  make_canonical?: boolean;
}

export interface ScanSummary {
  bucket_id: string;
  workers_found: number;
  workers_added?: string[];
  workers_updated?: string[];
  workers_removed?: string[];
  sessions_found: number;
  deliverables_new: number;
  scanned_at: number;
}

export class SwarmStorageNamespace {
  constructor(private transport: SwarmTransport) {}

  /**
   * Lists all configured storage buckets for this account.
   */
  async listBuckets(): Promise<StorageBucketRecord[]> {
    const res = await this.transport.request<{ buckets: StorageBucketRecord[] }>(
      '/v1/storage/buckets',
      { method: 'GET' }
    );
    return res.data.buckets;
  }

  /**
   * Registers a new S3, GCS, or local storage bucket.
   */
  async registerBucket(bucket: Partial<StorageBucketRecord>): Promise<StorageBucketRecord> {
    const res = await this.transport.request<{ bucket: StorageBucketRecord }>(
      '/v1/storage/buckets',
      { method: 'POST', body: bucket }
    );
    return res.data.bucket;
  }

  /**
   * Deletes a registered storage bucket.
   */
  async deleteBucket(id: string): Promise<boolean> {
    const res = await this.transport.request<{ deleted: boolean }>(
      `/v1/storage/buckets/${id}`,
      { method: 'DELETE' }
    );
    return res.data.deleted;
  }

  /**
   * Triggers an immediate scan of the storage bucket for workers and deliverables.
   */
  async scanBucket(id: string): Promise<ScanSummary> {
    const res = await this.transport.request<{ scan_summary: ScanSummary }>(
      `/v1/storage/buckets/${id}/scan`,
      { method: 'POST' }
    );
    return res.data.scan_summary;
  }

  /**
   * Lists workers discovered across all registered buckets.
   */
  async listWorkers(): Promise<StorageDiscoveredWorkerRecord[]> {
    const res = await this.transport.request<{ workers: StorageDiscoveredWorkerRecord[] }>(
      '/v1/storage/workers',
      { method: 'GET' }
    );
    return res.data.workers;
  }

  /**
   * Accepts and imports a discovered worker into local state.
   */
  async importWorker(workerId: string): Promise<StorageDiscoveredWorkerRecord> {
    const res = await this.transport.request<{ worker: StorageDiscoveredWorkerRecord }>(
      `/v1/storage/workers/${workerId}/import`,
      { method: 'POST' }
    );
    return res.data.worker;
  }

  /**
   * Lists deliverables discovered across all registered buckets.
   */
  async listDeliverables(): Promise<StorageDiscoveredDeliverableRecord[]> {
    const res = await this.transport.request<{ deliverables: StorageDiscoveredDeliverableRecord[] }>(
      '/v1/storage/deliverables',
      { method: 'GET' }
    );
    return res.data.deliverables;
  }

  /**
   * Imports a discovered deliverable and downloads its artifacts to the target workspace directory.
   */
  async importDeliverable(
    id: string,
    targetWorkspacePath?: string
  ): Promise<{ deliverable: StorageDiscoveredDeliverableRecord; targetDir: string }> {
    const res = await this.transport.request<{
      deliverable: StorageDiscoveredDeliverableRecord;
      target_dir: string;
    }>(`/v1/storage/deliverables/${id}/import`, {
      method: 'POST',
      body: { target_workspace_path: targetWorkspacePath },
    });
    return { deliverable: res.data.deliverable, targetDir: res.data.target_dir };
  }

  /**
   * Retrieves the current canonical Swarm cloud storage bucket.
   */
  async getCanonicalBucket(): Promise<StorageBucketRecord | null> {
    const res = await this.transport.request<{
      bucket: StorageBucketRecord | null;
      configured: boolean;
    }>('/v1/storage/canonical', { method: 'GET' });
    return res.data.bucket;
  }

  /**
   * Accepts and designates a storage bucket as the canonical Swarm cloud connection.
   */
  async setCanonicalBucket(id: string): Promise<StorageBucketRecord> {
    const res = await this.transport.request<{ bucket: StorageBucketRecord }>(
      `/v1/storage/buckets/${id}/accept-canonical`,
      { method: 'POST' }
    );
    return res.data.bucket;
  }

  /**
   * Proposes a new cloud storage connection from an AI worker/agent for user approval.
   */
  async proposeConnection(
    proposal: CloudConnectionProposal
  ): Promise<StorageBucketRecord> {
    const res = await this.transport.request<{ proposal: StorageBucketRecord }>(
      '/v1/storage/proposals',
      { method: 'POST', body: proposal }
    );
    return res.data.proposal;
  }

  /**
   * Lists all pending cloud storage connection proposals awaiting user approval.
   */
  async listProposals(): Promise<StorageBucketRecord[]> {
    const res = await this.transport.request<{
      proposals: StorageBucketRecord[];
      count: number;
    }>('/v1/storage/proposals', { method: 'GET' });
    return res.data.proposals;
  }

  /**
   * Rejects/dismisses a pending connection proposal.
   */
  async rejectProposal(id: string): Promise<boolean> {
    const res = await this.transport.request<{ rejected: boolean }>(
      `/v1/storage/buckets/${id}/reject`,
      { method: 'POST' }
    );
    return res.data.rejected;
  }
}
