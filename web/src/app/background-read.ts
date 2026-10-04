import { throwIfAborted } from './request-lifecycle'

// One bounded queue for optional card reads across independently mounted cards.
// Selected conversation hydration deliberately does not wait behind this queue.
let active = 0
const queue: Array<() => void> = []
function drain() {
  while (active < 4 && queue.length) queue.shift()!()
}
export function backgroundRead<T>(read: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const cancel = () => {
      const index = queue.indexOf(start)
      if (index >= 0) queue.splice(index, 1)
      reject(signal?.reason ?? new DOMException('Read cancelled', 'AbortError'))
    }
    const start = () => {
      signal?.removeEventListener('abort', cancel)
      if (signal?.aborted) { cancel(); return }
      active++
      void Promise.resolve().then(() => { throwIfAborted(signal); return read() })
        .then(resolve, reject).finally(() => { active--; drain() })
    }
    if (signal?.aborted) { cancel(); return }
    signal?.addEventListener('abort', cancel, { once: true })
    queue.push(start)
    drain()
  })
}
