/** Managed worktree isolation is a backend invariant, not client JSON or metadata intent. */
export function desktopRoutedSessionMetadata(
  metadata: Readonly<Record<string, unknown>> = {},
): Record<string, unknown> {
  const next = { ...metadata }
  delete next.managed_worktree_requested
  return next
}
