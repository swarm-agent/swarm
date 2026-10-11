/**
 * Swarm agents wrap copyable snippets (commands, config, file payloads) in
 * `<copy label="...">…</copy>` tags. Swarm's own desktop and terminal UIs render
 * them as copy buttons; everywhere else they are noise. `copyTagsToMarkdown`
 * turns each tag into a plain Markdown code block (with its label, if any, as a
 * bold line above it), so any Markdown renderer shows it correctly.
 *
 * Rules (same parsing as Swarm's own renderers):
 * - Tags inside fenced code blocks (``` or ~~~) are left untouched.
 * - The label comes from `label`, `title` or `name`.
 * - A tag that is never closed becomes a code block running to the end.
 * - Text without copy tags is returned unchanged.
 *
 * Pass `{ streaming: true }` for text that is still arriving: a half-written tag
 * at the very end is hidden, so converting the growing text only ever appends.
 *
 * The SDK applies it by default to assistant text from `chat.run`,
 * `chat.listMessages` and `chat.stream` (`onText`, `onComplete`); pass
 * `rawCopyTags: true` there to get the original text. Lower-level reads
 * (`sessions.get`, `realtime.watchSession`) return the stored text unchanged.
 */
export function copyTagsToMarkdown(text: string, options: { streaming?: boolean } = {}): string {
  if (typeof text !== 'string' || !text) return text;
  const streaming = options.streaming === true;
  if (!(streaming ? /</.test(text) : /<copy[\s>]/i.test(text))) return text;
  const source = text.replace(/\r\n?/g, '\n');
  const protectedRanges = fencedRanges(source);
  let out = '';
  let cursor = 0;
  while (cursor < source.length) {
    const open = nextOpenTag(source, cursor, protectedRanges);
    if (!open) {
      out += source.slice(cursor);
      break;
    }
    out += source.slice(cursor, open.start);
    const afterOpen = source.slice(open.end);
    const closeIndex = afterOpen.toLowerCase().indexOf('</copy>');
    if (closeIndex < 0) {
      let body = afterOpen;
      if (streaming) body = body.replace(PARTIAL_CLOSE_TAIL, '');
      return out + renderBlock(out, tagLabel(open.tag), body.replace(/^\n+/, ''), false, streaming);
    }
    // Streaming keeps trailing newlines it may already have delivered.
    const body = afterOpen.slice(0, closeIndex).replace(streaming ? /^\n+/ : /^\n+|\n+$/g, '');
    out += renderBlock(out, tagLabel(open.tag), body, true, streaming);
    cursor = open.end + closeIndex + '</copy>'.length;
    if (cursor < source.length && source[cursor] !== '\n') out += '\n';
  }
  if (streaming && !inside(out.length - 1, fencedRanges(out))) out = out.replace(PARTIAL_OPEN_TAIL, '');
  return out;
}

/** True when `text` contains at least one `<copy>` tag outside fenced code. */
export function hasCopyTags(text: string): boolean {
  if (typeof text !== 'string' || !/<copy[\s>]/i.test(text)) return false;
  const source = text.replace(/\r\n?/g, '\n');
  return nextOpenTag(source, 0, fencedRanges(source)) !== undefined;
}

const OPEN_TAG = /<copy(?:\s+[^>]*)?>/gi;
const LABEL_ATTR = /\b(?:label|title|name)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i;
// Streaming only: a tag cut off at the very end (`<`, `<co`, `<copy label="x`).
const PARTIAL_OPEN_TAIL = /<(?:c(?:o(?:p(?:y(?:\s[^<>]*)?)?)?)?)?$/i;
const PARTIAL_CLOSE_TAIL = /<\/?(?:c(?:o(?:p(?:y)?)?)?)?$/i;

interface Range { start: number; end: number }

function renderBlock(before: string, label: string, body: string, closed: boolean, streaming: boolean): string {
  let longest = 0;
  for (const run of body.match(/`+/g) ?? []) longest = Math.max(longest, run.length);
  // Streaming uses a 4-backtick fence so a payload that later shows a ``` line does
  // not force re-fencing text that was already delivered.
  const fence = '`'.repeat(Math.max(streaming ? 4 : 3, longest + 1));
  const lead = before === '' || before.endsWith('\n') ? '' : '\n';
  const title = label ? `**${label}**\n` : '';
  // An open (still streaming) block gets no trailing newline, so later text only appends.
  return closed
    ? `${lead}${title}${fence}\n${body}${body && !body.endsWith('\n') ? '\n' : ''}${fence}`
    : `${lead}${title}${fence}\n${body}`;
}

function tagLabel(tag: string): string {
  const match = LABEL_ATTR.exec(tag);
  if (!match) return '';
  return (match[1] ?? match[2] ?? match[3] ?? '').trim();
}

function inside(index: number, ranges: Range[]): boolean {
  return ranges.some((range) => index >= range.start && index < range.end);
}

function nextOpenTag(text: string, from: number, ranges: Range[]): { start: number; end: number; tag: string } | undefined {
  OPEN_TAG.lastIndex = from;
  for (let match = OPEN_TAG.exec(text); match; match = OPEN_TAG.exec(text)) {
    const start = match.index;
    const range = ranges.find((r) => start >= r.start && start < r.end);
    if (range) {
      OPEN_TAG.lastIndex = Math.max(range.end, start + match[0].length);
      continue;
    }
    return { start, end: start + match[0].length, tag: match[0] };
  }
  return undefined;
}

function fencedRanges(text: string): Range[] {
  const ranges: Range[] = [];
  let fence: { marker: string; count: number; start: number } | undefined;
  let lineStart = 0;
  while (lineStart < text.length) {
    const newline = text.indexOf('\n', lineStart);
    const lineEnd = newline < 0 ? text.length : newline;
    const next = newline < 0 ? text.length : newline + 1;
    const line = text.slice(lineStart, lineEnd).trim();
    const marker = line[0];
    if (marker === '`' || marker === '~') {
      let count = 0;
      while (line[count] === marker) count++;
      const info = line.slice(count).trim();
      if (!fence && count >= 3) fence = { marker, count, start: lineStart };
      else if (fence && marker === fence.marker && count >= fence.count && info === '') {
        ranges.push({ start: fence.start, end: next });
        fence = undefined;
      }
    }
    lineStart = next;
  }
  if (fence) ranges.push({ start: fence.start, end: text.length });
  return ranges;
}
