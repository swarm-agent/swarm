import type { DesktopV3MediaCapability } from '../../state/desktop-v3-cache-types'

// A mounted consumer reuses its initial canonical hydration, not a second GET.
// Only authority changes/reconnect refresh it. No retained cross-session cache.
export class SessionMediaCapabilityReader {
  private generation = 0
  private scope = ''
  private authority = ''
  private hydrated: DesktopV3MediaCapability | null = null
  private credentials: string | undefined
  private initialized = false
  private disconnected = false

  reset() { this.generation++; this.scope = ''; this.initialized = false }

  async update(input: {
    scope: string; authority: string; hydrated: DesktopV3MediaCapability | null
    ready: boolean; connected: boolean; credentials?: string
  }, read: () => Promise<DesktopV3MediaCapability>, publish: (value: DesktopV3MediaCapability | null, error?: string) => void): Promise<void> {
    if (input.scope !== this.scope) {
      this.reset()
      this.scope = input.scope
      this.authority = input.authority
      this.hydrated = null
      this.credentials = undefined
      this.disconnected = false
    }
    if (!input.scope || !input.ready) return
    // Initial optional credential discovery is not a change to the capability
    // already authorized by hydration. Later credential changes must reauthorize.
    const credentialsChanged = this.credentials !== undefined && input.credentials !== undefined && input.credentials !== this.credentials
    if (input.credentials !== undefined) this.credentials = input.credentials
    const authorityChanged = this.initialized && (input.authority !== this.authority || credentialsChanged)
    const reconnected = this.disconnected && input.connected
    this.disconnected = this.initialized && !input.connected
    this.authority = input.authority
    const hydrationChanged = input.hydrated !== this.hydrated
    this.hydrated = input.hydrated
    if (!authorityChanged && !reconnected && this.initialized && !hydrationChanged) return
    this.initialized = true
    const generation = ++this.generation
    if (!authorityChanged && !reconnected && input.hydrated) {
      publish(input.hydrated.resolution_error ? null : input.hydrated, input.hydrated.resolution_error)
      return
    }
    publish(null)
    try {
      const capability = await read()
      if (generation === this.generation) publish(capability)
    } catch (error) {
      if (generation === this.generation) publish(null, error instanceof Error ? error.message : 'Unable to load media capability')
    }
  }
}
