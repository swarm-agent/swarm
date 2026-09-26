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

export interface ProjectTaskMediaRef {
  id: string
  title?: string
  url?: string
  mediaType?: string
  kind?: 'image' | 'video' | 'audio' | 'doc'
  filename?: string
  data?: string
  sizeBytes?: number
  createdAt?: number
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
  projectContext?: string
  primarySessionId?: string
  uploadedMedia?: ProjectTaskMediaRef[]
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
  type: 'video' | 'image' | 'audio' | 'code' | 'pr' | 'report' | 'artifact'
  previewUrl?: string
  mediaUrl?: string
  thumbnailType?: DeliverableThumbnailType
  videoAspect?: '16:9' | '9:16' | '1:1'
  duration?: string
  status: 'ready' | 'generating' | 'pending' | 'accepted' | 'rejected'
  createdAt: string | number
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

export type MiddleCanvasVariant =
  | 'matrix' // Variant 1: Compact Matrix & Drawer (High-density 100+ tasks)
  | 'kanban' // Variant 2: Mission Pipeline Kanban (Multi-column stage workflow)
  | 'fleet' // Variant 3: Autonomous Worker Fleet (Worker-centric hierarchy)
  | 'split' // Variant 4: Split Studio Console (Master list + Live Inspector)
  | 'timeline' // Variant 5: Timeline Activity Stream (Temporal flow & telemetry)

export interface DeployedWorker {
  id: string
  name: string
  role: string
  triggerKind: 'trigger' | 'cron' | 'interval'
  scheduleLabel: string
  activeJobsCount: number
  completedJobsCount: number
  status: 'active' | 'idle' | 'busy'
  currentJobTitle?: string
  assignedTaskIds: string[]
}

export type TaskOutcomeType = 'code_pr' | 'media_bundle' | 'bug_patch' | 'audit_report' | 'video_story'

export interface ProjectTaskScene {
  scene_number: number
  title: string
  duration_sec: number
  prompt: string
  visual_notes?: string
}

export interface RunningTask {
  id: string
  title: string
  subtitle?: string
  agentType: 'coder' | 'finder' | 'designer' | 'swarm' | 'video' | 'image' | 'plan'
  status: 'running' | 'in_progress' | 'completed' | 'needs_review' | 'blocked' | 'queued' | 'pending_approval' | 'planning'
  outcomeType?: TaskOutcomeType
  workspaceTarget: string
  elapsed: string
  workerId?: string
  workerName?: string
  priority?: 'critical' | 'high' | 'medium' | 'low'
  tags?: string[]
  stageIndex?: number
  totalStages?: number
  subtasks: { id: string; title: string; completed: boolean }[]
  stepTimeline?: TaskStep[]
  diffPreview?: string
  diffLines?: DiffLine[]
  deliverables?: MediaDeliverable[]
  sessionId?: string
  createdAt?: number

  // Worktree & Outcome Tracking Fields
  workspacePath?: string
  worktreeBranch?: string
  worktreeName?: string
  baseBranch?: string
  unintegratedCommits?: number
  behindCommits?: number
  isIntegrated?: boolean
  diffSummary?: string
  isDirty?: boolean
  dirtyCount?: number
  syncWarning?: string
  actionNeeded?: string
  whatDidDo?: string[]
  whatNotDone?: string[]

  // Router, Tiered Planning & Refinement Fields
  workspacesInvolved?: string[]
  contextPoolSummary?: string
  planSummary?: string
  fullPlanMarkdown?: string
  tier?: 'direct' | 'discovery' | 'complex'
  revision?: number
  lastError?: string
  feedbackHistory?: string[]
  aspectRatio?: string
  variantCount?: number
  scenes?: ProjectTaskScene[]
  soundtrack?: string
  autoApprove?: boolean
  routerAlert?: string
  router_alert?: string
  attachedMedia?: ProjectTaskMediaRef[]
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
