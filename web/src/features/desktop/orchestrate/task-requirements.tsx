type Requirement = { id: string; text: string; checkpoint_id: string }
type ReviewDocument = {
  title?: string
  info?: { goal?: string }
  requirements?: Requirement[]
  requirement_changes?: string[]
  checkpoints?: { id: string; title: string; tasks?: string[]; acceptance_criteria?: string[] }[]
}

const nonblank = (value: unknown): value is string => typeof value === 'string' && value.trim().length > 0
const meaningfulCriterion = (value: unknown): value is string => nonblank(value) &&
  !/^(?:deliverable ready|branch clean|worktree clean)[.!]?$/i.test(value.trim())

/** Fail closed on missing, loading, legacy, or malformed review content. */
export function isTaskPlanReviewable(document: ReviewDocument | null): boolean {
  if (!document || !nonblank(document.title) || !nonblank(document.info?.goal)) return false
  const { requirements, checkpoints } = document
  if (!Array.isArray(requirements) || !requirements.length || !Array.isArray(checkpoints) || !checkpoints.length) return false
  if (!checkpoints.every(cp => cp && nonblank(cp.id) && nonblank(cp.title) && Array.isArray(cp.tasks) && cp.tasks.length > 0 && cp.tasks.every(nonblank) && Array.isArray(cp.acceptance_criteria) && cp.acceptance_criteria.length > 0 && cp.acceptance_criteria.every(nonblank))) return false
  const ids = new Set<string>()
  const criteria = new Set<string>()
  return requirements.every(r => {
    if (!r || !nonblank(r.id) || !nonblank(r.text) || !nonblank(r.checkpoint_id) || ids.has(r.id)) return false
    const key = `${r.checkpoint_id}\u0000${r.text}`
    if (criteria.has(key)) return false
    ids.add(r.id)
    criteria.add(key)
    return checkpoints.filter(cp => cp.id === r.checkpoint_id).flatMap(cp => cp.acceptance_criteria ?? []).filter(text => text === r.text).length === 1
  })
}

/** Presentation only: legacy criteria never grant approval or become editable state. */
export function TaskPlanChecklist({ document }: { document: ReviewDocument | null }) {
  const checkpoints = Array.isArray(document?.checkpoints) ? document.checkpoints : []
  const requirements = document?.requirements
  // Authored requirements are authoritative. Do not replace invalid bindings with
  // unrelated delivery gates or a task-description summary.
  const authored = Array.isArray(requirements) && requirements.length > 0
  const entries = authored
    ? requirements.filter(r => r && nonblank(r.text) && checkpoints.some(cp =>
      cp?.id === r.checkpoint_id && Array.isArray(cp.acceptance_criteria) && cp.acceptance_criteria.includes(r.text)))
      .map(r => r.text)
    : checkpoints.flatMap(cp => Array.isArray(cp?.acceptance_criteria) ? cp.acceptance_criteria.filter(meaningfulCriterion) : [])
  return <section aria-label="Plan checklist" className="p-3 text-sm font-sans space-y-2 min-w-0 [overflow-wrap:anywhere]">
    <h4 className="font-semibold">What will change</h4>
    {entries.length > 0 ? <ul className="space-y-2">
      {entries.map((text, index) => <li key={index} className="flex items-start gap-2">
        <span aria-hidden="true" className="shrink-0">□</span><span className="min-w-0 whitespace-pre-wrap">{text}</span>
      </li>)}
    </ul> : <p>No bound requirements or acceptance criteria are available yet.</p>}
    {authored && entries.length !== requirements.length && <p role="alert">Some requirements are not bound to the current plan. Request a corrected plan before approval.</p>}
  </section>
}

/** The checklist and actual structured plan share the exact persisted document. */
export function TaskRequirements({ document }: { document: ReviewDocument | null }) {
  if (!isTaskPlanReviewable(document)) return <p role="alert" className="p-3 text-sm">Plan review unavailable or invalid. A complete plan and bound What will change checklist are required before approval. Reload or request a corrected plan.</p>
  return <section aria-label="Plan review" className="p-3 text-sm font-sans space-y-2 [overflow-wrap:anywhere]">
    <h4 className="font-semibold">What will change</h4>
    <ul className="space-y-2">
      {document!.requirements!.map(requirement => <li key={requirement.id} data-requirement-id={requirement.id} className="flex gap-2">
        <span aria-hidden="true">□</span><span>{requirement.text}</span>
      </li>)}
    </ul>
    <section aria-label="Proposed plan">
      <h4 className="font-semibold">{document!.title}</h4>
      <p>{document!.info!.goal}</p>
      {document!.checkpoints!.map(cp => <section key={cp.id}>
        <h5 className="font-semibold">{cp.title}</h5>
        <ul>{cp.tasks!.map((text, i) => <li key={i}>{text}</li>)}</ul>
        <h6>Acceptance criteria</h6>
        <ul>{cp.acceptance_criteria!.map((text, i) => <li key={i}>{text}</li>)}</ul>
      </section>)}
    </section>
    {!!document?.requirement_changes?.length && <section aria-label="Changed requirements">
      <h5 className="font-semibold">Changed requirements</h5>
      <ul>{document.requirement_changes.map((change, index) => <li key={index}>{change}</li>)}</ul>
    </section>}
  </section>
}
