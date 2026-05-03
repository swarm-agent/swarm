import { useEffect, useMemo, useState, type ChangeEvent } from 'react'
import { Copy, Check, AlertCircle, ChevronDown } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { requestJson } from '../../../../app/api'
import { ChatMarkdown } from '../../chat/components/chat-markdown'
import type { ModelOptionRecord } from '../../chat/types/chat'
import { modelOptionsQueryOptions } from '../../../queries/query-options'
import { useQuery } from '@tanstack/react-query'
import type { DesktopPermissionRecord } from '../../types/realtime'
import {
  buildAskUserResolutionReason,
  buildWorkspaceScopeResolutionReason,
  buildGenericPermissionMarkdown,
  parseAgentChangePermission,
  type AgentEffectiveExecution,
  type AgentToolInventory,
  parseAskUserPermission,
  parseExitPlanPermission,
  parseManageTodosPermission,
  parsePlanUpdatePermission,
  parseTaskLaunchPermission,
  parseWorkspaceScopePermission,
  permissionDisplayToolName,
  permissionKind,
} from '../services/permission-payload'

interface DesktopPermissionModalProps {
  open: boolean
  permission: DesktopPermissionRecord | null
  pendingCount: number
  sessionMode: string
  onOpenChange: (open: boolean) => void
  onResolve: (
    action: 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny',
    reason: string,
    approvedArguments?: Record<string, unknown>,
  ) => Promise<void>
}

function shouldKeepNativeEnter(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false
  }
  if (target.isContentEditable) {
    return true
  }
  const tag = target.tagName.toLowerCase()
  if (tag === 'button' || tag === 'select' || tag === 'a') {
    return true
  }
  if (tag === 'input') {
    const type = (target as HTMLInputElement).type.toLowerCase()
    return ['button', 'checkbox', 'color', 'file', 'image', 'radio', 'range', 'reset', 'submit'].includes(type)
  }
  return false
}

function usePermissionKeyboardShortcuts({
  open,
  disabled,
  onPrimary,
  onDeny,
}: {
  open: boolean
  disabled: boolean
  onPrimary?: () => void
  onDeny: () => void
}) {
  useEffect(() => {
    if (!open) {
      return undefined
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (disabled || event.repeat) {
        return
      }
      if (event.key === 'Escape') {
        event.preventDefault()
        onDeny()
        return
      }
      if (event.key !== 'Enter' || event.shiftKey || event.isComposing || !onPrimary || shouldKeepNativeEnter(event.target)) {
        return
      }
      event.preventDefault()
      onPrimary()
    }
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [disabled, onDeny, onPrimary, open])
}

function ModalShell({
  open,
  title,
  subtitle,
  pendingCount,
  sessionMode,
  widthClassName,
  bodyClassName,
  footer,
  children,
  onOpenChange,
  onRequestClose,
  onPrimaryShortcut,
  onDenyShortcut,
  shortcutsDisabled = false,
  showSessionMeta = true,
}: {
  open: boolean
  title: string
  subtitle?: string
  pendingCount: number
  sessionMode: string
  widthClassName: string
  bodyClassName?: string
  footer?: React.ReactNode
  children: React.ReactNode
  onOpenChange: (open: boolean) => void
  onRequestClose?: () => void
  onPrimaryShortcut?: () => void
  onDenyShortcut?: () => void
  shortcutsDisabled?: boolean
  showSessionMeta?: boolean
}) {
  const handleRequestClose = () => {
    if (shortcutsDisabled) {
      return
    }
    if (onRequestClose) {
      onRequestClose()
      return
    }
    onOpenChange(false)
  }

  usePermissionKeyboardShortcuts({
    open,
    disabled: shortcutsDisabled,
    onPrimary: onPrimaryShortcut,
    onDeny: onDenyShortcut ?? handleRequestClose,
  })

  if (!open) {
    return null
  }

  return (
    <Dialog
      role="dialog"
      aria-modal="true"
      aria-label={title}
      className="z-[80] p-1.5 pt-[calc(var(--app-safe-area-top)_+_0.375rem)] sm:p-4"
    >
      <DialogBackdrop onClick={handleRequestClose} />
      <DialogPanel
        className={cn(
          'flex min-h-0 max-h-[calc(100dvh_-_var(--app-safe-area-top)_-_var(--app-safe-area-bottom)_-_12px)] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:max-h-[calc(100vh-32px)]',
          widthClassName,
        )}
      >
        <div className="shrink-0 flex items-start justify-between gap-2 border-b border-[var(--app-border)] px-3 py-2.5 sm:items-center sm:gap-3 sm:px-5 sm:py-3">
          <div className="min-w-0 flex flex-col gap-1 sm:flex-row sm:flex-wrap sm:items-center sm:gap-x-3 sm:gap-y-1">
            <h2 className="text-sm font-semibold tracking-tight text-[var(--app-text)] sm:text-base">{title}</h2>
            {subtitle?.trim() ? <span className="text-xs leading-5 text-[var(--app-text-muted)] sm:text-sm">{subtitle}</span> : null}
            {showSessionMeta ? (
              <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-subtle)]">
                <span>mode {sessionMode.trim() || 'plan'}</span>
                {pendingCount > 1 ? <span>{pendingCount} pending</span> : null}
              </div>
            ) : pendingCount > 1 ? (
              <span className="text-xs text-[var(--app-text-subtle)]">{pendingCount} pending</span>
            ) : null}
          </div>
          <ModalCloseButton onClick={handleRequestClose} aria-label="Close permission modal" />
        </div>
        <div className={cn('min-h-0 flex-1 overflow-auto px-3 py-3 sm:px-5 sm:py-4', bodyClassName)}>{children}</div>
        {footer}
      </DialogPanel>
    </Dialog>
  )
}

function PermissionActionBar({
  onApprove,
  onDeny,
  onAlwaysAllow,
  onAlwaysDeny,
  alwaysAllowLabel = 'Always Allow',
  alwaysDenyLabel = 'Always Deny',
  showPersistentActions = false,
  loading,
  approveLabel = 'Approve',
  denyLabel = 'Deny',
  note,
  onNoteChange,
  noteLabel = 'Optional note',
  notePlaceholder = 'Optional note to send back with this action…',
  shortcutHint = 'Enter approves · Esc denies',
}: {
  onApprove: () => void
  onDeny: () => void
  onAlwaysAllow?: () => void
  onAlwaysDeny?: () => void
  alwaysAllowLabel?: string
  alwaysDenyLabel?: string
  showPersistentActions?: boolean
  loading: boolean
  approveLabel?: string
  denyLabel?: string
  note?: string
  onNoteChange?: (value: string) => void
  noteLabel?: string
  notePlaceholder?: string
  shortcutHint?: string
}) {
  const [noteOpen, setNoteOpen] = useState(false)
  const showPersistentGroup = showPersistentActions && (onAlwaysDeny || onAlwaysAllow)
  const showNoteToggle = Boolean(onNoteChange)
  const hasNote = Boolean(note?.trim())

  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 pb-[max(0.5rem,var(--app-safe-area-bottom))] sm:px-5 sm:py-3 sm:pb-3">
      {showNoteToggle ? (
        <div className="mb-2">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setNoteOpen((current) => !current)}
            className="min-h-8 px-2.5 text-xs"
            aria-expanded={noteOpen}
          >
            {noteOpen ? 'Hide note' : hasNote ? 'Edit note' : 'Add note'}
          </Button>
          {noteOpen ? (
            <label className="mt-2 grid gap-1.5">
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{noteLabel}</span>
              <Textarea
                value={note ?? ''}
                onChange={(event) => onNoteChange?.(event.target.value)}
                placeholder={notePlaceholder}
                className="min-h-11 resize-none bg-[var(--app-bg-alt)] sm:min-h-[3.5rem]"
                rows={2}
              />
            </label>
          ) : null}
        </div>
      ) : null}
      <div className="flex touch-manipulation flex-col gap-2 sm:flex-row sm:flex-wrap sm:justify-end">
        <Button
          type="button"
          variant="primary"
          onClick={onApprove}
          disabled={loading}
          title="Enter"
          className="order-1 w-full sm:order-4 sm:w-auto"
        >
          {approveLabel}
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={onDeny}
          disabled={loading}
          title="Esc"
          className="order-2 w-full sm:order-1 sm:w-auto"
        >
          {denyLabel}
        </Button>
        {showPersistentGroup ? (
          <div className="order-3 grid grid-cols-2 gap-2 sm:contents">
            {onAlwaysDeny ? (
              <Button type="button" variant="outline" onClick={onAlwaysDeny} disabled={loading} className="w-full sm:order-2 sm:w-auto">
                {alwaysDenyLabel}
              </Button>
            ) : null}
            {onAlwaysAllow ? (
              <Button type="button" variant="outline" onClick={onAlwaysAllow} disabled={loading} className="w-full sm:order-3 sm:w-auto">
                {alwaysAllowLabel}
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
      {shortcutHint ? <div className="mt-1.5 hidden text-center text-[11px] text-[var(--app-text-subtle)] sm:block sm:text-right">{shortcutHint}</div> : null}
    </div>
  )
}

function GenericPermissionModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)
  const [alwaysPreview, setAlwaysPreview] = useState('')
  const [alwaysPreviewError, setAlwaysPreviewError] = useState('')

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
      setAlwaysPreview('')
      setAlwaysPreviewError('')
    }
  }, [open, permission?.id])

  useEffect(() => {
    if (!open || !permission || !genericPermissionSupportsPersistentActions(permission)) {
      return undefined
    }

    let cancelled = false
    setAlwaysPreview('')
    setAlwaysPreviewError('')

    void permissionPersistentRulePreview(permission, sessionMode)
      .then((preview) => {
        if (!cancelled) {
          setAlwaysPreview(preview)
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setAlwaysPreviewError(error instanceof Error ? error.message : String(error))
        }
      })

    return () => {
      cancelled = true
    }
  }, [open, permission, sessionMode])

  if (!permission) {
    return null
  }

  const body = buildGenericPermissionMarkdown(permission)

  const resolve = async (
    action: 'approve' | 'deny' | 'approve_always' | 'always_allow' | 'always_deny',
    approvedArguments?: Record<string, unknown>,
  ) => {
    setLoading(true)
    try {
      await onResolve(action, note.trim(), approvedArguments)
    } finally {
      setLoading(false)
    }
  }

  const persistentAllowed = genericPermissionSupportsPersistentActions(permission)
  const persistentRulePreview = alwaysPreview || genericPermissionPersistentRulePreview(permission)
  const persistentRuleDescription = persistentRulePreview
    || (alwaysPreviewError ? `Unable to preview reusable rule: ${alwaysPreviewError}` : 'Loading reusable policy rule preview…')

  return (
    <ModalShell
      open={open}
      title="Permission request"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(920px,calc(100vw-32px))]"
      bodyClassName="py-2.5 sm:py-3"
      showSessionMeta={false}
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-3">
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2.5 text-sm leading-6">
          <ChatMarkdown content={body} />
        </div>
        {persistentAllowed ? (
          <div className="flex flex-wrap items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
            <span className="font-medium text-[var(--app-text-subtle)]">Always allow prefix</span>
            <span className="min-w-0 flex-1 truncate font-mono text-[var(--app-text)]">{persistentRuleDescription || 'available after approval'}</span>
          </div>
        ) : null}
        <label className="grid gap-1.5">
          <span className="text-xs font-medium text-[var(--app-text-subtle)]">Response note</span>
          <Textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder="Optional note…"
            className="min-h-[56px] resize-y bg-[var(--app-bg-alt)] sm:min-h-[72px]"
            rows={2}
          />
        </label>
      </div>
      <PermissionActionBar
        loading={loading}
        onApprove={() => void resolve('approve')}
        onDeny={() => void resolve('deny')}
        onAlwaysAllow={persistentAllowed ? () => void resolve('approve_always') : undefined}
        onAlwaysDeny={persistentAllowed ? () => void resolve('always_deny') : undefined}
        showPersistentActions={persistentAllowed}
      />
    </ModalShell>
  )
}

function ExitPlanModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
      setCopyState('idle')
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parseExitPlanPermission(permission)

  const handleCopy = async () => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(payload.body)
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      await onResolve(action, note.trim())
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Exit Plan Mode'}
      subtitle={payload.planId ? `Plan ${payload.planId}` : 'Approve this request to leave plan mode and continue execution'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          note={note}
          onNoteChange={setNote}
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="flex h-full min-h-0 flex-col gap-3 sm:gap-4">
        <section className="flex min-h-0 flex-1 flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2 sm:gap-3">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Plan</span>
            <Button type="button" variant="outline" size="sm" className="min-h-8 px-2.5" onClick={() => void handleCopy()}>
              {copyState === 'copied' ? (
                <Check className="size-4" />
              ) : copyState === 'error' ? (
                <AlertCircle className="size-4" />
              ) : (
                <Copy className="size-4" />
              )}
              {copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Copy failed' : 'Copy'}
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto pr-1">
            <ChatMarkdown content={payload.body} className="text-base leading-7" />
          </div>
        </section>
      </div>
    </ModalShell>
  )
}

function PlanUpdateModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parsePlanUpdatePermission(permission)

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      const reason =
        action === 'approve'
          ? JSON.stringify({
              action: 'save',
              approved_arguments:
                Object.keys(payload.approvedArguments).length > 0
                  ? payload.approvedArguments
                  : {
                      action: 'save',
                      plan_id: payload.planId,
                      title: payload.title,
                      plan: payload.plan,
                    },
              note: note.trim(),
            })
          : note.trim()
      await onResolve(action, reason)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Review Plan Update'}
      subtitle={payload.planId ? `Plan ${payload.planId}` : 'Approve this request to revise an existing plan'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          approveLabel="Approve update"
          note={note}
          onNoteChange={setNote}
          noteLabel="Message to agent"
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-5">
        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Diff preview</div>
          <div className="mt-3 overflow-x-auto rounded-xl bg-[var(--app-bg-alt)] p-3 font-mono text-sm leading-6 text-[var(--app-text)]">
            {payload.diffLines.length > 0 ? (
              <div className="space-y-1">
                {payload.diffLines.map((line, index) => (
                  <div
                    key={`${index}:${line}`}
                    className={cn(
                      line.startsWith('+') && !line.startsWith('+++') && 'text-[var(--app-success)]',
                      line.startsWith('-') && !line.startsWith('---') && 'text-[var(--app-danger)]',
                      line.startsWith('@@') && 'text-[var(--app-primary)]',
                    )}
                  >
                    {line || ' '}
                  </div>
                ))}
              </div>
            ) : (
              <div className="text-[var(--app-text-muted)]">No diff lines were provided.</div>
            )}
          </div>
        </section>

        <div className="grid gap-4 lg:grid-cols-2">
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Previous plan</div>
            <div className="mt-2 text-sm font-medium text-[var(--app-text)]">{payload.priorTitle || payload.title || 'Plan'}</div>
            <div className="mt-3 rounded-xl bg-[var(--app-bg-alt)] p-3">
              <ChatMarkdown content={payload.priorPlan || 'No prior plan text was provided.'} />
            </div>
          </section>

          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Updated plan</div>
            <div className="mt-2 text-sm font-medium text-[var(--app-text)]">{payload.title || 'Plan'}</div>
            <div className="mt-3 rounded-xl bg-[var(--app-bg-alt)] p-3">
              <ChatMarkdown content={payload.plan || 'No updated plan text was provided.'} />
            </div>
          </section>
        </div>
      </div>
    </ModalShell>
  )
}

function ManageTodosModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parseManageTodosPermission(permission)

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      await onResolve(
        action,
        note.trim(),
        action === 'approve' && Object.keys(payload.approvedArguments).length > 0
          ? payload.approvedArguments
          : undefined,
      )
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Review Todo Changes'}
      subtitle="Approve this request to update workspace todos"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          note={note}
          onNoteChange={setNote}
          noteLabel="Message to agent"
          notePlaceholder="Type a note to send back with this action…"
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="flex h-full min-h-0 flex-col gap-3 sm:gap-4">
        <section className="flex min-h-0 flex-1 flex-col gap-3">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Task preview</span>
          <div className="min-h-0 flex-1 overflow-y-auto rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4 pr-5">
            {payload.isBatch ? (
              <div className="grid gap-4">
                <div className="text-sm font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Atomic batch preview</div>
                {payload.batchRows.length === 0 ? (
                  <div className="text-sm text-[var(--app-text-muted)]">No operations were provided.</div>
                ) : (
                  <div className="grid gap-3">
                    {payload.batchRows.map((row, index) => (
                      <div key={`${index}-${row.text}`} className="grid gap-1">
                        <div className="text-base leading-7 text-[var(--app-text)]">{row.text}</div>
                        {row.metadata.map((entry) => (
                          <div key={`${index}-${entry}`} className="pl-6 text-sm leading-6 text-[var(--app-text-muted)]">
                            meta: {entry}
                          </div>
                        ))}
                      </div>
                    ))}
                  </div>
                )}
                {payload.summaryLine ? <div className="text-sm text-[var(--app-text-muted)]">{payload.summaryLine}</div> : null}
              </div>
            ) : (
              <ChatMarkdown content={payload.body} className="text-base leading-7" />
            )}
          </div>
        </section>
      </div>
    </ModalShell>
  )
}

function AskUserModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const payload = useMemo(() => (permission ? parseAskUserPermission(permission) : null), [permission])
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [customInputs, setCustomInputs] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open || !payload) {
      return
    }
    const nextAnswers: Record<string, string> = {}
    const nextCustomInputs: Record<string, string> = {}
    for (const question of payload.questions) {
      const first = question.options[0]
      nextAnswers[question.id] = first?.allowCustom ? '' : (first?.value ?? '')
      nextCustomInputs[question.id] = ''
    }
    setAnswers(nextAnswers)
    setCustomInputs(nextCustomInputs)
    setError(null)
    setLoading(false)
  }, [open, payload, permission?.id])

  if (!permission || !payload) {
    return null
  }

  const updateAnswer = (questionId: string, value: string, isCustom: boolean) => {
    setAnswers((current) => ({ ...current, [questionId]: isCustom ? '__custom__' : value }))
    if (!isCustom) {
      setCustomInputs((current) => ({ ...current, [questionId]: '' }))
    }
  }

  const submit = async (action: 'approve' | 'deny') => {
    setLoading(true)
    setError(null)
    try {
      if (action === 'deny') {
        await onResolve('deny', '')
        return
      }

      const resolvedAnswers: Record<string, string> = {}
      for (const question of payload.questions) {
        const selected = (answers[question.id] ?? '').trim()
        if (selected === '__custom__') {
          resolvedAnswers[question.id] = (customInputs[question.id] ?? '').trim()
        } else {
          resolvedAnswers[question.id] = selected
        }
      }

      const reason = buildAskUserResolutionReason(payload, resolvedAnswers)
      if (reason === null) {
        setError('Answer each required question before submitting.')
        return
      }
      await onResolve('approve', reason)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Ask User'}
      subtitle="Answer the questions below, then submit your response"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1140px,calc(100vw-48px))]"
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void submit('approve')}
      onDenyShortcut={() => void submit('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-3 sm:gap-5">
        {payload.context.trim() ? (
          <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3 sm:rounded-2xl sm:p-4">
            <ChatMarkdown content={payload.context} />
          </div>
        ) : null}

        <div className="grid gap-3 sm:gap-4">
          {payload.questions.map((question, index) => {
            const selected = answers[question.id] ?? ''
            const selectedIsCustom = selected === '__custom__'
            return (
              <section key={question.id} className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 sm:rounded-2xl sm:p-4">
                <div className="mb-2.5 sm:mb-3">
                  <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                    {question.header || `Question ${index + 1}`}
                    {question.required ? ' · required' : ''}
                  </div>
                  <div className="mt-2 rounded-lg bg-[var(--app-bg-alt)] p-2.5 sm:rounded-xl sm:p-3">
                    <ChatMarkdown content={question.question} />
                  </div>
                </div>

                <div className="grid gap-2">
                  {question.options.map((option) => {
                    const isSelected = option.allowCustom ? selectedIsCustom : selected === option.value
                    return (
                      <button
                        key={`${question.id}:${option.value}:${option.label}`}
                        type="button"
                        className={cn(
                          'grid gap-1 rounded-xl border px-3 py-2.5 text-left transition sm:rounded-2xl sm:px-4 sm:py-3',
                          isSelected
                            ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_10%,var(--app-surface))]'
                            : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-bg-alt)]',
                        )}
                        onClick={() => updateAnswer(question.id, option.value, option.allowCustom)}
                      >
                        <span className="text-sm font-medium text-[var(--app-text)]">{option.label}</span>
                        {option.description ? <span className="text-xs text-[var(--app-text-muted)]">{option.description}</span> : null}
                      </button>
                    )
                  })}
                </div>

                {selectedIsCustom ? (
                  <label className="mt-3 grid gap-2">
                    <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Custom response</span>
                    <Textarea
                      value={customInputs[question.id] ?? ''}
                      onChange={(event) => setCustomInputs((current) => ({ ...current, [question.id]: event.target.value }))}
                      placeholder="Type your response…"
                      className="min-h-[88px] resize-y bg-[var(--app-bg-alt)] sm:min-h-[110px]"
                      rows={3}
                    />
                  </label>
                ) : null}
              </section>
            )
          })}
        </div>

        {error ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
      </div>
      <PermissionActionBar loading={loading} onApprove={() => void submit('approve')} onDeny={() => void submit('deny')} approveLabel="Submit response" />
    </ModalShell>
  )
}

function WorkspaceScopeModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (open) {
      setLoading(false)
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parseWorkspaceScopePermission(permission)
  const workspaceLabel = payload.workspaceName || payload.workspacePath || 'saved workspace'

  const resolve = async (action: 'approve' | 'deny', reason: string) => {
    setLoading(true)
    try {
      await onResolve(action, reason)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Allow read access?'}
      subtitle={`${payload.accessLabel} · ${permissionDisplayToolName(payload.toolName || permission.toolName)}`}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1120px,calc(100vw-48px))]"
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.sessionAllow.decision))}
      onDenyShortcut={() => void resolve('deny', '')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-5">
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
          <p className="text-sm leading-6 text-[var(--app-text)]">{payload.summary}</p>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Requested path</div>
            <div className="mt-2 break-all font-mono text-sm text-[var(--app-text)]">{payload.requestedPath || 'Unavailable'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Session scope root</div>
            <div className="mt-2 break-all font-mono text-sm text-[var(--app-text)]">{payload.directoryPath || payload.resolvedPath || payload.requestedPath || 'Unavailable'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Resolved target</div>
            <div className="mt-2 break-all font-mono text-sm text-[var(--app-text)]">{payload.resolvedPath || payload.requestedPath || 'Unavailable'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Workspace</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{payload.workspaceExists ? workspaceLabel : 'No saved workspace is active for this session.'}</div>
          </section>
        </div>

        <div className="grid gap-3 lg:grid-cols-2">
          <section className="flex h-full flex-col rounded-2xl border border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_8%,var(--app-surface))] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Temporary access</div>
            <h3 className="mt-2 text-base font-semibold text-[var(--app-text)]">{payload.sessionAllow.label}</h3>
            <p className="mt-2 flex-1 text-sm leading-6 text-[var(--app-text-muted)]">{payload.sessionAllow.description || payload.temporaryBehavior}</p>
            <Button
              type="button"
              variant="primary"
              className="mt-4 w-full sm:w-auto"
              onClick={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.sessionAllow.decision))}
              disabled={loading}
            >
              {payload.sessionAllow.label}
            </Button>
          </section>

          <section
            className={cn(
              'flex h-full flex-col rounded-2xl border p-4',
              payload.addToWorkspace.available
                ? 'border-[var(--app-border)] bg-[var(--app-surface)]'
                : 'border-[var(--app-border)] bg-[var(--app-bg-alt)] opacity-75',
            )}
          >
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Persistent access</div>
            <h3 className="mt-2 text-base font-semibold text-[var(--app-text)]">{payload.addToWorkspace.label}</h3>
            <p className="mt-2 flex-1 text-sm leading-6 text-[var(--app-text-muted)]">{payload.addToWorkspace.description || payload.persistentBehavior}</p>
            <Button
              type="button"
              variant="ghost"
              className="mt-4 w-full sm:w-auto"
              onClick={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.addToWorkspace.decision))}
              disabled={loading || !payload.addToWorkspace.available}
            >
              {payload.addToWorkspace.label}
            </Button>
          </section>
        </div>
      </div>

      <div className="flex flex-col gap-2 border-t border-[var(--app-border)] px-3 py-2.5 pb-[max(0.625rem,var(--app-safe-area-bottom))] sm:flex-row sm:justify-end sm:px-6 sm:py-4 sm:pb-4">
        <Button type="button" variant="ghost" className="w-full sm:w-auto" onClick={() => void resolve('deny', '')} disabled={loading}>
          Deny
        </Button>
      </div>
    </ModalShell>
  )
}

function TaskLaunchModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [note, setNote] = useState('')
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parseTaskLaunchPermission(permission)

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      await onResolve(action, note.trim())
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title}
      subtitle={payload.subtitle || 'Review the delegated task before spawning subagents'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1140px,calc(100vw-48px))]"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          approveLabel="Launch subagents"
          note={note}
          onNoteChange={setNote}
          noteLabel="Message to agent"
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
      onRequestClose={() => {
        if (loading) {
          return
        }
        void resolve('deny')
      }}
    >
      <div className="grid gap-4">
        <section className="rounded-2xl border border-[var(--app-border-accent)] bg-[var(--app-surface-subtle)] p-4">
          <div className="mb-3 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-primary)]">Task</div>
          <div className="rounded-xl bg-[var(--app-bg-alt)] p-3 text-sm leading-6 text-[var(--app-text)]">
            <ChatMarkdown content={payload.description || 'Delegated task'} />
          </div>
        </section>

        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <div className="mb-3 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Agent roles</div>
          {payload.launches.length > 0 ? (
            <div className="overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)]">
              <div className="grid grid-cols-[3.5rem_minmax(7rem,12rem)_1fr] gap-3 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                <div>#</div>
                <div>Agent</div>
                <div>Assignment</div>
              </div>
              {payload.launches.map((launch) => (
                <div key={`${launch.index}:${launch.requestedSubagentType}:${launch.resolvedAgentName}`} className="grid grid-cols-[3.5rem_minmax(7rem,12rem)_1fr] gap-3 border-b border-[var(--app-border)] px-3 py-3 text-sm last:border-b-0">
                  <div className="font-medium text-[var(--app-text-subtle)]">#{launch.index}</div>
                  <div className="break-words font-medium text-[var(--app-text)]">{launch.resolvedAgentName || launch.requestedSubagentType || 'subagent'}</div>
                  <div className="min-w-0 text-[var(--app-text)]">
                    <ChatMarkdown content={launch.assignment || 'No launch-specific instructions.'} />
                    {launch.resolvedAgentError ? <div className="mt-2 text-sm text-[var(--app-danger)]">{launch.resolvedAgentError}</div> : null}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <div className="rounded-xl bg-[var(--app-bg-alt)] px-3 py-2 text-sm text-[var(--app-text-muted)]">No launches were included in the manifest.</div>
          )}
        </section>

        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <div className="mb-3 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Meta</div>
          <div className="text-sm text-[var(--app-text-subtle)]">
            launches {payload.launchCount}
            <span className="px-2">·</span>
            bash {payload.allowBash ? 'yes' : 'no'}
            {payload.reportMaxChars > 0 ? <><span className="px-2">·</span>report {payload.reportMaxChars} chars</> : null}
            {payload.resolvedAgentName ? <><span className="px-2">·</span>router {payload.resolvedAgentName}</> : null}
          </div>
          {payload.resolvedAgentError ? <div className="mt-2 text-sm text-[var(--app-danger)]">{payload.resolvedAgentError}</div> : null}
        </section>
      </div>
    </ModalShell>
  )
}

interface AgentProfileFormState {
  name: string
  mode: string
  description: string
  provider: string
  model: string
  thinking: string
  prompt: string
  executionSetting: AgentEffectiveExecution
  exitPlanModeEnabled: boolean
  enabled: boolean
  toolContractPreset: string
  toolContractInheritPolicy: boolean
  toolContractTools: Record<string, AgentToolConfigFormState>
}

interface AgentToolConfigFormState {
  enabled: boolean
  bashPrefixes: string
}

function stringValue(record: Record<string, unknown>, key: string): string {
  const value = record[key]
  return typeof value === 'string' ? value : ''
}

function boolValue(record: Record<string, unknown>, key: string, fallback: boolean): boolean {
  const value = record[key]
  return typeof value === 'boolean' ? value : fallback
}

function stringArrayValue(record: Record<string, unknown>, key: string): string[] {
  const value = record[key]
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string') : []
}

function objectValue(record: Record<string, unknown>, key: string): Record<string, unknown> {
  const value = record[key]
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function normalizeExecutionSetting(value: string): AgentEffectiveExecution {
  const normalized = value.trim().toLowerCase()
  if (normalized === 'readwrite' || normalized === 'read_write' || normalized === 'read-write') return 'readwrite'
  if (normalized === 'read') return 'read'
  if (normalized === 'plan → auto' || normalized === 'plan_auto' || normalized === 'plan-auto') return 'plan → auto'
  return 'unset'
}

function normalizeEditableExecutionSetting(value: string): AgentEffectiveExecution {
  const normalized = normalizeExecutionSetting(value)
  return normalized === 'readwrite' ? 'readwrite' : 'read'
}

function profileFormFromPayload(payload: ReturnType<typeof parseAgentChangePermission>): AgentProfileFormState {
  const profile = payload.profile
  const approvedContent = objectValue(payload.approvedArguments, 'content')
  const source = { ...profile, ...approvedContent }
  return {
    name: stringValue(source, 'name') || payload.agentName,
    mode: stringValue(source, 'mode') || 'primary',
    description: stringValue(source, 'description'),
    provider: stringValue(source, 'provider'),
    model: stringValue(source, 'model'),
    thinking: stringValue(source, 'thinking'),
    prompt: stringValue(source, 'prompt'),
    executionSetting: normalizeEditableExecutionSetting(stringValue(source, 'effective_execution_setting') || stringValue(source, 'execution_setting') || payload.effectiveExecution || 'read'),
    exitPlanModeEnabled: boolValue(source, 'exit_plan_mode_enabled', false),
    enabled: boolValue(source, 'enabled', true),
    ...toolContractFormFromProfile(source),
  }
}

function splitCSV(value: string): string[] {
  return value.split(',').map((entry) => entry.trim()).filter(Boolean)
}

function toolContractFormFromProfile(source: Record<string, unknown>): Pick<AgentProfileFormState, 'toolContractPreset' | 'toolContractInheritPolicy' | 'toolContractTools'> {
  const contract = objectValue(source, 'tool_contract')
  const contractTools = objectValue(contract, 'tools')
  const tools: Record<string, AgentToolConfigFormState> = {}
  Object.entries(contractTools).forEach(([name, rawConfig]) => {
    const toolName = name.trim()
    if (!toolName) return
    const config = rawConfig && typeof rawConfig === 'object' && !Array.isArray(rawConfig) ? rawConfig as Record<string, unknown> : {}
    tools[toolName] = {
      enabled: boolValue(config, 'enabled', true),
      bashPrefixes: stringArrayValue(config, 'bash_prefixes').join(', '),
    }
  })

  const scope = objectValue(source, 'tool_scope')
  if (Object.keys(tools).length === 0) {
    stringArrayValue(scope, 'allow_tools').forEach((toolName) => { tools[toolName] = { enabled: true, bashPrefixes: '' } })
    stringArrayValue(scope, 'deny_tools').forEach((toolName) => { tools[toolName] = { enabled: false, bashPrefixes: '' } })
    const bashPrefixes = stringArrayValue(scope, 'bash_prefixes').join(', ')
    if (bashPrefixes) {
      tools.bash = { enabled: true, bashPrefixes }
    }
  }

  return {
    toolContractPreset: stringValue(contract, 'preset') || stringValue(scope, 'preset'),
    toolContractInheritPolicy: boolValue(contract, 'inherit_policy', boolValue(scope, 'inherit_policy', false)),
    toolContractTools: tools,
  }
}

function toolInventoryTools(payload: ReturnType<typeof parseAgentChangePermission>, form: AgentProfileFormState): AgentToolInventory['tools'] {
  const byName = new Map<string, AgentToolInventory['tools'][number]>()
  payload.toolInventory.tools.forEach((tool) => byName.set(tool.name, tool))
  Object.keys(form.toolContractTools).forEach((name) => {
    if (!byName.has(name)) {
      byName.set(name, { name, description: '', group: 'custom', kind: 'custom' })
    }
  })
  return Array.from(byName.values()).sort((left, right) => {
    const group = left.group.localeCompare(right.group)
    return group !== 0 ? group : left.name.localeCompare(right.name)
  })
}

const AGENT_WRITE_TOOL_NAMES = new Set(['write', 'edit', 'bash', 'task', 'git_add', 'git_commit'])
const AGENT_DEFAULT_READWRITE_TOOL_NAMES = ['write', 'edit']
const AGENT_THINKING_OPTIONS = [
  { value: '', label: 'Default' },
  { value: 'off', label: 'Off' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'X-High' },
]

function deriveExecutionSettingFromTools(tools: Record<string, AgentToolConfigFormState>): AgentEffectiveExecution {
  return Object.entries(tools).some(([name, config]) => {
    if (!AGENT_WRITE_TOOL_NAMES.has(name.trim().toLowerCase())) return false
    return config.enabled || splitCSV(config.bashPrefixes).length > 0
  }) ? 'readwrite' : 'read'
}

function agentToolContractFromForm(payload: ReturnType<typeof parseAgentChangePermission>, form: AgentProfileFormState): Record<string, unknown> {
  const tools: Record<string, unknown> = {}
  const inventoryToolNames = new Set(toolInventoryTools(payload, form).map((tool) => tool.name.trim()).filter(Boolean))
  inventoryToolNames.forEach((toolName) => {
    tools[toolName] = { enabled: false }
  })
  Object.entries(form.toolContractTools).forEach(([name, config]) => {
    const toolName = name.trim()
    if (!toolName) return
    const entry: Record<string, unknown> = { enabled: config.enabled }
    const bashPrefixes = splitCSV(config.bashPrefixes)
    if (bashPrefixes.length > 0) {
      entry.bash_prefixes = bashPrefixes
    }
    tools[toolName] = entry
  })
  const contract: Record<string, unknown> = {
    preset: form.toolContractPreset.trim(),
    inherit_policy: form.toolContractInheritPolicy,
  }
  if (Object.keys(tools).length > 0) {
    contract.tools = tools
  }
  return contract
}

function approvedArgumentsFromProfileForm(
  payload: ReturnType<typeof parseAgentChangePermission>,
  form: AgentProfileFormState,
): Record<string, unknown> {
  const args: Record<string, unknown> = { ...payload.approvedArguments }
  args.action = payload.action
  args.confirm = true
  const content: Record<string, unknown> = {
    ...(objectValue(payload.approvedArguments, 'content')),
    name: form.name.trim(),
    mode: form.mode.trim(),
    description: form.description.trim(),
    provider: form.provider.trim(),
    model: form.provider.trim() ? form.model.trim() : '',
    thinking: form.thinking.trim(),
    prompt: form.prompt,
    execution_setting: form.exitPlanModeEnabled || form.executionSetting === 'plan → auto' || form.executionSetting === 'unset' ? '' : form.executionSetting,
    exit_plan_mode_enabled: form.exitPlanModeEnabled,
    enabled: form.enabled,
  }
  content.tool_contract = agentToolContractFromForm(payload, form)
  delete content.tool_scope
  args.content = content
  args.agent = form.name.trim()
  args.name = form.name.trim()
  return args
}

interface AgentToolAccessSummary {
  allowed: string[]
  blocked: string[]
  restricted: string[]
  preset: string
  inheritPolicy: boolean
  catalogCount: number
}

function sortedUnique(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((left, right) => left.localeCompare(right))
}

function agentToolAccessSummary(payload: ReturnType<typeof parseAgentChangePermission>, form: AgentProfileFormState | null): AgentToolAccessSummary {
  if (form) {
    const allowed: string[] = []
    const blocked: string[] = []
    const restricted: string[] = []
    const explicitToolNames = new Set(Object.keys(form.toolContractTools).map((name) => name.trim()).filter(Boolean))
    Object.entries(form.toolContractTools).forEach(([name, config]) => {
      const toolName = name.trim()
      if (!toolName) return
      if (config.enabled) allowed.push(toolName)
      else blocked.push(toolName)
      if (splitCSV(config.bashPrefixes).length > 0) restricted.push(toolName)
    })
    toolInventoryTools(payload, form).forEach((tool) => {
      if (!explicitToolNames.has(tool.name)) {
        blocked.push(tool.name)
      }
    })
    return {
      allowed: sortedUnique(allowed),
      blocked: sortedUnique(blocked),
      restricted: sortedUnique(restricted),
      preset: form.toolContractPreset.trim(),
      inheritPolicy: form.toolContractInheritPolicy,
      catalogCount: toolInventoryTools(payload, form).length,
    }
  }

  const profile = payload.profile
  const contract = objectValue(profile, 'tool_contract')
  const scope = objectValue(profile, 'tool_scope')
  const contractTools = objectValue(contract, 'tools')
  const allowed: string[] = []
  const blocked: string[] = []
  const restricted: string[] = []

  Object.entries(contractTools).forEach(([name, rawConfig]) => {
    const toolName = name.trim()
    if (!toolName) return
    const config = rawConfig && typeof rawConfig === 'object' && !Array.isArray(rawConfig) ? rawConfig as Record<string, unknown> : {}
    if (stringArrayValue(config, 'bash_prefixes').length > 0) restricted.push(toolName)
    if ('enabled' in config) {
      if (boolValue(config, 'enabled', true)) allowed.push(toolName)
      else blocked.push(toolName)
    }
  })

  if (allowed.length === 0 && blocked.length === 0) {
    allowed.push(...stringArrayValue(scope, 'allow_tools'))
    blocked.push(...stringArrayValue(scope, 'deny_tools'))
    if (stringArrayValue(scope, 'bash_prefixes').length > 0) restricted.push('bash')
  }

  return {
    allowed: sortedUnique(allowed),
    blocked: sortedUnique(blocked),
    restricted: sortedUnique(restricted),
    preset: stringValue(contract, 'preset') || stringValue(scope, 'preset'),
    inheritPolicy: boolValue(contract, 'inherit_policy', boolValue(scope, 'inherit_policy', false)),
    catalogCount: payload.toolInventory.tools.length,
  }
}

function agentChangeCompactSummary(payload: ReturnType<typeof parseAgentChangePermission>): string {
  if (payload.target !== 'agent_profile') {
    return payload.summary
  }
  const action = payload.action.trim().toLowerCase()
  const actionLabel = action === 'create' ? 'Create' : action === 'update' ? 'Update' : action === 'delete' ? 'Delete' : 'Review'
  const parts = [
    `${actionLabel} agent profile`,
    payload.agentName ? `@${payload.agentName}` : '',
    payload.purpose,
  ].map((value) => value.trim()).filter(Boolean)
  return parts.join(' · ')
}

function AgentToolAccessSummaryCard({
  payload,
  form,
  disabled = false,
  onToolToggle,
}: {
  payload: ReturnType<typeof parseAgentChangePermission>
  form: AgentProfileFormState | null
  disabled?: boolean
  onToolToggle?: (toolName: string, enabled: boolean) => void
}) {
  const summary = agentToolAccessSummary(payload, form)
  const hasExplicitTools = summary.allowed.length > 0 || summary.blocked.length > 0
  const policyText = [
    summary.preset ? `preset ${summary.preset}` : '',
    summary.inheritPolicy ? 'inherits policy' : '',
    summary.catalogCount > 0 ? `${summary.catalogCount} catalog tools` : '',
  ].filter(Boolean).join(' · ')

  return (
    <div className="grid gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Tool access</div>
        {policyText ? <div className="text-xs text-[var(--app-text-muted)]">{policyText}</div> : null}
      </div>
      <div className="grid gap-2">
        <ToolAccessRow
          label="Allowed"
          count={summary.allowed.length}
          items={summary.allowed}
          emptyText={hasExplicitTools ? 'No explicit allows' : 'Controlled by preset / inherited policy'}
          tone="allow"
          onItemClick={onToolToggle ? (item) => onToolToggle(item, false) : undefined}
          disabled={disabled}
          itemTitle="Click to block this tool"
        />
        <ToolAccessRow
          label="Not allowed"
          count={summary.blocked.length}
          items={summary.blocked}
          emptyText={hasExplicitTools ? 'None blocked' : 'No explicit blocks'}
          tone="block"
          onItemClick={onToolToggle ? (item) => onToolToggle(item, true) : undefined}
          disabled={disabled}
          itemTitle="Click to allow this tool"
        />
      </div>
      {summary.restricted.length > 0 ? (
        <div className="text-xs text-[var(--app-warning)]">Restricted prefixes: {summary.restricted.join(', ')}</div>
      ) : null}
    </div>
  )
}

function ToolAccessRow({
  label,
  count,
  items,
  emptyText,
  tone,
  onItemClick,
  disabled = false,
  itemTitle,
}: {
  label: string
  count: number
  items: string[]
  emptyText: string
  tone: 'allow' | 'block'
  onItemClick?: (item: string) => void
  disabled?: boolean
  itemTitle?: string
}) {
  const toneClassName = tone === 'allow'
    ? 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
    : 'border-[color-mix(in_srgb,var(--app-error)_45%,var(--app-border))] bg-[color-mix(in_srgb,var(--app-error)_10%,transparent)] text-[var(--app-error)]'
  return (
    <div className="grid gap-1.5 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 sm:grid-cols-[8rem_1fr] sm:items-start">
      <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
        <span>{label}</span>
        <span className="rounded-md border border-[var(--app-border)] px-1.5 py-0.5 text-[10px] leading-none text-[var(--app-text-muted)]">{count}</span>
      </div>
      {items.length > 0 ? (
        <div className="flex min-w-0 flex-wrap gap-1.5">
          {items.map((item) => onItemClick ? (
            <button
              key={item}
              type="button"
              onClick={() => onItemClick(item)}
              disabled={disabled}
              title={itemTitle}
              className={cn('rounded-md border px-2 py-0.5 text-left text-xs leading-5', toneClassName, !disabled && 'hover:border-[var(--app-primary)]')}
            >
              {item}
            </button>
          ) : (
            <span key={item} className={cn('rounded-md border px-2 py-0.5 text-xs leading-5', toneClassName)}>{item}</span>
          ))}
        </div>
      ) : (
        <div className="text-xs leading-5 text-[var(--app-text-muted)]">{emptyText}</div>
      )}
    </div>
  )
}

function modelProviderGroups(options: ModelOptionRecord[]): Array<[string, ModelOptionRecord[]]> {
  const groups = new Map<string, ModelOptionRecord[]>()
  for (const option of options) {
    const provider = option.provider.trim()
    const model = option.model.trim()
    if (!provider || !model) {
      continue
    }
    const list = groups.get(provider) ?? []
    if (!list.some((entry) => entry.model === model)) {
      list.push({ ...option, provider, model })
    }
    groups.set(provider, list)
  }
  return Array.from(groups.entries()).sort(([left], [right]) => left.localeCompare(right))
}

function AgentProfileApprovalForm({
  form,
  payload,
  modelOptions,
  disabled,
  onChange,
}: {
  payload: ReturnType<typeof parseAgentChangePermission>
  form: AgentProfileFormState
  modelOptions: ModelOptionRecord[]
  disabled: boolean
  onChange: (next: AgentProfileFormState) => void
}) {
  const providers = useMemo(() => {
    const groups = modelProviderGroups(modelOptions)
    if (form.provider.trim() && !groups.some(([provider]) => provider === form.provider.trim())) {
      groups.push([form.provider.trim(), []])
    }
    return groups
  }, [form.provider, modelOptions])
  const activeModels = providers.find(([provider]) => provider === form.provider)?.[1] ?? []
  const inventoryTools = toolInventoryTools(payload, form)
  const [toolsExpanded, setToolsExpanded] = useState(false)
  const setToolEnabled = (toolName: string, enabled: boolean) => {
    const current = form.toolContractTools[toolName]
    const nextTools = {
      ...form.toolContractTools,
      [toolName]: { enabled, bashPrefixes: current?.bashPrefixes ?? '' },
    }
    onChange({
      ...form,
      executionSetting: deriveExecutionSettingFromTools(nextTools),
      toolContractTools: nextTools,
    })
  }

  return (
    <div className="grid gap-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Name</span>
          <input value={form.name} onChange={(event) => onChange({ ...form, name: event.target.value })} disabled={disabled} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Mode</span>
          <div className="relative">
            <select value={form.mode} onChange={(event: ChangeEvent<HTMLSelectElement>) => onChange({ ...form, mode: event.target.value })} disabled={disabled} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
              <option value="primary">primary</option>
              <option value="subagent">subagent</option>
              <option value="background">background</option>
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Provider</span>
          <div className="relative">
            <select value={form.provider} onChange={(event: ChangeEvent<HTMLSelectElement>) => {
              const provider = event.target.value
              const firstModel = providers.find(([candidate]) => candidate === provider)?.[1]?.[0]?.model ?? ''
              onChange({ ...form, provider, model: firstModel })
            }} disabled={disabled} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
              <option value="">None / inherit default</option>
              {providers.map(([provider]) => <option key={provider} value={provider}>{provider}</option>)}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Model</span>
          <div className="relative">
            <select value={form.model} onChange={(event: ChangeEvent<HTMLSelectElement>) => onChange({ ...form, model: event.target.value })} disabled={disabled || !form.provider} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
              <option value="">Choose model</option>
              {form.model && !activeModels.some((option) => option.model === form.model) ? <option value={form.model}>{form.model}</option> : null}
              {activeModels.map((option) => <option key={`${option.provider}:${option.model}:${option.contextMode}`} value={option.model}>{option.label || option.model}</option>)}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Thinking</span>
          <div className="relative">
            <select value={form.thinking} onChange={(event: ChangeEvent<HTMLSelectElement>) => onChange({ ...form, thinking: event.target.value })} disabled={disabled} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
              {AGENT_THINKING_OPTIONS.map((option) => <option key={option.value || 'default'} value={option.value}>{option.label}</option>)}
              {form.thinking && !AGENT_THINKING_OPTIONS.some((option) => option.value === form.thinking) ? <option value={form.thinking}>{form.thinking}</option> : null}
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </label>
        <label className="grid gap-1.5 text-sm">
          <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Execution</span>
          <div className="relative">
            <select value={form.executionSetting} onChange={(event: ChangeEvent<HTMLSelectElement>) => {
              const executionSetting = normalizeEditableExecutionSetting(event.target.value)
              const nextTools = { ...form.toolContractTools }
              Object.entries(nextTools).forEach(([name, config]) => {
                if (AGENT_WRITE_TOOL_NAMES.has(name.trim().toLowerCase()) && config.enabled && executionSetting === 'read') {
                  nextTools[name] = { ...config, enabled: false, bashPrefixes: '' }
                }
              })
              if (executionSetting === 'readwrite') {
                AGENT_DEFAULT_READWRITE_TOOL_NAMES.forEach((toolName) => {
                  const current = nextTools[toolName]
                  nextTools[toolName] = { enabled: true, bashPrefixes: current?.bashPrefixes ?? '' }
                })
              }
              onChange({ ...form, executionSetting: deriveExecutionSettingFromTools(nextTools), toolContractTools: nextTools })
            }} disabled={disabled || form.exitPlanModeEnabled} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
              <option value="readwrite">readwrite</option>
              <option value="read">read</option>
            </select>
            <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
          </div>
        </label>
      </div>
      <label className="grid gap-1.5 text-sm">
        <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Description</span>
        <input value={form.description} onChange={(event) => onChange({ ...form, description: event.target.value })} disabled={disabled} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
      </label>
      <label className="grid gap-1.5 text-sm">
        <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Prompt</span>
        <Textarea value={form.prompt} onChange={(event) => onChange({ ...form, prompt: event.target.value })} disabled={disabled} rows={9} className="min-h-[14rem] bg-[var(--app-bg-alt)] font-mono text-sm" />
      </label>
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="flex items-center gap-2 text-sm text-[var(--app-text)]">
          <input type="checkbox" checked={form.enabled} onChange={(event) => onChange({ ...form, enabled: event.target.checked })} disabled={disabled} />
          Enabled
        </label>
        <label className="flex items-center gap-2 text-sm text-[var(--app-text)]">
          <input type="checkbox" checked={form.exitPlanModeEnabled} onChange={(event) => onChange({ ...form, exitPlanModeEnabled: event.target.checked })} disabled={disabled} />
          Plan → auto runtime
        </label>
      </div>
      <div className="grid gap-2 border-t border-[var(--app-border)] pt-3">
        <div className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)]">
          <div className="flex flex-wrap items-center justify-between gap-3 px-3 py-2">
            <div className="flex min-w-0 items-center gap-2">
              <button
                type="button"
                onClick={() => setToolsExpanded((expanded) => !expanded)}
                className="flex items-center gap-2 text-left text-sm font-medium text-[var(--app-text)]"
                disabled={disabled}
                aria-expanded={toolsExpanded}
              >
                <ChevronDown size={15} className={cn('text-[var(--app-text-muted)] transition-transform', toolsExpanded ? 'rotate-180' : '-rotate-90')} />
                Tool descriptions
              </button>
              <span className="rounded-md border border-[var(--app-border)] px-1.5 py-0.5 text-[10px] leading-none text-[var(--app-text-muted)]">{inventoryTools.length || 'No'} tools</span>
            </div>
            <button type="button" onClick={() => setToolsExpanded((expanded) => !expanded)} disabled={disabled} className="rounded-md border border-[var(--app-border)] px-2.5 py-1.5 text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]">
              {toolsExpanded ? 'Hide descriptions' : 'Read what tools do'}
            </button>
          </div>
          {!toolsExpanded ? (
            <div className="border-t border-[var(--app-border)] px-3 py-2 text-xs text-[var(--app-text-muted)]">Advanced tool catalog hidden. Expand to read descriptions for each tool. Change permissions from the Tool Access summary above.</div>
          ) : null}
          {toolsExpanded ? (
            inventoryTools.length > 0 ? (
              <div className="grid max-h-56 gap-1.5 overflow-auto border-t border-[var(--app-border)] p-2 sm:grid-cols-2">
                {inventoryTools.map((tool) => {
                  const config = form.toolContractTools[tool.name]
                  const checked = config?.enabled ?? false
                  const bashPrefixes = config?.bashPrefixes ?? ''
                  return (
                    <div key={`${tool.kind}:${tool.name}`} className="grid gap-1 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] p-2">
                      <label className="flex items-center gap-2 text-sm text-[var(--app-text)]">
                        <input type="checkbox" checked={checked} onChange={(event) => setToolEnabled(tool.name, event.target.checked)} disabled={disabled} />
                        <span className="font-medium">{tool.name}</span>
                        <span className="text-xs text-[var(--app-text-muted)]">{tool.group}</span>
                      </label>
                      {tool.description ? <div className="text-xs leading-5 text-[var(--app-text-muted)]">{tool.description}</div> : null}
                      {tool.name === 'bash' || bashPrefixes ? (
                        <input value={bashPrefixes} onChange={(event) => {
                          const nextTools = { ...form.toolContractTools }
                          nextTools[tool.name] = { enabled: checked || event.target.value.trim() !== '', bashPrefixes: event.target.value }
                          onChange({ ...form, executionSetting: deriveExecutionSettingFromTools(nextTools), toolContractTools: nextTools })
                        }} disabled={disabled} placeholder="bash prefixes, comma-separated" className="rounded-md border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 py-1 text-xs text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]" />
                      ) : null}
                    </div>
                  )
                })}
              </div>
            ) : (
              <div className="border-t border-[var(--app-border)] px-3 py-2 text-sm text-[var(--app-text-muted)]">No backend tool inventory was included with this approval payload.</div>
            )
          ) : null}
        </div>
      </div>
    </div>
  )
}

function AgentChangeModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onResolve,
}: DesktopPermissionModalProps) {
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<AgentProfileFormState | null>(null)
  const { data: modelOptions = [] } = useQuery(modelOptionsQueryOptions())

  const payload = permission ? parseAgentChangePermission(permission) : null
  const editableProfile = Boolean(payload && payload.target === 'agent_profile' && (payload.action === 'create' || payload.action === 'update'))

  useEffect(() => {
    if (open) {
      setLoading(false)
      setForm(payload && editableProfile ? profileFormFromPayload(payload) : null)
    }
  }, [open, permission?.id])

  if (!permission || !payload) {
    return null
  }

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      const approvedArguments = action === 'approve' && editableProfile && form
        ? approvedArgumentsFromProfileForm(payload, form)
        : undefined
      await onResolve(action, '', approvedArguments)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title}
      subtitle={payload.subtitle || 'Review this manage-agent change before it is applied'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1040px,calc(100vw-48px))]"
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-4">
        <div className="grid gap-3 border-b border-[var(--app-border)] pb-3">
          <p className="text-sm leading-6 text-[var(--app-text)]">{agentChangeCompactSummary(payload)}</p>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--app-text-muted)]">
            <span>agent {payload.agentName ? `@${payload.agentName}` : 'n/a'}</span>
            <span>mode {payload.mode || 'n/a'}</span>
            <span>execution {editableProfile && form ? (form.exitPlanModeEnabled ? 'plan → auto' : form.executionSetting) : (payload.effectiveExecution || payload.execution || 'n/a')}</span>
          </div>
          {payload.target === 'agent_profile' ? <AgentToolAccessSummaryCard payload={payload} form={form} disabled={loading} onToolToggle={editableProfile && form ? (toolName, enabled) => {
            const current = form.toolContractTools[toolName]
            const nextTools = {
              ...form.toolContractTools,
              [toolName]: { enabled, bashPrefixes: current?.bashPrefixes ?? '' },
            }
            setForm({
              ...form,
              executionSetting: deriveExecutionSettingFromTools(nextTools),
              toolContractTools: nextTools,
            })
          } : undefined} /> : null}
        </div>

        {editableProfile && form ? (
          <AgentProfileApprovalForm payload={payload} form={form} modelOptions={modelOptions} disabled={loading} onChange={setForm} />
        ) : null}

      </div>
      <PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} approveLabel="Apply change" />
    </ModalShell>
  )
}

function genericPermissionSupportsPersistentActions(permission: DesktopPermissionRecord): boolean {
  const toolName = permissionDisplayToolName(permission.toolName)
  return toolName !== 'ask-user' && toolName !== 'exit_plan_mode'
}

interface PermissionExplainResponse {
  explain?: {
    rule_preview?: string
  }
}

function bashPersistentPrefixFromRulePreview(preview: string): string {
  const trimmed = preview.trim()
  const match = /^(?:allow|deny)\s+bash(?:\s+command)?\s+prefix:\s*(.+)$/i.exec(trimmed)
  return match?.[1]?.trim() || trimmed
}

async function permissionPersistentRulePreview(permission: DesktopPermissionRecord, sessionMode: string): Promise<string> {
  const params = new URLSearchParams()
  params.set('mode', (permission.mode || sessionMode).trim())
  params.set('tool', permission.toolName.trim())
  params.set('arguments', permission.toolArguments.trim())
  const response = await requestJson<PermissionExplainResponse>(`/v1/permissions/explain?${params.toString()}`)
  const preview = response.explain?.rule_preview?.trim() || ''
  if (preview && permissionDisplayToolName(permission.toolName) === 'bash') {
    return bashPersistentPrefixFromRulePreview(preview)
  }
  return preview || genericPermissionPersistentRulePreview(permission)
}

function genericPermissionPersistentRulePreview(permission: DesktopPermissionRecord): string {
  const savedRule = permission.savedRule
  if (savedRule?.kind?.trim().toLowerCase() === 'bash_prefix') {
    return (savedRule.pattern || '').trim()
  }
  if (savedRule?.kind?.trim().toLowerCase() === 'tool') {
    const decision = savedRule.decision?.trim() || 'allow'
    const tool = savedRule.tool?.trim() || permissionDisplayToolName(permission.toolName)
    return `${decision} tool: ${tool}`
  }
  if (savedRule?.kind?.trim().toLowerCase() === 'phrase') {
    const decision = savedRule.decision?.trim() || 'allow'
    return `${decision} phrase: ${(savedRule.pattern || '').trim()}`
  }
  const toolName = permissionDisplayToolName(permission.toolName)
  if (toolName === 'bash') {
    return ''
  }
  return `allow tool: ${toolName}`
}

export function DesktopPermissionModal(props: DesktopPermissionModalProps) {
  const kind = props.permission ? permissionKind(props.permission) : 'generic'

  if (kind === 'workspace-scope') {
    return <WorkspaceScopeModal {...props} />
  }
  if (kind === 'exit-plan') {
    return <ExitPlanModal {...props} />
  }
  if (kind === 'plan-update') {
    return <PlanUpdateModal {...props} />
  }
  if (kind === 'manage-todos') {
    return <ManageTodosModal {...props} />
  }
  if (kind === 'ask-user') {
    return <AskUserModal {...props} />
  }
  if (kind === 'task-launch') {
    return <TaskLaunchModal {...props} />
  }
  if (kind === 'agent-change') {
    return <AgentChangeModal {...props} />
  }
  return <GenericPermissionModal {...props} />
}
