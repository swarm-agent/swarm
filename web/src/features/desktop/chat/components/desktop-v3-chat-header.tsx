import { useEffect, useRef, useState } from 'react'
import { Archive, Clipboard, Download, LoaderCircle, MessageSquareText, MoreVertical, Pin, Plus } from 'lucide-react'
import { DesktopV3RunStatusPill, formatDesktopV3RunTimerLabel, type DesktopV3RunStatusModel } from './desktop-v3-run-status'

export interface DesktopV3ChatHeaderSessionActions {
  pinned: boolean
  canPin: boolean
  pendingAction?: 'pin' | 'archive' | 'copy' | 'download' | 'rename' | null
  onTogglePinned: () => void
  onArchive: () => void
  onCopyConversation?: () => void
  onDownloadConversation?: () => void
  onRename?: (title: string) => Promise<void>
}

export interface DesktopV3ChatHeaderProps {
  title: string
  workspaceName: string
  branchName?: string
  mode?: string
  runStatus?: DesktopV3RunStatusModel | null
  runStatusNow?: number
  sessionActions?: DesktopV3ChatHeaderSessionActions | null
  onOpenChats?: () => void
  onNewSession?: () => void
}

function normalizeTitle(value: string): string {
  return value.trim() || 'New conversation'
}

function normalizeWorkspaceName(value: string): string {
  return value.trim() || 'Workspace'
}

function normalizeMode(value: string | undefined): string {
  return value?.trim().toLowerCase() === 'plan' ? 'plan' : 'auto'
}

function normalizeBranchName(value: string | undefined): string {
  return value?.trim() ?? ''
}

export function DesktopV3ChatHeader({
  title,
  workspaceName,
  branchName,
  mode,
  runStatus = null,
  runStatusNow = Date.now(),
  sessionActions = null,
  onOpenChats,
  onNewSession,
}: DesktopV3ChatHeaderProps) {
  const displayTitle = normalizeTitle(title)
  const displayWorkspace = normalizeWorkspaceName(workspaceName)
  const displayBranch = normalizeBranchName(branchName)
  const displayMode = normalizeMode(mode)
  const mobileRunTimerLabel = runStatus ? formatDesktopV3RunTimerLabel(runStatus, runStatusNow) : ''
  const [mobileActionsOpen, setMobileActionsOpen] = useState(false)
  const mobileActionsRef = useRef<HTMLSpanElement | null>(null)
  const titleInputRef = useRef<HTMLInputElement | null>(null)
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState(displayTitle)
  const [titleError, setTitleError] = useState('')
  const pendingAction = sessionActions?.pendingAction ?? null
  const actionDisabled = Boolean(pendingAction)

  useEffect(() => {
    if (!sessionActions) setMobileActionsOpen(false)
  }, [sessionActions])

  useEffect(() => {
    if (!editingTitle) setTitleDraft(displayTitle)
  }, [displayTitle, editingTitle])

  useEffect(() => {
    if (editingTitle) titleInputRef.current?.focus()
  }, [editingTitle])

  const cancelTitleEdit = () => {
    if (pendingAction === 'rename') return
    setEditingTitle(false)
    setTitleDraft(displayTitle)
    setTitleError('')
  }
  const saveTitle = async () => {
    const nextTitle = titleDraft.trim()
    if (!nextTitle) {
      setTitleError('Title cannot be blank.')
      return
    }
    if (!sessionActions?.onRename || pendingAction === 'rename') return
    try {
      await sessionActions.onRename(nextTitle)
      setEditingTitle(false)
      setTitleError('')
    } catch (error) {
      setTitleError(error instanceof Error ? error.message : 'Failed to rename session.')
    }
  }
  const editableTitle = editingTitle ? (
    <span className="grid min-w-0 gap-0.5">
      <input
        ref={titleInputRef}
        value={titleDraft}
        disabled={pendingAction === 'rename'}
        aria-label="Conversation title"
        aria-invalid={Boolean(titleError)}
        className="min-w-0 rounded border border-[var(--app-border-strong)] bg-[var(--app-bg)] px-1.5 py-0.5 text-[13px] font-semibold text-[var(--app-text)] outline-none focus:ring-2 focus:ring-[var(--app-focus-ring)] sm:text-sm"
        onChange={(event) => { setTitleDraft(event.target.value); setTitleError('') }}
        onKeyDown={(event) => {
          if (event.key === 'Enter') { event.preventDefault(); void saveTitle() }
          if (event.key === 'Escape') { event.preventDefault(); cancelTitleEdit() }
        }}
      />
      {titleError ? <span role="alert" className="text-[10px] font-normal text-[var(--app-danger)]">{titleError}</span> : null}
    </span>
  ) : sessionActions?.onRename ? (
    <button type="button" className="min-w-0 truncate rounded text-left hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]" title={`${displayTitle} — click to rename`} aria-label={`Rename conversation: ${displayTitle}`} onClick={() => { setEditingTitle(true); setTitleDraft(displayTitle); setTitleError('') }}>
      {displayTitle}
    </button>
  ) : <span className="truncate" title={displayTitle}>{displayTitle}</span>

  return (
    <header className="min-h-[60px] shrink-0 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 pb-2 pt-[calc(var(--app-safe-area-top)_+_0.5rem)] sm:h-[60px] sm:px-4 sm:py-0">
      <div className="flex h-full min-w-0 items-center gap-1.5 sm:gap-2">
        {onOpenChats ? (
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:hidden"
            onClick={onOpenChats}
            aria-label="Open chats"
            title="Chats"
          >
            <MessageSquareText size={18} />
          </button>
        ) : null}

        <div className="min-w-0 flex-1">
          <div className="sm:hidden">
            <h1 className="min-w-0 text-[13px] font-semibold leading-tight text-[var(--app-text)]">
              {editableTitle}
            </h1>
            <div className="relative mt-1 grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 text-[10px] font-medium text-[var(--app-text-muted)]" title={displayWorkspace}>
              <span className="min-w-0 truncate text-left">{displayWorkspace}</span>
              {displayBranch ? (
                <span className="pointer-events-none absolute left-1/2 max-w-[42vw] -translate-x-1/2 truncate text-center text-[var(--app-text-muted)]" title={displayBranch}>
                  {displayBranch}
                </span>
              ) : null}
              {mobileRunTimerLabel ? (
                <span className="w-[8ch] shrink-0 justify-self-end text-right tabular-nums text-[var(--app-text)]" title={runStatus?.label}>
                  {mobileRunTimerLabel}
                </span>
              ) : null}
            </div>
          </div>

          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              {editableTitle}
              <span className="shrink-0 font-normal text-[var(--app-text-subtle)]">/</span>
              <span className="truncate font-normal text-[var(--app-text-muted)]" title={displayWorkspace}>{displayWorkspace}</span>
            </h1>
            <div className="mt-1 flex max-w-full items-center gap-1.5 overflow-hidden text-[11px] font-medium text-[var(--app-text-muted)]">
              <span>{displayMode}</span>
            </div>
          </div>
        </div>

        <div className="hidden sm:block">
          <DesktopV3RunStatusPill model={runStatus} now={runStatusNow} />
        </div>

        {onNewSession ? (
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
            onClick={onNewSession}
            aria-label="New session"
            title="New session"
          >
            <Plus size={19} />
          </button>
        ) : null}

        {sessionActions ? (
          <span
            ref={mobileActionsRef}
            className="relative z-20 inline-flex"
            onBlur={(event) => {
              if (!event.currentTarget.contains(event.relatedTarget)) setMobileActionsOpen(false)
            }}
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.preventDefault()
                setMobileActionsOpen(false)
              }
            }}
          >
            <button
              type="button"
              className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
              onClick={() => setMobileActionsOpen((open) => !open)}
              aria-label="Session actions"
              aria-expanded={mobileActionsOpen}
              title="Session actions"
            >
              <MoreVertical size={19} />
            </button>
            {mobileActionsOpen ? (
              <span className="absolute right-0 top-full z-50 mt-1 grid min-w-32 gap-0.5 rounded-md border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] p-1 text-[11px] shadow-lg [background-color:var(--app-surface-elevated)]">
                {sessionActions.canPin ? (
                  <button
                    type="button"
                    className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-default disabled:opacity-60 disabled:hover:bg-transparent"
                    disabled={actionDisabled}
                    aria-pressed={sessionActions.pinned}
                    onClick={() => {
                      if (actionDisabled) return
                      setMobileActionsOpen(false)
                      sessionActions.onTogglePinned()
                    }}
                  >
                    {pendingAction === 'pin' ? <LoaderCircle size={13} className="animate-spin" aria-hidden="true" /> : <Pin size={13} aria-hidden="true" />}
                    <span>{sessionActions.pinned ? 'Unpin session' : 'Pin session'}</span>
                  </button>
                ) : null}
                {sessionActions.onCopyConversation ? (
                  <button type="button" className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:opacity-60" disabled={actionDisabled} onClick={() => { setMobileActionsOpen(false); sessionActions.onCopyConversation?.() }}>
                    {pendingAction === 'copy' ? <LoaderCircle size={13} className="animate-spin" /> : <Clipboard size={13} />}<span>Copy</span>
                  </button>
                ) : null}
                {sessionActions.onDownloadConversation ? (
                  <button type="button" className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:opacity-60" disabled={actionDisabled} onClick={() => { setMobileActionsOpen(false); sessionActions.onDownloadConversation?.() }}>
                    {pendingAction === 'download' ? <LoaderCircle size={13} className="animate-spin" /> : <Download size={13} />}<span>Download</span>
                  </button>
                ) : null}
                <button
                  type="button"
                  className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:cursor-default disabled:opacity-60 disabled:hover:bg-transparent"
                  disabled={actionDisabled}
                  onClick={() => {
                    if (actionDisabled) return
                    setMobileActionsOpen(false)
                    sessionActions.onArchive()
                  }}
                >
                  {pendingAction === 'archive' ? <LoaderCircle size={13} className="animate-spin" aria-hidden="true" /> : <Archive size={13} aria-hidden="true" />}
                  <span>Archive session</span>
                </button>
              </span>
            ) : null}
          </span>
        ) : null}
      </div>
    </header>
  )
}
