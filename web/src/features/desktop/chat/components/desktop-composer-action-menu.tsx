import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { AlertTriangle, ChevronLeft, ChevronRight, ListChecks, ListTodo, LoaderCircle, Minimize2, Paperclip, Pin, Plus, Settings, Sparkles, Trash2, Zap } from 'lucide-react'
import { deleteWorkspaceAction, fetchWorkspaceActions, orderWorkspaceActionsForQuickAccess, type WorkspaceAction } from '../../../workspaces/actions/types'
import { deleteWorkspaceSkill, fetchWorkspaceSkills, type WorkspaceSkill } from '../services/workspace-skills'

export type DesktopComposerTaskMode = 'action' | 'plan'

interface DesktopComposerActionMenuProps {
  disabled?: boolean
  onPrimeTask: (mode: DesktopComposerTaskMode) => void
  onAttach?: () => void
  attachDisabled?: boolean
  attaching?: boolean
  contextLabel?: string
  contextTooltip?: string
  onCompact?: () => void
  compactDisabled?: boolean
  workspacePath?: string
  sessionId?: string
  onActionSelect?: (action: WorkspaceAction, confirmedLaunch: boolean) => void
  onOpenActionSettings?: () => void
  onSkillSelect?: (skill: WorkspaceSkill) => void
}

type ComposerActionMenuView = 'root' | 'task' | 'actions' | 'skills'
type PendingDeletion =
  | { kind: 'skill'; item: WorkspaceSkill }
  | { kind: 'action'; item: WorkspaceAction }

const TASK_EXPLANATION = 'Send your next message to a background agent in a managed worktree.'
const ACTIONS_EXPLANATION = 'Actions run workspace scripts and can include custom options you fill in when you launch them.'

export function DesktopComposerActionMenu({
  disabled = false,
  onPrimeTask,
  onAttach,
  attachDisabled = false,
  attaching = false,
  contextLabel = '',
  contextTooltip = '',
  onCompact,
  compactDisabled = false,
  workspacePath = '',
  sessionId = '',
  onActionSelect,
  onOpenActionSettings,
  onSkillSelect,
}: DesktopComposerActionMenuProps) {
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<ComposerActionMenuView>('root')
  const menuId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const deletionPendingRef = useRef(false)
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [actionsLoading, setActionsLoading] = useState(false)
  const [actionsError, setActionsError] = useState('')
  const [actionsRequest, setActionsRequest] = useState(0)
  const [skills, setSkills] = useState<WorkspaceSkill[]>([])
  const [skillsLoading, setSkillsLoading] = useState(false)
  const [skillsError, setSkillsError] = useState('')
  const [skillsRequest, setSkillsRequest] = useState(0)
  const [deletingItem, setDeletingItem] = useState('')
  const [deleteError, setDeleteError] = useState('')
  const [pendingDeletion, setPendingDeletion] = useState<PendingDeletion | null>(null)
  const [armedActionId, setArmedActionId] = useState('')
  const [submenuMinHeight, setSubmenuMinHeight] = useState(0)

  const closeMenu = useCallback(() => {
    setOpen(false)
    setView('root')
    setDeleteError('')
    setPendingDeletion(null)
    setArmedActionId('')
    setSubmenuMinHeight(0)
  }, [])

  useEffect(() => {
    if (!open) return

    const handlePointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) closeMenu()
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      if (pendingDeletion) {
        setPendingDeletion(null)
        setDeleteError('')
        return
      }
      closeMenu()
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [closeMenu, open, pendingDeletion])

  useEffect(() => {
    if (disabled) closeMenu()
  }, [closeMenu, disabled])

  useEffect(() => {
    setArmedActionId('')
  }, [workspacePath])

  useEffect(() => {
    if (!open || view !== 'actions' || !workspacePath.trim()) return
    const controller = new AbortController()
    setActionsLoading(true)
    setActionsError('')
    void fetchWorkspaceActions(workspacePath, controller.signal, sessionId)
      .then(setActions)
      .catch((error) => {
        if (!controller.signal.aborted) setActionsError(error instanceof Error ? error.message : 'Could not load Actions.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setActionsLoading(false)
      })
    return () => controller.abort()
  }, [actionsRequest, open, sessionId, view, workspacePath])

  useEffect(() => {
    if (!open || view !== 'skills' || !workspacePath.trim()) return
    const controller = new AbortController()
    setSkillsLoading(true)
    setSkillsError('')
    void fetchWorkspaceSkills(workspacePath, controller.signal)
      .then(setSkills)
      .catch((error) => {
        if (!controller.signal.aborted) setSkillsError(error instanceof Error ? error.message : 'Could not load Skills.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setSkillsLoading(false)
      })
    return () => controller.abort()
  }, [open, skillsRequest, view, workspacePath])

  const toggleMenu = () => {
    if (open) {
      closeMenu()
      return
    }
    setView('root')
    setDeleteError('')
    setSubmenuMinHeight(0)
    setOpen(true)
  }

  const showView = (nextView: Exclude<ComposerActionMenuView, 'root'>) => {
    setSubmenuMinHeight(nextView === 'skills' || nextView === 'actions' ? (menuRef.current?.getBoundingClientRect().height ?? 0) : 0)
    setDeleteError('')
    setPendingDeletion(null)
    setArmedActionId('')
    setView(nextView)
  }

  const showRootView = () => {
    setView('root')
    setDeleteError('')
    setPendingDeletion(null)
    setArmedActionId('')
    setSubmenuMinHeight(0)
  }

  const primeTask = (mode: DesktopComposerTaskMode) => {
    closeMenu()
    onPrimeTask(mode)
  }

  const attach = () => {
    closeMenu()
    onAttach?.()
  }

  const compact = () => {
    closeMenu()
    onCompact?.()
  }

  const deleteSkill = async (skill: WorkspaceSkill) => {
    if (deletionPendingRef.current) return false
    deletionPendingRef.current = true
    const pendingKey = `skill:${skill.canonicalName}`
    setDeletingItem(pendingKey)
    setDeleteError('')
    try {
      await deleteWorkspaceSkill(workspacePath, skill.canonicalName)
      setSkills((current) => current.filter((candidate) => candidate.canonicalName !== skill.canonicalName))
      return true
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : 'Could not delete Skill.')
      return false
    } finally {
      deletionPendingRef.current = false
      setDeletingItem('')
    }
  }

  const deleteAction = async (action: WorkspaceAction) => {
    if (deletionPendingRef.current) return false
    deletionPendingRef.current = true
    const pendingKey = `action:${action.id}`
    setDeletingItem(pendingKey)
    setDeleteError('')
    try {
      await deleteWorkspaceAction(workspacePath, action.id)
      setActions((current) => current.filter((candidate) => candidate.id !== action.id))
      return true
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : 'Could not delete Action.')
      return false
    } finally {
      deletionPendingRef.current = false
      setDeletingItem('')
    }
  }

  const requestDelete = (deletion: PendingDeletion) => {
    if (deletionPendingRef.current) return
    setDeleteError('')
    setArmedActionId('')
    setPendingDeletion(deletion)
  }

  const openActionSettings = () => {
    closeMenu()
    onOpenActionSettings?.()
  }

  const selectAction = (action: WorkspaceAction) => {
    setDeleteError('')
    setPendingDeletion(null)
    if (action.inputs.length > 0) {
      setArmedActionId('')
      closeMenu()
      onActionSelect?.(action, false)
      return
    }
    if (armedActionId !== action.id) {
      setArmedActionId(action.id)
      return
    }
    setArmedActionId('')
    closeMenu()
    onActionSelect?.(action, true)
  }

  const confirmDelete = async () => {
    const deletion = pendingDeletion
    if (!deletion || deletionPendingRef.current) return
    const deleted = deletion.kind === 'skill'
      ? await deleteSkill(deletion.item)
      : await deleteAction(deletion.item)
    if (deleted) setPendingDeletion(null)
  }

  const showCompactAction = Boolean(contextLabel || contextTooltip || onCompact)

  return (
    <div ref={rootRef} className="relative shrink-0 self-end pb-0.5" data-testid="desktop-composer-action-menu">
      <button
        type="button"
        disabled={disabled}
        aria-label="Open composer actions"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={toggleMenu}
        className="inline-flex h-9 w-9 items-center justify-center rounded-lg border-0 bg-transparent p-0 text-[var(--app-text-muted)] shadow-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-0 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Plus size={18} aria-hidden="true" />
      </button>

      {open ? (
        <div
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label={view === 'task' ? 'Task type' : view === 'actions' ? 'Workspace Actions' : view === 'skills' ? 'Workspace Skills' : 'Composer actions'}
          className="absolute bottom-full left-0 z-40 mb-2 w-[min(18rem,calc(100vw-2rem))] overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 shadow-[var(--shadow-panel)]"
          style={submenuMinHeight > 0 ? { minHeight: submenuMinHeight } : undefined}
          data-testid="desktop-composer-actions-menu"
        >
          {view === 'root' ? (
            <>
              {workspacePath.trim() && onActionSelect ? (
                <button
                  type="button"
                  role="menuitem"
                  aria-haspopup="menu"
                  onClick={() => showView('actions')}
                  className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)]"
                  data-testid="desktop-composer-actions-menu-item"
                >
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                    <Zap size={16} aria-hidden="true" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-semibold">Actions</span>
                    <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Run a saved workspace script</span>
                  </span>
                  <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
                </button>
              ) : null}

              {workspacePath.trim() && onSkillSelect ? (
                <button
                  type="button"
                  role="menuitem"
                  aria-haspopup="menu"
                  onClick={() => showView('skills')}
                  className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)]"
                  data-testid="desktop-composer-skills-menu-item"
                >
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                    <Sparkles size={16} aria-hidden="true" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-semibold">Skills</span>
                    <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Guide the AI with a saved skill</span>
                  </span>
                  <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
                </button>
              ) : null}

              <button
                type="button"
                role="menuitem"
                aria-haspopup="menu"
                onClick={() => showView('task')}
                className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)]"
                data-testid="desktop-composer-task-menu-item"
              >
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                  <ListTodo size={16} aria-hidden="true" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block font-semibold">Task</span>
                  <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Run work in the background</span>
                </span>
                <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
              </button>

              {onAttach ? (
                <button
                  type="button"
                  role="menuitem"
                  onClick={attach}
                  disabled={attachDisabled || attaching}
                  className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  data-testid="desktop-composer-attach-menu-item"
                >
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                    {attaching ? <LoaderCircle size={16} className="animate-spin" aria-hidden="true" /> : <Paperclip size={16} aria-hidden="true" />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-semibold">Attach</span>
                    <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Add images, Markdown, or code/text files</span>
                  </span>
                </button>
              ) : null}

              {showCompactAction ? (
                <button
                  type="button"
                  role="menuitem"
                  onClick={compact}
                  disabled={compactDisabled || !onCompact}
                  title={contextTooltip || 'Compact conversation'}
                  aria-label={contextTooltip || 'Compact conversation'}
                  className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  data-testid="desktop-composer-compact-menu-item"
                >
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                    <Minimize2 size={16} aria-hidden="true" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block font-semibold">Compact</span>
                    <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Context window · {contextLabel || 'unavailable'}</span>
                  </span>
                </button>
              ) : null}
            </>
          ) : view === 'task' ? (
            <div data-testid="desktop-composer-task-submenu">
              <button
                type="button"
                onClick={showRootView}
                className="mb-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-semibold text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)]"
                aria-label="Back to composer actions"
              >
                <ChevronLeft size={15} aria-hidden="true" />
                <span>Task</span>
              </button>
              <p className="px-2.5 pb-2 text-[11px] leading-4 text-[var(--app-text-subtle)]">{TASK_EXPLANATION}</p>
              <button
                type="button"
                role="menuitem"
                onClick={() => primeTask('plan')}
                className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)]"
              >
                <ListChecks size={17} className="shrink-0" aria-hidden="true" />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">Plan</span>
                  <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Review the approach before work starts</span>
                </span>
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => primeTask('action')}
                className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)]"
              >
                <ListTodo size={17} className="shrink-0" aria-hidden="true" />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">Action</span>
                  <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Start the work right away</span>
                </span>
              </button>
            </div>
          ) : view === 'skills' ? (
            <div data-testid="desktop-composer-skills-submenu">
              <button type="button" onClick={showRootView} className="mb-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-semibold text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)]" aria-label="Back to composer actions">
                <ChevronLeft size={15} aria-hidden="true" />
                <span>Back</span>
                <span className="text-[var(--app-text-subtle)]" aria-hidden="true">·</span>
                <span className="text-[var(--app-text)]">Skills</span>
              </button>
              {skillsLoading ? (
                <div className="flex items-center gap-2 px-2.5 py-4 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={15} className="animate-spin" />Loading Skills…</div>
              ) : skillsError ? (
                <div className="px-2.5 py-3" role="alert">
                  <p className="text-xs text-[var(--app-danger)]">{skillsError}</p>
                  <button type="button" onClick={() => setSkillsRequest((value) => value + 1)} className="mt-2 text-xs font-semibold text-[var(--app-primary)]">Try again</button>
                </div>
              ) : skills.length === 0 ? (
                <div className="mx-2.5 my-3 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-6 text-center">
                  <p className="text-xs font-medium leading-5 text-[var(--app-text-muted)]">Ask Swarm to help you manage your skills.</p>
                </div>
              ) : skills.map((skill) => {
                const pendingKey = `skill:${skill.canonicalName}`
                const deleting = deletingItem === pendingKey
                const confirming = pendingDeletion?.kind === 'skill' && pendingDeletion.item.canonicalName === skill.canonicalName
                return (
                  <div key={skill.canonicalName}>
                    <div className="group relative flex items-center rounded-lg hover:bg-[var(--app-surface-hover)] focus-within:bg-[var(--app-surface-hover)]">
                      <button type="button" role="menuitem" onClick={() => { closeMenu(); onSkillSelect?.(skill) }} disabled={Boolean(deletingItem)} className="flex min-w-0 flex-1 items-center rounded-lg py-2.5 pl-2.5 pr-10 text-left text-sm text-[var(--app-text)] outline-none disabled:cursor-not-allowed disabled:opacity-60">
                        <span className="min-w-0 flex-1 truncate font-semibold">{skill.name}</span>
                      </button>
                      <button type="button" aria-label={`Delete Skill ${skill.name}`} title="Delete Skill" disabled={Boolean(deletingItem)} onClick={() => requestDelete({ kind: 'skill', item: skill })} className="absolute right-1.5 grid h-7 w-7 place-items-center rounded-md text-[var(--app-text-subtle)] opacity-0 transition-opacity hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] focus-visible:opacity-100 focus-visible:outline-none group-hover:opacity-100 group-focus-within:opacity-100 disabled:cursor-not-allowed disabled:opacity-50">
                        {deleting ? <LoaderCircle size={14} className="animate-spin" aria-hidden="true" /> : <Trash2 size={14} aria-hidden="true" />}
                      </button>
                    </div>
                    {confirming ? (
                      <div className="mx-1 mb-1 rounded-lg border border-[var(--app-danger)]/40 bg-[var(--app-danger-bg)] px-2.5 py-2" role="alertdialog" aria-label={`Confirm deletion of Skill ${skill.name}`}>
                        <div className="flex items-start gap-2">
                          <AlertTriangle size={14} className="mt-0.5 shrink-0 text-[var(--app-danger)]" aria-hidden="true" />
                          <div className="min-w-0 flex-1">
                            <p className="text-xs font-semibold text-[var(--app-text)]">Delete Skill “{skill.name}”?</p>
                            <p className="mt-0.5 text-[11px] leading-4 text-[var(--app-text-muted)]">This deletes the Skill itself from the workspace.</p>
                            {deleteError ? <p className="mt-1 text-[11px] leading-4 text-[var(--app-danger)]" role="alert">{deleteError}</p> : null}
                            <div className="mt-2 flex justify-end gap-1.5">
                              <button type="button" disabled={deleting} onClick={() => { setPendingDeletion(null); setDeleteError('') }} className="rounded-md px-2 py-1 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50">No</button>
                              <button type="button" disabled={deleting} onClick={() => { void confirmDelete() }} className="inline-flex min-w-10 items-center justify-center gap-1 rounded-md bg-[var(--app-danger)] px-2 py-1 text-xs font-semibold text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60">
                                {deleting ? <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> : null} Yes
                              </button>
                            </div>
                          </div>
                        </div>
                      </div>
                    ) : null}
                  </div>
                )
              })}
            </div>
          ) : (
            <div data-testid="desktop-composer-actions-submenu">
              <button type="button" onClick={showRootView} className="mb-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-semibold text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)]" aria-label="Back to composer actions">
                <ChevronLeft size={15} aria-hidden="true" />
                <span>Back</span>
                <span className="text-[var(--app-text-subtle)]" aria-hidden="true">·</span>
                <span className="text-[var(--app-text)]">Actions</span>
              </button>
              {!actionsLoading && !actionsError && actions.length === 0 ? (
                <div className="mx-2.5 mb-3 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-3 py-3 text-center">
                  <p className="text-xs leading-5 text-[var(--app-text-muted)]">{ACTIONS_EXPLANATION}</p>
                  <p className="mt-1 text-xs font-medium leading-5 text-[var(--app-text)]">Ask Swarm to help you create or manage Actions.</p>
                </div>
              ) : null}
              {actionsLoading ? (
                <div className="flex items-center gap-2 px-2.5 py-4 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={15} className="animate-spin" />Loading Actions…</div>
              ) : actionsError ? (
                <div className="px-2.5 py-3" role="alert">
                  <p className="text-xs text-[var(--app-danger)]">{actionsError}</p>
                  <button type="button" onClick={() => setActionsRequest((value) => value + 1)} className="mt-2 text-xs font-semibold text-[var(--app-primary)]">Try again</button>
                </div>
              ) : actions.length === 0 ? (
                <p className="px-2.5 py-3 text-center text-xs text-[var(--app-text-subtle)]">No Actions are saved for this workspace.</p>
              ) : orderWorkspaceActionsForQuickAccess(actions).map((action) => {
                const pendingKey = `action:${action.id}`
                const deleting = deletingItem === pendingKey
                const confirming = pendingDeletion?.kind === 'action' && pendingDeletion.item.id === action.id
                const armed = armedActionId === action.id
                return (
                  <div key={action.id}>
                    <div className="group relative flex items-center rounded-lg hover:bg-[var(--app-surface-hover)] focus-within:bg-[var(--app-surface-hover)]">
                      <button type="button" role="menuitem" onClick={() => selectAction(action)} disabled={Boolean(deletingItem)} aria-label={armed ? `Run ${action.name}?` : action.name} className="flex min-w-0 flex-1 items-center rounded-lg py-2.5 pl-2.5 pr-10 text-left text-sm text-[var(--app-text)] outline-none disabled:cursor-not-allowed disabled:opacity-60">
                        <span className="min-w-0 flex-1 truncate font-semibold">{armed ? 'Run?' : action.name}</span>
                        {action.pinned ? <Pin size={12} className="ml-2 shrink-0 fill-current text-[var(--app-primary)]" aria-label="Pinned" /> : null}
                      </button>
                      <button type="button" aria-label={`Delete Action ${action.name}`} title="Delete Action" disabled={Boolean(deletingItem)} onClick={() => requestDelete({ kind: 'action', item: action })} className="absolute right-1.5 grid h-7 w-7 place-items-center rounded-md text-[var(--app-text-subtle)] opacity-0 transition-opacity hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] focus-visible:opacity-100 focus-visible:outline-none group-hover:opacity-100 group-focus-within:opacity-100 disabled:cursor-not-allowed disabled:opacity-50">
                        {deleting ? <LoaderCircle size={14} className="animate-spin" aria-hidden="true" /> : <Trash2 size={14} aria-hidden="true" />}
                      </button>
                    </div>
                    {confirming ? (
                      <div className="mx-1 mb-1 rounded-lg border border-[var(--app-danger)]/40 bg-[var(--app-danger-bg)] px-2.5 py-2" role="alertdialog" aria-label={`Confirm deletion of Action ${action.name}`}>
                        <div className="flex items-start gap-2">
                          <AlertTriangle size={14} className="mt-0.5 shrink-0 text-[var(--app-danger)]" aria-hidden="true" />
                          <div className="min-w-0 flex-1">
                            <p className="text-xs font-semibold text-[var(--app-text)]">Delete Action “{action.name}”?</p>
                            <p className="mt-0.5 text-[11px] leading-4 text-[var(--app-text-muted)]">This removes the Action from Swarm. It does not delete the script.</p>
                            {deleteError ? <p className="mt-1 text-[11px] leading-4 text-[var(--app-danger)]" role="alert">{deleteError}</p> : null}
                            <div className="mt-2 flex justify-end gap-1.5">
                              <button type="button" disabled={deleting} onClick={() => { setPendingDeletion(null); setDeleteError('') }} className="rounded-md px-2 py-1 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50">No</button>
                              <button type="button" disabled={deleting} onClick={() => { void confirmDelete() }} className="inline-flex min-w-10 items-center justify-center gap-1 rounded-md bg-[var(--app-danger)] px-2 py-1 text-xs font-semibold text-white hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60">
                                {deleting ? <LoaderCircle size={12} className="animate-spin" aria-hidden="true" /> : null} Yes
                              </button>
                            </div>
                          </div>
                        </div>
                      </div>
                    ) : null}
                  </div>
                )
              })}
              {onOpenActionSettings ? (
                <button type="button" onClick={openActionSettings} className="mt-1 flex w-full items-center gap-2.5 border-t border-[var(--app-border)] px-2.5 pt-2.5 pb-2 text-left text-xs font-semibold text-[var(--app-text-muted)] outline-none transition-colors hover:text-[var(--app-text)] focus-visible:text-[var(--app-text)]" data-testid="desktop-composer-manage-actions">
                  <Settings size={14} aria-hidden="true" />
                  Manage Actions in Settings
                </button>
              ) : null}
            </div>
          )}
        </div>
      ) : null}

    </div>
  )
}
