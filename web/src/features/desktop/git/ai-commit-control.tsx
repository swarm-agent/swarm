import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bot, ChevronUp, Link2, LoaderCircle, Pin, Plus, X, Zap } from 'lucide-react'
import { cn } from '../../../lib/cn'
import { loadStoredValue, saveStoredValue } from '../../workspaces/launcher/services/workspace-storage'
import { fetchWorkspaceActions, type WorkspaceAction } from '../../workspaces/actions/types'

const PINNED_GIT_FLOWS_STORAGE_KEY = 'swarm.web.desktop.git.pinned-flows.v1'

export type PinnedGitFlowKind = 'action' | 'ai-commit-action'

export interface PinnedGitFlow {
  actionId: string
  kind: PinnedGitFlowKind
}

interface AICommitButtonProps {
  phase?: 'generating' | 'committing' | null
  disabled?: boolean
  compact?: boolean
  onGenerate: () => void
}

interface GitActionFlowControlProps {
  workspacePath: string
  canAICommit: boolean
  disabled?: boolean
  compact?: boolean
  onActionRun: (action: WorkspaceAction) => void
  onAICommitActionRun: (action: WorkspaceAction, inputs: Record<string, string>) => void
}

type StoredPinnedGitFlows = Record<string, PinnedGitFlow[]>

function readPinnedGitFlows(): StoredPinnedGitFlows {
  const raw = loadStoredValue(PINNED_GIT_FLOWS_STORAGE_KEY)
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw) as Record<string, unknown>
    return Object.fromEntries(Object.entries(parsed).map(([workspacePath, value]) => [
      workspacePath,
      Array.isArray(value)
        ? value.filter((entry): entry is PinnedGitFlow => {
            if (!entry || typeof entry !== 'object') return false
            const flow = entry as Partial<PinnedGitFlow>
            return typeof flow.actionId === 'string' && (flow.kind === 'action' || flow.kind === 'ai-commit-action')
          })
        : [],
    ]))
  } catch {
    return {}
  }
}

export function loadPinnedGitFlows(workspacePath: string): PinnedGitFlow[] {
  return readPinnedGitFlows()[workspacePath] ?? []
}

export function savePinnedGitFlows(workspacePath: string, flows: PinnedGitFlow[]): void {
  const stored = readPinnedGitFlows()
  if (flows.length > 0) stored[workspacePath] = flows
  else delete stored[workspacePath]
  saveStoredValue(PINNED_GIT_FLOWS_STORAGE_KEY, Object.keys(stored).length > 0 ? JSON.stringify(stored) : null)
}

function actionOptionsPreview(action: WorkspaceAction): string {
  const options = [...action.arguments]
  for (const input of action.inputs) options.push(...input.arguments, `<${input.label}>`)
  return options.length > 0 ? options.join(' ') : 'No options'
}

function flowKey(flow: PinnedGitFlow): string {
  return `${flow.kind}:${flow.actionId}`
}

export function AICommitButton({ phase = null, disabled = false, compact = false, onGenerate }: AICommitButtonProps) {
  return (
    <button
      type="button"
      data-ai-commit-button
      className={cn('inline-flex min-h-9 min-w-0 items-center justify-center gap-1.5 rounded-lg bg-[var(--app-bg-alt)] px-2.5 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-60', compact && 'w-9 px-0')}
      disabled={disabled || phase !== null}
      onClick={onGenerate}
      aria-label={phase === 'generating' ? 'AI Commit is generating a message' : phase === 'committing' ? 'AI Commit is committing changes' : 'Generate a message and commit changes'}
      title={phase ? 'AI Commit is running; wait for it to finish' : 'Generate a message and commit all changes'}
    >
      {phase ? <LoaderCircle size={14} className="shrink-0 animate-spin" aria-hidden="true" /> : <Bot size={14} className="shrink-0" aria-hidden="true" />}
      {!compact ? <span className="truncate">{phase === 'generating' ? 'Generating…' : phase === 'committing' ? 'Committing…' : 'AI Commit'}</span> : null}
    </button>
  )
}

export function GitActionFlowControl({ workspacePath, canAICommit, disabled = false, compact = false, onActionRun, onAICommitActionRun }: GitActionFlowControlProps) {
  const [open, setOpen] = useState(false)
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [pinnedFlows, setPinnedFlows] = useState<PinnedGitFlow[]>(() => loadPinnedGitFlows(workspacePath))
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)
  const [configuredFlow, setConfiguredFlow] = useState<PinnedGitFlow | null>(null)
  const [inputValues, setInputValues] = useState<Record<string, string>>({})
  const [menuPosition, setMenuPosition] = useState({ bottom: 0, right: 0 })
  const rootRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = useId()

  useEffect(() => {
    setPinnedFlows(loadPinnedGitFlows(workspacePath))
    setConfiguredFlow(null)
  }, [workspacePath])

  useEffect(() => {
    if (!workspacePath.trim()) return
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetchWorkspaceActions(workspacePath, controller.signal)
      .then(setActions)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load Actions.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [request, workspacePath])

  const positionMenu = useCallback(() => {
    const bounds = rootRef.current?.getBoundingClientRect()
    if (!bounds) return
    setMenuPosition({
      bottom: Math.max(8, window.innerHeight - bounds.top + 8),
      right: Math.max(8, window.innerWidth - bounds.right),
    })
  }, [])

  useEffect(() => {
    if (!open) return
    positionMenu()
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('resize', positionMenu)
    window.addEventListener('scroll', positionMenu, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('resize', positionMenu)
      window.removeEventListener('scroll', positionMenu, true)
    }
  }, [open, positionMenu])

  const actionById = useMemo(() => new Map(actions.map((action) => [action.id, action])), [actions])
  const resolvedPinnedFlows = pinnedFlows.flatMap((flow) => {
    const action = actionById.get(flow.actionId)
    return action ? [{ flow, action }] : []
  })
  const configuredAction = configuredFlow ? actionById.get(configuredFlow.actionId) ?? null : null
  const missingRequiredInputs = Boolean(configuredAction?.inputs.some((input) => input.required && !(inputValues[input.id] ?? '').trim()))

  const updatePinnedFlows = (next: PinnedGitFlow[]) => {
    setPinnedFlows(next)
    savePinnedGitFlows(workspacePath, next)
  }

  const togglePin = (flow: PinnedGitFlow) => {
    const key = flowKey(flow)
    updatePinnedFlows(pinnedFlows.some((entry) => flowKey(entry) === key)
      ? pinnedFlows.filter((entry) => flowKey(entry) !== key)
      : [...pinnedFlows, flow])
  }

  const runFlow = (flow: PinnedGitFlow, action: WorkspaceAction) => {
    if (flow.kind === 'action') {
      setOpen(false)
      onActionRun(action)
      return
    }
    if (!canAICommit) return
    if (action.inputs.length === 0) {
      setOpen(false)
      onAICommitActionRun(action, {})
      return
    }
    setConfiguredFlow(flow)
    setInputValues(Object.fromEntries(action.inputs.map((input) => [input.id, input.defaultValue])))
    setOpen(true)
  }

  const submitConfiguredFlow = () => {
    if (!configuredAction || !configuredFlow || configuredFlow.kind !== 'ai-commit-action' || missingRequiredInputs) return
    setOpen(false)
    setConfiguredFlow(null)
    onAICommitActionRun(configuredAction, inputValues)
  }

  return (
    <div ref={rootRef} className="flex min-w-0 flex-1 items-center gap-1" data-git-action-flow-control>
      <button
        type="button"
        data-git-actions-button
        className="inline-flex min-h-9 shrink-0 items-center justify-center gap-1.5 rounded-lg bg-[var(--app-bg-alt)] px-2 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:opacity-60"
        disabled={disabled}
        onClick={() => {
          positionMenu()
          setConfiguredFlow(null)
          setOpen((current) => !current)
        }}
        aria-label="Open workspace Actions and flows"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        title="Run, pin, or combine workspace Actions"
      >
        <Plus size={14} aria-hidden="true" />
        {!compact ? <span>Actions</span> : null}
        <ChevronUp size={14} aria-hidden="true" />
      </button>
      <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto" data-pinned-git-flows>
        {resolvedPinnedFlows.map(({ flow, action }) => (
          <button
            key={flowKey(flow)}
            type="button"
            className="inline-flex h-9 shrink-0 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 text-[11px] font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-50"
            disabled={disabled || (flow.kind === 'ai-commit-action' && !canAICommit)}
            onClick={() => runFlow(flow, action)}
            aria-label={flow.kind === 'action' ? `Run pinned Action ${action.name}` : `Run pinned AI Commit then ${action.name}`}
            title={flow.kind === 'action' ? `Run ${action.name}` : `AI Commit, then run ${action.name}`}
          >
            {flow.kind === 'action' ? <Zap size={13} aria-hidden="true" /> : <><Bot size={13} aria-hidden="true" /><Link2 size={10} aria-hidden="true" /><Zap size={13} aria-hidden="true" /></>}
            {!compact ? <span className="max-w-28 truncate">{action.name}</span> : null}
          </button>
        ))}
      </div>
      {open ? createPortal(
        <div ref={menuRef} id={menuId} role="menu" aria-label="Workspace Actions and flows" className="fixed z-[100] w-[22rem] overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 text-left shadow-2xl" style={{ bottom: menuPosition.bottom, right: menuPosition.right }} data-menu-direction="up">
          <div className="flex items-center justify-between px-2 py-1.5">
            <div><div className="text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Actions</div><div className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">Run an Action now, pin it, or commit changes before it runs.</div></div>
            <button type="button" className="grid size-7 place-items-center rounded-md text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]" onClick={() => setOpen(false)} aria-label="Close Actions menu"><X size={13} /></button>
          </div>
          {configuredAction && configuredFlow ? (
            <div className="grid gap-3 border-t border-[var(--app-border)] p-2" data-ai-commit-action-inputs>
              <div className="text-xs font-semibold text-[var(--app-text)]">AI Commit → {configuredAction.name}</div>
              {configuredAction.inputs.map((input) => <label key={input.id} className="grid gap-1 text-[11px] text-[var(--app-text-muted)]"><span>{input.label}{input.required ? ' *' : ''}</span><input type={input.kind === 'secret' ? 'password' : 'text'} value={inputValues[input.id] ?? ''} placeholder={input.placeholder} onChange={(event) => setInputValues((current) => ({ ...current, [input.id]: event.target.value }))} className="h-8 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-alt)] px-2 text-xs text-[var(--app-text)] outline-none" /></label>)}
              <div className="flex justify-end gap-2"><button type="button" className="h-8 px-2 text-xs text-[var(--app-text-muted)]" onClick={() => setConfiguredFlow(null)}>Back</button><button type="button" className="h-8 rounded-lg bg-[var(--app-primary)] px-3 text-xs font-semibold text-[var(--app-primary-text)] disabled:opacity-50" disabled={missingRequiredInputs || !canAICommit} onClick={submitConfiguredFlow}>Run flow</button></div>
            </div>
          ) : (
            <>
              {resolvedPinnedFlows.length > 0 ? <div className="border-t border-[var(--app-border)] px-2 py-2"><div className="mb-1 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Pinned</div>{resolvedPinnedFlows.map(({ flow, action }) => <div key={flowKey(flow)} className="flex h-9 items-center gap-2"><button type="button" role="menuitem" className="flex min-w-0 flex-1 items-center gap-2 text-xs text-[var(--app-text)]" onClick={() => runFlow(flow, action)}>{flow.kind === 'action' ? <Zap size={13} /> : <><Bot size={13} /><Link2 size={10} /><Zap size={13} /></>}<span className="truncate">{flow.kind === 'action' ? action.name : `AI Commit → ${action.name}`}</span></button><button type="button" className="grid size-7 place-items-center text-[var(--app-text-muted)]" onClick={() => togglePin(flow)} aria-label={`Unpin ${action.name}`}><X size={12} /></button></div>)}</div> : null}
              {loading ? <div className="flex items-center gap-2 border-t border-[var(--app-border)] px-2 py-3 text-xs text-[var(--app-text-muted)]"><LoaderCircle size={13} className="animate-spin" />Loading Actions…</div> : null}
              {!loading && !error && actions.length === 0 ? <div className="border-t border-[var(--app-border)] px-2 py-3 text-xs text-[var(--app-text-muted)]">No workspace Actions configured.</div> : null}
              {actions.length > 0 ? <div className={cn('border-t border-[var(--app-border)]', actions.length > 5 && 'max-h-[300px] overflow-y-auto [scrollbar-gutter:stable]')} data-action-list-scroll={actions.length > 5 ? 'conditional' : undefined}>{actions.map((action) => {
                const actionFlow = { kind: 'action', actionId: action.id } satisfies PinnedGitFlow
                const comboFlow = { kind: 'ai-commit-action', actionId: action.id } satisfies PinnedGitFlow
                const actionPinned = pinnedFlows.some((flow) => flowKey(flow) === flowKey(actionFlow))
                const comboPinned = pinnedFlows.some((flow) => flowKey(flow) === flowKey(comboFlow))
                return <div key={action.id} className="grid gap-1 border-b border-[var(--app-border)] p-2 last:border-b-0"><button type="button" role="menuitem" className="flex min-w-0 items-center gap-2 text-xs text-[var(--app-text)]" onClick={() => { setOpen(false); onActionRun(action) }}><Zap size={13} className="shrink-0" /><span className="min-w-0 flex-1 text-left"><strong className="block truncate font-medium">{action.name}</strong><span className="mt-0.5 block truncate text-[10px] text-[var(--app-text-subtle)]" title={actionOptionsPreview(action)}>{actionOptionsPreview(action)}</span></span><span className="text-[10px] font-semibold uppercase text-[var(--app-primary)]">Run</span></button><div className="flex justify-end gap-1"><button type="button" aria-pressed={actionPinned} className={cn('inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[10px] text-[var(--app-text-muted)]', actionPinned && 'bg-[var(--app-selection-bg)] text-[var(--app-text)]')} onClick={() => togglePin(actionFlow)}><Pin size={11} />Pin</button><button type="button" aria-pressed={comboPinned} className={cn('inline-flex h-7 items-center gap-1 rounded-md border border-[var(--app-border)] px-2 text-[10px] text-[var(--app-text-muted)]', comboPinned && 'bg-[var(--app-selection-bg)] text-[var(--app-text)]')} onClick={() => togglePin(comboFlow)}><Bot size={11} /><Link2 size={9} /><Zap size={11} />Commit + Pin</button></div></div>
              })}</div> : null}
              {error ? <div className="border-t border-[var(--app-border)] px-2 py-2 text-xs text-[var(--app-danger)]" role="alert">{error}<button type="button" className="ml-2 underline" onClick={() => setRequest((current) => current + 1)}>Retry</button></div> : null}
            </>
          )}
        </div>,
        document.body,
      ) : null}
    </div>
  )
}
