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
