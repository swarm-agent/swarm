import { useEffect, useMemo, useState, type ChangeEvent, type ReactNode } from 'react'
import { Copy, Check, AlertCircle, Archive, CalendarClock, ChevronDown, Folder, GitCommit, Rocket, LockKeyhole, Server, type LucideIcon } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { Textarea } from '../../../../components/ui/textarea'
import { cn } from '../../../../lib/cn'
import { requestJson } from '../../../../app/api'
import { ChatMarkdown } from '../../chat/components/chat-markdown'
import { StructuredPlanDocumentView, normalizeStructuredPlanDocument, type StructuredPlanDocument } from '../../chat/components/structured-plan-document'
import { displayAgentName } from '../../chat/services/agent-display'
import { getToolTheme } from '../../chat/services/tool-theme'
import { AGENT_TOOL_PRESET_OPTIONS, CUSTOM_AGENT_TOOL_PRESET_ID } from '../../chat/services/agent-tool-presets'
import type { ModelOptionRecord } from '../../chat/types/chat'
import { defaultModelThinking, modelThinkingOptions } from '../../chat/services/model-options'
import { modelOptionsQueryOptions } from '../../../queries/query-options'
import { useQuery } from '@tanstack/react-query'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { safeString } from '../services/desktop-permission-normalization'
import { saveCapabilityPolicies, savePlanAcceptanceMode, type SessionDeployPolicy } from '../services/capability-policy'
import {
  buildAskUserResolutionReason,
  buildWorkspaceScopeResolutionReason,
  buildGenericPermissionMarkdown,
  buildPlanUpdateDiffPreview,
  parseAgentChangePermission,
  type AgentEffectiveExecution,
  type AgentToolInventory,
  parseAskUserPermission,
  parseExitPlanPermission,
  parseManageTodosPermission,
  parseSessionArchivePermission,
  parseSessionCommitPermission,
  parseSessionDeployPermission,
  type SessionDeployProposal,
  parsePlanUpdatePermission,
  type PlanUpdatePayload,
  type PlanAmendmentDeltaItem,
  parseTaskLaunchPermission,
  type TaskLaunchPayload,
  type TaskLaunchResolvedTools,
  parseWorkspaceScopePermission,
  permissionDisplayToolName,
  permissionKind,
  permissionRequirementLabel,
} from '../services/permission-payload'

interface DesktopPermissionModalProps {
  open: boolean
  permission: DesktopPermissionRecord | null
  pendingCount: number
  sessionMode: string
  onOpenChange: (open: boolean) => void
  onOpenPermissions?: () => void
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

function toolAccentWash(color: string, amount = 12): string {
  return `color-mix(in srgb, ${color} ${amount}%, transparent)`
}

function permissionModeLabel(mode: string, fallback = 'plan'): string {
  const trimmed = mode.trim()
  return trimmed || fallback
}

function ModalShell({
  open,
  title,
  subtitle,
  pendingCount,
  sessionMode,
  widthClassName,
  bodyClassName,
  headerExtra,
  headerActions,
  footer,
  planStyle = false,
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
  headerExtra?: React.ReactNode
  headerActions?: React.ReactNode
  footer?: React.ReactNode
  planStyle?: boolean
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
      className={planStyle ? 'z-[80] p-3 sm:p-6' : 'z-[80] overflow-hidden p-1.5 pt-[calc(var(--app-safe-area-top)_+_0.375rem)] pb-[calc(var(--app-safe-area-bottom)_+_0.375rem)] sm:p-4'}
    >
      <DialogBackdrop onClick={handleRequestClose} />
      <DialogPanel
        className={cn(
          planStyle
            ? 'flex min-h-0 max-h-[min(900px,calc(100vh-48px))] flex-col overflow-hidden rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)]'
            : 'flex min-h-0 max-h-[calc(100dvh_-_var(--app-safe-area-top)_-_var(--app-safe-area-bottom)_-_12px)] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:max-h-[calc(100dvh-32px)]',
          widthClassName,
        )}
      >
        <div className={planStyle ? 'grid shrink-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-4 border-b border-[var(--app-border)] px-6 py-4' : 'shrink-0 flex items-start justify-between gap-3 border-b border-[var(--app-border)] px-3 py-4 sm:px-5 sm:py-5'}>
          <div className="min-w-0 flex flex-col gap-1.5">
            <h2 className={planStyle ? 'truncate text-xl font-semibold tracking-tight text-[var(--app-text)]' : 'text-sm font-semibold tracking-tight text-[var(--app-text)] sm:text-base'}>{title}</h2>
            {subtitle?.trim() ? <span className={planStyle ? 'truncate text-sm leading-5 text-[var(--app-text-muted)]' : 'text-xs leading-5 text-[var(--app-text-muted)] sm:text-sm'}>{subtitle}</span> : null}
            {showSessionMeta ? (
              <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-subtle)]">
                <span>mode {sessionMode.trim() || 'plan'}</span>
                {pendingCount > 1 ? <span>{pendingCount} pending</span> : null}
              </div>
            ) : pendingCount > 1 ? (
              <span className="text-xs text-[var(--app-text-subtle)]">{pendingCount} pending</span>
            ) : null}
            {headerExtra ? <div className="mt-2">{headerExtra}</div> : null}
          </div>
          {headerActions ? (
            <div className="flex min-w-0 shrink-0 flex-nowrap items-center justify-end gap-2 overflow-x-auto whitespace-nowrap">
              {headerActions}
              <ModalCloseButton onClick={handleRequestClose} aria-label="Close permission modal" />
            </div>
          ) : (
            <ModalCloseButton onClick={handleRequestClose} aria-label="Close permission modal" />
          )}
        </div>
        <div className={cn(planStyle ? 'min-h-0 flex-auto overflow-y-auto overflow-x-hidden overscroll-contain px-6 py-5' : 'min-h-0 flex-auto overflow-y-auto overflow-x-hidden overscroll-contain px-3 py-3 sm:px-5 sm:py-4', bodyClassName)}>{children}</div>
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
  compactDesktop = false,
  denyVariant = 'ghost',
  leadingAction,
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
  compactDesktop?: boolean
  denyVariant?: 'secondary' | 'ghost' | 'outline'
  leadingAction?: ReactNode
}) {
  const [noteOpen, setNoteOpen] = useState(false)
  const showPersistentGroup = showPersistentActions && (onAlwaysDeny || onAlwaysAllow)
  const showNoteToggle = Boolean(onNoteChange)
  const hasNote = Boolean(note?.trim())

  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-3 pb-[max(0.75rem,var(--app-safe-area-bottom))] shadow-[0_-18px_36px_rgba(0,0,0,0.12)] sm:px-5 sm:py-4 sm:pb-4">
      <div
        className={cn(
          'min-w-0',
          compactDesktop && showNoteToggle ? 'sm:flex sm:items-end sm:gap-3' : null,
        )}
      >
        {showNoteToggle ? (
          <div className={cn('mb-2 min-w-0', compactDesktop ? 'sm:mb-0 sm:flex-1' : null)}>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setNoteOpen((current) => !current)}
              className="min-h-8 px-2.5 text-xs sm:hidden"
              aria-expanded={noteOpen}
            >
              {noteOpen ? 'Hide note' : hasNote ? 'Edit note' : 'Add note'}
            </Button>
            <label className={cn('mt-2 gap-1.5 sm:mt-0 sm:grid', noteOpen ? 'grid' : 'hidden')}>
              <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{noteLabel}</span>
              <Textarea
                value={note ?? ''}
                onChange={(event) => onNoteChange?.(event.target.value)}
                placeholder={notePlaceholder}
                className="min-h-11 resize-none bg-[var(--app-bg-alt)] sm:min-h-[3.25rem]"
                rows={2}
              />
            </label>
          </div>
        ) : null}
        <div
          className={cn(
            'flex touch-manipulation flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center sm:justify-end',
            showNoteToggle && !compactDesktop ? (noteOpen ? 'pt-3 sm:pt-4' : 'sm:pt-4') : null,
            compactDesktop ? 'sm:shrink-0 sm:flex-nowrap sm:self-end' : null,
          )}
        >
          <Button
            type="button"
            variant="primary"
            onClick={onApprove}
            disabled={loading}
            title="Enter"
            className="order-1 w-full sm:order-4 sm:w-auto sm:min-w-36"
          >
            {approveLabel}
          </Button>
          {leadingAction ? <div className="order-0 w-full sm:w-auto">{leadingAction}</div> : null}
          <Button
            type="button"
            variant={denyVariant}
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
      </div>
      {shortcutHint ? <div className="mt-2 text-center text-[11px] text-[var(--app-text-subtle)] sm:text-right">{shortcutHint}</div> : null}
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
  const toolName = permissionDisplayToolName(permission.toolName)
  const toolTheme = getToolTheme(toolName)
  const ToolIcon = toolTheme.icon
  const modeLabel = permissionModeLabel(permission.mode || sessionMode)
  const requirementLabel = permissionRequirementLabel(permission.requirement)
  const accentWash = toolAccentWash(toolTheme.color, 14)

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
    || (alwaysPreviewError ? `Unable to preview always-allow rule: ${alwaysPreviewError}` : 'Loading always-allow rule preview…')
  const isBashPermission = permissionDisplayToolName(permission.toolName) === 'bash'
  const persistentRuleLabel = isBashPermission ? 'Always allow prefix:' : 'Always allow rule:'
  const persistentRuleHint = isBashPermission
    ? 'Future bash commands starting with this prefix will be approved automatically.'
    : 'Future matching requests will be approved automatically.'

  return (
    <ModalShell
      open={open}
      title={`${toolName} permission`}
      subtitle="Review the requested tool call before it runs"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-[min(100%,calc(100vw-12px))] sm:w-[min(980px,calc(100vw-48px))] xl:w-[min(1040px,calc(100vw-64px))]"
      bodyClassName="px-3 py-4 sm:px-5 sm:py-5"
      headerExtra={
        <div className="flex flex-nowrap items-center gap-2 overflow-x-auto pb-0.5">
          <HeaderChip icon={ToolIcon}>{toolName}</HeaderChip>
          <HeaderChip icon={LockKeyhole}>{requirementLabel}</HeaderChip>
          <HeaderChip icon={Rocket}>mode {modeLabel}</HeaderChip>
        </div>
      }
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          onAlwaysAllow={persistentAllowed ? () => void resolve('approve_always') : undefined}
          onAlwaysDeny={persistentAllowed ? () => void resolve('always_deny') : undefined}
          showPersistentActions={persistentAllowed}
          note={note}
          onNoteChange={setNote}
          noteLabel="Response note"
          notePlaceholder="Optional note…"
        />
      }
      showSessionMeta={false}
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid min-w-0 gap-4">
        <section className="min-w-0 overflow-hidden rounded-2xl border border-[color-mix(in_oklab,var(--app-border)_78%,transparent)] bg-[linear-gradient(135deg,color-mix(in_oklab,var(--app-surface)_96%,var(--app-bg-alt)),color-mix(in_oklab,var(--app-bg-alt)_92%,transparent))] shadow-[0_18px_42px_rgba(0,0,0,0.10)]">
          <div className="flex min-w-0 items-center gap-3 border-b border-[var(--app-border)] bg-[color-mix(in_oklab,var(--app-surface)_86%,transparent)] px-3 py-3 sm:px-4">
            <span
              className="flex size-9 shrink-0 items-center justify-center rounded-xl"
              style={{ color: toolTheme.color, backgroundColor: accentWash }}
            >
              <ToolIcon className="size-4" aria-hidden="true" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-semibold text-[var(--app-text)]">{toolName}</div>
              <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-[var(--app-text-subtle)]">
                <span>{requirementLabel}</span>
                <span aria-hidden="true">·</span>
                <span>mode {modeLabel}</span>
              </div>
            </div>
          </div>
          <div className="max-h-[min(54dvh,34rem)] min-w-0 overflow-y-auto overscroll-contain px-3 py-3 sm:max-h-[min(58dvh,38rem)] sm:px-4 sm:py-4">
            <ChatMarkdown
              content={body}
              className="text-sm leading-6 [&_pre]:border-[color-mix(in_oklab,var(--app-border)_70%,transparent)] [&_pre]:bg-[color-mix(in_oklab,var(--app-bg-inset)_88%,black)] [&_pre]:shadow-inner [&_pre_code]:text-[13px]"
            />
          </div>
        </section>
        {persistentAllowed ? (
          <div className="grid gap-1.5 rounded-xl border border-[color-mix(in_oklab,var(--app-primary)_24%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-primary)_7%,var(--app-surface-subtle))] px-3 py-2.5 text-xs text-[var(--app-text-muted)]">
            <div className="flex min-w-0 flex-wrap items-baseline gap-2">
              <span className="font-medium text-[var(--app-text-subtle)]">{persistentRuleLabel}</span>
              <span className="min-w-0 flex-1 whitespace-pre-wrap break-words font-mono text-[var(--app-text)] [overflow-wrap:anywhere]">{persistentRuleDescription || 'available after approval'}</span>
            </div>
            <div className="text-[var(--app-text-subtle)]">{persistentRuleHint}</div>
          </div>
        ) : null}
      </div>
    </ModalShell>
  )
}


function ExitPlanDocumentView({ document }: { document: StructuredPlanDocument }) {
  return <StructuredPlanDocumentView document={document} review />
}

export function exitPlanExecutionArguments(): {
  execution_granularity: 'checkpointed'
  continue_automatically: true
  continuation_policy: 'automatic'
} {
  return {
    execution_granularity: 'checkpointed',
    continue_automatically: true,
    continuation_policy: 'automatic',
  }
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
  }, [open, permission])

  if (!permission) {
    return null
  }

  const payload = parseExitPlanPermission(permission)
  const structuredDocument = normalizeStructuredPlanDocument(payload.document)
  const hasStructuredPlan = Boolean(structuredDocument)
  const handleCopy = async () => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(structuredDocument ? JSON.stringify(payload.document, null, 2) : payload.body)
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }

  const resolve = async (action: 'approve' | 'deny' | 'approve_always') => {
    setLoading(true)
    try {
      if (action === 'approve_always') await savePlanAcceptanceMode('always_allow')
      const approvedArguments = action !== 'deny'
        ? {
            ...payload.approvedArguments,
            plan_id: payload.planId || structuredDocument?.id || payload.approvedArguments.plan_id,
            title: payload.title || structuredDocument?.title || payload.approvedArguments.title,
            plan: payload.body,
            document: payload.document ?? payload.approvedArguments.document,
            ...exitPlanExecutionArguments(),
          }
        : undefined
      await onResolve(action === 'approve_always' ? 'approve' : action, note.trim(), approvedArguments)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Exit Plan Mode'}
      subtitle={structuredDocument ? undefined : 'Approve this request to leave plan mode and continue execution'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-[min(1180px,calc(100vw-24px))] sm:w-[min(1280px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      showSessionMeta={false}
      planStyle
      headerActions={
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
      }
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          onAlwaysAllow={() => void resolve('approve_always')}
          showPersistentActions
          alwaysAllowLabel="Always allow plan acceptance"
          note={note}
          onNoteChange={setNote}
          leadingAction={hasStructuredPlan ? (
            <span className="text-sm text-[var(--app-text-muted)]">Starts automatically after approval</span>
          ) : undefined}
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-4">
        {structuredDocument ? (
          <ExitPlanDocumentView document={structuredDocument} />
        ) : (
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
            <ChatMarkdown content={payload.body} className="text-base leading-7" />
          </section>
        )}
      </div>
    </ModalShell>
  )
}

function countWords(text: string): number {
  return text.trim().split(/\s+/).filter(Boolean).length
}

function promptWordPreview(text: string, maxWords: number): string {
  const words = text.trim().split(/\s+/).filter(Boolean)
  if (words.length === 0) {
    return ''
  }
  const limit = Math.max(1, maxWords)
  if (words.length <= limit) {
    return words.join(' ')
  }
  return `${words.slice(0, limit).join(' ')}…`
}

function PlanTextPanel({ title, text, emptyText, tone }: { title: string; text: string; emptyText: string; tone: 'previous' | 'updated' }) {
  const hasText = text.trim() !== ''
  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)]">
      <div className="shrink-0 border-b border-[var(--app-border)] px-3 py-2 sm:px-4">
        <div
          className={cn(
            'text-xs font-semibold uppercase tracking-[0.08em]',
            tone === 'previous' ? 'text-[var(--app-warning)]' : 'text-[var(--app-success)]',
          )}
        >
          {title}
        </div>
      </div>
      <div className="min-h-[360px] flex-1 overflow-auto bg-[var(--app-bg-alt)] px-3 py-3 text-[var(--app-text)] sm:px-4">
        {hasText ? (
          <ChatMarkdown content={text} className="text-base leading-7 [overflow-wrap:anywhere]" />
        ) : (
          <div className="text-sm text-[var(--app-text-muted)]">{emptyText}</div>
        )}
      </div>
    </section>
  )
}

function PlanUpdateFullDiff({ diffLines, priorPlan, plan }: { diffLines: string[]; priorPlan: string; plan: string }) {
  const preview = buildPlanUpdateDiffPreview(diffLines, priorPlan, plan)
  const hasRows = preview.rows.length > 0

  return (
    <section className="min-h-0 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 sm:p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Complete line diff</div>
          <div className="mt-1 text-sm text-[var(--app-text-muted)]">All diff rows are shown in order. Nothing is auto-collapsed or hidden.</div>
        </div>
        <div className="flex flex-wrap gap-2 text-[11px] font-medium">
          <span className="rounded-full border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-2 py-0.5 text-[var(--app-success)]">+{preview.addedCount}</span>
          <span className="rounded-full border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-2 py-0.5 text-[var(--app-danger)]">-{preview.removedCount}</span>
          <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 py-0.5 text-[var(--app-text-muted)]">{preview.totalRows} rows</span>
        </div>
      </div>
      <div className="mt-3 max-h-[34dvh] overflow-auto rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] font-mono text-[12px] leading-5 sm:text-[13px]">
        {hasRows ? (
          <div className="divide-y divide-[var(--app-border)]/50">
            {preview.rows.map((row, index) => {
              if (row.kind === 'gap') {
                return (
                  <div key={`gap:${index}:${row.omittedCount ?? 0}`} className="px-3 py-1.5 text-center text-[11px] uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
                    {row.text}
                  </div>
                )
              }
              const prefix = row.kind === 'added' ? '+' : row.kind === 'removed' ? '−' : ' '
              return (
                <div
                  key={`${row.kind}:${index}:${row.text}`}
                  className={cn(
                    'grid grid-cols-[3.25rem_3.25rem_1.25rem_minmax(0,1fr)] gap-2 px-3 py-1.5',
                    row.kind === 'added' && 'bg-[color-mix(in_srgb,var(--app-success)_12%,transparent)] text-[var(--app-text)]',
                    row.kind === 'removed' && 'bg-[color-mix(in_srgb,var(--app-danger)_12%,transparent)] text-[var(--app-text)]',
                    row.kind === 'context' && 'text-[var(--app-text-muted)]',
                  )}
                >
                  <span className="select-none text-right text-[var(--app-text-subtle)]">{row.lineNumberBefore ?? ''}</span>
                  <span className="select-none text-right text-[var(--app-text-subtle)]">{row.lineNumberAfter ?? ''}</span>
                  <span className={cn('select-none font-semibold', row.kind === 'added' && 'text-[var(--app-success)]', row.kind === 'removed' && 'text-[var(--app-danger)]')}>
                    {prefix}
                  </span>
                  <span className="min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{row.text || '\u00a0'}</span>
                </div>
              )
            })}
          </div>
        ) : (
          <div className="px-3 py-4 text-sm text-[var(--app-text-muted)]">No textual changes were provided for this plan update.</div>
        )}
      </div>
    </section>
  )
}

type PlanUpdateReviewView = 'previous' | 'updated' | 'diff'

function PlanUpdateReview({
  diffLines,
  priorPlan,
  plan,
  priorTitle,
  updateSummary,
  updateScope,
  updateKind,
  checkpoint,
  document,
  priorDocument,
}: {
  diffLines: string[]
  priorPlan: string
  plan: string
  priorTitle: string
  updateSummary: string
  updateScope: string
  updateKind: string
  checkpoint: boolean
  document: unknown
  priorDocument: unknown
}) {
  const [selectedView, setSelectedView] = useState<PlanUpdateReviewView>('updated')
  const [copyState, setCopyState] = useState<'idle' | 'copied' | 'error'>('idle')
  const hasOverview =
    updateSummary.trim() !== '' || updateScope.trim() !== '' || updateKind.trim() !== '' || checkpoint
  const structuredDocument = normalizeStructuredPlanDocument(document)
  const structuredPriorDocument = normalizeStructuredPlanDocument(priorDocument)
  const viewOptions: Array<{ id: PlanUpdateReviewView; label: string }> = [
    { id: 'updated', label: structuredDocument ? 'Updated document' : 'Updated plan' },
    { id: 'previous', label: structuredPriorDocument ? 'Previous document' : 'Previous plan' },
    { id: 'diff', label: 'Diff' },
  ]
  const copyText = selectedView === 'previous'
    ? (structuredPriorDocument ? JSON.stringify(priorDocument, null, 2) : priorPlan)
    : selectedView === 'diff'
      ? diffLines.join('\n')
      : (structuredDocument ? JSON.stringify(document, null, 2) : plan)

  useEffect(() => {
    setCopyState('idle')
  }, [selectedView, copyText])

  const handleCopy = async () => {
    try {
      if (!copyText.trim() || typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(copyText)
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }

  return (
    <div className="flex min-h-0 flex-col gap-4">
      <section className="rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-3 text-sm leading-6 text-[var(--app-text)] sm:px-4">
        {hasOverview ? (
          <div>
            <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">
              Plan update overview
            </div>
            {updateSummary.trim() !== '' ? (
              <p className="mt-2 whitespace-pre-wrap break-words text-[var(--app-text)]">{updateSummary}</p>
            ) : null}
            <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--app-text-muted)]">
              {updateScope.trim() !== '' ? (
                <span>
                  <span className="text-[var(--app-text-subtle)]">Scope</span> {updateScope}
                </span>
              ) : null}
              {updateKind.trim() !== '' ? (
                <span>
                  <span className="text-[var(--app-text-subtle)]">Kind</span> {updateKind}
                </span>
              ) : null}
              {checkpoint ? <span className="text-[var(--app-success)]">Checkpoint</span> : null}
            </div>
          </div>
        ) : null}
        <div className={cn('flex flex-wrap items-center justify-between gap-2', hasOverview && 'mt-3')}>
          <div className="flex flex-wrap gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-1">
            {viewOptions.map((option) => (
              <button
                key={option.id}
                type="button"
                className={cn(
                  'rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
                  selectedView === option.id
                    ? 'bg-[var(--app-bg)] text-[var(--app-text)] shadow-sm'
                    : 'text-[var(--app-text-muted)] hover:bg-[var(--app-bg-alt)] hover:text-[var(--app-text)]',
                )}
                onClick={() => setSelectedView(option.id)}
              >
                {option.label}
              </button>
            ))}
          </div>
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
      </section>
      {selectedView === 'previous' ? (
        structuredPriorDocument ? (
          <StructuredPlanDocumentView document={structuredPriorDocument} emptyText="No previous structured plan document was provided." />
        ) : (
          <PlanTextPanel
            title={priorTitle ? `Previous plan: ${priorTitle}` : 'Previous plan'}
            text={priorPlan}
            emptyText="No previous plan text was provided."
            tone="previous"
          />
        )
      ) : selectedView === 'diff' ? (
        <PlanUpdateFullDiff diffLines={diffLines} priorPlan={priorPlan} plan={plan} />
      ) : structuredDocument ? (
        <StructuredPlanDocumentView document={structuredDocument} emptyText="No updated structured plan document was provided." />
      ) : (
        <PlanTextPanel title="Updated plan" text={plan} emptyText="No updated plan text was provided." tone="updated" />
      )}
    </div>
  )
}

export function planLifecycleApprovedArguments(payload: PlanUpdatePayload, fallbackAction: string): Record<string, unknown> {
  if (Object.keys(payload.approvedArguments).length > 0) {
    return payload.approvedArguments
  }
  const approved: Record<string, unknown> = {
    action: payload.action || fallbackAction,
  }
  if (payload.planId) approved.plan_id = payload.planId
  if (payload.title) approved.title = payload.title
  if (payload.plan) approved.plan = payload.plan
  if (payload.document) approved.document = payload.document
  if (payload.changeRequest) approved.change_request = payload.changeRequest
  if (payload.checkpointTitle) approved.checkpoint_title = payload.checkpointTitle
  if (payload.tasks.length > 0) approved.tasks = payload.tasks
  if (payload.acceptanceCriteria.length > 0) approved.acceptance_criteria = payload.acceptanceCriteria
  if (payload.notes) approved.notes = payload.notes
  if (payload.followupCheckpointPolicy) approved.followup_checkpoint_policy = payload.followupCheckpointPolicy
  if (
    payload.action === 'request_followup_checkpoint' ||
    fallbackAction === 'request_followup_checkpoint' ||
    payload.action === 'request_new_plan' ||
    fallbackAction === 'request_new_plan'
  ) approved.approval_confirmed = true
  return approved
}

export function newPlanLifecycleApprovedArguments(
  payload: PlanUpdatePayload,
): Record<string, unknown> {
  return {
    ...planLifecycleApprovedArguments(payload, 'request_new_plan'),
    approval_confirmed: true,
    ...exitPlanExecutionArguments(),
  }
}

function followupPolicyLabel(policy: string): string {
  switch (policy.trim().toLowerCase()) {
    case 'auto_start':
      return 'Auto-add & start session checkpoint'
    case 'require_approval':
      return 'Ask before adding session checkpoint'
    default:
      return policy.trim() || 'Ask before adding session checkpoint'
  }
}

function followupApproveLabel(policy: string): string {
  switch (policy.trim().toLowerCase()) {
    case 'auto_start':
      return 'Add & start session checkpoint'
    default:
      return 'Approve session checkpoint'
  }
}

function planLifecycleDocumentPreview(payload: PlanUpdatePayload, emptyText: string) {
  const structuredDocument = normalizeStructuredPlanDocument(payload.document)
  if (structuredDocument) {
    return <ExitPlanDocumentView document={structuredDocument} />
  }
  return (
    <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
      {payload.plan.trim() ? (
        <ChatMarkdown content={payload.plan} className="text-base leading-7" />
      ) : (
        <div className="text-sm text-[var(--app-text-muted)]">{emptyText}</div>
      )}
    </section>
  )
}

function PlanFollowupRequestModal({
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

  if (!permission) return null

  const payload = parsePlanUpdatePermission(permission)
  const effectivePolicy = payload.policyEffective || 'require_approval'
  const checkpointTitle = payload.checkpointTitle || payload.title || `Session checkpoint: ${promptWordPreview(payload.changeRequest, 10) || 'New checkpoint'}`
  const tasks = payload.tasks.length > 0 ? payload.tasks : payload.changeRequest ? [payload.changeRequest] : []
  const resolve = async (action: 'approve' | 'deny' | 'approve_always') => {
    setLoading(true)
    try {
      if (action === 'approve_always') await savePlanAcceptanceMode('always_allow')
      await onResolve(
        action === 'approve_always' ? 'approve' : action,
        note.trim(),
        action !== 'deny' ? planLifecycleApprovedArguments(payload, 'request_followup_checkpoint') : undefined,
      )
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Review Session Checkpoint'}
      subtitle={payload.planId ? `Plan ${payload.planId}` : 'Add one ordered checkpoint to the active session'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(980px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          onAlwaysAllow={() => void resolve('approve_always')}
          showPersistentActions
          alwaysAllowLabel="Always allow plan acceptance"
          approveLabel={followupApproveLabel(effectivePolicy)}
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
      <div className="grid gap-4">
        <section className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-4">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">User request</div>
          <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-6 text-[var(--app-text)]">{payload.changeRequest || 'No change request text was provided.'}</p>
        </section>
        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-4">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Checkpoint preview</div>
          <div className="mt-2 text-base font-semibold text-[var(--app-text)]">{checkpointTitle}</div>
          {tasks.length > 0 ? (
            <ul className="mt-3 grid gap-2 text-sm leading-6 text-[var(--app-text)]">
              {tasks.map((task) => <li key={task} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2">{task}</li>)}
            </ul>
          ) : null}
          {payload.acceptanceCriteria.length > 0 ? (
            <div className="mt-3">
              <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Acceptance criteria</div>
              <ul className="mt-2 list-disc space-y-1 pl-5 text-sm leading-6 text-[var(--app-text-muted)]">
                {payload.acceptanceCriteria.map((criterion) => <li key={criterion}>{criterion}</li>)}
              </ul>
            </div>
          ) : null}
          {payload.notes ? (
            <div className="mt-3">
              <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Handoff context</div>
              <p className="mt-2 whitespace-pre-wrap break-words rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 text-sm leading-6 text-[var(--app-text-muted)]">{payload.notes}</p>
            </div>
          ) : null}
        </section>
        <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4 text-sm leading-6">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Effective session checkpoint policy</div>
          <div className="mt-2 font-medium text-[var(--app-text)]">{followupPolicyLabel(effectivePolicy)}</div>
          <p className="mt-1 text-[var(--app-text-muted)]">Session checkpoint requests add one ordered checkpoint to the active session chain. They preserve lifecycle states for completed, review, blocked, and failed checkpoints and do not imply a single related thread of work.</p>
        </section>
      </div>
    </ModalShell>
  )
}

function checkpointDeltaDisplay(item: PlanAmendmentDeltaItem): string {
  const id = item.id.trim()
  const title = item.title.trim()
  if (id && title) return `${id} — ${title}`
  return id || title || 'checkpoint'
}

function PlanAmendmentDeltaPreview({ payload }: { payload: PlanUpdatePayload }) {
  const delta = payload.planAmendmentDelta
  const bullets = delta?.bullets ?? []
  const preserved = delta?.preservedCheckpoints ?? []
  const replaced = delta?.replacedCheckpoints ?? []
  const replacements = delta?.replacementCheckpoints ?? []
  return (
    <section className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-4 text-sm leading-6 text-[var(--app-text)]">
      <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Amendment delta</div>
      {bullets.length > 0 ? (
        <ul className="mt-3 list-disc space-y-1 pl-5">
          {bullets.map((bullet) => <li key={bullet}>{bullet}</li>)}
        </ul>
      ) : (
        <p className="mt-2 text-[var(--app-text-muted)]">Review the amendment control fields and structured document before approving.</p>
      )}
      <div className="mt-3 grid gap-3 md:grid-cols-3">
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Preserved</div>
          {preserved.length > 0 ? preserved.map((item) => <div key={`${item.id}:${item.title}`} className="mt-2 text-sm">{checkpointDeltaDisplay(item)} <span className="text-[var(--app-text-muted)]">{item.status}</span></div>) : <div className="mt-2 text-sm text-[var(--app-text-muted)]">No earlier checkpoints listed.</div>}
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Replaced from {delta?.replaceFromCheckpointId || 'future checkpoint'}</div>
          {replaced.length > 0 ? replaced.map((item) => <div key={`${item.id}:${item.title}`} className="mt-2 text-sm">{checkpointDeltaDisplay(item)} <span className="text-[var(--app-text-muted)]">{item.status}</span></div>) : <div className="mt-2 text-sm text-[var(--app-text-muted)]">Replacement start is recorded in the approved arguments.</div>}
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3">
          <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">New future work</div>
          {replacements.length > 0 ? replacements.map((item) => <div key={`${item.id}:${item.title}`} className="mt-2 text-sm">{checkpointDeltaDisplay(item)} <span className="text-[var(--app-text-muted)]">{item.status}</span></div>) : <div className="mt-2 text-sm text-[var(--app-text-muted)]">Open the full document preview for replacement details.</div>}
        </div>
      </div>
      <div className="mt-3 flex flex-wrap gap-2 text-xs text-[var(--app-text-muted)]">
        {delta?.baseRevision || payload.baseRevision ? <span>base revision {delta?.baseRevision || payload.baseRevision}</span> : null}
        {delta?.currentRevision || payload.currentRevision ? <span>current revision {delta?.currentRevision || payload.currentRevision}</span> : null}
        {delta?.overrideStale ? <span>override stale enabled</span> : null}
      </div>
    </section>
  )
}

function PlanAmendmentRequestModal({
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

  if (!permission) return null
  const payload = parsePlanUpdatePermission(permission)
  const resolve = async (action: 'approve' | 'deny' | 'approve_always') => {
    setLoading(true)
    try {
      if (action === 'approve_always') await savePlanAcceptanceMode('always_allow')
      await onResolve(action === 'approve_always' ? 'approve' : action, note.trim(), action !== 'deny' ? planLifecycleApprovedArguments(payload, 'amend_plan') : undefined)
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Review Plan Amendment'}
      subtitle={payload.planId ? `Plan ${payload.planId} · future-checkpoint amendment` : 'Future-checkpoint amendment approval required'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(1180px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={<PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} onAlwaysAllow={() => void resolve('approve_always')} showPersistentActions alwaysAllowLabel="Always allow plan acceptance" approveLabel="Approve amendment" note={note} onNoteChange={setNote} noteLabel="Message to agent" />}
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-4">
        <PlanAmendmentDeltaPreview payload={payload} />
        {planLifecycleDocumentPreview(payload, 'No proposed amendment document or plan text was provided.')}
      </div>
    </ModalShell>
  )
}

function NewPlanRequestModal({
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

  if (!permission) return null
  const payload = parsePlanUpdatePermission(permission)
  const structuredDocument = normalizeStructuredPlanDocument(payload.document)
  const handleCopy = async () => {
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(structuredDocument ? JSON.stringify(payload.document, null, 2) : payload.plan)
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }
  const resolve = async (action: 'approve' | 'deny' | 'approve_always') => {
    setLoading(true)
    try {
      if (action === 'approve_always') await savePlanAcceptanceMode('always_allow')
      await onResolve(
        action === 'approve_always' ? 'approve' : action,
        note.trim(),
        action !== 'deny' ? newPlanLifecycleApprovedArguments(payload) : undefined,
      )
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title={payload.title || 'Review New Plan'}
      subtitle="Explicit approval required before approving and activating a new plan"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-[min(1180px,calc(100vw-24px))] sm:w-[min(1280px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      showSessionMeta={false}
      planStyle
      headerActions={
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
      }
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          onAlwaysAllow={() => void resolve('approve_always')}
          showPersistentActions
          alwaysAllowLabel="Always allow plan acceptance"
          approveLabel="Approve new plan"
          note={note}
          onNoteChange={setNote}
          noteLabel="Message to agent"
          leadingAction={
            <span className="text-sm text-[var(--app-text-muted)]">Starts automatically after approval</span>
          }
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-4">
        <section className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] p-4 text-sm leading-6 text-[var(--app-text)]">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Lifecycle action</div>
          <p className="mt-2">Approve, activate, and prepare this separate new plan for execution. It will replace the current active plan only after this approval.</p>
          {payload.updateSummary ? <p className="mt-2 whitespace-pre-wrap break-words text-[var(--app-text-muted)]">{payload.updateSummary}</p> : null}
        </section>
        {structuredDocument ? (
          <ExitPlanDocumentView document={structuredDocument} />
        ) : (
          <section className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-5">
            {payload.plan.trim() ? (
              <ChatMarkdown content={payload.plan} className="text-base leading-7" />
            ) : (
              <div className="text-sm text-[var(--app-text-muted)]">No proposed new plan document or plan text was provided.</div>
            )}
          </section>
        )}
      </div>
    </ModalShell>
  )
}

// Legacy generic plan update approvals are reserved for draft/non-lifecycle saves.
// Active approved-plan lifecycle changes route to the typed modals above.
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
      await onResolve(action, note.trim(), action === 'approve' ? planLifecycleApprovedArguments(payload, 'save') : undefined)
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
      widthClassName="w-full sm:w-[min(1400px,calc(100vw-48px))] xl:w-[min(1550px,calc(100vw-64px))]"
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
      <PlanUpdateReview
        diffLines={payload.diffLines}
        priorPlan={payload.priorPlan}
        plan={payload.plan}
        priorTitle={payload.priorTitle}
        updateSummary={payload.updateSummary}
        updateScope={payload.updateScope}
        updateKind={payload.updateKind}
        checkpoint={payload.checkpoint}
        document={payload.document}
        priorDocument={payload.priorDocument}
      />
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

function sessionArchiveStateLabel(state: string): string {
  return state.replace(/[_-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function sessionArchiveUpdatedLabel(updatedAt: number): string {
  if (!updatedAt) {
    return 'Update time unavailable'
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(updatedAt))
}

interface SessionDeployFormProposal extends SessionDeployProposal {
  selected: boolean
}

function sessionDeployInitialProposals(proposals: SessionDeployProposal[]): SessionDeployFormProposal[] {
  const selectedIndex = proposals.findIndex((proposal) => proposal.selected)
  return proposals.map((proposal, index) => ({
    ...proposal,
    selected: index === (selectedIndex >= 0 ? selectedIndex : 0),
  }))
}

const SESSION_DEPLOY_CONTROL_CLASS = 'block h-10 w-full min-w-0 max-w-full overflow-hidden text-ellipsis whitespace-nowrap rounded-xl border border-[var(--app-border)] px-3 text-base text-[var(--app-text)] [field-sizing:fixed] sm:text-sm'

export function alwaysAllowSessionDeployPolicy(): SessionDeployPolicy {
  return {
    mode: 'always_allow',
    automatic_deployments_per_parent_run: 0,
    over_limit_action: 'ask',
  }
}

function SessionDeployModal({
  permission,
  open,
  pendingCount,
  sessionMode,
  onOpenChange,
  onOpenPermissions,
  onResolve,
}: DesktopPermissionModalProps) {
  const payload = useMemo(() => permission ? parseSessionDeployPermission(permission) : null, [permission])
  const [proposals, setProposals] = useState<SessionDeployFormProposal[]>(() => sessionDeployInitialProposals(payload?.proposals ?? []))
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open || !payload) return
    setProposals(sessionDeployInitialProposals(payload.proposals))
    setLoading(false)
    setError('')
  }, [open, payload, permission?.id])

  if (!permission || !payload) return null

  const updateProposal = (id: string, change: Partial<SessionDeployFormProposal>) => {
    setProposals((current) => current.map((proposal) => proposal.id === id ? { ...proposal, ...change } : proposal))
    setError('')
  }
  const selectedCount = proposals.filter((proposal) => proposal.selected).length
  const submit = async (action: 'approve' | 'deny' | 'approve_always') => {
    if (action === 'deny') {
      setLoading(true)
      try { await onResolve('deny', '') } finally { setLoading(false) }
      return
    }
    const selected = proposals.filter((proposal) => proposal.selected)
    if (selected.length === 0) {
      setError('Select at least one session to deploy.')
      return
    }
    if (selected.some((proposal) => !proposal.prompt.trim() || !proposal.agentName.trim() || !proposal.workspacePath.trim())) {
      setError('Every selected session requires a prompt, agent, and workspace.')
      return
    }
    if (selected.some((proposal) => proposal.managedWorktree && !proposal.worktreeBranch.trim())) {
      setError('Managed worktree sessions require the AI-provided branch suggestion.')
      return
    }
    const approvedProposals = proposals.map((proposal) => ({
      ...proposal.manifest,
      title: proposal.title.trim(),
      prompt: proposal.prompt.trim(),
      mode: proposal.mode,
      agent_name: proposal.agentName,
      agent_mode: proposal.agentMode,
      managed_worktree: proposal.managedWorktree,
      worktree_base_branch: proposal.worktreeBaseBranch.trim(),
      worktree_branch: proposal.worktreeBranch.trim(),
      selected: proposal.selected,
    }))
    setLoading(true)
    try {
      if (action === 'approve_always') await saveCapabilityPolicies({ session_deploy: alwaysAllowSessionDeployPolicy() })
      await onResolve('approve', '', {
        ...payload.approvedArguments,
        action: 'deploy',
        manifest_version: payload.manifestVersion,
        manifest_digest: payload.manifestDigest,
        selected_proposal_ids: selected.map((proposal) => proposal.id),
        proposals: approvedProposals,
      })
    } finally {
      setLoading(false)
    }
  }

  return (
    <ModalShell
      open={open}
      title="Deploy sessions?"
      subtitle="Review and select the exact local V3 sessions to create and start"
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="min-w-0 w-full max-w-[1100px]"
      bodyClassName="min-w-0 overflow-x-hidden overflow-y-auto"
      footer={<PermissionActionBar loading={loading} onApprove={() => void submit('approve')} onDeny={() => void submit('deny')} onAlwaysAllow={() => void submit('approve_always')} showPersistentActions alwaysAllowLabel="Always allow" approveLabel={selectedCount === 1 ? 'Deploy 1 session' : `Deploy ${selectedCount} sessions`} shortcutHint="Enter deploys selected · Esc denies" />}
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void submit('approve')}
      onDenyShortcut={() => void submit('deny')}
      shortcutsDisabled={loading}
      onRequestClose={() => void submit('deny')}
    >
      <div className="grid min-w-0 gap-3 sm:gap-4">
        <div className="min-w-0 break-words rounded-2xl border border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_8%,var(--app-surface))] px-3 py-3 text-sm leading-6 text-[var(--app-text-muted)] sm:px-4">
          One safe default is selected. Check additional proposals to allow more in this batch. Deploy approves only this request; Always allow also lets future session deployments run without asking.
        </div>
        <section className="flex min-w-0 flex-col gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3 sm:flex-row sm:items-center sm:justify-between sm:p-4" aria-label="Session deployment permission options">
          <div className="min-w-0">
            <div className="text-sm font-semibold text-[var(--app-text)]">Need granular deployment controls?</div>
            <div className="mt-1 break-words text-xs leading-5 text-[var(--app-text-muted)]">Set ask, bounded automatic limits, or over-limit behavior in Permissions settings.</div>
          </div>
          {onOpenPermissions ? <Button type="button" variant="outline" onClick={onOpenPermissions} className="w-full shrink-0 sm:w-auto">Permissions settings</Button> : null}
        </section>
        {proposals.length === 0 ? <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">No valid deployment proposals were provided.</div> : null}
        <section className="grid min-w-0 gap-3" aria-label="Session deployment proposals">
          {proposals.map((proposal, index) => (
            <article key={proposal.id} className={cn('min-w-0 overflow-hidden rounded-2xl border p-3 sm:p-4', proposal.selected ? 'border-[var(--app-border-accent)] bg-[color-mix(in_oklab,var(--app-primary)_6%,var(--app-surface))]' : 'border-[var(--app-border)] bg-[var(--app-surface)]')}>
              <label className="flex cursor-pointer items-start gap-3">
                <input type="checkbox" checked={proposal.selected} onChange={(event) => updateProposal(proposal.id, { selected: event.target.checked })} className="mt-1 size-4 accent-[var(--app-primary)]" />
                <span className="min-w-0 flex-1"><span className="block text-sm font-semibold text-[var(--app-text)]">Session {index + 1}</span><span className="block min-w-0 truncate text-xs text-[var(--app-text-muted)]" title={proposal.workspaceName || proposal.workspacePath}>{proposal.agentMode === 'primary' ? 'Primary agent' : 'Subagent'} · {proposal.workspaceName || proposal.workspacePath}</span></span>
              </label>
              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                <SessionDeployField label="Title"><input value={proposal.title} onChange={(event) => updateProposal(proposal.id, { title: event.target.value })} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)]')} placeholder="New session" /></SessionDeployField>
                <SessionDeployField label="Mode"><select value={proposal.mode} onChange={(event) => updateProposal(proposal.id, { mode: event.target.value as 'plan' | 'auto' })} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)]')}><option value="auto">Auto</option><option value="plan">Plan</option></select></SessionDeployField>
                <SessionDeployField label="Allowed agent"><select value={proposal.agentName} title={proposal.agentName} onChange={(event) => { const agent = payload.allowedAgents.find((candidate) => candidate.name === event.target.value); if (agent) updateProposal(proposal.id, { agentName: agent.name, agentMode: agent.mode }) }} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)]')}>{payload.allowedAgents.map((agent) => <option key={`${agent.mode}:${agent.name}`} value={agent.name}>{agent.name} ({agent.mode})</option>)}</select></SessionDeployField>
                <SessionDeployField label="Workspace"><select value={proposal.workspacePath} title={proposal.workspaceName || proposal.workspacePath} disabled={payload.allowedWorkspaces.length === 0} onChange={(event) => { const workspace = payload.allowedWorkspaces.find((candidate) => candidate.path === event.target.value); if (workspace) updateProposal(proposal.id, { workspacePath: workspace.path, workspaceName: workspace.name, manifest: { ...proposal.manifest, workspace_id: workspace.id, workspace_generation: workspace.generation, workspace_path: workspace.path, workspace_name: workspace.name } }) }} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)] disabled:opacity-50')}>{payload.allowedWorkspaces.length === 0 ? <option value="">No saved workspaces</option> : payload.allowedWorkspaces.map((workspace) => <option key={`${workspace.id}:${workspace.generation}`} value={workspace.path}>{workspace.name || workspace.path}</option>)}</select></SessionDeployField>
              </div>
              <SessionDeployField label="Prompt" className="mt-3"><Textarea value={proposal.prompt} onChange={(event) => updateProposal(proposal.id, { prompt: event.target.value })} rows={4} className="min-h-24 resize-y bg-[var(--app-bg-alt)]" /></SessionDeployField>
              <div className="mt-3 grid gap-3 sm:grid-cols-3">
                <SessionDeployField label="Worktree mode"><select aria-label="Worktree mode" value={proposal.managedWorktree ? 'managed' : 'workspace'} onChange={(event) => updateProposal(proposal.id, { managedWorktree: event.target.value === 'managed' })} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)]')}><option value="managed">Managed worktree (recommended)</option><option value="workspace">Use current workspace</option></select></SessionDeployField>
                <SessionDeployField label="Base branch"><input value={proposal.worktreeBaseBranch} disabled={!proposal.managedWorktree} onChange={(event) => updateProposal(proposal.id, { worktreeBaseBranch: event.target.value })} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)] disabled:opacity-50')} placeholder="Current branch" /></SessionDeployField>
                <SessionDeployField label="AI branch suggestion"><input value={proposal.worktreeBranch} disabled={!proposal.managedWorktree} onChange={(event) => updateProposal(proposal.id, { worktreeBranch: event.target.value })} className={cn(SESSION_DEPLOY_CONTROL_CLASS, 'bg-[var(--app-bg-alt)] disabled:opacity-50')} placeholder="Provided automatically" /></SessionDeployField>
              </div>
            </article>
          ))}
        </section>
        {error ? <div className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{error}</div> : null}
      </div>
    </ModalShell>
  )
}

function SessionDeployField({ label, className, children }: { label: string; className?: string; children: React.ReactNode }) {
  return <label className={cn('grid min-w-0 max-w-full gap-1.5 overflow-hidden', className)}><span className="min-w-0 break-words text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{label}</span>{children}</label>
}

function SessionCommitModal({
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
    if (open) { setNote(''); setLoading(false) }
  }, [open, permission?.id])
  if (!permission) return null
  const payload = parseSessionCommitPermission(permission)
  const resolve = async (action: 'approve' | 'deny' | 'approve_always' | 'always_deny') => {
    setLoading(true)
    try {
      const approved = (action === 'approve' || action === 'approve_always') && Object.keys(payload.approvedArguments).length > 0
        ? payload.approvedArguments
        : undefined
      await onResolve(action, note.trim(), approved)
    } finally { setLoading(false) }
  }
  return (
    <ModalShell open={open} title="Commit session changes?" subtitle="Review each exact commit message and attributed file before Git is mutated" pendingCount={pendingCount} sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(820px,calc(100vw-48px))]" bodyClassName="overflow-y-auto"
      footer={<PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} onAlwaysAllow={() => void resolve('approve_always')} onAlwaysDeny={() => void resolve('always_deny')} showPersistentActions approveLabel={payload.commits.length === 1 ? 'Approve commit' : `Approve ${payload.commits.length} commits`} note={note} onNoteChange={setNote} noteLabel="Message to agent" />}
      onOpenChange={onOpenChange} onPrimaryShortcut={() => void resolve('approve')} onDenyShortcut={() => void resolve('deny')} shortcutsDisabled={loading}>
      <section className="grid gap-3" aria-label="Session commits">
        {payload.commits.length === 0 ? <div className="rounded-2xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-4 text-sm text-[var(--app-danger)]">No valid commit details were provided.</div> : payload.commits.map((commit, index) => (
          <article key={`${index}:${commit.repository}:${commit.message}`} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 shadow-sm">
            <div className="flex items-start gap-3"><span className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-[var(--app-primary-soft)] text-[var(--app-primary)]"><GitCommit className="size-4" aria-hidden="true" /></span><div className="min-w-0"><h3 className="font-semibold text-[var(--app-text)]">Commit {index + 1}</h3><p className="mt-1 whitespace-pre-wrap break-words text-sm text-[var(--app-text)]">{commit.message || 'No commit message'}</p></div></div>
            {commit.repository ? <div className="mt-3 break-all font-mono text-xs text-[var(--app-text-muted)]">{commit.repository}</div> : null}
            <ul className="mt-3 grid gap-1.5" aria-label={`Changed files for commit ${index + 1}`}>{commit.files.map((file) => <li key={file} className="break-all rounded-lg bg-[var(--app-bg-alt)] px-3 py-2 font-mono text-xs text-[var(--app-text)]">{file}</li>)}</ul>
          </article>
        ))}
      </section>
    </ModalShell>
  )
}

function SessionArchiveModal({
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

  const payload = parseSessionArchivePermission(permission)
  const resolve = async (action: 'approve' | 'deny' | 'approve_always' | 'always_deny') => {
    setLoading(true)
    try {
      const approved = (action === 'approve' || action === 'approve_always') && Object.keys(payload.approvedArguments).length > 0
        ? payload.approvedArguments
        : undefined
      await onResolve(action, note.trim(), approved)
    } finally {
      setLoading(false)
    }
  }
  const unarchive = payload.action === 'unarchive'
  const verb = unarchive ? 'Unarchive' : 'Archive'

  return (
    <ModalShell
      open={open}
      title={`${verb} sessions?`}
      subtitle={unarchive ? 'Review the durable sessions that will be restored to your workspace view' : 'Review the sessions that will be removed from your active workspace view'}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-full sm:w-[min(760px,calc(100vw-48px))]"
      bodyClassName="overflow-y-auto"
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          onAlwaysAllow={() => void resolve('approve_always')}
          onAlwaysDeny={() => void resolve('always_deny')}
          showPersistentActions
          approveLabel={payload.sessions.length === 1 ? `${verb} session` : `${verb} ${payload.sessions.length} sessions`}
          note={note}
          onNoteChange={setNote}
          noteLabel="Message to agent"
          notePlaceholder="Optional note…"
        />
      }
      onOpenChange={onOpenChange}
      onPrimaryShortcut={() => void resolve('approve')}
      onDenyShortcut={() => void resolve('deny')}
      shortcutsDisabled={loading}
    >
      <div className="grid gap-4">
        <div className="flex items-start gap-3 rounded-2xl border border-[color-mix(in_oklab,var(--app-warning)_28%,var(--app-border))] bg-[color-mix(in_oklab,var(--app-warning)_8%,var(--app-surface))] px-4 py-3">
          <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-[color-mix(in_oklab,var(--app-warning)_16%,transparent)] text-[var(--app-warning)]">
            <Archive className="size-5" aria-hidden="true" />
          </span>
          <div className="min-w-0">
            <div className="font-semibold text-[var(--app-text)]">{payload.sessions.length} {payload.sessions.length === 1 ? 'session' : 'sessions'} selected</div>
            <p className="mt-1 text-sm leading-5 text-[var(--app-text-muted)]">{unarchive ? 'Unarchived sessions return to the active workspace view with their durable messages and history intact.' : 'Archived sessions stay in durable history and can be found later. This does not delete their messages.'}</p>
          </div>
        </div>

        <section className="grid gap-2.5" aria-label={unarchive ? 'Sessions to unarchive' : 'Sessions to archive'}>
          {payload.sessions.length === 0 ? (
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-5 text-sm text-[var(--app-text-muted)]">No session details were provided.</div>
          ) : payload.sessions.map((session, index) => (
            <article key={`${index}:${session.title}:${session.updatedAt}`} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 shadow-sm">
              <div className="flex min-w-0 items-start justify-between gap-3">
                <h3 className="min-w-0 break-words font-semibold leading-6 text-[var(--app-text)]">{session.title}</h3>
                <span className="shrink-0 rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text-muted)]">{sessionArchiveStateLabel(session.state)}</span>
              </div>
              <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1.5 text-xs text-[var(--app-text-muted)]">
                <span className="inline-flex items-center gap-1.5"><Folder className="size-3.5" aria-hidden="true" />{session.workspaceName}</span>
                <span className="inline-flex items-center gap-1.5"><CalendarClock className="size-3.5" aria-hidden="true" />Updated {sessionArchiveUpdatedLabel(session.updatedAt)}</span>
              </div>
            </article>
          ))}
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
      footer={<PermissionActionBar loading={loading} onApprove={() => void submit('approve')} onDeny={() => void submit('deny')} approveLabel="Submit response" />}
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
      footer={
        <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 pb-[max(0.5rem,var(--app-safe-area-bottom))] shadow-[0_-18px_36px_rgba(0,0,0,0.16)] sm:px-5 sm:py-3 sm:pb-3">
          <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:justify-end">
            <Button
              type="button"
              variant="primary"
              className="order-1 w-full sm:order-3 sm:w-auto"
              title={payload.sessionAllow.label}
              onClick={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.sessionAllow.decision))}
              disabled={loading}
            >
              <span className="min-w-0 whitespace-normal text-center leading-5">{payload.sessionAllow.label}</span>
            </Button>
            <Button
              type="button"
              variant="outline"
              className="order-2 w-full sm:w-auto"
              title={payload.addToWorkspace.label}
              onClick={() => void resolve('approve', buildWorkspaceScopeResolutionReason(payload.addToWorkspace.decision))}
              disabled={loading || !payload.addToWorkspace.available}
            >
              <span className="min-w-0 whitespace-normal text-center leading-5">{payload.addToWorkspace.label}</span>
            </Button>
            <Button type="button" variant="ghost" className="order-3 w-full sm:order-1 sm:w-auto" onClick={() => void resolve('deny', '')} disabled={loading}>
              Deny
            </Button>
          </div>
        </div>
      }
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
          </section>
        </div>
      </div>

    </ModalShell>
  )
}

function taskLaunchExecutionLabel(tools: TaskLaunchResolvedTools, fallback = ''): string {
  return tools.effectiveExecutionMode || tools.runtimeMode || fallback || 'subagent'
}

function taskLaunchToolsSummary(tools: TaskLaunchResolvedTools): string {
  const execution = taskLaunchExecutionLabel(tools, '')
  if (execution) {
    return execution === 'read' ? 'read-only tools' : `${execution} tools`
  }
  if (tools.preset) {
    return `${tools.preset.replace(/_/g, ' ')} tools`
  }
  if (tools.allowedTools.length > 0) {
    return `${tools.allowedTools.length} allowed tool${tools.allowedTools.length === 1 ? '' : 's'}`
  }
  return ''
}

function taskLaunchModalTitle(payload: TaskLaunchPayload): string {
  const task = promptWordPreview(payload.description || payload.prompt, 10)
  const prefix = payload.launchCount === 1 ? 'Launch subagent' : `Launch ${payload.launchCount} subagents`
  return task ? `${prefix}: ${task}` : prefix
}

function taskLaunchSubtitle(payload: TaskLaunchPayload): string {
  const parts = ['Review before launch']
  if (payload.reportMaxChars > 0) parts.push(`report ${payload.reportMaxChars} chars`)
  return parts.join(' · ')
}

function HeaderChip({ icon: Icon, children }: { icon: LucideIcon, children: React.ReactNode }) {
  return (
    <span className="inline-flex h-9 items-center gap-2 rounded-xl border border-[var(--app-border)] bg-[color-mix(in_oklab,var(--app-surface)_92%,var(--app-bg-alt))] px-3 text-xs font-medium leading-none text-[var(--app-text-muted)]">
      <Icon className="size-4 text-[var(--app-text-subtle)]" aria-hidden="true" />
      <span className="whitespace-nowrap">{children}</span>
    </span>
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
  const [promptExpanded, setPromptExpanded] = useState(false)

  useEffect(() => {
    if (open) {
      setNote('')
      setLoading(false)
      setPromptExpanded(false)
    }
  }, [open, permission?.id])

  if (!permission) {
    return null
  }

  const payload = parseTaskLaunchPermission(permission)
  const promptWords = countWords(payload.prompt)
  const promptPreview = promptWordPreview(payload.prompt, 42)
  const modalTitle = taskLaunchModalTitle(payload)
  const launchSummary = `${payload.launchCount} launch${payload.launchCount === 1 ? '' : 'es'}`
  const toolsSummary = taskLaunchToolsSummary(payload.resolvedTools)
  const routerSummary = payload.resolvedAgentName ? `Router: ${payload.resolvedAgentName}` : ''
  const budgetSummary = payload.automaticBudgetUsed || payload.automaticBudgetRemaining
    ? `Budget ${payload.automaticBudgetUsed} used · ${payload.automaticBudgetRemaining} remaining`
    : ''

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
      title={modalTitle}
      subtitle={taskLaunchSubtitle(payload)}
      pendingCount={pendingCount}
      sessionMode={sessionMode}
      widthClassName="w-[min(100%,calc(100vw-12px))] sm:w-[min(980px,calc(100vw-48px))] xl:w-[min(1040px,calc(100vw-64px))]"
      bodyClassName="px-3 py-4 sm:px-5 sm:py-5"
      showSessionMeta={false}
      headerExtra={
        <div className="flex flex-nowrap items-center gap-2 overflow-x-auto pb-0.5">
          <HeaderChip icon={Rocket}>{launchSummary}</HeaderChip>
          {toolsSummary ? <HeaderChip icon={LockKeyhole}>{toolsSummary}</HeaderChip> : null}
          {routerSummary ? <HeaderChip icon={Server}>{routerSummary}</HeaderChip> : null}
          {budgetSummary ? <HeaderChip icon={Rocket}>{budgetSummary}</HeaderChip> : null}
        </div>
      }
      footer={
        <PermissionActionBar
          loading={loading}
          onApprove={() => void resolve('approve')}
          onDeny={() => void resolve('deny')}
          approveLabel="Launch subagents"
          denyVariant="secondary"
          shortcutHint="Enter launches · Esc denies"
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
      <div className="grid min-w-0 gap-5">
        {payload.resolvedAgentError ? <div className="rounded-xl border border-[var(--app-danger)]/40 bg-[var(--app-danger)]/10 px-3 py-2 text-sm text-[var(--app-danger)]">{payload.resolvedAgentError}</div> : null}

        <section className="min-w-0">
          <div className="mb-3 text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Subagents</div>
          {payload.launches.length > 0 ? (
            <div className="grid max-h-[min(52dvh,34rem)] gap-3 overflow-y-auto overscroll-contain pr-1 sm:max-h-[min(56dvh,36rem)]">
              {payload.launches.map((launch) => {
                const resolvedAgentName = launch.resolvedAgentName || launch.requestedSubagentType || 'subagent'
                const agentName = displayAgentName(resolvedAgentName)
                const requestedLabel = launch.requestedSubagentType && displayAgentName(launch.requestedSubagentType) !== agentName ? displayAgentName(launch.requestedSubagentType) : ''
                const modelLabel = [launch.subagentProvider, launch.subagentModel].filter(Boolean).join(' / ')
                const toolLabel = taskLaunchToolsSummary(launch.resolvedTools)
                return (
                  <article
                    key={`${launch.index}:${launch.requestedSubagentType}:${launch.resolvedAgentName}`}
                    className="flex min-w-0 gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-3 sm:gap-4 sm:p-4"
                  >
                    <span className="flex size-7 shrink-0 items-center justify-center rounded-full border border-[var(--app-border)] bg-[var(--app-bg-alt)] text-xs font-semibold text-[var(--app-text-muted)]">
                      {launch.index}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
                        <div className="truncate text-sm font-semibold text-[var(--app-text)] sm:text-base">{agentName}</div>
                        {toolLabel ? (
                          <span className="text-xs font-normal text-[var(--app-text-subtle)] before:mr-2 before:text-[var(--app-text-subtle)] before:content-['·']">
                            {toolLabel}
                          </span>
                        ) : null}
                      </div>
                      {[requestedLabel ? `via ${requestedLabel}` : '', modelLabel].filter(Boolean).length > 0 ? (
                        <div className="mt-1 min-w-0 break-words text-xs leading-5 text-[var(--app-text-subtle)] [overflow-wrap:anywhere]">
                          {[requestedLabel ? `via ${requestedLabel}` : '', modelLabel].filter(Boolean).join(' · ')}
                        </div>
                      ) : null}
                      <div className="mt-2 grid gap-1 text-xs text-[var(--app-text-muted)] sm:grid-cols-2">
                        {launch.sourceAgentName ? <div><span className="text-[var(--app-text-subtle)]">Coder source:</span> {displayAgentName(launch.sourceAgentName)}</div> : null}
                        {launch.sourceProfileMode ? <div><span className="text-[var(--app-text-subtle)]">Profile mode:</span> {launch.sourceProfileMode}</div> : null}
                        {launch.inheritedRuntimeMode ? <div><span className="text-[var(--app-text-subtle)]">Runtime/session:</span> {launch.inheritedRuntimeMode} / {launch.childMode || '—'}</div> : null}
                        {launch.deliverable ? <div><span className="text-[var(--app-text-subtle)]">Deliverable:</span> {launch.deliverable}</div> : null}
                        {launch.ownedScope.length ? <div><span className="text-[var(--app-text-subtle)]">Owned scope:</span> {launch.ownedScope.join(', ')}</div> : null}
                        {launch.dependencyEvidence ? <div><span className="text-[var(--app-text-subtle)]">Dependency evidence:</span> {launch.dependencyEvidence}</div> : null}
                        {launch.isolation ? <div><span className="text-[var(--app-text-subtle)]">Isolation:</span> {launch.isolation}</div> : null}
                        {launch.resolvedTools.allowedTools.length ? <div><span className="text-[var(--app-text-subtle)]">Tools:</span> {launch.resolvedTools.allowedTools.join(', ')}</div> : null}
                      </div>
                      <div className="mt-3 min-w-0 text-sm leading-6 text-[var(--app-text)]">
                        <ChatMarkdown className="[overflow-wrap:anywhere]" content={launch.assignment || 'No launch-specific instructions.'} />
                        {launch.resolvedAgentError ? <div className="mt-2 text-sm text-[var(--app-danger)]">{launch.resolvedAgentError}</div> : null}
                      </div>
                    </div>
                  </article>
                )
              })}
            </div>
          ) : (
            <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-sm text-[var(--app-text-muted)]">No launches were included in the manifest.</div>
          )}
        </section>

        <section className="min-w-0 border-t border-[var(--app-border)] pt-5">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Full prompt</div>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              className="h-8 px-3 text-xs"
              onClick={() => setPromptExpanded((value) => !value)}
            >
              {promptExpanded ? 'Hide full prompt' : 'Show full prompt'}
            </Button>
          </div>
          <div className="max-h-[min(24dvh,14rem)] overflow-y-auto rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3 text-sm leading-6 text-[var(--app-text)] sm:max-h-[min(28dvh,16rem)] sm:p-4">
            {promptExpanded ? (
              <ChatMarkdown className="[overflow-wrap:anywhere]" content={payload.prompt || 'No prompt was included in the manifest.'} />
            ) : (
              <div className="text-[var(--app-text-muted)]">
                <span className="text-xs text-[var(--app-text-subtle)]">{promptWords} {promptWords === 1 ? 'word' : 'words'} · </span>
                {promptPreview || 'No prompt text.'}
              </div>
            )}
          </div>
        </section>

        <section className="min-w-0">
          <label className="grid gap-2">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Message to agent (optional)</span>
            <Textarea
              value={note}
              onChange={(event) => setNote(event.target.value)}
              placeholder="Add any notes for the agents before launch..."
              className="min-h-[5rem] resize-none bg-[var(--app-bg-alt)]"
              rows={3}
            />
          </label>
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
  payload.toolInventory.tools.forEach((tool) => byName.set(tool.contractName || tool.name, tool))
  Object.keys(form.toolContractTools).forEach((name) => {
    const contractName = name.trim()
    if (!contractName) return
    if (!byName.has(contractName)) {
      byName.set(contractName, { name: contractName, contractName, description: '', group: 'custom', kind: 'custom' })
    }
  })
  return Array.from(byName.values()).sort((left, right) => {
    const group = left.group.localeCompare(right.group)
    return group !== 0 ? group : (left.contractName || left.name).localeCompare(right.contractName || right.name)
  })
}

const AGENT_WRITE_TOOL_NAMES = new Set(['write', 'edit', 'bash', 'task', 'git_add', 'git_commit'])
const AGENT_DEFAULT_READWRITE_TOOL_NAMES = ['write', 'edit']
const FALLBACK_AGENT_THINKING_OPTIONS = [
  { value: '', label: 'Default' },
  { value: 'off', label: 'Off' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'X-High' },
  { value: 'max', label: 'Max' },
  { value: 'ultra', label: 'Ultra' },
]

function deriveExecutionSettingFromTools(tools: Record<string, AgentToolConfigFormState>): AgentEffectiveExecution {
  return Object.entries(tools).some(([name, config]) => {
    if (!AGENT_WRITE_TOOL_NAMES.has(name.trim().toLowerCase())) return false
    return config.enabled || splitCSV(config.bashPrefixes).length > 0
  }) ? 'readwrite' : 'read'
}

function agentToolContractFromForm(_payload: ReturnType<typeof parseAgentChangePermission>, form: AgentProfileFormState): Record<string, unknown> {
  const tools: Record<string, unknown> = {}
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

interface ToolAccessList {
  allowed: string[]
  blocked: string[]
}

function sortedUnique(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((left, right) => left.localeCompare(right))
}

function sameStringSet(left: string[], right: string[]): boolean {
  const leftSet = new Set(left.map((value) => value.trim()).filter(Boolean))
  const rightSet = new Set(right.map((value) => value.trim()).filter(Boolean))
  if (leftSet.size !== rightSet.size) return false
  for (const value of leftSet) {
    if (!rightSet.has(value)) return false
  }
  return true
}

function toolAccessForPreset(preset: AgentToolInventory['presets'][number] | null | undefined): ToolAccessList {
  return {
    allowed: sortedUnique(preset?.enabledTools ?? []),
    blocked: sortedUnique(preset?.disabledByDefault ?? []),
  }
}

function effectiveToolAccess(preset: AgentToolInventory['presets'][number] | null | undefined, overrides: Record<string, AgentToolConfigFormState>, fallbackNames: string[] = []): ToolAccessList {
  const allowed = new Set(toolAccessForPreset(preset).allowed)
  const blocked = new Set(toolAccessForPreset(preset).blocked)
  fallbackNames.forEach((name) => {
    const toolName = name.trim()
    if (toolName && !allowed.has(toolName) && !blocked.has(toolName)) blocked.add(toolName)
  })
  Object.entries(overrides).forEach(([name, config]) => {
    const toolName = name.trim()
    if (!toolName) return
    if (config.enabled) {
      allowed.add(toolName)
      blocked.delete(toolName)
    } else {
      blocked.add(toolName)
      allowed.delete(toolName)
    }
  })
  return { allowed: sortedUnique(Array.from(allowed)), blocked: sortedUnique(Array.from(blocked)) }
}

function matchedToolPresetID(access: ToolAccessList, presets: AgentToolInventory['presets']): string {
  const matched = presets.find((preset) => preset.id !== CUSTOM_AGENT_TOOL_PRESET_ID && sameStringSet(preset.enabledTools, access.allowed) && sameStringSet(preset.disabledByDefault, access.blocked))
  return matched?.id ?? CUSTOM_AGENT_TOOL_PRESET_ID
}

function customToolsFromAccess(access: ToolAccessList): Record<string, AgentToolConfigFormState> {
  const tools: Record<string, AgentToolConfigFormState> = {}
  access.allowed.forEach((name) => { tools[name] = { enabled: true, bashPrefixes: '' } })
  access.blocked.forEach((name) => { tools[name] = { enabled: false, bashPrefixes: '' } })
  return tools
}

function agentToolInventoryPreset(payload: ReturnType<typeof parseAgentChangePermission>, presetID: string) {
  const normalized = presetID.trim().toLowerCase()
  if (!normalized) return null
  return payload.toolInventory.presets.find((preset) => preset.id.trim().toLowerCase() === normalized)
    ?? AGENT_TOOL_PRESET_OPTIONS.find((preset) => preset.id === normalized)
    ?? null
}

function agentToolInventoryPresetOptions(payload: ReturnType<typeof parseAgentChangePermission>): AgentToolInventory['presets'] {
  const byID = new Map<string, AgentToolInventory['presets'][number]>()
  AGENT_TOOL_PRESET_OPTIONS.forEach((preset) => byID.set(preset.id, preset))
  payload.toolInventory.presets.forEach((preset) => {
    if (preset.id.trim()) byID.set(preset.id.trim(), preset)
  })
  return Array.from(byID.values()).sort((left, right) => {
    if (left.id === CUSTOM_AGENT_TOOL_PRESET_ID) return 1
    if (right.id === CUSTOM_AGENT_TOOL_PRESET_ID) return -1
    return left.label.localeCompare(right.label)
  })
}

function presetDisplayLabel(preset: { id: string; label: string }): string {
  return preset.label.trim() && preset.label !== preset.id ? `${preset.label} (${preset.id})` : preset.id
}

function agentToolAccessSummary(payload: ReturnType<typeof parseAgentChangePermission>, form: AgentProfileFormState | null): AgentToolAccessSummary {
  if (form) {
    const restricted: string[] = []
    const activePreset = agentToolInventoryPreset(payload, form.toolContractPreset)
    const access = effectiveToolAccess(
      activePreset,
      form.toolContractTools,
      payload.toolInventory.tools.map((tool) => tool.contractName || tool.name),
    )
    if ((activePreset?.bashPrefixes.length ?? 0) > 0) restricted.push('bash')
    Object.entries(form.toolContractTools).forEach(([name, config]) => {
      const toolName = name.trim()
      if (!toolName) return
      if (splitCSV(config.bashPrefixes).length > 0) restricted.push(toolName)
    })
    return {
      allowed: access.allowed,
      blocked: access.blocked,
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
  const overrideAllowed = form
    ? Object.entries(form.toolContractTools).filter(([, config]) => config.enabled).map(([name]) => name.trim()).filter(Boolean)
    : []
  const overrideBlocked = form
    ? Object.entries(form.toolContractTools).filter(([, config]) => !config.enabled).map(([name]) => name.trim()).filter(Boolean)
    : []
  const activePreset = agentToolInventoryPreset(payload, summary.preset)
  const presetName = activePreset ? presetDisplayLabel(activePreset) : summary.preset ? `preset ${summary.preset}` : ''
  const policyText = [
    presetName,
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
          label={presetName ? `${presetName} enabled` : 'Preset enabled'}
          count={summary.allowed.length}
          items={summary.allowed}
          emptyText="No preset-enabled tools"
          tone="allow"
          onItemClick={onToolToggle ? (item) => onToolToggle(item, false) : undefined}
          disabled={disabled}
          itemTitle="Click to block this tool"
        />
        <ToolAccessRow
          label={presetName ? `${presetName} disabled` : 'Preset disabled'}
          count={summary.blocked.length}
          items={summary.blocked}
          emptyText="No preset-disabled tools"
          tone="block"
          onItemClick={onToolToggle ? (item) => onToolToggle(item, true) : undefined}
          disabled={disabled}
          itemTitle="Click to allow this tool"
        />
      </div>
      {overrideAllowed.length > 0 || overrideBlocked.length > 0 ? (
        <div className="grid gap-2 border-t border-[var(--app-border)] pt-2">
          <ToolAccessRow
            label="Override allow"
            count={overrideAllowed.length}
            items={sortedUnique(overrideAllowed)}
            emptyText="No explicit allow overrides"
            tone="allow"
            onItemClick={onToolToggle ? (item) => onToolToggle(item, false) : undefined}
            disabled={disabled}
            itemTitle="Click to block this tool"
          />
          <ToolAccessRow
            label="Override block"
            count={overrideBlocked.length}
            items={sortedUnique(overrideBlocked)}
            emptyText="No explicit block overrides"
            tone="block"
            onItemClick={onToolToggle ? (item) => onToolToggle(item, true) : undefined}
            disabled={disabled}
            itemTitle="Click to allow this tool"
          />
        </div>
      ) : null}
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
  const activeModel = activeModels.find((option) => option.model === form.model)
  const thinkingOptions = activeModel
    ? [
        { value: '', label: 'Default' },
        ...modelThinkingOptions(activeModel).map((value) => ({
          value,
          label: value === 'xhigh' ? 'X-High' : `${value.charAt(0).toUpperCase()}${value.slice(1)}`,
        })),
      ]
    : FALLBACK_AGENT_THINKING_OPTIONS
  const visibleThinkingOptions = form.thinking && !thinkingOptions.some((option) => option.value === form.thinking)
    ? [...thinkingOptions, { value: form.thinking, label: form.thinking }]
    : thinkingOptions
  const inventoryTools = toolInventoryTools(payload, form)
  const presetOptions = agentToolInventoryPresetOptions(payload)
  const activePreset = agentToolInventoryPreset(payload, form.toolContractPreset)
  const [toolsExpanded, setToolsExpanded] = useState(false)
  const setToolPreset = (presetID: string) => {
    const preset = agentToolInventoryPreset(payload, presetID)
    if (!preset || preset.id === CUSTOM_AGENT_TOOL_PRESET_ID) {
      const access = effectiveToolAccess(
        activePreset,
        form.toolContractTools,
        inventoryTools.map((tool) => tool.contractName || tool.name),
      )
      onChange({
        ...form,
        toolContractPreset: CUSTOM_AGENT_TOOL_PRESET_ID,
        toolContractTools: customToolsFromAccess(access),
      })
      return
    }
    onChange({ ...form, toolContractPreset: preset.id, toolContractTools: {} })
  }
  const setToolEnabled = (toolName: string, enabled: boolean) => {
    const access = effectiveToolAccess(
      activePreset,
      form.toolContractTools,
      inventoryTools.map((tool) => tool.contractName || tool.name),
    )
    access.allowed = access.allowed.filter((name) => name !== toolName)
    access.blocked = access.blocked.filter((name) => name !== toolName)
    if (enabled) access.allowed.push(toolName)
    else access.blocked.push(toolName)
    access.allowed = sortedUnique(access.allowed)
    access.blocked = sortedUnique(access.blocked)
    const nextTools = customToolsFromAccess(access)
    onChange({
      ...form,
      executionSetting: deriveExecutionSettingFromTools(nextTools),
      toolContractPreset: matchedToolPresetID(access, presetOptions),
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
              const firstOption = providers.find(([candidate]) => candidate === provider)?.[1]?.[0]
              onChange({
                ...form,
                provider,
                model: firstOption?.model ?? '',
                thinking: firstOption ? defaultModelThinking(firstOption) : '',
              })
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
            <select value={form.model} onChange={(event: ChangeEvent<HTMLSelectElement>) => {
              const model = event.target.value
              const option = activeModels.find((candidate) => candidate.model === model)
              onChange({
                ...form,
                model,
                thinking: option ? defaultModelThinking(option) : form.thinking,
              })
            }} disabled={disabled || !form.provider} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
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
              {visibleThinkingOptions.map((option) => <option key={option.value || 'default'} value={option.value}>{option.label}</option>)}
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
        <div className="grid gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
          <label className="grid gap-1.5 text-sm">
            <span className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Tool preset</span>
            <div className="relative">
              <select value={form.toolContractPreset || CUSTOM_AGENT_TOOL_PRESET_ID} onChange={(event: ChangeEvent<HTMLSelectElement>) => setToolPreset(event.target.value)} disabled={disabled} className="w-full appearance-none rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 pr-8 text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]">
                <option value="">Choose a preset</option>
                {presetOptions.map((preset) => <option key={preset.id} value={preset.id}>{presetDisplayLabel(preset)}</option>)}
              </select>
              <ChevronDown size={14} className="pointer-events-none absolute right-3 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
            </div>
          </label>
          <AgentToolAccessSummaryCard
            payload={payload}
            form={form}
            disabled={disabled}
            onToolToggle={setToolEnabled}
          />
        </div>
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
                  const toolName = tool.contractName || tool.name
                  const config = form.toolContractTools[toolName]
                  const checked = effectiveToolAccess(activePreset, form.toolContractTools, inventoryTools.map((entry) => entry.contractName || entry.name)).allowed.includes(toolName)
                  const bashPrefixes = config?.bashPrefixes ?? ''
                  return (
                    <div key={`${tool.kind}:${toolName}`} className="grid gap-1 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] p-2">
                      <label className="flex items-center gap-2 text-sm text-[var(--app-text)]">
                        <input type="checkbox" checked={checked} onChange={(event) => setToolEnabled(toolName, event.target.checked)} disabled={disabled} />
                        <span className="font-medium">{toolName}</span>
                        <span className="text-xs text-[var(--app-text-muted)]">{tool.group}</span>
                      </label>
                      {tool.description ? <div className="text-xs leading-5 text-[var(--app-text-muted)]">{tool.description}</div> : null}
                      {tool.name === 'bash' || bashPrefixes ? (
                        <input value={bashPrefixes} onChange={(event) => {
                          const nextTools = { ...form.toolContractTools }
                          nextTools[toolName] = { enabled: checked || event.target.value.trim() !== '', bashPrefixes: event.target.value }
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
      footer={<PermissionActionBar loading={loading} onApprove={() => void resolve('approve')} onDeny={() => void resolve('deny')} approveLabel="Apply change" />}
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
    </ModalShell>
  )
}

function genericPermissionSupportsPersistentActions(permission: DesktopPermissionRecord): boolean {
  const toolName = permissionDisplayToolName(permission.toolName)
  return toolName !== 'ask-user' && toolName !== 'exit_plan_mode' && toolName !== 'manage_sessions'
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
  params.set('tool', safeString(permission.toolName))
  params.set('arguments', safeString(permission.toolArguments))
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
  if (kind === 'plan-followup-request') {
    return <PlanFollowupRequestModal {...props} />
  }
  if (kind === 'plan-amendment-request') {
    return <PlanAmendmentRequestModal {...props} />
  }
  if (kind === 'plan-new-request') {
    return <NewPlanRequestModal {...props} />
  }
  if (kind === 'plan-update') {
    return <PlanUpdateModal {...props} />
  }
  if (kind === 'manage-todos') {
    return <ManageTodosModal {...props} />
  }
  if (kind === 'session-commit') {
    return <SessionCommitModal {...props} />
  }
  if (kind === 'session-archive' || kind === 'session-unarchive') {
    return <SessionArchiveModal {...props} />
  }
  if (kind === 'session-deploy') {
    return <SessionDeployModal {...props} />
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
