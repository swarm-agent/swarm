import { useState } from 'react';
import { CalendarClock } from 'lucide-react';
import { Button } from '../../../../components/ui/button';
import type { DesktopPermissionRecord } from '../../types/realtime';
import { automationV2PermissionProposal } from '../../state/desktop-automation-v2-api';
import { getDesktopV3CacheSnapshot } from '../../state/desktop-v3-cache-store';
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2';
import { decidePendingWorkerReview } from '../../tools/automations/pending-worker-sidebar-reviews';
import { normalizeStructuredPlanDocument, StructuredPlanReviewView } from './structured-plan-document';

/** A permissioned worker request remains in the authoring chat until resolved. */
export function DesktopPendingWorkerAlert({ permission, onAccepted }: {
  permission: DesktopPermissionRecord;
  onAccepted: (title: string, workerId: string, message?: string) => void;
}) {
  const proposal = automationV2PermissionProposal(permission);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const document = proposal ? normalizeStructuredPlanDocument(proposal.document) : null;

  async function accept() {
    if (!proposal || !document || busy) return;
    setBusy(true);
    setError('');
    try {
      const response = await decidePendingWorkerReview(
        getDesktopV3CacheSnapshot(), permission.id, proposal.revision, proposal.digest,
        'accept_automation', input => desktopAutomationV2.mutate(input),
      );
      onAccepted(proposal.document.title, response.record!.automation_id, response.message);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Worker acceptance failed. Refresh before retrying.');
    } finally {
      setBusy(false);
    }
  }

  return <section className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 shadow-sm" data-testid="pending-worker-alert" aria-label="Pending worker request">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 items-center gap-3">
        <CalendarClock size={18} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
        <div className="min-w-0">
          <span className="text-xs font-semibold uppercase text-[var(--app-primary)]">Pending Worker</span>
          <p className="break-words text-sm font-semibold text-[var(--app-text)]">{proposal?.document.title || 'Worker plan review'}</p>
        </div>
      </div>
      <Button type="button" variant="outline" size="sm" aria-expanded={open} onClick={() => setOpen(value => !value)}>{open ? 'Hide review' : 'Review worker'}</Button>
    </div>
    {open ? <div className="mt-3 border-t border-[var(--app-border)] pt-3">
      {document ? <StructuredPlanReviewView document={document} /> : <p role="alert">Worker review unavailable. Refresh before accepting.</p>}
      {error ? <p role="alert" className="mt-2 text-[var(--app-danger)]">{error}</p> : null}
      <div className="mt-3 flex justify-end">
        <Button type="button" size="sm" disabled={!document || busy} onClick={() => void accept()}>{busy ? 'Accepting…' : 'Accept worker'}</Button>
      </div>
    </div> : null}
  </section>;
}

export function DesktopAcceptedWorkerCard({ title, workerId, message }: { title: string; workerId: string; message?: string }) {
  return <section role="status" data-testid="accepted-worker-card" className="rounded-2xl border border-[var(--app-primary-border)] bg-[var(--app-surface)] p-4 text-sm">
    <p className="font-semibold text-[var(--app-text)]">{title}</p>
    <p className="text-[var(--app-primary)]">Worker accepted · {workerId}</p>
    {message ? <p className="mt-1 text-xs text-[var(--app-muted)]">{message}</p> : null}
  </section>;
}
