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

export interface TaskAssemblyPart {
  name: string;
  instructions: string;
  ownedScope: string[];
}

export interface TaskToolRow {
  launchKey?: string;
  launchIndex: number;
  childSessionId: string;
  programId?: string;
  programJobId?: string;
  programStageId?: string;
  dependsOn?: string[];
  status: string;
  phase: string;
  agent: string;
  assignmentLabel: string;
  modelLabel: string;
  tool: string;
  toolActivitySummary?: string;
  liveToolCalls?: string;
  liveAssistantText?: string;
  time: string;
  previewKind: string;
  previewText: string;
  launchStartedAtMs: number;
  currentToolStartedAtMs: number;
  elapsedMs: number;
  currentToolMs: number;
  terminal: boolean;
  swarmMode?: boolean;
  swarmStrategy?: "explore" | "assembly";
  assemblyPart?: TaskAssemblyPart | null;
  integrationContract?: string;
  integrationRequired?: boolean;
}

export interface TaskProgramStage {
  id: string;
  dependsOn: string[];
  dependencyEvidence: string;
  state: "done" | "active" | "waiting" | "blocked" | "pending" | "failed";
  rows: TaskToolRow[];
}

export interface TaskProgram {
  id: string;
  state: string;
  activeStageId: string;
  nextAction: string;
  stages: TaskProgramStage[];
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
  queries: string[];
  queryCount: number;
  count: number;
  totalMatched: number;
  truncated: boolean;
  timedOut: boolean;
  files: SearchToolFileGroup[];
}

export interface WebResourceData {
  url: string;
  title: string;
  domain: string;
  author: string;
  publishedDate: string;
  summary: string;
  text: string;
  highlights: string[];
  error: string;
  status: string;
  subpages: WebResourceData[];
}

export interface WebSearchQueryData {
  query: string;
  count: number;
  searchType: string;
  timedOut: boolean;
  error: string;
  results: WebResourceData[];
}

export interface WebSearchToolData {
  queries: string[];
  queryCount: number;
  totalResults: number;
  failedQueries: number;
  truncated: boolean;
  searchType: string;
  queryResults: WebSearchQueryData[];
}

export interface WebFetchStatusData {
  id: string;
  status: string;
  source: string;
  error: string;
}

export interface WebFetchToolData {
  urls: string[];
  count: number;
  successCount: number;
  timedOut: boolean;
  truncated: boolean;
  results: WebResourceData[];
  statuses: WebFetchStatusData[];
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
  exitCode: number | null;
}

export interface TaskChildCardActions {
  workspaceSlug: string;
  parentSessionId: string;
  onNavigate: (sessionId: string, workspacePath: string) => void;
}

export interface ArtifactToolData {
  action: string;
  status: string;
  artifact?: import('../../session-v3/artifact-api').DesktopV3ArtifactCatalogEntry | null;
  reference?: import('../../session-v3/artifact-api').DesktopV3ArtifactSelection | null;
  count?: number;
}

export interface VideoToolData {
  action: string;
  status: string;
  title: string;
  activeTitle: string;
  summary: string;
  subject: string;
  sourceNames: string[];
  progress: number | null;
  projectId: string;
  revisionId: string;
  jobId: string;
  outputPreset: string;
  revisionNumber: number;
  count: number;
  durationMs: number;
  sizeBytes: number;
  width: number;
  height: number;
  language: string;
  validation: string;
}

export interface StructuredToolMessage {
  pathId: "run.tool-history.v2" | "run.v3.provider-tool-result.v1";
  tool: string;
  callId: string;
  runId?: string;
  toolInstanceId?: string;
  target: string | null;
  commandText: string;
  argumentsText: string;
  argumentsJson?: Record<string, unknown> | null;
  outputJson?: Record<string, unknown> | null;
  output: string;
  completedOutput: string;
  error: string;
  durationMs: number;
  summary: string;
  state: ToolMessageState;
  lifecycleStatus?: string;
  timelineSeq?: number;
  editDiff: EditDiffPreview | null;
  searchData?: SearchToolData | null;
  webSearchData?: WebSearchToolData | null;
  webFetchData?: WebFetchToolData | null;
  todoData?: TodoToolData | null;
  bashData?: BashToolData | null;
  artifactData?: ArtifactToolData | null;
  videoData?: VideoToolData | null;
  previewLines: string[];
  taskRows: TaskToolRow[];
  taskProgram?: TaskProgram | null;
  taskMode?: string;
  swarmStrategy?: "explore" | "assembly";
  integrationContract?: string;
  integrationRequired?: boolean;
  integrationStatus?: string;
  readyForDependentWork?: boolean;
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

export interface ModelProfileRecord extends ModelProfileSelectionRecord {
  profileId: string;
  name: string;
  createdAt: number;
  updatedAt: number;
  sortOrder: number;
  isDefault: boolean;
}

export interface ModelProfileState {
  profiles: ModelProfileRecord[];
  defaultProfileId: string;
}

export interface ModelProfileInput extends ModelProfileSelectionRecord {
  name: string;
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

export interface ModelMediaDirectionRecord {
  modality: string;
  state: 'supported' | 'unsupported' | 'unknown' | string;
  semantics: string;
  mimeTypes: string[];
  fileTypes: string[];
}

export interface ModelMediaCapabilitiesRecord {
  state: 'supported' | 'unsupported' | 'unknown' | string;
  providerSurface: string;
  credentialSurface: string;
  inputs: ModelMediaDirectionRecord[];
}

export interface ModelOptionRecord {
  key: string;
  provider: string;
  model: string;
  /** Upstream model namespace for routed providers; empty for direct providers. */
  upstreamFamily?: string;
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
  media?: ModelMediaCapabilitiesRecord | null;
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
  prompt?: string;
}

export interface DesktopPlanFinalHandoffCopyableCodeBlock {
  label: string;
  language: string;
  code: string;
}

export interface DesktopPlanFinalHandoffSuggestedPrompt {
  label: string;
  prompt: string;
}

export interface DesktopPlanFinalHandoffDetails {
  report: string;
  result: string;
  changedFiles: string[];
  validation: string[];
}

export interface DesktopPlanFinalHandoffArtifact {
  artifactId: string;
  label: string;
  description: string;
  mediaType: string;
  previewable: boolean;
  sessionId?: string;
  collectionId?: string;
  eventSeq?: number;
  kind?: string;
  category?: import('../../session-v3/artifact-api').DesktopV3ArtifactCategory;
  filename?: string;
}

export interface DesktopPlanFinalHandoff {
  schemaVersion: number;
  title: string;
  overview: string;
  impactBullets: string[];
  copyableCodeBlocks: DesktopPlanFinalHandoffCopyableCodeBlock[];
  recommendation: DesktopSessionPlanCheckpointRecommendation | null;
  suggestedPrompts: DesktopPlanFinalHandoffSuggestedPrompt[];
  pullRequestUrl: string;
  artifacts: DesktopPlanFinalHandoffArtifact[];
  details: DesktopPlanFinalHandoffDetails;
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

export interface DesktopSessionPlanArtifactReference {
  path: string;
  role: string;
  description: string;
  mediaType: string;
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
  artifacts: DesktopSessionPlanArtifactReference[];
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
  finalHandoff?: DesktopPlanFinalHandoff | null;
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
  artifacts: DesktopSessionPlanArtifactReference[];
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
