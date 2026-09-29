import type {
  RunningTask,
  RunningTaskPlanCheckpoint,
  TaskSessionStateItem,
  TaskSessionSummary,
  MediaDeliverable,
  TaskOutcomeType,
  BackendTaskModelPreview,
  ProjectTaskPlanBinding,
  ProjectSummary,
} from './orchestrate-types'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'
import { selectTaskPlanDocument } from './orchestrate-plan-authority'
import { extractTaskSessionIds, type TaskSessionCandidate } from '../runtime/desktop-projects-membership'
import { summarizeDesktopV3TaskToolActivity } from '../state/desktop-v3-cache-selectors'

export interface ImpendingAgentView {
  agent: string
  count: number
  label: string
  model: string
  isOverride: boolean
  provider?: string
  thinking?: string
  serviceTier?: string
  contextMode?: string
  previewFailed?: boolean
  error?: string
}

export interface DeployImpendingConfig {
  targetAgent: 'coder' | 'plan' | 'finder' | 'image' | 'video' | 'sound'
  targetOutcomeType: TaskOutcomeType
  targetTier: 'direct' | 'discovery' | 'complex'
  resolvedModel: string
  isOverridden: boolean
  accountDefaultModel: string
  provider?: string
  thinking?: string
  serviceTier?: string
  contextMode?: string
  previewFailed?: boolean
  error?: string
}

export function getPrimarySystemAgentName(agentType: string): string {
  const norm = (agentType || '').trim().toLowerCase()
  if (norm === 'coder') return 'system-coder'
  if (norm === 'finder') return 'system-finder'
  if (norm === 'designer' || norm === 'image') return 'system-designer'
  if (norm === 'plan' || norm === 'swarm') return 'swarm'
  return 'system-coder'
}

export function isMediaTaskType(task: Pick<RunningTask, 'agentType' | 'outcomeType'>): boolean {
  return (
    task.agentType === 'image' ||
    task.agentType === 'video' ||
    task.agentType === 'sound' ||
    task.agentType === 'audio' ||
    task.outcomeType === 'media_bundle' ||
    task.outcomeType === 'video_story' ||
    task.outcomeType === 'video_clip'
  )
}

export function resolveTaskImpendingAgents(
  task: Pick<RunningTask, 'agentType' | 'model' | 'outcomeType'>,
  programJobs: Array<{ agent_type?: string; id?: string; job_id?: string }> = [],
  findProgramJobDef?: (jobId: string) => { agent_type?: string } | undefined,
  agentModelSettings?: AgentModelSettings | null,
  defaultMediaModels?: { image?: string; video?: string; audio?: string },
  backendModelPreview?: BackendTaskModelPreview | null,
  previewError?: string | null
): ImpendingAgentView[] {
  // If preview failed with an error, do NOT guess a label! Fail visibly.
  if (previewError && previewError.trim()) {
    const agent = task.agentType || 'coder'
    return [
      {
        agent,
        count: 1,
        label: `@${agent}`,
        model: 'Preview unavailable',
        isOverride: false,
        previewFailed: true,
        error: previewError.trim(),
      },
    ]
  }

  const isOverride = Boolean(
    (backendModelPreview && backendModelPreview.model_source === 'task_override') ||
    (task.model && task.model.trim())
  )

  if (programJobs.length > 0) {
    const agentCounts: Record<string, number> = {}
    programJobs.forEach((j) => {
      const def = findProgramJobDef ? findProgramJobDef(j.job_id || j.id || '') : undefined
      const agentType = def?.agent_type || j.agent_type || 'coder'
      agentCounts[agentType] = (agentCounts[agentType] || 0) + 1
    })

    return Object.entries(agentCounts).map(([agentType, count]) => {
      let resolvedModel = ''
      let provider: string | undefined
      let thinking: string | undefined
      let serviceTier: string | undefined
      let contextMode: string | undefined
      // A task-scoped override belongs to its target agent, not every job in a
      // mixed-agent program. Other cohorts keep their own account defaults.
      const cohortHasOverride = isOverride && agentType === (backendModelPreview?.agent || task.agentType)
      let cohortIsOverride = cohortHasOverride

      if (cohortHasOverride && backendModelPreview?.resolved_model?.model) {
        resolvedModel = backendModelPreview.resolved_model.model
        provider = backendModelPreview.resolved_model.provider
        thinking = backendModelPreview.resolved_model.thinking
        serviceTier = backendModelPreview.resolved_model.service_tier
        contextMode = backendModelPreview.resolved_model.context_mode
      } else if (cohortHasOverride && task.model?.trim()) {
        resolvedModel = task.model.trim()
      } else if (backendModelPreview?.resolved_model?.model && agentType === (backendModelPreview.agent || task.agentType)) {
        resolvedModel = backendModelPreview.resolved_model.model
        provider = backendModelPreview.resolved_model.provider
        thinking = backendModelPreview.resolved_model.thinking
        serviceTier = backendModelPreview.resolved_model.service_tier
        contextMode = backendModelPreview.resolved_model.context_mode
        cohortIsOverride = backendModelPreview.model_source === 'task_override'
      } else {
        cohortIsOverride = false
        if (agentType === 'coder') {
          resolvedModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
          provider = agentModelSettings?.systemAgents?.coder?.provider
          thinking = agentModelSettings?.systemAgents?.coder?.thinking
          serviceTier = agentModelSettings?.systemAgents?.coder?.serviceTier
        } else if (agentType === 'finder') {
          resolvedModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
          provider = agentModelSettings?.systemAgents?.finder?.provider
          thinking = agentModelSettings?.systemAgents?.finder?.thinking
          serviceTier = agentModelSettings?.systemAgents?.finder?.serviceTier
        } else if (agentType === 'designer') {
          resolvedModel = agentModelSettings?.systemAgents?.designer?.model || defaultMediaModels?.image || 'Account default'
          provider = agentModelSettings?.systemAgents?.designer?.provider
          thinking = agentModelSettings?.systemAgents?.designer?.thinking
          serviceTier = agentModelSettings?.systemAgents?.designer?.serviceTier
        } else {
          resolvedModel = agentModelSettings?.swarm?.action?.model || 'Account default'
          provider = agentModelSettings?.swarm?.action?.provider
          thinking = agentModelSettings?.swarm?.action?.thinking
          serviceTier = agentModelSettings?.swarm?.action?.serviceTier
        }
      }

      const label =
        count > 1
          ? `${count} ${agentType === 'coder' ? 'Coders' : agentType === 'finder' ? 'Finders' : `${agentType}s`} (@${agentType})`
          : `@${agentType} (${agentType.charAt(0).toUpperCase() + agentType.slice(1)})`

      return {
        agent: agentType,
        count,
        label,
        model: resolvedModel,
        isOverride: cohortIsOverride,
        provider,
        thinking,
        serviceTier,
        contextMode,
      }
    })
  }

  const agent = task.agentType || 'coder'
  let resolvedModel = ''
  let provider: string | undefined
  let thinking: string | undefined
  let serviceTier: string | undefined
  let contextMode: string | undefined

  if (backendModelPreview?.resolved_model?.model) {
    resolvedModel = backendModelPreview.resolved_model.model
    provider = backendModelPreview.resolved_model.provider
    thinking = backendModelPreview.resolved_model.thinking
    serviceTier = backendModelPreview.resolved_model.service_tier
    contextMode = backendModelPreview.resolved_model.context_mode
  } else if (isOverride && task.model?.trim()) {
    resolvedModel = task.model.trim()
  } else {
    if (agent === 'coder') {
      resolvedModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
      provider = agentModelSettings?.systemAgents?.coder?.provider
      thinking = agentModelSettings?.systemAgents?.coder?.thinking
      serviceTier = agentModelSettings?.systemAgents?.coder?.serviceTier
    } else if (agent === 'finder') {
      resolvedModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
      provider = agentModelSettings?.systemAgents?.finder?.provider
      thinking = agentModelSettings?.systemAgents?.finder?.thinking
      serviceTier = agentModelSettings?.systemAgents?.finder?.serviceTier
    } else if (agent === 'designer' || agent === 'image') {
      resolvedModel = defaultMediaModels?.image || agentModelSettings?.systemAgents?.designer?.model || 'Account default'
    } else if (agent === 'video') {
      resolvedModel = defaultMediaModels?.video || 'Account default'
    } else if (agent === 'sound' || agent === 'audio') {
      resolvedModel = defaultMediaModels?.audio || 'Account default'
    } else if (agent === 'plan') {
      resolvedModel = agentModelSettings?.swarm?.plan?.model || agentModelSettings?.swarm?.action?.model || 'Account default'
      provider = agentModelSettings?.swarm?.plan?.provider || agentModelSettings?.swarm?.action?.provider
      thinking = agentModelSettings?.swarm?.plan?.thinking || agentModelSettings?.swarm?.action?.thinking
      serviceTier = agentModelSettings?.swarm?.plan?.serviceTier || agentModelSettings?.swarm?.action?.serviceTier
    } else {
      resolvedModel = agentModelSettings?.swarm?.action?.model || 'Account default'
      provider = agentModelSettings?.swarm?.action?.provider
      thinking = agentModelSettings?.swarm?.action?.thinking
      serviceTier = agentModelSettings?.swarm?.action?.serviceTier
    }
  }

  let label = `@${agent}`
  if (agent === 'coder') label = '@coder (Coder)'
  else if (agent === 'finder') label = '@finder (Finder)'
  else if (agent === 'plan') label = '@plan (Plan Mode)'
  else if (agent === 'image') label = '@image (Designer)'
  else if (agent === 'video') label = '@video (Video)'
  else if (agent === 'sound' || agent === 'audio') label = '@sound (Audio)'

  return [
    {
      agent,
      count: 1,
      label,
      model: resolvedModel,
      isOverride,
      provider,
      thinking,
      serviceTier,
      contextMode,
    },
  ]
}

export function resolveDeployImpendingConfig(
  taskIntent: 'code' | 'image' | 'video' | 'sound' | 'audit',
  featureSize: 'small' | 'big',
  newTaskModelOverride: string,
  agentModelSettings?: AgentModelSettings | null,
  mediaSelections?: {
    selectedImageModel?: string
    defaultImageModel?: string
    selectedVideoModel?: string
    defaultVideoModel?: string
    selectedAudioModel?: string
    defaultAudioModel?: string
  },
  backendModelPreview?: BackendTaskModelPreview | null,
  previewError?: string | null
): DeployImpendingConfig {
  let targetAgent: 'coder' | 'plan' | 'finder' | 'image' | 'video' | 'sound' = 'coder'
  let targetOutcomeType: TaskOutcomeType = 'code_pr'
  let targetTier: 'direct' | 'discovery' | 'complex' = 'direct'
  let accountDefaultModel = 'Account default'
  let provider: string | undefined
  let thinking: string | undefined
  let serviceTier: string | undefined
  let contextMode: string | undefined

  if (taskIntent === 'code') {
    if (featureSize === 'big') {
      targetAgent = 'plan'
      targetOutcomeType = 'plan_spec'
      targetTier = 'complex'
      accountDefaultModel =
        agentModelSettings?.swarm?.plan?.model ||
        agentModelSettings?.swarm?.action?.model ||
        'Account default'
      provider = agentModelSettings?.swarm?.plan?.provider || agentModelSettings?.swarm?.action?.provider
      thinking = agentModelSettings?.swarm?.plan?.thinking || agentModelSettings?.swarm?.action?.thinking
      serviceTier = agentModelSettings?.swarm?.plan?.serviceTier || agentModelSettings?.swarm?.action?.serviceTier
    } else {
      targetAgent = 'coder'
      targetOutcomeType = 'code_pr'
      targetTier = 'direct'
      accountDefaultModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
      provider = agentModelSettings?.systemAgents?.coder?.provider
      thinking = agentModelSettings?.systemAgents?.coder?.thinking
      serviceTier = agentModelSettings?.systemAgents?.coder?.serviceTier
    }
  } else if (taskIntent === 'audit') {
    targetAgent = 'finder'
    targetOutcomeType = 'audit_report'
    targetTier = 'discovery'
    accountDefaultModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
    provider = agentModelSettings?.systemAgents?.finder?.provider
    thinking = agentModelSettings?.systemAgents?.finder?.thinking
    serviceTier = agentModelSettings?.systemAgents?.finder?.serviceTier
  } else if (taskIntent === 'image') {
    targetAgent = 'image'
    targetOutcomeType = 'media_bundle'
    targetTier = 'direct'
    accountDefaultModel =
      mediaSelections?.defaultImageModel || mediaSelections?.selectedImageModel || 'Account default'
  } else if (taskIntent === 'video') {
    targetAgent = 'video'
    targetOutcomeType = 'video_clip'
    targetTier = 'direct'
    accountDefaultModel =
      mediaSelections?.defaultVideoModel || mediaSelections?.selectedVideoModel || 'Account default'
  } else if (taskIntent === 'sound') {
    targetAgent = 'sound'
    targetOutcomeType = 'audio_clip'
    targetTier = 'direct'
    accountDefaultModel =
      mediaSelections?.defaultAudioModel || mediaSelections?.selectedAudioModel || 'Account default'
  }

  // If preview failed with an error, do NOT guess a label! Fail visibly.
  if (previewError && previewError.trim()) {
    return {
      targetAgent,
      targetOutcomeType,
      targetTier,
      resolvedModel: 'Preview unavailable',
      isOverridden: false,
      accountDefaultModel,
      previewFailed: true,
      error: previewError.trim(),
    }
  }

  let resolvedModel = accountDefaultModel
  let isOverridden = false

  if (backendModelPreview?.resolved_model?.model) {
    resolvedModel = backendModelPreview.resolved_model.model
    provider = backendModelPreview.resolved_model.provider
    thinking = backendModelPreview.resolved_model.thinking
    serviceTier = backendModelPreview.resolved_model.service_tier
    contextMode = backendModelPreview.resolved_model.context_mode
    isOverridden = backendModelPreview.model_source === 'task_override'
    if (backendModelPreview.account_default_model?.model) {
      accountDefaultModel = backendModelPreview.account_default_model.model
    }
  } else if (taskIntent === 'code' || taskIntent === 'audit') {
    if (newTaskModelOverride && newTaskModelOverride.trim()) {
      resolvedModel = newTaskModelOverride.trim()
      isOverridden = true
    }
  } else if (taskIntent === 'image') {
    resolvedModel =
      mediaSelections?.selectedImageModel || mediaSelections?.defaultImageModel || 'Account default'
    isOverridden = Boolean(
      mediaSelections?.selectedImageModel &&
        mediaSelections.selectedImageModel !== mediaSelections.defaultImageModel
    )
  } else if (taskIntent === 'video') {
    resolvedModel =
      mediaSelections?.selectedVideoModel || mediaSelections?.defaultVideoModel || 'Account default'
    isOverridden = Boolean(
      mediaSelections?.selectedVideoModel &&
        mediaSelections.selectedVideoModel !== mediaSelections.defaultVideoModel
    )
  } else if (taskIntent === 'sound') {
    resolvedModel =
      mediaSelections?.selectedAudioModel || mediaSelections?.defaultAudioModel || 'Account default'
    isOverridden = Boolean(
      mediaSelections?.selectedAudioModel &&
        mediaSelections.selectedAudioModel !== mediaSelections.defaultAudioModel
    )
  }

  return {
    targetAgent,
    targetOutcomeType,
    targetTier,
    resolvedModel,
    isOverridden,
    accountDefaultModel,
    provider,
    thinking,
    serviceTier,
    contextMode,
  }
}

export function resolveOptimisticApprovedDeliverables(
  task: Pick<RunningTask, 'id' | 'title' | 'agentType' | 'outcomeType' | 'variantCount' | 'workerName' | 'deliverables'>
): MediaDeliverable[] {
  if (task.deliverables && task.deliverables.length > 0) {
    return task.deliverables.map((d) => ({ ...d, status: 'generating' as const }))
  }
  if (!isMediaTaskType(task)) {
    return []
  }
  return Array.from({ length: task.variantCount || 1 }, (_, i) => ({
    id: `deliv_${task.id}_${i + 1}`,
    title: `${task.title} (Variant ${i + 1})`,
    type: (task.agentType === 'video' ? 'video' : 'image') as any,
    status: 'generating' as const,
    createdAt: new Date().toISOString(),
    author: task.workerName || 'Orchestrator',
  }))
}

// Auto-detect is omission, not an implicit choice of the project's coordination root.
// Only the backend may bind an omitted target to a unique authorized repository.
export function resolveTaskWorkspace(override?: string): string | undefined {
  return override?.trim() || undefined
}

export function taskWorkspaceSelection(override: string, project?: Pick<ProjectSummary, 'workspaces'> | null): {
  workspace_path?: string
  workspace_id?: string
  workspace_generation?: number
} {
  const workspace_path = resolveTaskWorkspace(override)
  if (!workspace_path) return {}
  if (workspace_path === '.') throw new Error("dot '.' is not allowed as an execution target")
  const matches = project?.workspaces?.filter((ws) => ws.path === workspace_path) || []
  if (matches.length !== 1) throw new Error('Selected target workspace is not linked to this project; refresh the project and select a workspace.')
  const selected = matches[0]
  return {
    workspace_path,
    workspace_id: selected.workspace_id || undefined,
    ...(selected.workspace_generation != null ? { workspace_generation: selected.workspace_generation } : {}),
  }
}

export function taskDeployRequestIdentity(
  pending: { payloadKey: string; clientTaskId: string } | null,
  projectId: string,
  payload: object,
  makeId: () => string
): { payloadKey: string; clientTaskId: string } {
  const payloadKey = `${projectId}:${JSON.stringify(payload)}`
  return pending?.payloadKey === payloadKey ? pending : { payloadKey, clientTaskId: makeId() }
}

export interface TaskAcceptanceTarget {
  sessionId?: string
  session_id?: string
  planBinding?: ProjectTaskPlanBinding
  plan_binding?: ProjectTaskPlanBinding
  agentType?: string
  outcomeType?: string
  planDocument?: unknown
  plan_document?: unknown
  revision?: number
}

export interface TaskAcceptancePayload {
  session_id?: string
  plan_id?: string
  definition_revision?: number
}

export function buildTaskAcceptancePayload(
  task: TaskAcceptanceTarget | null | undefined
): TaskAcceptancePayload {
  const binding = task?.planBinding || task?.plan_binding
  const bindingSessionId = binding?.sessionId || binding?.session_id
  const executionSessionId = task?.sessionId || task?.session_id
  const sessionId = bindingSessionId || executionSessionId

  if (!binding) {
    return {
      session_id: sessionId ? String(sessionId).trim() : undefined,
      plan_id: undefined,
      definition_revision: undefined,
    }
  }

  const planId = binding.planId || binding.plan_id
  let definitionRevision: number | undefined
  if (typeof binding.definitionRevision === 'number') {
    definitionRevision = binding.definitionRevision
  } else if (typeof binding.definition_revision === 'number') {
    definitionRevision = binding.definition_revision
  }

  return {
    session_id: sessionId ? String(sessionId).trim() : undefined,
    plan_id: planId ? String(planId).trim() : undefined,
    definition_revision: definitionRevision,
  }
}

export interface SelectedTaskContextSnapshot {
  projectId: string
  projectName?: string
  taskId: string
  taskTitle: string
  agentType?: string
  status: string
  sessionId?: string
  taskRevision: number
  planId?: string
  planDefinitionRevision?: number
  snapshotTimestamp: number
}

export interface ValidateSelectedTaskResult {
  valid: boolean
  error?: string
  reason?: 'missing_project' | 'missing_task' | 'cross_project' | 'stale_task' | 'stale_revision'
  snapshot?: SelectedTaskContextSnapshot
}

/**
 * Validates selected task context before chat forwarding.
 * Invariants:
 * - Exact project/task/session/plan revision; never title inference.
 * - Stale/missing/cross-project tasks are rejected immediately.
 * - Snapshot on send: creates an immutable snapshot of verified task state at send time.
 * Note: This client-side validation ensures the UI forwards accurate, verified task context
 * to the AI model. It is not a server security boundary or tamper-proof authorization guarantee.
 */
export function validateSelectedTaskForContext(
  project: Pick<ProjectSummary, 'id' | 'name' | 'repoPath'> | null | undefined,
  task: RunningTask | null | undefined,
  currentTasks?: RunningTask[] | RunningTask,
): ValidateSelectedTaskResult {
  if (!project || !project.id || !project.id.trim()) {
    return {
      valid: false,
      reason: 'missing_project',
      error: 'No active project selected for task context forwarding',
    }
  }

  if (!task || !task.id || !task.id.trim()) {
    return {
      valid: false,
      reason: 'missing_task',
      error: 'No task selected for context forwarding',
    }
  }

  // Cross-project & existence check: match exclusively by task.id (NEVER title inference)
  if (currentTasks) {
    const selectedId = task.id
    const liveMatch = Array.isArray(currentTasks)
      ? currentTasks.find((t) => t.id === selectedId)
      : (currentTasks.id === task.id ? currentTasks : undefined)

    if (!liveMatch) {
      return {
        valid: false,
        reason: 'cross_project',
        error: `Selected task ${task.id} is missing from active project ${project.id} or belongs to another project`,
      }
    }

    // Project ID verification if present on live match
    const liveProjectId = (liveMatch as any).projectId || (liveMatch as any).project_id
    if (liveProjectId && liveProjectId !== project.id.trim()) {
      return {
        valid: false,
        reason: 'cross_project',
        error: `Selected task ${task.id} belongs to project ${liveProjectId}, not active project ${project.id}`,
      }
    }

    // Stale revision check: ensure the caller's task revision matches authoritative current state
    const callerRev = typeof task.revision === 'number' ? task.revision : 1
    const liveRev = typeof liveMatch.revision === 'number' ? liveMatch.revision : 1
    if (callerRev !== liveRev) {
      return {
        valid: false,
        reason: 'stale_revision',
        error: `Selected task ${task.id} revision r${callerRev} is stale (project task is at r${liveRev})`,
      }
    }

    // Stale session check: ensure session IDs match if both specify one
    const callerSid = task.sessionId || (task.planBinding?.sessionId) || (task as any).plan_binding?.sessionId || (task as any).plan_binding?.session_id
    const liveSid = liveMatch.sessionId || (liveMatch.planBinding?.sessionId) || (liveMatch as any).plan_binding?.sessionId || (liveMatch as any).plan_binding?.session_id
    if (callerSid && liveSid && callerSid !== liveSid) {
      return {
        valid: false,
        reason: 'stale_task',
        error: `Selected task ${task.id} session ${callerSid} is stale (project task session is ${liveSid})`,
      }
    }

    // Stale plan check: ensure plan IDs and definition revisions match
    const callerBinding = task.planBinding || (task as any).plan_binding
    const liveBinding = liveMatch.planBinding || (liveMatch as any).plan_binding
    const callerPlanId = callerBinding?.planId || callerBinding?.plan_id
    const livePlanId = liveBinding?.planId || liveBinding?.plan_id
    if (callerPlanId !== livePlanId) {
      return {
        valid: false,
        reason: 'stale_task',
        error: `Selected task ${task.id} plan ${callerPlanId || 'none'} is stale (project task plan is ${livePlanId || 'none'})`,
      }
    }
    const callerPlanRev = typeof callerBinding?.definitionRevision === 'number'
      ? callerBinding.definitionRevision
      : (typeof callerBinding?.definition_revision === 'number' ? callerBinding.definition_revision : undefined)
    const livePlanRev = typeof liveBinding?.definitionRevision === 'number'
      ? liveBinding.definitionRevision
      : (typeof liveBinding?.definition_revision === 'number' ? liveBinding.definition_revision : undefined)
    if (callerPlanRev !== livePlanRev) {
      return {
        valid: false,
        reason: 'stale_revision',
        error: `Selected task ${task.id} plan definition revision ${callerPlanRev !== undefined ? `r${callerPlanRev}` : 'none'} is stale (project task plan is at ${livePlanRev !== undefined ? `r${livePlanRev}` : 'none'})`,
      }
    }

    // Use latest live match to ensure snapshot reflects latest verified attributes
    task = liveMatch
  }

  // Exact plan definition revision guard
  const binding = task.planBinding || task.plan_binding
  const planId = binding?.planId || binding?.plan_id
  let planDefinitionRevision: number | undefined
  if (typeof binding?.definitionRevision === 'number') {
    planDefinitionRevision = binding.definitionRevision
  } else if (typeof binding?.definition_revision === 'number') {
    planDefinitionRevision = binding.definition_revision
  }

  const taskSessionId = task.sessionId || binding?.sessionId || binding?.session_id

  const snapshot: SelectedTaskContextSnapshot = {
    projectId: project.id.trim(),
    projectName: project.name?.trim(),
    taskId: task.id.trim(),
    taskTitle: task.title?.trim() || task.id,
    agentType: task.agentType,
    status: task.status,
    sessionId: taskSessionId ? String(taskSessionId).trim() : undefined,
    taskRevision: typeof task.revision === 'number' ? task.revision : 1,
    planId: planId ? String(planId).trim() : undefined,
    planDefinitionRevision,
    snapshotTimestamp: Date.now(),
  }

  return { valid: true, snapshot }
}

/**
 * Reconciles the selected task ID against the current list of project tasks.
 *
 * Invariants:
 * - Never implicitly auto-selects or defaults to the first task or any task.
 * - Ordinary chat must remain unattached unless the user explicitly selects a task.
 * - Deliberate selection is preserved as long as the selected task exists in the current project.
 * - Stale selections (e.g. task deleted, project switched, or empty tasks) are cleared to an empty string.
 */
export function reconcileSelectedTaskId(
  currentSelectedTaskId: string | null | undefined,
  availableTasks: Array<{ id: string }>
): string {
  if (!currentSelectedTaskId || !currentSelectedTaskId.trim()) return ''
  const trimmed = currentSelectedTaskId.trim()
  const exists = availableTasks.some((t) => t.id === trimmed)
  return exists ? trimmed : ''
}

/**
 * Builds explicit user-message envelope for forwarding selected-task context.
 * This guarantees the AI receives exact task context in content without backend schema changes
 * or relying on invented metadata ignored by the model executor.
 * Note: This message envelope is a client-side prompt formatting mechanism so the AI
 * orchestrator understands which task the operator is discussing; it does not constitute
 * a server security boundary.
 */
export function buildSelectedTaskMessageEnvelope(
  snapshot: SelectedTaskContextSnapshot,
  userPrompt: string,
): string {
  const headerLines = [
    `[Task Context: ${snapshot.taskId}]`,
    `project_id: ${snapshot.projectId}`,
    `task_id: ${snapshot.taskId}`,
    `task_title: ${snapshot.taskTitle}`,
    `task_status: ${snapshot.status}`,
    `task_revision: ${snapshot.taskRevision}`,
  ]
  if (snapshot.agentType) {
    headerLines.push(`agent_type: ${snapshot.agentType}`)
  }
  if (snapshot.sessionId) {
    headerLines.push(`session_id: ${snapshot.sessionId}`)
  }
  if (snapshot.planId) {
    headerLines.push(`plan_id: ${snapshot.planId}`)
  }
  if (snapshot.planDefinitionRevision != null) {
    headerLines.push(`plan_definition_revision: ${snapshot.planDefinitionRevision}`)
  }
  headerLines.push('---')

  const promptText = userPrompt.trim()
  if (promptText) {
    headerLines.push(promptText)
  }

  return headerLines.join('\n')
}

/**
 * Builds tracking metadata accompanying selected-task chat messages.
 */
export function buildSelectedTaskMessageMetadata(
  snapshot: SelectedTaskContextSnapshot,
  baseMetadata?: Record<string, unknown>,
): Record<string, unknown> {
  return {
    ...baseMetadata,
    orchestrate_view: true,
    project_id: snapshot.projectId,
    task_id: snapshot.taskId,
    selected_task_id: snapshot.taskId,
    task_revision: snapshot.taskRevision,
    ...(snapshot.sessionId ? { task_session_id: snapshot.sessionId } : {}),
    ...(snapshot.planId ? { plan_id: snapshot.planId } : {}),
    ...(snapshot.planDefinitionRevision != null ? { plan_definition_revision: snapshot.planDefinitionRevision } : {}),
  }
}

/**
 * Parses explicit user-message envelope from message content.
 */
export function parseSelectedTaskMessageEnvelope(content: string): {
  hasEnvelope: boolean
  taskId?: string
  projectId?: string
  taskRevision?: number
  planId?: string
  planDefinitionRevision?: number
  agentType?: string
  taskStatus?: string
  userPrompt: string
} {
  const trimmed = (content || '').trim()
  const match = trimmed.match(/^\[Task Context:\s*([^\]]+)\]\n([\s\S]*?)\n---\n?([\s\S]*)$/)
  if (!match) {
    return { hasEnvelope: false, userPrompt: trimmed }
  }

  const taskId = match[1].trim()
  const headerBlock = match[2]
  const userPrompt = match[3].trim()

  let projectId: string | undefined
  let taskRevision: number | undefined
  let planId: string | undefined
  let planDefinitionRevision: number | undefined
  let agentType: string | undefined
  let taskStatus: string | undefined

  for (const line of headerBlock.split('\n')) {
    const colonIdx = line.indexOf(':')
    if (colonIdx === -1) continue
    const key = line.slice(0, colonIdx).trim()
    const val = line.slice(colonIdx + 1).trim()
    if (key === 'project_id') projectId = val
    if (key === 'plan_id') planId = val
    if (key === 'agent_type') agentType = val
    if (key === 'task_status') taskStatus = val
    if (key === 'task_revision') {
      const parsed = parseInt(val, 10)
      if (!isNaN(parsed)) taskRevision = parsed
    }
    if (key === 'plan_definition_revision') {
      const parsed = parseInt(val, 10)
      if (!isNaN(parsed)) planDefinitionRevision = parsed
    }
  }

  return {
    hasEnvelope: true,
    taskId,
    projectId,
    taskRevision,
    planId,
    planDefinitionRevision,
    agentType,
    taskStatus,
    userPrompt,
  }
}

export interface SessionDataLookup {
  sessionRecord?: any
  view?: any
  intent?: any
  liveRun?: any
  planRecord?: any
}

/**
 * Aggregate real-time task lifecycle across authoritative backend state, primary session,
 * and all associated child sessions (task program jobs, delegated planning, etc.).
 *
 * Invariants:
 * 1. Single session: track running, needs_review, failed, or completed with git verification.
 * 2. Multi-session / Task Programs: NEVER whole-task complete from one child.
 * 3. Planning tasks: transition to pending_approval when plan document is authored, failed if planning fails.
 * 4. Preserves explicit accepted completion / integration; never conflates cancelled/failed with success.
 */
export function aggregateTaskLiveState(
  task: RunningTask,
  liveTaskSessionsData: Record<string, SessionDataLookup | undefined>,
): RunningTask {
  const associatedSids = extractTaskSessionIds(task as TaskSessionCandidate)
  const primarySessionId =
    task.sessionId ||
    task.planBinding?.sessionId ||
    task.planBinding?.session_id ||
    (task as any).plan_binding?.sessionId ||
    (task as any).plan_binding?.session_id ||
    (associatedSids.length > 0 ? associatedSids[0] : undefined)

  const primaryData = primarySessionId ? liveTaskSessionsData[primarySessionId] : undefined
  const primaryRecord = primaryData?.sessionRecord
  const primarySess = primaryRecord?.kind === 'full' ? primaryRecord.session : undefined
  const primaryView = primaryData?.view
  const primaryIntent = primaryData?.intent
  const primaryLiveRun = primaryData?.liveRun
  const primaryPlanRecord = primaryData?.planRecord as any
  const primaryPlanDoc = selectTaskPlanDocument(task, primaryPlanRecord)
  const primaryLifecycle = primarySess?.lifecycle as any

  const primaryIntentStatus = primaryIntent?.status?.trim().toLowerCase()
  const primaryRunStatus = primaryView?.current_run_state?.status?.trim().toLowerCase()
  const primaryPhase = primaryLifecycle?.phase?.trim().toLowerCase()

  const isPrimaryActive = Boolean(
    primaryLifecycle?.active === true ||
    primaryIntentStatus === 'running' ||
    primaryIntentStatus === 'pending_executor' ||
    primaryRunStatus === 'running' ||
    primaryRunStatus === 'pending_executor'
  )
  const isPrimaryReview = Boolean(
    primaryPlanRecord?.status === 'waiting_review' ||
    primaryPhase === 'needs_review' ||
    primaryPlanDoc?.executionState?.status === 'waiting_review' ||
    primaryPlanDoc?.checkpoints?.some((cp: any) => cp.status === 'needs_review')
  )
  const isPrimaryFailed = Boolean(
    primaryIntentStatus === 'failed' ||
    primaryIntentStatus === 'cancelled' ||
    primaryIntentStatus === 'interrupted' ||
    primaryIntentStatus === 'expired' ||
    primaryPhase === 'failed' ||
    primaryPhase === 'cancelled'
  )

  const programJobs =
    task.taskProgramStatus?.jobs ||
    (task as any).task_program_status?.jobs ||
    task.taskProgram?.jobs ||
    (task as any).task_program?.jobs ||
    []
  const hasTaskProgram = Boolean(
    task.taskProgramStatus ||
    (task as any).task_program_status ||
    task.taskProgram ||
    (task as any).task_program ||
    programJobs.length > 0
  )

  // Map state for each associated session
  const sessionStates: TaskSessionStateItem[] = associatedSids.map((sid) => {
    const sData = liveTaskSessionsData[sid]
    const sRecord = sData?.sessionRecord
    const sSess = sRecord?.kind === 'full' ? sRecord.session : undefined
    const sIntent = sData?.intent
    const sView = sData?.view
    const sLifecycle = sSess?.lifecycle as any
    const sPlan = sData?.planRecord as any

    // CRITICAL: Do NOT assign parent's plan to child coder sessions!
    const isPlanOwner = Boolean(
      (task.planBinding?.sessionId && task.planBinding.sessionId === sid) ||
      (task.planBinding?.session_id && task.planBinding.session_id === sid) ||
      ((task as any).plan_binding?.sessionId && (task as any).plan_binding.sessionId === sid) ||
      ((task as any).plan_binding?.session_id && (task as any).plan_binding.session_id === sid) ||
      (!hasTaskProgram && primarySessionId === sid)
    )
    const sPlanDoc = isPlanOwner ? selectTaskPlanDocument(task, sPlan) : sPlan?.document

    const intentStatus = sIntent?.status?.trim().toLowerCase()
    const currentRunStatus = sView?.current_run_state?.status?.trim().toLowerCase()
    const lifecyclePhase = sLifecycle?.phase?.trim().toLowerCase()
    const isLifecycleActiveFlag = Boolean(sLifecycle?.active === true)

    const isAct = Boolean(
      intentStatus === 'running' ||
      intentStatus === 'pending_executor' ||
      currentRunStatus === 'running' ||
      currentRunStatus === 'pending_executor' ||
      isLifecycleActiveFlag
    )
    const isRev = Boolean(
      sPlan?.status === 'waiting_review' ||
      lifecyclePhase === 'needs_review' ||
      sPlanDoc?.executionState?.status === 'waiting_review' ||
      sPlanDoc?.checkpoints?.some((cp: any) => cp.status === 'needs_review')
    )
    const isFail = Boolean(
      intentStatus === 'failed' ||
      intentStatus === 'cancelled' ||
      intentStatus === 'interrupted' ||
      intentStatus === 'expired' ||
      lifecyclePhase === 'failed' ||
      lifecyclePhase === 'cancelled'
    )

    // CRITICAL: An approved plan with unfinished checkpoints does NOT complete on provider turn completion!
    let isPlanUnfinished = false
    if (isPlanOwner && sPlanDoc?.checkpoints && Array.isArray(sPlanDoc.checkpoints) && sPlanDoc.checkpoints.length > 0) {
      if (sPlanDoc.checkpoints.some((cp: any) => cp.status !== 'completed')) {
        isPlanUnfinished = true
      }
    }

    const hasCompletedIntent = Boolean(
      (intentStatus === 'completed' || lifecyclePhase === 'completed') && !isRev && !isFail
    )
    const isComp = hasCompletedIntent && !isPlanUnfinished

    const matchingJob = programJobs.find(
      (j: any) => j.child_session_id === sid || j.current_session_id === sid
    )

    const isHydrated = Boolean(sData && (sRecord || sIntent || sView))

    let itemStatus: TaskSessionStateItem['status'] = 'unknown'
    if (isHydrated) {
      // Runtime session terminal/active evidence is authoritative over stale job state
      if (intentStatus === 'dispatch_blocked' || lifecyclePhase === 'blocked') {
        itemStatus = 'blocked'
      } else if (lifecyclePhase === 'paused') {
        itemStatus = 'paused'
      } else if (intentStatus === 'pending_executor' || currentRunStatus === 'pending_executor') {
        itemStatus = 'queued'
      } else if (isFail) {
        itemStatus = 'failed'
      } else if (isRev) {
        itemStatus = 'needs_review'
      } else if (isComp) {
        itemStatus = 'completed'
      } else if (matchingJob?.state === 'integrated' || matchingJob?.state === 'completed') {
        itemStatus = 'completed'
      } else if (matchingJob?.state === 'handoff_ready') {
        itemStatus = 'needs_review'
      } else if (matchingJob?.state === 'failed' || matchingJob?.state === 'conflict') {
        itemStatus = 'failed'
      } else if (isAct) {
        itemStatus = 'running'
      } else if (matchingJob) {
        if (matchingJob.state === 'running') itemStatus = 'running'
        else if (matchingJob.state === 'conflict' || matchingJob.state === 'failed') itemStatus = 'failed'
        else if (matchingJob.state === 'handoff_ready') itemStatus = 'needs_review'
        else if (matchingJob.state === 'integrated' || matchingJob.state === 'completed') itemStatus = 'completed'
        else itemStatus = 'queued'
      }
    } else {
      // Unhydrated != queued: use matchingJob state if present
      if (matchingJob) {
        if (matchingJob.state === 'running') itemStatus = 'running'
        else if (matchingJob.state === 'conflict' || matchingJob.state === 'failed') itemStatus = 'failed'
        else if (matchingJob.state === 'handoff_ready') itemStatus = 'needs_review'
        else if (matchingJob.state === 'integrated' || matchingJob.state === 'completed') itemStatus = 'completed'
        else itemStatus = 'queued'
      } else {
        itemStatus = 'unknown'
      }
    }

    // Completed direct sessions may no longer have an active intent or lifecycle
    // projection. Only a one-session task can safely supply its terminal state;
    // never spread a parent's aggregate result across a delegated cohort.
    if (itemStatus === 'unknown' && !hasTaskProgram && associatedSids.length === 1 && sid === primarySessionId) {
      if (task.status === 'needs_review' || task.status === 'completed' || task.status === 'failed' || task.status === 'blocked') {
        itemStatus = task.status
      }
    }

    const jobDef = task.taskProgram?.jobs?.find((j: any) => j.id === matchingJob?.job_id)

    return {
      sessionId: sid,
      title: jobDef?.title || matchingJob?.job_id || sSess?.title || `Session ${sid.slice(0, 8)}`,
      role: (sSess?.metadata?.role as string) || (matchingJob ? 'coder' : undefined),
      status: itemStatus,
      lastError: matchingJob?.blocker?.message || sIntent?.blocked_reason || sLifecycle?.last_error || undefined,
    }
  })

  // For Task Programs: separate child coder jobs from the parent coordinator session
  const isTaskProgramTask = hasTaskProgram && programJobs.length > 0
  const relevantSessionStates = isTaskProgramTask
    ? sessionStates.filter((s) => s.sessionId !== primarySessionId || programJobs.some((j: any) => (j.current_session_id || j.child_session_id) === s.sessionId))
    : sessionStates

  let unallocatedJobsCount = 0
  let jobRunningCount = 0
  let jobReviewCount = 0
  let jobFailedCount = 0
  let jobCompletedCount = 0
  for (const j of programJobs) {
    const jobSid = (j.current_session_id || j.child_session_id)?.trim()
    const state = j.state?.trim().toLowerCase()
    if (!jobSid || state === 'declared' || state === 'queued') {
      unallocatedJobsCount++
    }
    if (state === 'running') jobRunningCount++
    else if (state === 'conflict' || state === 'failed') jobFailedCount++
    else if (state === 'handoff_ready') jobReviewCount++
    else if (state === 'integrated' || state === 'completed') jobCompletedCount++
  }

  const runningSessions = isTaskProgramTask
    ? relevantSessionStates.filter((s) => s.status === 'running').length
    : Math.max(relevantSessionStates.filter((s) => s.status === 'running').length, jobRunningCount)

  const reviewSessions = isTaskProgramTask
    ? relevantSessionStates.filter((s) => s.status === 'needs_review').length
    : Math.max(relevantSessionStates.filter((s) => s.status === 'needs_review').length, jobReviewCount)

  const failedSessions = isTaskProgramTask
    ? relevantSessionStates.filter((s) => s.status === 'failed').length
    : Math.max(relevantSessionStates.filter((s) => s.status === 'failed').length, jobFailedCount)

  const completedSessions = isTaskProgramTask
    ? relevantSessionStates.filter((s) => s.status === 'completed').length
    : Math.max(relevantSessionStates.filter((s) => s.status === 'completed').length, jobCompletedCount)

  const sessionSummary: TaskSessionSummary = {
    totalSessions: relevantSessionStates.length,
    runningSessions,
    reviewSessions,
    failedSessions,
    completedSessions,
    sessionStates: relevantSessionStates,
  }

  // Aggregate Status computation
  let status = task.status
  const isLifecycleActive = isPrimaryActive || runningSessions > 0 || unallocatedJobsCount > 0
  const hasReviewRequired = isPrimaryReview || reviewSessions > 0
  const isAnyFailed = isPrimaryFailed || failedSessions > 0

  if (task.status === 'completed' || task.isIntegrated) {
    status = 'completed'
  } else if (task.status === 'rejected') {
    status = 'rejected'
  } else if (task.status === 'pending_approval') {
    status = 'pending_approval'
  } else if (task.status === 'planning') {
    const hasAuthoredPlan = Boolean(
      (primaryPlanDoc?.checkpoints && primaryPlanDoc.checkpoints.length > 0) ||
      (primaryPlanRecord?.document?.checkpoints && primaryPlanRecord.document.checkpoints.length > 0) ||
      primaryPlanRecord?.status === 'waiting_review'
    )
    if (hasAuthoredPlan) {
      status = 'pending_approval'
    } else if (isAnyFailed && !isLifecycleActive) {
      status = 'failed'
    } else {
      status = 'planning'
    }
  } else if (task.status === 'queued') {
    if (isLifecycleActive) {
      status = 'running'
    } else {
      status = 'queued'
    }
  } else {
    // In execution (in_progress, running, needs_review, failed)
    const tpRecordState = (task.taskProgramStatus?.state || (task as any).task_program_status?.state)?.trim().toLowerCase()
    if (tpRecordState) {
      // Backend TaskProgram record is authoritative
      if (tpRecordState === 'completed') {
        status = task.isIntegrated ? 'completed' : 'needs_review'
      } else if (tpRecordState === 'failed' || tpRecordState === 'cancelled') {
        status = 'failed'
      } else if (tpRecordState === 'blocked') {
        status = 'needs_review'
      } else if (tpRecordState === 'running') {
        status = 'running'
      }
    } else {
      // Live session aggregation
      if (sessionStates.some((s) => s.status === 'blocked' || s.status === 'paused') && runningSessions === 0) {
        status = 'blocked'
      } else if (isLifecycleActive) {
        status = 'running'
      } else if (isAnyFailed) {
        // CRITICAL: Check failure BEFORE review; never mask failures with review!
        status = 'failed'
      } else if (hasReviewRequired) {
        if (task.isIntegrated) {
          status = 'completed'
        } else {
          status = 'needs_review'
        }
      } else if (
        isTaskProgramTask
          ? (programJobs.length > 0 && completedSessions === programJobs.length && unallocatedJobsCount === 0)
          : (relevantSessionStates.length > 0 && completedSessions === relevantSessionStates.length)
      ) {
        if (task.isIntegrated) {
          status = 'completed'
        } else {
          status = 'needs_review'
        }
      } else if (!primarySessionId && !hasTaskProgram && (status === 'in_progress' || status === 'running')) {
        status = 'failed'
      }
    }
  }

  // Plan Checkpoints & Subtasks extraction
  let activePlanCheckpoints: RunningTaskPlanCheckpoint[] | undefined
  let totalSubtasks = 0
  let completedSubtasks = 0
  let activeSubtaskId = ''
  let activeCheckpointTitle = ''
  let activeSubtaskTitle = ''

  const effectivePlanDoc = primaryPlanDoc || (task as any).planDocument || (task as any).plan_document
  if (effectivePlanDoc?.checkpoints && Array.isArray(effectivePlanDoc.checkpoints)) {
    const activeCheckpointId =
      effectivePlanDoc.activeCheckpointId || effectivePlanDoc.executionState?.lastCheckpointId || ''
    activePlanCheckpoints = effectivePlanDoc.checkpoints.map((cp: any) => {
      const isCpActive = cp.id === activeCheckpointId
      if (isCpActive && cp.title) {
        activeCheckpointTitle = cp.title
      }
      const subs = (cp.subtasks || []).map((st: any) => {
        const isDone = st.status === 'completed' || st.completed === true
        if (isDone) completedSubtasks++
        totalSubtasks++
        if (cp.activeSubtaskId === st.id || st.status === 'in_progress') {
          activeSubtaskId = st.id
          activeSubtaskTitle = st.title
        }
        return {
          id: st.id,
          title: st.title,
          status: st.status || (st.completed ? 'completed' : 'pending'),
          completed: isDone,
        }
      })
      if (!cp.subtasks || cp.subtasks.length === 0) {
        totalSubtasks++
        if (cp.status === 'completed') completedSubtasks++
      }
      return {
        id: cp.id,
        title: cp.title,
        status: cp.status,
        subtasks: subs,
      }
    })
  }

  // Tool calls & live streaming text: prioritize running session
  const activeSid = sessionStates.find((s) => s.status === 'running')?.sessionId || primarySessionId
  const activeData = activeSid ? liveTaskSessionsData[activeSid] : primaryData
  const activeSettings = activeData?.view?.agentic_settings
  const activePreference = activeSettings?.effective_preference as { provider?: string; model?: string } | undefined
  const effectiveLiveRun = activeData?.liveRun || primaryLiveRun

  const toolCalls = effectiveLiveRun ? Object.values((effectiveLiveRun.toolCallsByCallId || {}) as Record<string, any>) : []
  const currentTool = [...toolCalls]
    .sort((a, b) => b.updatedAt - a.updatedAt)
    .map((t) => t.toolDisplay?.trim() || t.toolName?.trim() || '')
    .find(Boolean) || ''
  const toolActivitySummary = summarizeDesktopV3TaskToolActivity(toolCalls.filter((call: any) =>
    ['running', 'in_progress', 'pending', 'started'].includes((call.status || '').trim().toLowerCase())) as any)
  const liveAssistantText = [
    ...(effectiveLiveRun?.assistantSegments ?? []),
    ...(effectiveLiveRun?.assistantDraft ? [effectiveLiveRun.assistantDraft] : []),
  ]
    .sort((a: any, b: any) => (a.timelineSeq ?? 0) - (b.timelineSeq ?? 0) || a.updatedAt - b.updatedAt)
    .map((s: any) => s.content)
    .join('')
    .trim()
  const liveToolCalls = [...toolCalls]
    .sort((a, b) => a.updatedAt - b.updatedAt)
    .map((t) => t.toolDisplay?.trim() || t.toolName?.trim() || '')
    .filter(Boolean)
    .join('\n')

  const currentFocus =
    activeSubtaskTitle ||
    activeCheckpointTitle ||
    toolActivitySummary ||
    currentTool ||
    (isLifecycleActive ? 'Executing autonomous mission plan...' : '')

  const programJobsCount = programJobs.length
  let planProgressPercent: number | undefined
  if (totalSubtasks > 0) {
    planProgressPercent = Math.min(100, Math.round((completedSubtasks / totalSubtasks) * 100))
  } else if (programJobsCount > 0) {
    planProgressPercent = Math.min(100, Math.round((completedSessions / programJobsCount) * 100))
  } else {
    planProgressPercent = task.planProgressPercent
  }

  const startedAt =
    primaryIntent?.started_at ||
    primaryLifecycle?.started_at ||
    task.createdAt ||
    (primarySess ? primarySess.created_at : undefined)
  const elapsedMs = primaryIntent?.duration_ms || (startedAt ? Date.now() - startedAt : 0)

  return {
    ...task,
    status,
    activeAgent: activeSettings?.resolved_agent_name?.trim() || activeSettings?.agent_name?.trim() || undefined,
    activeProvider: activePreference?.provider?.trim() || undefined,
    activeModel: activePreference?.model?.trim() || undefined,
    planDocument: effectivePlanDoc,
    planBinding:
      task.planBinding ||
      (task as any).plan_binding ||
      (primaryPlanRecord?.id ? { planId: primaryPlanRecord.id, sessionId: primarySessionId } : undefined),
    currentFocus: currentFocus || task.currentFocus,
    currentTool: currentTool || task.currentTool,
    liveAssistantText: liveAssistantText || task.liveAssistantText,
    liveToolCalls: liveToolCalls || task.liveToolCalls,
    toolActivitySummary: toolActivitySummary || undefined,
    activePlanCheckpoints:
      activePlanCheckpoints && activePlanCheckpoints.length > 0 ? activePlanCheckpoints : task.activePlanCheckpoints,
    activeSubtaskId: activeSubtaskId || task.activeSubtaskId,
    planProgressPercent: planProgressPercent !== undefined ? planProgressPercent : task.planProgressPercent,
    subtasksCount:
      totalSubtasks > 0
        ? { completed: completedSubtasks, total: totalSubtasks }
        : programJobsCount > 0
        ? { completed: Math.min(completedSessions, programJobsCount), total: programJobsCount }
        : task.subtasksCount,
    startedAt,
    elapsedMs,
    sessionSummary,
    associatedSessionIds: associatedSids,
  }
}


