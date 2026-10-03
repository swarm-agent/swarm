import { requestJson } from '../../../app/api'
import { ArchiveQueue } from './archive-queue'

// Project tasks have an independent four-operation lane; design/session work
// cannot occupy it. Queue reservations include work not yet started.
export const projectTaskArchiveQueue = new ArchiveQueue()
export type TaskArchiveReceipt = { id: string; revision: number; archived: boolean }

export async function archiveProjectTask(
  projectId: string, row: { id: string; revision?: number }, current: () => boolean,
  request: typeof requestJson = requestJson,
): Promise<TaskArchiveReceipt | undefined> {
  if (!current()) throw new Error('Project or account changed')
  if (!Number.isSafeInteger(row.revision) || (row.revision ?? 0) <= 0) throw new Error('Missing task revision; refresh and retry')
  const response = await request<{ task: TaskArchiveReceipt }>(
    `/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(row.id)}/archive`,
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision: row.revision }) },
  )
  if (!current()) return undefined
  const receipt = response?.task
  if (!receipt || receipt.id !== row.id || receipt.archived !== true ||
    !Number.isSafeInteger(receipt.revision) || receipt.revision <= row.revision!) {
    throw new Error('Invalid task archive receipt; refresh and retry')
  }
  return receipt
}
