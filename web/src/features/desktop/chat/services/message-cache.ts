import type { ChatMessageRecord } from '../types/chat'

export const DESKTOP_MESSAGE_HOT_CACHE_LIMIT = 200

function messageSort(left: ChatMessageRecord, right: ChatMessageRecord): number {
  const leftSeq = Number.isFinite(left.globalSeq) ? left.globalSeq : 0
  const rightSeq = Number.isFinite(right.globalSeq) ? right.globalSeq : 0
  if (leftSeq !== rightSeq) {
    return leftSeq - rightSeq
  }
  return (left.createdAt - right.createdAt) || left.id.localeCompare(right.id)
}

function sortMessages(messages: ChatMessageRecord[]): ChatMessageRecord[] {
  return [...messages].sort(messageSort)
}

function normalizeMessageLimit(limit: number): number {
  return Number.isFinite(limit) ? Math.max(0, Math.floor(limit)) : DESKTOP_MESSAGE_HOT_CACHE_LIMIT
}

function trimMessageCache(messages: ChatMessageRecord[], limit = DESKTOP_MESSAGE_HOT_CACHE_LIMIT): ChatMessageRecord[] {
  const normalizedLimit = normalizeMessageLimit(limit)
  if (normalizedLimit === 0) {
    return []
  }
  const sorted = sortMessages(messages)
  return sorted.length > normalizedLimit ? sorted.slice(sorted.length - normalizedLimit) : sorted
}

export function isPendingUserMessage(message: ChatMessageRecord): boolean {
  return message.role === 'user' && message.id.startsWith('pending-user:')
}

export function createPendingUserMessage(sessionId: string, prompt: string, baselineSeq: number): ChatMessageRecord {
  const normalizedBaselineSeq = Number.isFinite(baselineSeq) ? Math.max(0, Math.floor(baselineSeq)) : 0
  const id = `pending-user:${sessionId}:${normalizedBaselineSeq + 1}`
  return {
    id,
    sessionId,
    globalSeq: normalizedBaselineSeq + 1,
    role: 'user',
    content: prompt,
    createdAt: Date.now(),
    metadata: { client_request_id: `desktop-v3-message:${id}` },
  }
}

function messageMetadataString(message: ChatMessageRecord, key: string): string {
  const value = message.metadata?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function messageClientRequestId(message: ChatMessageRecord): string {
  return messageMetadataString(message, 'client_request_id')
    || messageMetadataString(message, 'clientRequestId')
    || messageMetadataString(message, 'request_id')
    || messageMetadataString(message, 'requestId')
}

function samePendingUserMessage(left: ChatMessageRecord, right: ChatMessageRecord): boolean {
  if (left.sessionId !== right.sessionId || left.role !== 'user' || right.role !== 'user') {
    return false
  }
  const leftRequestId = messageClientRequestId(left)
  const rightRequestId = messageClientRequestId(right)
  if (leftRequestId && rightRequestId) {
    return leftRequestId === rightRequestId
  }
  return left.content.trim() === right.content.trim()
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

function mergeMessageIntoCacheUnbounded(current: ChatMessageRecord[] | undefined, incoming: ChatMessageRecord): ChatMessageRecord[] {
  const messages = current ?? []
  const canonicalIndex = messages.findIndex((entry) => isSameCanonicalMessage(entry, incoming))
  if (canonicalIndex >= 0) {
    const updated = [...messages]
    updated[canonicalIndex] = incoming
    return sortMessages(updated)
  }

  const pendingIndex = messages.findIndex((entry) => isPendingUserMessage(entry) && samePendingUserMessage(entry, incoming))
  if (pendingIndex >= 0) {
    const updated = [...messages]
    updated[pendingIndex] = incoming
    return sortMessages(updated)
  }

  const existingIndex = messages.findIndex((entry) => shouldReplaceBySequence(entry, incoming))
  if (existingIndex >= 0) {
    const updated = [...messages]
    updated[existingIndex] = incoming
    return sortMessages(updated)
  }

  return sortMessages([...messages, incoming])
}

export function dedupeAndTrimMessages(messages: ChatMessageRecord[], limit = DESKTOP_MESSAGE_HOT_CACHE_LIMIT): ChatMessageRecord[] {
  const merged = messages.reduce<ChatMessageRecord[]>((current, incoming) => mergeMessageIntoCacheUnbounded(current, incoming), [])
  return trimMessageCache(merged, limit)
}

export function mergeMessageIntoCache(current: ChatMessageRecord[] | undefined, incoming: ChatMessageRecord): ChatMessageRecord[] {
  return trimMessageCache(mergeMessageIntoCacheUnbounded(current, incoming))
}

export function appendPendingUserMessage(current: ChatMessageRecord[] | undefined, pending: ChatMessageRecord): ChatMessageRecord[] {
  const messages = current ?? []
  if (messages.some((entry) => samePendingUserMessage(entry, pending) && !isPendingUserMessage(entry))) {
    return trimMessageCache(messages)
  }
  if (messages.some((entry) => entry.id === pending.id)) {
    return trimMessageCache(messages)
  }
  return mergeMessageIntoCache(messages, pending)
}

export function removePendingUserMessage(current: ChatMessageRecord[] | undefined, pendingId: string): ChatMessageRecord[] {
  return trimMessageCache((current ?? []).filter((entry) => entry.id !== pendingId))
}
