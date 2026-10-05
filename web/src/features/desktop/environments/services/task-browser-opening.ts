import { verifiedBrowserURL } from './task-browser-access'
import type { TaskBrowserEndpoint } from '../types/environments'

export interface ReservedBrowserWindow {
  opener: unknown
  location: { replace(url: string): void }
  close(): void
  closed: boolean
}

// Reserve during the click, before any await. The blank never receives a URL
// until fresh backend evidence passes both the ownership fence and URL policy.
export class TaskBrowserOpening {
  private pending?: ReservedBrowserWindow
  private version = 0
  cancel(): void {
    this.version++
    this.pending?.close()
    this.pending = undefined
  }
  async open(reserve: () => ReservedBrowserWindow | null, check: () => Promise<TaskBrowserEndpoint[]>, endpointId: string, valid: () => boolean): Promise<TaskBrowserEndpoint[]> {
    this.cancel()
    const version = this.version
    if (!valid()) throw new Error('Attachment unavailable')
    const popup = reserve()
    if (!popup) throw new Error('Popup blocked. Allow popups for Swarm and retry.')
    this.pending = popup
    try {
      popup.opener = null
      const endpoints = await check()
      if (version !== this.version || !valid() || popup.closed) throw new Error('Browser opening cancelled')
      const endpoint = endpoints.find(item => item.id === endpointId)
      const url = endpoint && verifiedBrowserURL(endpoint)
      if (!url) throw new Error('Frontend not ready')
      popup.location.replace(url)
      this.pending = undefined
      return endpoints
    } catch (error) {
      popup.close()
      if (this.pending === popup) this.pending = undefined
      throw error
    }
  }
}

export function attachmentError(message?: string): string {
  return (message || '').replace(/[\x00-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/g, ' ').trim().slice(0, 512)
}
