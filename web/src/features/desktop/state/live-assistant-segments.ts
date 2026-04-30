import type { DesktopLiveAssistantSegment } from '../types/realtime'

export function appendLiveAssistantSegment(
  segments: DesktopLiveAssistantSegment[],
  content: string,
  createdAt: number,
  seq: number,
): DesktopLiveAssistantSegment[] {
  const trimmed = content.trim()
  if (!trimmed) {
    return segments
  }
  const safeCreatedAt = createdAt > 0 ? createdAt : Date.now()
  const safeSeq = seq > 0 ? seq : 0
  return [
    ...segments,
    {
      id: `live-assistant:${safeCreatedAt}:${safeSeq}:${segments.length}`,
      content: trimmed,
      createdAt: safeCreatedAt,
      seq: safeSeq,
    },
  ]
}
