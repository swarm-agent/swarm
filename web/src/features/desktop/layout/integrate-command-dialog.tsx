import { useEffect, useRef, useState } from 'react'
import { Bot, GitMerge, LoaderCircle, X } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import { inspectIntegration, integrationRepairPrompt, runIntegration, type IntegrationProgress, type IntegrationSelection } from '../git/integrate-command'

export function IntegrateCommandDialog({ sessionId, onClose, onRepair, build }: { build?: { check: (path: string) => Promise<void>; run: (path: string) => Promise<void> }; sessionId: string; onClose: () => void; onRepair: (sessionId: string, workspacePath: string, prompt: string) => Promise<void> }) {
  const [selection, setSelection] = useState<IntegrationSelection | null>(null)
  const [state, setState] = useState<IntegrationProgress>({ phase: 'Inspecting session worktree', committed: false, integrated: false })
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [handoffError, setHandoffError] = useState('')
  const flight = useRef(false)
  useEffect(() => {
    let disposed = false
    void inspectIntegration(sessionId).then(value => { if (!disposed) setSelection(value) }).catch(error => {
      if (!disposed) setState({ phase: 'Inspection', committed: false, integrated: false, error: error instanceof Error ? error.message : String(error) })
    }).finally(() => { if (!disposed) setLoading(false) })
    return () => { disposed = true }
  }, [sessionId])
  const confirm = async () => {
    if (!selection || flight.current || state.error || state.integrated) return
    flight.current = true
    setBusy(true)
    try { await runIntegration(selection, setState, undefined, build) }
    catch (error) { setState(current => ({ ...current, error: error instanceof Error ? error.message : String(error) })) }
    finally { flight.current = false; setBusy(false) }
  }
  const repair = async () => {
    if (!selection || flight.current) return
    flight.current = true
    setBusy(true)
    setHandoffError('')
    try { await onRepair(sessionId, selection.repository.source_path, integrationRepairPrompt(selection, state)); onClose() }
    catch (error) { setHandoffError(error instanceof Error ? error.message : String(error)) }
    finally { flight.current = false; setBusy(false) }
  }
  const button = 'inline-flex min-h-11 items-center justify-center gap-2 rounded-lg px-4 py-2 text-sm font-semibold hover:bg-[var(--app-selection-bg)] disabled:opacity-50'
  return <Dialog role="dialog" aria-modal="true" aria-label="Integrate session worktree" className="z-[80]">
    <DialogBackdrop className="fixed inset-0 bg-black/50" />
    <div className="fixed inset-0 flex items-center justify-center p-4">
      <DialogPanel className="w-full max-w-lg rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] overflow-y-auto p-5 text-[var(--app-text)] shadow-2xl">
        <header className="flex items-center justify-between gap-3"><h2 className="flex items-center gap-2 text-base font-semibold"><GitMerge size={18} />Integrate session worktree</h2><button type="button" className={button} aria-label="Close integration" disabled={busy} onClick={onClose}><X size={16} /></button></header>
        {loading ? <p role="status" className="mt-4 flex items-center gap-2 text-sm"><LoaderCircle size={15} className="animate-spin" />Inspecting current session…</p> : null}
        {selection ? <div className="mt-4 grid gap-2 text-sm"><p className="break-all"><strong>{selection.repository.branch}</strong> → <strong>{selection.targetBranch}</strong></p><p className="break-all text-xs text-[var(--app-text-muted)]">{selection.repository.source_path}</p><p>{selection.dirty ? 'Generate an AI commit message and commit all current source changes, then integrate into the captured checkout.' : 'Integrate the committed source changes into the captured checkout.'} {build ? 'Then rebuild Swarm using the configured dev checkout. This may restart Swarm. No archive.' : 'No archive or rebuild.'}</p></div> : null}
        {busy || state.integrated || state.error ? <div className="mt-4 grid gap-2 text-sm" aria-live="polite"><p className="flex items-center gap-2">{busy ? <LoaderCircle size={15} className="animate-spin" /> : null}{state.phase}</p>{state.integrated ? <p>Integration succeeded and will not be repeated.</p> : null}{state.committed ? <p>Source commit succeeded and is preserved.</p> : null}{state.error ? <><p role="alert" className="max-h-48 overflow-auto whitespace-pre-wrap break-words text-[var(--app-error)]">{state.error}</p><p className="text-xs text-[var(--app-text-muted)]">No automatic retry. Inspect Git before retrying; the server may have completed a mutation before returning an error.</p></> : null}</div> : null}
        {handoffError ? <p role="alert" className="mt-3 text-sm text-[var(--app-error)]">{handoffError}</p> : null}
        <footer className="mt-5 flex flex-wrap justify-end gap-2"><button type="button" className={button} disabled={busy} onClick={onClose}>{state.integrated ? 'Done' : 'Close'}</button>{state.error && selection ? <button type="button" className={button} disabled={busy} onClick={() => void repair()}><Bot size={15} />Send to Swarm…</button> : null}{selection && !state.error && !state.integrated ? <button type="button" className={`${button} text-[var(--app-primary)]`} disabled={busy || loading} onClick={() => void confirm()}>{busy ? 'Working…' : build ? 'Confirm integration and rebuild' : selection.dirty ? 'AI commit and integrate' : 'Confirm integration'}</button> : null}</footer>
        {state.error && selection ? <p className="mt-2 text-xs text-[var(--app-text-muted)]">Send to Swarm opens the error in this session’s composer for review and sending.</p> : null}
      </DialogPanel>
    </div>
  </Dialog>
}
