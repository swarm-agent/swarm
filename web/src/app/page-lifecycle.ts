import { withRequestDeadline } from './request-lifecycle'

export interface PageLifecycle {
  document: Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>
  window: Pick<Window, 'addEventListener' | 'removeEventListener'>
}

export function observePageActivity(onChange: (active: boolean) => void, page: PageLifecycle = { document, window }): () => void {
  let hidden = false
  const update = () => onChange(!hidden && page.document.visibilityState !== 'hidden')
  const hide = () => { hidden = true; update() }
  const show = () => { hidden = false; update() }
  page.document.addEventListener('visibilitychange', update)
  page.window.addEventListener('pagehide', hide)
  page.window.addEventListener('pageshow', show)
  update()
  return () => {
    page.document.removeEventListener('visibilitychange', update)
    page.window.removeEventListener('pagehide', hide)
    page.window.removeEventListener('pageshow', show)
  }
}

export async function withPageRequest<T>(operation: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> {
  if (typeof document === 'undefined' || typeof window === 'undefined') return operation(signal ?? new AbortController().signal)
  const controller = new AbortController()
  const abort = () => controller.abort(signal?.reason)
  signal?.addEventListener('abort', abort, { once: true })
  if (signal?.aborted) abort()
  const release = observePageActivity((active) => { if (!active) controller.abort() })
  try {
    return await withRequestDeadline(operation, undefined, controller.signal)
  } finally {
    release()
    signal?.removeEventListener('abort', abort)
  }
}

// An activity epoch owns both its request and delay. Hide, teardown and route
// replacement synchronously invalidate it; a late result cannot publish.
export function startPagePolling(
  refresh: (signal: AbortSignal) => Promise<number>,
  page?: PageLifecycle,
): () => void {
  let current: AbortController | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  const stop = () => {
    current?.abort()
    current = undefined
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }
  const release = observePageActivity((active) => {
    if (!active) { stop(); return }
    if (current) return
    const epoch = new AbortController()
    current = epoch
    const poll = async () => {
      let delay = 5_000
      try { delay = await refresh(epoch.signal) } catch { /* Retry only in the same live epoch. */ }
      if (!epoch.signal.aborted) timer = setTimeout(() => { void poll() }, Math.max(250, delay))
    }
    void poll()
  }, page)
  return () => { release(); stop() }
}
