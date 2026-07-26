import { useState, useSyncExternalStore } from 'react'
import { CheckCircle2, LoaderCircle, MemoryStick, X, XCircle } from 'lucide-react'

import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import {
  captureDesktopLongSessionDiagnostics,
  getDesktopLongSessionDiagnosticsAvailability,
  subscribeDesktopLongSessionDiagnosticsAvailability,
} from './desktop-long-session-diagnostics'

type FeedbackTone = 'success' | 'error' | 'info'

interface DesktopLongSessionDiagnosticsControlProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onFeedback: (message: string, tone: FeedbackTone) => void
}

export function useDesktopLongSessionDiagnosticsAvailability() {
  return useSyncExternalStore(
    subscribeDesktopLongSessionDiagnosticsAvailability,
    getDesktopLongSessionDiagnosticsAvailability,
    getDesktopLongSessionDiagnosticsAvailability,
  )
}

export function DesktopLongSessionDiagnosticsControl({
  open,
  onOpenChange,
  onFeedback,
}: DesktopLongSessionDiagnosticsControlProps) {
  const availability = useDesktopLongSessionDiagnosticsAvailability()
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<{ tone: FeedbackTone; message: string } | null>(null)

  const handleCapture = async () => {
    if (busy) return
    setBusy(true)
    setStatus({ tone: 'info', message: 'Writing the current browser sample and daemon pprof artifacts…' })
    try {
      const result = await captureDesktopLongSessionDiagnostics()
      const location = result.artifactLocation || availability.artifactLocation
      if (!location || result.artifacts.length === 0) {
        throw new Error('The daemon did not report any created diagnostics artifacts.')
      }
      const message = `Created ${result.artifacts.join(', ')} in ${location}`
      setStatus({ tone: 'success', message })
      onFeedback(message, 'success')
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      setStatus({ tone: 'error', message: `Diagnostics capture failed: ${message}` })
      onFeedback(`Diagnostics capture failed: ${message}`, 'error')
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  return (
    <Dialog role="dialog" aria-modal="true" aria-labelledby="desktop-memory-diagnostics-title">
      <DialogBackdrop onClick={() => onOpenChange(false)} />
      <DialogPanel className="w-[min(520px,100%)]">
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <MemoryStick size={20} className="text-[var(--app-primary)]" />
              <h2 id="desktop-memory-diagnostics-title" className="text-lg font-semibold">Memory diagnostics</h2>
            </div>
            <p className="mt-2 text-sm text-[var(--app-text-muted)]">
              Browser responsiveness, DOM, and cache metadata are recorded every 30 seconds. Capture now also asks the daemon to write fresh Go runtime and pprof artifacts.
            </p>
          </div>
          <Button variant="ghost" className="h-9 w-9 min-w-9 p-0" onClick={() => onOpenChange(false)} aria-label="Close memory diagnostics">
            <X size={18} />
          </Button>
        </div>

        <div className="mt-5 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Artifact location</div>
          <div className="mt-1 break-all font-mono text-xs text-[var(--app-text)]">
            {availability.artifactLocation || 'Waiting for the daemon to report a diagnostics directory'}
          </div>
        </div>

        <Button onClick={() => { void handleCapture() }} disabled={busy} className="mt-5 min-h-11 w-full">
          {busy ? <LoaderCircle size={16} className="mr-2 animate-spin" /> : <MemoryStick size={16} className="mr-2" />}
          Capture now
        </Button>

        {status ? (
          <div className={`mt-4 flex items-start gap-2 rounded-lg border p-3 text-sm ${status.tone === 'error' ? 'border-[var(--app-error)] text-[var(--app-error)]' : status.tone === 'success' ? 'border-[var(--app-success)] text-[var(--app-text)]' : 'border-[var(--app-border)] text-[var(--app-text-muted)]'}`} role={status.tone === 'error' ? 'alert' : 'status'}>
            {status.tone === 'error' ? <XCircle size={17} className="mt-0.5 shrink-0" /> : status.tone === 'success' ? <CheckCircle2 size={17} className="mt-0.5 shrink-0 text-[var(--app-success)]" /> : <LoaderCircle size={17} className="mt-0.5 shrink-0 animate-spin" />}
            <span className="min-w-0 break-words">{status.message}</span>
          </div>
        ) : null}
      </DialogPanel>
    </Dialog>
  )
}
