import { requestJson } from '../../../app/api'

export interface DeliverableActionContract {
  action: string;
  target_url?: string;
  target_secret_ref?: string;
  parameters?: Record<string, unknown>;
}

export interface DeliverableRevisionFeedback {
  requested_at: number;
  requested_by?: string;
  notes: string;
  tags?: string[];
}

export interface DeliverableRecord {
  id: string;
  account_id: string;
  workspace_id?: string;
  workspace_path?: string;
  worker_id?: string;
  occurrence_id?: string;
  session_id?: string;
  title: string;
  kind: 'social_post' | 'alert' | 'report' | 'pr_patch' | 'media_bundle' | 'code_patch' | 'media' | 'custom' | string;
  status: 'pending_review' | 'approved' | 'rejected' | 'published' | 'dismissed' | 'needs_revision' | string;
  summary?: string;
  payload?: Record<string, unknown>;
  media_refs?: Array<{
    label?: string;
    path?: string;
    media_type?: string;
    filename?: string;
    artifact_id?: string;
    revision_ref?: string;
    [key: string]: unknown;
  }>;
  action_contract?: DeliverableActionContract;
  action_result?: Record<string, unknown>;
  revision_feedback?: DeliverableRevisionFeedback;
  revision_history?: DeliverableRevisionFeedback[];
  created_at: number;
  updated_at: number;
  reviewed_at?: number;
  reviewed_by?: string;
}

export interface DeliverableFilter {
  status?: string;
  worker_id?: string;
  kind?: string;
  workspace_id?: string;
  limit?: number;
}

export async function fetchDeliverables(filter: DeliverableFilter = {}): Promise<DeliverableRecord[]> {
  const q = new URLSearchParams()
  if (filter.status) q.set('status', filter.status)
  if (filter.worker_id) q.set('worker_id', filter.worker_id)
  if (filter.kind) q.set('kind', filter.kind)
  if (filter.workspace_id) q.set('workspace_id', filter.workspace_id)
  if (typeof filter.limit === 'number') q.set('limit', String(filter.limit))

  const path = q.toString() ? `/v3/deliverables?${q.toString()}` : '/v3/deliverables'
  const res = await requestJson<{ deliverables?: DeliverableRecord[]; count: number }>(path)
  return res.deliverables || []
}

export async function fetchDeliverable(id: string): Promise<DeliverableRecord> {
  const res = await requestJson<{ deliverable: DeliverableRecord }>(`/v3/deliverables/${encodeURIComponent(id)}`)
  return res.deliverable
}

export async function approveDeliverable(
  id: string,
  params: { parameters?: Record<string, unknown> } = {}
): Promise<{ deliverable: DeliverableRecord; action_result?: Record<string, unknown> }> {
  return requestJson<{ deliverable: DeliverableRecord; action_result?: Record<string, unknown> }>(
    `/v3/deliverables/${encodeURIComponent(id)}/approve`,
    {
      method: 'POST',
      body: JSON.stringify(params),
    }
  )
}

export async function dismissDeliverable(id: string): Promise<DeliverableRecord> {
  const res = await requestJson<{ deliverable: DeliverableRecord }>(
    `/v3/deliverables/${encodeURIComponent(id)}/dismiss`,
    {
      method: 'POST',
    }
  )
  return res.deliverable
}

export async function requestDeliverableChanges(
  id: string,
  params: { notes: string; tags?: string[] }
): Promise<DeliverableRecord> {
  const res = await requestJson<{ deliverable: DeliverableRecord }>(
    `/v3/deliverables/${encodeURIComponent(id)}/request_changes`,
    {
      method: 'POST',
      body: JSON.stringify(params),
    }
  )
  return res.deliverable
}

export async function deleteDeliverable(id: string): Promise<void> {
  await requestJson<{ ok: boolean; deleted_id: string }>(
    `/v3/deliverables/${encodeURIComponent(id)}`,
    {
      method: 'DELETE',
    }
  )
}
