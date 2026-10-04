import { throwIfAborted } from '../../../app/request-lifecycle'

interface Entry<T> { id: string; resolve: (value: T) => void; reject: (error: unknown) => void; signal?: AbortSignal }

/** Visible metadata has its own bounded I/O budget, never optional media's FIFO.
 * One microtask coalesces independently mounted cards; no debounce/timer delay.
 * Each completed batch publishes immediately, without an all-siblings barrier.
 */
export class ChildCardHydrationQueue<T> {
  private pending: Entry<T>[] = []
  private active = 0
  private scheduled = false
  constructor(private readonly read: (ids: string[], signal: AbortSignal, currentIds: () => string[]) => Promise<T>, private readonly batchSize = 8, private readonly concurrency = 3) {}
  enqueue(id: string, signal?: AbortSignal): Promise<T> {
    return new Promise((resolve, reject) => {
      if (signal?.aborted) { reject(signal.reason); return }
      this.pending.push({ id, resolve, reject, signal })
      this.schedule()
    })
  }
  private schedule() {
    if (this.scheduled) return
    this.scheduled = true
    queueMicrotask(() => { this.scheduled = false; this.drain() })
  }
  private drain() {
    while (this.active < this.concurrency && this.pending.length) {
      const entries = this.pending.splice(0, this.batchSize).filter(entry => {
        if (!entry.signal?.aborted) return true
        entry.reject(entry.signal.reason); return false
      })
      if (!entries.length) continue
      const controller = new AbortController()
      const cancel = () => { if (entries.every(entry => entry.signal?.aborted)) controller.abort() }
      for (const entry of entries) entry.signal?.addEventListener('abort', cancel, { once: true })
      this.active++
      void Promise.resolve().then(() => { throwIfAborted(controller.signal); return this.read([...new Set(entries.map(entry => entry.id))], controller.signal, () => [...new Set(entries.filter(entry => !entry.signal?.aborted).map(entry => entry.id))]) })
        .then(value => { for (const entry of entries) entry.signal?.aborted ? entry.reject(entry.signal.reason) : entry.resolve(value) }, error => { for (const entry of entries) entry.reject(error) })
        .finally(() => {
          for (const entry of entries) entry.signal?.removeEventListener('abort', cancel)
          this.active--; this.schedule()
        })
    }
  }
}
