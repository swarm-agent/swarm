import { apiFetch, readErrorMessage } from '../../../app/api'

export interface GitWatchSelector {
  workspace_path: string
  session_id?: string
  branch: string
}
export interface GitWatchNotice { index: number; kind: 'ready' | 'changed' | 'lost'; error?: string; reason_code?: string }
export type SubscribeGit = (repositories: GitWatchSelector[], listener: (notice: GitWatchNotice) => void) => () => void

// One multiplexed push stream, not a status/long-poll loop. Only a
// broken transport reconnects; ready after native setup repairs the missed gap.
export const subscribeGit: SubscribeGit = (repositories, listener) => {
  const controller = new AbortController()
  let retry: ReturnType<typeof setTimeout> | undefined
  let delay = 1000
  // Stable admission order is supplied by the board: retain admitted selectors,
  // then fill vacant slots. Overflow is explicitly stale, never a second stream.
  const admitted = repositories.slice(0, 256)
  queueMicrotask(() => {
    if (!controller.signal.aborted) repositories.slice(256).forEach((_, offset) => listener({
      index: 256 + offset, kind: 'lost', reason_code: 'watch_capacity',
      error: 'Git watch capacity reached (256 selectors); retry after other subscriptions are released',
    }))
  })
  const connect = async () => {
    let retryable = true
    try {
      const response = await apiFetch('/v1/workspace/git/subscriptions', {
        method: 'POST', signal: controller.signal,
        headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ repositories: admitted }),
      })
      if (!response.ok) {
        retryable = response.status >= 500 || response.status === 429
        throw new Error(await readErrorMessage(response))
      }
      if (!response.body) throw new Error('Git subscription has no stream')
      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      try {
        while (!controller.signal.aborted) {
          const { done, value } = await reader.read()
          if (done) throw new Error('Git subscription disconnected')
          buffer += decoder.decode(value, { stream: true })
          if (buffer.length > 64 * 1024) throw new Error('Git subscription frame too large')
          let end: number
          while ((end = buffer.indexOf('\n\n')) >= 0) {
            const frame = buffer.slice(0, end)
            buffer = buffer.slice(end + 2)
            if (!frame.startsWith('data: ')) continue
            const notice = JSON.parse(frame.slice(6)) as GitWatchNotice
            if (!Number.isInteger(notice.index) || notice.index < 0 || notice.index >= admitted.length ||
              !['ready', 'changed', 'lost'].includes(notice.kind)) throw new Error('Invalid Git subscription frame')
            delay = 1000
            listener(notice)
          }
        }
      } finally {
        await reader.cancel().catch(() => undefined)
        reader.releaseLock()
      }
    } catch (error) {
      if (controller.signal.aborted) return
      admitted.forEach((_, index) => listener({ index, kind: 'lost', error: error instanceof Error ? error.message : 'Git subscription disconnected' }))
      if (!retryable) return // explicit refresh/auth reconnect owns permanent HTTP failures
      retry = setTimeout(() => { void connect() }, delay)
      delay = Math.min(delay * 2, 30_000)
    }
  }
  if (admitted.length) void connect()
  return () => { controller.abort(); if (retry !== undefined) clearTimeout(retry) }
}
