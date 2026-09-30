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
    <fieldset className="space-y-2 pt-2 border-t border-slate-800/60 text-xs" aria-describedby="image-prompt-help">
      <legend className="text-slate-300">Should AI create different variant prompts?</legend>
      <div className="flex flex-wrap gap-3">
        <label className="flex items-center gap-1.5 text-slate-300">
          <input type="radio" name="image-prompt-mode" checked={!selected} onChange={() => onChange(false)} />
          Same prompt (default)
        </label>
        <label className="flex items-center gap-1.5 text-slate-300">
          <input type="radio" name="image-prompt-mode" checked={selected} disabled={!enabled} onChange={() => onChange(true)} />
          AI-created different prompts
        </label>
      </div>
      <p id="image-prompt-help" className="text-[10px] text-slate-400">
        {enabled
          ? 'Same prompt sends your unchanged prompt independently to the image model. AI-created prompts add a Router prompt-generation step before images are generated.'
          : 'One image sends your prompt directly to the image model, with no Router or Designer session. Select multiple images to enable AI-created prompts.'}
      </p>
    </fieldset>
  )
}
