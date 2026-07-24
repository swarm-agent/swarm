/**
 * Variant 4: "Timeline pulse"
 * Emphasize progress and sequence with a vertical-flow/timeline feel,
 * strong active-state rhythm, clear next-up separation, and a subagent
 * dropdown styled as live parallel work lanes.
 */
export const desktopPlanSidebarVariant4 = {
  id: "variant-4",
  name: "Timeline pulse",
  description: "Emphasize progress and sequence with a vertical-flow/timeline feel, strong active-state rhythm, clear next-up separation, and a subagent dropdown styled as live parallel work lanes.",
  classes: {
    sidebar:
      "hidden h-full min-h-0 min-w-0 w-[360px] max-w-[360px] flex-1 overflow-hidden border-l border-[var(--app-border)] bg-[var(--app-surface)] px-5 py-4 min-[1300px]:flex min-[1300px]:flex-col relative before:absolute before:left-8 before:top-24 before:bottom-0 before:w-0.5 before:bg-gradient-to-b before:from-[var(--app-primary-border)] before:via-[var(--app-primary)]/20 before:to-transparent before:pointer-events-none",
    inner:
      "min-w-0 max-w-full gap-5 [&_*]:min-w-0 flex min-h-0 flex-1 flex-col overflow-hidden relative",
    header:
      "shrink-0 border-b border-[var(--app-border)] pb-3 flex items-center justify-between bg-gradient-to-r from-[var(--app-surface)] to-[var(--app-surface-subtle)]",
    headerTitle:
      "text-xs uppercase tracking-[0.2em] font-bold text-[var(--app-text)] flex items-center gap-2 before:size-2 before:rounded-full before:bg-[var(--app-primary)] before:animate-pulse",
    scrollRegion:
      "min-h-0 shrink basis-auto overflow-y-auto [scrollbar-gutter:stable] pr-1 grid content-start gap-6",
    checkpointSection:
      "min-w-0 relative pl-6 pb-6 border-b border-[var(--app-border)] last:border-0",
    checkpointLabel:
      "min-w-0 text-[9px] font-bold uppercase tracking-[0.2em] text-[var(--app-primary)] opacity-90 mb-1",
    status:
      "inline-flex max-w-[132px] shrink-0 items-center px-2 py-0.5 rounded text-[9px] font-bold uppercase tracking-[0.1em] bg-[var(--app-primary-soft)] border border-[var(--app-primary-border)]",
    checkpointWrapper:
      "pt-1",
    checkpointTitle:
      "min-w-0 line-clamp-3 break-words text-sm font-semibold leading-5 text-[var(--app-text)] bg-[var(--app-surface-subtle)] border-l-2 border-[var(--app-primary)] px-3.5 py-2.5 rounded-r-lg shadow-sm hover:shadow-md transition-shadow duration-200",
    checkpointId:
      "mr-2 inline-block font-mono text-xs font-bold text-[var(--app-primary)] select-none",
    progress:
      "mt-3",
    progressMeta:
      "mb-1 flex items-center justify-between text-[10px] text-[var(--app-text-subtle)] font-medium tracking-wide",
    progressTrack:
      "h-1.5 overflow-hidden rounded-full bg-[var(--app-border)]/50 border border-[var(--app-border)]/20",
    progressFill:
      "h-full rounded-full bg-gradient-to-r from-[var(--app-primary)] to-cyan-500 animate-pulse transition-all duration-300",
    nextUp:
      "mt-5 min-w-0 pt-4 border-t border-[var(--app-border)] text-xs",
    nextCheckpoint:
      "mt-3 flex min-w-0 items-start gap-3 border-l-2 border-dashed border-[var(--app-border)] pl-3.5 hover:border-[var(--app-primary-border)] transition-colors duration-200",
    tasksSection:
      "mt-4 min-h-0 border-t border-[var(--app-border)]/65 pt-4 text-xs text-[var(--app-text-muted)]",
    tasksHeading:
      "text-[9px] font-bold uppercase tracking-[0.15em] text-[var(--app-text-subtle)] mb-2.5",
    taskRow:
      "flex min-w-0 items-start gap-2 leading-4 py-1 hover:text-[var(--app-text)] transition-colors duration-150 data-[plan-task-active=true]:font-semibold data-[plan-task-active=true]:text-[var(--app-primary)] data-[plan-task-active=true]:bg-[var(--app-primary-soft)]/40 data-[plan-task-active=true]:px-2 data-[plan-task-active=true]:py-1 data-[plan-task-active=true]:rounded",
    taskDisclosure:
      "mt-2 flex min-h-0 shrink-0 flex-col border-t border-[var(--app-border)]/40 pt-1.5",
    openPlanButton:
      "mt-4 h-8.5 w-full rounded-lg text-xs font-semibold border border-[var(--app-border)] hover:bg-[var(--app-surface-subtle)] focus-visible:ring-2 focus-visible:ring-[var(--app-primary)] transition-all duration-150",
    actionsSection:
      "min-w-0 pt-2 border-t border-[var(--app-border)]",
    subagents: {
      root:
        "group min-w-0 overflow-hidden bg-[var(--app-surface-subtle)] rounded-xl border border-[var(--app-border)] p-1",
      summary:
        "flex min-h-10 cursor-pointer list-none items-center gap-2.5 px-3 text-xs font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface)] rounded-lg transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)] [&::-webkit-details-marker]:hidden",
      icon:
        "size-4 shrink-0 text-[var(--app-primary)] animate-pulse",
      label:
        "font-mono uppercase tracking-[0.1em] text-[10px] text-[var(--app-text-muted)]",
      chevron:
        "size-3.5 text-[var(--app-text-muted)] ml-auto transition-transform duration-200 group-open:rotate-90",
      list:
        "min-w-0 overflow-x-hidden overflow-y-auto mt-2 grid gap-2 p-1 max-h-64 border-t border-[var(--app-border)]/50 pt-2",
      row:
        "group/row grid min-h-12 min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-2 border-l-4 border-l-[var(--app-primary)] hover:border-l-[var(--app-primary)] hover:bg-[var(--app-surface-subtle)] focus-within:ring-1 focus-within:ring-[var(--app-primary)] transition-all duration-150 shadow-sm hover:shadow",
      status:
        "size-2 shrink-0 rounded-full ring-4 ring-offset-0 ring-[var(--app-surface)] shadow-sm",
      navigate:
        "min-w-0 text-left focus-visible:outline-none group/btn",
      title:
        "truncate text-xs font-bold text-[var(--app-text)] group-hover/btn:text-[var(--app-primary)] transition-colors duration-150",
      meta:
        "flex min-w-0 items-center gap-1.5 text-[10px] text-[var(--app-text-subtle)] truncate font-mono mt-0.5",
      progressTrack:
        "mt-1.5 h-1 overflow-hidden rounded-full bg-[var(--app-border)]/40",
      progressFill:
        "h-full bg-gradient-to-r from-[var(--app-primary)] to-cyan-400",
      stop:
        "grid h-8 w-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] focus-visible:ring-1 focus-visible:ring-[var(--app-primary)] md:opacity-0 md:group-hover/row:opacity-100 transition-all duration-150"
    }
  }
};
