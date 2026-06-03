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
  launchIndex: number;
  childSessionId: string;
  status: string;
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

export interface StructuredToolMessage {
  pathId: "run.tool-history.v2";
  tool: string;
  callId: string;
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
  editDiff: EditDiffPreview | null;
  searchData?: SearchToolData | null;
  todoData?: TodoToolData | null;
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

export interface ResolvedSessionPreference {
  preference: SessionPreferenceRecord;
  contextWindow: number;
  maxOutputTokens: number;
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

export interface ModelOptionRecord {
  key: string;
  provider: string;
  model: string;
  contextMode: string;
  label: string;
  thinking: string;
  favorite: boolean;
  contextWindow: number;
}

export interface DesktopSessionPlanInfo {
  goal: string;
  context: string;
  decisions: string[];
  constraints: string[];
  assumptions: string[];
  openQuestions: string[];
  relevantFiles: string[];
  validationStrategy: string;
}

export interface DesktopSessionPlanCheckpoint {
  id: string;
  title: string;
  status: string;
  objective: string;
  tasks: string[];
  acceptanceCriteria: string[];
  notes: string;
  report: string;
  result: string;
  changedFiles: string[];
  validation: string[];
  order: number;
}

export interface DesktopSessionPlanDocument {
  id: string;
  title: string;
  status: string;
  schemaVersion: string;
  revisionId: string;
  info: DesktopSessionPlanInfo;
  checkpoints: DesktopSessionPlanCheckpoint[];
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
  version: number;
  parentRevision: number;
  checkpoint: boolean;
}
