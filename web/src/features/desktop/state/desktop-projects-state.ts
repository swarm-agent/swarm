import type { RunningTask, ProjectTaskMediaRef, ProjectTaskPlanBinding } from '../orchestrate/orchestrate-types'

export interface DesktopProjectState {
  projectId: string
  environmentWorkspaceCatalog?: Array<{ workspaceId?: string; path: string }>
  tasks: RunningTask[]
  archivedRevisions?: Record<string, number>
  // Detail-read provenance, owned by the canonical cache (never the collection).
  gitObservations?: Record<string, string>
  media: ProjectTaskMediaRef[]
  mediaLoading?: boolean
  mediaError?: string
  mediaRequestId?: string
  loading: boolean
  stale: boolean
  error?: string
  generation: number
  requestId?: string
  lastObservedAt?: number
}

export type DesktopProjectsState = Record<string, DesktopProjectState>

export type DesktopProjectsAction =
  | { type: 'projects.environmentCatalog'; projectId: string; workspaces: Array<{ workspaceId?: string; path: string }> }
  | { type: 'projects.beginLoad'; projectId: string; requestId: string }
  | {
      type: 'projects.loadSuccess'
      projectId: string
      requestId: string
      generation: number
      tasks: RunningTask[]
      media?: ProjectTaskMediaRef[]
    }
  | { type: 'projects.mediaResult'; projectId: string; requestId: string; generation: number; media?: ProjectTaskMediaRef[]; error?: string }
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
      archivedReceipt?: { id: string; revision: number }
      inspectedTaskId?: string
    }
  | {
      type: 'projects.updateMedia'
      projectId: string
      media: ProjectTaskMediaRef[] | ((prev: ProjectTaskMediaRef[]) => ProjectTaskMediaRef[])
    }
  | { type: 'projects.invalidateGit'; projectId: string; taskId?: string }
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

// Revision plus execution/repository identity bounds reuse of a detail observation.
// Git HEAD changes arrive through durable invalidations, not collection freshness.
export function taskGitIdentity(task: RunningTask): string {
  return JSON.stringify([task.id, task.revision, task.sessionId, task.activeAttemptId,
    ['completed', 'needs_review'].includes(task.status) ? 'review' : task.status,
    task.workspacePath, task.sourceWorkspacePath, task.sourceWorkspaceId,
    task.sourceWorkspaceGeneration, task.sourceWorkspaceProvenance,
    task.worktreeBranch, task.baseBranch, task.baseCommit, task.integration, task.taskProgramStatus,
    task.boardSummary?.program,
    task.attempts?.map(attempt => [attempt.id, attempt.session_id, attempt.integration])])
}

// A late list/read response must not undo a newer durable task revision.
function retainNewerTasks(incoming: RunningTask[], previous: RunningTask[]): RunningTask[] {
  const byId = new Map(previous.map(task => [task.id, task]))
  return incoming.map(task => {
    const prior = byId.get(task.id)
    if (prior && (prior.revision ?? 0) > (task.revision ?? 0)) return prior
    // Only reuse detail for the exact task/plan identity. Summary membership and
    // lifecycle stay fresh even when the omitted definition is retained.
    if (!prior?.detailLoaded || !task.boardSummary || prior.revision !== task.revision ||
      prior.sessionId !== task.sessionId || prior.activeAttemptId !== task.activeAttemptId ||
      JSON.stringify(prior.planBinding) !== JSON.stringify(task.planBinding) || task.boardSummary.plan_binding_stale ||
      (task.status === 'pending_approval' && task.boardSummary.plan &&
        task.boardSummary.plan.version !== task.planBinding?.definitionRevision)) return task
    return { ...task, detailLoaded: true,
      fullPlanMarkdown: prior.fullPlanMarkdown, planDocument: prior.planDocument, plan_document: prior.plan_document,
      taskProgram: prior.taskProgram, task_program: prior.task_program,
      taskProgramStatus: prior.taskProgramStatus, task_program_status: prior.task_program_status,
      attempts: prior.attempts, scenes: prior.scenes, soundtrack: prior.soundtrack,
      attachedMedia: prior.attachedMedia, feedbackHistory: prior.feedbackHistory,
      deliverables: task.deliverables?.map(deliverable => {
        const detail = prior.deliverables?.find(item => item.id === deliverable.id)
        return detail ? { ...deliverable, prompt: detail.prompt } : deliverable
      }) }
  })
}

export function mapBackendTask(t: any): RunningTask {
  return {
    id: t.id,
    boardSummary: t.board_summary,
    detailLoaded: t.board_summary === undefined,
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
    baseCommit: t.base_commit,
    deliveryAssessment: t.delivery_assessment,
    unintegratedCommits: t.delivery_assessment?.candidate_commits ?? t.unintegrated_commits ?? 0,
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
    revision: t.revision,
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
    activeAttemptId: t.active_attempt_id,
    environmentAttachments: Array.isArray(t.environment_attachments) ? t.environment_attachments : [],
    environmentsStale: false,
    attempts: t.attempts,
    integration: t.integration,
    handoffSummary: t.status === 'needs_review' && t.active_attempt_id
      ? t.attempts?.find((attempt: any) => attempt.id === t.active_attempt_id && attempt.session_id === t.session_id)?.summary || 'No ready summary recorded for this attempt. Validation and integration remain unverified.'
      : undefined,
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
      prompt: t.image_prompts?.[(t.deliverables || []).indexOf(d)] || t.description || t.title,
      description: d.description,
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
    subtasks: [],
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
  if (action.type === 'projects.environmentCatalog') {
    if (!previous) return state
    return { ...state, [action.projectId]: { ...previous, environmentWorkspaceCatalog: action.workspaces } }
  }
  if (action.type === 'projects.beginLoad') {
    return {
      ...state,
      [action.projectId]: {
        projectId: action.projectId,
        environmentWorkspaceCatalog: previous?.environmentWorkspaceCatalog,
        tasks: previous?.tasks ?? [],
        lastObservedAt: previous?.lastObservedAt,
        media: previous?.media ?? [],
        gitObservations: previous?.gitObservations,
        archivedRevisions: previous?.archivedRevisions,
        mediaLoading: true,
        mediaError: undefined,
        mediaRequestId: action.requestId,
        generation: previous?.generation ?? 0,
        requestId: action.requestId,
        loading: true,
        stale: previous?.stale ?? true,
        error: undefined,
      },
    }
  }
  if (action.type === 'projects.invalidateGit') {
    if (!previous) return state
    const gitObservations = { ...previous.gitObservations }
    const tasks = previous.tasks.map(task => {
      if (action.taskId && task.id !== action.taskId) return task
      delete gitObservations[task.id]
      return { ...task, gitStatus: 'stale' as const }
    })
    return { ...state, [action.projectId]: { ...previous, tasks, gitObservations } }
  }
  if (action.type === 'projects.updateTasks') {
    if (!previous) return state
    const newTasks = typeof action.tasks === 'function' ? action.tasks(previous.tasks) : action.tasks
    const archivedRevisions = { ...previous.archivedRevisions }
    if (action.archivedReceipt) archivedRevisions[action.archivedReceipt.id] = Math.max(archivedRevisions[action.archivedReceipt.id] ?? 0, action.archivedReceipt.revision)
    const tasks = retainNewerTasks(newTasks, previous.tasks).filter(task => (task.revision ?? 0) > (archivedRevisions[task.id] ?? -1))
    const identities = new Map(tasks.map(task => [task.id, taskGitIdentity(task)]))
    const gitObservations = Object.fromEntries(Object.entries(previous.gitObservations ?? {}).filter(([id, identity]) => identities.get(id) === identity))
    const inspected = action.inspectedTaskId && tasks.find(task => task.id === action.inspectedTaskId)
    if (inspected) gitObservations[inspected.id] = taskGitIdentity(inspected)
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        tasks,
        archivedRevisions,
        gitObservations,
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
        mediaRequestId: undefined,
        mediaLoading: false,
        mediaError: undefined,
      },
    }
  }
  if (action.type === 'projects.mediaResult') {
    if (!previous || previous.mediaRequestId !== action.requestId) return state
    const current = previous.generation === action.generation
    return { ...state, [action.projectId]: { ...previous,
      media: current && action.media ? action.media : previous.media,
      mediaError: current ? action.error : previous.mediaError,
      mediaLoading: false, mediaRequestId: undefined,
    } }
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
    const incoming = retainNewerTasks(action.tasks, previous.tasks).filter(task => (task.revision ?? 0) > (previous.archivedRevisions?.[task.id] ?? -1))
    const priorById = new Map(previous.tasks.map(task => [task.id, task]))
    const identities = new Map(incoming.map(task => [task.id, taskGitIdentity(task)]))
    return {
      ...state,
      [action.projectId]: {
        ...previous,
        tasks: incoming.map(task => {
          const prior = priorById.get(task.id)
          if (!prior || previous.gitObservations?.[task.id] !== taskGitIdentity(task)) return task
          // Collection GET deliberately does not inspect Git. Preserve only the
          // inspected projection, while accepting all other live task fields.
          return { ...task, status: prior.status, gitStatus: prior.gitStatus, isIntegrated: prior.isIntegrated,
            deliveryAssessment: prior.deliveryAssessment,
            unintegratedCommits: prior.unintegratedCommits, behindCommits: prior.behindCommits,
            isDirty: prior.isDirty, dirtyCount: prior.dirtyCount, diffSummary: prior.diffSummary,
            syncWarning: prior.syncWarning, actionNeeded: prior.actionNeeded }
        }),
        gitObservations: Object.fromEntries(Object.entries(previous.gitObservations ?? {}).filter(([id, identity]) =>
          identities.get(id) === identity)),
        media: action.media ?? previous.media,
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
