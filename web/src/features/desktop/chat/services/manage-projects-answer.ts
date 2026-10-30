// A manage_projects tool result as the chat reads it. Swarm answers every
// manage_projects call in one envelope: {tool, action, machine, state, data,
// next_action, next, untrusted_content}; the action's own answer is data.
// Messages stored before the envelope hold the answer at the top level, or,
// from a linked machine, {machine, action, data, untrusted_content}; those
// are still read so older conversations keep their cards.

export interface ManageProjectsAnswer {
  /** The action's own answer (task, proposal, task_id, ...). */
  data: Record<string, unknown> | null
  /** The envelope's outcome word (ok, watching, milestone, ...); '' for an older message. */
  state: string
  /** The envelope's machine (always set: this machine's own name or a linked one's); for an older message the linked machine, else ''. */
  machine: string
  nextAction: string
  /** Whether the result is the envelope. In the envelope, machine is always set: whether the call went to a linked machine is in its arguments. */
  envelope: boolean
}

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

function text(value: Record<string, unknown> | null | undefined, key: string): string {
  const v = value?.[key]
  return typeof v === 'string' ? v.trim() : ''
}

/** Whether a result is the catalog envelope. */
export function isManageProjectsEnvelope(out: Record<string, unknown> | null | undefined): boolean {
  return Boolean(out && text(out, 'tool') && text(out, 'action') && text(out, 'machine') && text(out, 'state') && 'data' in out)
}

export function manageProjectsAnswer(out: Record<string, unknown> | null | undefined): ManageProjectsAnswer {
  if (!out) return { data: null, state: '', machine: '', nextAction: '', envelope: false }
  if (isManageProjectsEnvelope(out)) {
    return { data: record(out.data), state: text(out, 'state'), machine: text(out, 'machine'), nextAction: text(out, 'next_action'), envelope: true }
  }
  // Older message: a linked machine's wrapped answer, or the answer itself.
  const machine = text(out, 'machine')
  if (machine && record(out.data)) {
    return { data: record(out.data), state: '', machine, nextAction: '', envelope: false }
  }
  return { data: out, state: '', machine, nextAction: text(out, 'next_action'), envelope: false }
}

/**
 * A catalog tool's own answer (manage_sessions and the other catalog tools
 * answer in the same envelope): the envelope's data, or, for a message
 * stored before the envelope, the answer as it was.
 */
export function catalogAnswerData(out: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
  if (!out) return null
  return isManageProjectsEnvelope(out) ? record(out.data) : out
}

/** Whether the call ran on a linked machine: its machine argument, or an older message's wrapper. */
export function manageProjectsLinked(args: Record<string, unknown> | null | undefined, out: Record<string, unknown> | null | undefined): boolean {
  if (text(args, 'machine')) return true
  const answer = manageProjectsAnswer(out)
  return !answer.envelope && Boolean(answer.machine)
}
