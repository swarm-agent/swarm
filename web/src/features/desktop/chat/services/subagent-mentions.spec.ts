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
  const actual = normalizeMentionSubagents([' Finder ', 'memory', 'finder', '', 'Clone'])
  assert(actual.length === 3, `expected 3 unique names, got ${actual.length}`)
  assert(actual[0] === 'Clone', `expected Clone first, got ${actual[0]}`)
  assert(actual[1] === 'Finder', `expected Finder second, got ${actual[1]}`)
  assert(actual[2] === 'memory', `expected memory third, got ${actual[2]}`)
}

function testParsesTargetedPrompt(): void {
  const parsed = parseTargetedSubagentPrompt('@finder investigate desktop mentions', ['memory', 'Finder'])
  assert(parsed !== null, 'expected targeted prompt to parse')
  assert(parsed?.targetKind === 'subagent', `expected subagent target kind, got ${parsed?.targetKind}`)
  assert(parsed?.targetName === 'Finder', `expected canonical subagent name, got ${parsed?.targetName}`)
  assert(parsed?.prompt === 'investigate desktop mentions', `expected stripped task prompt, got ${parsed?.prompt}`)
}

function testRejectsMissingTaskOrUnknownSubagent(): void {
  assert(parseTargetedSubagentPrompt('@finder', ['finder']) === null, 'expected mention without task to fail')
  assert(parseTargetedSubagentPrompt('@unknown investigate', ['finder']) === null, 'expected unknown mention to fail')
  assert(parseTargetedSubagentPrompt('plain prompt', ['finder']) === null, 'expected plain prompt to bypass mention parsing')
}

function testMentionPaletteHelpers(): void {
  assert(mentionPaletteQuery('@find') === 'find', 'expected mention palette query to capture first token')
  assert(mentionPaletteQuery('  @Finder ') === 'finder', 'expected mention palette query to normalize case')
  assert(mentionPaletteQuery('hello') === '', 'expected non-mention query to be empty')
  assert(mentionHasArgs('@finder investigate') === true, 'expected mention with task args')
  assert(mentionHasArgs('@finder') === false, 'expected bare mention without args')
  assert(mentionPaletteActive('@find', ['finder']) === true, 'expected bare mention to activate palette')
  assert(mentionPaletteActive('@finder investigate', ['finder']) === false, 'expected mention with args to hide palette')
  const matches = chatMentionCandidates('in', ['memory', 'Finder', 'clone'])
  assert(matches.length === 2, `expected two mention matches, got ${matches.length}`)
  assert(matches[0] === 'Finder', `expected Finder prefix/contains ordering first, got ${matches[0]}`)
  assert(matches[1] === 'clone', `expected clone second, got ${matches[1]}`)
  const allMatches = chatMentionCandidates('', ['memory', 'Finder'])
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
