export function TaskCardActionButtons({ onArchiveTask, onAskOrchestrator }: {
  onArchiveTask?: () => void
  onAskOrchestrator?: () => void
}) {
  const buttonClass = 'shrink-0 rounded-md border border-slate-600 bg-slate-800 px-2 py-1 text-[11px] text-slate-200 hover:bg-slate-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-sky-400'
  return <>
    {onArchiveTask && <button type="button" className={buttonClass}
      onClick={event => { event.stopPropagation(); onArchiveTask() }}>Archive task</button>}
    {onAskOrchestrator && <button type="button" className={buttonClass}
      onClick={event => { event.stopPropagation(); onAskOrchestrator() }}>Ask orchestrator</button>}
  </>
}
