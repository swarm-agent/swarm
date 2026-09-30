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
  primaryWorkspaceId?: string
  workspaces?: Array<{ workspace_id?: string; workspace_generation?: number; path: string; role?: string; label?: string }>
  activeWorkersCount: number
  pendingDeliverablesCount: number
  runningTasksCount: number
  projectContext?: string
  themeId?: string // canonical project theme_id; empty inherits Swarm default
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
  status: 'ready' | 'generating' | 'queued' | 'pending' | 'failed' | 'accepted' | 'rejected'
  description?: string
  createdAt: string | number
  author: string
  prompt?: string
  model?: string
  aspectRatio?: string
  resolution?: string
  durationSeconds?: number
  videoProvenance?: unknown
  metrics?: { views?: string; tokens?: string; renderTime?: string }
  parentDeliverableId?: string
  sourceMediaRef?: string
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

export type TaskOutcomeType =
  | 'code_pr'
  | 'media_bundle'
  | 'bug_patch'
  | 'audit_report'
  | 'video_story'
  | 'video_clip'
  | 'plan_spec'
  | 'general'
  | 'audio_clip'
  | string

export interface ProjectTaskScene {
  scene_number: number
  title: string
  duration_sec: number
  prompt: string
  visual_notes?: string
}

export interface TaskProgramJobSpec {
  id: string
  stage_id: string
  depends_on?: string[]
  agent_type: string
  title: string
  meta_prompt?: string
  deliverable?: string
  owned_scope?: string[]
  acceptance_criteria?: string[]
}

export interface TaskProgramStageSpec {
  id: string
  depends_on?: string[]
  dependency_evidence?: string
}

export interface TaskProgramDefinition {
  id?: string
  stages: TaskProgramStageSpec[]
  jobs: TaskProgramJobSpec[]
}

export interface TaskProgramJobRecord {
  job_id: string
  stage_id: string
  state: string
  attempt_number?: number
  child_session_id?: string
  current_session_id?: string
  current_run_id?: string
  worktree_branch?: string
  child_head?: string
  integration_state?: string
  generation_history?: Array<{
    generation: number
    session_id: string
    state: string
    started_at: number
    finished_at?: number
  }>
  blocker?: {
    code: string
    message: string
  }
}

export interface TaskProgramRecord {
  parent_session_id: string
  program_id: string
  state: string
  active_stage_id?: string
  definition: TaskProgramDefinition
  jobs: TaskProgramJobRecord[]
  blocker?: {
    code: string
    message: string
  }
}

export interface TaskSessionStateItem {
  sessionId: string
  hydrated?: boolean
  title?: string
  role?: string
  status: 'running' | 'needs_review' | 'completed' | 'failed' | 'queued' | 'blocked' | 'paused' | 'unknown'
  lastError?: string
}

export interface TaskSessionSummary {
  totalSessions: number
  runningSessions: number
  reviewSessions: number
  failedSessions: number
  completedSessions: number
  sessionStates: TaskSessionStateItem[]
}

export interface RunningTaskPlanCheckpoint {
  id: string
  title: string
  status: string
  subtasks?: Array<{
    id: string
    title: string
    status?: string
    completed?: boolean
  }>
}

export interface TaskAttempt {
  id: string
  session_id: string
  run_id?: string
  request?: string
  request_revision?: number
  client_request_id?: string
  recovery?: { session_id: string }
  role: string
  created_at?: number
  status: string
  last_error?: string
  launch_state?: string
  summary?: string
  integration?: { state: string; error?: string }
}

export interface RunningTask {
  activeAttemptId?: string
  attempts?: TaskAttempt[]
  integration?: { state: string; error?: string }

  id: string
  title: string
  subtitle?: string
  agentType: 'coder' | 'finder' | 'designer' | 'swarm' | 'video' | 'image' | 'sound' | 'audio' | 'plan'
  status: 'running' | 'in_progress' | 'completed' | 'needs_review' | 'blocked' | 'queued' | 'pending' | 'failed' | 'pending_approval' | 'planning' | 'rejected'
  outcomeType?: TaskOutcomeType
  workspaceTarget: string
  elapsed: string
  workerId?: string
  worker_id?: string
  workerRunId?: string
  worker_run_id?: string
  automationId?: string
  automation_id?: string
  workerName?: string
  worker_name?: string
  priority?: 'critical' | 'high' | 'medium' | 'low'
  tags?: string[]
  stageIndex?: number
  totalStages?: number
  subtasks: { id: string; title: string; completed: boolean }[]
  taskTodos?: import('../state/task-progress').TaskTodo[]
  handoffSummary?: string
  stepTimeline?: TaskStep[]
  diffPreview?: string
  diffLines?: DiffLine[]
  deliverables?: MediaDeliverable[]
  sessionId?: string
  createdAt?: number
  startedAt?: number
  elapsedMs?: number

  // Live session sync, plan & streaming fields
  currentFocus?: string
  currentTool?: string
  currentToolName?: string
  currentToolEventKey?: string
  toolCallCount?: number
  toolActivitySummary?: string
  activeTodo?: string
  metadata?: Record<string, any>
  /** Resolved identity from the active session view, not the task's requested agent/model. */
  activeAgent?: string
  activeProvider?: string
  activeModel?: string
  liveAssistantText?: string
  liveToolCalls?: string
  activePlanCheckpoints?: RunningTaskPlanCheckpoint[]
  activeSubtaskId?: string
  planProgressPercent?: number
  subtasksCount?: { completed: number; total: number }
  sessionIds?: string[]
  sessionSummary?: TaskSessionSummary
  associatedSessionIds?: string[]

  // Worktree & Outcome Tracking Fields
  workspacePath?: string
  sourceWorkspacePath?: string
  sourceWorkspaceId?: string
  sourceWorkspaceGeneration?: number
  sourceWorkspaceProvenance?: string
  worktreeBranch?: string
  worktreeName?: string
  baseBranch?: string
  unintegratedCommits?: number
  behindCommits?: number
  gitStatus?: string
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
  description?: string
  resolution?: string
  model?: string
  featureSize?: 'small' | 'big' | string
  feature_size?: 'small' | 'big' | string
  durationSeconds?: number
  scenes?: ProjectTaskScene[]
  soundtrack?: string
  autoApprove?: boolean
  routerAlert?: string
  router_alert?: string
  attachedMedia?: ProjectTaskMediaRef[]

  // Standalone Multi-Agent Task Program Execution
  taskProgram?: TaskProgramDefinition
  task_program?: TaskProgramDefinition
  taskProgramId?: string
  task_program_id?: string
  taskProgramStatus?: TaskProgramRecord
  task_program_status?: TaskProgramRecord

  // Structured Plan Document & Plan Binding
  planBinding?: ProjectTaskPlanBinding
  plan_binding?: ProjectTaskPlanBinding
  planDocument?: any
  plan_document?: any
}

export interface ProjectTaskPlanBinding {
  planId?: string
  definitionRevision?: number
  sessionId?: string
  receipt?: string
  // raw snake_case compatibility
  plan_id?: string
  definition_revision?: number
  session_id?: string
}

export interface BackendModelPreference {
  provider: string
  model: string
  thinking?: string
  service_tier?: string
  context_mode?: string
}

export interface BackendTaskModelPreview {
  task_id?: string
  agent: string
  resolved_agent: string
  feature_size?: string
  task_model_override?: string
  resolved_model?: BackendModelPreference | null
  model_source: 'task_override' | 'account_settings' | 'account_default' | string
  account_default_model?: BackendModelPreference | null
  account_settings_path: string
}

export interface TaskCreationPreviewResult {
  task_plan?: any
  model_preview: BackendTaskModelPreview
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
