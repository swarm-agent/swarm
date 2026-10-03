import { ChatMarkdown } from '../chat/components/chat-markdown'

// Display projection only. Never feed normalized legacy content back into approval
// validation, and never enumerate arbitrary wire keys (prompts/routing are private).
type RecordValue = Record<string, unknown>
type Detail = { label: string; values: string[]; checklist?: boolean }
type Step = { title: string; details: Detail[]; steps: Step[] }
type PlanPresentation = { title: string; details: Detail[]; steps: Step[] }
const record = (value: unknown): RecordValue | null => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as RecordValue : null
const text = (value: unknown): string => typeof value === 'string' ? value.trim() : ''
const rows = (value: unknown): unknown[] => Array.isArray(value) ? value : []
const first = (value: RecordValue, ...keys: string[]) => keys.map(key => value[key]).find(item => typeof item === 'string' ? !!item.trim() : Array.isArray(item) ? item.length > 0 : item != null)
const label = (value: unknown): string => {
  if (typeof value === 'string') return text(value)
  const item = record(value)
  return item ? text(first(item, 'text', 'title', 'criterion', 'description')) : ''
}
const values = (value: unknown): string[] => (Array.isArray(value) ? value : [value]).map(label).filter(Boolean)
const detail = (label: string, value: unknown, checklist = false): Detail => ({ label, values: values(value), checklist })
const present = (details: Detail[]) => details.filter(item => item.values.length > 0)

/** Persisted documents may be objects, JSON strings, or document envelopes. */
export function taskPlanDocument(value: unknown, depth = 0): RecordValue | null {
  if (depth > 6) return null
  if (typeof value === 'string') {
    const source = value.trim().replace(/^```(?:json)?\s*\n([\s\S]*?)\n```$/i, '$1')
    try { return taskPlanDocument(JSON.parse(source), depth + 1) } catch { return null }
  }
  const object = record(value)
  if (!object) return null
  if (object.document != null) return taskPlanDocument(object.document, depth + 1)
  if (object.plan_document != null) return taskPlanDocument(object.plan_document, depth + 1)
  return object
}

function taskDetails(value: RecordValue): Detail[] {
  const tasks = rows(first(value, 'tasks', 'subtasks'))
  return present([
    detail('Objective', first(value, 'objective', 'goal')),
    detail('Description', value.description),
    detail('Tasks', tasks, true),
    ...tasks.flatMap(item => {
      const task = record(item)
      return task ? [detail(`${label(task) || 'Task'} · Notes`, task.notes), detail(`${label(task) || 'Task'} · Details`, task.description !== label(task) ? task.description : undefined)] : []
    }),
    detail('Acceptance criteria', first(value, 'acceptance_criteria', 'acceptanceCriteria', 'criteria'), true),
    detail('Notes', value.notes),
    detail('Relevant files', first(value, 'relevant_files', 'relevantFiles')),
    detail('Validation', first(value, 'validation_strategy', 'validationStrategy', 'validation')),
  ])
}

function dependencies(value: RecordValue, peers: RecordValue[]): Detail[] {
  const names = values(first(value, 'depends_on', 'dependsOn')).map(id => text(peers.find(peer => peer.id === id)?.title) || id)
  return present([detail('Depends on', names), detail('Dependency evidence', first(value, 'dependency_evidence', 'dependencyEvidence'))])
}

function programSteps(source: unknown): Step[] {
  const program = taskPlanDocument(source)
  if (!program) return []
  const stages = rows(program.stages).map(record).filter((row): row is RecordValue => !!row)
  const jobs = rows(program.jobs).map(record).filter((row): row is RecordValue => !!row)
  const jobStep = (job: RecordValue, index: number): Step => ({
    title: text(job.title) || `Assignment ${index + 1}`,
    details: [...taskDetails(job), ...dependencies(job, jobs), ...present([
      detail('Deliverable', job.deliverable),
      detail('Scope', first(job, 'owned_scope', 'ownedScope')),
    ])], steps: [],
  })
  return [
    ...stages.map((stage, index): Step => ({
      title: text(stage.title) || text(stage.id) || `Stage ${index + 1}`,
      details: [...taskDetails(stage), ...dependencies(stage, stages)],
      steps: jobs.filter(job => first(job, 'stage_id', 'stageId') === stage.id).map(jobStep),
    })),
    // Malformed/legacy stage bindings must not silently discard substantive jobs.
    ...jobs.filter(job => !stages.some(stage => stage.id === first(job, 'stage_id', 'stageId'))).map(jobStep),
  ]
}

export function taskPlanPresentation(source: unknown, program?: unknown, checkpoints?: unknown): PlanPresentation {
  const doc = taskPlanDocument(source) || {}
  const info = record(doc.info) || {}
  const cps = rows(first(doc, 'checkpoints') || checkpoints).map(record).filter((row): row is RecordValue => !!row)
  const ordered = cps.map((cp, index) => ({ cp, order: typeof cp.order === 'number' ? cp.order : index + 1 })).sort((a, b) => a.order - b.order)
  return {
    title: text(doc.title),
    details: present([
      detail('Goal', first(info, 'goal') || first(doc, 'goal', 'objective')),
      detail('Scope', info.scope), detail('Context', info.context),
      detail('Decisions', info.decisions), detail('Constraints', info.constraints),
      detail('Assumptions', info.assumptions), detail('Open questions', first(info, 'open_questions', 'openQuestions')),
      detail('Relevant files', first(info, 'relevant_files', 'relevantFiles')),
      detail('Success criteria', first(info, 'success_criteria', 'successCriteria'), true),
      detail('Validation strategy', first(info, 'validation_strategy', 'validationStrategy')),
      detail('Notes', info.notes), detail('Requirements', doc.requirements, true),
      detail('Changed requirements', first(doc, 'requirement_changes', 'requirementChanges')),
      ...taskDetails(doc),
    ]),
    steps: [
      ...ordered.map(({ cp }, index): Step => ({
        title: text(cp.title) || `Checkpoint ${index + 1}`,
        details: [...taskDetails(cp), ...dependencies(cp, cps)],
        steps: programSteps(first(cp, 'task_program', 'taskProgram')),
      })),
      ...programSteps(program || first(doc, 'task_program', 'taskProgram') || (doc.stages || doc.jobs ? doc : null)),
    ],
  }
}

function Details({ details }: { details: Detail[] }) {
  return details.map((item, index) => <section key={index} className="min-w-0 space-y-1">
    <h5 className="font-semibold">{item.label}</h5>
    <ul className="space-y-2">{item.values.map((value, i) => <li key={i} className="min-w-0 flex items-start gap-2">
      {item.checklist && <span aria-hidden="true" className="shrink-0">□</span>}
      <div className="min-w-0 flex-1"><PlanSource source={value} /></div>
    </li>)}</ul>
  </section>)
}
function Steps({ steps }: { steps: Step[] }) {
  return steps.length ? <ol className="min-w-0 space-y-4">{steps.map((step, index) => <li key={index} className="min-w-0 space-y-3 border-l border-slate-700 pl-3">
    <h4 className="font-semibold whitespace-pre-wrap">{index + 1}. {step.title}</h4>
    <Details details={step.details} />
    <Steps steps={step.steps} />
    {!step.details.length && !step.steps.length && <p role="status">No tasks or acceptance criteria are available for this step.</p>}
  </li>)}</ol> : null
}
function Presentation({ plan }: { plan: PlanPresentation }) {
  return <>
    {plan.title && <h4 className="font-semibold whitespace-pre-wrap">{plan.title}</h4>}
    <Details details={plan.details} /><Steps steps={plan.steps} />
  </>
}
const hasContent = (plan: PlanPresentation): boolean => plan.details.length > 0 || plan.steps.some(step => step.details.length > 0 || step.steps.length > 0)
const unavailable = <p role="status">Readable plan details are unavailable. Request a complete human-readable plan.</p>

/** Prose uses the existing safe Markdown renderer. Legacy serialized sources are
 * projected by named fields, never printed as code or used as an approval grant. */
function PlanSource({ source }: { source: string }) {
  const document = taskPlanDocument(source)
  if (document) {
    const plan = taskPlanPresentation(document)
    return hasContent(plan) ? <Presentation plan={plan} /> : unavailable
  }
  if (/^\s*(?:[\[{]|```(?:json)\b)/i.test(source)) return unavailable
  // A prose document can contain a serialized definition. Project those blocks
  // too, while leaving ordinary technical code samples to the Markdown renderer.
  const segments = source.split(/(```(?:json)?\s*\n\s*[{\[][\s\S]*?\n```)/gi)
  if (segments.length > 1) return <>{segments.filter(Boolean).map((part, i) => /^```/.test(part)
    ? <PlanSource key={i} source={part} /> : <ChatMarkdown key={i} content={part} />)}</>
  return <ChatMarkdown content={source} className="text-sm leading-6" />
}

export function TaskPlanDetails({ document, program, checkpoints, markdown, description }: {
  document?: unknown; program?: unknown; checkpoints?: unknown; markdown?: string; description?: string
}) {
  const plan = taskPlanPresentation(document, program, checkpoints)
  const structured = hasContent(plan)
  const authoredDocument = hasContent(taskPlanPresentation(document, undefined, checkpoints))
  const doc = taskPlanDocument(document)
  const prose = markdown?.trim() || (doc ? text(first(doc, 'plan', 'display_text', 'displayText', 'rendered_text', 'renderedText')) : typeof document === 'string' ? document.trim() : '')
  return <section aria-label="Proposed plan" className="task-plan-readable min-w-0 max-w-full space-y-4 p-3 text-sm font-sans leading-6 [overflow-wrap:anywhere] [&_pre]:whitespace-pre-wrap [&_pre]:max-w-full [&_code]:whitespace-pre-wrap [&_table]:table-fixed [&_table]:w-full">
    {description?.trim() && <section aria-label="Proposed changes"><h4 className="font-semibold">Proposed changes</h4><PlanSource source={description} /></section>}
    {structured && <Presentation plan={plan} />}
    {!authoredDocument && prose && <PlanSource source={prose} />}
    {!structured && !prose && !description?.trim() && unavailable}
  </section>
}
