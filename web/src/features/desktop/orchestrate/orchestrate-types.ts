export type OrchestrateThemeId =
  // 5 Minimal Consumer Creator Variations (Dark Warm Peach & Automation)
  | 'creator_apricot'
  | 'creator_operator'
  | 'creator_sunset'
  | 'creator_linear'
  | 'creator_espresso'
  // Architectural styles
  | 'apple_glass'
  | 'swarm_tactical'
  | 'cyber_hud'
  | 'studio_synth'
  | 'minimalist_craft'
  // Legacy aliases for backward compatibility
  | 'obsidian'
  | 'cyberpunk'
  | 'emerald'
  | 'midnight'
  | 'amber'

export interface OrchestrateTheme {
  id: OrchestrateThemeId
  name: string
  category: string
  subtitle: string
  accentColor: string
  secondaryColor?: string
  bgClass: string
  panelBgClass: string
  borderClass: string
  textPrimaryClass: string
  textSecondaryClass: string
  accentBgClass: string
  accentTextClass: string
  glowClass: string
  cardBgClass: string
  cardBorderClass?: string
  cardHoverClass?: string
  tagClass: string
  panelRadius: string
  cardRadius: string
  buttonRadius: string
  fontFamily: string
  features: {
    hasAmbientGlow?: boolean
    hasScanlines?: boolean
    hasCornerScrews?: boolean
    hasCornerBrackets?: boolean
    hasVuMeters?: boolean
    hasDotGrid?: boolean
    hasNumberIndices?: boolean
    hasEnginePill?: boolean
    hasTapeReel?: boolean
    hasDynamicIsland?: boolean
    hasStatusChips?: boolean
    hasFilamentPip?: boolean
  }
  customVars: Record<string, string>
}

export interface ProjectSummary {
  id: string
  name: string
  slug: string
  description: string
  repoPath: string
  branch: string
  gitStatus: 'clean' | 'dirty' | 'diverged'
  linkedWorkspaces: string[]
  activeWorkersCount: number
  pendingDeliverablesCount: number
  runningTasksCount: number
}

export interface RunningAutomation {
  id: string
  name: string
  kind: 'interval' | 'cron' | 'trigger'
  status: 'running' | 'idle' | 'scheduled' | 'attention'
  progressPercent?: number
  currentStep?: string
  nextRun?: string
  lastRun?: string
  outputSummary?: string
  totalRuns: number
}

export interface MediaDeliverable {
  id: string
  title: string
  type: 'video' | 'image' | 'audio' | 'code' | 'report'
  previewUrl?: string
  videoAspect?: '16:9' | '9:16' | '1:1'
  duration?: string
  status: 'ready' | 'generating' | 'accepted' | 'rejected'
  createdAt: string
  author: string
  prompt?: string
  metrics?: { views?: string; tokens?: string; renderTime?: string }
}

export interface RunningTask {
  id: string
  title: string
  agentType: 'coder' | 'finder' | 'designer' | 'swarm'
  status: 'running' | 'completed' | 'needs_review' | 'blocked'
  workspaceTarget: string
  elapsed: string
  subtasks: { id: string; title: string; completed: boolean }[]
  diffPreview?: string
}

export interface OrchestratorMessage {
  id: string
  sender: 'user' | 'orchestrator'
  text: string
  timestamp: string
  actionPills?: { label: string; action: string }[]
  linkedDeliverableIds?: string[]
}
