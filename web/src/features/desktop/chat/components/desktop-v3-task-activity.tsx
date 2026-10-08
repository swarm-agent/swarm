import type { DesktopTaskActivity } from '../../state/desktop-v3-task-updates'

export function DesktopV3TaskActivity({ activity }: { activity: DesktopTaskActivity }) {
  return <section aria-label={activity.label} data-testid="desktop-task-activity" className="min-w-0 rounded-xl border border-[var(--app-border)] px-4 py-3 text-sm text-[var(--app-text)]">
    <h3 className="font-medium">{activity.label}</h3>
    <p className="mt-1 text-xs text-[var(--app-text-muted)]">{activity.detail}</p>
    {activity.rows.map((row, index) => <div key={index} className="mt-2 min-w-0">
      {row.title || row.status ? <p className="font-medium [overflow-wrap:anywhere]">{row.title}{row.status ? ` · ${row.status}` : ''}</p> : null}
      {row.summary ? <p className="whitespace-pre-wrap [overflow-wrap:anywhere]">{row.summary}</p> : null}
    </div>)}
  </section>
}
