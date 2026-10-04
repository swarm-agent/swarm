import type { RunningTask } from './orchestrate-types'

// The server stages large batches; confirm the complete persisted count rather
// than an individual provider chunk. Non-media approval is unaffected.
export function confirmMediaBatch(task: RunningTask, confirm: (message: string) => boolean): boolean {
  const count = Math.max(task.variantCount || 0, task.deliverables?.length || 0)
  return !['image', 'video', 'sound', 'audio'].includes(task.agentType) || count < 25 ||
    confirm(`Generate all ${count} media iterations? Cancel starts no generation.`)
}
