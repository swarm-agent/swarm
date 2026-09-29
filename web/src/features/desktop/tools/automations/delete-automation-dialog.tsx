import React, { useRef, useState } from 'react'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import type { AutomationV2Record } from '../../state/desktop-automation-v2-api'

export function DeleteAutomationDialog({ record, onClose, onDeleted }: {
  record: AutomationV2Record
  onClose: () => void
  onDeleted?: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const inFlight = useRef(false)
  const remove = async () => {
    if (inFlight.current) return
    inFlight.current = true
    setBusy(true)
    setError('')
    try {
      await desktopAutomationV2.deleteRecord(record)
      onDeleted?.()
      onClose()
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Could not delete automation.'
      setError(`${message} Close this dialog, refresh the list, and review the automation before retrying.`)
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" role="dialog" aria-modal="true" aria-label="Delete automation">
      <div className="w-full max-w-md rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] p-6 shadow-2xl space-y-4">
        <h3 className="text-base font-semibold">Delete automation?</h3>
        <p className="text-sm">Delete “{record.document.title}”? This removes the automation and cancels its scheduled work. Your conversation is kept.</p>
        {error && <p role="alert" className="text-sm text-red-400">{error}</p>}
        <div className="flex justify-end gap-2">
          <button type="button" disabled={busy} onClick={onClose} className="rounded-lg border px-3 py-2 text-sm disabled:opacity-50">Cancel</button>
          <button type="button" disabled={busy} onClick={() => void remove()} className="rounded-lg bg-red-700 px-3 py-2 text-sm text-white disabled:opacity-50">{busy ? 'Deleting…' : 'Delete automation'}</button>
        </div>
      </div>
    </div>
  )
}
