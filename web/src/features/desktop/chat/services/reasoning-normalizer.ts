const CODEX_REASONING_BOLD_LEAD = /^(?:\*\*|__)(.+?)(?:\*\*|__)(?:\s+|$)(.*)$/s
const REASONING_XML_TAG = /<\/?[a-zA-Z][a-zA-Z0-9:_-]*(?:\s+[^<>]*)?>/g
const REASONING_BRACKET_TAG = /\[(?:\/?(?:thinking|analysis|reasoning|summary)[^\]]*)\]/gi
const REASONING_COLLAPSED_MARKERS = /[*_]{4,}/g
const REASONING_WHITESPACE = /\s+/g

export interface NormalizedReasoningSnapshot {
  summary: string
  text: string
  markdown: string
}

function cleanReasoningInlineText(value: string): string {
  return value
    .replace(REASONING_XML_TAG, ' ')
    .replace(REASONING_BRACKET_TAG, ' ')
    .replace(REASONING_COLLAPSED_MARKERS, ' ')
    .replace(/\*\*/g, '')
    .replace(/__/g, '')
    .replace(REASONING_WHITESPACE, ' ')
    .trim()
}

function splitReasoningParagraphs(value: string): string[] {
  return value
    .replace(/\r\n/g, '\n')
    .split(/\n\s*\n+/)
    .map((paragraph) => paragraph.trim())
    .filter(Boolean)
}

function firstSentence(value: string): string {
  return cleanReasoningInlineText(value).split(/(?<=[.!?])\s+/)[0]?.trim() ?? ''
}

export function normalizeReasoningSnapshot(value: string, provider = 'codex'): NormalizedReasoningSnapshot {
  const raw = value.trim()
  if (!raw) {
    return { summary: '', text: '', markdown: '' }
  }

  const paragraphs = splitReasoningParagraphs(raw)
  if (paragraphs.length === 0) {
    return { summary: '', text: '', markdown: '' }
  }

  // Codex Responses API reasoning summaries stream as snapshots shaped like:
  //   **Short headline**
  //
  //   Longer explanatory paragraph...
  // Normalize that into a stable title + body instead of rendering the whole
  // snapshot as an opaque paragraph in Desktop live state.
  if (provider.trim().toLowerCase() === 'codex' || provider.trim() === '') {
    const first = paragraphs[0].replace(REASONING_XML_TAG, ' ').replace(REASONING_BRACKET_TAG, ' ').trim()
    const match = CODEX_REASONING_BOLD_LEAD.exec(first)
    if (match) {
      const summary = cleanReasoningInlineText(match[1] ?? '')
      const firstRemainder = cleanReasoningInlineText(match[2] ?? '')
      const bodyParagraphs = [firstRemainder, ...paragraphs.slice(1).map(cleanReasoningInlineText)].filter(Boolean)
      const text = bodyParagraphs.join('\n\n')
      const markdown = [summary ? `**${summary}**` : '', text].filter(Boolean).join('\n\n')
      return { summary, text, markdown }
    }
  }

  const cleaned = paragraphs.map(cleanReasoningInlineText).filter(Boolean)
  const text = cleaned.join('\n\n')
  const summary = firstSentence(cleaned[0] ?? '').slice(0, 120)
  return { summary, text, markdown: text }
}
