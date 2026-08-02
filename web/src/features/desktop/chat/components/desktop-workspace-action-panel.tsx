import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { CheckCircle2, LoaderCircle, Square, X, Zap } from 'lucide-react'
import { cancelWorkspaceActionRun, fetchWorkspaceActionRun, startWorkspaceAction, type WorkspaceAction, type WorkspaceActionRun } from '../../../workspaces/actions/types'

interface DesktopWorkspaceActionPanelProps {
  workspacePath: string
  action: WorkspaceAction
  autoLaunch?: boolean
  onClose: () => void
}

function invocationPreview(action: WorkspaceAction): string {
  const parts = [action.entrypoint, ...action.arguments]
  for (const input of action.inputs) {
    parts.push(...input.arguments, `<${input.label}>`)
  }
  return parts.join(' ')
}

export function DesktopWorkspaceActionPanel({ workspacePath, action, autoLaunch = false, onClose }: DesktopWorkspaceActionPanelProps) {
  const [values, setValues] = useState<Record<string, string>>(() => Object.fromEntries(action.inputs.map((input) => [input.id, input.defaultValue])))
  const [run, setRun] = useState<WorkspaceActionRun | null>(null)
  const [error, setError] = useState('')
  const [launching, setLaunching] = useState(false)
  const [successNotice, setSuccessNotice] = useState('')
  const outputRef = useRef<HTMLPreElement | null>(null)
  const autoLaunchStartedRef = useRef(false)
  const missingRequired = useMemo(() => action.inputs.some((input) => input.required && !(values[input.id] ?? '').trim()), [action.inputs, values])

  useEffect(() => {
    if (!run || run.status !== 'running') return
    const controller = new AbortController()
    const timer = window.setInterval(() => {
      void fetchWorkspaceActionRun(workspacePath, run.id, controller.signal)
        .then((next) => {
          setRun(next)
          setError('')
          if (next.status === 'succeeded') setSuccessNotice(`${next.actionName} completed successfully.`)
        })
        .catch((cause) => {
          if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not refresh Action output.')
        })
    }, 750)
    return () => {
      controller.abort()
      window.clearInterval(timer)
    }
  }, [run?.id, run?.status, workspacePath])

  useEffect(() => {
    if (run?.status !== 'succeeded') return
    const timer = window.setTimeout(onClose, 1800)
    return () => window.clearTimeout(timer)
  }, [onClose, run?.status])

  useEffect(() => {
    if (outputRef.current) outputRef.current.scrollTop = outputRef.current.scrollHeight
  }, [run?.output])

  const launch = useCallback(async () => {
    setLaunching(true)
    setError('')
    try {
      setRun(await startWorkspaceAction(workspacePath, action.id, values))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not start Action.')
    } finally {
      setLaunching(false)
    }
  }, [action.id, values, workspacePath])

  useEffect(() => {
    if (!autoLaunch || action.inputs.length > 0 || autoLaunchStartedRef.current) return
    autoLaunchStartedRef.current = true
    void launch()
  }, [action.inputs.length, autoLaunch, launch])

  const stop = async () => {
    if (!run) return
    setError('')
    try {
      setRun(await cancelWorkspaceActionRun(workspacePath, run.id))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not stop Action.')
    }
  }

  return (
    <section className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm" data-testid="desktop-workspace-action-panel" aria-label={`Run ${action.name}`}>
      <div className="flex items-start gap-3 px-4 py-3">
        <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]"><Zap size={16} /></span>
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold text-[var(--app-text)]">{action.name}</h3>
          {action.description ? <p className="mt-0.5 text-xs text-[var(--app-text-muted)]">{action.description}</p> : null}
          <code className="mt-2 block overflow-x-auto whitespace-nowrap rounded-md bg-[var(--app-code-bg,var(--app-bg-alt))] px-2 py-1.5 text-[11px] text-[var(--app-text-muted)]">{invocationPreview(action)}</code>
        </div>
        {run?.status === 'running' ? null : <button type="button" onClick={onClose} aria-label="Close Action panel" className="grid h-8 w-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]"><X size={15} /></button>}
      </div>

      {!run ? (
        <div className="grid gap-3 border-t border-[var(--app-border)] px-4 py-3">
          {action.inputs.map((input) => (
            <label key={input.id} className="grid gap-1 text-xs font-medium text-[var(--app-text)]">
              <span>{input.label}{input.required ? ' *' : ''}</span>
              {input.description ? <span className="font-normal text-[11px] text-[var(--app-text-muted)]">{input.description}</span> : null}
              <input type={input.kind === 'secret' ? 'password' : 'text'} value={values[input.id] ?? ''} placeholder={input.placeholder} onChange={(event) => setValues((current) => ({ ...current, [input.id]: event.target.value }))} className="h-9 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-3 text-sm outline-none focus:border-[var(--app-primary)]" />
            </label>
          ))}
          {action.inputs.length === 0 ? <p className="text-xs text-[var(--app-text-muted)]">This Action has no prompted inputs.</p> : null}
          {error ? <p className="text-xs text-[var(--app-danger)]" role="alert">{error}</p> : null}
          <div className="flex justify-end"><button type="button" onClick={() => { void launch() }} disabled={launching || missingRequired} className="inline-flex h-9 items-center gap-2 rounded-lg bg-[var(--app-primary)] px-3 text-xs font-semibold text-[var(--app-primary-text)] disabled:opacity-50">{launching ? <LoaderCircle size={14} className="animate-spin" /> : <Zap size={14} />}Run</button></div>
        </div>
      ) : (
        <div className="grid gap-2 border-t border-[var(--app-border)] px-4 py-3">
          <div className="flex items-center justify-between gap-3 text-xs">
            <span className={run.status === 'succeeded' ? 'inline-flex items-center gap-1.5 font-semibold text-[var(--app-success)]' : run.status === 'running' ? 'inline-flex items-center gap-1.5 font-semibold text-[var(--app-primary)]' : 'font-semibold text-[var(--app-danger)]'}>
              {run.status === 'running' ? <LoaderCircle size={14} className="animate-spin" /> : run.status === 'succeeded' ? <CheckCircle2 size={14} /> : null}
              {run.status.replace('_', ' ')}
            </span>
            {run.status === 'running' ? <button type="button" onClick={() => { void stop() }} className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-[var(--app-border)] px-2.5 font-semibold text-[var(--app-text)]"><Square size={12} />Stop</button> : null}
          </div>
          {run.output ? <pre ref={outputRef} className="max-h-52 overflow-auto whitespace-pre-wrap rounded-lg bg-[var(--app-code-bg,var(--app-bg-alt))] p-3 font-mono text-[11px] leading-5 text-[var(--app-text)]">{run.output}</pre> : <p className="text-xs text-[var(--app-text-muted)]">Waiting for output…</p>}
          {run.outputTruncated ? <p className="text-[11px] text-[var(--app-warning)]">Earlier output was truncated.</p> : null}
          {run.error ? <p className="text-xs text-[var(--app-danger)]">{run.error}{run.exitCode !== null ? ` (exit ${run.exitCode})` : ''}</p> : null}
          {error ? <p className="text-xs text-[var(--app-danger)]" role="alert">{error}</p> : null}
          {successNotice ? <div className="rounded-lg border border-[var(--app-success-border)] bg-[var(--app-success-bg)] px-3 py-2 text-xs font-medium text-[var(--app-success)]" role="status">{successNotice}</div> : null}
          {run.status !== 'running' && run.status !== 'succeeded' ? <div className="flex justify-end"><button type="button" onClick={onClose} className="h-8 rounded-lg border border-[var(--app-border)] px-3 text-xs font-semibold">Close</button></div> : null}
        </div>
      )}
    </section>
  )
}
