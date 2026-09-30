import type { RunningTask } from './orchestrate-types'

const fallback = 'Review the task to see what changed.'
const technical = /`|\$|https?:\/\/|(?:^|\s)[/~][\w.]|\b[\w.-]+\.(?:tsx?|jsx?|go|json|md|sh|yaml|yml)\b|\b[0-9a-f]{7,40}\b|\b(?:commit|branch|worktree|workspace|command|npm|pnpm|git|bash|stdout|stderr|handoff|agent|implementation recap)\b/i
const validation = /\b(?:tests?|checks?|validation|build|tested|integrat\w*|passed|verified)\b/i
const outcome = /^(?:added|fixed|updated|removed|replaced|improved|corrected|enabled|disabled|simplified|preserved|prevented|changed|restored|implemented)\b|\b(?:now|no longer)\b|\b(?:was|were|has been|have been) (?:fixed|updated|removed|added|replaced|corrected|simplified)\b/i
const narrative = /^(?:fix|add|update|remove|replace|improve|enable|disable|implement|simplify)\b|\b(?:I|we|my|our)\b|\b(?:inspected|searched|read|reviewed|investigated|plan to|will|should|please|must|need to|next|todo|not yet|not done|incomplete|failed)\b/i

/** Presentation only: the canonical handoff is attempt-bound upstream. Never turn
 * task titles/requests or unbound historical outcome fields into completion facts.
 * Reject entire unsuitable statements, rather than clipping an agent recap.
 */
export function taskReviewMessage(task: Pick<RunningTask, 'handoffSummary' | 'whatDidDo' | 'whatNotDone'>) {
  const raw = typeof task.handoffSummary === 'string' ? task.handoffSummary : ''
  const outcomes: string[] = []
  let fenced = false
  for (const line of raw.split(/\r?\n/)) {
    if (/^\s*(```|~~~)/.test(line)) { fenced = !fenced; continue }
    if (fenced || /^\s*(?:#|>|\||\{)/.test(line)) continue
    const cleaned = line.replace(/^\s*(?:[-*+]\s+|\d+[.)]\s+)/, '').replace(/\*\*|__/g, '').trim()
    for (const sentence of cleaned.split(/(?<=[.!?])\s+/)) {
      if (sentence.length < 12 || sentence.length > 200 || technical.test(sentence) || validation.test(sentence)
        || narrative.test(sentence) || !outcome.test(sentence)) continue
      if (!outcomes.includes(sentence) && outcomes.length < 2
        && [...outcomes, sentence].join(' ').length <= 240) outcomes.push(sentence)
    }
  }

  // Warnings are intentionally conservative; they never promote agent claims of
  // successful validation/integration over the separate authoritative Git facts.
  const evidence = [raw, ...(Array.isArray(task.whatDidDo) ? task.whatDidDo : []),
    ...(Array.isArray(task.whatNotDone) ? task.whatNotDone : [])].filter(value => typeof value === 'string').join('\n')
  const warnings: string[] = []
  if (/\b(?:tests?|checks?|validation|build)\b[^\n.!?]{0,100}\b(?:not run|not executed|not tested|not performed|pending|skipped|unverified|required)\b|\b(?:not run|not executed|did not run|haven't run|unable to run|could not run)\b[^\n.!?]{0,80}\b(?:tests?|checks?|validation|build)\b/i.test(evidence)) {
    warnings.push('Validation still needs to be run.')
  }
  if (/\b(?:tests?|checks?|validation|build)\b[^\n.!?]{0,80}\b(?:failed|failing|failure)\b|\b(?:failed|failing)\b[^\n.!?]{0,80}\b(?:tests?|checks?|validation|build)\b/i.test(evidence)) {
    warnings.push('Some checks failed; review the details.')
  }
  if (/\b(?:incomplete|unfinished|not implemented|not completed|not done|still pending|remaining work|partially implemented)\b/i.test(evidence)
    || (Array.isArray(task.whatNotDone) && task.whatNotDone.some(value => typeof value === 'string' && value.trim()))) {
    warnings.push('Some work remains; review the details.')
  }
  if (/\b(?:integration|integrated|promotion|promoted|landed)\b[^\n.!?]{0,80}\b(?:not|unverified|pending|required)\b|\b(?:not integrated|not promoted|not landed|integration not verified)\b/i.test(evidence)) {
    warnings.push('Integration has not been verified.')
  }
  return { outcome: outcomes.slice(0, 2).join(' ') || fallback, hasOutcome: outcomes.length > 0, warnings, raw }
}
