export type OrchestrateThemeId =
  // 5 Modern Unassuming Navy & Slate Themes
  | 'modern_navy'
  | 'midnight_slate'
  | 'deep_indigo'
  | 'steel_cobalt'
  | 'nordic_dark'
  // Backward compatibility aliases
  | 'apple_peach'
  | 'vision_glass'
  | 'studio_obsidian'
  | 'cupertino_mono'
  | 'sunset_titanium'
  | 'creator_apricot'
  | 'creator_operator'
  | 'creator_sunset'
  | 'creator_linear'
  | 'creator_espresso'
  | 'apple_glass'
  | 'swarm_tactical'
  | 'cyber_hud'
  | 'studio_synth'
  | 'minimalist_craft'
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

export type DeliverableThumbnailType = 'cyber_lattice' | 'neural_core' | 'orbital_data' | 'default'

export interface MediaDeliverable {
  id: string
  title: string
  type: 'video' | 'image' | 'audio' | 'code' | 'report'
  previewUrl?: string
  thumbnailType?: DeliverableThumbnailType
  videoAspect?: '16:9' | '9:16' | '1:1'
  duration?: string
  status: 'ready' | 'generating' | 'accepted' | 'rejected'
  createdAt: string
  author: string
  prompt?: string
  metrics?: { views?: string; tokens?: string; renderTime?: string }
}

export interface TaskStep {
  step: number
  label: string
  status: 'complete' | 'processing' | 'pending'
}

export interface DiffLine {
  lineNum: number
  type: 'del' | 'add' | 'normal'
  text: string
}

export interface RunningTask {
  id: string
  title: string
  subtitle?: string
  agentType: 'coder' | 'finder' | 'designer' | 'swarm'
  status: 'running' | 'completed' | 'needs_review' | 'blocked'
  workspaceTarget: string
  elapsed: string
  subtasks: { id: string; title: string; completed: boolean }[]
  stepTimeline?: TaskStep[]
  diffPreview?: string
  diffLines?: DiffLine[]
  deliverables?: MediaDeliverable[]
}

export interface VideoProgressCard {
  title: string
  progressPercent: number
  clipsLabel: string
  aspect?: string
}

export interface OrchestratorMessage {
  id: string
  sender: 'user' | 'orchestrator'
  text: string
  timestamp: string
  actionPills?: { label: string; action: string }[]
  linkedDeliverableIds?: string[]
  videoProgressCard?: VideoProgressCard
  actionButton?: { label: string; action: string }
}
