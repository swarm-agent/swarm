import type { MarkdownBlock, MarkdownInlineNode, MarkdownInlineSegments, MarkdownListItem } from './types'

function isBlankLine(line: string): boolean {
  return line.trim() === ''
}

function countLeadingSpaces(line: string): number {
  let count = 0
  while (count < line.length && line[count] === ' ') {
    count += 1
  }
  return count
}

function isCodeFenceLine(line: string): boolean {
  return line.trim().startsWith('```')
}

function isBlockquoteLine(line: string): boolean {
  return /^\s*>\s?/.test(line)
}

function isUnorderedListLine(line: string): boolean {
  return /^\s*[-*]\s+/.test(line)
}

function isOrderedListLine(line: string): boolean {
  return /^\s*\d+\.\s+/.test(line)
}

type ListMarker = {
  baseIndent: number
  body: string
}

function isHeadingLine(line: string): boolean {
  return /^(#{1,6})\s+/.test(line)
}

function isHr(line: string): boolean {
  const trimmed = line.trim()
  return trimmed === '---' || trimmed === '***' || trimmed === '___'
}

function normalizeHref(raw: string): string | null {
  const trimmed = raw.trim()
  if (!trimmed) return null
  const lower = trimmed.toLowerCase()
  if (
    lower.startsWith('http://') ||
    lower.startsWith('https://') ||
    lower.startsWith('mailto:') ||
    lower.startsWith('/') ||
    lower.startsWith('#')
  ) {
    return trimmed
  }
  return null
}

function isAutolinkBoundary(source: string, index: number): boolean {
  if (index <= 0) return true
  return /\s|[([{"']/.test(source[index - 1])
}

function isAutolinkPrefix(source: string, index: number): boolean {
  const lower = source.slice(index).toLowerCase()
  return lower.startsWith('http://') || lower.startsWith('https://') || lower.startsWith('mailto:')
}

function trimAutolinkTrailingPunctuation(value: string): string {
  let end = value.length
  while (end > 0 && /[.,!?;:'"\])}]/.test(value[end - 1])) {
    end -= 1
  }
  return value.slice(0, end)
}

function parseAutolinkRun(source: string, start: number): { node: MarkdownInlineNode; nextIndex: number } | null {
  if (!isAutolinkBoundary(source, start) || !isAutolinkPrefix(source, start)) return null

  const match = source.slice(start).match(/^[^\s<]+/)
  if (!match) return null

  const href = normalizeHref(trimAutolinkTrailingPunctuation(match[0]))
  if (!href) return null

  return {
    node: {
      type: 'link',
      href,
      children: [{ type: 'text', text: href }],
    },
    nextIndex: start + href.length,
  }
}

function mergeTextNodes(nodes: MarkdownInlineNode[]): MarkdownInlineNode[] {
  const merged: MarkdownInlineNode[] = []
  for (const node of nodes) {
    const previous = merged[merged.length - 1]
    if (node.type === 'text' && previous?.type === 'text') {
      previous.text += node.text
      continue
    }
    merged.push(node)
  }
  return merged
}

function parseDelimitedRun(
  source: string,
  delimiter: string,
  start: number,
  build: (children: MarkdownInlineNode[]) => MarkdownInlineNode,
  autolink = true,
): { node: MarkdownInlineNode; nextIndex: number } | null {
  if (!source.startsWith(delimiter, start)) return null
  const contentStart = start + delimiter.length
  const end = source.indexOf(delimiter, contentStart)
  if (end <= contentStart) return null
  const inner = source.slice(contentStart, end)
  const children = parseInlineNodes(inner, autolink)
  if (children.length === 0) return null
  return {
    node: build(children),
    nextIndex: end + delimiter.length,
  }
}

function parseInlineNodes(source: string, autolink = true): MarkdownInlineNode[] {
  const nodes: MarkdownInlineNode[] = []
  let index = 0

  while (index < source.length) {
    if (source.startsWith('**', index)) {
      const strong = parseDelimitedRun(source, '**', index, (children) => ({ type: 'strong', children }), autolink)
      if (strong) {
        nodes.push(strong.node)
        index = strong.nextIndex
        continue
      }
    }

    if (source[index] === '*' || source[index] === '_') {
      const delimiter = source[index]
      const emphasis = parseDelimitedRun(source, delimiter, index, (children) => ({ type: 'em', children }), autolink)
      if (emphasis) {
        nodes.push(emphasis.node)
        index = emphasis.nextIndex
        continue
      }
    }

    if (source[index] === '`') {
      const end = source.indexOf('`', index + 1)
      if (end > index + 1) {
        nodes.push({ type: 'code', text: source.slice(index + 1, end) })
        index = end + 1
        continue
      }
    }

    if (autolink) {
      const link = parseAutolinkRun(source, index)
      if (link) {
        nodes.push(link.node)
        index = link.nextIndex
        continue
      }
    }

    if (source[index] === '[') {
      const labelEnd = source.indexOf(']', index + 1)
      if (labelEnd > index + 1 && source[labelEnd + 1] === '(') {
        const hrefEnd = source.indexOf(')', labelEnd + 2)
        if (hrefEnd > labelEnd + 2) {
          const href = normalizeHref(source.slice(labelEnd + 2, hrefEnd))
          if (href) {
            nodes.push({
              type: 'link',
              href,
              children: parseInlineNodes(source.slice(index + 1, labelEnd), false),
            })
            index = hrefEnd + 1
            continue
          }
        }
      }
    }

    let nextSpecial = source.length
    for (const token of ['**', '*', '_', '`', '[', 'http://', 'https://', 'mailto:']) {
      const haystack = token.includes(':') ? source.toLowerCase() : source
      const found = haystack.indexOf(token, index + 1)
      if (found !== -1 && found < nextSpecial) {
        nextSpecial = found
      }
    }
    const textEnd = nextSpecial === source.length ? source.length : nextSpecial
    nodes.push({ type: 'text', text: source.slice(index, textEnd) })
    index = textEnd
  }

  return mergeTextNodes(nodes)
}

function parseInlineSegments(lines: string[]): MarkdownInlineSegments {
  return {
    segments: lines.map((line) => parseInlineNodes(line)),
  }
}

function parseListMarker(line: string, ordered: boolean): ListMarker | null {
  const match = ordered
    ? line.match(/^(\s*)\d+\.\s+(.*)$/)
    : line.match(/^(\s*)[-*]\s+(.*)$/)
  if (!match) {
    return null
  }
  return {
    baseIndent: match[1].length,
    body: match[2],
  }
}

function parseListItems(lines: string[], ordered: boolean): MarkdownListItem[] {
  const items: MarkdownListItem[] = []
  let index = 0

  while (index < lines.length) {
    const line = lines[index]
    if (isBlankLine(line)) {
      index += 1
      continue
    }

    const marker = parseListMarker(line, ordered)
    if (!marker) {
      index += 1
      continue
    }

    const baseIndent = marker.baseIndent
    const itemLines = [marker.body]
    const nestedLines: string[] = []
    index += 1

    while (index < lines.length) {
      const nextLine = lines[index]
      if (isBlankLine(nextLine)) {
        index += 1
        break
      }

      const nextIndent = countLeadingSpaces(nextLine)
      if ((parseListMarker(nextLine, ordered) || parseListMarker(nextLine, !ordered)) && nextIndent <= baseIndent) {
        break
      }

      if (nextIndent > baseIndent) {
        nestedLines.push(nextLine.slice(Math.min(nextLine.length, baseIndent + 2)).trimEnd())
      } else {
        itemLines.push(nextLine.trimEnd())
      }
      index += 1
    }

    const item: MarkdownListItem = {
      content: parseInlineSegments(itemLines),
    }
    if (nestedLines.length > 0) {
      item.children = parseMarkdownBlocks(nestedLines.join('\n'))
    }
    items.push(item)
  }

  return items
}

function parseParagraph(lines: string[]): MarkdownBlock {
  return { type: 'paragraph', ...parseInlineSegments(lines) }
}

function parseHeading(line: string): MarkdownBlock | null {
  const match = line.match(/^(#{1,6})\s+(.*)$/)
  if (!match) return null
  return {
    type: 'heading',
    level: match[1].length as 1 | 2 | 3 | 4 | 5 | 6,
    ...parseInlineSegments([match[2]]),
  }
}

function normalizeNewlines(value: string): string {
  return value.replace(/\r\n/g, '\n')
}

function parseBlockquote(lines: string[]): MarkdownBlock {
  return {
    type: 'blockquote',
    ...parseInlineSegments(lines.map((line) => line.replace(/^\s*>\s?/, ''))),
  }
}

function parseCodeFence(lines: string[]): MarkdownBlock {
  const opening = lines[0]?.trim() ?? '```'
  const language = opening.slice(3).trim()
  const closing = lines.length > 1 && isCodeFenceLine(lines[lines.length - 1])
  const body = closing ? lines.slice(1, -1) : lines.slice(1)
  return {
    type: 'code',
    language,
    code: body.join('\n'),
  }
}

export function splitMarkdownBlocks(content: string): string[] {
  const normalized = normalizeNewlines(content)
  const lines = normalized.split('\n')
  const blocks: string[] = []
  let index = 0

  while (index < lines.length) {
    const line = lines[index]

    if (isBlankLine(line)) {
      index += 1
      continue
    }

    if (isCodeFenceLine(line)) {
      const collected = [line]
      index += 1
      while (index < lines.length) {
        collected.push(lines[index])
        if (isCodeFenceLine(lines[index])) {
          index += 1
          break
        }
        index += 1
      }
      blocks.push(collected.join('\n'))
      continue
    }

    if (isHr(line) || isHeadingLine(line)) {
      blocks.push(line)
      index += 1
      continue
    }

    if (isBlockquoteLine(line)) {
      const collected: string[] = []
      while (index < lines.length && isBlockquoteLine(lines[index])) {
        collected.push(lines[index])
        index += 1
      }
      blocks.push(collected.join('\n'))
      continue
    }

    if (isUnorderedListLine(line) || isOrderedListLine(line)) {
      const ordered = isOrderedListLine(line)
      const collected: string[] = []
      const baseIndent = countLeadingSpaces(line)
      while (index < lines.length) {
        const nextLine = lines[index]
        if (isBlankLine(nextLine)) {
          collected.push(nextLine)
          index += 1
          continue
        }

        const nextIndent = countLeadingSpaces(nextLine)
        if ((ordered && isOrderedListLine(nextLine)) || (!ordered && isUnorderedListLine(nextLine))) {
          if (nextIndent <= baseIndent || collected.length === 0) {
            collected.push(nextLine)
            index += 1
            continue
          }
        }

        if (nextIndent > baseIndent) {
          collected.push(nextLine)
          index += 1
          continue
        }

        break
      }
      while (collected.length > 0 && isBlankLine(collected[collected.length - 1])) {
        collected.pop()
      }
      blocks.push(collected.join('\n'))
      continue
    }

    const collected: string[] = []
    while (index < lines.length) {
      const nextLine = lines[index]
      if (isBlankLine(nextLine)) {
        break
      }
      if (
        isCodeFenceLine(nextLine) ||
        isHr(nextLine) ||
        isHeadingLine(nextLine) ||
        isBlockquoteLine(nextLine) ||
        isUnorderedListLine(nextLine) ||
        isOrderedListLine(nextLine)
      ) {
        break
      }
      collected.push(nextLine)
      index += 1
    }
    blocks.push(collected.join('\n'))
  }

  return blocks.filter((block) => block.trim() !== '')
}

export function parseMarkdownBlock(source: string): MarkdownBlock {
  const lines = normalizeNewlines(source).split('\n')
  const first = lines[0] ?? ''

  if (isCodeFenceLine(first)) {
    return parseCodeFence(lines)
  }

  if (lines.length === 1 && isHr(first)) {
    return { type: 'hr' }
  }

  if (lines.length === 1) {
    const heading = parseHeading(first)
    if (heading) {
      return heading
    }
  }

  if (lines.every((line: string) => isBlockquoteLine(line))) {
    return parseBlockquote(lines)
  }

  if (isUnorderedListLine(first)) {
    return { type: 'unordered-list', items: parseListItems(lines, false) }
  }

  if (isOrderedListLine(first)) {
    return { type: 'ordered-list', items: parseListItems(lines, true) }
  }

  return parseParagraph(lines)
}

export function parseMarkdownBlocks(content: string): MarkdownBlock[] {
  return splitMarkdownBlocks(content).map((block) => parseMarkdownBlock(block))
}
