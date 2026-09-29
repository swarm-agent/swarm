import React, { useState } from 'react'
import { desktopAutomationV2, useAutomationV2Page } from '../runtime/desktop-automation-v2'
import type { AutomationV2Record } from '../state/desktop-automation-v2-api'
import { DeleteAutomationDialog } from '../tools/automations/delete-automation-dialog'

// Legacy automation records are not durable Workers Hub records. Keep their
// removal reachable here and subscribe to the canonical automation cache.
export function LegacyAutomations() {
  const [cursor, setCursor] = useState('')
  const input = { action: 'list' as const, archived_mode: 'include' as const, cursor }
  const page = useAutomationV2Page(input)
  const [deleting, setDeleting] = useState<AutomationV2Record | null>(null)
  const records = page?.data?.records ?? []
  return (
    <section aria-label="Legacy automations" className="mx-3.5 mt-2 mb-1.5 rounded-2xl border border-slate-800 bg-slate-900/60 p-3">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-xs font-semibold">Legacy automations · all workspaces</h3>
        <button type="button" onClick={() => desktopAutomationV2.invalidate()} className="text-xs text-blue-400">Refresh</button>
      </div>
      <p className="mt-1 text-xs text-slate-400">Separate from Workers Hub. Deleting an automation keeps its conversation.</p>
      {page?.error && <p role="alert" className="mt-2 text-xs text-red-400">{page.error}</p>}
      {!page || page.loading ? <p className="mt-2 text-xs">Loading automations…</p> : null}
      {page?.stale && <p className="mt-2 text-xs text-amber-300">Refreshing automation state. Delete checks the displayed generation.</p>}
      {page && !page.loading && !page.error && !page.stale && records.length === 0 && <p className="mt-2 text-xs text-slate-400">No legacy automations on this page.</p>}
      <div className="mt-2 grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {records.map(record => (
          <div key={record.automation_id || record.session_id} className="flex items-center justify-between gap-3 rounded-xl border border-slate-800 p-3">
            <div className="min-w-0">
              <h4 className="truncate text-xs font-semibold">{record.document.title}</h4>
              <p className="text-xs text-slate-400">{record.cancelled ? 'Cancelled' : record.enabled ? 'Enabled' : 'Paused'}{record.archived ? ' · Archived' : ''}</p>
            </div>
            <button type="button" aria-label={`Delete ${record.document.title}`} onClick={() => setDeleting(record)} className="text-xs text-red-400">Delete</button>
          </div>
        ))}
      </div>
      {(cursor || page?.data?.next_cursor) && <div className="mt-2 flex gap-3 text-xs">
        {cursor && <button type="button" onClick={() => setCursor('')}>First page</button>}
        {page?.data?.next_cursor && <button type="button" disabled={page.loading || page.stale} onClick={() => setCursor(page.data!.next_cursor!)}>Next page</button>}
      </div>}
      {deleting && <DeleteAutomationDialog record={deleting} onClose={() => setDeleting(null)} onDeleted={() => setCursor('')} />}
    </section>
  )
}
