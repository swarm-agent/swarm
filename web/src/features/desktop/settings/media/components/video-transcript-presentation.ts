import type { VideoTranscript } from '../queries/get-media-settings'

export type VideoTranscriptSegment = VideoTranscript['segments'][number]

function timestamp(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  const short = `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
  return hours > 0 ? `${String(hours).padStart(2, '0')}:${short}` : short
}

export function formatTimelineRange(startMs: number, endMs: number): string {
  return `${timestamp(startMs)}–${timestamp(Math.max(startMs, endMs))}`
}

export function transcriptSegmentDetails(segment: VideoTranscriptSegment): Array<{ label: string; value: string }> {
  const details = [
    { label: 'Speech', value: segment.speech?.trim() ?? '' },
    { label: 'Audio', value: segment.audio?.trim() ?? '' },
    { label: 'Visual', value: segment.visual?.trim() ?? '' },
    { label: 'On-screen text', value: segment.on_screen_text?.trim() ?? '' },
  ].filter((detail) => detail.value)
  return details.length ? details : [{ label: 'Timeline', value: segment.text.trim() }]
}
