// Deadlines belong to bounded reads, not the raw fetch/stream or mutation contract.
export const STARTUP_REQUEST_TIMEOUT_MS = 15_000

export function abortReason(signal: AbortSignal): unknown {
  return signal.reason ?? new DOMException('Request cancelled', 'AbortError')
}

export function throwIfAborted(signal?: AbortSignal | null): void {
  if (signal?.aborted) throw abortReason(signal)
}

// Stop waiting even when an adapter/body reader does not implement abort correctly.
export function waitForSignal<T>(promise: Promise<T>, signal?: AbortSignal | null): Promise<T> {
  if (!signal) return promise
  let abort: () => void
  return new Promise<T>((resolve, reject) => {
    abort = () => reject(abortReason(signal))
    signal.addEventListener('abort', abort, { once: true })
    promise.then(resolve, reject)
    if (signal.aborted) abort()
  }).finally(() => signal.removeEventListener('abort', abort))
}

export async function withRequestDeadline<T>(
  operation: (signal: AbortSignal) => Promise<T>,
  timeoutMs?: number,
  callerSignal?: AbortSignal | null,
): Promise<T> {
  throwIfAborted(callerSignal)
  const controller = new AbortController()
  const abort = () => controller.abort(callerSignal ? abortReason(callerSignal) : undefined)
  callerSignal?.addEventListener('abort', abort, { once: true })
  const timer = timeoutMs === undefined ? undefined : setTimeout(() => {
    controller.abort(new DOMException('Request timed out. Please try again.', 'TimeoutError'))
  }, timeoutMs)
  try {
    const result = await waitForSignal(operation(controller.signal), controller.signal)
    throwIfAborted(controller.signal)
    return result
  } finally {
    if (timer !== undefined) clearTimeout(timer)
    callerSignal?.removeEventListener('abort', abort)
  }
}

interface SharedRequest<T> {
  controller: AbortController
  promise: Promise<T>
  consumers: number
  settled: boolean
}

// Each caller owns a lease, not the shared controller. The last departing caller
// aborts the transport and evicts immediately, so a replacement cannot join it.
export class SharedRequestPool<T> {
  private requests = new Map<string, SharedRequest<T>>()

  run(key: string, operation: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> {
    try { throwIfAborted(signal) } catch (error) { return Promise.reject(error) }
    let request = this.requests.get(key)
    if (!request) {
      const controller = new AbortController()
      request = { controller, consumers: 0, settled: false, promise: undefined! }
      const entry = request
      this.requests.set(key, entry)
      let pending: Promise<T>
      try { pending = operation(controller.signal) } catch (error) { pending = Promise.reject(error) }
      entry.promise = pending.finally(() => {
        entry.settled = true
        if (this.requests.get(key) === entry) this.requests.delete(key)
      })
    }
    const entry = request
    entry.consumers++
    let released = false
    const release = () => {
      if (released) return
      released = true
      signal?.removeEventListener('abort', release)
      if (--entry.consumers === 0 && !entry.settled) {
        if (this.requests.get(key) === entry) this.requests.delete(key)
        entry.controller.abort()
      }
    }
    signal?.addEventListener('abort', release, { once: true })
    return waitForSignal(entry.promise, signal).finally(release)
  }
}
