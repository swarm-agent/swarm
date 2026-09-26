import { requestJson } from '../../../app/api'
import type { StorageBucket, StorageDiscoveredWorker, StorageScanSummary } from './types'

export async function listStorageBuckets(): Promise<StorageBucket[]> {
  try {
    const res = await requestJson<{ buckets: StorageBucket[]; count: number }>('/v1/storage/buckets')
    return res.buckets || []
  } catch (err) {
    console.warn('[storage] failed to list buckets', err)
    return []
  }
}

export async function registerStorageBucket(
  bucket: Partial<StorageBucket>
): Promise<StorageBucket> {
  const res = await requestJson<{ bucket: StorageBucket }>('/v1/storage/buckets', {
    method: 'POST',
    body: JSON.stringify(bucket),
  })
  return res.bucket
}

export async function deleteStorageBucket(id: string): Promise<void> {
  await requestJson(`/v1/storage/buckets/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

export async function scanStorageBucket(id: string): Promise<StorageScanSummary> {
  const res = await requestJson<{ scan_summary: StorageScanSummary }>(
    `/v1/storage/buckets/${encodeURIComponent(id)}/scan`,
    {
      method: 'POST',
    }
  )
  return res.scan_summary
}

export async function listStorageWorkers(): Promise<StorageDiscoveredWorker[]> {
  try {
    const res = await requestJson<{ workers: StorageDiscoveredWorker[]; count: number }>(
      '/v1/storage/workers'
    )
    return res.workers || []
  } catch (err) {
    console.warn('[storage] failed to list workers', err)
    return []
  }
}

export async function getCanonicalStorageBucket(): Promise<StorageBucket | null> {
  try {
    const res = await requestJson<{ bucket: StorageBucket | null; configured: boolean }>(
      '/v1/storage/canonical'
    )
    return res.bucket
  } catch (err) {
    console.warn('[storage] failed to get canonical bucket', err)
    return null
  }
}

export async function setCanonicalStorageBucket(id: string): Promise<StorageBucket> {
  const res = await requestJson<{ bucket: StorageBucket }>(
    `/v1/storage/buckets/${encodeURIComponent(id)}/accept-canonical`,
    { method: 'POST' }
  )
  return res.bucket
}

export async function listStorageProposals(): Promise<StorageBucket[]> {
  try {
    const res = await requestJson<{ proposals: StorageBucket[]; count: number }>(
      '/v1/storage/proposals'
    )
    return res.proposals || []
  } catch (err) {
    console.warn('[storage] failed to list proposals', err)
    return []
  }
}

export async function rejectStorageProposal(id: string): Promise<void> {
  await requestJson(`/v1/storage/buckets/${encodeURIComponent(id)}/reject`, {
    method: 'POST',
  })
}
