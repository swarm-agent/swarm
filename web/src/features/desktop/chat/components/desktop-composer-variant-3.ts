export const desktopComposerVariant3 = {
  id: 3 as const,
  label: 'Segmented dock',
  description: 'Intentionally segments the input box and action toolbar with distinct tonal surfaces and a divider, keeping it highly compact and tactile.',
  shellClassName: 'relative min-w-0 overflow-visible rounded-2xl border border-[var(--app-border-muted)] bg-[var(--app-surface-subtle)] transition-all duration-200 shadow-sm focus-within:border-[var(--app-border-strong)] focus-within:shadow-md',
  inputRowClassName: 'flex min-w-0 items-end gap-3 px-4 py-2 sm:py-3 lg:py-2.5 bg-[var(--app-bg-alt)] rounded-t-2xl border-b border-[var(--app-border-muted)] focus-within:bg-[var(--app-bg)] transition-colors duration-200',
  bottomRowClassName: 'flex min-w-0 items-center gap-2 overflow-visible bg-[var(--app-surface)] rounded-b-2xl px-4 py-2 sm:py-2.5 text-[11px]'
};
