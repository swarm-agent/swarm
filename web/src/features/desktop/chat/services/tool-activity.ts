import { describeToolActivity, type ToolActivitySemanticKind } from './tool-message'
import type { StructuredToolMessage, ToolMessageState } from '../types/chat'

export type ToolActivityDisplayState = 'running' | 'done' | 'error' | 'cancelled'

export interface ToolActivityPresentation {
  kind: ToolActivitySemanticKind
  state: ToolActivityDisplayState
  title: string
  statusLabel: string
  announcement: string
}

function isCancelledStatus(status: string): boolean {
  const normalized = status.trim().toLowerCase()
  return normalized === 'cancelled' || normalized === 'canceled' || normalized === 'interrupted'
}

export function resolveToolActivityDisplayState(
  state: ToolMessageState,
  lifecycleStatus = '',
): ToolActivityDisplayState {
  if (isCancelledStatus(lifecycleStatus)) return 'cancelled'
  if (state === 'running') return 'running'
  if (state === 'error') return 'error'
  return 'done'
}

export function toolActivityPresentation(
  toolName: string,
  state: ToolMessageState,
  lifecycleStatus = '',
): ToolActivityPresentation {
  const descriptor = describeToolActivity(toolName)
  const displayState = resolveToolActivityDisplayState(state, lifecycleStatus)
  if (displayState === 'running') {
    const title = `${descriptor.activeLabel}…`
    return { kind: descriptor.kind, state: displayState, title, statusLabel: 'Active', announcement: title }
  }
  if (displayState === 'error') {
    const title = `${descriptor.label} failed`
    return { kind: descriptor.kind, state: displayState, title, statusLabel: 'Failed', announcement: title }
  }
  if (displayState === 'cancelled') {
    const title = `${descriptor.label} cancelled`
    return { kind: descriptor.kind, state: displayState, title, statusLabel: 'Cancelled', announcement: title }
  }
  const title = descriptor.kind === 'task' ? 'Subagents launched' : `${descriptor.label} complete`
  return { kind: descriptor.kind, state: displayState, title, statusLabel: 'Done', announcement: title }
}

function jsonString(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function jsonNumber(record: Record<string, unknown> | null | undefined, key: string): number {
  const value = record?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

export function toolActivityStartSummary(message: StructuredToolMessage): string {
  const args = message.argumentsJson
  switch (message.tool.trim().toLowerCase().replace(/-/g, '_')) {
    case 'edit':
    case 'write':
      return message.target || jsonString(args, 'path')
    case 'plan':
    case 'plan_manage':
    case 'exit_plan_mode': {
      const title = jsonString(args, 'title') || jsonString(args, 'checkpoint_title')
      const action = jsonString(args, 'action').replace(/_/g, ' ')
      return title || action
    }
    case 'task':
    case 'subagent':
    case 'launch_subagent': {
      const description = jsonString(args, 'description') || jsonString(args, 'goal') || jsonString(args, 'title')
      const launchCount = jsonNumber(args, 'launch_count')
      return description || (launchCount > 0 ? `${launchCount} ${launchCount === 1 ? 'subagent' : 'subagents'}` : '')
    }
    case 'manage_video': {
      const action = jsonString(args, 'action').toLowerCase()
      const title = jsonString(args, 'title')
      const preset = jsonString(args, 'output_preset').replace(/_/g, ' ')
      const sourceNames = Array.isArray(args?.source_names)
        ? args.source_names.filter((value): value is string => typeof value === 'string' && Boolean(value.trim())).map((value) => value.trim())
        : []
      const sourceLabel = sourceNames.length === 1 ? sourceNames[0] : sourceNames.length > 1 ? `${sourceNames.length} source videos` : ''
      const subject = title || sourceLabel || preset
      const activity: Record<string, string> = {
        list_source_roots: 'Finding available video sources',
        browse_source: 'Browsing video sources',
        inspect_attachments: 'Checking attached videos',
        start_transcription: 'Starting video transcription',
        status: 'Checking transcription progress',
        cancel: 'Cancelling video transcription',
        read_transcript: 'Reading video transcript',
        create_project: 'Setting up a video project',
        read_project: 'Loading the video project',
        get_project: 'Loading the video project',
        list_projects: 'Loading video projects',
        create_revision: 'Saving a new video version',
        restore_revision: 'Restoring a video version',
        start_render: 'Starting the video render',
        render_status: 'Checking render progress',
        cancel_render: 'Cancelling the video render',
      }
      const label = activity[action] || 'Working on video'
      return subject ? `${label} · ${subject}` : label
    }
    case 'manage_worktree': {
      const action = jsonString(args, 'action') || 'inspect'
      const taskCallId = jsonString(args, 'task_call_id')
      const sessionIds = Array.isArray(args?.session_ids)
        ? args.session_ids.filter((value): value is string => typeof value === 'string' && Boolean(value.trim()))
        : []
      if (taskCallId) return `${action.replace(/_/g, ' ')} · ${taskCallId}`
      if (sessionIds.length > 0) return `${action.replace(/_/g, ' ')} · ${sessionIds.length} ${sessionIds.length === 1 ? 'session' : 'sessions'}`
      return action.replace(/_/g, ' ')
    }
    default:
      return message.target || message.commandText
  }
}
