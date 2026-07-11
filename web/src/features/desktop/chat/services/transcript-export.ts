import type { ChatMessageRecord } from '../types/chat'

export interface TranscriptMetadata {
  title: string
  workspaceName?: string
  sessionId?: string
  exportedAt?: Date
}

export function sanitizeTranscriptFilename(title: string): string {
  const stem = title.trim().toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80)
  return `${stem || 'conversation'}.md`
}

export function formatConversationMarkdown(
  metadata: TranscriptMetadata,
  messages: ReadonlyArray<Pick<ChatMessageRecord, 'id' | 'globalSeq' | 'role' | 'content'>>,
): string {
  const title = metadata.title.trim() || 'Conversation'
  const lines = [`# ${title}`, '']
  if (metadata.workspaceName?.trim()) lines.push(`- Workspace: ${metadata.workspaceName.trim()}`)
  if (metadata.sessionId?.trim()) lines.push(`- Session: ${metadata.sessionId.trim()}`)
  if (metadata.exportedAt) lines.push(`- Exported: ${metadata.exportedAt.toISOString()}`)
  if (lines.length > 2) lines.push('')

  const visible = messages
    .filter((message) => message.role.toLowerCase() === 'user' || message.role.toLowerCase() === 'assistant')
    .filter((message) => message.content.trim())
    .slice()
    .sort((left, right) => left.globalSeq - right.globalSeq || left.id.localeCompare(right.id))

  for (const message of visible) {
    lines.push(`## ${message.role.toLowerCase() === 'user' ? 'User' : 'Assistant'}`, '', message.content.trim(), '')
  }
  return `${lines.join('\n').trimEnd()}\n`
}

export async function loadCompleteConversationMessages(
  initialMessages: ChatMessageRecord[],
  fetchOlder: (beforeSeq: number) => Promise<{ messages: ChatMessageRecord[]; hasMoreOlder: boolean; nextBeforeSeq: number }>,
): Promise<ChatMessageRecord[]> {
  const byId = new Map(initialMessages.map((message) => [message.id, message]))
  let beforeSeq = initialMessages.reduce((min, message) => min === 0 ? message.globalSeq : Math.min(min, message.globalSeq), 0)
  if (beforeSeq <= 0) {
    const page = await fetchOlder(0)
    page.messages.forEach((message) => byId.set(message.id, message))
    beforeSeq = page.nextBeforeSeq
    if (!page.hasMoreOlder) return [...byId.values()].sort((a, b) => a.globalSeq - b.globalSeq)
  }
  while (beforeSeq > 0) {
    const page = await fetchOlder(beforeSeq)
    page.messages.forEach((message) => byId.set(message.id, message))
    if (!page.hasMoreOlder) break
    if (!page.messages.length || page.nextBeforeSeq <= 0 || page.nextBeforeSeq >= beforeSeq) {
      throw new Error('Unable to retrieve complete conversation history')
    }
    beforeSeq = page.nextBeforeSeq
  }
  return [...byId.values()].sort((a, b) => a.globalSeq - b.globalSeq || a.id.localeCompare(b.id))
}
