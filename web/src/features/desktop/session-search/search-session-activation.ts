import type { DesktopSessionSearchItem } from './session-search-api'

export interface SearchSessionActivationDependencies {
  unarchive: (versions: Record<string, number>) => Promise<unknown>
  openSession: (item: DesktopSessionSearchItem) => void
}

export async function activateSearchSession(
  item: DesktopSessionSearchItem,
  dependencies: SearchSessionActivationDependencies,
): Promise<void> {
  if (item.archived) await dependencies.unarchive({ [item.id]: item.updated_at })
  dependencies.openSession(item)
}
