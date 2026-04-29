import { memo, useMemo, useRef, useState, type ReactNode } from 'react'
import { cn } from '../../../../lib/cn'
import { parseMarkdownBlock, splitMarkdownBlocks } from './parser'
import type { MarkdownBlock, MarkdownInlineNode, MarkdownInlineSegments, MarkdownListItem } from './types'

interface MarkdownRendererProps {
  content: string
}

type MarkdownCopySegment =
  | { type: 'markdown'; source: string }
  | { type: 'copy'; label: string; content: string }

const copyOpenTagPattern = /<copy(?:\s+[^>]*)?>/gi
const copyLabelPattern = /\b(?:label|title|name)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))/i

function normalizeCopyContent(content: string): string {
  return content.replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/^\n+|\n+$/g, '')
}

function copyTagLabel(openTag: string): string {
  const match = openTag.match(copyLabelPattern)
  if (!match) return ''
  return (match[2] ?? match[3] ?? match[4] ?? '').trim()
}

function splitMarkdownCopySegments(content: string): MarkdownCopySegment[] {
  const normalized = content.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  const segments: MarkdownCopySegment[] = []
  let cursor = 0
  copyOpenTagPattern.lastIndex = 0

  while (true) {
    const match = copyOpenTagPattern.exec(normalized)
    if (!match) break
    if (match.index > cursor) {
      segments.push({ type: 'markdown', source: normalized.slice(cursor, match.index) })
    }

    const contentStart = match.index + match[0].length
    const closeIndex = normalized.toLowerCase().indexOf('</copy>', contentStart)
    const contentEnd = closeIndex === -1 ? normalized.length : closeIndex
    segments.push({
      type: 'copy',
      label: copyTagLabel(match[0]),
      content: normalizeCopyContent(normalized.slice(contentStart, contentEnd)),
    })
    cursor = closeIndex === -1 ? normalized.length : contentEnd + '</copy>'.length
    copyOpenTagPattern.lastIndex = cursor
  }

  if (cursor < normalized.length) {
    segments.push({ type: 'markdown', source: normalized.slice(cursor) })
  }
  return segments.filter((segment) => segment.type === 'copy' || segment.source.trim() !== '')
}

function renderInlineNode(node: MarkdownInlineNode, key: string): ReactNode {
  switch (node.type) {
    case 'text':
      return <span key={key}>{node.text}</span>
    case 'code':
      return (
        <code
          key={key}
          className="rounded-md border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-1.5 py-0.5 font-mono text-[13px] text-[var(--app-text)] whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
        >
          {node.text}
        </code>
      )
    case 'strong':
      return <strong key={key}>{renderInlineNodes(node.children, key)}</strong>
    case 'em':
      return <em key={key}>{renderInlineNodes(node.children, key)}</em>
    case 'link':
      return (
        <a
          key={key}
          href={node.href}
          className="text-[var(--app-primary)] underline underline-offset-2 hover:opacity-85"
          target="_blank"
          rel="noreferrer"
        >
          {renderInlineNodes(node.children, key)}
        </a>
      )
    case 'br':
      return <br key={key} />
    default:
      return null
  }
}

function renderInlineNodes(nodes: MarkdownInlineNode[], keyPrefix: string): ReactNode[] {
  return nodes.map((node, index) => renderInlineNode(node, `${keyPrefix}-${index}`))
}

function renderSegments(segments: MarkdownInlineSegments, keyPrefix: string): ReactNode[] {
  const children: ReactNode[] = []
  segments.segments.forEach((line, index) => {
    if (index > 0) {
      children.push(<br key={`${keyPrefix}-br-${index}`} />)
    }
    children.push(...renderInlineNodes(line, `${keyPrefix}-line-${index}`))
  })
  return children
}

function renderListItems(items: MarkdownListItem[], keyPrefix: string): ReactNode[] {
  return items.map((item, index) => (
    <li key={`${keyPrefix}-${index}`} className="my-1 min-w-0">
      {renderSegments(item.content, `${keyPrefix}-${index}`)}
      {item.children && item.children.length > 0 ? (
        <div className="mt-2 grid min-w-0 gap-3">
          {item.children.map((child, childIndex) => renderBlock(child, `${keyPrefix}-${index}-child-${childIndex}`))}
        </div>
      ) : null}
    </li>
  ))
}

function CopyBlock({ label, content }: { label: string; content: string }) {
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const preview = content.split('\n').slice(0, 8)

  const handleCopy = async () => {
    setFailed(false)
    try {
      if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText) {
        throw new Error('Clipboard unavailable')
      }
      await navigator.clipboard.writeText(content)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      setFailed(true)
      window.setTimeout(() => setFailed(false), 1800)
    }
  }

  return (
    <div className="my-0 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)]">
      <div className="flex items-center justify-between gap-3 border-b border-[var(--app-border)] px-3 py-2 text-xs text-[var(--app-text-muted)]">
        <span className="min-w-0 truncate font-mono text-[var(--app-primary)]">
          /copy{label.trim() ? ` · ${label.trim()}` : ''}
        </span>
        <button
          type="button"
          onClick={() => void handleCopy()}
          className="shrink-0 rounded-md border border-[var(--app-border)] px-2 py-1 text-[var(--app-text)] hover:bg-[var(--app-bg)]"
        >
          {copied ? 'Copied' : failed ? 'Copy failed' : 'Copy'}
        </button>
      </div>
      <pre className="my-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere] px-4 py-3">
        <code className="block min-w-0 font-mono text-[13px] leading-6 text-[var(--app-text)]">
          {preview.join('\n') || '(empty copy block)'}
          {content.split('\n').length > preview.length ? `\n… ${content.split('\n').length - preview.length} more lines` : ''}
        </code>
      </pre>
    </div>
  )
}

function renderBlock(block: MarkdownBlock, key: string | number): ReactNode {
  switch (block.type) {
    case 'paragraph':
      return (
        <p key={key} className="my-0 min-w-0">
          {renderSegments(block, `p-${key}`)}
        </p>
      )
    case 'heading': {
      const className = cn(
        'my-0 min-w-0 font-semibold text-[var(--app-text)]',
        block.level === 1 && 'text-xl',
        block.level === 2 && 'text-lg',
        block.level === 3 && 'text-base',
        block.level >= 4 && 'text-sm uppercase tracking-[0.06em]',
      )
      const Tag = `h${block.level}` as const
      return (
        <Tag key={key} className={className}>
          {renderSegments(block, `h-${key}`)}
        </Tag>
      )
    }
    case 'blockquote':
      return (
        <blockquote
          key={key}
          className="my-0 min-w-0 border-l-2 border-[var(--app-border-strong)] pl-4 text-[var(--app-text-muted)]"
        >
          {renderSegments(block, `quote-${key}`)}
        </blockquote>
      )
    case 'unordered-list':
      return (
        <ul key={key} className="my-0 list-disc pl-6">
          {renderListItems(block.items, `ul-${key}`)}
        </ul>
      )
    case 'ordered-list':
      return (
        <ol key={key} className="my-0 list-decimal pl-6">
          {renderListItems(block.items, `ol-${key}`)}
        </ol>
      )
    case 'code':
      return (
        <pre
          key={key}
          className="my-0 overflow-x-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-inset)] px-4 py-3 whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
        >
          <code className="block min-w-0 whitespace-pre-wrap break-words [overflow-wrap:anywhere] font-mono text-[13px] leading-6 text-[var(--app-text)]">{block.code}</code>
        </pre>
      )
    case 'hr':
      return <hr key={key} className="my-0 border-0 border-t border-[var(--app-border)]" />
    default:
      return null
  }
}

function MarkdownRendererInner({ content }: MarkdownRendererProps) {
  const cacheRef = useRef(new Map<string, MarkdownBlock>())

  const entries = useMemo(() => {
    const segments = splitMarkdownCopySegments(content)
    const nextCache = new Map<string, MarkdownBlock>()
    const resolved = segments.flatMap((segment) => {
      if (segment.type === 'copy') {
        return [{ segment, source: '', block: null as MarkdownBlock | null }]
      }
      return splitMarkdownBlocks(segment.source).map((source) => {
        const cached = cacheRef.current.get(source)
        const block = cached ?? parseMarkdownBlock(source)
        nextCache.set(source, block)
        return { segment, source, block }
      })
    })
    cacheRef.current = nextCache
    return resolved
  }, [content])

  if (entries.length === 0) {
    return null
  }

  return (
    <div className="grid min-w-0 gap-3">
      {entries.map((entry, index) =>
        entry.segment.type === 'copy' ? (
          <CopyBlock key={index} label={entry.segment.label} content={entry.segment.content} />
        ) : entry.block ? (
          renderBlock(entry.block, index)
        ) : null,
      )}
    </div>
  )
}

export const MarkdownRenderer = memo(MarkdownRendererInner)
