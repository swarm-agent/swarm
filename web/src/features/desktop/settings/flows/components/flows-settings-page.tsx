import { useCallback, useEffect, useMemo, useState, type ChangeEvent, type FormEvent, type ReactNode } from 'react'
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
  setFlowEnabled,
  updateFlow,
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
type ScheduleMode = 'guided' | 'cron'
type DailyScheduleMode = 'once' | 'times_between' | 'interval_window'

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
  targetStale: boolean
  targetStaleReason: string
  raw: FlowSummaryRecord
}

export interface AddFlowForm {
  name: string
  agentKey: string
  targetKey: string
  scheduleMode: ScheduleMode
  scheduleCadence: ScheduleCadence
  dailyMode: DailyScheduleMode
  scheduleTimes: string[]
  dailyRunCount: string
  dailyIntervalHours: string
  dailyWindowStart: string
  dailyWindowEnd: string
  highRunCountConfirmed: boolean
  scheduleDay: string
  scheduleDate: string
  timezone: string
  cronExpression: string
  workspacePath: string
  task: string
}

const flowSwarmTargetsQueryKey = ['flows', 'swarm-targets'] as const
const flowWorkspacesQueryKey = ['flows', 'workspaces'] as const
const flowWorkspacesForTargetQueryKey = (targetKey: string) => [...flowWorkspacesQueryKey, 'target', targetKey] as const

export interface FlowTargetOption {
  key: string
  label: string
  helper: string
  groupLabel: string
  target: FlowSwarmTarget
}

export interface FlowWorkspaceOption {
  key: string
  label: string
  helper: string
  workspace: FlowWorkspaceEntry
}

export interface FlowAgentOption {
  key: string
  label: string
  helper: string
  contractSummary: string
  groupLabel: string
  profile: FlowAgentProfile
}

const scheduleCadenceOptions: ScheduleCadence[] = ['Daily', 'Weekly', 'Monthly']
const dailyScheduleModeOptions: Array<{ value: DailyScheduleMode; label: string; helper: string }> = [
  { value: 'once', label: 'Once per day', helper: 'One predictable daily run.' },
  { value: 'times_between', label: 'X times in window', helper: 'Evenly spread runs between two times.' },
  { value: 'interval_window', label: 'Every X hours', helper: 'Repeat on an hourly interval inside a window.' },
]
const scheduleDayOptions = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const scheduleDateOptions = Array.from({ length: 31 }, (_, index) => String(index + 1))
const scheduleTimeOptions = Array.from({ length: 48 }, (_, index) => {
  const hour24 = Math.floor(index / 2)
  const minute = index % 2 === 0 ? '00' : '30'
  const period = hour24 < 12 ? 'AM' : 'PM'
  const hour12 = hour24 % 12 === 0 ? 12 : hour24 % 12
  return `${hour12}:${minute} ${period}`
})
const highDailyRunWarningThreshold = 8
const maxDailyScheduleTimes = 48

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
  if (form.scheduleCadence !== 'Daily') {
    return [form.scheduleTimes[0] || '12:00 AM']
  }
  switch (form.dailyMode) {
    case 'times_between':
      return spreadTimesBetween(form.dailyRunCount, form.dailyWindowStart, form.dailyWindowEnd)
    case 'interval_window':
      return intervalTimesBetween(form.dailyIntervalHours, form.dailyWindowStart, form.dailyWindowEnd)
    case 'once':
    default:
      return [form.scheduleTimes[0] || '12:00 AM']
  }
}

const defaultAddFlowForm: AddFlowForm = {
  name: '',
  agentKey: '',
  targetKey: '',
  scheduleMode: 'guided',
  scheduleCadence: 'Daily',
  dailyMode: 'once',
  scheduleTimes: ['12:00 AM'],
  dailyRunCount: '4',
  dailyIntervalHours: '2',
  dailyWindowStart: '9:00 AM',
  dailyWindowEnd: '5:00 PM',
  highRunCountConfirmed: false,
  scheduleDay: 'Mon',
  scheduleDate: '1',
  timezone: userTimeZone(),
  cronExpression: '0 9,13,17 * * Mon-Fri',
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
const filterControlClass = 'inline-flex min-h-9 min-w-0 max-w-full items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 text-xs text-[var(--app-text-muted)] transition hover:border-[var(--app-border-strong)] hover:text-[var(--app-text)]'
const fieldClass = 'h-9 w-full min-w-0 rounded-xl border border-[rgba(255,255,255,0.13)] bg-[rgba(255,255,255,0.045)] px-3 text-sm text-[rgba(255,255,255,0.88)] outline-none transition placeholder:text-[rgba(255,255,255,0.38)] hover:border-[rgba(255,255,255,0.20)] focus:border-[#b87586] focus:ring-1 focus:ring-[#b87586]/25 disabled:cursor-not-allowed disabled:opacity-55'
const textareaClass = 'min-h-[80px] w-full min-w-0 resize-none rounded-xl border border-[rgba(255,255,255,0.13)] bg-[rgba(255,255,255,0.045)] px-3 py-2 text-sm leading-5 text-[rgba(255,255,255,0.88)] outline-none transition placeholder:text-[rgba(255,255,255,0.38)] hover:border-[rgba(255,255,255,0.20)] focus:border-[#b87586] focus:ring-1 focus:ring-[#b87586]/25'
const labelClass = 'text-[11px] font-medium uppercase tracking-[0.14em] text-[rgba(255,255,255,0.62)]'
const helperClass = 'text-[11px] leading-4 text-[rgba(255,255,255,0.55)]'

function buildScheduleLabel(form: AddFlowForm) {
  if (form.scheduleMode === 'cron') {
    const expression = form.cronExpression.trim()
    return expression ? `Raw cron controls timing: ${expression} (${form.timezone})` : 'Raw cron controls all run timing.'
  }
  if (form.scheduleCadence === 'On demand') {
    return 'Runs only when you start it manually.'
  }
  const times = scheduleTimesForCadence(form)
  const timesLabel = times.join(', ')
  if (form.scheduleCadence === 'Weekly') {
    return `Every ${formatSelectedDays(form.scheduleDay)} at ${timesLabel} (${form.timezone})`
  }
  if (form.scheduleCadence === 'Monthly') {
    return `Monthly on day ${form.scheduleDate} at ${timesLabel} (${form.timezone})`
  }
  return `Every day at ${timesLabel} (${form.timezone})`
}

function minutesFromClockLabel(value: string): number {
  const hhmm = clockLabelToHHMM(value)
  const [hour, minute] = hhmm.split(':').map(Number)
  if (!Number.isFinite(hour) || !Number.isFinite(minute)) {
    return 0
  }
  return hour * 60 + minute
}

function clockLabelFromMinutes(value: number): string {
  const normalized = ((Math.round(value) % 1440) + 1440) % 1440
  const hour24 = Math.floor(normalized / 60)
  const minute = normalized % 60
  const period = hour24 < 12 ? 'AM' : 'PM'
  const hour12 = hour24 % 12 === 0 ? 12 : hour24 % 12
  return `${hour12}:${String(minute).padStart(2, '0')} ${period}`
}

function spreadTimesBetween(runCountValue: string, startValue: string, endValue: string): string[] {
  const count = Math.max(1, Math.min(maxDailyScheduleTimes, Number.parseInt(runCountValue, 10) || 1))
  const start = minutesFromClockLabel(startValue)
  const end = Math.max(start, minutesFromClockLabel(endValue))
  if (count === 1 || end === start) {
    return [clockLabelFromMinutes(start)]
  }
  const step = (end - start) / (count - 1)
  return Array.from({ length: count }, (_, index) => clockLabelFromMinutes(start + step * index))
}

function intervalTimesBetween(intervalValue: string, startValue: string, endValue: string): string[] {
  const intervalMinutes = Math.max(30, Math.min(24 * 60, (Number.parseInt(intervalValue, 10) || 1) * 60))
  const start = minutesFromClockLabel(startValue)
  const end = Math.max(start, minutesFromClockLabel(endValue))
  const times: string[] = []
  for (let cursor = start; cursor <= end && times.length < maxDailyScheduleTimes; cursor += intervalMinutes) {
    times.push(clockLabelFromMinutes(cursor))
  }
  return times.length ? times : [clockLabelFromMinutes(start)]
}

function selectedScheduleDays(value: string): string[] {
  const selected = value
    .split(',')
    .map((day) => day.trim())
    .filter((day) => scheduleDayOptions.includes(day))
  return selected.length ? Array.from(new Set(selected)) : ['Mon']
}

function formatSelectedDays(value: string): string {
  const days = selectedScheduleDays(value)
  if (days.length === 1) {
    return days[0]
  }
  if (days.length === 2) {
    return `${days[0]} and ${days[1]}`
  }
  return `${days.slice(0, -1).join(', ')}, and ${days[days.length - 1]}`
}

function scheduleDayToCron(value: string): string {
  return selectedScheduleDays(value).join(',')
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

function expandCronField(field: string, min: number, max: number): number[] {
  const values = new Set<number>()
  for (const rawPart of field.split(',')) {
    const part = rawPart.trim()
    if (!part) {
      continue
    }
    const [rangePart, stepPart] = part.split('/')
    const step = Math.max(1, Number.parseInt(stepPart || '1', 10) || 1)
    const [rawStart, rawEnd] = rangePart === '*' ? [String(min), String(max)] : rangePart.split('-')
    const start = Math.max(min, Number.parseInt(rawStart, 10))
    const end = Math.min(max, Number.parseInt(rawEnd || rawStart, 10))
    if (!Number.isFinite(start) || !Number.isFinite(end) || start > end) {
      return []
    }
    for (let value = start; value <= end; value += step) {
      values.add(value)
    }
  }
  return Array.from(values).sort((left, right) => left - right)
}

function cronDayPreview(field: string): string[] {
  if (field === '*') {
    return ['Every day']
  }
  const dayAliases: Record<string, string> = { '0': 'Sun', '1': 'Mon', '2': 'Tue', '3': 'Wed', '4': 'Thu', '5': 'Fri', '6': 'Sat', '7': 'Sun' }
  return field.split(',').map((part) => {
    const trimmed = part.trim()
    const [rangePart] = trimmed.split('/')
    if (rangePart.includes('-')) {
      const [start, end] = rangePart.split('-')
      return `${dayAliases[start] || start}-${dayAliases[end] || end}`
    }
    return dayAliases[rangePart] || rangePart
  }).filter(Boolean)
}

function cronPreviewLabels(expression: string): string[] {
  const fields = expression.trim().split(/\s+/)
  if (fields.length !== 5) {
    return ['Enter a 5-field cron expression to preview timing.']
  }
  const [minuteField, hourField, dayOfMonthField, monthField, dayField] = fields
  const minutes = expandCronField(minuteField, 0, 59)
  const hours = expandCronField(hourField, 0, 23)
  const days = cronDayPreview(dayField)
  if (!minutes.length || !hours.length || !days.length) {
    return ['Preview unavailable for this cron expression.']
  }
  const labels: string[] = []
  for (const day of days) {
    for (const hour of hours) {
      for (const minute of minutes) {
        labels.push(`${day} ${hhmmToDisplay(`${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`)}`)

      }
    }
  }
  if (dayOfMonthField !== '*' || monthField !== '*') {
    labels.push(`Limited by date/month fields: ${dayOfMonthField} ${monthField}`)
  }
  return labels.length ? labels : ['Preview unavailable for this cron expression.']
}

export function targetOptionKey(target: FlowSwarmTarget): string {
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

function targetDisplayName(target: FlowSwarmTarget): string {
  return target.name?.trim() || target.swarm_id?.trim() || target.kind
}

function targetHostKey(target: FlowSwarmTarget): string {
  return target.host_swarm_id?.trim() || ''
}

function managedTargetGroupKey(target: FlowSwarmTarget): string {
  return targetHostKey(target) || target.swarm_id?.trim() || target.name?.trim() || 'managed-host'
}

function isPrimaryTarget(target: FlowSwarmTarget): boolean {
  const kind = target.kind?.trim().toLowerCase()
  const relationship = target.relationship?.trim().toLowerCase()
  return kind === 'self' || relationship === 'self' || target.current
}

function isPrimaryContainerTarget(target: FlowSwarmTarget): boolean {
  return target.kind?.trim().toLowerCase() === 'local'
}

function isManagedHostTarget(target: FlowSwarmTarget): boolean {
  const kind = target.kind?.trim().toLowerCase()
  const relationship = target.relationship?.trim().toLowerCase()
  return kind === 'host' || relationship === 'managed'
}

export function targetOptionLabel(target: FlowSwarmTarget): string {
  return target.current ? `${targetDisplayName(target)} (current)` : targetDisplayName(target)
}

export function targetOptionHelper(target: FlowSwarmTarget): string {
  if (!target.selectable) {
    return target.last_error?.trim() || 'Not selectable from here.'
  }
  if (isPrimaryTarget(target)) {
    return 'Runs on the primary swarm.'
  }
  if (isManagedHostTarget(target)) {
    return 'Runs on this managed host.'
  }
  const host = target.host_swarm_id?.trim()
  return host ? 'Runs in a container on its managed host.' : 'Runs in a linked container.'
}

export function targetOptionGroupLabel(target: FlowSwarmTarget, _targets: FlowSwarmTarget[]): string {
  if (isPrimaryTarget(target) || isPrimaryContainerTarget(target) || !targetHostKey(target) && !isManagedHostTarget(target)) {
    return 'Primary'
  }
  return 'Managed host'
}

export function groupedTargetOptions(options: FlowTargetOption[]): Array<{ key: string; label: string; options: FlowTargetOption[] }> {
  const primaryGroup = { key: 'primary', label: 'Primary', options: [] as FlowTargetOption[] }
  const managedGroups: Array<{ key: string; label: string; options: FlowTargetOption[] }> = []
  const managedByHost = new Map<string, { key: string; label: string; options: FlowTargetOption[] }>()

  function ensureManagedGroup(option: FlowTargetOption): { key: string; label: string; options: FlowTargetOption[] } {
    const hostKey = managedTargetGroupKey(option.target)
    let group = managedByHost.get(hostKey)
    if (!group) {
      group = { key: `managed:${hostKey}`, label: 'Managed host', options: [] }
      managedByHost.set(hostKey, group)
      managedGroups.push(group)
    }
    return group
  }

  for (const option of options) {
    const target = option.target
    if (isPrimaryTarget(target) || isPrimaryContainerTarget(target) || !targetHostKey(target) && !isManagedHostTarget(target)) {
      primaryGroup.options.push(option)
      continue
    }
    const group = ensureManagedGroup(option)
    if (isManagedHostTarget(target)) {
      group.options.unshift(option)
      continue
    }
    group.options.push(option)
  }

  return [primaryGroup, ...managedGroups].filter((group) => group.options.length > 0)
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

function workspaceBindingID(workspace: FlowWorkspaceEntry): string {
  return workspace.topologyRoutes.find((route) => route.workspaceBindingId.trim())?.workspaceBindingId.trim() || ''
}

function workspaceRuntimePath(workspace: FlowWorkspaceEntry): string {
  return workspace.topologyRoutes.find((route) => route.runtimeWorkspacePath.trim())?.runtimeWorkspacePath.trim() || ''
}

function workspaceOptionKey(workspace: FlowWorkspaceEntry): string {
  const bindingID = workspaceBindingID(workspace)
  return bindingID ? `binding:${bindingID}` : workspace.path
}

function workspaceOptionLabel(workspace: FlowWorkspaceEntry): string {
  const name = workspace.workspaceName?.trim()
  return name ? `${name} — ${workspace.path}` : workspace.path
}

function workspaceOptionHelper(workspace: FlowWorkspaceEntry): string {
  const routeCount = workspace.topologyRoutes.length
  const directoryCount = workspace.directories.length
  return [workspace.active ? 'active' : '', routeCount ? `${routeCount} topology route${routeCount === 1 ? '' : 's'}` : '', directoryCount ? `${directoryCount} director${directoryCount === 1 ? 'y' : 'ies'}` : '']
    .filter(Boolean)
    .join(' • ')
}

export function workspaceOptionsFromEntries(workspaces: FlowWorkspaceEntry[]): FlowWorkspaceOption[] {
  const seen = new Set<string>()
  return workspaces
    .map((workspace) => ({ key: workspaceOptionKey(workspace), label: workspaceOptionLabel(workspace), helper: workspaceOptionHelper(workspace), workspace }))
    .filter((option) => {
      if (!option.key || seen.has(option.key)) {
        return false
      }
      seen.add(option.key)
      return true
    })
}

export function agentOptionKey(profile: FlowAgentProfile): string {
  return `${profile.name.trim().toLowerCase()}::${profile.mode.trim().toLowerCase()}`
}

export function agentOptionLabel(profile: FlowAgentProfile): string {
  return profile.name.trim() || 'Unnamed agent'
}

export function agentOptionGroupLabel(profile: FlowAgentProfile): string {
  switch (profile.mode.trim().toLowerCase()) {
    case 'primary':
      return 'Primary'
    case 'background':
      return 'Background'
    default:
      return 'Assigned Subagents'
  }
}

export function agentOptionGroupRank(profile: FlowAgentProfile): number {
  switch (profile.mode.trim().toLowerCase()) {
    case 'primary':
      return 0
    case 'background':
      return 2
    default:
      return 1
  }
}

export function groupedAgentOptions(options: FlowAgentOption[]): Array<{ label: string; options: FlowAgentOption[] }> {
  const groups: Array<{ label: string; options: FlowAgentOption[] }> = []
  for (const option of options) {
    let group = groups.find((candidate) => candidate.label === option.groupLabel)
    if (!group) {
      group = { label: option.groupLabel, options: [] }
      groups.push(group)
    }
    group.options.push(option)
  }
  return groups
}

export function agentOptionHelper(profile: FlowAgentProfile): string {
  const provider = profile.provider.trim() || 'Inherit'
  const model = profile.model.trim() || 'Inherit'
  const thinking = profile.thinking.trim() || 'Inherit'
  return `${provider} / ${model} • thinking ${thinking}`
}

export function agentContractSummary(profile: FlowAgentProfile): string {
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

function nearestScheduleTimeLabel(value?: string): string {
  if (!value) {
    return '12:00 AM'
  }
  const label = hhmmToDisplay(value)
  return scheduleTimeOptions.includes(label) ? label : '12:00 AM'
}

export function optionKeyForTargetSelection(selection: FlowSummaryRecord['definition']['target'], targets: FlowTargetOption[]): string {
  const normalized = {
    swarmID: selection.swarm_id?.trim().toLowerCase() || '',
    deploymentID: selection.deployment_id?.trim().toLowerCase() || '',
    kind: selection.kind?.trim().toLowerCase() || '',
    name: selection.name?.trim().toLowerCase() || '',
  }
  return targets.find((option) => {
    const target = option.target
    return (
      (!!normalized.swarmID && target.swarm_id?.trim().toLowerCase() === normalized.swarmID) ||
      (!!normalized.deploymentID && target.deployment_id?.trim().toLowerCase() === normalized.deploymentID) ||
      (!!normalized.name && target.name?.trim().toLowerCase() === normalized.name && target.kind?.trim().toLowerCase() === normalized.kind)
    )
  })?.key || targets[0]?.key || ''
}

export function optionKeyForAgentSelection(selection: FlowSummaryRecord['definition']['agent'], agents: FlowAgentOption[]): string {
  const profileName = selection.profile_name?.trim().toLowerCase() || ''
  const profileMode = selection.profile_mode?.trim().toLowerCase() || ''
  return agents.find((option) => {
    const profile = option.profile
    return profile.name.trim().toLowerCase() === profileName && (!profileMode || profile.mode.trim().toLowerCase() === profileMode)
  })?.key || agents[0]?.key || ''
}

export function optionKeyForWorkspaceContext(workspace: FlowSummaryRecord['definition']['workspace'], workspaces: FlowWorkspaceOption[]): string {
  const path = workspace.workspace_path?.trim() || workspace.host_workspace_path?.trim() || workspace.cwd?.trim() || ''
  return workspaces.find((option) => option.workspace.path === path)?.key || path || workspaces[0]?.key || ''
}

function dailyModeFromTimes(times: string[]): DailyScheduleMode {
  if (times.length <= 1) {
    return 'once'
  }
  const minutes = times.map(minutesFromClockLabel)
  const step = minutes[1] - minutes[0]
  if (step > 0 && minutes.every((value, index) => index === 0 || value - minutes[index - 1] === step) && step % 60 === 0) {
    return 'interval_window'
  }
  return 'times_between'
}

export function recordToFlowForm(record: FlowSummaryRecord, targets: FlowTargetOption[], workspaces: FlowWorkspaceOption[], agents: FlowAgentOption[]): AddFlowForm {
  const schedule = record.definition.schedule
  const cadence = cadenceLabel(schedule.cadence)
  const rawTimes = Array.isArray(schedule.times) && schedule.times.length ? schedule.times : schedule.time ? [schedule.time] : []
  const scheduleTimes = rawTimes.length ? rawTimes.map(nearestScheduleTimeLabel) : ['12:00 AM']
  const dailyMode = cadence === 'Daily' ? dailyModeFromTimes(scheduleTimes) : 'once'
  const dailyIntervalHours = scheduleTimes.length > 1 ? String(Math.max(1, Math.round((minutesFromClockLabel(scheduleTimes[1]) - minutesFromClockLabel(scheduleTimes[0])) / 60))) : defaultAddFlowForm.dailyIntervalHours
  return {
    ...defaultAddFlowForm,
    name: record.definition.name || record.definition.flow_id,
    agentKey: optionKeyForAgentSelection(record.definition.agent, agents),
    targetKey: optionKeyForTargetSelection(record.definition.target, targets),
    scheduleMode: schedule.cron?.trim() ? 'cron' : 'guided',
    scheduleCadence: cadence,
    dailyMode,
    scheduleTimes,
    dailyRunCount: String(Math.max(1, scheduleTimes.length)),
    dailyIntervalHours,
    dailyWindowStart: scheduleTimes[0] || defaultAddFlowForm.dailyWindowStart,
    dailyWindowEnd: scheduleTimes[scheduleTimes.length - 1] || defaultAddFlowForm.dailyWindowEnd,
    highRunCountConfirmed: scheduleTimes.length > highDailyRunWarningThreshold,
    scheduleDay: schedule.weekday || defaultAddFlowForm.scheduleDay,
    scheduleDate: String(schedule.month_day || defaultAddFlowForm.scheduleDate),
    timezone: schedule.timezone || defaultAddFlowForm.timezone,
    cronExpression: schedule.cron?.trim() || defaultAddFlowForm.cronExpression,
    workspacePath: optionKeyForWorkspaceContext(record.definition.workspace, workspaces),
    task: record.definition.intent.prompt || '',
  }
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
    return <div className="min-w-0 break-words text-sm text-[var(--app-text)]">{value || '—'}</div>
  }
  const match = value.match(/^(.*)\s(\d{1,2}:\d{2}\u00A0(?:AM|PM))$/)
  if (!match) {
    return <div className="min-w-0 break-words text-sm text-[var(--app-text)]">{value}</div>
  }
  return (
    <div className="min-w-0 leading-tight">
      <div className="break-words text-sm text-[var(--app-text)]">{match[1]}</div>
      <div className="mt-1 break-words font-mono text-xs text-[var(--app-text-muted)]">{match[2]}</div>
      {meta ? <div className="mt-1 break-words text-xs text-[var(--app-text-muted)]">{meta}</div> : null}
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

function targetSelectionLabel(selection: FlowSummaryRecord['definition']['target']): string {
  return selection.swarm_id?.trim() || selection.deployment_id?.trim() || selection.name?.trim() || selection.kind?.trim() || 'target'
}

export function flowHasUnresolvedTarget(record: FlowSummaryRecord): boolean {
  const hasTarget = Boolean(targetSelectionLabel(record.definition.target) !== 'target')
  return Boolean(record.target_stale || (hasTarget && !record.target_detail))
}

function flowUnresolvedTargetReason(record: FlowSummaryRecord): string {
  return record.target_stale_reason?.trim() || `Saved flow target ${targetSelectionLabel(record.definition.target)} no longer resolves.`
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
    return `Weekly on ${formatSelectedDays(schedule.weekday || 'Mon')} ${timeLabel}${timezoneSuffix}`
  }
  if (cadence === 'Monthly') {
    return `Monthly on day ${schedule.month_day || 1} ${timeLabel}${timezoneSuffix}`
  }
  return `Daily at ${timeLabel}${timezoneSuffix}`
}

function statusFromRecord(record: FlowSummaryRecord): FlowStatus {
  if (flowHasUnresolvedTarget(record)) {
    return 'needs_review'
  }
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
  const targetStale = flowHasUnresolvedTarget(record)
  const target = targetStale
    ? `${targetSelectionLabel(assignment.target)} (stale)`
    : record.target_detail?.name?.trim() || record.target_detail?.swarm_id?.trim() || assignment.target.name?.trim() || assignment.target.swarm_id?.trim() || assignment.target.kind?.trim() || 'local'
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
    targetStale,
    targetStaleReason: targetStale ? flowUnresolvedTargetReason(record) : '',
    raw: record,
  }
}

export function formToCreateInput(form: AddFlowForm, targets: FlowTargetOption[], workspaces: FlowWorkspaceOption[], agents: FlowAgentOption[], enabled?: boolean): CreateFlowInput {
  const isOnDemand = form.scheduleCadence === 'On demand'
  const cadence = isOnDemand ? 'on_demand' : form.scheduleCadence.toLowerCase()
  const selectedTimes = scheduleTimesForCadence(form).map((value) => clockLabelToHHMM(value))
  const targetOption = targets.find((option) => option.key === form.targetKey)
  const workspaceOption = workspaces.find((option) => option.key === form.workspacePath)
  const agentOption = agents.find((option) => option.key === form.agentKey)
  const workspacePath = workspaceOption?.workspace.path.trim() || form.workspacePath.trim()
  const workspaceName = workspaceOption?.workspace.workspaceName.trim() || workspacePath.replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean).pop() || ''
  const workspaceBindingId = workspaceOption ? workspaceBindingID(workspaceOption.workspace) : ''
  const runtimeWorkspacePath = workspaceOption ? workspaceRuntimePath(workspaceOption.workspace) : ''
  const task = form.task.trim() || 'Run the configured task prompt.'
  const cronExpression = form.scheduleMode === 'cron' ? form.cronExpression.trim() : ''
  return {
    name: form.name.trim() || 'Untitled flow',
    enabled: enabled ?? !isOnDemand,
    target: targetToSelection(targetOption?.target),
    agent: {
      profile_name: agentOption?.profile.name.trim() || '',
      profile_mode: agentOption?.profile.mode.trim() || undefined,
    },
    workspace: {
      workspace_path: workspacePath,
      host_workspace_path: workspacePath,
      runtime_workspace_path: runtimeWorkspacePath || undefined,
      workspace_binding_id: workspaceBindingId || undefined,
      workspace_name: workspaceName || undefined,
      cwd: workspacePath,
    },
    schedule: {
      cadence,
      time: cadence === 'on_demand' ? undefined : selectedTimes[0],
      times: cadence === 'on_demand' ? undefined : selectedTimes,
      weekday: cadence === 'weekly' ? scheduleDayToCron(form.scheduleDay) : undefined,
      month_day: cadence === 'monthly' ? Number(form.scheduleDate) : undefined,
      timezone: form.timezone.trim() || 'UTC',
      cron: cronExpression || undefined,
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
      <span className="min-w-0 truncate">{options.find((option) => option.value === value)?.label ?? label}</span>
      <ChevronDown size={14} className="absolute right-3 shrink-0 text-[var(--app-text-muted)]" />
    </label>
  )
}

export function FlowSettingsModal({
  open,
  mode = 'create',
  initialForm,
  enabledOverride,
  onClose,
  onConfirm,
  busy,
  targetOptions,
  workspaceOptions,
  agentOptions,
  loadingOptions,
  loadWorkspacesForTarget,
  footerAccessory,
}: {
  open: boolean
  mode?: 'create' | 'edit'
  initialForm?: AddFlowForm | null
  enabledOverride?: boolean
  onClose: () => void
  onConfirm: (input: CreateFlowInput) => void
  busy?: boolean
  targetOptions: FlowTargetOption[]
  workspaceOptions: FlowWorkspaceOption[]
  agentOptions: FlowAgentOption[]
  loadingOptions?: boolean
  loadWorkspacesForTarget: (target: FlowSwarmTarget, signal?: AbortSignal) => Promise<FlowWorkspaceEntry[]>
  footerAccessory?: ReactNode
}) {
  const defaultInitialForm = useMemo(() => initialAddFlowForm(targetOptions, workspaceOptions, agentOptions), [agentOptions, targetOptions, workspaceOptions])
  const effectiveInitialForm = useMemo(() => initialForm ?? defaultInitialForm, [defaultInitialForm, initialForm])
  const [form, setForm] = useState<AddFlowForm>(effectiveInitialForm)
  const [targetWorkspaceOptions, setTargetWorkspaceOptions] = useState<FlowWorkspaceOption[]>(workspaceOptions)
  const [targetWorkspacesLoading, setTargetWorkspacesLoading] = useState(false)
  const [targetWorkspacesError, setTargetWorkspacesError] = useState('')
  const [now, setNow] = useState(() => new Date())

  useEffect(() => {
    if (open) {
      setForm(effectiveInitialForm)
      setTargetWorkspaceOptions(workspaceOptions)
      setTargetWorkspacesError('')
      setNow(new Date())
    }
  }, [effectiveInitialForm, open, workspaceOptions])

  useEffect(() => {
    if (!open) {
      return undefined
    }
    const interval = window.setInterval(() => setNow(new Date()), 1000)
    return () => window.clearInterval(interval)
  }, [open])

  useEffect(() => {
    if (!open) {
      return undefined
    }
    const targetKey = form.targetKey
    const target = targetOptions.find((option) => option.key === targetKey)?.target
    if (!target) {
      setTargetWorkspaceOptions([])
      setForm((current) => current.workspacePath ? { ...current, workspacePath: '' } : current)
      return undefined
    }
    const controller = new AbortController()
    setTargetWorkspacesLoading(true)
    setTargetWorkspacesError('')
    void loadWorkspacesForTarget(target, controller.signal)
      .then((workspaces) => {
        if (controller.signal.aborted) {
          return
        }
        const options = workspaceOptionsFromEntries(workspaces)
        setTargetWorkspaceOptions(options)
        setForm((current) => {
          if (current.targetKey !== targetKey) {
            return current
          }
          if (options.some((option) => option.key === current.workspacePath)) {
            return current
          }
          return { ...current, workspacePath: options.find((option) => option.workspace.active)?.key ?? options[0]?.key ?? '' }
        })
      })
      .catch((err) => {
        if (controller.signal.aborted) {
          return
        }
        setTargetWorkspaceOptions([])
        setTargetWorkspacesError(err instanceof Error ? err.message : 'Failed to load target workspaces')
        setForm((current) => current.targetKey === targetKey && current.workspacePath ? { ...current, workspacePath: '' } : current)
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setTargetWorkspacesLoading(false)
        }
      })
    return () => controller.abort()
  }, [form.targetKey, loadWorkspacesForTarget, open, targetOptions])

  if (!open) {
    return null
  }

  const scopedWorkspaceOptions = targetWorkspaceOptions
  const selectorsLoading = loadingOptions || targetWorkspacesLoading

  const update = (field: keyof AddFlowForm) => (event: ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
    const value = event.target.value
    setForm((current) => {
      if (field === 'scheduleCadence') {
        const nextCadence = value as ScheduleCadence
        return { ...current, scheduleCadence: nextCadence, scheduleTimes: [current.scheduleTimes[0] || '12:00 AM'], highRunCountConfirmed: false }
      }
      if (field === 'dailyMode') {
        const nextMode = value as DailyScheduleMode
        return { ...current, dailyMode: nextMode, scheduleTimes: [current.scheduleTimes[0] || '12:00 AM'], highRunCountConfirmed: false }
      }
      if (field === 'dailyRunCount' || field === 'dailyIntervalHours' || field === 'dailyWindowStart' || field === 'dailyWindowEnd') {
        return { ...current, [field]: value, highRunCountConfirmed: false }
      }
      if (field === 'targetKey') {
        return { ...current, targetKey: value, workspacePath: '' }
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
  const toggleScheduleDay = (day: string) => {
    setForm((current) => {
      const selected = selectedScheduleDays(current.scheduleDay)
      const next = selected.includes(day) ? selected.filter((value) => value !== day) : [...selected, day]
      return { ...current, scheduleDay: (next.length ? next : [day]).join(',') }
    })
  }

  const schedulePreview = buildScheduleLabel(form)
  const selectedTarget = targetOptions.find((option) => option.key === form.targetKey)
  const selectedWorkspace = scopedWorkspaceOptions.find((option) => option.key === form.workspacePath)
  const selectedAgent = agentOptions.find((option) => option.key === form.agentKey)
  const guidedScheduleTimes = scheduleTimesForCadence(form)
  const cronPreviewTimes = cronPreviewLabels(form.cronExpression)
  const selectedScheduleDayValues = selectedScheduleDays(form.scheduleDay)
  const selectedTimezoneNow = timeInZone(form.timezone, now)
  const needsHighRunCountConfirmation = form.scheduleMode === 'guided' && form.scheduleCadence === 'Daily' && guidedScheduleTimes.length > highDailyRunWarningThreshold
  const canSubmit = Boolean(selectedTarget && selectedWorkspace && selectedAgent && form.task.trim()) && (form.scheduleMode !== 'cron' || Boolean(form.cronExpression.trim())) && (!needsHighRunCountConfirmation || form.highRunCountConfirmed) && !busy && !targetWorkspacesLoading

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!canSubmit) {
      return
    }
    onConfirm(formToCreateInput(form, targetOptions, scopedWorkspaceOptions, agentOptions, enabledOverride))
    setForm(effectiveInitialForm)
  }

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={mode === 'edit' ? 'Edit Flow' : 'Add Flow'} className="z-[80] p-2 sm:p-5" data-testid="flows-add-modal">
      <DialogBackdrop onClick={busy ? undefined : onClose} />
      <DialogPanel className="max-h-[calc(100vh-16px)] w-[min(920px,100%)] min-w-0 gap-0 overflow-hidden rounded-[14px] border border-[rgba(255,255,255,0.12)] bg-[#1a1921] p-0 shadow-xl shadow-black/30 sm:max-h-[calc(100vh-40px)]">
        <form onSubmit={submit} className="flex max-h-[calc(100vh-16px)] min-w-0 flex-col sm:max-h-[min(820px,calc(100vh-40px))]">
          <div className="grid gap-3 border-b border-[rgba(255,255,255,0.10)] px-4 py-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:px-5">
            <div className="min-w-0">
              <div className={labelClass}>{mode === 'edit' ? 'Flow settings' : 'New scheduled job'}</div>
              <h2 className="mt-1 break-words text-lg font-semibold text-[rgba(255,255,255,0.90)]">{mode === 'edit' ? 'Edit Flow' : 'Add Flow'}</h2>
              <p className="mt-1 break-words text-sm text-[rgba(255,255,255,0.55)]">{mode === 'edit' ? 'Updates the controller Flow and syncs the new assignment to the target.' : 'Creates a controller Flow and syncs it to the selected target.'}</p>
            </div>
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-3 sm:flex sm:items-start">
              <div className="min-w-0 rounded-xl border border-[#b87586]/20 bg-[#b87586]/10 px-3 py-2 text-left text-[11px] leading-4 text-[#d7a0ad] sm:whitespace-nowrap sm:text-right">
                Need complex cron? Ask your agent.
              </div>
              <ModalCloseButton className="rounded-xl" onClick={onClose} aria-label={mode === 'edit' ? 'Close Edit Flow' : 'Close Add Flow'} />
            </div>
          </div>

          <div className="min-w-0 overflow-y-auto px-4 py-4 sm:px-5">
            <div className="grid min-w-0 gap-x-5 gap-y-4 md:grid-cols-2">
              <label className="flex min-w-0 flex-col gap-2">
                <span className={labelClass}>Flow name</span>
                <Input data-testid="flows-add-name" value={form.name} onChange={update('name')} className={fieldClass} />
              </label>
              <label className="flex min-w-0 flex-col gap-2">
                <span className={labelClass}>Target swarm</span>
                <select data-testid="flows-add-target" value={form.targetKey} onChange={update('targetKey')} className={fieldClass} disabled={loadingOptions || !targetOptions.length}>
                  {groupedTargetOptions(targetOptions).map((group) => (
                    <optgroup key={group.key} label={group.label}>
                      {group.options.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                    </optgroup>
                  ))}
                </select>
                <span className={helperClass}>
                  {loadingOptions ? 'Loading linked swarms…' : selectedTarget?.helper || 'No linked swarm targets returned by the controller.'}
                </span>
              </label>
              <label className="flex min-w-0 flex-col gap-2">
                <span className={labelClass}>Workspace</span>
                <select data-testid="flows-add-workspace" value={form.workspacePath} onChange={update('workspacePath')} className={fieldClass} disabled={selectorsLoading || !scopedWorkspaceOptions.length}>
                  {scopedWorkspaceOptions.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                </select>
                <span className={helperClass}>
                  {targetWorkspacesLoading ? 'Loading target workspaces…' : targetWorkspacesError || selectedWorkspace?.helper || 'No workspace records returned by the selected target.'}
                </span>
              </label>
              <label className="flex min-w-0 flex-col gap-2">
                <span className={labelClass}>Agent</span>
                <select data-testid="flows-add-agent" value={form.agentKey} onChange={update('agentKey')} className={fieldClass} disabled={loadingOptions || !agentOptions.length}>
                  {groupedAgentOptions(agentOptions).map((group) => (
                    <optgroup key={group.label} label={group.label}>
                      {group.options.map((option) => <option key={option.key} value={option.key}>{option.label}</option>)}
                    </optgroup>
                  ))}
                </select>
                <span className={helperClass}>
                  {loadingOptions ? 'Loading saved agents…' : selectedAgent?.helper || 'No enabled saved agents returned by the controller.'}
                </span>
              </label>
              <section className="grid gap-3 rounded-[14px] border border-[rgba(255,255,255,0.10)] bg-transparent p-3 md:col-span-2" aria-label="Schedule">
                <div className="grid min-w-0 gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
                  <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
                    <span className={labelClass}>Schedule</span>
                    <span className="text-[rgba(255,255,255,0.38)]">·</span>
                    <span className="min-w-0 break-words text-sm font-medium text-[rgba(255,255,255,0.86)]">{schedulePreview}</span>
                  </div>

                  <div className="grid grid-cols-2 rounded-xl border border-[rgba(255,255,255,0.10)] bg-[rgba(255,255,255,0.025)] p-0.5 sm:flex sm:shrink-0">
                    {(['guided', 'cron'] as ScheduleMode[]).map((mode) => {
                      const selected = form.scheduleMode === mode
                      return (
                        <button
                          key={mode}
                          type="button"
                          onClick={() => setForm((current) => ({ ...current, scheduleMode: mode }))}
                          className={cn(
                            'h-7 rounded-lg px-2.5 text-xs font-medium transition focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[#b87586]/35',
                            selected
                              ? 'bg-[#b87586]/16 text-[rgba(255,255,255,0.90)]'
                              : 'text-[rgba(255,255,255,0.58)] hover:text-[rgba(255,255,255,0.78)]',
                          )}
                        >
                          {mode === 'guided' ? 'Guided' : 'Raw cron'}
                        </button>
                      )
                    })}
                  </div>
                </div>

                {form.scheduleMode === 'guided' ? (
                  <div className="grid gap-2.5">
                    <div className="grid gap-2.5 md:grid-cols-3">
                      <label className="flex min-w-0 flex-col gap-1.5">
                        <span className={labelClass}>Frequency</span>
                        <select data-testid="flows-add-cadence" value={form.scheduleCadence} onChange={update('scheduleCadence')} className={cn(fieldClass, 'h-8 rounded-lg text-xs')}>
                          {scheduleCadenceOptions.map((cadence) => <option key={cadence}>{cadence}</option>)}
                        </select>
                      </label>

                      <label className="flex min-w-0 flex-col gap-1.5">
                        <span className={labelClass}>{form.scheduleCadence === 'Daily' ? 'Daily mode' : form.scheduleCadence === 'Monthly' ? 'Run on day' : 'Run days'}</span>
                        {form.scheduleCadence === 'Daily' ? (
                          <select value={form.dailyMode} onChange={update('dailyMode')} className={cn(fieldClass, 'h-8 rounded-lg text-xs')}>
                            {dailyScheduleModeOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                          </select>
                        ) : form.scheduleCadence === 'Monthly' ? (
                          <select value={form.scheduleDate} onChange={update('scheduleDate')} className={cn(fieldClass, 'h-8 rounded-lg text-xs')}>
                            {scheduleDateOptions.map((date) => <option key={date}>{date}</option>)}
                          </select>
                        ) : (
                          <div className="flex min-h-8 items-center rounded-lg border border-[rgba(255,255,255,0.10)] bg-[rgba(255,255,255,0.025)] px-2 py-1">
                            <div className="flex min-w-0 flex-wrap gap-1">
                              {scheduleDayOptions.map((day) => {
                                const selected = selectedScheduleDayValues.includes(day)
                                return (
                                  <button
                                    key={day}
                                    type="button"
                                    onClick={() => toggleScheduleDay(day)}
                                    className={cn(
                                      'h-5 rounded-md px-1.5 text-[10px] font-medium transition focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[#b87586]/35',
                                      selected
                                        ? 'bg-[#b87586]/18 text-[rgba(255,255,255,0.86)]'
                                        : 'text-[rgba(255,255,255,0.52)] hover:text-[rgba(255,255,255,0.76)]',
                                    )}
                                  >
                                    {day.slice(0, 3)}
                                  </button>
                                )
                              })}
                            </div>
                          </div>
                        )}
                      </label>

                      <label className="flex min-w-0 flex-col gap-1.5">
                        <span className={labelClass}>{form.scheduleCadence === 'Daily' && form.dailyMode !== 'once' ? 'Run window' : 'Run time'}</span>
                        {form.scheduleCadence === 'Daily' && form.dailyMode === 'times_between' ? (
                          <div className="grid gap-1.5 sm:grid-cols-[minmax(0,0.75fr)_minmax(0,1fr)_minmax(0,1fr)]">
                            <input type="number" min="1" max={maxDailyScheduleTimes} value={form.dailyRunCount} onChange={update('dailyRunCount')} aria-label="Runs per day" className={cn(fieldClass, 'h-8 rounded-lg px-2 text-xs')} />
                            <select value={form.dailyWindowStart} onChange={update('dailyWindowStart')} aria-label="Start" className={cn(fieldClass, 'h-8 rounded-lg px-2 text-xs')}>
                              {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                            </select>
                            <select value={form.dailyWindowEnd} onChange={update('dailyWindowEnd')} aria-label="End" className={cn(fieldClass, 'h-8 rounded-lg px-2 text-xs')}>
                              {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                            </select>
                          </div>
                        ) : form.scheduleCadence === 'Daily' && form.dailyMode === 'interval_window' ? (
                          <div className="grid gap-1.5 sm:grid-cols-[minmax(0,0.75fr)_minmax(0,1fr)_minmax(0,1fr)]">
                            <div className="flex items-center gap-1.5">
                              <input type="number" min="1" max="24" value={form.dailyIntervalHours} onChange={update('dailyIntervalHours')} aria-label="Interval hours" className={cn(fieldClass, 'h-8 min-w-0 rounded-lg px-2 text-xs')} />
                              <span className="text-[11px] text-[rgba(255,255,255,0.55)]">h</span>
                            </div>
                            <select value={form.dailyWindowStart} onChange={update('dailyWindowStart')} aria-label="Start" className={cn(fieldClass, 'h-8 rounded-lg px-2 text-xs')}>
                              {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                            </select>
                            <select value={form.dailyWindowEnd} onChange={update('dailyWindowEnd')} aria-label="End" className={cn(fieldClass, 'h-8 rounded-lg px-2 text-xs')}>
                              {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                            </select>
                          </div>
                        ) : (
                          <select value={guidedScheduleTimes[0] || '12:00 AM'} onChange={updateScheduleTime(0)} className={cn(fieldClass, 'h-8 rounded-lg text-xs')}>
                            {scheduleTimeOptions.map((option) => <option key={option}>{option}</option>)}
                          </select>
                        )}
                      </label>
                    </div>

                    {form.scheduleCadence === 'Daily' ? (
                      <div className={helperClass}>{dailyScheduleModeOptions.find((option) => option.value === form.dailyMode)?.helper}</div>
                    ) : form.scheduleCadence === 'Weekly' ? (
                      <div className={helperClass}>Runs every {formatSelectedDays(form.scheduleDay)} at the selected time.</div>
                    ) : null}
                  </div>
                ) : (
                  <div className="grid gap-1.5">
                    <label className="flex flex-col gap-1.5">
                      <span className={labelClass}>Cron expression</span>
                      <input
                        value={form.cronExpression}
                        onChange={update('cronExpression')}
                        className={cn(fieldClass, 'h-8 rounded-lg text-xs')}
                        placeholder="0 0 * * *"
                      />
                    </label>
                    <div className={cn(helperClass, 'break-words')}>{schedulePreview}</div>
                  </div>
                )}

                {needsHighRunCountConfirmation ? (
                  <label className="flex items-start gap-2 rounded-lg border border-[#b87586]/30 bg-[#b87586]/10 p-2 text-xs text-[rgba(255,255,255,0.78)]">
                    <input
                      type="checkbox"
                      checked={form.highRunCountConfirmed}
                      onChange={(event) => setForm((current) => ({ ...current, highRunCountConfirmed: event.target.checked }))}
                      className="mt-0.5 h-3.5 w-3.5 accent-[#b87586]"
                    />
                    <span className="min-w-0 break-words">
                      This will run {guidedScheduleTimes.length} times per day. Yes, I really want to run this many.
                    </span>
                  </label>
                ) : null}

                <div className="flex flex-wrap items-center gap-1.5 text-xs text-[rgba(255,255,255,0.55)]">
                  <span className="font-medium text-[rgba(255,255,255,0.62)]">Preview:</span>
                  {(form.scheduleMode === 'cron' ? cronPreviewTimes : guidedScheduleTimes).map((time, index) => (
                    <span key={`${time}-${index}`} className="rounded-md border border-[rgba(255,255,255,0.10)] bg-[rgba(255,255,255,0.035)] px-2 py-0.5 text-[11px] text-[rgba(255,255,255,0.72)]">
                      {time}
                    </span>
                  ))}
                </div>

                <div className="flex flex-col gap-2 border-t border-[rgba(255,255,255,0.08)] pt-2.5 sm:flex-row sm:items-center">
                  <label className="flex flex-1 flex-col gap-1.5 sm:max-w-[320px]">
                    <span className={labelClass}>Timezone</span>
                    <select data-testid="flows-add-timezone" value={form.timezone} onChange={update('timezone')} className={cn(fieldClass, 'h-8 rounded-lg text-xs')}>
                      {timezoneOptions.map((timezone) => (
                        <option key={timezone} value={timezone}>{timezone}</option>
                      ))}
                    </select>
                  </label>
                  <span className="min-w-0 break-words text-xs text-[rgba(255,255,255,0.55)] sm:pt-5">Currently {selectedTimezoneNow}</span>
                </div>
              </section>
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Task</span>
                <textarea data-testid="flows-add-task" value={form.task} onChange={update('task')} rows={3} className={textareaClass} />
              </label>
            </div>
          </div>

          <div className="grid gap-3 border-t border-[rgba(255,255,255,0.10)] bg-[#1a1921] px-4 py-3 sm:px-5">
            {footerAccessory ? <div className="min-w-0">{footerAccessory}</div> : null}
            <div className="grid gap-2 sm:flex sm:items-center sm:justify-end">
              <Button variant="outline" className="w-full rounded-xl border-[rgba(255,255,255,0.13)] bg-transparent text-[rgba(255,255,255,0.70)] hover:border-[rgba(255,255,255,0.20)] hover:bg-[rgba(255,255,255,0.035)] sm:w-auto" onClick={onClose} disabled={busy}>Cancel</Button>
              <Button data-testid="flows-add-submit" type="submit" variant="primary" className="w-full rounded-xl border-[#b87586]/40 bg-[#a86678] text-white hover:bg-[#b87586] active:bg-[#96596a] sm:w-auto" disabled={!canSubmit}>{busy ? (mode === 'edit' ? 'Saving…' : 'Adding…') : (mode === 'edit' ? 'Save' : 'Add Flow')}</Button>
            </div>
          </div>
        </form>
      </DialogPanel>
    </Dialog>
  )
}

function FlowDetail({
  flow,
  onBack,
  onRunNow,
  onDelete,
  onToggleEnabled,
  onEdit,
  busy,
}: {
  flow: FlowDefinition
  onBack: () => void
  onRunNow: (id: string) => void
  onDelete: (id: string) => void
  onToggleEnabled: (flow: FlowDefinition) => void
  onEdit: (flow: FlowDefinition) => void
  busy?: boolean
}) {
  return (
    <div data-testid="flows-detail" className="flex min-h-full flex-col gap-8 pb-10 text-[var(--app-text)]">
      <div className="grid gap-4 border-b border-[var(--app-border)] pb-5 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
        <div className="min-w-0">
          <button type="button" onClick={onBack} className="mb-4 inline-flex items-center gap-2 text-sm text-[var(--app-text-muted)] hover:text-[var(--app-text)]">
            <ArrowLeft size={16} /> Back to flows
          </button>
          <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
            <FlowStatusDot status={flow.status} /> <span className="min-w-0 break-words">{statusLabels[flow.status]} / {flow.mode}</span>
          </div>
          <h1 className="mt-2 break-words text-2xl font-semibold tracking-tight text-[var(--app-text)]">{flow.name}</h1>
          <p className="mt-2 max-w-3xl break-words text-sm leading-6 text-[var(--app-text-muted)]">{flow.task}</p>
        </div>
        <div className="grid grid-cols-2 gap-2 md:flex md:shrink-0 md:items-center">
          <Button data-testid="flows-detail-edit" variant="outline" className="w-full rounded-xl md:w-auto" onClick={() => onEdit(flow)} disabled={busy}>
            Edit
          </Button>
          <Button data-testid="flows-detail-run-now" variant="outline" className="w-full rounded-xl md:w-auto" onClick={() => onRunNow(flow.id)} disabled={busy}>
            Run once
          </Button>
          <Button variant="outline" className="w-full rounded-xl md:w-auto" onClick={() => onToggleEnabled(flow)} disabled={busy}>
            {flow.enabled ? 'Stop' : 'Start'}
          </Button>
          <Button variant="ghost" className="w-full rounded-xl text-[var(--app-danger)] md:w-auto" onClick={() => onDelete(flow.id)} disabled={busy}>
            Delete
          </Button>
        </div>
      </div>

      {flow.targetStale ? (
        <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning)]">
          This flow points at a target that no longer resolves. You can delete it safely from this page. {flow.targetStaleReason}
        </div>
      ) : null}

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
          <div key={label} className="min-w-0">
            <div className={labelClass}>{label}</div>
            <div className="mt-2 break-words text-sm text-[var(--app-text)]">{value}</div>
          </div>
        ))}
      </section>

      <section className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div>
          <div className="mb-4 flex items-center justify-between gap-3">
            <div>
              <h2 className="text-base font-semibold text-[var(--app-text)]">Tasks inside this flow</h2>
            </div>
          </div>
          <div className="overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]">
            {flow.tasks.map((task, index) => (
              <div key={task.id} className="grid min-w-0 gap-3 border-b border-[var(--app-border)] px-4 py-4 last:border-b-0 sm:grid-cols-[36px_minmax(0,1fr)] md:grid-cols-[36px_120px_minmax(0,1fr)] md:gap-4">
                <div className="font-mono text-xs text-[var(--app-text-muted)]">{String(index + 1).padStart(2, '0')}</div>
                <div className="min-w-0 text-xs uppercase tracking-[0.16em] text-[var(--app-text-muted)] sm:col-span-1 md:col-span-1">{task.action}</div>
                <div className="min-w-0 sm:col-span-2 md:col-span-1">
                  <div className="break-words text-sm font-medium text-[var(--app-text)]">{task.title}</div>
                  <div className="mt-1 break-words text-sm leading-6 text-[var(--app-text-muted)]">{task.detail}</div>
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
                <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
                  <span className="break-words font-mono text-xs text-[var(--app-text-muted)]">{run.startedAt}</span>
                  <span className={cn('text-xs capitalize', runStatusClasses[run.status])}>{run.status}</span>
                </div>
                <p className="mt-2 break-words text-xs leading-5 text-[var(--app-text-muted)]">{run.summary}</p>
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
  const agentStateQuery = useQuery(agentStateQueryOptions())
  const [selectedFlowRecord, setSelectedFlowRecord] = useState<FlowDetailRecord | null>(null)
  const flows = useMemo(() => (flowsQuery.data ?? []).map(recordToFlow), [flowsQuery.data])
  const [workspaceFilter, setWorkspaceFilter] = useState('all')
  const [agentFilter, setAgentFilter] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')
  const [query, setQuery] = useState('')
  const [selectedFlowID, setSelectedFlowIDState] = useState<string | null>(routeFlowID || null)
  const [addOpen, setAddOpen] = useState(false)
  const [editingFlowRecord, setEditingFlowRecord] = useState<FlowDetailRecord | null>(null)
  const [busyID, setBusyID] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const targetOptions = useMemo<FlowTargetOption[]>(() => {
    const seen = new Set<string>()
    return (swarmTargetsQuery.data ?? [])
      .filter((target) => target.selectable || target.current)
      .map((target) => ({
        key: targetOptionKey(target),
        label: targetOptionLabel(target),
        helper: targetOptionHelper(target),
        groupLabel: targetOptionGroupLabel(target, swarmTargetsQuery.data ?? []),
        target,
      }))
      .filter((option) => {
        if (!option.key || seen.has(option.key)) {
          return false
        }
        seen.add(option.key)
        return true
      })
  }, [swarmTargetsQuery.data])
  const addWorkspaceOptions = useMemo<FlowWorkspaceOption[]>(() => [], [])
  const savedAgentOptions = useMemo<FlowAgentOption[]>(() => {
    const seen = new Set<string>()
    return (agentStateQuery.data?.profiles ?? [])
      .filter((profile) => profile.enabled && profile.name.trim() !== '')
      .map((profile) => ({
        key: agentOptionKey(profile),
        label: agentOptionLabel(profile),
        helper: agentOptionHelper(profile),
        contractSummary: agentContractSummary(profile),
        groupLabel: agentOptionGroupLabel(profile),
        profile,
      }))
      .filter((option) => {
        if (!option.key || seen.has(option.key)) {
          return false
        }
        seen.add(option.key)
        return true
      })
      .sort((left, right) => agentOptionGroupRank(left.profile) - agentOptionGroupRank(right.profile) || left.label.localeCompare(right.label))
  }, [agentStateQuery.data?.profiles])
  const loadingAddFlowOptions = swarmTargetsQuery.isLoading || agentStateQuery.isLoading
  const loadFlowWorkspacesForTarget = useCallback((target: FlowSwarmTarget, signal?: AbortSignal) => {
    const targetKey = targetOptionKey(target)
    return queryClient.fetchQuery({
      queryKey: flowWorkspacesForTargetQueryKey(targetKey),
      queryFn: () => fetchFlowWorkspaces(target, signal),
    })
  }, [queryClient])

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

  const editFlow = async (input: CreateFlowInput) => {
    if (!editingFlowRecord) {
      return
    }
    const flowID = editingFlowRecord.definition.flow_id
    setSaving(true)
    setError(null)
    try {
      const detail = await updateFlow(flowID, input)
      setEditingFlowRecord(null)
      setSelectedFlowRecord(detail)
      setSelectedFlowID(detail.definition.flow_id)
      await refreshFlows()
      const refreshed = await fetchFlow(detail.definition.flow_id)
      setSelectedFlowRecord(refreshed)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update flow')
    } finally {
      setSaving(false)
    }
  }

  const openEditFlow = async (flow: FlowDefinition) => {
    setError(null)
    try {
      const detail = selectedFlowRecord?.definition.flow_id === flow.id ? selectedFlowRecord : await fetchFlow(flow.id)
      setEditingFlowRecord(detail)
      setSelectedFlowRecord(detail)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load flow settings')
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

  const handleToggleEnabled = async (flow: FlowDefinition) => {
    if (busyID) {
      return
    }
    setBusyID(flow.id)
    setError(null)
    try {
      const detail = await setFlowEnabled(flow.id, !flow.enabled)
      setSelectedFlowRecord((current) => (current?.definition.flow_id === flow.id ? detail : current))
      await refreshFlows()
      if (selectedFlowID === flow.id) {
        setSelectedFlowRecord(await fetchFlow(flow.id))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to update flow')
    } finally {
      setBusyID(null)
    }
  }

  const handleDelete = async (id: string) => {
    setBusyID(id)
    setError(null)
    try {
      await deleteFlow(id)
      setEditingFlowRecord(null)
      setSelectedFlowRecord(null)
      setSelectedFlowID(null)
      await refreshFlows()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to delete flow')
    } finally {
      setBusyID(null)
    }
  }

  const editInitialForm = useMemo(
    () => editingFlowRecord ? recordToFlowForm(editingFlowRecord, targetOptions, addWorkspaceOptions, savedAgentOptions) : null,
    [addWorkspaceOptions, editingFlowRecord, savedAgentOptions, targetOptions],
  )

  if (selectedFlow) {
    return (
      <>
        {error ? <div data-testid="flows-error" className="mb-4 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
        <FlowDetail flow={selectedFlow} onBack={() => { setSelectedFlowRecord(null); setSelectedFlowID(null); setEditingFlowRecord(null) }} onRunNow={handleRunNow} onDelete={handleDelete} onToggleEnabled={handleToggleEnabled} onEdit={openEditFlow} busy={busyID === selectedFlow.id} />
        <FlowSettingsModal
          open={Boolean(editingFlowRecord)}
          mode="edit"
          initialForm={editInitialForm}
          enabledOverride={editingFlowRecord?.definition.enabled}
          onClose={() => setEditingFlowRecord(null)}
          onConfirm={(input) => void editFlow(input)}
          busy={saving}
          targetOptions={targetOptions}
          workspaceOptions={addWorkspaceOptions}
          agentOptions={savedAgentOptions}
          loadingOptions={loadingAddFlowOptions}
          loadWorkspacesForTarget={loadFlowWorkspacesForTarget}
        />
      </>
    )
  }

  return (
    <div data-testid="flows-settings-page" className="flex min-h-full min-w-0 flex-col gap-5 pb-10 text-[var(--app-text)]">
      <header className="grid gap-4 border-b border-[var(--app-border)] pb-5 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
        <div className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
            <Workflow size={14} className="shrink-0" /> <span className="min-w-0 break-words">Workspace / Flows</span>
          </div>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight text-[var(--app-text)]">Flows</h1>
          <p className="mt-2 max-w-2xl break-words text-sm leading-6 text-[var(--app-text-muted)]">Triage scheduled and background agent jobs from real controller data.</p>
        </div>
        <div className="grid grid-cols-2 gap-2 md:flex md:items-center md:justify-end">
          <Button variant="outline" className="w-full rounded-xl md:w-auto" onClick={handleBackToChat}>
            <ArrowLeft size={15} /> Back
          </Button>
          <Button data-testid="flows-add-open" variant="outline" className="w-full rounded-xl md:w-auto" onClick={() => setAddOpen(true)}>
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
      {swarmTargetsQuery.isError || agentStateQuery.isError ? (
        <div className="rounded-xl border border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] px-3 py-2 text-sm text-[var(--app-warning)]">
          Add Flow selectors could not load real controller targets, workspaces, or saved agents. Refresh after the controller endpoints recover.
        </div>
      ) : null}

      <section className={cn(surfaceClass, 'grid grid-cols-2 gap-3 px-3 py-3 sm:grid-cols-3 lg:grid-cols-6 lg:px-4')}>
        {metaHeaderItems.map((item) => {
          const toneClass = item.tone === 'primary' ? 'text-[var(--app-primary)]' : statusTextClasses[item.tone]
          return (
            <div key={item.label} className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 lg:flex lg:items-center lg:gap-3 lg:border-0 lg:bg-transparent lg:px-0 lg:py-0 lg:pr-4 lg:[&:not(:last-child)]:border-r">
              <FlowStatusDot status={item.tone === 'primary' ? 'active' : item.tone} className={cn('h-1.5 w-1.5', item.tone === 'primary' ? 'bg-[var(--app-primary)]' : '')} />
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <span className={cn('break-words font-mono text-sm font-semibold', toneClass)}>{item.value}</span>
                  <span className="min-w-0 break-words text-xs font-medium text-[var(--app-text)]">{item.label}</span>
                </div>
                <div className="mt-0.5 min-w-0 break-words text-[11px] text-[var(--app-text-muted)]">{item.helper}</div>
              </div>
            </div>
          )
        })}
      </section>

      <section className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_360px]">
        <div className={cn(surfaceClass, 'min-w-0 p-4 sm:p-5')}>
          <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
            <div className="min-w-0">
              <h2 className="text-base font-semibold text-[var(--app-text)]">Schedule</h2>
            </div>
            <div className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-3 md:flex md:flex-wrap md:items-center md:justify-end">
              <FilterSelect label="Workspace filter" value={workspaceFilter} onChange={setWorkspaceFilter} options={workspaceOptions} />
              <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentFilterOptions} />
              <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
            </div>
          </div>

          <div className="mt-5 border-y border-[var(--app-border)]">
            <div className="hidden grid-cols-[88px_140px_minmax(0,1fr)_120px] gap-3 border-b border-[var(--app-border)] px-0 py-2 text-[11px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)] md:grid">
              <div>Time</div>
              <div>Next</div>
              <div>Flow</div>
              <div className="text-right">Status</div>
            </div>
            <div className="divide-y divide-[var(--app-border)]">
              {scheduleItems.length ? scheduleItems.map((event) => (
                <button key={event.flow.id} type="button" onClick={() => setSelectedFlowID(event.flow.id)} className="grid w-full min-w-0 gap-2 py-4 text-left transition hover:bg-[var(--app-surface-subtle)] md:grid-cols-[88px_140px_minmax(0,1fr)_120px] md:items-center md:gap-3">
                  <span className="font-mono text-sm text-[var(--app-text)]">{event.time}</span>
                  <span className="min-w-0 break-words text-xs text-[var(--app-text-muted)]">{event.day}</span>
                  <span className="min-w-0">
                    <span className="block break-words text-sm font-medium text-[var(--app-text)] md:truncate">{event.flow.name}</span>
                    <span className="mt-1 block break-words text-xs text-[var(--app-text-muted)] md:truncate">{event.flow.workspace} / {event.flow.agent} / {event.meta}</span>
                  </span>
                  <span className="justify-self-start md:justify-self-end"><StatusOutlineToken status={event.flow.status} /></span>
                </button>
              )) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No scheduled flows yet.</div>}
            </div>
          </div>
        </div>

        <aside className={cn(surfaceClass, 'flex min-w-0 flex-col p-4 sm:p-5')}>
          <h2 className="text-base font-semibold text-[var(--app-text)]">Needs attention</h2>
          <div className="mt-4 flex-1 divide-y divide-[var(--app-border)] overflow-hidden border-y border-[var(--app-border)]">
            {attentionItems.length ? attentionItems.map((item) => (
              <button key={item.flow.id} type="button" onClick={() => setSelectedFlowID(item.flow.id)} className="grid w-full min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-3 px-3 py-3 text-left transition hover:bg-[var(--app-surface-subtle)] sm:grid-cols-[auto_minmax(0,1fr)_auto]">
                <FlowStatusDot status={item.dotStatus} className="mt-1" />
                <span className="min-w-0">
                  <span className="block break-words text-sm font-medium text-[var(--app-text)]">{item.flow.name}</span>
                  <span className="mt-1 block break-words text-xs text-[var(--app-text-muted)]">{item.meta}</span>
                </span>
                <span className="col-span-2 justify-self-start sm:col-span-1 sm:justify-self-auto"><StatusOutlineToken status={item.flow.status} /></span>
              </button>
            )) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No flows need attention.</div>}
          </div>
          <div className="mt-4 text-xs text-[var(--app-text-muted)]">{reviewCount} needs review • {pausedCount} paused • {draftCount} draft</div>
        </aside>
      </section>

      <section className={cn(surfaceClass, 'min-w-0 overflow-hidden')}>
        <div className="grid gap-4 border-b border-[var(--app-border)] p-4 sm:p-5 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-start">
          <div className="min-w-0">
            <h2 className="text-base font-semibold text-[var(--app-text)]">Flow controls</h2>
            <p className="mt-1 break-words text-sm text-[var(--app-text-muted)]">Run and delete controller-backed flows.</p>
          </div>
          <div className="grid min-w-0 gap-2 sm:grid-cols-2 lg:flex lg:flex-wrap lg:items-center lg:justify-end">
            <label className="relative min-w-0 sm:col-span-2 lg:w-[148px] lg:shrink-0">
              <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
              <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search flows" className="!h-9 !min-h-9 w-full min-w-0 rounded-xl border-[var(--app-border)] bg-[var(--app-surface-subtle)] !py-0 pl-8 pr-3 text-xs leading-none focus-visible:ring-0 focus-visible:ring-offset-0" />
            </label>
            <FilterSelect label="Workspace filter" value={workspaceFilter} onChange={setWorkspaceFilter} options={workspaceOptions} />
            <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentFilterOptions} />
            <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
          </div>
        </div>

        <div className="hidden overflow-x-auto md:block">
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
                    <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="max-w-[520px] min-w-0 text-left">
                      <div className="truncate text-sm font-medium text-[var(--app-text)]">{flow.name}</div>
                      <div className="mt-1 line-clamp-2 break-words text-xs leading-5 text-[var(--app-text-muted)]">{flow.task}</div>
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
                      <EnabledToggle enabled={flow.enabled} disabled={busyID === flow.id} onToggle={() => { void handleToggleEnabled(flow) }} />
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
        <div data-testid="flows-cards" className="divide-y divide-[var(--app-border)] md:hidden">
          {filteredFlows.length ? filteredFlows.map((flow) => (
            <article key={flow.id} data-testid="flows-card" data-flow-id={flow.id} className="min-w-0 px-4 py-4">
              <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="block w-full min-w-0 text-left">
                <div className="flex min-w-0 items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <h3 className="break-words text-sm font-medium text-[var(--app-text)]">{flow.name}</h3>
                    <p className="mt-1 line-clamp-3 break-words text-xs leading-5 text-[var(--app-text-muted)]">{flow.task}</p>
                  </div>
                  <StatusOutlineToken status={flow.status} />
                </div>
                <div className="mt-3 grid min-w-0 grid-cols-2 gap-2 text-xs">
                  <div className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
                    <div className={labelClass}>Last</div>
                    <FlowDateTime value={flow.lastRun} />
                  </div>
                  <div className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
                    <div className={labelClass}>Next</div>
                    <FlowDateTime value={flow.nextRun} meta={flow.nextRunMeta} />
                  </div>
                  <div className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
                    <div className={labelClass}>Runs</div>
                    <div className="mt-1 font-mono text-sm text-[var(--app-text)]">{flow.totalRuns}</div>
                  </div>
                  <div className="min-w-0 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
                    <div className={labelClass}>Manage</div>
                    <div className="mt-1 text-sm text-[var(--app-text)]">Open details</div>
                  </div>
                </div>
                <div className="mt-3 grid min-w-0 gap-1.5 text-[11px] text-[var(--app-text-subtle)]">
                  <span className="inline-flex min-w-0 items-start gap-1.5"><MapPin size={12} className="mt-0.5 shrink-0" /> <span className="min-w-0 break-words">{flow.workspace} / {flow.target}</span></span>
                  <span className="inline-flex min-w-0 items-start gap-1.5"><Bot size={12} className="mt-0.5 shrink-0" /> <span className="min-w-0 break-words">{flow.agent}</span></span>
                  <span className="inline-flex min-w-0 items-start gap-1.5"><Clock size={12} className="mt-0.5 shrink-0" /> <span className="min-w-0 break-words">{flow.schedule}</span></span>
                </div>
              </button>
              <div className="mt-3 flex items-center justify-between gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2">
                <span className={labelClass}>Enabled</span>
                <EnabledToggle enabled={flow.enabled} disabled={busyID === flow.id} onToggle={() => { void handleToggleEnabled(flow) }} />
              </div>
            </article>
          )) : (
            <div className="px-4 py-8 text-center text-sm text-[var(--app-text-muted)]">No flows found.</div>
          )}
        </div>
      </section>

      <FlowSettingsModal
        open={addOpen}
        mode="create"
        onClose={() => setAddOpen(false)}
        onConfirm={(input) => void addFlow(input)}
        busy={saving}
        targetOptions={targetOptions}
        workspaceOptions={addWorkspaceOptions}
        agentOptions={savedAgentOptions}
        loadingOptions={loadingAddFlowOptions}
        loadWorkspacesForTarget={loadFlowWorkspacesForTarget}
      />
    </div>
  )
}
