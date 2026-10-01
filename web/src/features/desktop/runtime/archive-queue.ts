/** Shared bounded mutation lane: keys are reserved synchronously, including queued work. */
export class ArchiveQueue {
  private pending = new Map<string, Promise<unknown>>()
  private waiting: Array<() => void> = []
  private active = 0
  run<T>(key: string, work: () => Promise<T>): Promise<T> {
    const existing = this.pending.get(key)
    if (existing) return existing as Promise<T>
    const result = new Promise<T>((resolve, reject) => {
      this.waiting.push(() => { this.active++; void Promise.resolve().then(work).then(resolve, reject).finally(() => { this.active--; this.pending.delete(key); this.drain() }) })
    })
    this.pending.set(key, result)
    this.drain()
    return result
  }
  private drain() { while (this.active < 4 && this.waiting.length) this.waiting.shift()!() }
}
export const archiveQueue = new ArchiveQueue()
