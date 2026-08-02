export const DESKTOP_ROUTED_MANAGED_WORKTREE_REQUESTED_METADATA_KEY = 'managed_worktree_requested'

export interface DesktopRoutedWorktreeIntent {
  requested: boolean
}

export interface DesktopRoutedWorktreeIntentSnapshot {
  version: 1
  requested: boolean
}

function requireBoolean(value: boolean, context: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`${context} must be boolean`)
  return value
}

/** Managed worktrees are the app default; callers pass false only for an explicit current-workspace override. */
export function createDesktopRoutedWorktreeIntent(requested = true): DesktopRoutedWorktreeIntent {
  return { requested: requireBoolean(requested, 'Desktop routed worktree intent') }
}

export function setDesktopRoutedWorktreeIntent(
  _current: DesktopRoutedWorktreeIntent,
  requested: boolean,
): DesktopRoutedWorktreeIntent {
  return { requested: requireBoolean(requested, 'Desktop routed worktree intent') }
}

export function toggleDesktopRoutedWorktreeIntent(
  current: DesktopRoutedWorktreeIntent,
): DesktopRoutedWorktreeIntent {
  return { requested: !current.requested }
}

/** Captures the complete local worktree intent so a failed routed start can restore it exactly. */
export function captureDesktopRoutedWorktreeIntent(
  current: DesktopRoutedWorktreeIntent,
): DesktopRoutedWorktreeIntentSnapshot {
  return { version: 1, requested: current.requested }
}

export function restoreDesktopRoutedWorktreeIntent(
  snapshot: DesktopRoutedWorktreeIntentSnapshot,
): DesktopRoutedWorktreeIntent {
  if (snapshot.version !== 1 || typeof snapshot.requested !== 'boolean') {
    throw new Error('Desktop routed worktree intent snapshot is invalid')
  }
  return { requested: snapshot.requested }
}

/**
 * Adds only boolean UI intent to routed metadata. Operation identity remains owned
 * by the routed-start controller, so this helper cannot create a second request.
 */
export function encodeDesktopRoutedWorktreeIntentMetadata(
  current: DesktopRoutedWorktreeIntent,
  metadata: Readonly<Record<string, unknown>> = {},
): Record<string, unknown> {
  return {
    ...metadata,
    [DESKTOP_ROUTED_MANAGED_WORKTREE_REQUESTED_METADATA_KEY]: current.requested,
  }
}
