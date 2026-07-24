export const desktopComposerVariant5 = {
  id: 5 as const,
  label: "Layered card",
  description: "An elevated layered card with depth conveyed by surface contrast/shadow and a distinct inset writing area or toolbar relationship",
  shellClassName: "relative min-w-0 overflow-visible rounded-2xl border border-[var(--app-border)]/80 bg-[var(--app-bg-panel)] shadow-lg transition-all duration-200 focus-within:border-[var(--app-border-accent)] focus-within:shadow-xl",
  inputRowClassName: "flex min-w-0 items-end gap-3 px-4 py-3 sm:py-3.5 bg-[var(--app-bg-inset)] rounded-xl border border-[var(--app-border)]/60 shadow-inner mx-3 mt-3",
  bottomRowClassName: "flex min-w-0 items-center gap-2 overflow-visible bg-transparent px-4 py-2.5 text-[11px]",
} as const;
