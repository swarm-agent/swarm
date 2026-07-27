import type { DesktopLiveAssistantSegment } from '../types/realtime'

export function appendLiveAssistantSegment(
  segments: DesktopLiveAssistantSegment[],
  content: string,
  createdAt: number,
  seq: number,
): DesktopLiveAssistantSegment[] {
  if (!content.trim()) {
    return segments
  }
  const safeCreatedAt = createdAt > 0 ? createdAt : Date.now()
  const safeSeq = seq > 0 ? seq : 0
  return [
    ...segments,
    {
      id: `live-assistant:${safeCreatedAt}:${safeSeq}:${segments.length}`,
      content,
      createdAt: safeCreatedAt,
      seq: safeSeq,
    },
  ]
}
