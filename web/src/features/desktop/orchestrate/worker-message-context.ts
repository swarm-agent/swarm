import type { SelectedWorker } from './worker-hub'

/** Client forwards only the immutable reference; the server authorizes and resolves all worker facts. */
export function selectedWorkerMetadata(selection: SelectedWorker | null): { selected_worker?: { worker_id: string; expected_revision: number } } {
  if (!selection) return {}
  if (!selection.id || !Number.isSafeInteger(selection.revision) || selection.revision < 1) throw new Error('Selected worker reference is invalid')
  return { selected_worker: { worker_id: selection.id, expected_revision: selection.revision } }
}

/** No optimistic consumption: a failed append retains selection, and an in-flight replacement survives. */
export async function submitWithWorkerSelection<T>(selection: SelectedWorker | null, submit: (metadata: ReturnType<typeof selectedWorkerMetadata>) => Promise<T>, current: () => SelectedWorker | null, clear: () => void): Promise<T> {
  const result = await submit(selectedWorkerMetadata(selection))
  if (selection && current() === selection) clear()
  return result
}
