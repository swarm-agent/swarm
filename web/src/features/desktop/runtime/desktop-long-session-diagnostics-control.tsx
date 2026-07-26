import { useEffect, useRef, useState, useSyncExternalStore } from 'react'
import { CheckCircle2, LoaderCircle, MemoryStick, Square, X, XCircle } from 'lucide-react'

import { Button } from '../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../components/ui/dialog'
import {
  captureDesktopLongSessionDiagnostics,
  getDesktopLongSessionDiagnosticsAvailability,
  subscribeDesktopLongSessionDiagnosticsAvailability,
} from './desktop-long-session-diagnostics'

const MONITOR_INTERVAL_MS = 30_000
const DURATION_OPTIONS = [1, 5, 15, 30, 60] as const

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
  const [durationMinutes, setDurationMinutes] = useState(5)
  const [busy, setBusy] = useState(false)
  const [monitoring, setMonitoring] = useState(false)
  const [captureCount, setCaptureCount] = useState(0)
  const [endsAt, setEndsAt] = useState<number | null>(null)
  const [status, setStatus] = useState<{ tone: FeedbackTone; message: string } | null>(null)
  const intervalRef = useRef<number | null>(null)
  const timeoutRef = useRef<number | null>(null)
  const captureCountRef = useRef(0)
  const artifactLocationRef = useRef(availability.artifactLocation)

  const clearMonitoringTimers = () => {
    if (intervalRef.current !== null) window.clearInterval(intervalRef.current)
    if (timeoutRef.current !== null) window.clearTimeout(timeoutRef.current)
    intervalRef.current = null
    timeoutRef.current = null
  }

  useEffect(() => () => clearMonitoringTimers(), [])

  useEffect(() => {
    if (availability.artifactLocation) artifactLocationRef.current = availability.artifactLocation
  }, [availability.artifactLocation])

  useEffect(() => {
    if (!availability.enabled && monitoring) {
      clearMonitoringTimers()
      setMonitoring(false)
      setEndsAt(null)
      const message = availability.error || 'Long-session diagnostics were disabled while monitoring.'
      setStatus({ tone: 'error', message })
      onFeedback(message, 'error')
    }
  }, [availability.enabled, availability.error, monitoring, onFeedback])

  const runCapture = async (): Promise<string> => {
    const result = await captureDesktopLongSessionDiagnostics()
    const location = result.artifactLocation || availability.artifactLocation || 'the canonical long-session diagnostics run directory'
    artifactLocationRef.current = location
    captureCountRef.current += 1
    setCaptureCount(captureCountRef.current)
    setStatus({ tone: 'success', message: `Renderer and daemon diagnostics written to ${location}` })
    return location
  }

  const handleInstantDump = async () => {
    if (busy || monitoring) return
    setBusy(true)
    setStatus({ tone: 'info', message: 'Capturing renderer and daemon diagnostics…' })
    try {
      const location = await runCapture()
      onFeedback(`Memory diagnostics written to ${location}`, 'success')
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      setStatus({ tone: 'error', message: `Memory diagnostics failed: ${message}` })
      onFeedback(`Memory diagnostics failed: ${message}`, 'error')
    } finally {
      setBusy(false)
    }
  }

  const stopMonitoring = (completed = false) => {
    clearMonitoringTimers()
    setMonitoring(false)
    setEndsAt(null)
    const location = artifactLocationRef.current || 'the canonical long-session diagnostics run directory'
    const completedCaptureCount = captureCountRef.current
    const message = completed
      ? `Monitoring complete. ${completedCaptureCount} capture${completedCaptureCount === 1 ? '' : 's'} written to ${location}`
      : `Monitoring stopped. Captures remain in ${location}`
    setStatus({ tone: 'success', message })
    onFeedback(message, 'success')
  }

  const handleStartMonitoring = async () => {
    if (busy || monitoring) return
    setBusy(true)
    captureCountRef.current = 0
    setCaptureCount(0)
    setStatus({ tone: 'info', message: 'Taking the first capture…' })
    try {
      const location = await runCapture()
      const durationMS = durationMinutes * 60_000
      const end = Date.now() + durationMS
      setEndsAt(end)
      setMonitoring(true)
      intervalRef.current = window.setInterval(() => {
        void runCapture().catch((error) => {
          clearMonitoringTimers()
          setMonitoring(false)
          setEndsAt(null)
          const message = error instanceof Error ? error.message : String(error)
          setStatus({ tone: 'error', message: `Monitoring stopped after a capture failed: ${message}` })
          onFeedback(`Memory monitoring failed: ${message}`, 'error')
        })
      }, MONITOR_INTERVAL_MS)
      timeoutRef.current = window.setTimeout(() => stopMonitoring(true), durationMS)
      const message = `Monitoring for ${durationMinutes} minute${durationMinutes === 1 ? '' : 's'}; writing every 30 seconds to ${location}`
      setStatus({ tone: 'success', message })
      onFeedback(message, 'success')
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      setStatus({ tone: 'error', message: `Monitoring could not start: ${message}` })
      onFeedback(`Memory monitoring could not start: ${message}`, 'error')
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
              Capture renderer heap/cache evidence and daemon memory profiles in the same diagnostics run directory.
            </p>
          </div>
          <Button variant="ghost" className="h-9 w-9 min-w-9 p-0" onClick={() => onOpenChange(false)} aria-label="Close memory diagnostics">
            <X size={18} />
          </Button>
        </div>

        <div className="mt-5 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-4">
          <div className="text-xs font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Artifact location</div>
          <div className="mt-1 break-all font-mono text-xs text-[var(--app-text)]">
            {availability.artifactLocation || 'Canonical long-session diagnostics run directory'}
          </div>
        </div>

        <div className="mt-5 grid gap-3 sm:grid-cols-2">
          <Button onClick={() => { void handleInstantDump() }} disabled={busy || monitoring} className="min-h-11">
            {busy && !monitoring ? <LoaderCircle size={16} className="mr-2 animate-spin" /> : <MemoryStick size={16} className="mr-2" />}
            Dump now
          </Button>
          {monitoring ? (
            <Button variant="secondary" onClick={() => stopMonitoring(false)} className="min-h-11">
              <Square size={15} className="mr-2" /> Stop monitoring
            </Button>
          ) : (
            <Button variant="secondary" onClick={() => { void handleStartMonitoring() }} disabled={busy} className="min-h-11">
              {busy ? <LoaderCircle size={16} className="mr-2 animate-spin" /> : <MemoryStick size={16} className="mr-2" />}
              Start monitoring
            </Button>
          )}
        </div>

        <label className="mt-4 grid gap-2 text-sm font-medium text-[var(--app-text)]">
          How long do you want to monitor?
          <select
            value={durationMinutes}
            onChange={(event) => setDurationMinutes(Number(event.target.value))}
            disabled={busy || monitoring}
            className="h-10 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-3 text-sm outline-none focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)]"
          >
            {DURATION_OPTIONS.map((minutes) => <option key={minutes} value={minutes}>{minutes} minute{minutes === 1 ? '' : 's'}</option>)}
          </select>
        </label>

        {monitoring ? (
          <div className="mt-4 text-sm text-[var(--app-primary)]" role="status">
            Monitoring active · every 30 seconds · {captureCount} capture{captureCount === 1 ? '' : 's'} written
            {endsAt ? ` · ends ${new Date(endsAt).toLocaleTimeString()}` : ''}
          </div>
        ) : null}

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
