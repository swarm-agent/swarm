import type { DesktopPermissionRecord } from '../../types/realtime'

export interface AskUserOption {
  value: string
  label: string
  description: string
  allowCustom: boolean
}

export interface AskUserQuestion {
  id: string
  header: string
  question: string
  options: AskUserOption[]
  required: boolean
}

export interface AskUserPayload {
  title: string
  context: string
  questions: AskUserQuestion[]
}

export interface ExitPlanPayload {
  title: string
  body: string
  planId: string
}

export interface PlanUpdatePayload {
  title: string
  planId: string
  priorTitle: string
  priorPlan: string
  plan: string
  diffLines: string[]
  approvedArguments: Record<string, unknown>
}

export interface ManageTodosPreviewRow {
  text: string
  metadata: string[]
}

export interface ManageTodosPayload {
  title: string
  body: string
  isBatch: boolean
  batchRows: ManageTodosPreviewRow[]
  summaryLine: string
  approvedArguments: Record<string, unknown>
}

export interface WorkspaceScopeAction {
  decision: string
  label: string
  description: string
  available: boolean
}

export interface WorkspaceScopePayload {
  title: string
  summary: string
  toolName: string
  accessLabel: string
  requestedPath: string
  resolvedPath: string
  directoryPath: string
  workspacePath: string
  workspaceName: string
  workspaceExists: boolean
  temporaryBehavior: string
  persistentBehavior: string
  sessionAllow: WorkspaceScopeAction
  addToWorkspace: WorkspaceScopeAction
}

export interface TaskLaunchPayload {
  title: string
  subtitle: string
  summary: string
  launchCount: number
  description: string
  prompt: string
  action: string
  allowBash: boolean
  reportMaxChars: number
  parentMode: string
  effectiveChildMode: string
  subagentType: string
  resolvedAgentName: string
  resolvedAgentError: string
  disabledTools: string[]
  launches: TaskLaunchRow[]
}

export interface TaskLaunchRow {
  index: number
  requestedSubagentType: string
  resolvedAgentName: string
  resolvedAgentError: string
  assignment: string
  childTitlePreview: string
  childMode: string
  allowBash: boolean
  reportMaxChars: number
  disabledTools: string[]
}

export interface AgentChangeField {
  label: string
  value: string
}

export interface AgentChangePayload {
  title: string
  subtitle: string
  summary: string
  action: string
  target: string
  agentName: string
  purpose: string
  mode: string
  execution: string
  tools: string
  status: string
  model: string
  descriptionPreview: string
  promptPreview: string
  changes: AgentChangeField[]
}

export type DesktopPermissionKind = 'generic' | 'exit-plan' | 'plan-update' | 'manage-todos' | 'ask-user' | 'workspace-scope' | 'task-launch' | 'agent-change'

function decodePermissionArguments(raw: string): Record<string, unknown> | null {
  const trimmed = raw.trim()
  if (!trimmed.startsWith('{') || !trimmed.endsWith('}')) {
    return null
  }
  try {
    const parsed = JSON.parse(trimmed) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : null
  } catch {
    return null
  }
}

function mapStringArg(payload: Record<string, unknown>, key: string): string {
  const value = payload[key]
  return typeof value === 'string' ? value.trim() : ''
}

function mapObjectArg(payload: Record<string, unknown>, key: string): Record<string, unknown> {
  const value = payload[key]
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : {}
}

function mapBoolArg(payload: Record<string, unknown>, key: string): boolean {
  const value = payload[key]
  if (typeof value === 'boolean') {
    return value
  }
  if (typeof value === 'string') {
    return ['1', 'true', 'yes', 'y', 'on'].includes(value.trim().toLowerCase())
  }
  return false
}

function normalizePermissionPayloadValue(value: unknown, depth = 0): unknown {
  if (depth > 8) {
    return value
  }
  if (Array.isArray(value)) {
    return value.map((entry) => normalizePermissionPayloadValue(entry, depth + 1))
  }
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    const normalized: Record<string, unknown> = {}
    Object.entries(record).forEach(([key, entry]) => {
      normalized[key] = normalizePermissionPayloadValue(entry, depth + 1)
    })
    return normalized
  }
  if (typeof value !== 'string') {
    return value
  }
  const trimmed = value.trim()
  if (!(trimmed.startsWith('{') && trimmed.endsWith('}')) && !(trimmed.startsWith('[') && trimmed.endsWith(']'))) {
    return value
  }
  try {
    return normalizePermissionPayloadValue(JSON.parse(trimmed) as unknown, depth + 1)
  } catch {
    return value
  }
}

function firstNonEmptyString(...values: string[]): string {
  for (const value of values) {
    const trimmed = value.trim()
    if (trimmed) {
      return trimmed
    }
  }
  return ''
}

function defaultAskUserOptions(): AskUserOption[] {
  return [
    {
      value: '__custom__',
      label: 'Custom response',
      description: 'Type your own response.',
      allowCustom: true,
    },
  ]
}

function parseAskUserOptions(raw: unknown): AskUserOption[] {
  if (!Array.isArray(raw)) {
    return []
  }

  const options: AskUserOption[] = []
  for (const entry of raw) {
    if (typeof entry === 'string') {
      const label = entry.trim()
      if (!label) {
        continue
      }
      options.push({
        value: label,
        label,
        description: '',
        allowCustom: label.toLowerCase() === '__custom__',
      })
      continue
    }

    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      continue
    }

    const record = entry as Record<string, unknown>
    let label = mapStringArg(record, 'label')
    let value = mapStringArg(record, 'value')
    let allowCustom = mapBoolArg(record, 'allow_custom') || mapBoolArg(record, 'allowCustom')
    if (value.trim().toLowerCase() === '__custom__') {
      allowCustom = true
    }
    if (allowCustom && !value) {
      value = '__custom__'
    }
    if (allowCustom && !label) {
      label = 'Custom response'
    }
    if (!label) {
      label = value
    }
    if (!value) {
      value = label
    }
    if (!label && !value) {
      continue
    }
    options.push({
      value,
      label,
      description: mapStringArg(record, 'description'),
      allowCustom,
    })
  }

  return options
}

function parseAskUserQuestions(args: Record<string, unknown>): AskUserQuestion[] {
  const raw = args.questions
  if (!Array.isArray(raw)) {
    return []
  }

  const questions: AskUserQuestion[] = []
  for (let index = 0; index < raw.length; index += 1) {
    const entry = raw[index]
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      continue
    }

    const record = entry as Record<string, unknown>
    let question = mapStringArg(record, 'question')
    if (!question) {
      question = mapStringArg(record, 'prompt') || mapStringArg(record, 'text') || mapStringArg(record, 'title')
    }

    let required = true
    if ('required' in record) {
      const value = record.required
      if (typeof value === 'boolean') {
        required = value
      } else if (typeof value === 'string') {
        required = !['false', '0', 'no'].includes(value.trim().toLowerCase())
      }
    }

    questions.push({
      id: mapStringArg(record, 'id') || `q_${index + 1}`,
      header: mapStringArg(record, 'header'),
      question: question || 'User input requested',
      options: parseAskUserOptions(record.options),
      required,
    })
  }

  return questions
}

export function normalizePermissionToolName(raw: string): string {
  let name = raw.trim().toLowerCase()
  const dot = name.lastIndexOf('.')
  if (dot >= 0 && dot + 1 < name.length) {
    name = name.slice(dot + 1)
  }
  name = name.split('-').join('_')
  switch (name) {
    case 'askuser':
      return 'ask_user'
    case 'exitplanmode':
      return 'exit_plan_mode'
    case 'managetodos':
      return 'manage_todos'
    default:
      return name
  }
}

export function normalizePermissionSessionMode(raw: string): 'plan' | 'auto' | 'yolo' {
  switch (raw.trim().toLowerCase()) {
    case 'auto':
    case 'yolo':
      return raw.trim().toLowerCase() as 'auto' | 'yolo'
    default:
      return 'plan'
  }
}

export function permissionRequiresApproval(
  permission: Pick<DesktopPermissionRecord, 'toolName' | 'mode' | 'requirement'>,
  fallbackMode = 'plan',
): boolean {
  if (permission.requirement.trim().toLowerCase() === 'workspace_scope') {
    return true
  }
  const toolName = normalizePermissionToolName(permission.toolName)
  const mode = normalizePermissionSessionMode(permission.mode || fallbackMode)

  switch (toolName) {
    case 'read':
    case 'grep':
    case 'websearch':
    case 'webfetch':
    case 'agentic_search':
    case 'list':
    case 'skill_use':
      return false
    case 'plan_manage':
      return permission.requirement.trim().toLowerCase() === 'plan_update'
    case 'task':
      return true
    case 'ask_user':
    case 'exit_plan_mode':
      return true
    case 'write':
    case 'edit':
      return mode === 'plan'
    case 'bash':
      return mode !== 'yolo'
    default:
      return mode !== 'yolo'
  }
}

export function countApprovalRequiredPermissions(
  permissions: Array<Pick<DesktopPermissionRecord, 'toolName' | 'mode' | 'requirement'>>,
  fallbackMode = 'plan',
): number {
  return permissions.reduce(
    (count, permission) => count + (permissionRequiresApproval(permission, fallbackMode) ? 1 : 0),
    0,
  )
}

export function permissionKind(permission: DesktopPermissionRecord): DesktopPermissionKind {
  if (permission.requirement.trim().toLowerCase() === 'workspace_scope') {
    return 'workspace-scope'
  }
  switch (normalizePermissionToolName(permission.toolName)) {
    case 'exit_plan_mode':
      return 'exit-plan'
    case 'plan_manage':
      if (permission.requirement.trim().toLowerCase() === 'plan_update') {
        return 'plan-update'
      }
      return 'generic'
    case 'manage_todos':
      return 'manage-todos'
    case 'ask_user':
      return 'ask-user'
    case 'task':
      if (permission.requirement.trim().toLowerCase() === 'task_launch') {
        return 'task-launch'
      }
      return 'generic'
    case 'manage_agent':
      if (permission.requirement.trim().toLowerCase() === 'agent_change') {
        return 'agent-change'
      }
      return 'generic'
    default:
      return 'generic'
  }
}

export function permissionDisplayToolName(raw: string): string {
  const normalized = normalizePermissionToolName(raw)
  if (!normalized) {
    return 'tool'
  }
  switch (normalized) {
    case 'ask_user':
      return 'ask-user'
    case 'exit_plan_mode':
      return 'exit_plan_mode'
    case 'manage_todos':
      return 'manage_todos'
    default:
      return normalized
  }
}

export function permissionRequirementLabel(raw: string): string {
  switch (raw.trim().toLowerCase()) {
    case '':
      return 'permission'
    case 'workspace_scope':
      return 'workspace access'
    default:
      return raw.trim()
  }
}

export function parseExitPlanPermission(permission: DesktopPermissionRecord): ExitPlanPayload {
  const payload = decodePermissionArguments(permission.toolArguments)
  return {
    title: payload ? mapStringArg(payload, 'title') || 'Exit Plan Mode' : 'Exit Plan Mode',
    body:
      payload?.plan && typeof payload.plan === 'string' && payload.plan.trim() !== ''
        ? payload.plan.trim()
        : 'Review and approve this plan to switch the session from plan mode to auto mode. Once approved, execution continues; this is not a handoff to another agent.',
    planId: payload ? mapStringArg(payload, 'plan_id') || mapStringArg(payload, 'planID') : '',
  }
}

export function parsePlanUpdatePermission(permission: DesktopPermissionRecord): PlanUpdatePayload {
  const payload = decodePermissionArguments(permission.toolArguments) ?? {}
  return {
    title: mapStringArg(payload, 'title') || 'Plan',
    planId: mapStringArg(payload, 'plan_id') || mapStringArg(payload, 'id'),
    priorTitle: mapStringArg(payload, 'prior_title'),
    priorPlan: mapStringArg(payload, 'prior_plan'),
    plan: mapStringArg(payload, 'plan'),
    diffLines: mapStringArrayArg(payload, 'diff_lines'),
    approvedArguments: mapObjectArg(payload, 'approved_arguments'),
  }
}

export function parseManageTodosPermission(permission: DesktopPermissionRecord): ManageTodosPayload {
  const payload = decodePermissionArguments(permission.toolArguments) ?? {}
  const action = mapStringArg(payload, 'action').toLowerCase()
  const text = mapStringArg(payload, 'text')
  const itemId = mapStringArg(payload, 'id')
  const workspacePath = mapStringArg(payload, 'workspace_path')
  const ownerKind = (mapStringArg(payload, 'owner_kind') || 'user').toLowerCase()
  const ownerLabel = manageTodosOwnerKindLabel(ownerKind)
  const priority = mapStringArg(payload, 'priority')
  const group = mapStringArg(payload, 'group')
  const tags = mapStringArrayArg(payload, 'tags')
  const orderedIds = mapStringArrayArg(payload, 'ordered_ids')
  const sessionId = mapStringArg(payload, 'session_id')
  const parentId = mapStringArg(payload, 'parent_id')
  const done = 'done' in payload ? mapBoolArg(payload, 'done') : null
  const inProgress = 'in_progress' in payload ? mapBoolArg(payload, 'in_progress') : null
  const operations = Array.isArray(payload.operations)
    ? payload.operations.filter((entry): entry is Record<string, unknown> => !!entry && typeof entry === 'object' && !Array.isArray(entry))
    : []
  const approvedArguments = mapObjectArg(payload, 'approved_arguments')

  let title = 'Review Todo Changes'
  switch (action) {
    case 'create':
      title = 'Review Todo Changes: Create Task'
      break
    case 'update':
      title = 'Review Todo Changes: Update Task'
      break
    case 'delete':
      title = 'Review Todo Changes: Delete Task'
      break
    case 'in_progress':
      title = 'Review Todo Changes: In Progress Task'
      break
    case 'reorder':
      title = 'Review Todo Changes: Reorder Tasks'
      break
    case 'batch':
      title = 'Review Todo Changes: Atomic Batch'
      break
  }

  const lines: string[] = ['Approve this request to change workspace todos.', '']

  if (action === 'batch') {
    const batchRows = operations.map((operation) => manageTodosBatchOperationPreview({ owner_kind: ownerKind, ...operation }))
    const targetLabel = ownerLabel || 'workspace todos'
    const summaryLine = workspacePath
      ? `Atomic batch for \`${workspacePath}\` on ${targetLabel} with \`${operations.length}\` ${operations.length === 1 ? 'operation' : 'operations'}.`
      : `Atomic batch on ${targetLabel} with \`${operations.length}\` ${operations.length === 1 ? 'operation' : 'operations'}.`
    lines.push('## Atomic batch preview', '')
    if (batchRows.length === 0) {
      lines.push('No operations were provided.')
    } else {
      batchRows.forEach((row, index) => {
        lines.push(row.text)
        row.metadata.forEach((entry) => {
          lines.push(`meta: ${entry}`)
        })
        if (index < batchRows.length - 1) {
          lines.push('')
        }
      })
    }
    lines.push('', summaryLine)
    return {
      title,
      body: lines.join('\n').trim(),
      isBatch: true,
      batchRows,
      summaryLine,
      approvedArguments,
    }
  }

  lines.push('## Task preview', '')

  if (action === 'create' || action === 'update') {
    const checkbox = done ? '[x]' : '[ ]'
    const label = text || (itemId ? `todo \`${itemId}\`` : 'todo change')
    lines.push(`- ${checkbox} ${label}`)
  } else if (action === 'delete') {
    lines.push(itemId ? `- Delete task \`${itemId}\`` : '- Delete a task')
  } else if (action === 'in_progress') {
    lines.push(itemId ? `- Mark task \`${itemId}\` in progress` : '- Change in-progress task')
  } else if (action === 'reorder') {
    lines.push('- Reorder workspace tasks')
  } else {
    lines.push('- Update workspace tasks')
  }

  if (ownerLabel) lines.push(`- List: \`${ownerLabel}\``)
  if (priority && ownerKind !== 'agent') lines.push(`- Priority: \`${priority}\``)
  if (group) lines.push(`- Group: \`${group}\``)
  if (tags.length > 0) lines.push(`- Tags: ${tags.map((tag) => `\`#${tag}\``).join(' ')}`)
  if (sessionId) lines.push(`- Conversation: \`${sessionId}\``)
  if (parentId) lines.push(`- Parent Task: \`${parentId}\``)
  if (inProgress !== null) lines.push(`- In Progress: \`${inProgress ? 'yes' : 'no'}\``)
  if (done !== null && action !== 'create' && action !== 'update') lines.push(`- Done: \`${done ? 'yes' : 'no'}\``)

  if (orderedIds.length > 0) {
    lines.push('', '## Requested order', '')
    orderedIds.forEach((orderedId, index) => {
      lines.push(`${index + 1}. \`${orderedId}\``)
    })
  }

  lines.push('', '## Details', '')
  if (action) lines.push(`- action: \`${action}\``)
  if (workspacePath) lines.push(`- workspace: \`${workspacePath}\``)
  if (ownerLabel) lines.push(`- owner_kind: \`${ownerLabel}\``)
  if (itemId) lines.push(`- id: \`${itemId}\``)
  if (action === 'update' && !('done' in payload)) lines.push('- done: _unchanged_')
  if (action === 'update' && !text) lines.push('- text: _unchanged_')

  return {
    title,
    body: lines.join('\n').trim(),
    isBatch: false,
    batchRows: [],
    summaryLine: '',
    approvedArguments,
  }
}

export function parseTaskLaunchPermission(permission: DesktopPermissionRecord): TaskLaunchPayload {
  const payload = decodePermissionArguments(permission.toolArguments) ?? {}
  const launches = parseTaskLaunchRows(payload)
  const launchCount = Math.max(0, mapNumberArg(payload, 'launch_count')) || launches.length
  const summary =
    launchCount === 1
      ? 'Review this subagent launch before it starts. Bypass permissions does not skip this review.'
      : `Review these ${taskLaunchSummaryLabel(launchCount)} before they start. Bypass permissions does not skip this review.`
  const subtitleParts = [taskLaunchSummaryLabel(launchCount)]
  const childMode = mapStringArg(payload, 'effective_child_mode')
  if (childMode) {
    subtitleParts.push(`child mode ${childMode}`)
  }

  return {
    title: 'Review Task Launch',
    subtitle: subtitleParts.join(' · '),
    summary,
    launchCount,
    description: mapStringArg(payload, 'description') || mapStringArg(payload, 'goal') || 'Delegated task',
    prompt: mapStringArg(payload, 'prompt'),
    action: mapStringArg(payload, 'action') || 'spawn',
    allowBash: mapBoolArg(payload, 'allow_bash'),
    reportMaxChars: mapNumberArg(payload, 'report_max_chars'),
    parentMode: mapStringArg(payload, 'parent_mode'),
    effectiveChildMode: childMode,
    subagentType: mapStringArg(payload, 'subagent_type'),
    resolvedAgentName: mapStringArg(payload, 'resolved_agent_name'),
    resolvedAgentError: mapStringArg(payload, 'resolved_agent_error'),
    disabledTools: mapStringArrayArg(payload, 'disabled_tools'),
    launches,
  }
}


export function parseAgentChangePermission(permission: DesktopPermissionRecord): AgentChangePayload {
  const payload = decodePermissionArguments(permission.toolArguments) ?? {}
  const change = mapObjectArg(payload, 'change')
  const action = firstNonEmptyString(mapStringArg(payload, 'action'), mapStringArg(change, 'operation'), 'change')
  const target = firstNonEmptyString(mapStringArg(change, 'target'), mapStringArg(payload, 'target'), 'agent state')
  const before = normalizePermissionPayloadValue(change.before)
  const after = normalizePermissionPayloadValue(change.after)
  const beforeProfile = asRecord(before)
  const afterProfile = asRecord(after)
  const agentRecord = afterProfile ?? beforeProfile ?? asRecord(payload.agent) ?? {}
  const snapshotProfile = action === 'delete' ? (beforeProfile ?? afterProfile ?? {}) : (afterProfile ?? beforeProfile ?? {})
  const agentName = firstNonEmptyString(
    mapStringArg(agentRecord, 'name'),
    mapStringArg(payload, 'agent'),
    mapStringArg(payload, 'name'),
    mapStringArg(change, 'agent'),
  )
  const mode = target === 'agent_profile' ? agentProfileMode(snapshotProfile) : ''
  const execution = target === 'agent_profile' ? agentProfileExecution(snapshotProfile) : ''
  const tools = target === 'agent_profile' ? agentProfileTools(snapshotProfile) : ''
  const status = target === 'agent_profile' ? agentProfileStatus(snapshotProfile) : ''
  const model = target === 'agent_profile' ? agentProfileModel(snapshotProfile) : ''
  const descriptionPreview = target === 'agent_profile' ? agentProfileDescription(snapshotProfile) : ''
  const promptPreview =
    target === 'agent_profile' && (action === 'create' || action === 'update')
      ? mapStringArg(afterProfile ?? {}, 'prompt')
      : ''
  const purpose = firstNonEmptyString(mapStringArg(payload, 'purpose'), mapStringArg(change, 'purpose'))
  const subtitleParts = [agentName ? `@${agentName}` : '', mode ? `mode ${mode}` : '', execution]
    .map((value) => value.trim())
    .filter(Boolean)
  const summary =
    mapStringArg(payload, 'summary') || buildAgentChangeSummary(action, target, agentName, purpose, mode, execution, tools)

  return {
    title: 'Review Agent Change',
    subtitle: subtitleParts.join(' · ') || 'Review this manage-agent change before applying it',
    summary,
    action,
    target,
    agentName,
    purpose,
    mode,
    execution,
    tools,
    status,
    model,
    descriptionPreview,
    promptPreview,
    changes: buildAgentChangeFields(action, target, before, after, purpose),
  }
}

function buildAgentChangeSummary(
  action: string,
  target: string,
  agentName: string,
  purpose: string,
  mode: string,
  execution: string,
  tools: string,
): string {
  let actionLabel = capitalizeLabel(action || 'change')
  if (action.trim().toLowerCase() === 'create') {
    actionLabel = 'Create'
  } else if (action.trim().toLowerCase() === 'update') {
    actionLabel = 'Update'
  } else if (action.trim().toLowerCase() === 'delete') {
    actionLabel = 'Delete'
  }
  if (target === 'agent_profile') {
    const parts = [agentName ? `@${agentName}` : 'agent', mode, execution, tools].filter(Boolean)
    return `${actionLabel} ${parts.join(' · ')}`.trim()
  }
  if (target === 'active_primary') {
    return agentName ? `${actionLabel} active primary → @${agentName}` : `${actionLabel} active primary`
  }
  if (target === 'active_subagent') {
    const purposeLabel = purpose ? `${purpose} router` : 'subagent router'
    return agentName ? `${actionLabel} ${purposeLabel} → @${agentName}` : `${actionLabel} ${purposeLabel}`
  }
  return agentName ? `${actionLabel} @${agentName}` : actionLabel
}

function buildAgentChangeFields(
  action: string,
  target: string,
  before: unknown,
  after: unknown,
  purpose: string,
): AgentChangeField[] {
  if (target !== 'agent_profile') {
    return buildGenericAgentChangeFields(action, target, before, after, purpose)
  }
  const beforeProfile = asRecord(before) ?? {}
  const afterProfile = asRecord(after) ?? {}
  if (action === 'create') {
    const fields: AgentChangeField[] = [{ label: 'Result', value: 'A new saved agent profile will be created.' }]
    if (mapStringArg(afterProfile, 'description')) {
      fields.push({ label: 'Description', value: 'Set' })
    }
    if (mapStringArg(afterProfile, 'prompt')) {
      fields.push({ label: 'Prompt', value: 'Set' })
    }
    return fields
  }
  if (action === 'delete') {
    return [{ label: 'Result', value: 'This saved agent profile will be deleted.' }]
  }
  const fields: AgentChangeField[] = []
  pushAgentProfileChange(fields, 'Mode', agentProfileMode(beforeProfile), agentProfileMode(afterProfile))
  pushAgentProfileChange(fields, 'Execution', agentProfileExecution(beforeProfile), agentProfileExecution(afterProfile))
  pushAgentProfileChange(fields, 'Tools', agentProfileTools(beforeProfile), agentProfileTools(afterProfile))
  pushAgentProfileChange(fields, 'Status', agentProfileStatus(beforeProfile), agentProfileStatus(afterProfile))
  pushAgentProfileChange(fields, 'Model', agentProfileModel(beforeProfile), agentProfileModel(afterProfile))
  pushAgentProfileChange(fields, 'Thinking', mapStringArg(beforeProfile, 'thinking'), mapStringArg(afterProfile, 'thinking'))
  pushAgentTextChange(fields, 'Description', mapStringArg(beforeProfile, 'description'), mapStringArg(afterProfile, 'description'))
  pushAgentTextChange(fields, 'Prompt', mapStringArg(beforeProfile, 'prompt'), mapStringArg(afterProfile, 'prompt'))
  if (fields.length === 0) {
    fields.push({ label: 'Result', value: 'Saved agent settings stay effectively the same.' })
  }
  return fields
}

function buildGenericAgentChangeFields(
  action: string,
  target: string,
  before: unknown,
  after: unknown,
  purpose: string,
): AgentChangeField[] {
  const fields: AgentChangeField[] = []
  const beforeText = formatAgentChangeValue(before)
  const afterText = formatAgentChangeValue(after)
  if (target === 'active_primary') {
    fields.push({ label: 'Primary agent', value: joinChange(beforeText, afterText) })
    return fields
  }
  if (target === 'active_subagent') {
    fields.push({ label: 'Purpose', value: purpose || 'subagent router' })
    if (action === 'remove_active_subagent') {
      fields.push({ label: 'Result', value: beforeText ? `${beforeText} will be cleared` : 'Assignment will be cleared' })
      return fields
    }
    fields.push({ label: 'Assignment', value: joinChange(beforeText, afterText) })
    return fields
  }
  fields.push({ label: 'Before', value: beforeText || 'No prior state' })
  fields.push({ label: 'After', value: afterText || 'No resulting state' })
  return fields
}

function pushAgentProfileChange(fields: AgentChangeField[], label: string, before: string, after: string) {
  if (before.trim() === after.trim()) {
    return
  }
  fields.push({ label, value: joinChange(before, after) })
}

function pushAgentTextChange(fields: AgentChangeField[], label: string, before: string, after: string) {
  if (before.trim() === after.trim()) {
    return
  }
  let value = 'Updated'
  if (!before.trim() && after.trim()) {
    value = 'Set'
  } else if (before.trim() && !after.trim()) {
    value = 'Cleared'
  }
  fields.push({ label, value })
}

function joinChange(before: string, after: string): string {
  const beforeValue = before.trim() || 'unset'
  const afterValue = after.trim() || 'unset'
  if (beforeValue === afterValue) {
    return afterValue
  }
  return `${beforeValue} → ${afterValue}`
}

function agentProfileMode(profile: Record<string, unknown>): string {
  return mapStringArg(profile, 'mode') || 'unset'
}

function agentProfileExecution(profile: Record<string, unknown>): string {
  if (mapBoolArg(profile, 'exit_plan_mode_enabled')) {
    return 'plan → auto'
  }
  return mapStringArg(profile, 'execution_setting') || 'unset'
}

function agentProfileTools(profile: Record<string, unknown>): string {
  const scope = mapObjectArg(profile, 'tool_scope')
  const allowTools = mapStringArrayArg(scope, 'allow_tools')
  const denyTools = mapStringArrayArg(scope, 'deny_tools')
  const bashPrefixes = mapStringArrayArg(scope, 'bash_prefixes')
  const preset = mapStringArg(scope, 'preset')
  if (allowTools.length > 0) {
    return `limited to ${allowTools.join(', ')}`
  }
  if (denyTools.length > 0) {
    return `removed: ${denyTools.join(', ')}`
  }
  if (bashPrefixes.length > 0) {
    return 'bash restricted'
  }
  if (preset) {
    return `preset ${preset}`
  }
  return 'all enabled'
}

function agentProfileStatus(profile: Record<string, unknown>): string {
  return mapBoolArg(profile, 'enabled') ? 'enabled' : 'disabled'
}

function agentProfileModel(profile: Record<string, unknown>): string {
  const provider = mapStringArg(profile, 'provider')
  const model = mapStringArg(profile, 'model')
  if (!provider && !model) {
    return ''
  }
  return [provider, model].filter(Boolean).join(' / ')
}

function agentProfileDescription(profile: Record<string, unknown>): string {
  return mapStringArg(profile, 'description')
}

function formatAgentChangeValue(value: unknown): string {
  if (value == null) {
    return ''
  }
  if (typeof value === 'string') {
    return value.trim()
  }
  if (typeof value === 'number' || typeof value === 'boolean') {
    return String(value)
  }
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null
}

function capitalizeLabel(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return ''
  }
  return trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
}

function parseTaskLaunchRows(payload: Record<string, unknown>): TaskLaunchRow[] {
  const raw = payload.launches
  if (!Array.isArray(raw)) {
    return []
  }
  return raw
    .map((entry, index) => {
      if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
        return null
      }
      const record = entry as Record<string, unknown>
      const requestedSubagentType = firstNonEmptyString(
        mapStringArg(record, 'requested_subagent_type'),
        mapStringArg(record, 'resolved_agent_name'),
        'subagent',
      )
      return {
        index: mapNumberArg(record, 'launch_index') || mapNumberArg(record, 'index') || index + 1,
        requestedSubagentType,
        resolvedAgentName: firstNonEmptyString(mapStringArg(record, 'resolved_agent_name'), requestedSubagentType),
        resolvedAgentError: mapStringArg(record, 'resolved_agent_error'),
        assignment: firstNonEmptyString(
          mapStringArg(record, 'meta_prompt'),
          mapStringArg(record, 'description'),
          mapStringArg(record, 'prompt'),
          'No launch-specific instructions.',
        ),
        childTitlePreview: mapStringArg(record, 'child_title_preview'),
        childMode: mapStringArg(record, 'effective_child_mode'),
        allowBash: mapBoolArg(record, 'allow_bash'),
        reportMaxChars: mapNumberArg(record, 'report_max_chars'),
        disabledTools: mapStringArrayArg(record, 'disabled_tools'),
      }
    })
    .filter((entry): entry is TaskLaunchRow => entry !== null)
}

function mapNumberArg(payload: Record<string, unknown>, key: string): number {
  const value = payload[key]
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value
  }
  if (typeof value === 'string') {
    const parsed = Number.parseInt(value.trim(), 10)
    if (Number.isFinite(parsed)) {
      return parsed
    }
  }
  return 0
}

function mapStringArrayArg(payload: Record<string, unknown>, key: string): string[] {
  const value = payload[key]
  if (!Array.isArray(value)) {
    return []
  }
  return value
    .map((entry) => (typeof entry === 'string' ? entry.trim() : ''))
    .filter((entry) => entry !== '')
}

function manageTodosOwnerKindLabel(ownerKind: string): string {
  const normalizedOwnerKind = (ownerKind || 'user').trim().toLowerCase()
  switch (normalizedOwnerKind) {
    case 'agent':
      return 'Agent Checklist'
    case 'user':
      return 'User Todos'
    default:
      return ''
  }
}

function manageTodosBatchOperationPreview(payload: Record<string, unknown>): ManageTodosPreviewRow {
  const action = mapStringArg(payload, 'action').toLowerCase()
  const itemId = mapStringArg(payload, 'id')
  const text = mapStringArg(payload, 'text')
  return {
    text: manageTodosOperationRow(action, itemId, text, payload),
    metadata: manageTodosOperationMetadata(action, itemId, payload),
  }
}

function manageTodosOperationRow(
  action: string,
  itemId: string,
  text: string,
  payload: Record<string, unknown>,
): string {
  const done = 'done' in payload ? mapBoolArg(payload, 'done') : false
  const checkbox = done ? '[x]' : '[ ]'
  switch (action) {
    case 'create':
      return `${checkbox} ${text || 'New task'}`
    case 'update':
      if (text) return `${checkbox} ${text}`
      return itemId ? `${checkbox} Update ${itemId}` : `${checkbox} Update task`
    case 'delete':
      return itemId ? `${checkbox} Delete ${itemId}` : `${checkbox} Delete task`
    case 'in_progress':
      return itemId ? `${checkbox} Mark ${itemId} in progress` : `${checkbox} Mark task in progress`
    case 'reorder': {
      const orderedIds = mapStringArrayArg(payload, 'ordered_ids')
      return orderedIds.length > 0 ? `${checkbox} Reorder ${orderedIds.length} tasks` : `${checkbox} Reorder tasks`
    }
    default:
      if (text) return `${checkbox} ${text}`
      if (itemId) return `${checkbox} Todo ${itemId}`
      return action ? `${checkbox} ${action}` : `${checkbox} Todo change`
  }
}

function manageTodosOperationMetadata(
  action: string,
  itemId: string,
  payload: Record<string, unknown>,
): string[] {
  const metadata: string[] = []
  if (action) metadata.push(`action=${action}`)

  if (action === 'update' && itemId) {
    metadata.push(`id=${itemId}`)
  }

  const ownerKind = mapStringArg(payload, 'owner_kind').trim().toLowerCase()
  const priority = mapStringArg(payload, 'priority')
  if (priority && ownerKind !== 'agent') metadata.push(`priority=${priority}`)

  const group = mapStringArg(payload, 'group')
  if (group) metadata.push(`group=${group}`)

  const tags = mapStringArrayArg(payload, 'tags')
  if (tags.length > 0) metadata.push(`tags=${tags.map((tag) => `#${tag}`).join(', ')}`)

  const ownerLabel = manageTodosOwnerKindLabel(mapStringArg(payload, 'owner_kind'))
  if (ownerLabel) metadata.push(`owner_kind=${ownerLabel}`)

  const sessionId = mapStringArg(payload, 'session_id')
  if (sessionId) metadata.push(`session_id=${sessionId}`)

  const parentId = mapStringArg(payload, 'parent_id')
  if (parentId) metadata.push(`parent_id=${parentId}`)

  if ('in_progress' in payload) {
    metadata.push(`in_progress=${mapBoolArg(payload, 'in_progress') ? 'yes' : 'no'}`)
  }

  if ('done' in payload) {
    metadata.push(`done=${mapBoolArg(payload, 'done') ? 'yes' : 'no'}`)
  }

  if (action === 'reorder') {
    const orderedIds = mapStringArrayArg(payload, 'ordered_ids')
    if (orderedIds.length > 0) {
      metadata.push(`items=${orderedIds.length}`)
    }
  }

  return metadata
}

function taskLaunchSummaryLabel(count: number): string {
  return `${count} ${count === 1 ? 'launch' : 'launches'}`
}

export function parseAskUserPermission(permission: DesktopPermissionRecord): AskUserPayload {
  const payload = decodePermissionArguments(permission.toolArguments)
  if (!payload) {
    return {
      title: 'Ask User',
      context: '',
      questions: [
        {
          id: 'q_1',
          header: '',
          question: 'User input requested',
          options: defaultAskUserOptions(),
          required: true,
        },
      ],
    }
  }

  let questions = parseAskUserQuestions(payload)
  if (questions.length === 0) {
    questions = [
      {
        id: 'q_1',
        header: '',
        question: mapStringArg(payload, 'question') || 'User input requested',
        options: parseAskUserOptions(payload.options),
        required: true,
      },
    ]
  }

  questions = questions.map((question, index) => ({
    ...question,
    id: question.id || `q_${index + 1}`,
    question: question.question || 'User input requested',
    options: question.options.length > 0 ? question.options : defaultAskUserOptions(),
  }))

  return {
    title: mapStringArg(payload, 'title') || 'Ask User',
    context: mapStringArg(payload, 'context'),
    questions,
  }
}

function workspaceScopeAccessLabelForTool(toolName: string): string {
  switch (normalizePermissionToolName(toolName)) {
    case 'read':
    case 'list':
    case 'grep':
    case 'agentic_search':
      return 'read access'
    default:
      return 'access'
  }
}

function parseWorkspaceScopeAction(
  payload: Record<string, unknown>,
  defaults: WorkspaceScopeAction,
): WorkspaceScopeAction {
  return {
    decision: mapStringArg(payload, 'decision') || defaults.decision,
    label: mapStringArg(payload, 'label') || defaults.label,
    description: mapStringArg(payload, 'description') || defaults.description,
    available: 'available' in payload ? mapBoolArg(payload, 'available') : defaults.available,
  }
}

export function parseWorkspaceScopePermission(permission: DesktopPermissionRecord): WorkspaceScopePayload {
  const payload = decodePermissionArguments(permission.toolArguments) ?? {}
  const tool = mapObjectArg(payload, 'tool')
  const request = mapObjectArg(payload, 'request')
  const workspace = mapObjectArg(payload, 'workspace')
  const actions = mapObjectArg(payload, 'actions')

  const toolName = firstNonEmptyString(mapStringArg(tool, 'name'), permission.toolName)
  const accessLabel = firstNonEmptyString(
    mapStringArg(request, 'access_label'),
    workspaceScopeAccessLabelForTool(toolName),
  )
  const requestedPath = mapStringArg(request, 'requested_path')
  const resolvedPath = mapStringArg(request, 'resolved_target_path')
  const directoryPath = firstNonEmptyString(mapStringArg(request, 'directory_path'), resolvedPath, requestedPath)
  const workspaceExists = mapBoolArg(workspace, 'exists')
  const workspacePath = mapStringArg(workspace, 'path')
  const workspaceName = mapStringArg(workspace, 'name')
  const temporaryBehavior = firstNonEmptyString(
    mapStringArg(request, 'temporary_behavior'),
    `Approving this allows ${accessLabel} to ${directoryPath || 'the requested directory'} for this chat session only. It does not save or change the workspace.`,
  )
  const persistentBehavior = firstNonEmptyString(
    mapStringArg(workspace, 'persistent_behavior'),
    workspaceExists
      ? `You can instead add ${directoryPath || 'the requested directory'} to the saved workspace. Future access inside that workspace stops asking for permission.`
      : 'No saved workspace is active for this session, so permanent add-dir access is not available here.',
  )

  return {
    title: mapStringArg(payload, 'title') || `Allow ${accessLabel} outside the current workspace?`,
    summary: firstNonEmptyString(
      mapStringArg(payload, 'summary'),
      workspaceExists
        ? `Allow ${accessLabel} for this chat session only, or add the directory to the saved workspace permanently.`
        : `Allow ${accessLabel} for this chat session only.`,
    ),
    toolName,
    accessLabel,
    requestedPath,
    resolvedPath,
    directoryPath,
    workspacePath,
    workspaceName,
    workspaceExists,
    temporaryBehavior,
    persistentBehavior,
    sessionAllow: parseWorkspaceScopeAction(mapObjectArg(actions, 'session_allow'), {
      decision: 'session_allow',
      label: 'Allow This Session',
      description: temporaryBehavior,
      available: true,
    }),
    addToWorkspace: parseWorkspaceScopeAction(mapObjectArg(actions, 'workspace_add_dir'), {
      decision: 'workspace_add_dir',
      label: 'Add To Workspace',
      description: persistentBehavior,
      available: workspaceExists,
    }),
  }
}

export function buildWorkspaceScopeResolutionReason(decision: string): string {
  const normalizedDecision = decision.trim().toLowerCase() === 'workspace_add_dir'
    ? 'workspace_add_dir'
    : 'session_allow'
  return JSON.stringify({
    path_id: 'permission.workspace_scope.decision.v1',
    decision: normalizedDecision,
  })
}

export function buildAskUserResolutionReason(payload: AskUserPayload, answers: Record<string, string>): string | null {
  if (payload.questions.length === 0) {
    return ''
  }

  const normalizedAnswers: Record<string, string> = {}
  const ordered: Array<{ id: string; question: string; answer: string }> = []
  for (const question of payload.questions) {
    const id = question.id.trim()
    const answer = (answers[id] ?? '').trim()
    if (!answer && question.required) {
      return null
    }
    if (id) {
      normalizedAnswers[id] = answer
    }
    ordered.push({
      id,
      question: question.question.trim(),
      answer,
    })
  }

  if (payload.questions.length === 1) {
    return (normalizedAnswers[payload.questions[0].id.trim()] ?? '').trim()
  }

  return JSON.stringify({
    path_id: 'tool.ask-user.ui.v1',
    answers: normalizedAnswers,
    items: ordered,
  })
}

export function prettyPermissionArguments(raw: string): string {
  const trimmed = raw.trim()
  if (!trimmed) {
    return '{}'
  }
  try {
    return JSON.stringify(normalizePermissionPayloadValue(JSON.parse(trimmed) as unknown), null, 2)
  } catch {
    return trimmed
  }
}

function permissionPreferredArgumentKeys(toolName: string): string[] {
  switch (normalizePermissionToolName(toolName)) {
    case 'bash':
      return ['command', 'workdir', 'justification', 'sandbox_permissions', 'prefix_rule', 'timeout_ms', 'yield_time_ms', 'max_output_tokens', 'shell', 'login', 'tty']
    case 'read':
      return ['path', 'line_start', 'max_lines']
    case 'write':
      return ['path', 'append', 'content']
    case 'edit':
      return ['path', 'old_string', 'new_string', 'replace_all']
    case 'grep':
      return ['pattern', 'path', 'max_results', 'timeout_ms']
    case 'glob':
      return ['pattern', 'path', 'max_results', 'timeout_ms']
    case 'list':
      return ['path', 'pattern', 'max_results', 'cursor']
    case 'websearch':
      return ['query', 'queries', 'search_type', 'include_domains', 'max_results', 'recency_days']
    case 'webfetch':
      return ['urls', 'retrieval_mode', 'timeout_ms']
    case 'task':
      return ['description', 'prompt', 'subagent_type', 'max_steps']
    case 'task_launch':
      return ['goal', 'description', 'prompt', 'launch_count', 'launches']
    default:
      return []
  }
}

function orderedPermissionArguments(toolName: string, payload: Record<string, unknown>): Array<[string, unknown]> {
  const ordered: Array<[string, unknown]> = []
  const seen = new Set<string>()
  const add = (key: string) => {
    const normalizedKey = key.trim()
    if (!normalizedKey || seen.has(normalizedKey) || !(normalizedKey in payload)) {
      return
    }
    seen.add(normalizedKey)
    ordered.push([normalizedKey, payload[normalizedKey]])
  }

  permissionPreferredArgumentKeys(toolName).forEach(add)
  Object.keys(payload).sort((left, right) => left.localeCompare(right)).forEach(add)
  return ordered
}

function bashSavedRulePrefix(rule: DesktopPermissionRecord['savedRule']): string {
  if (!rule || rule.kind?.trim().toLowerCase() !== 'bash_prefix') {
    return ''
  }
  return (rule.pattern || '').trim()
}

function permissionArgumentLabel(key: string): string {
  switch (key.trim().toLowerCase()) {
    case 'command':
      return 'Command'
    case 'workdir':
      return 'Working directory'
    case 'justification':
      return 'Justification'
    case 'sandbox_permissions':
      return 'Sandbox permissions'
    case 'prefix_rule':
      return 'Prefix rule'
    case 'timeout_ms':
      return 'Timeout'
    case 'yield_time_ms':
      return 'Yield time'
    case 'max_output_tokens':
      return 'Max output tokens'
    case 'line_start':
      return 'Start line'
    case 'max_lines':
      return 'Max lines'
    case 'old_string':
      return 'Old text'
    case 'new_string':
      return 'New text'
    case 'replace_all':
      return 'Replace all'
    case 'include_domains':
      return 'Include domains'
    case 'subagent_type':
      return 'Subagent type'
    case 'max_steps':
      return 'Max steps'
    case 'launch_count':
      return 'Launch count'
    default:
      return key
        .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
        .split('_')
        .join(' ')
        .trim()
        .replace(/\s+/g, ' ')
        .replace(/\b\w/g, (char) => char.toUpperCase())
    }
}

function inlinePermissionValue(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return '(empty)'
  }
  if (trimmed.includes('`')) {
    return trimmed
  }
  return `\`${trimmed}\``
}

function stripWrappingBackticks(value: string): string {
  let trimmed = value.trim()
  if (!trimmed) {
    return ''
  }

  const fenceMatch = trimmed.match(/^```[^\n`]*\n([\s\S]*?)\n```$/)
  if (fenceMatch) {
    trimmed = fenceMatch[1].trim()
  }

  if (trimmed.length >= 2 && trimmed.startsWith('`') && trimmed.endsWith('`')) {
    trimmed = trimmed.slice(1, -1).trim()
  }

  return trimmed
}

function permissionScalarValue(key: string, value: string | number | boolean | null): string {
  if (value === null) {
    return '`null`'
  }
  if (typeof value === 'boolean') {
    return value ? '`true`' : '`false`'
  }
  if (typeof value === 'number') {
    if (key.endsWith('_ms')) {
      return `\`${value} ms\``
    }
    return `\`${value}\``
  }
  return inlinePermissionValue(value)
}

function permissionArgumentCodeLanguage(toolName: string, key: string): string {
  const normalizedTool = normalizePermissionToolName(toolName)
  const normalizedKey = key.trim().toLowerCase()
  if (normalizedTool === 'bash' && normalizedKey === 'command') {
    return 'bash'
  }
  if (normalizedKey === 'content' || normalizedKey === 'old_string' || normalizedKey === 'new_string' || normalizedKey === 'prompt') {
    return 'text'
  }
  return ''
}

function shouldRenderPermissionArgumentBlock(key: string, value: string): boolean {
  const normalizedKey = key.trim().toLowerCase()
  if (!value.trim()) {
    return false
  }
  switch (normalizedKey) {
    case 'content':
    case 'old_string':
    case 'new_string':
    case 'prompt':
      return true
    default:
      return value.includes('\n')
  }
}

function permissionArgumentBlockMarkdown(toolName: string, key: string, value: unknown): string {
  const label = permissionArgumentLabel(key)
  const normalizedTool = normalizePermissionToolName(toolName)
  const normalizedKey = key.trim().toLowerCase()
  if (typeof value === 'string') {
    const trimmed = normalizedTool === 'bash' && normalizedKey === 'command'
      ? stripWrappingBackticks(value)
      : value.trim()
    if (!trimmed) {
      return `- **${label}:** (empty)`
    }
    if (normalizedTool === 'bash' && normalizedKey === 'command') {
      return [`**${label}**`, '', '```bash', trimmed, '```'].join('\n')
    }
    if (shouldRenderPermissionArgumentBlock(key, trimmed)) {
      const language = permissionArgumentCodeLanguage(toolName, key)
      const fence = language ? `\`\`\`${language}` : '```'
      return [`**${label}**`, '', fence, trimmed, '```'].join('\n')
    }
    return `- **${label}:** ${permissionScalarValue(key, trimmed)}`
  }

  if (
    value === null
    || typeof value === 'number'
    || typeof value === 'boolean'
  ) {
    return `- **${label}:** ${permissionScalarValue(key, value)}`
  }

  const encoded = JSON.stringify(value, null, 2)
  if (!encoded) {
    return `- **${label}:** (empty)`
  }
  return [`**${label}**`, '', '```json', encoded, '```'].join('\n')
}

function buildPermissionArgumentsMarkdown(permission: DesktopPermissionRecord): string {
  const payload = decodePermissionArguments(permission.toolArguments)
  if (!payload) {
    return ['```json', prettyPermissionArguments(permission.toolArguments), '```'].join('\n')
  }

  const normalized = normalizePermissionPayloadValue(payload) as Record<string, unknown>

  if (normalizePermissionToolName(permission.toolName) === 'bash') {
    const command = typeof normalized.command === 'string' ? stripWrappingBackticks(normalized.command) : ''
    const supportingFields = orderedPermissionArguments(permission.toolName, normalized)
      .filter(([key]) => !['command', 'timeout_ms', 'yield_time_ms', 'max_output_tokens'].includes(key.trim().toLowerCase()))
      .map(([key, value]) => permissionArgumentBlockMarkdown(permission.toolName, key, value))

    if (command) {
      return [permissionArgumentBlockMarkdown(permission.toolName, 'command', command), ...supportingFields].filter(Boolean).join('\n\n')
    }
  }

  const fields = orderedPermissionArguments(permission.toolName, normalized).map(([key, value]) => (
    permissionArgumentBlockMarkdown(permission.toolName, key, value)
  ))

  if (fields.length === 0) {
    return ['```json', '{}', '```'].join('\n')
  }

  return fields.join('\n\n')
}

export function buildGenericPermissionMarkdown(permission: DesktopPermissionRecord): string {
  const sections: string[] = []
  const bashPrefix = bashSavedRulePrefix(permission.savedRule)

  if (permission.reason.trim()) {
    sections.push(permission.reason.trim())
  }

  const details = [
    `Tool: ${permissionDisplayToolName(permission.toolName)}`,
    `Requirement: ${permissionRequirementLabel(permission.requirement)}`,
    permission.mode.trim() ? `Mode: ${permission.mode.trim()}` : '',
  ].filter(Boolean).join(' · ')

  sections.push(bashPrefix ? `${details} · Always allow prefix: \`${bashPrefix}\`` : details)

  if (permission.toolArguments.trim()) {
    sections.push(buildPermissionArgumentsMarkdown(permission))
  }

  return sections.filter(Boolean).join('\n\n')
}
