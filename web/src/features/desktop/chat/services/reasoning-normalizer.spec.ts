import { normalizeReasoningSnapshot } from './reasoning-normalizer'

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message)
}

function testCodexResponsesReasoningSummaryShape(): void {
  const normalized = normalizeReasoningSnapshot('**Planning codebase review**\n\nThe user wants me to search the codebase to see if it is launch-ready.', 'codex')
  assert(normalized.summary === 'Planning codebase review', `unexpected summary: ${normalized.summary}`)
  assert(normalized.text === 'The user wants me to search the codebase to see if it is launch-ready.', `unexpected text: ${normalized.text}`)
  assert(normalized.markdown === '**Planning codebase review**\n\nThe user wants me to search the codebase to see if it is launch-ready.', `unexpected markdown: ${normalized.markdown}`)
}

function testCodexStreamingPartialHeadline(): void {
  const normalized = normalizeReasoningSnapshot('**Planning tool usage**\n\nI need', 'codex')
  assert(normalized.summary === 'Planning tool usage', `unexpected partial summary: ${normalized.summary}`)
  assert(normalized.text === 'I need', `unexpected partial text: ${normalized.text}`)
}

function testFallbackReasoningShape(): void {
  const normalized = normalizeReasoningSnapshot('Thinking: inspect files and then edit. Continue with validation.', 'anthropic')
  assert(normalized.summary === 'Thinking: inspect files and then edit.', `unexpected fallback summary: ${normalized.summary}`)
  assert(normalized.text === 'Thinking: inspect files and then edit. Continue with validation.', `unexpected fallback text: ${normalized.text}`)
}

function main(): void {
  testCodexResponsesReasoningSummaryShape()
  testCodexStreamingPartialHeadline()
  testFallbackReasoningShape()
  console.log('reasoning-normalizer tests passed')
}

main()
