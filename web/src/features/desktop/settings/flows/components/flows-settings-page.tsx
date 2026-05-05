import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import { ArrowLeft, Bot, ChevronDown, Clock, MapPin, MoreHorizontal, Plus, Search, Workflow } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../../components/ui/dialog'
import { Input } from '../../../../../components/ui/input'
import { ModalCloseButton } from '../../../../../components/ui/modal-close-button'
import { cn } from '../../../../../lib/cn'
import {
  createFlow,
  deleteFlow,
  fetchFlow,
  fetchFlows,
  fetchFlowSwarmTargets,
  fetchFlowWorkspaces,
  flowsQueryKey,
  runFlowNow,
  type CreateFlowInput,
  type FlowAgentProfile,
  type FlowDetailRecord,
  type FlowSummaryRecord,
  type FlowSwarmTarget,
  type FlowTaskStep,
  type FlowWorkspaceEntry,
} from '../api'
import { agentStateQueryOptions } from '../../../../queries/query-options'
import { useDesktopStore } from '../../../state/use-desktop-store'

type FlowStatus = 'active' | 'paused' | 'draft' | 'needs_review' | 'failed'
type FlowMode = 'Scheduled background job' | 'Manual one-shot'
type ScheduleCadence = 'Daily' | 'Weekly' | 'Monthly' | 'On demand'

interface FlowTask {
  id: string
  title: string
  detail: string
  action: 'read' | 'propose' | 'write' | 'review'
}

interface FlowRun {
  id: string
  startedAt: string
  duration: string
  status: 'success' | 'skipped' | 'review' | 'failed'
  summary: string
}

interface FlowDefinition {
  id: string
  name: string
  workspace: string
  location: string
  target: string
  agent: string
  schedule: string
  startTime: string
  lastRun: string
  lastRunMeta: string
  nextRun: string
  nextRunMeta: string
  totalRuns: number
  status: FlowStatus
  enabled: boolean
  mode: FlowMode
  context: string
  task: string
  tasks: FlowTask[]
  runs: FlowRun[]
  assignmentStatuses: Array<{ label: string; detail: string; pendingSync: boolean }>
  outbox: Array<{ commandID: string; status: string; detail: string }>
  raw: FlowSummaryRecord
}

interface AddFlowForm {
  name: string
  agentKey: string
  targetKey: string
  scheduleCadence: ScheduleCadence
  scheduleTimes: string[]
  scheduleDay: string
  scheduleDate: string
  timezone: string
  workspacePath: string
  task: string
}

const flowSwarmTargetsQueryKey = ['flows', 'swarm-targets'] as const
const flowWorkspacesQueryKey = ['flows', 'workspaces'] as const

interface FlowTargetOption {
  key: string
  label: string
  helper: string
  target: FlowSwarmTarget
}

interface FlowWorkspaceOption {
  key: string
  label: string
  helper: string
  workspace: FlowWorkspaceEntry
}

interface FlowAgentOption {
  key: string
  label: string
  helper: string
  contractSummary: string
  profile: FlowAgentProfile
}

const scheduleCadenceOptions: ScheduleCadence[] = ['Daily', 'Weekly', 'Monthly', 'On demand']
const scheduleDayOptions = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const scheduleDateOptions = Array.from({ length: 31 }, (_, index) => String(index + 1))
const scheduleTimeOptions = Array.from({ length: 48 }, (_, index) => {
  const hour24 = Math.floor(index / 2)
  const minute = index % 2 === 0 ? '00' : '30'
  const period = hour24 < 12 ? 'AM' : 'PM'
  const hour12 = hour24 % 12 === 0 ? 12 : hour24 % 12
  return `${hour12}:${minute} ${period}`
})
const maxDailyScheduleTimes = 8

const fallbackTimeZones = [
  'UTC',
  'America/Los_Angeles',
  'America/Denver',
  'America/Chicago',
  'America/New_York',
  'America/Sao_Paulo',
  'Europe/London',
  'Europe/Berlin',
  'Europe/Paris',
  'Europe/Madrid',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Australia/Sydney',
] as const

type IntlWithSupportedValues = typeof Intl & { supportedValuesOf?: (key: 'timeZone') => string[] }

function userTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

function availableTimeZones(): string[] {
  const detected = userTimeZone()
  const supported = (Intl as IntlWithSupportedValues).supportedValuesOf?.('timeZone') ?? []
  return Array.from(new Set([detected, 'UTC', ...supported, ...fallbackTimeZones]))
    .filter(Boolean)
    .sort((left, right) => {
      if (left === detected) return -1
      if (right === detected) return 1
      if (left === 'UTC') return -1
      if (right === 'UTC') return 1
      return left.localeCompare(right)
    })
}

const timezoneOptions = availableTimeZones()

function timeInZone(timezone: string, now = new Date()): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      hour: 'numeric',
      minute: '2-digit',
      second: '2-digit',
      timeZone: timezone,
      timeZoneName: 'short',
    }).format(now)
  } catch {
    return 'time unavailable'
  }
}

function scheduleTimesForCadence(form: AddFlowForm): string[] {
  if (form.scheduleCadence === 'On demand') {
    return []
  }
  if (form.scheduleCadence === 'Daily') {
    return form.scheduleTimes.length ? form.scheduleTimes : ['12:00 AM']
  }
  return [form.scheduleTimes[0] || '12:00 AM']
}

const defaultAddFlowForm: AddFlowForm = {
  name: '',
  agentKey: '',
  targetKey: '',
  scheduleCadence: 'On demand',
  scheduleTimes: ['12:00 AM'],
  scheduleDay: 'Mon',
  scheduleDate: '1',
  timezone: userTimeZone(),
  workspacePath: '',
  task: '',
}

const statusLabels: Record<FlowStatus, string> = {
  active: 'Active',
  paused: 'Paused',
  draft: 'Draft',
  needs_review: 'Needs review',
  failed: 'Failed',
}

const statusDotClasses: Record<FlowStatus, string> = {
  active: 'bg-[var(--app-success)]',
  paused: 'bg-[var(--app-text-muted)]',
  draft: 'bg-[var(--app-text-subtle)]',
  needs_review: 'bg-[var(--app-warning)]',
  failed: 'bg-[var(--app-danger)]',
}

const statusTextClasses: Record<FlowStatus, string> = {
  active: 'text-[var(--app-success)]',
  paused: 'text-[var(--app-text-muted)]',
  draft: 'text-[var(--app-text-muted)]',
  needs_review: 'text-[var(--app-warning)]',
  failed: 'text-[var(--app-danger)]',
}

const runStatusClasses: Record<FlowRun['status'], string> = {
  success: 'text-[var(--app-success)]',
  skipped: 'text-[var(--app-text-muted)]',
  review: 'text-[var(--app-warning)]',
  failed: 'text-[var(--app-danger)]',
}

const surfaceClass = 'rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm'
const filterControlClass = 'inline-flex min-h-9 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-xs text-[var(--app-text-muted)] transition hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]'
const fieldClass = 'h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm text-[var(--app-text)] outline-none transition hover:border-[var(--app-border-strong)] focus:border-[var(--app-border-accent)] focus:ring-2 focus:ring-[var(--app-focus-ring)]'
const labelClass = 'text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--app-text-muted)]'

function buildScheduleLabel(form: AddFlowForm) {
  if (form.scheduleCadence === 'On demand') {
    return 'Runs only when you start it manually.'
  }
  const times = scheduleTimesForCadence(form)
  const timesLabel = times.join(', ')
  if (form.scheduleCadence === 'Weekly') {
    return `Every ${form.scheduleDay} at ${timesLabel} (${form.timezone})`
  }
  if (form.scheduleCadence === 'Monthly') {
    return `Monthly on day ${form.scheduleDate} at ${timesLabel} (${form.timezone})`
  }
  return `Every day at ${timesLabel} (${form.timezone})`
}

function clockLabelToHHMM(value: string): string {
  const match = value.trim().match(/^(\d{1,2}):(\d{2})\s*(AM|PM)$/i)
  if (!match) {
    return '00:00'
  }
  let hour = Number(match[1])
  const minute = match[2]
  const period = match[3].toUpperCase()
  if (period === 'AM' && hour === 12) {
    hour = 0
  }
  if (period === 'PM' && hour !== 12) {
    hour += 12
  }
  return `${String(hour).padStart(2, '0')}:${minute}`
}

function targetOptionKey(target: FlowSwarmTarget): string {
  const swarmID = target.swarm_id?.trim()
  if (swarmID) {
    return `swarm:${swarmID}`
  }
  const deploymentID = target.deployment_id?.trim()
  if (deploymentID) {
    return `deployment:${deploymentID}`
  }
  return `target:${target.kind}:${target.name}`
}

function targetOptionLabel(target: FlowSwarmTarget): string {
  const name = target.name?.trim() || target.swarm_id?.trim() || target.kind
  const role = target.role?.trim()
  const relationship = target.relationship?.trim()
  const parts = [name, role, relationship].filter(Boolean)
  return target.current ? `${parts.join(' / ')} (current)` : parts.join(' / ')
}

function targetOptionHelper(target: FlowSwarmTarget): string {
  return [target.kind, target.online ? 'online' : 'offline', target.selectable ? 'selectable' : 'not selectable', target.swarm_id]
    .map((value) => String(value ?? '').trim())
    .filter(Boolean)
    .join(' • ')
}

function targetToSelection(target?: FlowSwarmTarget): CreateFlowInput['target'] {
  if (!target) {
    return {}
  }
  return {
    swarm_id: target.swarm_id?.trim() || undefined,
    kind: target.kind?.trim() || undefined,
    deployment_id: target.deployment_id?.trim() || undefined,
    name: target.name?.trim() || undefined,
  }
}

function workspaceOptionKey(workspace: FlowWorkspaceEntry): string {
  return workspace.path
}

function workspaceOptionLabel(workspace: FlowWorkspaceEntry): string {
  const name = workspace.workspaceName?.trim()
  return name ? `${name} — ${workspace.path}` : workspace.path
}

function workspaceOptionHelper(workspace: FlowWorkspaceEntry): string {
  const linkedCount = workspace.replicationLinks.length
  const directoryCount = workspace.directories.length
  return [workspace.active ? 'active' : '', linkedCount ? `${linkedCount} linked swarm${linkedCount === 1 ? '' : 's'}` : '', directoryCount ? `${directoryCount} director${directoryCount === 1 ? 'y' : 'ies'}` : '']
    .filter(Boolean)
    .join(' • ')
}

function agentOptionKey(profile: FlowAgentProfile): string {
  return `${profile.name.trim().toLowerCase()}::${profile.mode.trim().toLowerCase()}`
}

function agentOptionLabel(profile: FlowAgentProfile): string {
  return profile.name.trim() || 'Unnamed agent'
}

function agentOptionHelper(profile: FlowAgentProfile): string {
  const parts = [profile.mode.trim(), profile.provider.trim(), profile.model.trim()].filter(Boolean)
  return parts.join(' • ')
}

function agentContractSummary(profile: FlowAgentProfile): string {
  const toolScope = profile.toolScope
  if (!toolScope) {
    return 'No explicit contract restrictions saved'
  }
  const parts: string[] = []
  if (toolScope.preset.trim()) {
    parts.push(`preset ${toolScope.preset.trim()}`)
  }
  if (toolScope.allowTools.length) {
    parts.push(`${toolScope.allowTools.length} allowed tool${toolScope.allowTools.length === 1 ? '' : 's'}`)
  }
  if (toolScope.denyTools.length) {
    parts.push(`${toolScope.denyTools.length} denied tool${toolScope.denyTools.length === 1 ? '' : 's'}`)
  }
  if (toolScope.bashPrefixes.length) {
    parts.push(`${toolScope.bashPrefixes.length} bash prefix${toolScope.bashPrefixes.length === 1 ? '' : 'es'}`)
  }
  if (toolScope.inheritPolicy) {
    parts.push('inherits policy')
  }
  return parts.join(' • ') || 'Custom contract configured'
}

function initialAddFlowForm(targetOptions: FlowTargetOption[], workspaceOptions: FlowWorkspaceOption[], agentOptions: FlowAgentOption[]): AddFlowForm {
  const target = targetOptions.find((option) => option.target.current && option.target.selectable) ?? targetOptions.find((option) => option.target.selectable) ?? targetOptions[0]
  const workspace = workspaceOptions.find((option) => option.workspace.active) ?? workspaceOptions[0]
  const agent = agentOptions[0]
  return {
    ...defaultAddFlowForm,
    agentKey: agent?.key ?? '',
    targetKey: target?.key ?? '',
    workspacePath: workspace?.key ?? '',
  }
}

function hhmmToDisplay(value: string): string {
  const [rawHour, minute = '00'] = value.split(':')
  const hour24 = Number(rawHour)
  if (!Number.isFinite(hour24)) {
    return value || 'Manual'
  }
  const period = hour24 < 12 ? 'AM' : 'PM'
  const hour12 = hour24 % 12 === 0 ? 12 : hour24 % 12
  return `${hour12}:${minute.padStart(2, '0')} ${period}`
}

function isoDisplay(value?: string): string {
  if (!value) {
    return '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getTime() <= 0) {
    return '—'
  }
  const day = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(date)
  const time = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' }).format(date).replace(/\s+/g, '\u00A0')
  return `${day} ${time}`
}

function FlowDateTime({ value, meta }: { value: string; meta?: string }) {
  if (!value || value === '—' || value === 'Never') {
    return <div className="text-sm text-[var(--app-text)]">{value || '—'}</div>
  }
  const match = value.match(/^(.*)\s(\d{1,2}:\d{2}\u00A0(?:AM|PM))$/)
  if (!match) {
    return <div className="whitespace-nowrap text-sm text-[var(--app-text)]">{value}</div>
  }
  return (
    <div className="leading-tight">
      <div className="whitespace-nowrap text-sm text-[var(--app-text)]">{match[1]}</div>
      <div className="mt-1 whitespace-nowrap font-mono text-xs text-[var(--app-text-muted)]">{match[2]}</div>
      {meta ? <div className="mt-1 whitespace-nowrap text-xs text-[var(--app-text-muted)]">{meta}</div> : null}
    </div>
  )
}

function durationLabel(ms?: number): string {
  if (!ms || ms <= 0) {
    return '—'
  }
  if (ms < 1000) {
    return `${ms}ms`
  }
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) {
    return `${seconds}s`
  }
  const minutes = Math.floor(seconds / 60)
  return `${minutes}m ${String(seconds % 60).padStart(2, '0')}s`
}

function cadenceLabel(cadence: string): ScheduleCadence {
  switch (cadence.trim().toLowerCase()) {
    case 'daily':
      return 'Daily'
    case 'weekly':
      return 'Weekly'
    case 'monthly':
      return 'Monthly'
    default:
      return 'On demand'
  }
}

function scheduleLabelFromRecord(record: FlowSummaryRecord): string {
  const schedule = record.definition.schedule
  const cadence = cadenceLabel(schedule.cadence)
  if (cadence === 'On demand') {
    return 'On demand'
  }
  const rawTimes = Array.isArray(schedule.times) && schedule.times.length ? schedule.times : schedule.time ? [schedule.time] : []
  const timeLabel = rawTimes.map((value) => hhmmToDisplay(value)).join(', ')
  const timezoneSuffix = schedule.timezone ? ` (${schedule.timezone})` : ''
  if (cadence === 'Weekly') {
    return `Weekly on ${schedule.weekday || 'Mon'} ${timeLabel}${timezoneSuffix}`
  }
  if (cadence === 'Monthly') {
    return `Monthly on day ${schedule.month_day || 1} ${timeLabel}${timezoneSuffix}`
  }
  return `Daily at ${timeLabel}${timezoneSuffix}`
}

function statusFromRecord(record: FlowSummaryRecord): FlowStatus {
  if (record.last_run?.status === 'failed') {
    return 'failed'
  }
  if (record.last_run?.status === 'review') {
    return 'needs_review'
  }
  if (!record.definition.enabled) {
    return record.history_count > 0 ? 'paused' : 'draft'
  }
  const statuses = record.assignment_statuses ?? []
  if (statuses.some((status) => status.pending_sync || status.status === 'target_offline' || status.status === 'target_unusable')) {
    return 'needs_review'
  }
  return 'active'
}

function modeFromRecord(record: FlowSummaryRecord): FlowMode {
  const cadence = cadenceLabel(record.definition.schedule.cadence)
  if (cadence === 'On demand') {
    return 'Manual one-shot'
  }
  return 'Scheduled background job'
}

function normalizeRunStatus(status: string): FlowRun['status'] {
  if (status === 'failed') {
    return 'failed'
  }
  if (status === 'review') {
    return 'review'
  }
  if (status === 'skipped') {
    return 'skipped'
  }
  return 'success'
}

function normalizeTask(task: FlowTaskStep, index: number): FlowTask {
  const rawAction = task.action.trim().toLowerCase()
  const action: FlowTask['action'] = rawAction === 'write' || rawAction === 'review' || rawAction === 'read' ? rawAction : 'propose'
  return {
    id: task.id.trim() || `task-${index + 1}`,
    title: task.title.trim() || `Task ${index + 1}`,
    detail: task.detail?.trim() || task.title.trim() || 'Run configured flow step.',
    action,
  }
}

export function recordToFlow(record: FlowSummaryRecord): FlowDefinition {
  const assignment = record.definition
  const history = Array.isArray(record.history) && record.history.length ? record.history : record.last_run ? [record.last_run] : []
  const runs = history.map((run): FlowRun => ({
    id: run.run_id,
    startedAt: isoDisplay(run.started_at || run.scheduled_at),
    duration: durationLabel(run.duration_ms),
    status: normalizeRunStatus(run.status),
    summary: run.summary || run.status,
  }))
  const workspace = record.workspace_detail?.workspace_path?.trim() || assignment.workspace.workspace_path?.trim() || assignment.workspace.host_workspace_path?.trim() || 'workspace'
  const target = record.target_detail?.name?.trim() || record.target_detail?.swarm_id?.trim() || assignment.target.name?.trim() || assignment.target.swarm_id?.trim() || assignment.target.kind?.trim() || 'local'
  const agent = record.agent_detail?.name?.trim() || assignment.agent.profile_name?.trim() || 'unknown agent'
  const tasks = assignment.intent.tasks?.length
    ? assignment.intent.tasks.map(normalizeTask)
    : [{ id: `${assignment.flow_id}-prompt`, title: 'Run prompt', detail: assignment.intent.prompt || 'Run configured prompt.', action: 'propose' as const }]
  const assignmentStatuses = (record.assignment_statuses ?? []).map((status) => ({
    label: status.target.swarm_id || status.target.name || status.target_swarm_id || 'target',
    detail: [status.status, status.reason].filter(Boolean).join(' • ') || status.status,
    pendingSync: status.pending_sync,
  }))
  const outbox = (record.outbox ?? []).map((command) => ({
    commandID: command.command_id,
    status: command.status,
    detail: command.last_error?.trim() || `${command.attempt_count ?? 0} attempts`,
  }))
  return {
    id: assignment.flow_id,
    name: assignment.name || assignment.flow_id,
    workspace,
    location: assignment.workspace.cwd?.trim() || workspace,
    target,
    agent,
    schedule: scheduleLabelFromRecord(record),
    startTime: assignment.schedule.time ? hhmmToDisplay(assignment.schedule.time) : 'Manual',
    lastRun: record.last_run ? isoDisplay(record.last_run.started_at || record.last_run.scheduled_at) : 'Never',
    lastRunMeta: record.history_count ? `${record.history_count} runs` : '',
    nextRun: assignment.next_due_at ? isoDisplay(assignment.next_due_at) : '—',
    nextRunMeta: record.assignment_statuses?.some((status) => status.pending_sync) ? 'pending sync' : '',
    totalRuns: record.history_count,
    status: statusFromRecord(record),
    enabled: assignment.enabled,
    mode: modeFromRecord(record),
    context: assignment.intent.mode || assignment.catch_up_policy.mode || 'target-owned schedule',
    task: assignment.intent.prompt || tasks.map((task) => task.title).join(', '),
    tasks,
    runs,
    assignmentStatuses,
    outbox,
    raw: record,
  }
}

export function formToCreateInput(form: AddFlowForm, targets: FlowTargetOption[], workspaces: FlowWorkspaceOption[], agents: FlowAgentOption[]): CreateFlowInput {
  const isOnDemand = form.scheduleCadence === 'On demand'
  const cadence = isOnDemand ? 'on_demand' : form.scheduleCadence.toLowerCase()
  const selectedTimes = scheduleTimesForCadence(form).map((value) => clockLabelToHHMM(value))
  const targetOption = targets.find((option) => option.key === form.targetKey)
  const workspaceOption = workspaces.find((option) => option.key === form.workspacePath)
  const agentOption = agents.find((option) => option.key === form.agentKey)
  const workspacePath = workspaceOption?.workspace.path.trim() || form.workspacePath.trim()
  const task = form.task.trim() || 'Run the configured task prompt.'
  return {
    name: form.name.trim() || 'Untitled flow',
    enabled: !isOnDemand,
    target: targetToSelection(targetOption?.target),
    agent: {
      profile_name: agentOption?.profile.name.trim() || '',
      profile_mode: agentOption?.profile.mode.trim() || undefined,
    },
    workspace: {
      workspace_path: workspacePath,
      host_workspace_path: workspacePath,
      cwd: workspacePath,
    },
    schedule: {
      cadence,
      time: cadence === 'on_demand' ? undefined : selectedTimes[0],
      times: cadence === 'on_demand' ? undefined : selectedTimes,
      weekday: cadence === 'weekly' ? form.scheduleDay : undefined,
      month_day: cadence === 'monthly' ? Number(form.scheduleDate) : undefined,
      timezone: form.timezone.trim() || 'UTC',
    },
    catch_up_policy: {
      mode: 'once',
    },
    intent: {
      prompt: task,
      mode: 'target-owned schedule',
      tasks: [
        { id: 'context', title: 'Prepare run context', detail: `Target ${targetOption?.label || 'selected swarm'} in ${workspacePath || 'the selected workspace'}.`, action: 'read' },
        { id: 'task', title: 'Run agent task', detail: task, action: 'propose' },
      ],
    },
  }
}

function FlowStatusDot({ status, className }: { status: FlowStatus; className?: string }) {
  return <span className={cn('inline-block h-2 w-2 shrink-0 rounded-full', statusDotClasses[status], className)} />
}

function StatusOutlineToken({ status }: { status: FlowStatus }) {
  return (
    <span className="inline-flex items-center gap-2 rounded-lg border border-[var(--app-border)] bg-transparent px-2.5 py-1.5 font-mono text-[11px] uppercase tracking-[0.12em] text-[var(--app-text-muted)]">
      <FlowStatusDot status={status} className="h-1.5 w-1.5" />
      {statusLabels[status]}
    </span>
  )
}

function EnabledToggle({ enabled, disabled, onToggle }: { enabled: boolean; disabled?: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      onClick={onToggle}
      disabled={disabled}
      className={cn(
        'relative inline-flex h-6 w-11 items-center rounded-full border transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] disabled:cursor-not-allowed disabled:opacity-50',
        enabled ? 'border-[var(--app-success-border)] bg-[var(--app-success)]' : 'border-[var(--app-border)] bg-[var(--app-surface-active)]',
      )}
    >
      <span className={cn('h-4 w-4 rounded-full bg-[var(--app-bg)] shadow-sm transition', enabled ? 'translate-x-6' : 'translate-x-1')} />
      <span className="sr-only">{enabled ? 'Disable flow' : 'Enable flow'}</span>
    </button>
  )
}

function FilterSelect({ value, onChange, options, label }: { value: string; onChange: (value: string) => void; options: Array<{ value: string; label: string }>; label: string }) {
  return (
    <label className={cn(filterControlClass, 'relative pr-8')}>
      <span className="sr-only">{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)} className="absolute inset-0 cursor-pointer opacity-0" aria-label={label}>
        {options.map((option) => (
          <option key={option.value} value={option.value}>{option.label}</option>
        ))}
      </select>
      <span>{options.find((option) => option.value === value)?.label ?? label}</span>
      <ChevronDown size={14} className="absolute right-3 text-[var(--app-text-muted)]" />
    </label>
  )
}

function AddFlowModal({
  open,
  onClose,
  onAdd,
  busy,
  targetOptions,
  workspaceOptions,
  agentOptions,
  loadingOptions,
}: {
  open: boolean
  onClose: () => void
  onAdd: (input: CreateFlowInput) => void
  busy?: boolean
  targetOptions: FlowTargetOption[]
  workspaceOptions: FlowWorkspaceOption[]
  agentOptions: FlowAgentOption[]
  loadingOptions?: boolean
}) {
  const initialForm = useMemo(() => initialAddFlowForm(targetOptions, workspaceOptions, agentOptions), [agentOptions, targetOptions, workspaceOptions])
  const [form, setForm] = useState<AddFlowForm>(initialForm)
  const [now, setNow] = useState(() => new Date())

  useEffect(() => {
    if (open) {
      setForm(initialForm)
      setNow(new Date())
    }
  }, [initialForm, open])

  useEffect(() => {
    if (!open) {
      return undefined
    }
    const interval = window.setInterval(() => setNow(new Date()), 1000)
    return () => window.clearInterval(interval)
  }, [open])

  if (!open) {
    return null
  }

  const update = (field: keyof AddFlowForm) => (event: ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
    const value = event.target.value
    setForm((current) => {
      if (field === 'scheduleCadence') {
        const nextCadence = value as ScheduleCadence
        const nextTimes = nextCadence === 'Daily' ? current.scheduleTimes : [current.scheduleTimes[0] || '12:00 AM']
        return { ...current, scheduleCadence: nextCadence, scheduleTimes: nextTimes }
      }
      return { ...current, [field]: value }
    })
  }
  const updateScheduleTime = (index: number) => (event: ChangeEvent<HTMLSelectElement>) => {
    setForm((current) => ({
      ...current,
      scheduleTimes: current.scheduleTimes.map((value, currentIndex) => (currentIndex === index ? event.target.value : value)),
    }))
  }
  const addScheduleTime = () => {
    setForm((current) => {
      if (current.scheduleCadence !== 'Daily' || current.scheduleTimes.length >= maxDailyScheduleTimes) {
        return current
      }
      return { ...current, scheduleTimes: [...current.scheduleTimes, current.scheduleTimes[current.scheduleTimes.length - 1] || '12:00 AM'] }
    })
  }
  const removeScheduleTime = (index: number) => {
    setForm((current) => {
      const remaining = current.scheduleTimes.filter((_, currentIndex) => currentIndex !== index)
      return {
        ...current,
        scheduleTimes: remaining.length ? remaining : ['12:00 AM'],
      }
    })
  }

  const schedulePreview = buildScheduleLabel(form)
  const selectedTarget = targetOptions.find((option) => option.key === form.targetKey)
  const selectedWorkspace = workspaceOptions.find((option) => option.key === form.workspacePath)
  const selectedAgent = agentOptions.find((option) => option.key === form.agentKey)
  const isScheduled = form.scheduleCadence !== 'On demand'
  const supportsMultipleTimes = form.scheduleCadence === 'Daily'
  const selectedTimezoneNow = timeInZone(form.timezone, now)
  const detectedTimezone = userTimeZone()
  const localTimezoneNow = timeInZone(detectedTimezone, now)
  const canSubmit = Boolean(selectedTarget && selectedWorkspace && selectedAgent && form.task.trim()) && !busy

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!canSubmit) {
      return
    }
    onAdd(formToCreateInput(form, targetOptions, workspaceOptions, agentOptions))
    setForm(initialForm)
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Add Flow" className="z-[80] p-4 sm:p-6" data-testid="flows-add-modal">
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="w-[min(780px,100%)] gap-0 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-2xl">
        <form onSubmit={submit} className="flex max-h-[min(820px,calc(100vh-48px))] flex-col">
          <div className="flex items-start justify-between gap-4 border-b border-[var(--app-border)] px-6 py-5">
            <div>
              <div className={labelClass}>New scheduled job</div>
              <h2 className="mt-1 text-lg font-semibold text-[var(--app-text)]">Add Flow</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Creates a controller Flow and syncs it to the selected target.</p>
            </div>
            <ModalCloseButton className="rounded-xl" onClick={onClose} aria-label="Close Add Flow" />
          </div>

          <div className="overflow-y-auto px-6 py-5">
            <div className="grid gap-x-8 gap-y-5 md:grid-cols-2">
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Flow name</span>
                <Input data-testid="flows-add-name" value={form.name} onChange={update('name')} className={fieldClass} />
              </label>
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Agent</span>
                <select data-testid="flows-add-agent" value={form.agentKey} onChange={update('agentKey')} className={fieldClass} disabled={loadingOptions || !agentOptions.length}>
                  {agentOptions.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                </select>
                <span className="text-[11px] leading-4 text-[var(--app-text-muted)]">
                  {loadingOptions ? 'Loading saved agents…' : selectedAgent?.helper || 'No enabled saved agents returned by the controller.'}
                </span>
              </label>
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Target swarm</span>
                <select data-testid="flows-add-target" value={form.targetKey} onChange={update('targetKey')} className={fieldClass} disabled={loadingOptions || !targetOptions.length}>
                  {targetOptions.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                </select>
                <span className="text-[11px] leading-4 text-[var(--app-text-muted)]">
                  {loadingOptions ? 'Loading linked swarms…' : selectedTarget?.helper || 'No linked swarm targets returned by the controller.'}
                </span>
              </label>
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Workspace</span>
                <select data-testid="flows-add-workspace" value={form.workspacePath} onChange={update('workspacePath')} className={fieldClass} disabled={loadingOptions || !workspaceOptions.length}>
                  {workspaceOptions.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                </select>
                <span className="text-[11px] leading-4 text-[var(--app-text-muted)]">
                  {loadingOptions ? 'Loading real workspaces…' : selectedWorkspace?.helper || 'No workspace records returned by the controller.'}
                </span>
              </label>
              <section className="grid gap-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 md:col-span-2" aria-label="Schedule">
                <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                  <div>
                    <div className={labelClass}>Schedule</div>
                    <div className="mt-1 text-sm font-medium text-[var(--app-text)]">{schedulePreview}</div>
                    <div className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">
                      Pick the recurrence first. Only daily flows can run multiple times per day; weekly and monthly flows run once on their selected day.
                    </div>
                  </div>
                  <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2 text-xs text-[var(--app-text-muted)] md:min-w-[220px]">
                    <div className="font-medium text-[var(--app-text)]">Now: {localTimezoneNow}</div>
                    <div className="mt-0.5">Your timezone: {detectedTimezone}</div>
                  </div>
                </div>

                <div className="grid gap-3 md:grid-cols-3">
                  <label className="flex flex-col gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">Repeats</span>
                    <select data-testid="flows-add-cadence" value={form.scheduleCadence} onChange={update('scheduleCadence')} className={fieldClass}>
                      {scheduleCadenceOptions.map((cadence) => <option key={cadence}>{cadence}</option>)}
                    </select>
                  </label>
                  {form.scheduleCadence === 'Weekly' ? (
                    <label className="flex flex-col gap-2">
                      <span className="text-xs text-[var(--app-text-muted)]">Run on</span>
                      <select value={form.scheduleDay} onChange={update('scheduleDay')} className={fieldClass}>
                        {scheduleDayOptions.map((day) => <option key={day}>{day}</option>)}
                      </select>
                    </label>
                  ) : null}
                  {form.scheduleCadence === 'Monthly' ? (
                    <label className="flex flex-col gap-2">
                      <span className="text-xs text-[var(--app-text-muted)]">Run on day</span>
                      <select value={form.scheduleDate} onChange={update('scheduleDate')} className={fieldClass}>
                        {scheduleDateOptions.map((date) => <option key={date}>{date}</option>)}
                      </select>
                    </label>
                  ) : null}
                </div>

                {isScheduled ? (
                  <>
                    <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_minmax(220px,0.75fr)]">
                      <div className="flex flex-col gap-2">
                        <div className="flex items-center justify-between gap-3">
                          <span className="text-xs text-[var(--app-text-muted)]">{supportsMultipleTimes ? 'Run times' : 'Run time'}</span>
                          {supportsMultipleTimes ? (
                            <button
                              type="button"
                              onClick={addScheduleTime}
                              disabled={form.scheduleTimes.length >= maxDailyScheduleTimes}
                              className="text-xs font-medium text-[var(--app-primary)] disabled:cursor-not-allowed disabled:text-[var(--app-text-subtle)]"
                            >
                              Add daily run time
                            </button>
                          ) : null}
                        </div>
                        <div className="grid gap-2">
                          {scheduleTimesForCadence(form).map((time, index) => (
                            <div key={`${form.scheduleCadence}-${time}-${index}`} className="flex items-center gap-2">
                              <select value={time} onChange={updateScheduleTime(index)} className={cn(fieldClass, 'flex-1')}>
                                {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                              </select>
                              {supportsMultipleTimes ? (
                                <Button type="button" variant="ghost" className="rounded-xl" onClick={() => removeScheduleTime(index)} disabled={form.scheduleTimes.length === 1}>
                                  Remove
                                </Button>
                              ) : null}
                            </div>
                          ))}
                        </div>
                        <div className="text-[11px] leading-4 text-[var(--app-text-muted)]">
                          {supportsMultipleTimes
                            ? `Daily flows can run up to ${maxDailyScheduleTimes} times per day. Use this for agents that should check in repeatedly.`
                            : 'Need several runs in one day? Choose Daily instead. Weekly and monthly schedules intentionally run once on their selected day.'}
                        </div>
                      </div>
                      <label className="flex flex-col gap-2">
                        <span className="text-xs text-[var(--app-text-muted)]">Timezone</span>
                        <select data-testid="flows-add-timezone" value={form.timezone} onChange={update('timezone')} className={fieldClass}>
                          {timezoneOptions.map((timezone) => (
                            <option key={timezone} value={timezone}>{timezone}</option>
                          ))}
                        </select>
                        <span className="text-[11px] leading-4 text-[var(--app-text-muted)]">Selected timezone now: {selectedTimezoneNow}</span>
                      </label>
                    </div>
                  </>
                ) : (
                  <div className="rounded-xl border border-dashed border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2 text-xs leading-5 text-[var(--app-text-muted)]">
                    On-demand flows do not have a clock schedule. You can still run them manually from the flow detail page.
                  </div>
                )}
              </section>
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Task</span>
                <textarea data-testid="flows-add-task" value={form.task} onChange={update('task')} rows={4} className="resize-none rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2 text-sm leading-6 text-[var(--app-text)] outline-none transition hover:border-[var(--app-border-strong)] focus:border-[var(--app-border-accent)] focus:ring-2 focus:ring-[var(--app-focus-ring)]" />
              </label>
            </div>
          </div>

          <div className="flex items-center justify-between border-t border-[var(--app-border)] px-6 py-4">
            <p className="text-xs text-[var(--app-text-muted)]">Targets keep accepted assignments and schedule locally.</p>
            <div className="flex items-center gap-2">
              <Button variant="ghost" className="rounded-xl" onClick={onClose} disabled={busy}>Cancel</Button>
              <Button data-testid="flows-add-submit" type="submit" variant="primary" className="rounded-xl" disabled={!canSubmit}>{busy ? 'Adding…' : 'Add Flow'}</Button>
            </div>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  )
}

function FlowDetail({ flow, onBack, onRunNow, onDelete, busy }: { flow: FlowDefinition; onBack: () => void; onRunNow: (id: string) => void; onDelete: (id: string) => void; busy?: boolean }) {
  return (
    <div data-testid="flows-detail" className="flex min-h-full flex-col gap-8 pb-10 text-[var(--app-text)]">
      <div className="flex items-center justify-between gap-4 border-b border-[var(--app-border)] pb-5">
        <div className="min-w-0">
          <button type="button" onClick={onBack} className="mb-4 inline-flex items-center gap-2 text-sm text-[var(--app-text-muted)] hover:text-[var(--app-text)]">
            <ArrowLeft size={16} /> Back to flows
          </button>
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
            <FlowStatusDot status={flow.status} /> {statusLabels[flow.status]} / {flow.mode}
          </div>
          <h1 className="mt-2 truncate text-2xl font-semibold tracking-tight text-[var(--app-text)]">{flow.name}</h1>
          <p className="mt-2 max-w-3xl text-sm leading-6 text-[var(--app-text-muted)]">{flow.task}</p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Button data-testid="flows-detail-run-now" variant="outline" className="rounded-xl" onClick={() => onRunNow(flow.id)} disabled={busy}>
            Run now
          </Button>
          <Button variant="ghost" className="rounded-xl text-[var(--app-danger)]" onClick={() => onDelete(flow.id)} disabled={busy}>
            Delete
          </Button>
        </div>
      </div>

      <section className="grid gap-x-10 gap-y-6 border-b border-[var(--app-border)] pb-8 md:grid-cols-3">
        {[
          ['Target', flow.target],
          ['Agent', flow.agent],
          ['Schedule', flow.schedule],
          ['Workspace', flow.workspace],
          ['Location', flow.location],
          ['Context', flow.context],
          ['Next due', flow.nextRun],
          ['Saved status', statusLabels[flow.status]],
          ['Saved runs', String(flow.totalRuns)],
        ].map(([label, value]) => (
          <div key={label}>
            <div className={labelClass}>{label}</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{value}</div>
          </div>
        ))}
      </section>

      <section className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div>
          <div className="mb-6 grid gap-4 md:grid-cols-2">
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
              <div className={labelClass}>Assignment status</div>
              <div data-testid="flows-detail-assignment-status" className="mt-3 space-y-3">
                {flow.assignmentStatuses.length ? flow.assignmentStatuses.map((status) => (
                  <div key={`${status.label}-${status.detail}`} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2">
                    <div className="text-sm font-medium text-[var(--app-text)]">{status.label}</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">{status.detail}{status.pendingSync ? ' • pending sync' : ''}</div>
                  </div>
                )) : <div className="text-sm text-[var(--app-text-muted)]">No assignment status mirrored yet.</div>}
              </div>
            </div>
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
              <div className={labelClass}>Outbox</div>
              <div data-testid="flows-detail-outbox" className="mt-3 space-y-3">
                {flow.outbox.length ? flow.outbox.map((command) => (
                  <div key={command.commandID} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 py-2">
                    <div className="text-sm font-medium text-[var(--app-text)]">{command.commandID}</div>
                    <div className="mt-1 text-xs text-[var(--app-text-muted)]">{command.status} • {command.detail}</div>
                  </div>
                )) : <div className="text-sm text-[var(--app-text-muted)]">No pending outbox commands.</div>}
              </div>
            </div>
          </div>
          <div className="mb-4 flex items-center justify-between gap-3">
            <div>
              <h2 className="text-base font-semibold text-[var(--app-text)]">Tasks inside this flow</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Tasks are sent as durable assignment intent to the target.</p>
            </div>
          </div>
          <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]">
            {flow.tasks.map((task, index) => (
              <div key={task.id} className="grid gap-4 border-b border-[var(--app-border)] px-4 py-4 last:border-b-0 md:grid-cols-[36px_120px_minmax(0,1fr)]">
                <div className="font-mono text-xs text-[var(--app-text-muted)]">{String(index + 1).padStart(2, '0')}</div>
                <div className="text-xs uppercase tracking-[0.16em] text-[var(--app-text-muted)]">{task.action}</div>
                <div>
                  <div className="text-sm font-medium text-[var(--app-text)]">{task.title}</div>
                  <div className="mt-1 text-sm leading-6 text-[var(--app-text-muted)]">{task.detail}</div>
                </div>
              </div>
            ))}
          </div>
        </div>

        <aside>
          <h2 className="text-base font-semibold text-[var(--app-text)]">Recent runs</h2>
          <div data-testid="flows-recent-runs" className="mt-4 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]">
            {flow.runs.length ? flow.runs.map((run) => (
              <div key={run.id} className="border-b border-[var(--app-border)] px-4 py-3 last:border-b-0">
                <div className="flex items-center justify-between gap-3">
                  <span className="font-mono text-xs text-[var(--app-text-muted)]">{run.startedAt}</span>
                  <span className={cn('text-xs capitalize', runStatusClasses[run.status])}>{run.status}</span>
                </div>
                <p className="mt-2 text-xs leading-5 text-[var(--app-text-muted)]">{run.summary}</p>
                <div className="mt-1 text-[11px] text-[var(--app-text-muted)]">{run.duration}</div>
              </div>
            )) : (
              <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No mirrored runs yet.</div>
            )}
          </div>
        </aside>
      </section>
    </div>
  )
}

export function FlowsSettingsPage() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const globalFlowMatch = matchRoute({ to: '/flow/$flowId', fuzzy: false })
  const workspaceFlowMatch = matchRoute({ to: '/$workspaceSlug/flow', fuzzy: false })
  const workspaceFlowDetailMatch = matchRoute({ to: '/$workspaceSlug/flow/$flowId', fuzzy: false })
  const routeWorkspaceSlug = (workspaceFlowDetailMatch ? workspaceFlowDetailMatch.workspaceSlug : workspaceFlowMatch ? workspaceFlowMatch.workspaceSlug : '').trim()
  const routeFlowID = (workspaceFlowDetailMatch ? workspaceFlowDetailMatch.flowId : globalFlowMatch ? globalFlowMatch.flowId : '').trim()
  const activeSessionId = useDesktopStore((state) => state.activeSessionId)
  const flowsQuery = useQuery({ queryKey: flowsQueryKey, queryFn: ({ signal }) => fetchFlows(signal) })
  const swarmTargetsQuery = useQuery({ queryKey: flowSwarmTargetsQueryKey, queryFn: fetchFlowSwarmTargets })
  const flowWorkspacesQuery = useQuery({ queryKey: flowWorkspacesQueryKey, queryFn: fetchFlowWorkspaces })
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const [selectedFlowRecord, setSelectedFlowRecord] = useState<FlowDetailRecord | null>(null)
  const flows = useMemo(() => (flowsQuery.data ?? []).map(recordToFlow), [flowsQuery.data])
  const [workspaceFilter, setWorkspaceFilter] = useState('all')
  const [agentFilter, setAgentFilter] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')
  const [query, setQuery] = useState('')
  const [selectedFlowID, setSelectedFlowIDState] = useState<string | null>(routeFlowID || null)
  const [addOpen, setAddOpen] = useState(false)
  const [busyID, setBusyID] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const targetOptions = useMemo<FlowTargetOption[]>(() => {
    const seen = new Set<string>()
    return (swarmTargetsQuery.data ?? [])
      .filter((target) => target.selectable || target.current)
      .map((target) => ({ key: targetOptionKey(target), label: targetOptionLabel(target), helper: targetOptionHelper(target), target }))
      .filter((option) => {
        if (!option.key || seen.has(option.key)) {
          return false
        }
        seen.add(option.key)
        return true
      })
  }, [swarmTargetsQuery.data])
  const addWorkspaceOptions = useMemo<FlowWorkspaceOption[]>(() => {
    const seen = new Set<string>()
    return (flowWorkspacesQuery.data ?? [])
      .map((workspace) => ({ key: workspaceOptionKey(workspace), label: workspaceOptionLabel(workspace), helper: workspaceOptionHelper(workspace), workspace }))
      .filter((option) => {
        if (!option.key || seen.has(option.key)) {
          return false
        }
        seen.add(option.key)
        return true
      })
  }, [flowWorkspacesQuery.data])
  const savedAgentOptions = useMemo<FlowAgentOption[]>(() => {
    const seen = new Set<string>()
    return (agentStateQuery.data?.profiles ?? [])
      .filter((profile) => profile.enabled && profile.name.trim() !== '')
      .map((profile) => ({
        key: agentOptionKey(profile),
        label: agentOptionLabel(profile),
        helper: agentOptionHelper(profile),
        contractSummary: agentContractSummary(profile),
        profile,
      }))
      .filter((option) => {
        if (!option.key || seen.has(option.key)) {
          return false
        }
        seen.add(option.key)
        return true
      })
      .sort((left, right) => left.label.localeCompare(right.label))
  }, [agentStateQuery.data?.profiles])
  const loadingAddFlowOptions = swarmTargetsQuery.isLoading || flowWorkspacesQuery.isLoading || agentStateQuery.isLoading

  const workspaces = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.workspace)))], [flows])
  const agents = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.agent)))], [flows])
  const statuses = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.status)))], [flows])

  const workspaceOptions = workspaces.map((workspace) => ({ value: workspace, label: workspace === 'all' ? 'All workspaces' : workspace }))
  const agentFilterOptions = agents.map((agent) => ({ value: agent, label: agent === 'all' ? 'All agents' : agent }))
  const statusOptions = statuses.map((status) => ({ value: status, label: status === 'all' ? 'All statuses' : statusLabels[status as FlowStatus] }))

  const filteredFlows = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    return flows.filter((flow) => {
      const workspaceMatch = workspaceFilter === 'all' || flow.workspace === workspaceFilter
      const agentMatch = agentFilter === 'all' || flow.agent === agentFilter
      const statusMatch = statusFilter === 'all' || flow.status === statusFilter
      const queryMatch = !normalizedQuery || [flow.name, flow.agent, flow.workspace, flow.target, flow.task, flow.schedule].some((value) => value.toLowerCase().includes(normalizedQuery))
      return workspaceMatch && agentMatch && statusMatch && queryMatch
    })
  }, [agentFilter, flows, query, statusFilter, workspaceFilter])

  const handleBackToChat = useCallback(() => {
    if (routeWorkspaceSlug && activeSessionId) {
      void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug: routeWorkspaceSlug, sessionId: activeSessionId } })
      return
    }
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    void navigate({ to: '/' })
  }, [activeSessionId, navigate, routeWorkspaceSlug])

  const setSelectedFlowID = useCallback((id: string | null) => {
    setSelectedFlowIDState(id)
    if (id) {
      if (routeWorkspaceSlug) {
        void navigate({ to: '/$workspaceSlug/flow/$flowId', params: { workspaceSlug: routeWorkspaceSlug, flowId: id } })
        return
      }
      void navigate({ to: '/flow/$flowId', params: { flowId: id } })
      return
    }
    if (routeWorkspaceSlug) {
      void navigate({ to: '/$workspaceSlug/flow', params: { workspaceSlug: routeWorkspaceSlug } })
      return
    }
    void navigate({ to: '/flow' })
  }, [navigate, routeWorkspaceSlug])

  useEffect(() => {
    setSelectedFlowIDState(routeFlowID || null)
  }, [routeFlowID])

  const selectedFlow = useMemo(() => {
    if (selectedFlowRecord && selectedFlowID === selectedFlowRecord.definition.flow_id) {
      return recordToFlow(selectedFlowRecord)
    }
    return selectedFlowID ? flows.find((flow) => flow.id === selectedFlowID) ?? null : null
  }, [flows, selectedFlowID, selectedFlowRecord])

  useEffect(() => {
    if (!selectedFlowID) {
      setSelectedFlowRecord(null)
      return
    }
    if (selectedFlowRecord?.definition.flow_id === selectedFlowID) {
      return
    }
    let cancelled = false
    void fetchFlow(selectedFlowID)
      .then((detail) => {
        if (!cancelled) {
          setSelectedFlowRecord(detail)
        }
      })
      .catch((err) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Failed to load flow detail')
        }
      })
    return () => {
      cancelled = true
    }
  }, [selectedFlowID, selectedFlowRecord])

  const reviewCount = flows.filter((flow) => flow.status === 'needs_review').length
  const draftCount = flows.filter((flow) => flow.status === 'draft').length
  const pausedCount = flows.filter((flow) => flow.status === 'paused').length
  const runningCount = (flowsQuery.data ?? []).filter((record) => record.last_run?.status === 'running').length
  const nextFlow = flows.find((flow) => flow.nextRun !== '—') ?? null

  const metaHeaderItems = [
    { label: 'Flows', value: String(flows.length), helper: 'controller records', tone: 'primary' as const },
    { label: 'Running now', value: String(runningCount), helper: 'active jobs', tone: 'active' as const },
    { label: 'Next up', value: nextFlow?.startTime ?? '—', helper: nextFlow?.name ?? 'no scheduled flows', tone: 'primary' as const },
    { label: 'Needs review', value: String(reviewCount), helper: 'requires attention', tone: 'needs_review' as const },
    { label: 'Paused', value: String(pausedCount), helper: 'disabled', tone: 'paused' as const },
    { label: 'Drafts', value: String(draftCount), helper: 'not enabled', tone: 'draft' as const },
  ]

  const scheduleItems = flows
    .filter((flow) => flow.nextRun !== '—')
    .slice(0, 6)
    .map((flow) => ({ flow, time: flow.startTime, day: flow.nextRun, meta: `${flow.schedule}${flow.nextRunMeta ? ` / ${flow.nextRunMeta}` : ''}` }))

  const attentionItems = flows
    .filter((flow) => flow.status === 'needs_review' || flow.status === 'failed' || flow.status === 'draft' || flow.status === 'paused')
    .slice(0, 6)
    .map((flow) => ({ flow, meta: flow.lastRun === 'Never' ? `Next run: ${flow.nextRun}` : `Last run: ${flow.lastRun}`, dotStatus: flow.status }))

  const refreshFlows = async () => {
    await queryClient.invalidateQueries({ queryKey: flowsQueryKey })
  }

  const addFlow = async (input: CreateFlowInput) => {
    setSaving(true)
    setError(null)
    try {
      const detail = await createFlow(input)
      setAddOpen(false)
      setSelectedFlowRecord(detail)
      setSelectedFlowID(detail.definition.flow_id)
      await refreshFlows()
      const refreshed = await fetchFlow(detail.definition.flow_id)
      setSelectedFlowRecord(refreshed)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create flow')
    } finally {
      setSaving(false)
    }
  }

  const handleRunNow = async (id: string) => {
    setBusyID(id)
    setError(null)
    try {
      await runFlowNow(id)
      await refreshFlows()
      setSelectedFlowRecord(await fetchFlow(id))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to run flow')
    } finally {
      setBusyID(null)
    }
  }

  const handleDelete = async (id: string) => {
    setBusyID(id)
    setError(null)
    try {
      await deleteFlow(id)
      setSelectedFlowRecord(null)
      setSelectedFlowID(null)
      await refreshFlows()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete flow')
    } finally {
      setBusyID(null)
    }
  }

  if (selectedFlow) {
    return (
      <>
        {error ? <div data-testid="flows-error" className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
        <FlowDetail flow={selectedFlow} onBack={() => { setSelectedFlowRecord(null); setSelectedFlowID(null) }} onRunNow={handleRunNow} onDelete={handleDelete} busy={busyID === selectedFlow.id} />
      </>
    )
  }

  return (
    <div data-testid="flows-settings-page" className="flex min-h-full flex-col gap-5 pb-10 text-[var(--app-text)]">
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--app-border)] pb-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
            <Workflow size={14} /> Workspace / Flows
          </div>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight text-[var(--app-text)]">Flows</h1>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">Triage scheduled and background agent jobs from real controller data.</p>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button variant="outline" className="rounded-xl" onClick={handleBackToChat}>
            <ArrowLeft size={15} /> Back to chat
          </Button>
          <Button data-testid="flows-add-open" variant="outline" className="rounded-xl" onClick={() => setAddOpen(true)}>
            <Plus size={16} /> Add Flow
          </Button>
        </div>
      </header>

      {error ? (
        <div data-testid="flows-error" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div>
      ) : null}
      {flowsQuery.isLoading ? (
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">Loading flows…</div>
      ) : null}
      {swarmTargetsQuery.isError || flowWorkspacesQuery.isError || agentStateQuery.isError ? (
        <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning)]">
          Add Flow selectors could not load real controller targets, workspaces, or saved agents. Refresh after the controller endpoints recover.
        </div>
      ) : null}

      <section className={cn(surfaceClass, 'flex flex-wrap items-center justify-between gap-x-6 gap-y-3 px-4 py-3')}>
        {metaHeaderItems.map((item) => {
          const toneClass = item.tone === 'primary' ? 'text-[var(--app-primary)]' : statusTextClasses[item.tone]
          return (
            <div key={item.label} className="flex min-w-[132px] items-center gap-3 border-r border-[var(--app-border)] pr-6 last:border-r-0 last:pr-0">
              <FlowStatusDot status={item.tone === 'primary' ? 'active' : item.tone} className={cn('h-1.5 w-1.5', item.tone === 'primary' ? 'bg-[var(--app-primary)]' : '')} />
              <div className="min-w-0">
                <div className="flex items-baseline gap-2">
                  <span className={cn('font-mono text-sm font-semibold', toneClass)}>{item.value}</span>
                  <span className="truncate text-xs font-medium text-[var(--app-text)]">{item.label}</span>
                </div>
                <div className="mt-0.5 max-w-[220px] truncate text-[11px] text-[var(--app-text-muted)]">{item.helper}</div>
              </div>
            </div>
          )
        })}
      </section>

      <section className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_360px]">
        <div className={cn(surfaceClass, 'p-5')}>
          <div className="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h2 className="text-base font-semibold text-[var(--app-text)]">Schedule</h2>
            </div>
            <div className="flex flex-wrap items-center justify-end gap-2">
              <FilterSelect label="Workspace filter" value={workspaceFilter} onChange={setWorkspaceFilter} options={workspaceOptions} />
              <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentFilterOptions} />
              <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
            </div>
          </div>

          <div className="mt-5 border-y border-[var(--app-border)]">
            <div className="grid grid-cols-[88px_140px_minmax(0,1fr)_120px] gap-3 border-b border-[var(--app-border)] px-0 py-2 text-[11px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
              <div>Time</div>
              <div>Next</div>
              <div>Flow</div>
              <div className="text-right">Status</div>
            </div>
            <div className="divide-y divide-[var(--app-border)]">
              {scheduleItems.length ? scheduleItems.map((event) => (
                <button key={event.flow.id} type="button" onClick={() => setSelectedFlowID(event.flow.id)} className="grid w-full grid-cols-[88px_140px_minmax(0,1fr)_120px] items-center gap-3 py-4 text-left transition hover:bg-[var(--app-surface-subtle)]">
                  <span className="font-mono text-sm text-[var(--app-text)]">{event.time}</span>
                  <span className="truncate text-xs text-[var(--app-text-muted)]">{event.day}</span>
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium text-[var(--app-text)]">{event.flow.name}</span>
                    <span className="mt-1 block truncate text-xs text-[var(--app-text-muted)]">{event.flow.workspace} / {event.flow.agent} / {event.meta}</span>
                  </span>
                  <span className="justify-self-end"><StatusOutlineToken status={event.flow.status} /></span>
                </button>
              )) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No scheduled flows yet.</div>}
            </div>
          </div>
        </div>

        <aside className={cn(surfaceClass, 'flex flex-col p-5')}>
          <h2 className="text-base font-semibold text-[var(--app-text)]">Needs attention</h2>
          <div className="mt-4 flex-1 divide-y divide-[var(--app-border)] overflow-hidden border-y border-[var(--app-border)]">
            {attentionItems.length ? attentionItems.map((item) => (
              <button key={item.flow.id} type="button" onClick={() => setSelectedFlowID(item.flow.id)} className="flex w-full items-start gap-3 px-3 py-3 text-left transition hover:bg-[var(--app-surface-subtle)]">
                <FlowStatusDot status={item.dotStatus} className="mt-1" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-[var(--app-text)]">{item.flow.name}</span>
                  <span className="mt-1 block truncate text-xs text-[var(--app-text-muted)]">{item.meta}</span>
                </span>
                <StatusOutlineToken status={item.flow.status} />
              </button>
            )) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No flows need attention.</div>}
          </div>
          <div className="mt-4 text-xs text-[var(--app-text-muted)]">{reviewCount} needs review • {pausedCount} paused • {draftCount} draft</div>
        </aside>
      </section>

      <section className={cn(surfaceClass, 'overflow-hidden')}>
        <div className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--app-border)] p-5">
          <div>
            <h2 className="text-base font-semibold text-[var(--app-text)]">Flow controls</h2>
            <p className="mt-1 text-sm text-[var(--app-text-muted)]">Run and delete controller-backed flows.</p>
          </div>
          <div className="flex flex-1 flex-wrap items-center justify-end gap-2">
            <label className="relative w-[148px] shrink-0">
              <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
              <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search flows" className="h-9 min-h-9 rounded-xl border-[var(--app-border)] bg-[var(--app-surface-subtle)] py-0 pl-8 pr-3 text-xs focus-visible:ring-0 focus-visible:ring-offset-0" />
            </label>
            <FilterSelect label="Workspace filter" value={workspaceFilter} onChange={setWorkspaceFilter} options={workspaceOptions} />
            <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentFilterOptions} />
            <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
          </div>
        </div>

        <div className="overflow-x-auto">
          <table data-testid="flows-table" className="w-full min-w-[840px] border-collapse text-left">
            <thead>
              <tr className="border-b border-[var(--app-border)] text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">
                <th className="px-5 py-3 font-medium">Flow</th>
                <th className="px-4 py-3 font-medium">Last run</th>
                <th className="px-4 py-3 font-medium">Total</th>
                <th className="px-4 py-3 font-medium">Next run</th>
                <th className="px-4 py-3 text-center font-medium">Status</th>
                <th className="px-4 py-3 text-center font-medium">Enabled</th>
                <th className="px-5 py-3 text-center font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filteredFlows.length ? filteredFlows.map((flow) => (
                <tr key={flow.id} data-testid="flows-row" data-flow-id={flow.id} className="border-b border-[var(--app-border)] last:border-b-0 hover:bg-[var(--app-surface-subtle)]">
                  <td className="px-5 py-4 align-top">
                    <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="max-w-[520px] text-left">
                      <div className="truncate text-sm font-medium text-[var(--app-text)]">{flow.name}</div>
                      <div className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">{flow.task}</div>
                      <div className="mt-3 flex max-w-[680px] flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-[var(--app-text-subtle)]">
                        <span className="inline-flex min-w-0 items-center gap-1.5"><MapPin size={12} className="shrink-0" /> <span className="truncate">{flow.workspace} / {flow.target}</span></span>
                        <span className="inline-flex min-w-0 items-center gap-1.5"><Bot size={12} className="shrink-0" /> <span className="truncate">{flow.agent}</span></span>
                        <span className="inline-flex min-w-0 items-center gap-1.5"><Clock size={12} className="shrink-0" /> <span className="truncate">{flow.schedule}</span></span>
                      </div>
                    </button>
                  </td>
                  <td className="px-4 py-4 align-middle">
                    <FlowDateTime value={flow.lastRun} />
                  </td>
                  <td className="px-4 py-4 align-middle">
                    <div className="font-mono text-sm text-[var(--app-text)]">{flow.totalRuns}</div>
                  </td>
                  <td className="px-4 py-4 align-middle">
                    <FlowDateTime value={flow.nextRun} meta={flow.nextRunMeta} />
                  </td>
                  <td className="px-4 py-4 align-middle">
                    <div className="flex justify-center">
                      <StatusOutlineToken status={flow.status} />
                    </div>
                  </td>
                  <td className="px-4 py-4 align-middle">
                    <div className="flex justify-center">
                      <EnabledToggle enabled={flow.enabled} disabled onToggle={() => undefined} />
                    </div>
                  </td>
                  <td className="px-5 py-4 align-middle">
                    <div className="flex justify-center">
                      <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="inline-flex h-8 w-8 items-center justify-center rounded-lg border border-[var(--app-border)] text-[var(--app-text-muted)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" aria-label={`Manage ${flow.name}`}>
                        <MoreHorizontal size={16} />
                      </button>
                    </div>
                  </td>
                </tr>
              )) : (
                <tr><td colSpan={7} className="px-5 py-8 text-center text-sm text-[var(--app-text-muted)]">No flows found.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <AddFlowModal
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onAdd={(input) => void addFlow(input)}
        busy={saving}
        targetOptions={targetOptions}
        workspaceOptions={addWorkspaceOptions}
        agentOptions={savedAgentOptions}
        loadingOptions={loadingAddFlowOptions}
      />
    </div>
  )
}
