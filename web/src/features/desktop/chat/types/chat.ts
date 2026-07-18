import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'

export interface ChatMessageRecord {
  id: string;
  sessionId: string;
  globalSeq: number;
  role: string;
  content: string;
  createdAt: number;
  metadata?: Record<string, unknown>;
  toolMessage?: StructuredToolMessage | null;
}

export interface EditDiffHunk {
  index: number;
  oldLines: string[];
  newLines: string[];
  oldTruncated: boolean;
  newTruncated: boolean;
}

export interface EditDiffPreview {
  oldLines: string[];
  newLines: string[];
  oldTruncated: boolean;
  newTruncated: boolean;
  hunks: EditDiffHunk[];
}

export type ToolMessageState = "done" | "running" | "error";

export interface TaskToolRow {
  launchKey?: string;
  launchIndex: number;
  childSessionId: string;
  status: string;
  phase: string;
  agent: string;
  assignmentLabel: string;
  modelLabel: string;
  tool: string;
  time: string;
  previewKind: string;
  previewText: string;
  launchStartedAtMs: number;
  currentToolStartedAtMs: number;
  elapsedMs: number;
  currentToolMs: number;
  terminal: boolean;
}

export interface SearchToolLineMatch {
  line: number;
  column: number;
  text: string;
}

export interface SearchToolLineGroup {
  query: string;
  lines: number[];
  matches: SearchToolLineMatch[];
  extraLineCount: number;
}

export interface SearchToolFileGroup {
  path: string;
  matchCount: number;
  queryGroups: SearchToolLineGroup[];
  extraQueryCount: number;
}

export interface SearchToolData {
  mode: string;
  path: string;
  queryCount: number;
  count: number;
  totalMatched: number;
  truncated: boolean;
  timedOut: boolean;
  files: SearchToolFileGroup[];
}

export interface TodoToolSummaryCounts {
  taskCount: number;
  openCount: number;
  inProgressCount: number;
}

export interface TodoToolData {
  action: string;
  ownerKind: string;
  operationCount: number;
  summary: TodoToolSummaryCounts | null;
}

export interface BashToolData {
  command: string;
  output: string;
  stdout: string;
  stderr: string;
  outputText: string;
  completedOutput: string;
  exitCode: number | null;
}

export interface TaskChildCardActions {
  workspaceSlug: string;
  parentSessionId: string;
  onNavigate: (sessionId: string, workspacePath: string) => void;
}

export interface StructuredToolMessage {
  pathId: "run.tool-history.v2" | "run.v3.provider-tool-result.v1";
  tool: string;
  callId: string;
  toolInstanceId?: string;
  target: string | null;
  commandText: string;
  argumentsText: string;
  argumentsJson?: Record<string, unknown> | null;
  output: string;
  completedOutput: string;
  error: string;
  durationMs: number;
  summary: string;
  state: ToolMessageState;
  timelineSeq?: number;
  editDiff: EditDiffPreview | null;
  searchData?: SearchToolData | null;
  todoData?: TodoToolData | null;
  bashData?: BashToolData | null;
  previewLines: string[];
  taskRows: TaskToolRow[];
}

export interface WorkspaceSessionCacheRecord<SessionRecord> {
  workspacePath: string;
  sessions: SessionRecord[];
  fetchedAt: number;
}

export interface SessionMessageCacheRecord {
  sessionId: string;
  workspacePath: string;
  messages: ChatMessageRecord[];
  lastGlobalSeq: number;
  fetchedAt: number;
}

export interface ScrollAnchorRecord {
  anchorSeq: number;
  offset: number;
  updatedAt: number;
}

export interface SessionPreferenceRecord {
  provider: string;
  model: string;
  thinking: string;
  serviceTier: string;
  contextMode: string;
  updatedAt: number;
}

export interface ModelProfileSelectionRecord {
  provider: string;
  model: string;
  thinking: string;
  serviceTier: string;
  contextMode: string;
}

export interface ModelProfileRecord {
  profileId: string;
  name: string;
  modelMode: 'single' | 'split';
  single: ModelProfileSelectionRecord | null;
  plan: ModelProfileSelectionRecord | null;
  auto: ModelProfileSelectionRecord | null;
  createdAt: number;
  updatedAt: number;
  isDefault: boolean;
}

export interface ModelProfileState {
  profiles: ModelProfileRecord[];
  defaultProfileId: string;
}

export interface ModelProfileInput {
  name: string;
  modelMode: 'single' | 'split';
  single: ModelProfileSelectionRecord | null;
  plan: ModelProfileSelectionRecord | null;
  auto: ModelProfileSelectionRecord | null;
}

export type ModelProfileChoice =
  | { kind: 'account-default' }
  | { kind: 'saved'; profileId: string }
  | { kind: 'temporary'; profile: ModelProfileInput }
  | { kind: 'agent-default' }

export interface ActiveModelProfileState {
  source: 'saved' | 'temporary' | 'agent-default' | '';
  profileId: string;
  name: string;
  modelMode: 'single' | 'split' | '';
}

export interface ResolvedSessionPreference {
  preference: SessionPreferenceRecord;
  contextWindow: number;
  maxOutputTokens: number;
}

export interface AgentModelPolicyRecord {
  agentName: string;
  resolvedAgentName: string;
  source: string;
  locked: boolean;
  reason: string;
  preference: SessionPreferenceRecord;
  contextWindow: number;
  maxOutputTokens: number;
  profileId?: string;
  profileName?: string;
  profileSource?: string;
  profileMode?: string;
}

export interface AgentToolScopeRecord {
  preset: string;
  allowTools: string[];
  denyTools: string[];
  bashPrefixes: string[];
  inheritPolicy: boolean;
}

export interface AgentToolContractToolRecord {
  enabled?: boolean;
  bashPrefixes: string[];
}

export interface AgentToolContractRecord {
  preset: string;
  inheritPolicy: boolean;
  tools: Record<string, AgentToolContractToolRecord>;
}

export interface ResolvedAgentToolRecord {
  enabled: boolean;
  bashPrefixes: string[];
  source: string;
}

export interface ResolvedAgentToolContractRecord {
  runtimeMode: string;
  rawPreset: string;
  inheritPolicy: boolean;
  availableTools: string[];
  unavailableTools: string[];
  tools: Record<string, ResolvedAgentToolRecord>;
}

export interface AgentToolContractRuntimeRecord {
  agent: string;
  rawToolContract: AgentToolContractRecord | null;
  resolved: ResolvedAgentToolContractRecord | null;
  compiledPolicy?: unknown;
  toolInventory: AgentToolInventoryRecord | null;
}

export interface AgentProfileRecord {
  name: string;
  mode: string;
  description: string;
  provider: string;
  model: string;
  thinking: string;
  modelMode: "single" | "split";
  planProvider: string;
  planModel: string;
  planThinking: string;
  planServiceTier: string;
  autoProvider: string;
  autoModel: string;
  autoThinking: string;
  autoServiceTier: string;
  prompt: string;
  runtimeMode: "plan_auto" | "read" | "readwrite" | "";
  defaultSessionMode: DesktopSessionMode;
  executionSetting: "read" | "readwrite" | "";
  exitPlanModeEnabled: boolean;
  toolScope: AgentToolScopeRecord | null;
  toolContract: AgentToolContractRecord | null;
  enabled: boolean;
  protected: boolean;
  updatedAt: number;
}

export interface ProviderDefaultsPreviewRecord {
  provider: string;
  primaryAgent: string;
  primaryModel: string;
  primaryThinking: string;
  utilityProvider: string;
  utilityModel: string;
  utilityThinking: string;
  utilityAgents: string[];
  affectedAgents: string[];
  outOfSyncAgents: string[];
  inheritingAgents: string[];
  staleInheritedAgents: string[];
  customUtilityAgents: string[];
  utilityBaselineAgents: string[];
  overwriteExplicit?: boolean;
}

export interface AgentToolInventoryToolRecord {
  name: string;
  contractName: string;
  description: string;
  group: string;
  kind: string;
}

export interface AgentToolInventoryPresetRecord {
  id: string;
  label: string;
  description: string;
  enabledTools: string[];
  disabledByDefault: string[];
  bashPrefixes: string[];
}

export interface AgentToolInventoryRecord {
  tools: AgentToolInventoryToolRecord[];
  presets: AgentToolInventoryPresetRecord[];
}

export interface AgentStateRecord {
  profiles: AgentProfileRecord[];
  activePrimary: string;
  activeSubagent: Record<string, string>;
  version: number;
  providerDefaultsPreview: ProviderDefaultsPreviewRecord | null;
  toolInventory: AgentToolInventoryRecord | null;
}

export interface ModelPricingRecord {
  currency?: string;
  input_price_per_million_tokens?: number | null;
  output_price_per_million_tokens?: number | null;
  cached_input_price_per_million_tokens?: number | null;
  reasoning_price_per_million_tokens?: number | null;
  image_input_price?: number | null;
  image_output_price?: number | null;
  audio_input_price?: number | null;
  audio_output_price?: number | null;
  is_free?: boolean | null;
  [key: string]: unknown;
}

export interface ModelThinkingMappingRecord {
  swarm_setting: string;
  provider_parameter?: string;
  provider_value?: string;
  effective_provider_value?: string;
  behavior?: string;
}

export interface ModelServiceTierMappingRecord {
  tier: string;
  swarm_setting?: string;
  provider_parameter?: string;
  provider_value?: string;
  beta_header?: string;
  request_model_path?: string;
}

export interface ModelContextModeRecord {
  mode: string;
  label?: string;
  context_window?: number;
  default?: boolean;
}

export interface ModelOptionRecord {
  key: string;
  provider: string;
  model: string;
  contextMode: string;
  label: string;
  thinking: string;
  thinkingOptions: string[];
  defaultThinking: string;
  thinkingProviderParameter: string;
  thinkingMappings: ModelThinkingMappingRecord[];
  favorite: boolean;
  contextWindow: number;
  pricing: ModelPricingRecord | null;
  serviceTiers: string[];
  defaultServiceTier: string;
  serviceTierMappings: ModelServiceTierMappingRecord[];
  contextModes: ModelContextModeRecord[];
}

export interface DesktopSessionPlanInfo {
  goal: string;
  scope: string;
  context: string;
  decisions: string[];
  constraints: string[];
  assumptions: string[];
  openQuestions: string[];
  relevantFiles: string[];
  successCriteria: string[];
  validationStrategy: string;
}

export interface DesktopSessionPlanExecutionPolicy {
  mode: string;
  shape: string;
  followupCheckpointPolicy: string;
}

export interface DesktopSessionPlanExecutionState {
  status: string;
  activeAttemptId: string;
  parentSessionId: string;
  currentSessionId: string;
  currentRunId: string;
  lastCheckpointId: string;
  lastAttemptId: string;
  lastOutcome: string;
  startedAt: number;
  updatedAt: number;
  completedAt: number;
}

export interface DesktopSessionPlanCheckpointReview {
  status: string;
  reviewerId: string;
  reviewerType: string;
  result: string;
  notes: string;
  reviewedAt: number;
}

export interface DesktopSessionPlanCheckpointRecommendation {
  decision: string;
  action: string;
  reason: string;
  actionState: string;
}

export interface DesktopSessionPlanCheckpointAttempt {
  id: string;
  checkpointId: string;
  status: string;
  outcome: string;
  runId: string;
  sessionId: string;
  parentSessionId: string;
  startedAt: number;
  completedAt: number;
  report: string;
  result: string;
  changedFiles: string[];
  validation: string[];
}

export interface DesktopSessionPlanSubtask {
  id: string;
  title: string;
  status: string;
  notes: string;
  result: string;
  startedAt: number;
  completedAt: number;
  order: number;
}

export interface DesktopSessionPlanCheckpoint {
  id: string;
  title: string;
  status: string;
  objective: string;
  tasks: string[];
  subtasks?: DesktopSessionPlanSubtask[];
  activeSubtaskId?: string;
  acceptanceCriteria: string[];
  notes: string;
  report: string;
  result: string;
  changedFiles: string[];
  validation: string[];
  attemptId: string;
  runId: string;
  sessionId: string;
  startedAt: number;
  completedAt: number;
  review: DesktopSessionPlanCheckpointReview | null;
  recommendation?: DesktopSessionPlanCheckpointRecommendation | null;
  attempts: DesktopSessionPlanCheckpointAttempt[];
  order: number;
}

export interface DesktopSessionPlanDocument {
  id: string;
  title: string;
  status: string;
  schemaVersion: string;
  revisionId: string;
  info: DesktopSessionPlanInfo;
  executionPolicy: DesktopSessionPlanExecutionPolicy | null;
  executionState: DesktopSessionPlanExecutionState | null;
  checkpoints: DesktopSessionPlanCheckpoint[];
  originalCheckpoints: DesktopSessionPlanCheckpoint[];
  activeCheckpointId: string;
  renderedText: string;
  displayText: string;
}

export interface DesktopSessionPlanRecord {
  id: string;
  title: string;
  plan: string;
  document: DesktopSessionPlanDocument | null;
  status: string;
  approvalState: string;
  updatedAt: number;
}

export interface DesktopSessionPlanRevisionRecord extends DesktopSessionPlanRecord {
  key: string;
  createdAt: number;
  priorTitle: string;
  priorPlan: string;
  diffLines: string[];
  updateSummary: string;
  updateScope: string;
  updateKind: string;
  revisionKind: string;
  restoredFromVersion: number;
  version: number;
  parentRevision: number;
  checkpoint: boolean;
}
