import { requestJson } from '../../../app/api'

export type TaskUnarchiveReceipt = { id: string; revision: number; archived: false; status: string; [key: string]: unknown }

export async function unarchiveProjectTask(
  projectId: string, row: { id: string; revision?: number }, current: () => boolean,
  request: typeof requestJson = requestJson,
): Promise<TaskUnarchiveReceipt | undefined> {
  if (!current()) throw new Error('Project or account changed')
  if (!Number.isSafeInteger(row.revision) || (row.revision ?? 0) <= 0) throw new Error('Missing task revision; refresh and retry')
  const response = await request<{ task: TaskUnarchiveReceipt }>(
    `/v3/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(row.id)}/unarchive`,
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision: row.revision }) },
  )
  if (!current()) return undefined
  const receipt = response?.task
  if (!receipt || receipt.id !== row.id || receipt.archived !== false || !receipt.status ||
    !Number.isSafeInteger(receipt.revision) || receipt.revision !== row.revision! + 1) {
    throw new Error('Invalid task unarchive receipt; refresh and retry')
  }
  return receipt
}
