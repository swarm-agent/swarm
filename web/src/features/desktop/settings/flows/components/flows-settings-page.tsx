import { useMemo, useState, type ChangeEvent, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, Bot, ChevronDown, Clock, MapPin, MoreHorizontal, Plus, Search, Workflow } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../../components/ui/dialog'
import { Input } from '../../../../../components/ui/input'
import { ModalCloseButton } from '../../../../../components/ui/modal-close-button'
import { cn } from '../../../../../lib/cn'
import {
  createFlow,
  deleteFlow,
  fetchFlows,
  flowsQueryKey,
  runFlowNow,
  type CreateFlowInput,
  type FlowSummaryRecord,
  type FlowTaskStep,
} from '../api'

type FlowStatus = 'active' | 'paused' | 'draft' | 'needs_review' | 'failed'
type FlowMode = 'One-shot background job' | 'Scheduled background job' | 'Manual one-shot'
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
  agentType: string
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
  raw: FlowSummaryRecord
}

interface AddFlowForm {
  name: string
  agent: string
  target: string
  scheduleCadence: ScheduleCadence
  scheduleTime: string
  scheduleDay: string
  scheduleDate: string
  workspace: string
  location: string
  context: string
  mode: FlowMode
  task: string
}

const formWorkspaceOptions = ['.', 'swarm-go', 'desktop-ui', 'infra']
const formLocationOptions = ['current workspace on laptop', 'release workspace', 'docs workspace', 'ops workspace']
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

const defaultAddFlowForm: AddFlowForm = {
  name: 'Nightly AGENTS.md memory refresh',
  agent: 'memory',
  target: 'local',
  scheduleCadence: 'Daily',
  scheduleTime: '12:00 AM',
  scheduleDay: 'Mon',
  scheduleDate: '1',
  workspace: '.',
  location: 'current workspace on laptop',
  context: 'plan → approval → execute',
  mode: 'Scheduled background job',
  task: 'Read git diffs and update AGENTS.md to be more accurate.',
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

const statusBadgeClasses: Record<FlowStatus, string> = {
  active: 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]',
  paused: 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
  draft: 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
  needs_review: 'border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning)]',
  failed: 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]',
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
    return 'On demand'
  }
  if (form.scheduleCadence === 'Weekly') {
    return `Weekly on ${form.scheduleDay} ${form.scheduleTime}`
  }
  if (form.scheduleCadence === 'Monthly') {
    return `Monthly on day ${form.scheduleDate} ${form.scheduleTime}`
  }
  return `Daily at ${form.scheduleTime}`
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
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }).format(date)
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
  const schedule = record.definition.assignment.schedule
  const cadence = cadenceLabel(schedule.cadence)
  if (cadence === 'On demand') {
    return 'On demand'
  }
  const time = hhmmToDisplay(schedule.time ?? '')
  if (cadence === 'Weekly') {
    return `Weekly on ${schedule.weekday || 'Mon'} ${time}`
  }
  if (cadence === 'Monthly') {
    return `Monthly on day ${schedule.month_day || 1} ${time}`
  }
  return `Daily at ${time}`
}

function statusFromRecord(record: FlowSummaryRecord): FlowStatus {
  if (record.last_run?.status === 'failed') {
    return 'failed'
  }
  if (record.last_run?.status === 'review') {
    return 'needs_review'
  }
  if (!record.definition.assignment.enabled) {
    return record.history_count > 0 ? 'paused' : 'draft'
  }
  const statuses = record.assignment_statuses ?? []
  if (statuses.some((status) => status.pending_sync || status.status === 'target_offline' || status.status === 'target_unusable')) {
    return 'needs_review'
  }
  return 'active'
}

function modeFromRecord(record: FlowSummaryRecord): FlowMode {
  const cadence = cadenceLabel(record.definition.assignment.schedule.cadence)
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

function recordToFlow(record: FlowSummaryRecord): FlowDefinition {
  const assignment = record.definition.assignment
  const history = record.last_run ? [record.last_run] : []
  const runs = history.map((run): FlowRun => ({
    id: run.run_id,
    startedAt: isoDisplay(run.started_at || run.scheduled_at),
    duration: durationLabel(run.duration_ms),
    status: normalizeRunStatus(run.status),
    summary: run.summary || run.status,
  }))
  const workspace = assignment.workspace.workspace_path?.trim() || 'workspace'
  const target = assignment.target.name?.trim() || assignment.target.swarm_id?.trim() || assignment.target.kind?.trim() || 'local'
  const agent = assignment.agent.target_name?.trim() || 'memory'
  const agentType = assignment.agent.target_kind?.trim() || 'background'
  const tasks = assignment.intent.tasks?.length
    ? assignment.intent.tasks.map(normalizeTask)
    : [{ id: `${assignment.flow_id}-prompt`, title: 'Run prompt', detail: assignment.intent.prompt || 'Run configured prompt.', action: 'propose' as const }]
  return {
    id: assignment.flow_id,
    name: assignment.name || assignment.flow_id,
    workspace,
    location: assignment.workspace.cwd?.trim() || workspace,
    target,
    agent,
    agentType,
    schedule: scheduleLabelFromRecord(record),
    startTime: assignment.schedule.time ? hhmmToDisplay(assignment.schedule.time) : 'Manual',
    lastRun: record.last_run ? isoDisplay(record.last_run.started_at || record.last_run.scheduled_at) : 'Never',
    lastRunMeta: record.history_count ? `${record.history_count} runs` : '',
    nextRun: record.definition.next_due_at ? isoDisplay(record.definition.next_due_at) : '—',
    nextRunMeta: record.assignment_statuses?.some((status) => status.pending_sync) ? 'pending sync' : '',
    totalRuns: record.history_count,
    status: statusFromRecord(record),
    enabled: assignment.enabled,
    mode: modeFromRecord(record),
    context: assignment.intent.mode || assignment.catch_up_policy.mode || 'target-owned schedule',
    task: assignment.intent.prompt || tasks.map((task) => task.title).join(', '),
    tasks,
    runs,
    raw: record,
  }
}

function formToCreateInput(form: AddFlowForm): CreateFlowInput {
  const cadence = form.scheduleCadence === 'On demand' ? 'on_demand' : form.scheduleCadence.toLowerCase()
  const targetName = form.target.trim()
  const isLocalTarget = targetName.toLowerCase() === 'local'
  const workspacePath = form.workspace.trim() || '.'
  return {
    name: form.name.trim() || 'Untitled flow',
    enabled: form.scheduleCadence !== 'On demand',
    target: {
      kind: isLocalTarget ? 'self' : undefined,
      name: isLocalTarget ? undefined : targetName || undefined,
    },
    agent: {
      target_kind: 'background',
      target_name: form.agent.trim() || 'memory',
    },
    workspace: {
      workspace_path: workspacePath,
    },
    schedule: {
      cadence,
      time: cadence === 'on_demand' ? undefined : clockLabelToHHMM(form.scheduleTime),
      weekday: cadence === 'weekly' ? form.scheduleDay : undefined,
      month_day: cadence === 'monthly' ? Number(form.scheduleDate) : undefined,
      timezone: 'UTC',
    },
    catch_up_policy: {
      mode: 'once',
    },
    intent: {
      prompt: form.task.trim() || 'Run the configured task prompt.',
      mode: form.context.trim(),
      tasks: [
        { id: 'context', title: 'Prepare run context', detail: `Target ${form.target || 'local'} in ${form.location || 'the selected workspace'}.`, action: 'read' },
        { id: 'task', title: 'Run agent task', detail: form.task.trim() || 'Run the configured task prompt.', action: 'propose' },
      ],
    },
  }
}

function FlowStatusDot({ status, className }: { status: FlowStatus; className?: string }) {
  return <span className={cn('inline-block h-2 w-2 shrink-0 rounded-full', statusDotClasses[status], className)} />
}

function StatusBadge({ status }: { status: FlowStatus }) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-[11px] font-medium', statusBadgeClasses[status])}>
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

function AddFlowModal({ open, onClose, onAdd, busy }: { open: boolean; onClose: () => void; onAdd: (input: CreateFlowInput) => void; busy?: boolean }) {
  const [form, setForm] = useState<AddFlowForm>(defaultAddFlowForm)

  if (!open) {
    return null
  }

  const update = (field: keyof AddFlowForm) => (event: ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
    setForm((current) => ({ ...current, [field]: event.target.value }))
  }

  const schedulePreview = buildScheduleLabel(form)

  const submit = (event: FormEvent) => {
    event.preventDefault()
    onAdd(formToCreateInput(form))
    setForm(defaultAddFlowForm)
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
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Agent/profile</span>
                <select data-testid="flows-add-agent" value={form.agent} onChange={update('agent')} className={fieldClass}>
                  <option value="memory">memory</option>
                  <option value="swarm">swarm</option>
                  <option value="explorer">explorer</option>
                  <option value="parallel">parallel</option>
                </select>
              </label>
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Target</span>
                <select data-testid="flows-add-target" value={form.target} onChange={update('target')} className={fieldClass}>
                  <option>local</option>
                  <option>laptop</option>
                  <option>container</option>
                </select>
              </label>
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Workspace</span>
                <select data-testid="flows-add-workspace" value={form.workspace} onChange={update('workspace')} className={fieldClass}>
                  {formWorkspaceOptions.map((workspace) => <option key={workspace}>{workspace}</option>)}
                </select>
              </label>
              <label className="flex flex-col gap-2">
                <span className={labelClass}>Location</span>
                <select value={form.location} onChange={update('location')} className={fieldClass}>
                  {formLocationOptions.map((location) => <option key={location}>{location}</option>)}
                </select>
              </label>
              <div className="grid gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 md:col-span-2 md:grid-cols-[1fr_1fr_1fr]">
                <div className="md:col-span-3">
                  <div className={labelClass}>Schedule</div>
                  <div className="mt-1 text-xs text-[var(--app-text-muted)]">{schedulePreview}</div>
                </div>
                <label className="flex flex-col gap-2">
                  <span className="text-xs text-[var(--app-text-muted)]">Cadence</span>
                  <select data-testid="flows-add-cadence" value={form.scheduleCadence} onChange={update('scheduleCadence')} className={fieldClass}>
                    {scheduleCadenceOptions.map((cadence) => <option key={cadence}>{cadence}</option>)}
                  </select>
                </label>
                {form.scheduleCadence === 'Weekly' ? (
                  <label className="flex flex-col gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">Day</span>
                    <select value={form.scheduleDay} onChange={update('scheduleDay')} className={fieldClass}>
                      {scheduleDayOptions.map((day) => <option key={day}>{day}</option>)}
                    </select>
                  </label>
                ) : null}
                {form.scheduleCadence === 'Monthly' ? (
                  <label className="flex flex-col gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">Day of month</span>
                    <select value={form.scheduleDate} onChange={update('scheduleDate')} className={fieldClass}>
                      {scheduleDateOptions.map((date) => <option key={date}>{date}</option>)}
                    </select>
                  </label>
                ) : null}
                {form.scheduleCadence !== 'On demand' ? (
                  <label className="flex flex-col gap-2">
                    <span className="text-xs text-[var(--app-text-muted)]">Time</span>
                    <select value={form.scheduleTime} onChange={update('scheduleTime')} className={fieldClass}>
                      {scheduleTimeOptions.map((time) => <option key={time}>{time}</option>)}
                    </select>
                  </label>
                ) : null}
              </div>
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Execution context</span>
                <Input value={form.context} onChange={update('context')} className={fieldClass} />
              </label>
              <label className="flex flex-col gap-2 md:col-span-2">
                <span className={labelClass}>Launch mode</span>
                <select value={form.mode} onChange={update('mode')} className={fieldClass}>
                  <option>Scheduled background job</option>
                  <option>One-shot background job</option>
                  <option>Manual one-shot</option>
                </select>
              </label>
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
              <Button data-testid="flows-add-submit" type="submit" variant="primary" className="rounded-xl" disabled={busy}>{busy ? 'Adding…' : 'Add Flow'}</Button>
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
          ['Agent', `${flow.agentType} / ${flow.agent}`],
          ['Schedule', flow.schedule],
          ['Workspace', flow.workspace],
          ['Location', flow.location],
          ['Context', flow.context],
        ].map(([label, value]) => (
          <div key={label}>
            <div className={labelClass}>{label}</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{value}</div>
          </div>
        ))}
      </section>

      <section className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div>
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
  const flowsQuery = useQuery({ queryKey: flowsQueryKey, queryFn: ({ signal }) => fetchFlows(signal) })
  const flows = useMemo(() => (flowsQuery.data ?? []).map(recordToFlow), [flowsQuery.data])
  const [workspaceFilter, setWorkspaceFilter] = useState('all')
  const [agentFilter, setAgentFilter] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')
  const [query, setQuery] = useState('')
  const [selectedFlowID, setSelectedFlowID] = useState<string | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [busyID, setBusyID] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const workspaces = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.workspace)))], [flows])
  const agents = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.agentType)))], [flows])
  const statuses = useMemo(() => ['all', ...Array.from(new Set(flows.map((flow) => flow.status)))], [flows])

  const workspaceOptions = workspaces.map((workspace) => ({ value: workspace, label: workspace === 'all' ? 'All workspaces' : workspace }))
  const agentOptions = agents.map((agent) => ({ value: agent, label: agent === 'all' ? 'All agents' : agent }))
  const statusOptions = statuses.map((status) => ({ value: status, label: status === 'all' ? 'All statuses' : statusLabels[status as FlowStatus] }))

  const filteredFlows = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase()
    return flows.filter((flow) => {
      const workspaceMatch = workspaceFilter === 'all' || flow.workspace === workspaceFilter
      const agentMatch = agentFilter === 'all' || flow.agentType === agentFilter
      const statusMatch = statusFilter === 'all' || flow.status === statusFilter
      const queryMatch = !normalizedQuery || [flow.name, flow.agent, flow.agentType, flow.workspace, flow.target, flow.task, flow.schedule].some((value) => value.toLowerCase().includes(normalizedQuery))
      return workspaceMatch && agentMatch && statusMatch && queryMatch
    })
  }, [agentFilter, flows, query, statusFilter, workspaceFilter])

  const selectedFlow = selectedFlowID ? flows.find((flow) => flow.id === selectedFlowID) ?? null : null
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
      setSelectedFlowID(detail.definition.flow_id)
      await refreshFlows()
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
        <FlowDetail flow={selectedFlow} onBack={() => setSelectedFlowID(null)} onRunNow={handleRunNow} onDelete={handleDelete} busy={busyID === selectedFlow.id} />
      </>
    )
  }

  return (
    <div data-testid="flows-settings-page" className="flex min-h-full flex-col gap-5 pb-10 text-[var(--app-text)]">
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--app-border)] pb-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--app-text-muted)]">
            <Workflow size={14} /> Settings / Flows
          </div>
          <h1 className="mt-2 text-3xl font-semibold tracking-tight text-[var(--app-text)]">Flows</h1>
          <p className="mt-2 max-w-2xl text-sm leading-6 text-[var(--app-text-muted)]">Control scheduled and background agent jobs from real controller data.</p>
        </div>
        <Button data-testid="flows-add-open" variant="primary" className="rounded-xl" onClick={() => setAddOpen(true)}>
          <Plus size={16} /> Add Flow
        </Button>
      </header>

      {error ? (
        <div data-testid="flows-error" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div>
      ) : null}
      {flowsQuery.isLoading ? (
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-sm text-[var(--app-text-muted)]">Loading flows…</div>
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
              <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentOptions} />
              <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
            </div>
          </div>

          <div className="mt-5 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-inset)]">
            <div className="grid grid-cols-[88px_140px_minmax(0,1fr)_120px] gap-3 border-b border-[var(--app-border)] px-4 py-2 text-[11px] uppercase tracking-[0.14em] text-[var(--app-text-subtle)]">
              <div>Time</div>
              <div>Next</div>
              <div>Flow</div>
              <div className="text-right">Status</div>
            </div>
            <div className="divide-y divide-[var(--app-border)]">
              {scheduleItems.length ? scheduleItems.map((event) => (
                <button key={event.flow.id} type="button" onClick={() => setSelectedFlowID(event.flow.id)} className="grid w-full grid-cols-[88px_140px_minmax(0,1fr)_120px] items-center gap-3 px-4 py-4 text-left transition hover:bg-[var(--app-surface-subtle)]">
                  <span className="font-mono text-sm text-[var(--app-text)]">{event.time}</span>
                  <span className="truncate text-xs text-[var(--app-text-muted)]">{event.day}</span>
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium text-[var(--app-text)]">{event.flow.name}</span>
                    <span className="mt-1 block truncate text-xs text-[var(--app-text-muted)]">{event.flow.workspace} / {event.flow.agentType} / {event.meta}</span>
                  </span>
                  <span className="justify-self-end"><StatusBadge status={event.flow.status} /></span>
                </button>
              )) : <div className="px-4 py-5 text-sm text-[var(--app-text-muted)]">No scheduled flows yet.</div>}
            </div>
          </div>
        </div>

        <aside className={cn(surfaceClass, 'flex flex-col p-5')}>
          <h2 className="text-base font-semibold text-[var(--app-text)]">Needs attention</h2>
          <div className="mt-4 flex-1 divide-y divide-[var(--app-border)] overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)]">
            {attentionItems.length ? attentionItems.map((item) => (
              <button key={item.flow.id} type="button" onClick={() => setSelectedFlowID(item.flow.id)} className="flex w-full items-start gap-3 px-3 py-3 text-left transition hover:bg-[var(--app-surface-subtle)]">
                <FlowStatusDot status={item.dotStatus} className="mt-1" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-[var(--app-text)]">{item.flow.name}</span>
                  <span className="mt-1 block truncate text-xs text-[var(--app-text-muted)]">{item.meta}</span>
                </span>
                <StatusBadge status={item.flow.status} />
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
            <FilterSelect label="Agent filter" value={agentFilter} onChange={setAgentFilter} options={agentOptions} />
            <FilterSelect label="Status filter" value={statusFilter} onChange={setStatusFilter} options={statusOptions} />
          </div>
        </div>

        <div className="overflow-x-auto">
          <table data-testid="flows-table" className="w-full min-w-[980px] border-collapse text-left">
            <thead>
              <tr className="border-b border-[var(--app-border)] bg-[var(--app-bg-inset)] text-[11px] uppercase tracking-[0.16em] text-[var(--app-text-muted)]">
                <th className="px-5 py-3 font-medium">Flow</th>
                <th className="px-4 py-3 font-medium">Run context</th>
                <th className="px-4 py-3 font-medium">Last run</th>
                <th className="px-4 py-3 font-medium">Next run</th>
                <th className="px-4 py-3 font-medium">Status</th>
                <th className="px-4 py-3 font-medium">Enabled</th>
                <th className="px-5 py-3 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {filteredFlows.length ? filteredFlows.map((flow) => (
                <tr key={flow.id} data-testid="flows-row" data-flow-id={flow.id} className="border-b border-[var(--app-border)] last:border-b-0 hover:bg-[var(--app-surface-subtle)]">
                  <td className="px-5 py-4 align-top">
                    <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="max-w-[520px] text-left">
                      <div className="truncate text-sm font-medium text-[var(--app-text)]">{flow.name}</div>
                      <div className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">{flow.task}</div>
                    </button>
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="max-w-[300px] rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] p-3">
                      <div className="flex items-center gap-2 text-xs text-[var(--app-text)]"><MapPin size={13} className="text-[var(--app-text-muted)]" /> {flow.workspace} <span className="text-[var(--app-text-subtle)]">/ {flow.target}</span></div>
                      <div className="mt-2 flex items-center gap-2 text-xs text-[var(--app-text)]"><Bot size={13} className="text-[var(--app-text-muted)]" /> {flow.agentType} <span className="text-[var(--app-text-subtle)]">/ {flow.agent}</span></div>
                      <div className="mt-2 flex items-center gap-2 text-xs text-[var(--app-text)]"><Clock size={13} className="text-[var(--app-text-muted)]" /> {flow.schedule}</div>
                    </div>
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="text-sm text-[var(--app-text)]">{flow.lastRun}</div>
                    {flow.lastRunMeta ? <div className="mt-1 text-xs text-[var(--app-text-muted)]">{flow.lastRunMeta}</div> : null}
                  </td>
                  <td className="px-4 py-4 align-top">
                    <div className="text-sm text-[var(--app-text)]">{flow.nextRun}</div>
                    {flow.nextRunMeta ? <div className="mt-1 text-xs text-[var(--app-text-muted)]">{flow.nextRunMeta}</div> : null}
                  </td>
                  <td className="px-4 py-4 align-top"><StatusBadge status={flow.status} /></td>
                  <td className="px-4 py-4 align-top"><EnabledToggle enabled={flow.enabled} disabled onToggle={() => undefined} /></td>
                  <td className="px-5 py-4 text-right align-top">
                    <button type="button" onClick={() => setSelectedFlowID(flow.id)} className="inline-flex h-8 w-8 items-center justify-center rounded-lg border border-[var(--app-border)] text-[var(--app-text-muted)] transition hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" aria-label={`Manage ${flow.name}`}>
                      <MoreHorizontal size={16} />
                    </button>
                  </td>
                </tr>
              )) : (
                <tr><td colSpan={7} className="px-5 py-8 text-center text-sm text-[var(--app-text-muted)]">No flows found.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <AddFlowModal open={addOpen} onClose={() => setAddOpen(false)} onAdd={(input) => void addFlow(input)} busy={saving} />
    </div>
  )
}
