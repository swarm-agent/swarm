import { useEffect, useMemo, useState } from 'react'
import { Copy, Check, AlertCircle } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { requestJson } from '../../../../app/api'
import { ChatMarkdown } from '../../chat/components/chat-markdown'
import type { DesktopPermissionRecord } from '../../types/realtime'
import {
  buildAskUserResolutionReason,
  buildWorkspaceScopeResolutionReason,
  buildGenericPermissionMarkdown,
  parseAgentChangePermission,
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
    <Dialog role="dialog" aria-modal="true" aria-label={title} className="z-[80] p-3 sm:p-4">
      <DialogBackdrop onClick={handleRequestClose} />
      <DialogPanel className={cn('flex min-h-0 max-h-[calc(100vh-18px)] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:max-h-[calc(100vh-32px)]', widthClassName)}>
        <div className="shrink-0 flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-3">
          <div className="min-w-0 flex flex-wrap items-center gap-x-3 gap-y-1">
            <h2 className="text-base font-semibold tracking-tight text-[var(--app-text)]">{title}</h2>
            {subtitle?.trim() ? <span className="text-sm text-[var(--app-text-muted)]">{subtitle}</span> : null}
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
        <div className={cn('min-h-0 flex-1 overflow-auto px-5 py-4', bodyClassName)}>{children}</div>
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
  children,
  shortcutHint = 'Enter approves · Esc denies · Shift+Enter adds a newline',
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
  children?: React.ReactNode
  shortcutHint?: string
}) {
  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-3">
      {children ? <div className="mb-3">{children}</div> : null}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onDeny} disabled={loading} title="Esc">
          {denyLabel}
        </Button>
        {showPersistentActions && onAlwaysDeny ? (
          <Button type="button" variant="outline" onClick={onAlwaysDeny} disabled={loading}>
            {alwaysDenyLabel}
          </Button>
        ) : null}
        {showPersistentActions && onAlwaysAllow ? (
          <Button type="button" variant="outline" onClick={onAlwaysAllow} disabled={loading}>
            {alwaysAllowLabel}
          </Button>
        ) : null}
        <Button type="button" variant="primary" onClick={onApprove} disabled={loading} title="Enter">
          {approveLabel}
        </Button>
      </div>
      {shortcutHint ? <div className="mt-2 text-right text-[11px] text-[var(--app-text-subtle)]">{shortcutHint}</div> : null}
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
      widthClassName="w-[min(860px,calc(100vw-18px))] sm:w-[min(920px,calc(100vw-32px))]"
      bodyClassName="py-3"
      showSessionMeta={false}
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-3">
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2.5 text-sm">
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
            className="min-h-[72px] resize-y bg-[var(--app-bg-alt)]"
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')}>
          <Textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder="Optional note to send back with this action…"
            className="min-h-[3.5rem] resize-none bg-[var(--app-bg-alt)]"
            rows={2}
          />
        </PermissionActionBar>
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="flex h-full min-h-0 flex-col gap-4">
        <section className="flex min-h-0 flex-1 flex-col gap-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Plan</span>
            <Button type="button" variant="outline" size="sm" onClick={() => void handleCopy()}>
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} approveLabel="Approve update">
          <label className="grid gap-2">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Message to agent</span>
            <Textarea
              value={note}
              onChange={(event) => setNote(event.target.value)}
              placeholder="Optional note to send back with this action…"
              className="min-h-[3.5rem] resize-none bg-[var(--app-bg-alt)]"
              rows={2}
            />
          </label>
        </PermissionActionBar>
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1120px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')}>
          <label className="grid gap-2">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Message to agent</span>
            <Textarea
              value={note}
              onChange={(event) => setNote(event.target.value)}
              placeholder="Type a note to send back with this action…"
              className="min-h-[3.5rem] resize-none bg-[var(--app-bg-alt)]"
              rows={2}
            />
          </label>
        </PermissionActionBar>
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="flex h-full min-h-0 flex-col gap-4">
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1140px,calc(100vw-48px))]"
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void submit('approve')}
      onDenyShortcut={() => void submit('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-5">
        {payload.context.trim() ? (
          <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
            <ChatMarkdown content={payload.context} />
          </div>
        ) : null}

        <div className="grid gap-4">
          {payload.questions.map((question, index) => {
            const selected = answers[question.id] ?? ''
            const selectedIsCustom = selected === '__custom__'
            return (
              <section key={question.id} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
                <div className="mb-3">
                  <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                    {question.header || `Question ${index + 1}`}
                    {question.required ? ' · required' : ''}
                  </div>
                  <div className="mt-2 rounded-xl bg-[var(--app-bg-alt)] p-3">
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
                          'grid gap-1 rounded-2xl border px-4 py-3 text-left transition',
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
                      className="min-h-[110px] resize-y bg-[var(--app-bg-alt)]"
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1120px,calc(100vw-48px))]"
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
              className="mt-4"
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
              className="mt-4"
              onClick={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.addToWorkspace.decision))}
              disabled={loading || !payload.addToWorkspace.available}
            >
              {payload.addToWorkspace.label}
            </Button>
          </section>
        </div>
      </div>

      <div className="flex flex-wrap justify-end gap-2 border-t border-[var(--app-border)] px-6 py-4">
        <Button type="button" variant="ghost" onClick={() => void resolve('deny', '')} disabled={loading}>
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
      widthClassName="w-[min(1080px,calc(100vw-24px))] sm:w-[min(1140px,calc(100vw-48px))]"
      footer={
        <PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} approveLabel="Launch subagents">
          <label className="grid gap-2">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Message to agent</span>
            <Textarea
              value={note}
              onChange={(event) => setNote(event.target.value)}
              placeholder="Optional note to send back with this action…"
              className="min-h-[3.5rem] resize-none bg-[var(--app-bg-alt)]"
              rows={2}
            />
          </label>
        </PermissionActionBar>
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

function AgentChangeModal({
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

  const payload = parseAgentChangePermission(permission)

  const resolve = async (action: 'approve' | 'deny') => {
    setLoading(true)
    try {
      await onResolve(action, '')
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
      widthClassName="w-[min(980px,calc(100vw-24px))] sm:w-[min(1040px,calc(100vw-48px))]"
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-5">
        <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
          <p className="text-sm leading-6 text-[var(--app-text)]">{payload.summary}</p>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Agent</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{payload.agentName ? `@${payload.agentName}` : 'n/a'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Mode</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{payload.mode || 'n/a'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Execution</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{payload.execution || 'n/a'}</div>
          </section>
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Tools</div>
            <div className="mt-2 text-sm text-[var(--app-text)]">{payload.tools || 'n/a'}</div>
          </section>
        </div>

        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">What changes</div>
          <div className="mt-3 grid gap-2 text-sm text-[var(--app-text)]">
            {payload.changes.length > 0 ? (
              payload.changes.map((change) => (
                <div key={`${change.label}:${change.value}`} className="flex flex-col gap-1 sm:flex-row sm:items-start sm:gap-3">
                  <div className="min-w-[112px] text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{change.label}</div>
                  <div className="text-sm text-[var(--app-text)]">{change.value}</div>
                </div>
              ))
            ) : (
              <div className="text-sm text-[var(--app-text-muted)]">No visible settings change.</div>
            )}
          </div>
        </section>

        {(payload.status || payload.model || payload.descriptionPreview || payload.promptPreview || payload.purpose) ? (
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Details</div>
            <div className="mt-3 grid gap-2 text-sm text-[var(--app-text)]">
              {payload.purpose ? <div>Purpose: {payload.purpose}</div> : null}
              {payload.status ? <div>Status: {payload.status}</div> : null}
              {payload.model ? <div>Model: {payload.model}</div> : null}
              {payload.descriptionPreview ? <div>Description: {payload.descriptionPreview}</div> : null}
              {payload.promptPreview ? <div>Prompt: updated</div> : null}
            </div>
          </section>
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
