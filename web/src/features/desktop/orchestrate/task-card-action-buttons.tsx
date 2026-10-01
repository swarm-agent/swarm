import { Archive, MessageCircle } from 'lucide-react'

export function TaskCardActionButtons({ onArchiveTask, onAskOrchestrator }: {
  onArchiveTask?: () => void
  onAskOrchestrator?: () => void
}) {
  const buttonClass = 'inline-flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-md border border-slate-600 bg-slate-800 text-slate-200 hover:bg-slate-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-400'
  return <>
    {onArchiveTask && <button type="button" className={buttonClass} aria-label="Archive task" title="Archive task"
      onClick={event => { event.stopPropagation(); onArchiveTask() }}><Archive size={15.6} aria-hidden="true" /></button>}
    {onAskOrchestrator && <button type="button" className={buttonClass} aria-label="Ask orchestrator" title="Ask orchestrator"
      onClick={event => { event.stopPropagation(); onAskOrchestrator() }}><MessageCircle size={15.6} aria-hidden="true" /></button>}
  </>
}
