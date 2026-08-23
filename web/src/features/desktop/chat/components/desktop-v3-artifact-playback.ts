import type {
  DesktopV3ArtifactIterationDescriptor,
  DesktopV3ArtifactIterationSection,
} from '../../session-v3/artifact-iteration-protocol'

export interface DesktopV3ArtifactAutoplayRequest {
  artifactKey: string
  sectionId: string
}

export function resolveDesktopV3ArtifactAutoplaySection(
  request: DesktopV3ArtifactAutoplayRequest | null,
  selectedArtifactKey: string,
  loadedArtifactKey: string,
  descriptor: DesktopV3ArtifactIterationDescriptor | null,
): DesktopV3ArtifactIterationSection | null {
  if (!request?.artifactKey || !request.sectionId || !descriptor) return null
  if (selectedArtifactKey !== request.artifactKey || loadedArtifactKey !== request.artifactKey) return null
  return descriptor.sections.find((section) => section.id === request.sectionId) ?? null
}

type SeekSender = (timeMs: number) => string

export class DesktopV3ArtifactSeekAcknowledger {
  private activeRequestId = ''
  private pendingSeekMs: number | null = null
  private onSettled: (() => void) | null = null

  constructor(private readonly send: SeekSender) {}

  reset(): void {
    this.activeRequestId = ''
    this.pendingSeekMs = null
    this.onSettled = null
  }

  setOnSettled(callback: (() => void) | null): void {
    this.onSettled = callback
  }

  queue(timeMs: number): void {
    if (this.activeRequestId) {
      this.pendingSeekMs = timeMs
      return
    }
    this.activeRequestId = this.send(timeMs)
  }

  acknowledge(requestId: unknown): boolean {
    if (typeof requestId !== 'string' || !this.activeRequestId || requestId !== this.activeRequestId) return false
    this.activeRequestId = ''
    const pendingSeekMs = this.pendingSeekMs
    this.pendingSeekMs = null
    if (pendingSeekMs !== null) {
      this.queue(pendingSeekMs)
      return true
    }
    const onSettled = this.onSettled
    this.onSettled = null
    onSettled?.()
    return true
  }
}
