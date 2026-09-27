import type { RunningTask, MediaDeliverable, TaskOutcomeType, BackendTaskModelPreview, ProjectTaskPlanBinding } from './orchestrate-types'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'

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
      let cohortIsOverride = isOverride

      if (isOverride && backendModelPreview?.resolved_model?.model) {
        resolvedModel = backendModelPreview.resolved_model.model
        provider = backendModelPreview.resolved_model.provider
        thinking = backendModelPreview.resolved_model.thinking
        serviceTier = backendModelPreview.resolved_model.service_tier
        contextMode = backendModelPreview.resolved_model.context_mode
      } else if (isOverride && task.model?.trim()) {
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

export function resolveTaskWorkspace(
  override?: string,
  project?: { repoPath?: string; workspaces?: Array<{ path?: string }>; linkedWorkspaces?: string[] } | null
): string | undefined {
  const chosen = override?.trim()
  if (chosen && chosen !== '.') return chosen
  if (project?.repoPath && project.repoPath.trim() !== '.') return project.repoPath.trim()
  const firstWs = project?.workspaces?.find((w) => w?.path && w.path.trim() !== '.')?.path
  if (firstWs) return firstWs.trim()
  const firstLinked = project?.linkedWorkspaces?.find((w) => w && w.trim() !== '.')
  if (firstLinked) return firstLinked.trim()
  return undefined
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

