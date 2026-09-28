import type { RunningTask, ProjectTaskMediaRef, ProjectTaskPlanBinding } from '../orchestrate/orchestrate-types'

export interface DesktopProjectState {
  projectId: string
  tasks: RunningTask[]
  media: ProjectTaskMediaRef[]
  loading: boolean
  stale: boolean
  error?: string
  generation: number
  requestId?: string
  lastObservedAt?: number
}

export type DesktopProjectsState = Record<string, DesktopProjectState>

export type DesktopProjectsAction =
  | { type: 'projects.beginLoad'; projectId: string; requestId: string }
  | {
      type: 'projects.loadSuccess'
      projectId: string
      requestId: string
      generation: number
      tasks: RunningTask[]
      media: ProjectTaskMediaRef[]
    }
  | {
      type: 'projects.loadError'
      projectId: string
      requestId: string
      generation: number
      error: string
    }
  | {
      type: 'projects.updateTasks'
      projectId: string
      tasks: RunningTask[] | ((prev: RunningTask[]) => RunningTask[])
    }
  | {
      type: 'projects.updateMedia'
      projectId: string
      media: ProjectTaskMediaRef[] | ((prev: ProjectTaskMediaRef[]) => ProjectTaskMediaRef[])
    }
  | { type: 'projects.invalidate'; projectId?: string }
  | { type: 'projects.evict'; projectId: string }

export function normalizePlanBinding(raw: any): ProjectTaskPlanBinding | undefined {
  if (!raw || typeof raw !== 'object') return undefined
  const planId = raw.planId ?? raw.plan_id
  const definitionRevision =
    typeof raw.definitionRevision === 'number'
      ? raw.definitionRevision
      : typeof raw.definition_revision === 'number'
        ? raw.definition_revision
        : undefined
  const sessionId = raw.sessionId ?? raw.session_id
  const receipt = raw.receipt
  if (!planId && definitionRevision === undefined && !sessionId && !receipt) {
    return undefined
  }
  return {
    planId: planId ? String(planId).trim() : undefined,
    definitionRevision,
    sessionId: sessionId ? String(sessionId).trim() : undefined,
    receipt: receipt ? String(receipt).trim() : undefined,
    plan_id: raw.plan_id ? String(raw.plan_id).trim() : (planId ? String(planId).trim() : undefined),
    definition_revision: typeof raw.definition_revision === 'number' ? raw.definition_revision : definitionRevision,
    session_id: raw.session_id ? String(raw.session_id).trim() : (sessionId ? String(sessionId).trim() : undefined),
  }
}

export function mapBackendTask(t: any): RunningTask {
  return {
    id: t.id,
    title: t.title,
    subtitle: t.description || `Autonomous execution unit for ${t.agent || 'coder'}`,
    agentType: (t.agent === 'designer' || t.agent === 'finder' || t.agent === 'video' || t.agent === 'swarm' || t.agent === 'image' || t.agent === 'plan' || t.agent === 'sound' || t.agent === 'audio' ? t.agent : 'coder') as any,
    status: (t.status === 'in_progress' ? 'running' : t.status) || 'queued',
    outcomeType: t.outcome_type,
    workspaceTarget: t.source_workspace?.path || t.workspace_path || t.project_id,
    workspacePath: t.workspace_path,
    sourceWorkspacePath: t.source_workspace?.path,
    sourceWorkspaceId: t.source_workspace?.workspace_id,
    sourceWorkspaceGeneration: t.source_workspace?.workspace_generation,
    sourceWorkspaceProvenance: t.source_workspace?.provenance,
    worktreeBranch: t.worktree_branch,
    worktreeName: t.worktree_name || (t.worktree_branch ? t.worktree_branch.replace(/^agent\//, '').replace(/^worktree\//, '') : undefined),
    baseBranch: t.base_branch || 'main',
    unintegratedCommits: t.unintegrated_commits ?? 0,
    behindCommits: t.behind_commits ?? 0,
    gitStatus: t.git_status,
    isIntegrated: !!t.is_integrated,
    diffSummary: t.diff_summary ?? '',
    isDirty: !!t.is_dirty,
    dirtyCount: t.dirty_count ?? 0,
    syncWarning: t.sync_warning,
    actionNeeded: t.action_needed,
    whatDidDo: t.what_did_do,
    whatNotDone: t.what_not_done,
    workspacesInvolved: t.workspaces_involved || (t.workspace_path ? [t.workspace_path] : []),
    planSummary: t.plan_summary,
    fullPlanMarkdown: t.full_plan_markdown,
    tier: t.tier || 'direct',
    revision: t.revision || 1,
    lastError: t.last_error,
    feedbackHistory: t.feedback_history,
    aspectRatio: t.aspect_ratio,
    resolution: t.resolution,
    model: t.model,
    featureSize: t.feature_size || t.featureSize,
    feature_size: t.feature_size || t.featureSize,
    durationSeconds: t.duration_seconds,
    description: t.description,
    variantCount: t.variant_count,
    scenes: t.scenes,
    soundtrack: t.soundtrack,
    autoApprove: t.auto_approve,
    routerAlert: t.router_alert,
    elapsed: t.created_at ? `${Math.max(1, Math.round((Date.now() - t.created_at) / 60000))}m` : 'Just now',
    workerId: t.worker_id || t.workerId || undefined,
    worker_id: t.worker_id || t.workerId || undefined,
    workerRunId: t.worker_run_id || t.workerRunId || undefined,
    worker_run_id: t.worker_run_id || t.workerRunId || undefined,
    automationId: t.automation_id || t.automationId || undefined,
    automation_id: t.automation_id || t.automationId || undefined,
    workerName: t.worker_name || t.workerName || `@${t.agent || 'Coder'} Worker`,
    worker_name: t.worker_name || t.workerName || undefined,
    priority: 'high',
    sessionId: t.session_id,
    createdAt: t.created_at,
    stageIndex: t.current_stage_index,
    totalStages: t.pipeline_stages?.length || 4,
    stepTimeline: (t.pipeline_stages || ['Inspect', 'Implement', 'Verify', 'Review']).map((st: string, idx: number) => ({
      step: idx + 1,
      label: st,
      status: idx < (t.current_stage_index || 0) ? 'complete' : (idx === t.current_stage_index ? 'processing' : 'pending'),
    })),
    deliverables: (t.deliverables || []).map((d: any) => ({
      id: d.id,
      title: d.title,
      type: d.kind || (t.agent === 'coder' || t.outcome_type === 'code_pr' || t.outcome_type === 'bug_patch' ? 'code' : t.agent === 'finder' || t.outcome_type === 'audit_report' ? 'report' : t.agent === 'video' ? 'video' : t.agent === 'audio' || t.agent === 'sound' ? 'audio' : 'image'),
      status: d.status || 'pending',
      duration: d.duration || '0:15',
      previewUrl: d.media_url || d.preview_url || (d.thumbnail && (d.thumbnail.startsWith('data:') || d.thumbnail.startsWith('http') || d.thumbnail.startsWith('/')) ? d.thumbnail : undefined),
      mediaUrl: d.media_url,
      thumbnailType: (d.thumbnail || (t.agent === 'coder' || t.outcome_type === 'code_pr' || t.outcome_type === 'bug_patch' ? 'default' : 'cyber_lattice')) as any,
      videoAspect: d.aspect_ratio || d.aspectRatio || undefined,
      prompt: t.subtitle || t.title,
      model: d.model || undefined,
      aspectRatio: d.aspect_ratio || d.aspectRatio || undefined,
      resolution: d.resolution || undefined,
      durationSeconds: d.duration_seconds || d.durationSeconds || undefined,
      videoProvenance: d.video_provenance || d.videoProvenance,
      createdAt: d.created_at ? (isNaN(new Date(d.created_at).getTime()) ? new Date().toISOString() : new Date(d.created_at).toISOString()) : (d.createdAt || new Date().toISOString()),
      author: t.worker_name || 'Orchestrator',
      parentDeliverableId: d.parent_deliverable_id || d.parentDeliverableId,
      sourceMediaRef: d.source_media_ref || d.sourceMediaRef,
    })),
    attachedMedia: t.attached_media,
    taskProgram: t.task_program || t.taskProgram,
    task_program: t.task_program || t.taskProgram,
    taskProgramId: t.task_program_id || t.taskProgramId,
    task_program_id: t.task_program_id || t.taskProgramId,
    taskProgramStatus: t.task_program_status || t.taskProgramStatus,
    task_program_status: t.task_program_status || t.taskProgramStatus,
    planBinding: normalizePlanBinding(t.plan_binding || t.planBinding),
    plan_binding: t.plan_binding || t.planBinding,
    planDocument: typeof (t.plan_document || t.planDocument || t.document) === 'string'
      ? (() => { try { return JSON.parse(t.plan_document || t.planDocument || t.document) } catch { return t.plan_document || t.planDocument || t.document } })()
      : (t.plan_document || t.planDocument || t.document),
    plan_document: typeof (t.plan_document || t.planDocument || t.document) === 'string'
      ? (() => { try { return JSON.parse(t.plan_document || t.planDocument || t.document) } catch { return t.plan_document || t.planDocument || t.document } })()
      : (t.plan_document || t.planDocument || t.document),
    subtasks: [
      { id: '1', title: 'Verify task scope', completed: true },
      { id: '2', title: 'Execute implementation', completed: t.status === 'completed' || t.status === 'needs_review' },
    ],
  }
}

export function mapBackendTasks(tasks: any[]): RunningTask[] {
  return (tasks || []).map(mapBackendTask)
}

export function reduceDesktopProjectsState(
  state: DesktopProjectsState = {},
  action: DesktopProjectsAction
): DesktopProjectsState {
  if (action.type === 'projects.invalidate') {
    if (Object.keys(state).length === 0) return state
    let changed = false
    const next: DesktopProjectsState = {}
    for (const [id, proj] of Object.entries(state)) {
      if (!action.projectId || proj.projectId === action.projectId) {
        changed = true
        next[id] = { ...proj, generation: proj.generation + 1, stale: true }
      } else {
        next[id] = proj
      }
    }
    return changed ? next : state
  }
  if (action.type === 'projects.evict') {
    if (!state[action.projectId]) return state
    const next = { ...state }
    delete next[action.projectId]
    return next
  }
  const previous = state[action.projectId]
  if (action.type === 'projects.beginLoad') {
    return {
      ...state,
      [action.projectId]: {
        projectId: action.projectId,
        tasks: previous?.tasks ?? [],
        media: previous?.media ?? [],
        generation: previous?.generation ?? 0,
        requestId: action.requestId,
        loading: true,
        stale: previous?.stale ?? true,
        error: undefined,
      },
    }
  }
  if (action.type === 'projects.updateTasks') {
    if (!previous) return state
    const newTasks = typeof action.tasks === 'function' ? action.tasks(previous.tasks) : action.tasks
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        tasks: newTasks,
      },
    }
  }
  if (action.type === 'projects.updateMedia') {
    if (!previous) return state
    const newMedia = typeof action.media === 'function' ? action.media(previous.media) : action.media
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        media: newMedia,
      },
    }
  }
  if (!previous || previous.requestId !== action.requestId) return state
  if (previous.generation !== action.generation) {
    // Newer invalidation occurred while request was in-flight; keep stale
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        loading: false,
        requestId: undefined,
      },
    }
  }
  if (action.type === 'projects.loadSuccess') {
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        tasks: action.tasks,
        media: action.media,
        loading: false,
        stale: false,
        error: undefined,
        requestId: undefined,
        lastObservedAt: Date.now(),
      },
    }
  }
  if (action.type === 'projects.loadError') {
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        loading: false,
        stale: true,
        error: action.error,
        requestId: undefined,
      },
    }
  }
  return state
}
