import type { StructuredToolMessage } from '../types/chat'
import { manageProjectsAnswer, manageProjectsLinked } from './manage-projects-answer'

// The media job a finished chat tool call started, so the transcript can show
// that job as a card under the call. The job itself (status, files) is never
// read from the transcript: it is the project's media task, the one record the
// Orchestrate studio and the Media library show too.

export interface ManageMediaJobRef {
  projectId: string
  taskId: string
}

export type ManageMediaJobState = 'generating' | 'ready' | 'failed'

const MEDIA_AGENTS = new Set(['image', 'video', 'sound', 'audio'])

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

function text(value: Record<string, unknown> | null | undefined, key: string): string {
  const v = value?.[key]
  return typeof v === 'string' ? v.trim() : ''
}

function parsed(raw: string): Record<string, unknown> | null {
  if (!raw.trim()) return null
  try {
    return record(JSON.parse(raw))
  } catch {
    return null
  }
}

/**
 * manage_media generate or retry returns `job_id` media/PROJECT/TASK;
 * manage_projects create_task or propose_task for the image, video or sound
 * agent returns its task id. Anything else, an unfinished or failed call, or a
 * linked machine's call (its task is not in this Swarm's project) has none.
 */
export function manageMediaJobRef(message: Pick<StructuredToolMessage, 'tool' | 'state' | 'argumentsJson' | 'argumentsText' | 'outputJson' | 'output' | 'completedOutput'>): ManageMediaJobRef | null {
  if (message.state !== 'done') return null
  const tool = message.tool.trim().toLowerCase().replace(/-/g, '_')
  const args = message.argumentsJson ?? parsed(message.argumentsText)
  const out = message.outputJson ?? parsed(message.output) ?? parsed(message.completedOutput)
  if (!out || text(args, 'machine')) return null
  const action = text(args, 'action')
  if (tool === 'manage_media' && (action === 'generate' || action === 'retry')) {
    // The catalog envelope's data is the answer (an older message held it at
    // the top level); a linked call is told by its machine argument.
    if (manageProjectsLinked(args, out)) return null
    const parts = text(manageProjectsAnswer(out).data, 'job_id').split('/')
    if (parts.length !== 3 || parts[0] !== 'media' || !parts[1] || !parts[2]) return null
    return { projectId: parts[1], taskId: parts[2] }
  }
  if (tool === 'manage_projects' && (action === 'create_task' || action === 'propose_task')) {
    // The envelope's data is the answer; an older message held it at the
    // top level. The envelope always names a machine (this one's own name),
    // so a linked call is told by its machine argument.
    if (manageProjectsLinked(args, out)) return null
    const answer = manageProjectsAnswer(out).data
    const task = record(answer?.task)
    const proposal = record(answer?.proposal)
    const agent = text(args, 'agent') || text(answer, 'agent') || text(task, 'agent') || text(proposal, 'agent')
    if (!MEDIA_AGENTS.has(agent)) return null
    const projectId = text(args, 'project_id') || text(answer, 'project_id') || text(task, 'project_id') || text(proposal, 'project_id')
    const taskId = text(answer, 'task_id') || text(task, 'id') || text(proposal, 'task_id')
    return projectId && taskId ? { projectId, taskId } : null
  }
  return null
}

/** Ready once any file is; failed when the task ended without one; else generating (also before the task is read). */
export function manageMediaJobState(task?: { status: string; deliverables?: ReadonlyArray<{ status: string }> } | null): ManageMediaJobState {
  if (!task) return 'generating'
  if ((task.deliverables ?? []).some(output => output.status === 'ready' || output.status === 'accepted')) return 'ready'
  if (['failed', 'rejected', 'cancelled', 'canceled', 'blocked', 'completed', 'needs_review', 'integrated'].includes(task.status)) return 'failed'
  return 'generating'
}
