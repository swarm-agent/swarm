import type { ChatMessageRecord } from '../types/chat'

function messageSort(left: ChatMessageRecord, right: ChatMessageRecord): number {
  const leftSeq = Number.isFinite(left.globalSeq) ? left.globalSeq : 0
  const rightSeq = Number.isFinite(right.globalSeq) ? right.globalSeq : 0
  if (leftSeq !== rightSeq) {
    return leftSeq - rightSeq
  }
  return left.createdAt - right.createdAt
}

export function isPendingUserMessage(message: ChatMessageRecord): boolean {
  return message.role === 'user' && message.id.startsWith('pending-user:')
}

export function createPendingUserMessage(sessionId: string, prompt: string, baselineSeq: number): ChatMessageRecord {
  const normalizedBaselineSeq = Number.isFinite(baselineSeq) ? Math.max(0, Math.floor(baselineSeq)) : 0
  return {
    id: `pending-user:${sessionId}:${normalizedBaselineSeq + 1}`,
    sessionId,
    globalSeq: normalizedBaselineSeq + 1,
    role: 'user',
    content: prompt,
    createdAt: Date.now(),
  }
}

function samePendingUserMessage(left: ChatMessageRecord, right: ChatMessageRecord): boolean {
  return left.sessionId === right.sessionId
    && left.role === 'user'
    && right.role === 'user'
    && left.content.trim() === right.content.trim()
}

function isSameCanonicalMessage(left: ChatMessageRecord, right: ChatMessageRecord): boolean {
  return left.id.trim() !== '' && left.id === right.id
}

function shouldReplaceBySequence(existing: ChatMessageRecord, incoming: ChatMessageRecord): boolean {
  if (existing.sessionId !== incoming.sessionId || existing.globalSeq <= 0 || existing.globalSeq !== incoming.globalSeq) {
    return false
  }
  if (isPendingUserMessage(existing)) {
    return false
  }
  if (isSameCanonicalMessage(existing, incoming)) {
    return true
  }
  return existing.role === incoming.role && !existing.toolMessage && !incoming.toolMessage
}

export function mergeMessageIntoCache(current: ChatMessageRecord[] | undefined, incoming: ChatMessageRecord): ChatMessageRecord[] {
  const messages = current ?? []
  const canonicalIndex = messages.findIndex((entry) => isSameCanonicalMessage(entry, incoming))
  if (canonicalIndex >= 0) {
    const updated = [...messages]
    updated[canonicalIndex] = incoming
    return updated.sort(messageSort)
  }

  const pendingIndex = messages.findIndex((entry) => isPendingUserMessage(entry) && samePendingUserMessage(entry, incoming))
  if (pendingIndex >= 0) {
    const updated = [...messages]
    updated[pendingIndex] = incoming
    return updated.sort(messageSort)
  }

  const existingIndex = messages.findIndex((entry) => shouldReplaceBySequence(entry, incoming))
  if (existingIndex >= 0) {
    const updated = [...messages]
    updated[existingIndex] = incoming
    return updated.sort(messageSort)
  }

  return [...messages, incoming].sort(messageSort)
}

export function appendPendingUserMessage(current: ChatMessageRecord[] | undefined, pending: ChatMessageRecord): ChatMessageRecord[] {
  const messages = current ?? []
  if (messages.some((entry) => samePendingUserMessage(entry, pending) && !isPendingUserMessage(entry))) {
    return messages.sort(messageSort)
  }
  if (messages.some((entry) => entry.id === pending.id)) {
    return messages.sort(messageSort)
  }
  return mergeMessageIntoCache(messages, pending)
}

export function removePendingUserMessage(current: ChatMessageRecord[] | undefined, pendingId: string): ChatMessageRecord[] {
  return (current ?? []).filter((entry) => entry.id !== pendingId).sort(messageSort)
}
