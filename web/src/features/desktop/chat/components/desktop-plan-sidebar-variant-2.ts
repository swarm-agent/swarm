// Desktop Plan Sidebar Variant 2 - "Command deck"
// A crisp operational-console treatment with squared/compact hierarchy, subtle rails,
// monospace checkpoint emphasis, and a polished subagent roster that feels like active process telemetry.

export const desktopPlanSidebarVariant2 = {
  id: "variant-2",
  name: "Command deck",
  description: "A crisp operational-console treatment with squared/compact hierarchy, subtle rails, mono checkpoint emphasis, and a polished subagent roster that feels like active process telemetry.",
  classes: {
    // Outer sidebar container styling
    sidebar: "hidden h-full min-h-0 min-w-0 w-[360px] max-w-[360px] flex-1 overflow-hidden border-l border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-4 min-[1300px]:flex min-[1300px]:flex-col font-mono selection:bg-[var(--app-primary-soft)]",
    
    // Inner column layout
    inner: "flex flex-col flex-1 min-h-0 overflow-hidden gap-3 text-[11px] leading-relaxed",
    
    // Header slot - squared borders
    header: "shrink-0 border-b-2 border-double border-[var(--app-border)] pb-2.5",
    
    // Header title - mono layout
    headerTitle: "text-xs font-bold uppercase tracking-widest text-[var(--app-text)] font-mono flex items-center gap-1.5 before:content-['>_'] before:text-[var(--app-primary)] before:animate-pulse",
    
    // Main scroll region
    scrollRegion: "flex-1 min-h-0 overflow-y-auto pr-1 space-y-3.5 [scrollbar-width:thin]",
    
    // Active checkpoint section - console panel style with a left rail accent
    checkpointSection: "min-w-0 border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 rounded-none relative overflow-hidden before:absolute before:top-0 before:left-0 before:w-[3px] before:h-full before:bg-[var(--app-primary)]",
    
    // Label for current checkpoint
    checkpointLabel: "text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-muted)] font-mono",
    
    // Status badge - classic telemetry outline
    status: "inline-flex items-center text-[9px] font-bold tracking-wider uppercase px-1.5 py-0.5 border border-current rounded-none bg-transparent select-none",
    
    // Checkpoint title box wrapper
    checkpointWrapper: "mt-1.5",
    
    // Checkpoint title
    checkpointTitle: "text-xs font-semibold leading-relaxed break-all font-mono text-[var(--app-text)] bg-transparent p-0 border-0",
    
    // Checkpoint id
    checkpointId: "text-[var(--app-primary)] font-bold mr-1 before:content-['#']",
    
    // Progress container
    progress: "mt-3 pt-2.5 border-t border-dashed border-[var(--app-border)]",
    
    // Progress text and fractions layout
    progressMeta: "flex items-center justify-between text-[9px] font-mono text-[var(--app-text-muted)] uppercase tracking-wider",
    
    // Progress track bar
    progressTrack: "h-1 bg-[var(--app-border)] mt-1.5 rounded-none overflow-hidden",
    
    // Inner progress line
    progressFill: "h-full bg-[var(--app-primary)] transition-all duration-300",
    
    // Next Up container slot
    nextUp: "mt-3 pt-2.5 border-t border-dashed border-[var(--app-border)]",
    
    // Next checkpoint detail box
    nextCheckpoint: "mt-2 border border-[var(--app-border)] bg-[var(--app-surface)] p-2.5 rounded-none flex items-start gap-2 border-l-[3px] border-l-[var(--app-primary-border)] hover:bg-[var(--app-surface-subtle)] transition-colors",
    
    // Tasks list section
    tasksSection: "mt-3 pt-3 border-t border-dashed border-[var(--app-border)]",
    
    // Tasks list uppercase heading
    tasksHeading: "text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-muted)] font-mono mb-2",
    
    // Individual task checklist item
    taskRow: "flex items-start gap-2 py-1 text-[10px] font-mono text-[var(--app-text-muted)] hover:text-[var(--app-text)] leading-relaxed border-b border-dashed border-[var(--app-border)] last:border-0",
    
    // Disclosure button for completed / overflow tasks
    taskDisclosure: "mt-2 flex items-center justify-center w-full py-1 text-[9px] font-mono uppercase tracking-widest text-[var(--app-text-muted)] hover:text-[var(--app-primary)] border border-dashed border-[var(--app-border)] rounded-none hover:bg-[var(--app-surface-subtle)] transition-all",
    
    // Open full plan action button
    openPlanButton: "mt-3.5 h-7 w-full rounded-none border border-[var(--app-border)] hover:bg-[var(--app-surface-subtle)] text-[9px] font-mono uppercase tracking-widest text-[var(--app-text-muted)] hover:text-[var(--app-primary)] hover:border-[var(--app-primary)] transition-all",
    
    // Actions section slot
    actionsSection: "min-w-0 border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-3 rounded-none relative overflow-hidden before:absolute before:top-0 before:left-0 before:w-[3px] before:h-full before:bg-[var(--app-warning)]",
    
    // Subagents nested slots
    subagents: {
      // Details element wrapper
      root: "group min-w-0 overflow-hidden border border-[var(--app-border)] rounded-none bg-[var(--app-surface-subtle)]",
      
      // Summary head
      summary: "flex h-8 cursor-pointer list-none items-center gap-2 px-2.5 text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-muted)] hover:text-[var(--app-primary)] font-mono transition-colors focus-visible:outline-none [&::-webkit-details-marker]:hidden",
      
      // Bot icon
      icon: "size-3 text-[var(--app-text-muted)] group-hover:text-[var(--app-primary)] transition-colors",
      
      // Subagent count label
      label: "font-mono",
      
      // Chevron indicator
      chevron: "size-3 ml-auto text-[var(--app-text-muted)] transition-transform duration-150 group-open:rotate-90",
      
      // Subagents open listing box
      list: "p-1.5 grid gap-1.5 border-t border-[var(--app-border)] bg-[var(--app-surface)] max-h-60 overflow-y-auto [scrollbar-width:thin]",
      
      // Telemetry row for an active/inactive subagent
      row: "group/row flex items-center gap-2 border border-[var(--app-border)] p-1.5 rounded-none hover:border-[var(--app-primary)] transition-colors focus-within:border-[var(--app-primary)] bg-[var(--app-surface-subtle)]",
      
      // Subagent status pill indicator
      status: "size-1.5 shrink-0 rounded-none bg-[var(--app-text-muted)] ring-1 ring-[var(--app-border)] shadow-sm",
      
      // Navigation button wrapper
      navigate: "flex-1 text-left focus-visible:outline-none min-w-0",
      
      // Subagent label/title
      title: "truncate text-[10px] font-bold tracking-tight text-[var(--app-text)] font-mono group-hover/row:text-[var(--app-primary)] transition-colors",
      
      // Metadata (running time, current action summary)
      meta: "flex items-center gap-1 text-[9px] font-mono text-[var(--app-text-muted)] mt-0.5 truncate uppercase tracking-tight",
      
      // Telemetry context/token track bar
      progressTrack: "mt-1 h-[2px] bg-[var(--app-border)] rounded-none overflow-hidden",
      
      // Inner context filled percentage bar
      progressFill: "h-full bg-[var(--app-primary)] transition-all duration-300",
      
      // Emergency telemetry stop button
      stop: "grid h-6 w-6 place-items-center rounded-none border border-[var(--app-border)] text-[var(--app-text-muted)] hover:bg-[var(--app-danger-bg)] hover:text-[var(--app-danger)] hover:border-[var(--app-danger)] focus-visible:outline-none transition-all"
    }
  }
};
