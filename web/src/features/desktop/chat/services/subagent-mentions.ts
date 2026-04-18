export function normalizeMentionSubagents(items: string[]): string[] {
  if (items.length === 0) {
    return []
  }
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of items) {
    const name = item.trim()
    if (!name) {
      continue
    }
    const key = name.toLowerCase()
    if (seen.has(key)) {
      continue
    }
    seen.add(key)
    out.push(name)
  }
  return out.sort((left, right) => left.toLowerCase().localeCompare(right.toLowerCase()))
}

export function resolveMentionSubagent(name: string, subagents: string[]): string | null {
  const trimmed = name.trim()
  if (!trimmed) {
    return null
  }
  for (const candidate of normalizeMentionSubagents(subagents)) {
    if (candidate.trim().toLowerCase() === trimmed.toLowerCase()) {
      return candidate
    }
  }
  return null
}

export interface ParsedTargetedSubagentPrompt {
  prompt: string
  targetKind: 'subagent'
  targetName: string
}

export function mentionPaletteQuery(prompt: string): string {
  const trimmedStart = prompt.replace(/^[\s\t\r\n]+/, '')
  if (!trimmedStart.startsWith('@')) {
    return ''
  }
  const withoutAt = trimmedStart.slice(1)
  const fields = withoutAt.trim().split(/\s+/).filter(Boolean)
  if (fields.length === 0) {
    return ''
  }
  return fields[0].trim().toLowerCase()
}

export function mentionHasArgs(prompt: string): boolean {
  const trimmedStart = prompt.replace(/^[\s\t\r\n]+/, '')
  if (!trimmedStart.startsWith('@')) {
    return false
  }
  return trimmedStart.slice(1).trim().split(/\s+/).filter(Boolean).length > 1
}

export function chatMentionCandidates(query: string, subagents: string[]): string[] {
  const candidates = normalizeMentionSubagents(subagents)
  const normalizedQuery = query.trim().toLowerCase()
  if (!normalizedQuery) {
    return candidates
  }
  const prefixMatches: string[] = []
  const containsMatches: string[] = []
  for (const candidate of candidates) {
    const normalizedCandidate = candidate.trim().toLowerCase()
    if (!normalizedCandidate) {
      continue
    }
    if (normalizedCandidate.startsWith(normalizedQuery)) {
      prefixMatches.push(candidate)
      continue
    }
    if (normalizedCandidate.includes(normalizedQuery)) {
      containsMatches.push(candidate)
    }
  }
  return [...prefixMatches, ...containsMatches]
}

export function mentionPaletteActive(prompt: string, subagents: string[]): boolean {
  if (normalizeMentionSubagents(subagents).length === 0) {
    return false
  }
  const trimmedStart = prompt.replace(/^[\s\t\r\n]+/, '')
  if (!trimmedStart.startsWith('@')) {
    return false
  }
  return !mentionHasArgs(trimmedStart)
}

export function parseTargetedSubagentPrompt(
  prompt: string,
  subagents: string[],
): ParsedTargetedSubagentPrompt | null {
  const trimmed = prompt.trim()
  if (!trimmed.startsWith('@')) {
    return null
  }

  const withoutAt = trimmed.slice(1)
  const firstWhitespace = withoutAt.search(/\s/)
  const token = (firstWhitespace >= 0 ? withoutAt.slice(0, firstWhitespace) : withoutAt).trim()
  if (!token) {
    return null
  }

  const task = (firstWhitespace >= 0 ? withoutAt.slice(firstWhitespace) : '').trim()
  if (!task) {
    return null
  }

  const targetName = resolveMentionSubagent(token, subagents)
  if (!targetName) {
    return null
  }

  return {
    prompt: task,
    targetKind: 'subagent',
    targetName,
  }
}
