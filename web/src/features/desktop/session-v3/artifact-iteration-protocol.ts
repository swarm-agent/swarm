export const DESKTOP_V3_ARTIFACT_PLAYER_PROTOCOL = 'swarm-player/v1'
export const DESKTOP_V3_ARTIFACT_ITERATION_VERSION = 'swarm.iteration/v1'

export interface DesktopV3ArtifactIterationNarration {
  startMs: number
  endMs: number
  text: string
  detail: string
}

export interface DesktopV3ArtifactIterationSection {
  id: string
  label: string
  startMs: number
  endMs: number
  narration: DesktopV3ArtifactIterationNarration[]
}

export interface DesktopV3ArtifactIterationDescriptor {
  version: typeof DESKTOP_V3_ARTIFACT_ITERATION_VERSION
  durationMs: number
  sections: DesktopV3ArtifactIterationSection[]
}

type UnknownRecord = Record<string, unknown>

function record(value: unknown): UnknownRecord | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as UnknownRecord : null
}

function string(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function integer(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : -1
}

function normalizeNarration(value: unknown, sectionStartMs: number, sectionEndMs: number): DesktopV3ArtifactIterationNarration | null {
  const item = record(value)
  if (!item) return null
  const startMs = integer(item.start_ms)
  const endMs = integer(item.end_ms)
  const text = string(item.text)
  const detail = string(item.detail)
  if (startMs < sectionStartMs || endMs <= startMs || endMs > sectionEndMs || (!text && !detail)) return null
  return { startMs, endMs, text, detail }
}

function normalizeSection(value: unknown, durationMs: number): DesktopV3ArtifactIterationSection | null {
  const item = record(value)
  if (!item) return null
  const id = string(item.id)
  const label = string(item.label)
  const startMs = integer(item.start_ms)
  const endMs = integer(item.end_ms)
  if (!id || !label || startMs < 0 || endMs <= startMs || endMs > durationMs) return null
  const narration = Array.isArray(item.narration)
    ? item.narration.map((entry) => normalizeNarration(entry, startMs, endMs)).filter((entry): entry is DesktopV3ArtifactIterationNarration => entry !== null)
    : []
  for (let index = 1; index < narration.length; index += 1) {
    if (narration[index]!.startMs < narration[index - 1]!.startMs) return null
  }
  return { id, label, startMs, endMs, narration }
}

/**
 * Normalizes the optional descriptor returned by a sandboxed animation artifact.
 * The iframe remains opaque; it may describe sections only through swarm-player/v1.
 */
export function normalizeDesktopV3ArtifactIterationDescriptor(value: unknown): DesktopV3ArtifactIterationDescriptor | null {
  const item = record(value)
  if (!item || string(item.version) !== DESKTOP_V3_ARTIFACT_ITERATION_VERSION) return null
  const durationMs = integer(item.duration_ms)
  if (durationMs <= 0 || !Array.isArray(item.sections) || item.sections.length === 0 || item.sections.length > 64) return null
  const sections = item.sections.map((entry) => normalizeSection(entry, durationMs))
  if (sections.some((entry) => entry === null)) return null
  const normalized = sections as DesktopV3ArtifactIterationSection[]
  for (let index = 1; index < normalized.length; index += 1) {
    if (normalized[index]!.startMs < normalized[index - 1]!.endMs) return null
  }
  return { version: DESKTOP_V3_ARTIFACT_ITERATION_VERSION, durationMs, sections: normalized }
}

export function desktopV3ArtifactIterationMessage(id: string, type: 'describe' | 'seek' | 'stop', timeMs?: number): UnknownRecord {
  return {
    protocol: DESKTOP_V3_ARTIFACT_PLAYER_PROTOCOL,
    id,
    type,
    ...(type === 'seek' ? { time_ms: Math.max(0, Math.round(timeMs ?? 0)) } : {}),
  }
}

export function formatDesktopV3ArtifactIterationTime(timeMs: number): string {
  const bounded = Math.max(0, Math.round(timeMs))
  const minutes = Math.floor(bounded / 60_000)
  const seconds = Math.floor((bounded % 60_000) / 1000)
  const millis = bounded % 1000
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}.${String(millis).padStart(3, '0')}`
}

function desktopV3ArtifactIterationSectionBrief(section: DesktopV3ArtifactIterationSection, alternativeCount: number): string[] {
  const count = Math.min(50, Math.max(1, Math.round(alternativeCount)))
  const narration = section.narration
    .map((line) => `${formatDesktopV3ArtifactIterationTime(line.startMs)}–${formatDesktopV3ArtifactIterationTime(line.endMs)} ${[line.text, line.detail].filter(Boolean).join(' — ')}`)
    .join('\n')
  return [
    `Create ${count} new alternatives for animation section "${section.label}" (${section.id}) from ${formatDesktopV3ArtifactIterationTime(section.startMs)} to ${formatDesktopV3ArtifactIterationTime(section.endMs)}.`,
    `Exact section_target: ${JSON.stringify({ id: section.id, label: section.label, start_ms: section.startMs, end_ms: section.endMs })}`,
    narration ? `Section narration:\n${narration}` : 'This section declares no narration.',
  ]
}

/** Requests sibling alternatives from the immutable source that began the current review round. */
export function desktopV3ArtifactIterationChangeDescription(section: DesktopV3ArtifactIterationSection, alternativeCount = 5): string {
  return [
    ...desktopV3ArtifactIterationSectionBrief(section, alternativeCount),
    'Use a managed Designer Iteration Swarm with the attached exact current-round source artifact and this exact section_target.',
    'Generate sibling alternatives from that round source, not from whichever candidate is currently previewed. Each result must remain a complete derived artifact that preserves every other section, the global duration, deterministic seek contract, output geometry, and surrounding transitions.',
    'Return complete branches for comparison. Do not durably select a collection variant and do not compose or splice content across branches.',
  ].join('\n')
}

/** Advances the active complete artifact head to the next ordered manifest section. */
export function desktopV3ArtifactIterationNextSectionDescription(section: DesktopV3ArtifactIterationSection, alternativeCount = 5): string {
  return [
    ...desktopV3ArtifactIterationSectionBrief(section, alternativeCount),
    'The attached exact artifact is the newly accepted complete artifact head from the previous step. Use it as the sole source artifact for this next ordered section.',
    'Use a managed Designer Iteration Swarm with this exact section_target. Each result must be a complete derived artifact that preserves all non-target sections and the deterministic animation contract.',
    'Continue linearly from this head. Do not compose or splice content from any other branch, and do not durably select a collection variant automatically.',
  ].join('\n')
}
