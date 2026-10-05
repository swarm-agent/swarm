import { apiFetch, readErrorMessage } from '../../../app/api'

export interface GitWatchSelector {
  workspace_path: string
  session_id?: string
  branch: string
}
export interface GitWatchNotice { index: number; kind: 'ready' | 'changed' | 'lost'; error?: string }
export type SubscribeGit = (repositories: GitWatchSelector[], listener: (notice: GitWatchNotice) => void) => () => void

// One multiplexed push stream, not a status/long-poll loop. Only a
// broken transport reconnects; ready after native setup repairs the missed gap.
export const subscribeGit: SubscribeGit = (repositories, listener) => {
  const controller = new AbortController()
  let retry: ReturnType<typeof setTimeout> | undefined
  let delay = 1000
  if (repositories.length > 256) {
    queueMicrotask(() => { if (!controller.signal.aborted) repositories.forEach((_, index) => listener({ index, kind: 'lost', error: 'Git watch capacity exceeded (256 repository selectors)' })) })
    return () => controller.abort()
  }
  const connect = async () => {
    try {
      const response = await apiFetch('/v1/workspace/git/subscriptions', {
        method: 'POST', signal: controller.signal,
        headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ repositories }),
      })
      if (!response.ok) throw new Error(await readErrorMessage(response))
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
            if (!Number.isInteger(notice.index) || notice.index < 0 || notice.index >= repositories.length ||
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
      repositories.forEach((_, index) => listener({ index, kind: 'lost', error: error instanceof Error ? error.message : 'Git subscription disconnected' }))
      retry = setTimeout(() => { void connect() }, delay)
      delay = Math.min(delay * 2, 30_000)
    }
  }
  void connect()
  return () => { controller.abort(); if (retry !== undefined) clearTimeout(retry) }
}
