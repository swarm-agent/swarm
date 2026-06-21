import { writeDesktopV3OwnerAndTails } from './desktop-v3-cache-db'
import type {
  PersistedDesktopV3MessageTailV1,
  PersistedDesktopV3OwnerV1,
} from './desktop-v3-cache-persisted-types'

export class DesktopV3CachePersistenceCoordinator {
  private tail: Promise<void> = Promise.resolve()

  enqueue<T>(operation: () => Promise<T>): Promise<T> {
    const result = this.tail.then(operation)
    this.tail = result.then(
      () => undefined,
      () => undefined,
    )
    return result
  }

  async idle(): Promise<void> {
    await this.tail
  }
}

export const desktopV3CachePersistenceCoordinator =
  new DesktopV3CachePersistenceCoordinator()

export function persistDesktopV3OwnerAndTails(
  owner: PersistedDesktopV3OwnerV1,
  tails: PersistedDesktopV3MessageTailV1[] = [],
): Promise<boolean> {
  return writeDesktopV3OwnerAndTails(owner, tails)
}
