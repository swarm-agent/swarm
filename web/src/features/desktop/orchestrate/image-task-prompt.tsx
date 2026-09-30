import { MediaTaskHelp } from './media-task-controls'

export type ImagePromptState = { count: number; aiVariants: boolean }
export type ImagePromptAction =
  | { type: 'count'; count: number }
  | { type: 'choice'; aiVariants: boolean }
  | { type: 'reset' }

export const initialImagePromptState: ImagePromptState = { count: 1, aiVariants: false }

// Consent is never inferred from count, and cannot survive a single-image or task reset.
export function imagePromptReducer(state: ImagePromptState, action: ImagePromptAction): ImagePromptState {
  if (action.type === 'reset') return { ...state, aiVariants: false }
  if (action.type === 'count') return { count: action.count, aiVariants: action.count > 1 && state.aiVariants }
  return { ...state, aiVariants: state.count > 1 && action.aiVariants }
}

export function imagePromptEnhancement(count: number, aiVariants: boolean): boolean {
  return count > 1 && aiVariants
}

export function imageExecutionLabel(count: number, aiVariants: boolean): string {
  return imagePromptEnhancement(count, aiVariants) ? 'Router → image model' : 'Direct image generation'
}

export function ImagePromptControls({ count, aiVariants, onChange }: {
  count: number
  aiVariants: boolean
  onChange: (aiVariants: boolean) => void
}) {
  const enabled = count > 1
  const selected = imagePromptEnhancement(count, aiVariants)
  return (
    <fieldset className="text-xs">
      <legend className="sr-only">Image prompt mode</legend>
      <div className="flex flex-wrap items-start gap-2">
        <label className="flex min-h-9 items-center gap-1.5 rounded border border-slate-700 px-2 text-sm text-slate-300 cursor-pointer">
          <input type="radio" name="image-prompt-mode" checked={!selected} onChange={() => onChange(false)} />
          Same prompt
        </label>
        <label className="flex min-h-9 items-center gap-1.5 rounded border border-slate-700 px-2 text-sm text-slate-300 cursor-pointer">
          <input type="radio" name="image-prompt-mode" checked={selected} disabled={!enabled} onChange={() => onChange(true)} />
          AI variants
        </label>
        <MediaTaskHelp label="About AI variants">
          {enabled
            ? 'Same prompt sends your unchanged prompt independently to the image model. AI variants add a Router prompt-generation step. Only enabled by your explicit choice.'
            : 'One image goes directly to the image model. Select multiple images to enable AI variants.'}
        </MediaTaskHelp>
      </div>
    </fieldset>
  )
}
