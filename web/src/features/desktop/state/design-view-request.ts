/** Exact-view request fence; obsolete responses cannot paint a newly viewed revision. */
export class DesignViewRequest {
  private controller?: AbortController
  async load(read: (signal: AbortSignal) => Promise<string>, ready: (text: string) => void, failed: (error: unknown) => void) {
    this.cancel()
    const controller = new AbortController()
    this.controller = controller
    try {
      const text = await read(controller.signal)
      if (!controller.signal.aborted) ready(text)
    } catch (error) { if (!controller.signal.aborted) failed(error) }
  }
  cancel() { this.controller?.abort() }
}
