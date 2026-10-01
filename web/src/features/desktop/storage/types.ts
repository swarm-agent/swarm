export interface StorageBucket {
  id: string
  account_scope_id: string
  name: string
  provider: 's3' | 'gcs' | 'local' | 'mock'
  bucket_name: string
  endpoint?: string
  region?: string
  prefix?: string
  access_key_id?: string
  secret_access_key?: string
  enabled: boolean
  canonical?: boolean
  status?: 'active' | 'pending_approval' | 'rejected'
  proposed_by?: string
  proposal_reason?: string
  created_at: number
  updated_at: number
}

export interface StorageDiscoveredWorker {
  worker_id: string
  bucket_id: string
  account_scope_id: string
  name: string
  description?: string
  version?: string
  tags?: string[]
  status?: 'pending_approval' | 'active' | 'paused' | 'disabled' | string
  target?: 'cloud' | 'local'
  cloud_config?: Record<string, any>
  schedule?: Record<string, any>
  brain?: Record<string, any>
  tasks?: Array<Record<string, any>>
  total_jobs_count?: number
  total_spend_usd?: number
  total_tokens?: number
  has_base_context: boolean
  sessions_count: number
  last_session_id?: string
  last_status?: string
  last_step?: string
  last_progress?: number
  last_activity_at: number
  imported: boolean
  imported_at?: number
  discovered_at: number
  updated_at: number
}

export interface StorageScanSummary {
  bucket_id: string
  scanned_at: number
  workers_found: number
  sessions_found: number
  deliverables_found?: number
  deliverables_new?: number
}

export interface DeliverableTelemetry {
  model?: string
  thinking_level?: string
  prompt_tokens?: number
  candidate_tokens?: number
  thinking_tokens?: number
  total_tokens?: number
  compute_duration_ms?: number
  cost_usd?: number
}

export interface DeliverableReview {
  decision: 'approved' | 'rejected' | 'published' | 'dismissed'
  target?: 'cloud' | 'local'
  reviewed_by?: string
  reviewed_at?: number
  note?: string
  tx_id?: string
}

export interface StorageDiscoveredDeliverable {
  deliverable_id: string
  worker_id: string
  session_id: string
  bucket_id: string
  account_scope_id?: string
  title: string
  summary?: string
  kind?: string
  status: 'pending_review' | 'accepted' | 'approved' | 'rejected' | 'published' | string
  sha256?: string
  files?: Array<{
    name: string
    path: string
    size_bytes: number
    sha256: string
    content_type?: string
  }>
  actions?: Array<{
    id: string
    label: string
    action_type?: string
    endpoint: string
    variant?: string
  }>
  payload?: Record<string, any>
  telemetry?: DeliverableTelemetry
  review?: DeliverableReview
  created_at: number
  updated_at: number
  imported_at?: number
}
