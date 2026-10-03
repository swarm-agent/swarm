type Requirement = { id: string; text: string; checkpoint_id: string }

/** Render authored outcomes, never execution prompts or serialized plan JSON. */
export function TaskRequirements({ document }: { document: { requirements?: Requirement[]; requirement_changes?: string[]; info?: { goal?: string }; checkpoints?: { id: string; title: string }[] } | null }) {
  const requirements = document?.requirements ?? []
  return <section aria-label="What will change" className="p-3 text-sm font-sans space-y-2 [overflow-wrap:anywhere]">
    <h4 className="font-semibold">What will change</h4>
    {requirements.length ? <ul className="space-y-2">
      {requirements.map(requirement => <li key={requirement.id} data-requirement-id={requirement.id} className="flex gap-2">
        <span aria-hidden="true">□</span><span>{requirement.text}</span>
      </li>)}
    </ul> : <>
      <p>{document?.info?.goal || 'Review the proposed outcomes before starting.'}</p>
      <p className="text-xs text-slate-400">A requirements checklist has not been authored yet. Request a concise summary or open execution details.</p>
    </>}
    {!!document?.requirement_changes?.length && <section aria-label="Changed requirements">
      <h5 className="font-semibold">Changed requirements</h5>
      <ul>{document.requirement_changes.map((change, index) => <li key={index}>{change}</li>)}</ul>
    </section>}
  </section>
}
