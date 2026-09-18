import { useEffect, useRef, useState } from 'react'
import { SessionAttachments } from './session-attachments'
import { Archive, Clipboard, Download, Film, LoaderCircle, MessageSquare, MessageSquareText, MoreVertical, Pin } from 'lucide-react'
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
  sessionId?: string
  branchName?: string
  modelLabel?: string
  runStatus?: DesktopV3RunStatusModel | null
  runStatusNow?: number
  sessionActions?: DesktopV3ChatHeaderSessionActions | null
  studioMode?: 'session' | 'studio' | null
  onToggleStudioMode?: () => void
  onOpenChats?: () => void
  onNewSession?: () => void
  hideMobileIdentity?: boolean
}

function normalizeTitle(value: string): string {
  return value.trim() || 'New conversation'
}

function normalizeWorkspaceName(value: string): string {
  return value.trim() || 'Workspace'
}

function normalizeBranchName(value: string | undefined): string {
  const normalized = value?.trim() ?? ''
  return ['undefined', 'null'].includes(normalized.toLowerCase()) ? '' : normalized
}

export function DesktopV3ChatHeader({
  title,
  workspaceName,
  sessionId,
  branchName,
  modelLabel,
  runStatus = null,
  runStatusNow: controlledRunStatusNow,
  sessionActions = null,
  studioMode = null,
  onToggleStudioMode,
  onOpenChats,
  onNewSession,
  hideMobileIdentity = false,
}: DesktopV3ChatHeaderProps) {
  const displayTitle = normalizeTitle(title)
  const displayWorkspace = sessionId ? 'Session workspaces' : normalizeWorkspaceName(workspaceName)
  const displayBranch = normalizeBranchName(branchName)
  const resolvedModelLabel = modelLabel?.trim() ?? ''
  const [liveRunStatusNow, setLiveRunStatusNow] = useState(() => Date.now())
  const runStatusNow = controlledRunStatusNow ?? liveRunStatusNow
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
    if (controlledRunStatusNow !== undefined || !runStatus?.active) return
    const timer = window.setInterval(() => setLiveRunStatusNow(Date.now()), 1_000)
    return () => window.clearInterval(timer)
  }, [controlledRunStatusNow, runStatus?.active])

  useEffect(() => {
    if (!sessionActions && !(studioMode && onToggleStudioMode)) setMobileActionsOpen(false)
  }, [onToggleStudioMode, sessionActions, studioMode])

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
    <span className="grid min-w-0 max-w-full flex-1 gap-0.5">
      <input
        ref={titleInputRef}
        value={titleDraft}
        disabled={pendingAction === 'rename'}
        aria-label="Conversation title"
        aria-invalid={Boolean(titleError)}
        className="min-w-0 w-full max-w-full rounded border border-[var(--app-border-strong)] bg-[var(--app-bg)] px-1.5 py-0.5 text-[13px] font-semibold text-[var(--app-text)] outline-none focus:ring-2 focus:ring-[var(--app-focus-ring)] sm:text-sm"
        onChange={(event) => { setTitleDraft(event.target.value); setTitleError('') }}
        onKeyDown={(event) => {
          if (event.key === 'Enter') { event.preventDefault(); void saveTitle() }
          if (event.key === 'Escape') { event.preventDefault(); cancelTitleEdit() }
        }}
      />
      {titleError ? <span role="alert" className="text-[10px] font-normal text-[var(--app-danger)]">{titleError}</span> : null}
    </span>
  ) : sessionActions?.onRename ? (
    <button type="button" className="block max-w-full min-w-0 truncate rounded text-left hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]" title={`${displayTitle} — click to rename`} aria-label={`Rename conversation: ${displayTitle}`} onClick={() => { setEditingTitle(true); setTitleDraft(displayTitle); setTitleError('') }}>
      {displayTitle}
    </button>
  ) : <span className="block max-w-full min-w-0 truncate" title={displayTitle}>{displayTitle}</span>

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

        <div className="min-w-0 flex-1 overflow-hidden">
          {!hideMobileIdentity ? (
            <div className="sm:hidden">
              <h1 className="flex min-w-0 items-center overflow-hidden text-[13px] font-semibold leading-tight text-[var(--app-text)]">
                {editableTitle}
              </h1>
            </div>
          ) : null}

          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              {editableTitle}
            </h1>
          </div>
          <div className={`${hideMobileIdentity ? 'hidden sm:flex' : 'mt-1 flex'} min-w-0 max-w-full items-center gap-1.5 overflow-hidden text-[11px] font-medium text-[var(--app-text-muted)]`}>
            {displayBranch ? (
              <>
                <span className="hidden truncate sm:inline" data-testid="desktop-v3-git-branch">{displayBranch}</span>
                <span aria-hidden="true" className="hidden shrink-0 text-[var(--app-text-subtle)] sm:inline">·</span>
              </>
            ) : null}
            <div data-testid="session-workspace-row" className="min-w-0 max-w-full flex items-center overflow-hidden">
              {sessionId ? <SessionAttachments key={sessionId} sessionId={sessionId} /> : (
                <span className="min-w-0 truncate text-[10px] text-[var(--app-text-muted)] sm:text-[11px]" title={displayWorkspace}>{displayWorkspace}</span>
              )}
            </div>
            {resolvedModelLabel ? (
              <>
                <span aria-hidden="true" className="hidden shrink-0 text-[var(--app-text-subtle)] sm:inline">·</span>
                <span className="hidden truncate sm:inline" data-testid="desktop-v3-resolved-model" title={resolvedModelLabel}>{resolvedModelLabel}</span>
              </>
            ) : null}
            {mobileRunTimerLabel ? (
              <span className="shrink-0 justify-self-end text-[10px] tabular-nums text-[var(--app-text)] sm:hidden" title={runStatus?.label}>
                {mobileRunTimerLabel}
              </span>
            ) : null}
          </div>
        </div>
        <div className="hidden sm:block">
          <DesktopV3RunStatusPill model={runStatus} now={runStatusNow} />
        </div>

        {studioMode && onToggleStudioMode ? (
          <button
            type="button"
            className="hidden h-9 shrink-0 touch-manipulation items-center gap-1.5 rounded-xl border border-transparent bg-transparent px-2 text-xs font-medium text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:inline-flex"
            onClick={onToggleStudioMode}
            aria-label={studioMode === 'studio' ? 'Switch to session mode' : 'Switch to Video Studio'}
            aria-pressed={studioMode === 'studio'}
            title={studioMode === 'studio' ? 'Video Studio is on. Switch to session mode.' : 'Open this video session in Video Studio.'}
            data-testid="desktop-v3-video-studio-toggle"
          >
            {studioMode === 'studio' ? <MessageSquare size={15} aria-hidden="true" /> : <Film size={15} aria-hidden="true" />}
            <span>{studioMode === 'studio' ? 'Chat' : 'Studio'}</span>
          </button>
        ) : null}

        {onNewSession ? (
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
            onClick={onNewSession}
            aria-label="New session"
            title="New session"
          >
            <MessageSquare size={19} />
          </button>
        ) : null}

        {sessionActions || (studioMode && onToggleStudioMode) ? (
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
              <span className="absolute right-0 top-full z-50 mt-1 grid min-w-44 gap-0.5 rounded-md border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] p-1 text-[11px] shadow-lg [background-color:var(--app-surface-elevated)]">
                {studioMode && onToggleStudioMode ? (
                  <button
                    type="button"
                    className="inline-flex h-9 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left font-medium text-[var(--app-text)] transition hover:bg-[var(--app-surface-active)] sm:hidden"
                    onClick={() => {
                      setMobileActionsOpen(false)
                      onToggleStudioMode()
                    }}
                  >
                    {studioMode === 'studio' ? <MessageSquare size={14} aria-hidden="true" /> : <Film size={14} aria-hidden="true" />}
                    <span>{studioMode === 'studio' ? 'Open chat' : 'Open Video Studio'}</span>
                  </button>
                ) : null}
                {sessionActions?.canPin ? (
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
                {sessionActions?.onCopyConversation ? (
                  <button type="button" className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:opacity-60" disabled={actionDisabled} onClick={() => { setMobileActionsOpen(false); sessionActions.onCopyConversation?.() }}>
                    {pendingAction === 'copy' ? <LoaderCircle size={13} className="animate-spin" /> : <Clipboard size={13} />}<span>Copy</span>
                  </button>
                ) : null}
                {sessionActions?.onDownloadConversation ? (
                  <button type="button" className="inline-flex h-8 w-full items-center gap-2 rounded border-0 bg-transparent px-2 text-left text-[var(--app-text-subtle)] transition hover:bg-[var(--app-surface-active)] hover:text-[var(--app-text)] disabled:opacity-60" disabled={actionDisabled} onClick={() => { setMobileActionsOpen(false); sessionActions.onDownloadConversation?.() }}>
                    {pendingAction === 'download' ? <LoaderCircle size={13} className="animate-spin" /> : <Download size={13} />}<span>Download</span>
                  </button>
                ) : null}
                {sessionActions ? (
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
                ) : null}
              </span>
            ) : null}
          </span>
        ) : null}
      </div>
    </header>
  )
}
