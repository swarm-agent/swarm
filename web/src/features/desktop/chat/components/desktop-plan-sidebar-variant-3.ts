export const desktopPlanSidebarVariant3 = {
  id: "soft-focus",
  name: "Soft focus",
  description: "A calm, airy treatment with gentle surfaces, restrained rounding, soft primary accents, generous rhythm, and an elegant subagent dropdown whose rows feel light but legible.",
  classes: {
    // The `<aside>` root element of the sidebar
    sidebar: "hidden h-full min-h-0 min-w-0 w-[360px] max-w-[360px] flex-1 overflow-hidden border-l border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)] px-6 py-6 min-[1300px]:flex min-[1300px]:flex-col gap-6 font-sans shadow-sm",
    // The inner container wrapper
    inner: "flex min-h-0 flex-1 flex-col overflow-hidden gap-5 min-w-0 max-w-full [&_*]:min-w-0",
    // Header section containing the title
    header: "shrink-0 border-b border-[var(--app-border)]/40 pb-4",
    // Title inside the header
    headerTitle: "text-base font-semibold text-[var(--app-text)] tracking-tight",
    // Scrollable region containing sections
    scrollRegion: "grid content-start gap-5 min-h-0 shrink basis-auto overflow-y-auto [scrollbar-gutter:stable] pr-1",
    // Current checkpoint section container
    checkpointSection: "min-w-0 border-b border-[var(--app-border)]/40 pb-5",
    // Small uppercase label above current checkpoint title
    checkpointLabel: "text-[10px] font-semibold uppercase tracking-[0.2em] text-[var(--app-text-subtle)]/90",
    // Status text inside the badge
    status: "uppercase tracking-[0.12em] text-[10px] font-semibold opacity-90",
    // Current checkpoint detail text wrapper
    checkpointWrapper: "mt-2.5 min-w-0",
    // The active checkpoint title
    checkpointTitle: "break-words text-sm font-medium leading-relaxed text-[var(--app-text)]/95",
    // Active checkpoint ID/badge text
    checkpointId: "font-mono text-[10px] font-bold text-[var(--app-primary)]/80 tracking-wide mr-1.5",
    // Progress bar container
    progress: "mt-3",
    // Progress label/meta container
    progressMeta: "mb-1.5 flex items-center justify-between text-[10px] text-[var(--app-text-subtle)]/90",
    // Progress track background
    progressTrack: "h-1.5 overflow-hidden rounded-full bg-[var(--app-border)]/40",
    // Active progress fill
    progressFill: "h-full rounded-full bg-[var(--app-primary)]/70 transition-all duration-300",
    // "Next up" section container
    nextUp: "mt-5 min-w-0 border-t border-[var(--app-border)]/35 pt-4 text-xs",
    // Wrapper around the next checkpoint description
    nextCheckpoint: "mt-3 flex min-w-0 items-start gap-3 border-l-2 border-[var(--app-primary-border)]/50 pl-3",
    // Tasks checklist section
    tasksSection: "mt-4 min-h-0 border-t border-[var(--app-border)]/35 pt-4 text-xs text-[var(--app-text-muted)]/90",
    // Uppercase header for checklist section
    tasksHeading: "text-[10px] font-semibold uppercase tracking-[0.15em] text-[var(--app-text-subtle)]/80",
    // Individual task checkbox row
    taskRow: "flex min-w-0 items-start gap-2.5 leading-relaxed py-0.5",
    // Disclosure button to toggle completed tasks
    taskDisclosure: "mt-2.5 w-full text-left text-[10px] font-medium text-[var(--app-text-subtle)]/80 hover:text-[var(--app-text-muted)] transition-colors",
    // Primary button to open the full session plan
    openPlanButton: "mt-4 h-8 w-full rounded-md text-xs font-medium bg-[var(--app-surface-subtle)] border border-[var(--app-border)]/70 hover:bg-[var(--app-surface)] text-[var(--app-text-muted)] hover:text-[var(--app-text)] transition-all",
    // Control actions buttons section
    actionsSection: "min-w-0 pt-1 border-t border-[var(--app-border)]/30 mt-4",
    // Nested object specifically customizing the subagent dropdown/list elements
    subagents: {
      // The `<details>` wrapper of the subagent dropdown
      root: "group min-w-0 overflow-hidden my-3",
      // Summary trigger block of the subagents dropdown
      summary: "flex min-h-10 cursor-pointer list-none items-center rounded-lg border border-[var(--app-border)]/60 bg-[var(--app-surface-subtle)] text-xs font-medium text-[var(--app-text)]/90 hover:bg-[var(--app-surface)] hover:border-[var(--app-border)]/80 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]/40 [&::-webkit-details-marker]:hidden gap-2 px-3 transition-all",
      // Small robot icon on trigger block
      icon: "size-3.5 shrink-0 text-[var(--app-text-subtle)] opacity-80",
      // Text label "N subagents" on trigger
      label: "tracking-normal font-medium",
      // Chevron arrow icon that rotates when expanded
      chevron: "size-3.5 text-[var(--app-text-subtle)] transition-transform group-open:rotate-90 ml-auto opacity-70",
      // List container housing active subagents card rows
      list: "min-w-0 overflow-x-hidden overflow-y-auto mt-2.5 grid max-h-64 gap-2 pr-0.5",
      // Individual subagent card row wrapper
      row: "group/row grid min-h-12 min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 rounded-lg border border-[var(--app-border)]/40 bg-[var(--app-surface)]/45 px-3 py-2 hover:bg-[var(--app-surface)] hover:border-[var(--app-border)]/70 focus-within:ring-1 focus-within:ring-[var(--app-primary)]/45 transition-all duration-200 shadow-sm/5",
      // Status dot indicating subagent activity status
      status: "size-1.5 shrink-0 rounded-full ring-2 ring-white/5 shadow-sm",
      // Navigational button framing the subagent details
      navigate: "min-w-0 text-left focus-visible:outline-none cursor-pointer",
      // Assignment or agent title of subagent
      title: "truncate text-xs font-medium text-[var(--app-text)]/90 tracking-tight leading-snug",
      // Metadata line for active step and elapsed duration
      meta: "flex min-w-0 items-center gap-1.5 text-[10px] text-[var(--app-text-muted)]/85 mt-0.5",
      // Background track of context usage progress bar
      progressTrack: "mt-1.5 h-1 overflow-hidden rounded-full bg-[var(--app-border)]/20",
      // Accent bar filling the context window meter
      progressFill: "h-full bg-[var(--app-primary)]/60 rounded-full transition-all",
      // Stop execution square button for active subagent
      stop: "grid h-8 w-8 place-items-center rounded-md text-[var(--app-text-subtle)] hover:bg-[var(--app-danger-bg)]/80 hover:text-[var(--app-danger)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-primary)]/40 transition-colors opacity-100 md:opacity-0 md:group-hover/row:opacity-100 md:group-focus-within/row:opacity-100"
    }
  }
};
