import { useMemo, useState, type DragEvent, useRef, useEffect, useLayoutEffect, type FocusEvent } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, Copy, GripVertical, ListChecks, Play, Plus, Trash2, X } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Input } from '../../../../components/ui/input'
import { Select } from '../../../../components/ui/select'
import { Textarea } from '../../../../components/ui/textarea'
import type { WorkspaceTodoItem, WorkspaceTodoOwnerKind, WorkspaceTodoPriority, WorkspaceTodoSummary } from '../types'

interface WorkspaceTodoModalSection {
  ownerKind: WorkspaceTodoOwnerKind
  title: string
  description: string
  emptyText: string
  items: WorkspaceTodoItem[]
  summary: WorkspaceTodoSummary
}

interface WorkspaceTodoModalProps {
  open: boolean
  workspaceName: string
  userSection: WorkspaceTodoModalSection
  saving: boolean
  onOpenChange: (open: boolean) => void
  onCreate: (ownerKind: WorkspaceTodoOwnerKind, input: { text: string; priority: WorkspaceTodoPriority; group: string; tags: string[]; sessionId?: string; parentId?: string }) => Promise<void> | void
  onToggleDone: (item: WorkspaceTodoItem, done: boolean) => Promise<void> | void
  onToggleInProgress: (item: WorkspaceTodoItem, inProgress: boolean) => Promise<void> | void
  onUpdate: (item: WorkspaceTodoItem, patch: Partial<Pick<WorkspaceTodoItem, 'text' | 'priority' | 'group' | 'tags'>>) => Promise<void> | void
  onDelete: (item: WorkspaceTodoItem) => Promise<void> | void
  onDeleteDone: (ownerKind: WorkspaceTodoOwnerKind) => Promise<void> | void
  onDeleteAll: (ownerKind: WorkspaceTodoOwnerKind) => Promise<void> | void
  onReorder: (ownerKind: WorkspaceTodoOwnerKind, orderedIDs: string[]) => Promise<void> | void
}

const PRIORITY_GROUPS: WorkspaceTodoPriority[] = ['urgent', 'high', 'medium', 'low']
const TODO_DRAG_MIME = 'application/x-swarm-workspace-todo'
const BULK_MENU_WIDTH = 180

function formatSummary(summary: WorkspaceTodoSummary): string {
  if (summary.taskCount <= 0) return 'No tasks'
  return `${summary.openCount} open • ${summary.inProgressCount} in progress • ${summary.taskCount} total`
}

function formatPriorityLabel(priority: WorkspaceTodoPriority): string {
  return priority.charAt(0).toUpperCase() + priority.slice(1)
}

function createPriorityBuckets(): Record<WorkspaceTodoPriority, WorkspaceTodoItem[]> {
  return {
    urgent: [],
    high: [],
    medium: [],
    low: [],
  }
}

export function WorkspaceTodoModal({
  open,
  workspaceName,
  userSection,
  saving,
  onOpenChange,
  onCreate,
  onToggleDone,
  onToggleInProgress,
  onUpdate,
  onDelete,
  onDeleteDone,
  onDeleteAll,
  onReorder,
}: WorkspaceTodoModalProps) {
  const [isAdding, setIsAdding] = useState(false)
  const [draftText, setDraftText] = useState('')
  const [draftPriority, setDraftPriority] = useState<WorkspaceTodoPriority>('medium')
  const [draggedTodoID, setDraggedTodoID] = useState<string | null>(null)
  const [bulkMenuOpen, setBulkMenuOpen] = useState(false)
  const [confirmDeleteAllOwner, setConfirmDeleteAllOwner] = useState<WorkspaceTodoOwnerKind | null>(null)
  const [focusedTodoID, setFocusedTodoID] = useState<string | null>(null)
  const [focusedTodoDraft, setFocusedTodoDraft] = useState('')
  const [copyFeedback, setCopyFeedback] = useState<{ id: string; status: 'copied' | 'error' } | null>(null)
  const addInputRef = useRef<HTMLInputElement>(null)
  const bulkTriggerRef = useRef<HTMLButtonElement | null>(null)
  const bulkMenuRef = useRef<HTMLDivElement | null>(null)
  const copyFeedbackTimeoutRef = useRef<number | null>(null)
  const [bulkMenuPosition, setBulkMenuPosition] = useState<{ top: number; left: number } | null>(null)

  const items = useMemo(() => [...userSection.items], [userSection.items])
  const summary = userSection.summary
  const userCompletedCount = useMemo(() => userSection.items.filter((item) => item.done).length, [userSection.items])

  const clearFocusedTodo = () => {
    setFocusedTodoID(null)
    setFocusedTodoDraft('')
  }

  const showCopyFeedback = (id: string, status: 'copied' | 'error') => {
    if (copyFeedbackTimeoutRef.current !== null) {
      window.clearTimeout(copyFeedbackTimeoutRef.current)
    }
    setCopyFeedback({ id, status })
    copyFeedbackTimeoutRef.current = window.setTimeout(() => {
      setCopyFeedback((current) => (current?.id === id ? null : current))
      copyFeedbackTimeoutRef.current = null
    }, 1500)
  }

  const persistTodoText = (item: WorkspaceTodoItem, nextDraft: string) => {
    const nextText = nextDraft.trim()
    if (nextText === '' || nextText === item.text) {
      return
    }
    void onUpdate(item, { text: nextText })
  }

  const focusTodo = (item: WorkspaceTodoItem) => {
    if (focusedTodoID === item.id) {
      return
    }
    setFocusedTodoID(item.id)
    setFocusedTodoDraft(item.text)
  }

  const handleTodoBlur = (event: FocusEvent<HTMLDivElement>, item: WorkspaceTodoItem) => {
    const nextTarget = event.relatedTarget as Node | null
    if (nextTarget && event.currentTarget.contains(nextTarget)) {
      return
    }
    if (focusedTodoID !== item.id) {
      return
    }
    persistTodoText(item, focusedTodoDraft)
    clearFocusedTodo()
  }

  const handleCopyTodo = async (item: WorkspaceTodoItem) => {
    if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
      showCopyFeedback(item.id, 'error')
      return
    }
    try {
      await navigator.clipboard.writeText(item.text)
      showCopyFeedback(item.id, 'copied')
    } catch {
      showCopyFeedback(item.id, 'error')
    }
  }

  useEffect(() => {
    if (isAdding && addInputRef.current) {
      addInputRef.current.focus()
    }
  }, [isAdding])

  useEffect(() => {
    return () => {
      if (copyFeedbackTimeoutRef.current !== null) {
        window.clearTimeout(copyFeedbackTimeoutRef.current)
      }
    }
  }, [])

  useLayoutEffect(() => {
    if (!bulkMenuOpen || !bulkTriggerRef.current) {
      setBulkMenuPosition(null)
      return
    }
    const rect = bulkTriggerRef.current.getBoundingClientRect()
    setBulkMenuPosition({
      top: rect.bottom + 6,
      left: Math.max(8, rect.left),
    })
  }, [bulkMenuOpen])

  useEffect(() => {
    if (!bulkMenuOpen) return
    function reposition() {
      if (!bulkTriggerRef.current) return
      const rect = bulkTriggerRef.current.getBoundingClientRect()
      setBulkMenuPosition({
        top: rect.bottom + 6,
        left: Math.max(8, rect.left),
      })
    }
    window.addEventListener('scroll', reposition, true)
    window.addEventListener('resize', reposition)
    return () => {
      window.removeEventListener('scroll', reposition, true)
      window.removeEventListener('resize', reposition)
    }
  }, [bulkMenuOpen])

  useEffect(() => {
    if (!bulkMenuOpen) {
      setConfirmDeleteAllOwner(null)
      return
    }
    function handleClickOutside(event: MouseEvent) {
      const target = event.target as Node
      if (bulkTriggerRef.current?.contains(target) || bulkMenuRef.current?.contains(target)) {
        return
      }
      setBulkMenuOpen(false)
      setConfirmDeleteAllOwner(null)
    }
    function handleEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setBulkMenuOpen(false)
        setConfirmDeleteAllOwner(null)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [bulkMenuOpen])

  useEffect(() => {
    if (!open) {
      setBulkMenuOpen(false)
      setConfirmDeleteAllOwner(null)
      clearFocusedTodo()
      setCopyFeedback(null)
    }
  }, [open])

  useEffect(() => {
    if (focusedTodoID && !items.some((item) => item.id === focusedTodoID)) {
      clearFocusedTodo()
    }
  }, [items, focusedTodoID])

  const handleCreate = (ownerKind: WorkspaceTodoOwnerKind) => {
    const text = draftText.trim()
    if (!text) return
    void onCreate(ownerKind, {
      text,
      priority: draftPriority,
      group: '',
      tags: [],
    })
    setDraftText('')
  }

  const buildOrderedIDs = (sourceID: string, destinationPriority: WorkspaceTodoPriority, ownerKind: WorkspaceTodoOwnerKind, targetID?: string) => {
    const sectionItems = items.filter((item) => item.ownerKind === ownerKind)
    const sourceItem = sectionItems.find((item) => item.id === sourceID)
    if (!sourceItem) {
      return null
    }

    const buckets = createPriorityBuckets()
    for (const item of sectionItems) {
      if (item.id === sourceID) {
        continue
      }
      buckets[item.priority].push(item)
    }

    const destinationItems = buckets[destinationPriority]
    const targetIndex = targetID ? destinationItems.findIndex((item) => item.id === targetID) : -1
    destinationItems.splice(targetIndex >= 0 ? targetIndex : destinationItems.length, 0, sourceItem)

    return PRIORITY_GROUPS.flatMap((priority) => buckets[priority].map((item) => item.id))
  }

  const moveTask = async (sourceID: string, destinationPriority: WorkspaceTodoPriority, ownerKind: WorkspaceTodoOwnerKind, targetID?: string) => {
    const sourceItem = items.find((item) => item.id === sourceID)
    if (!sourceItem || sourceItem.ownerKind !== ownerKind) {
      return
    }
    if (sourceItem.priority === destinationPriority && targetID === sourceItem.id) {
      return
    }

    const orderedIDs = buildOrderedIDs(sourceID, destinationPriority, ownerKind, targetID)
    if (!orderedIDs) {
      return
    }

    if (sourceItem.priority !== destinationPriority) {
      await onUpdate(sourceItem, { priority: destinationPriority })
    }
    await onReorder(ownerKind, orderedIDs)
  }

  const handleItemDrop = (event: DragEvent<HTMLDivElement>, targetItem: WorkspaceTodoItem) => {
    event.preventDefault()
    event.stopPropagation()
    const sourceID = event.dataTransfer.getData(TODO_DRAG_MIME).trim()
    if (!sourceID) return
    const sourceItem = items.find((item) => item.id === sourceID)
    if (!sourceItem || sourceItem.ownerKind !== targetItem.ownerKind || targetItem.ownerKind !== 'user') return
    setDraggedTodoID(null)
    void moveTask(sourceID, targetItem.priority, targetItem.ownerKind, targetItem.id)
  }

  const handlePriorityDrop = (event: DragEvent<HTMLDivElement>, priority: WorkspaceTodoPriority, ownerKind: WorkspaceTodoOwnerKind) => {
    event.preventDefault()
    const sourceID = event.dataTransfer.getData(TODO_DRAG_MIME).trim()
    if (!sourceID) return
    const sourceItem = items.find((item) => item.id === sourceID)
    if (!sourceItem || sourceItem.ownerKind !== ownerKind || ownerKind !== 'user') return
    setDraggedTodoID(null)
    void moveTask(sourceID, priority, ownerKind)
  }

  const renderTodoRow = (item: WorkspaceTodoItem, options: { allowDrag: boolean; childCount?: number }) => {
    const isFocused = focusedTodoID === item.id
    const copyStatus = copyFeedback?.id === item.id ? copyFeedback.status : null
    const metaParts: string[] = []

    if (item.group.trim()) {
      metaParts.push(item.group.trim())
    }
    if (item.ownerKind === 'agent' && (options.childCount ?? 0) > 0) {
      metaParts.push(`${options.childCount} substep${options.childCount === 1 ? '' : 's'}`)
    }
    if (item.ownerKind === 'user' && item.tags.length > 0) {
      metaParts.push(item.tags.map((tag) => `#${tag}`).join(' '))
    }

    return (
      <div
        key={item.id}
        draggable={options.allowDrag ? !isFocused : undefined}
        onDragStart={options.allowDrag ? (event) => {
          event.dataTransfer.effectAllowed = 'move'
          event.dataTransfer.setData(TODO_DRAG_MIME, item.id)
          event.dataTransfer.setData('text/plain', item.text)
          setDraggedTodoID(item.id)
        } : undefined}
        onDragEnd={options.allowDrag ? () => setDraggedTodoID(null) : undefined}
        onDragOver={options.allowDrag ? (event) => {
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
        } : undefined}
        onDrop={options.allowDrag ? (event) => handleItemDrop(event, item) : undefined}
        onBlurCapture={(event) => handleTodoBlur(event, item)}
        className={[
          'group flex items-start gap-3 rounded-lg px-2 py-2',
          item.inProgress
            ? 'border border-[var(--app-primary)]/40 bg-[var(--app-surface)]'
            : 'border border-transparent hover:bg-[var(--app-surface)]/60',
          draggedTodoID === item.id ? 'opacity-60' : '',
          isFocused ? 'bg-[var(--app-surface-subtle)]/60' : '',
        ].filter(Boolean).join(' ')}
      >
        <div className="flex shrink-0 items-center justify-center pt-1">
          <button
            type="button"
            onClick={() => onToggleDone(item, !item.done)}
            className={`flex size-4 items-center justify-center rounded-[3px] border ${
              item.done
                ? 'border-[var(--app-text)] bg-[var(--app-text)] text-[var(--app-surface)]'
                : 'border-[var(--app-text-muted)] bg-[var(--app-surface)] hover:border-[var(--app-text)]'
            } transition-colors`}
          >
            {item.done && <Check size={12} strokeWidth={3} />}
          </button>
        </div>

        <div className="min-w-0 flex-1">
          {isFocused ? (
            <>
              <Textarea
                value={focusedTodoDraft}
                onChange={(event) => setFocusedTodoDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Escape') {
                    event.preventDefault()
                    clearFocusedTodo()
                    return
                  }
                  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
                    event.preventDefault()
                    persistTodoText(item, focusedTodoDraft)
                    clearFocusedTodo()
                  }
                }}
                autoFocus
                rows={Math.max(4, Math.min(12, focusedTodoDraft.split('\n').length + 1))}
                className={`min-h-[96px] resize-y bg-[var(--app-surface-subtle)] text-sm leading-5 ${
                  item.done ? 'text-[var(--app-text-muted)] line-through' : 'font-medium text-[var(--app-text)]'
                }`}
              />
              <p className="px-1 pt-1 text-[11px] text-[var(--app-text-subtle)]">Full todo visible. Click away to save.</p>
            </>
          ) : (
            <button
              type="button"
              onClick={() => focusTodo(item)}
              onFocus={() => focusTodo(item)}
              className="block w-full rounded-lg px-1 py-1 text-left transition hover:bg-[var(--app-surface-subtle)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
              title="Focus to view and edit the full todo"
            >
              <span
                className={`line-clamp-3 whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm leading-5 ${
                  item.done ? 'text-[var(--app-text-muted)] line-through' : 'font-medium text-[var(--app-text)]'
                }`}
              >
                {item.text}
              </span>
              {metaParts.length > 0 ? (
                <span className="mt-1 block text-[11px] text-[var(--app-text-subtle)]">
                  {metaParts.join(' • ')}
                </span>
              ) : null}
            </button>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-1 self-start">
          <button
            type="button"
            onClick={() => void handleCopyTodo(item)}
            className={`flex size-6 items-center justify-center rounded transition-colors hover:bg-[var(--app-surface-subtle)] ${
              copyStatus === 'copied'
                ? 'text-emerald-500'
                : copyStatus === 'error'
                  ? 'text-red-500'
                  : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
            }`}
            title={copyStatus === 'copied' ? 'Copied' : copyStatus === 'error' ? 'Copy failed' : 'Copy task'}
            aria-label={copyStatus === 'copied' ? 'Copied' : copyStatus === 'error' ? 'Copy failed' : 'Copy task'}
          >
            {copyStatus === 'copied' ? <Check size={14} className="shrink-0" /> : copyStatus === 'error' ? <X size={14} className="shrink-0" /> : <Copy size={14} className="shrink-0" />}
          </button>
          <button
            type="button"
            onClick={() => onToggleInProgress(item, !item.inProgress)}
            className={`flex size-6 items-center justify-center rounded transition-colors hover:bg-[var(--app-surface-subtle)] ${
              item.inProgress ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-subtle)] hover:text-[var(--app-text-muted)]'
            }`}
            title={item.inProgress ? 'Clear in-progress' : 'Mark in progress'}
          >
            <Play size={14} className={item.inProgress ? 'shrink-0 fill-current' : 'shrink-0'} />
          </button>
          <button
            type="button"
            onClick={() => onDelete(item)}
            className="flex size-6 items-center justify-center rounded text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-subtle)] hover:text-red-500"
            title="Delete task"
          >
            <Trash2 size={14} className="shrink-0" />
          </button>
          {options.allowDrag ? (
            <div className={`flex size-6 items-center justify-center rounded text-[var(--app-text-muted)] transition-colors ${isFocused ? 'opacity-30' : 'cursor-grab hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)]'}`}>
              <GripVertical size={14} className="shrink-0" />
            </div>
          ) : null}
        </div>
      </div>
    )
  }

  const bulkMenu = bulkMenuOpen && bulkMenuPosition
    ? createPortal(
        <div
          ref={bulkMenuRef}
          style={{
            position: 'fixed',
            top: `${bulkMenuPosition.top}px`,
            left: `${bulkMenuPosition.left}px`,
            width: `${BULK_MENU_WIDTH}px`,
            zIndex: 9999,
          }}
        >
          <div className="overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-xl shadow-black/30">
            <div className="flex flex-col py-1">
              <button
                type="button"
                onClick={() => {
                  setBulkMenuOpen(false)
                  setConfirmDeleteAllOwner(null)
                  void onDeleteDone('user')
                }}
                disabled={saving || userCompletedCount === 0}
                className="flex w-full items-center justify-between px-3 py-2 text-left text-sm text-[var(--app-text-muted)] transition hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50"
              >
                <span>Delete Done User Todos</span>
                <span className="text-xs text-[var(--app-text-subtle)]">{userCompletedCount}</span>
              </button>
              {confirmDeleteAllOwner !== 'user' ? (
                <button
                  type="button"
                  onClick={() => setConfirmDeleteAllOwner('user')}
                  disabled={saving || userSection.items.length === 0}
                  className="flex w-full items-center justify-between px-3 py-2 text-left text-sm text-amber-600 transition hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <span>Delete All</span>
                  <span className="text-xs text-[var(--app-text-subtle)]">warning</span>
                </button>
              ) : (
                <div className="border-t border-[var(--app-border)] px-3 py-2">
                  <p className="text-xs text-[var(--app-text-muted)]">Delete all user todos? This cannot be undone.</p>
                  <div className="mt-2 flex items-center justify-end gap-2">
                    <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" onClick={() => setConfirmDeleteAllOwner(null)}>
                      Cancel
                    </Button>
                    <Button
                      size="sm"
                      className="h-7 px-2 text-xs"
                      onClick={() => {
                        setBulkMenuOpen(false)
                        setConfirmDeleteAllOwner(null)
                        void onDeleteAll('user')
                      }}
                    >
                      Confirm
                    </Button>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>,
        document.body,
      )
    : null

  const userInProgressItems = userSection.items.filter((entry) => entry.inProgress)
  const userGrouped = createPriorityBuckets()
  for (const item of userSection.items.filter((entry) => !entry.inProgress)) {
    userGrouped[item.priority].push(item)
  }

  return (
    <Dialog className={open ? undefined : 'hidden'} aria-hidden={!open}>
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="mx-auto mt-[5vh] flex max-h-[min(85vh,900px)] min-h-[400px] max-w-[min(900px,calc(100vw-32px))] flex-col overflow-hidden rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-2xl sm:max-w-[min(900px,calc(100vw-48px))]">
        <div className="flex shrink-0 items-center justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-6 py-4">
          <div className="flex min-w-0 items-center gap-3">
            <div className="flex size-9 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)]">
              <ListChecks size={18} />
            </div>
            <div className="min-w-0">
              <h2 className="truncate text-base font-semibold tracking-tight text-[var(--app-text)]">{workspaceName}</h2>
              <p className="text-xs text-[var(--app-text-muted)]">{formatSummary(summary)}</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              ref={bulkTriggerRef}
              type="button"
              onClick={() => {
                setBulkMenuOpen((prev) => !prev)
                setConfirmDeleteAllOwner(null)
              }}
              className="inline-flex min-h-9 items-center justify-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-transparent px-3 text-sm font-medium text-[var(--app-text)] transition duration-150 hover:bg-[var(--app-surface-subtle)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] disabled:cursor-not-allowed disabled:opacity-60"
              disabled={saving || items.length === 0}
            >
              <Trash2 size={14} />
              <ChevronDown size={14} className={bulkMenuOpen ? 'rotate-180 transition-transform' : 'transition-transform'} />
            </button>
            <Button
              variant={isAdding ? 'primary' : 'outline'}
              size="sm"
              onClick={() => setIsAdding(!isAdding)}
              className="h-8 gap-1.5"
            >
              {isAdding ? <X size={14} /> : <Plus size={14} />}
              {isAdding ? 'Cancel' : 'Add Task'}
            </Button>
            <Button
              size="sm"
              onClick={() => onOpenChange(false)}
              className="h-8 border border-[var(--app-primary)] bg-transparent px-3 text-[var(--app-primary)] hover:border-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)] active:bg-[var(--app-surface-hover)]"
            >
              Close
            </Button>
          </div>
        </div>

        {isAdding ? (
          <div className="border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Input
                ref={addInputRef}
                value={draftText}
                onChange={(event) => setDraftText(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') handleCreate('user')
                  if (event.key === 'Escape') setIsAdding(false)
                }}
                placeholder="Task description..."
                className="h-8 min-w-[200px] flex-1 bg-[var(--app-surface)] text-sm shadow-none focus-visible:ring-1"
              />
              <Select
                value={draftPriority}
                onChange={(event) => setDraftPriority(event.target.value as WorkspaceTodoPriority)}
                className="h-8 w-[140px] bg-[var(--app-surface)] text-sm"
              >
                {PRIORITY_GROUPS.map((priority) => (
                  <option key={priority} value={priority}>{formatPriorityLabel(priority)}</option>
                ))}
              </Select>
              <Button type="button" onClick={() => handleCreate('user')} disabled={saving || draftText.trim() === ''} size="sm" className="h-8">
                Add User Todo
              </Button>
            </div>
            <p className="mt-2 text-xs text-[var(--app-text-subtle)]">
              Priority controls how user todos are grouped in this workspace.
            </p>
          </div>
        ) : null}

        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-4">
          {summary.taskCount === 0 && !isAdding ? (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-[var(--app-text-muted)] opacity-70">
              <ListChecks size={32} />
              <p className="text-sm">No tasks here yet.</p>
              <Button variant="outline" size="sm" onClick={() => setIsAdding(true)}>Create the first task</Button>
            </div>
          ) : (
            <div className="space-y-6">
              <section className="space-y-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)]/40 px-4 py-4">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold text-[var(--app-text)]">{userSection.title}</h3>
                    <p className="text-xs text-[var(--app-text-muted)]">{userSection.description}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide text-[var(--app-text-muted)]">
                      {formatSummary(userSection.summary)}
                    </span>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() => handleCreate('user')}
                      disabled={saving || draftText.trim() === ''}
                      className="h-7 px-2 text-xs"
                    >
                      Add from draft as todo
                    </Button>
                  </div>
                </div>

                {userInProgressItems.length > 0 ? (
                  <div className="rounded-xl border border-[var(--app-primary)]/40 bg-[var(--app-surface)] px-3 py-3">
                    <div className="flex items-center justify-between gap-3 pb-3">
                      <h4 className="text-sm font-semibold text-[var(--app-text)]">In Progress</h4>
                      <span className="rounded-full border border-[var(--app-primary)]/40 bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide text-[var(--app-primary)]">
                        pinned
                      </span>
                    </div>
                    <div className="space-y-2">
                      {userInProgressItems.map((item) => renderTodoRow(item, { allowDrag: false }))}
                    </div>
                  </div>
                ) : null}

                <div className="divide-y divide-[var(--app-border)] border-y border-[var(--app-border)]">
                  {PRIORITY_GROUPS.map((priority) => {
                    const visibleItems = userGrouped[priority]
                    return (
                      <div
                        key={`user-${priority}`}
                        onDragOver={(event) => {
                          event.preventDefault()
                          event.dataTransfer.dropEffect = 'move'
                        }}
                        onDrop={(event) => handlePriorityDrop(event, priority, 'user')}
                        className="py-3"
                      >
                        <div className="flex items-center justify-between gap-3 px-1 pb-3">
                          <h4 className="text-sm font-semibold text-[var(--app-text)]">{formatPriorityLabel(priority)}</h4>
                          <span className="text-xs text-[var(--app-text-muted)]">{visibleItems.length}</span>
                        </div>
                        <div>
                          {visibleItems.length === 0 ? (
                            <div className="px-1 py-2 text-sm text-[var(--app-text-muted)]">{userSection.emptyText}</div>
                          ) : (
                            visibleItems.map((item) => renderTodoRow(item, { allowDrag: true }))
                          )}
                        </div>
                      </div>
                    )
                  })}
                </div>
              </section>

            </div>
          )}
        </div>
      </DialogPanel>
      {bulkMenu}
    </Dialog>
  )
}
