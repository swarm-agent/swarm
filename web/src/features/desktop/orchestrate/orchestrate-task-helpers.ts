import type { RunningTask, MediaDeliverable, TaskOutcomeType } from './orchestrate-types'
import type { AgentModelSettings } from '../settings/swarm/types/agent-model-settings'

export interface ImpendingAgentView {
  agent: string
  count: number
  label: string
  model: string
  isOverride: boolean
}

export interface DeployImpendingConfig {
  targetAgent: 'coder' | 'plan' | 'finder' | 'image' | 'video' | 'sound'
  targetOutcomeType: TaskOutcomeType
  targetTier: 'direct' | 'discovery' | 'complex'
  resolvedModel: string
  isOverridden: boolean
  accountDefaultModel: string
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
  defaultMediaModels?: { image?: string; video?: string; audio?: string }
): ImpendingAgentView[] {
  const isOverride = Boolean(task.model && task.model.trim())

  if (programJobs.length > 0) {
    const agentCounts: Record<string, number> = {}
    programJobs.forEach((j) => {
      const def = findProgramJobDef ? findProgramJobDef(j.job_id || j.id || '') : undefined
      const agentType = def?.agent_type || j.agent_type || 'coder'
      agentCounts[agentType] = (agentCounts[agentType] || 0) + 1
    })

    return Object.entries(agentCounts).map(([agentType, count]) => {
      let resolvedModel = ''
      if (isOverride) {
        resolvedModel = task.model!.trim()
      } else {
        if (agentType === 'coder') {
          resolvedModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
        } else if (agentType === 'finder') {
          resolvedModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
        } else if (agentType === 'designer') {
          resolvedModel = agentModelSettings?.systemAgents?.designer?.model || defaultMediaModels?.image || 'Account default'
        } else {
          resolvedModel = agentModelSettings?.swarm?.action?.model || 'Account default'
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
        isOverride,
      }
    })
  }

  const agent = task.agentType || 'coder'
  let resolvedModel = ''
  if (isOverride) {
    resolvedModel = task.model!.trim()
  } else {
    if (agent === 'coder') {
      resolvedModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
    } else if (agent === 'finder') {
      resolvedModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
    } else if (agent === 'designer' || agent === 'image') {
      resolvedModel = defaultMediaModels?.image || agentModelSettings?.systemAgents?.designer?.model || 'Account default'
    } else if (agent === 'video') {
      resolvedModel = defaultMediaModels?.video || 'Account default'
    } else if (agent === 'sound' || agent === 'audio') {
      resolvedModel = defaultMediaModels?.audio || 'Account default'
    } else if (agent === 'plan') {
      resolvedModel = agentModelSettings?.swarm?.plan?.model || agentModelSettings?.swarm?.action?.model || 'Account default'
    } else {
      resolvedModel = agentModelSettings?.swarm?.action?.model || 'Account default'
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
  }
): DeployImpendingConfig {
  let targetAgent: 'coder' | 'plan' | 'finder' | 'image' | 'video' | 'sound' = 'coder'
  let targetOutcomeType: TaskOutcomeType = 'code_pr'
  let targetTier: 'direct' | 'discovery' | 'complex' = 'direct'
  let accountDefaultModel = 'Account default'

  if (taskIntent === 'code') {
    if (featureSize === 'big') {
      targetAgent = 'plan'
      targetOutcomeType = 'plan_spec'
      targetTier = 'complex'
      accountDefaultModel =
        agentModelSettings?.swarm?.plan?.model ||
        agentModelSettings?.swarm?.action?.model ||
        'Account default'
    } else {
      targetAgent = 'coder'
      targetOutcomeType = 'code_pr'
      targetTier = 'direct'
      accountDefaultModel = agentModelSettings?.systemAgents?.coder?.model || 'Account default'
    }
  } else if (taskIntent === 'audit') {
    targetAgent = 'finder'
    targetOutcomeType = 'audit_report'
    targetTier = 'discovery'
    accountDefaultModel = agentModelSettings?.systemAgents?.finder?.model || 'Account default'
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

  let resolvedModel = accountDefaultModel
  let isOverridden = false

  if (taskIntent === 'code' || taskIntent === 'audit') {
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
