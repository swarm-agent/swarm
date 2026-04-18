import {
  chatMentionCandidates,
  mentionHasArgs,
  mentionPaletteActive,
  mentionPaletteQuery,
  normalizeMentionSubagents,
  parseTargetedSubagentPrompt,
} from './subagent-mentions'

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}

function testNormalizesSubagents(): void {
  const actual = normalizeMentionSubagents([' Explorer ', 'memory', 'explorer', '', 'Clone'])
  assert(actual.length === 3, `expected 3 unique names, got ${actual.length}`)
  assert(actual[0] === 'Clone', `expected Clone first, got ${actual[0]}`)
  assert(actual[1] === 'Explorer', `expected Explorer second, got ${actual[1]}`)
  assert(actual[2] === 'memory', `expected memory third, got ${actual[2]}`)
}

function testParsesTargetedPrompt(): void {
  const parsed = parseTargetedSubagentPrompt('@explorer investigate desktop mentions', ['memory', 'Explorer'])
  assert(parsed !== null, 'expected targeted prompt to parse')
  assert(parsed?.targetKind === 'subagent', `expected subagent target kind, got ${parsed?.targetKind}`)
  assert(parsed?.targetName === 'Explorer', `expected canonical subagent name, got ${parsed?.targetName}`)
  assert(parsed?.prompt === 'investigate desktop mentions', `expected stripped task prompt, got ${parsed?.prompt}`)
}

function testRejectsMissingTaskOrUnknownSubagent(): void {
  assert(parseTargetedSubagentPrompt('@explorer', ['explorer']) === null, 'expected mention without task to fail')
  assert(parseTargetedSubagentPrompt('@unknown investigate', ['explorer']) === null, 'expected unknown mention to fail')
  assert(parseTargetedSubagentPrompt('plain prompt', ['explorer']) === null, 'expected plain prompt to bypass mention parsing')
}

function testMentionPaletteHelpers(): void {
  assert(mentionPaletteQuery('@expl') === 'expl', 'expected mention palette query to capture first token')
  assert(mentionPaletteQuery('  @Explorer ') === 'explorer', 'expected mention palette query to normalize case')
  assert(mentionPaletteQuery('hello') === '', 'expected non-mention query to be empty')
  assert(mentionHasArgs('@explorer investigate') === true, 'expected mention with task args')
  assert(mentionHasArgs('@explorer') === false, 'expected bare mention without args')
  assert(mentionPaletteActive('@expl', ['explorer']) === true, 'expected bare mention to activate palette')
  assert(mentionPaletteActive('@explorer investigate', ['explorer']) === false, 'expected mention with args to hide palette')
  const matches = chatMentionCandidates('pl', ['memory', 'Explorer', 'clone'])
  assert(matches.length === 2, `expected two mention matches, got ${matches.length}`)
  assert(matches[0] === 'Explorer', `expected Explorer prefix/contains ordering first, got ${matches[0]}`)
  assert(matches[1] === 'clone', `expected clone second, got ${matches[1]}`)
  const allMatches = chatMentionCandidates('', ['memory', 'Explorer'])
  assert(allMatches.length === 2, `expected all subagents when query empty, got ${allMatches.length}`)
}

function main(): void {
  testNormalizesSubagents()
  testParsesTargetedPrompt()
  testRejectsMissingTaskOrUnknownSubagent()
  testMentionPaletteHelpers()
  console.log('subagent-mentions tests passed')
}

main()
