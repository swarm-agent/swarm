export type MarkdownInlineNode =
  | { type: 'text'; text: string }
  | { type: 'code'; text: string }
  | { type: 'strong'; children: MarkdownInlineNode[] }
  | { type: 'em'; children: MarkdownInlineNode[] }
  | { type: 'link'; href: string; children: MarkdownInlineNode[] }
  | { type: 'br' }

export interface MarkdownInlineSegments {
  segments: MarkdownInlineNode[][]
}

export interface MarkdownListItem {
  content: MarkdownInlineSegments
  children?: MarkdownBlock[]
}

export type MarkdownBlock =
  | ({ type: 'paragraph' } & MarkdownInlineSegments)
  | ({ type: 'heading'; level: 1 | 2 | 3 | 4 | 5 | 6 } & MarkdownInlineSegments)
  | ({ type: 'blockquote' } & MarkdownInlineSegments)
  | { type: 'unordered-list'; items: MarkdownListItem[] }
  | { type: 'ordered-list'; items: MarkdownListItem[] }
  | { type: 'code'; language: string; code: string }
  | { type: 'hr' }
