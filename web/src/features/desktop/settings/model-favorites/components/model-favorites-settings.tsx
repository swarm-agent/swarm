import { useState, type FormEvent } from 'react'
import { ArrowDown, ArrowUp, Check, Pencil, Plus, Star, Trash2, X } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Input } from '../../../../../components/ui/input'
import { Select } from '../../../../../components/ui/select'

export interface FlatModelFavorite {
  id: string
  name: string
  provider: string
  model: string
  thinking: string
  serviceTier: string
  contextMode: string
  isDefault: boolean
}

export interface FlatModelOption {
  provider: string
  model: string
  label?: string
  thinkingOptions?: string[]
  serviceTierOptions?: string[]
  contextModeOptions?: string[]
}

export type FlatModelFavoriteInput = Omit<FlatModelFavorite, 'id' | 'isDefault'>

export interface ModelFavoritesSettingsProps {
  favorites: FlatModelFavorite[]
  modelOptions: FlatModelOption[]
  busy?: boolean
  busyFavoriteId?: string | null
  error?: string | null
  onCreate: (favorite: FlatModelFavoriteInput) => void | Promise<void>
  onUpdate: (id: string, favorite: FlatModelFavoriteInput) => void | Promise<void>
  onDelete: (id: string) => void | Promise<void>
  onReorder: (orderedFavoriteIds: string[]) => void | Promise<void>
  onSetDefault: (id: string) => void | Promise<void>
}

type Editor = { kind: 'create' } | { kind: 'edit'; id: string }

const EMPTY_FAVORITE: FlatModelFavoriteInput = {
  name: '',
  provider: '',
  model: '',
  thinking: '',
  serviceTier: '',
  contextMode: '',
}

function optionKey(provider: string, model: string): string {
  return `${encodeURIComponent(provider)}:${encodeURIComponent(model)}`
}

export function validateFlatModelFavorite(input: FlatModelFavoriteInput): string[] {
  const errors: string[] = []
  if (!input.name.trim()) errors.push('Enter a favorite name.')
  if (!input.provider.trim() || !input.model.trim()) errors.push('Choose a model.')
  return errors
}

export function moveFavoriteIds(favorites: FlatModelFavorite[], id: string, direction: -1 | 1): string[] {
  const ids = favorites.map((favorite) => favorite.id)
  const index = ids.indexOf(id)
  const target = index + direction
  if (index < 0 || target < 0 || target >= ids.length) return ids
  ;[ids[index], ids[target]] = [ids[target], ids[index]]
  return ids
}

function favoriteToInput(favorite: FlatModelFavorite): FlatModelFavoriteInput {
  return {
    name: favorite.name,
    provider: favorite.provider,
    model: favorite.model,
    thinking: favorite.thinking,
    serviceTier: favorite.serviceTier,
    contextMode: favorite.contextMode,
  }
}

function uniqueOptions(values: string[] | undefined, current: string): string[] {
  return Array.from(new Set([...(values ?? []), current].map((value) => value.trim()).filter(Boolean)))
}

interface FavoriteEditorProps {
  draft: FlatModelFavoriteInput
  modelOptions: FlatModelOption[]
  submitting: boolean
  submitLabel: string
  onChange: (draft: FlatModelFavoriteInput) => void
  onCancel: () => void
  onSubmit: () => void
}

function FavoriteEditor({ draft, modelOptions, submitting, submitLabel, onChange, onCancel, onSubmit }: FavoriteEditorProps) {
  const selectedOption = modelOptions.find((option) => option.provider === draft.provider && option.model === draft.model) ?? null
  const validationErrors = validateFlatModelFavorite(draft)
  const fieldClass = 'grid gap-1.5 text-xs font-medium text-[var(--app-text-muted)]'

  const update = (patch: Partial<FlatModelFavoriteInput>) => onChange({ ...draft, ...patch })

  return (
    <form
      className="grid gap-4 rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] p-4"
      onSubmit={(event: FormEvent) => {
        event.preventDefault()
        if (validationErrors.length === 0) onSubmit()
      }}
    >
      <div className="grid gap-4 md:grid-cols-2">
        <label className={fieldClass}>
          Favorite name
          <Input aria-label="Favorite name" value={draft.name} onChange={(event) => update({ name: event.target.value })} disabled={submitting} autoFocus />
        </label>
        <label className={fieldClass}>
          Model
          <Select
            aria-label="Favorite model"
            value={optionKey(draft.provider, draft.model)}
            onChange={(event) => {
              const option = modelOptions.find((candidate) => optionKey(candidate.provider, candidate.model) === event.target.value)
              update({
                provider: option?.provider ?? '',
                model: option?.model ?? '',
                thinking: option?.thinkingOptions?.[0] ?? '',
                serviceTier: option?.serviceTierOptions?.[0] ?? '',
                contextMode: option?.contextModeOptions?.[0] ?? '',
              })
            }}
            disabled={submitting}
          >
            <option value={optionKey('', '')}>Choose a model</option>
            {modelOptions.map((option) => (
              <option key={optionKey(option.provider, option.model)} value={optionKey(option.provider, option.model)}>
                {option.label?.trim() || `${option.provider} / ${option.model}`}
              </option>
            ))}
          </Select>
        </label>
        <label className={fieldClass}>
          Thinking
          <Select aria-label="Favorite thinking" value={draft.thinking} onChange={(event) => update({ thinking: event.target.value })} disabled={submitting || !selectedOption}>
            <option value="">Provider default</option>
            {uniqueOptions(selectedOption?.thinkingOptions, draft.thinking).map((value) => <option key={value} value={value}>{value}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Service tier
          <Select aria-label="Favorite service tier" value={draft.serviceTier} onChange={(event) => update({ serviceTier: event.target.value })} disabled={submitting || !selectedOption}>
            <option value="">Provider default</option>
            {uniqueOptions(selectedOption?.serviceTierOptions, draft.serviceTier).map((value) => <option key={value} value={value}>{value}</option>)}
          </Select>
        </label>
        <label className={fieldClass}>
          Context mode
          <Select aria-label="Favorite context mode" value={draft.contextMode} onChange={(event) => update({ contextMode: event.target.value })} disabled={submitting || !selectedOption}>
            <option value="">Provider default</option>
            {uniqueOptions(selectedOption?.contextModeOptions, draft.contextMode).map((value) => <option key={value} value={value}>{value}</option>)}
          </Select>
        </label>
      </div>

      {validationErrors.length > 0 ? (
        <div role="alert" className="text-sm text-[var(--app-danger)]">{validationErrors.join(' ')}</div>
      ) : null}

      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" onClick={onCancel} disabled={submitting}><X size={15} />Cancel</Button>
        <Button type="submit" variant="primary" disabled={submitting || validationErrors.length > 0}><Check size={15} />{submitLabel}</Button>
      </div>
    </form>
  )
}

export function ModelFavoritesSettings({
  favorites,
  modelOptions,
  busy = false,
  busyFavoriteId = null,
  error = null,
  onCreate,
  onUpdate,
  onDelete,
  onReorder,
  onSetDefault,
}: ModelFavoritesSettingsProps) {
  const [editor, setEditor] = useState<Editor | null>(null)
  const [draft, setDraft] = useState<FlatModelFavoriteInput>(EMPTY_FAVORITE)
  const [localError, setLocalError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [deleteCandidate, setDeleteCandidate] = useState<string | null>(null)

  const editingFavoriteId = editor?.kind === 'edit' ? editor.id : null
  const locked = busy || submitting

  const run = async (operation: () => void | Promise<void>, closeEditor = false) => {
    setSubmitting(true)
    setLocalError(null)
    try {
      await operation()
      if (closeEditor) setEditor(null)
    } catch (cause) {
      setLocalError(cause instanceof Error ? cause.message : 'The favorite could not be saved.')
    } finally {
      setSubmitting(false)
    }
  }

  const beginCreate = () => {
    setDraft(EMPTY_FAVORITE)
    setLocalError(null)
    setEditor({ kind: 'create' })
  }

  const beginEdit = (favorite: FlatModelFavorite) => {
    setDraft(favoriteToInput(favorite))
    setLocalError(null)
    setEditor({ kind: 'edit', id: favorite.id })
  }

  return (
    <section aria-labelledby="model-favorites-heading" className="grid gap-5">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h2 id="model-favorites-heading" className="text-xl font-semibold text-[var(--app-text)]">Model favorites</h2>
          <p className="mt-1 max-w-2xl text-sm text-[var(--app-text-muted)]">Save flat model choices for fast reuse. Each favorite contains one provider and model with its own thinking, service tier, and context mode.</p>
        </div>
        <Button variant="primary" onClick={beginCreate} disabled={locked || editor?.kind === 'create'}><Plus size={16} />Add favorite</Button>
      </header>

      {error || localError ? <div role="alert" className="rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] px-3 py-2 text-sm text-[var(--app-danger)]">{localError || error}</div> : null}

      {editor?.kind === 'create' ? (
        <FavoriteEditor draft={draft} modelOptions={modelOptions} submitting={locked} submitLabel="Create favorite" onChange={setDraft} onCancel={() => setEditor(null)} onSubmit={() => void run(() => onCreate({ ...draft, name: draft.name.trim() }), true)} />
      ) : null}

      {favorites.length === 0 && editor?.kind !== 'create' ? (
        <div className="rounded-xl border border-dashed border-[var(--app-border-strong)] px-5 py-8 text-center text-sm text-[var(--app-text-muted)]">No favorites yet. Add a model favorite to get started.</div>
      ) : null}

      <ol aria-label="Model favorites order" className="grid gap-3">
        {favorites.map((favorite, index) => {
          const itemBusy = locked || busyFavoriteId === favorite.id
          const isEditing = editingFavoriteId === favorite.id
          const confirmingDelete = deleteCandidate === favorite.id
          return (
            <li key={favorite.id} className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-4">
              {isEditing ? (
                <FavoriteEditor draft={draft} modelOptions={modelOptions} submitting={itemBusy} submitLabel="Save favorite" onChange={setDraft} onCancel={() => setEditor(null)} onSubmit={() => void run(() => onUpdate(favorite.id, { ...draft, name: draft.name.trim() }), true)} />
              ) : (
                <div className="grid gap-4">
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-semibold text-[var(--app-text)]">{favorite.name}</span>
                        {favorite.isDefault ? <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-primary)] px-2 py-0.5 text-xs font-medium text-[var(--app-primary-text)]"><Star size={12} fill="currentColor" />Default</span> : null}
                      </div>
                      <div className="mt-1 truncate text-sm text-[var(--app-text-muted)]">{favorite.provider} / {favorite.model}</div>
                      <dl className="mt-3 grid gap-x-5 gap-y-2 text-xs sm:grid-cols-3">
                        <div><dt className="text-[var(--app-text-subtle)]">Thinking</dt><dd className="mt-0.5 text-[var(--app-text)]">{favorite.thinking || 'Provider default'}</dd></div>
                        <div><dt className="text-[var(--app-text-subtle)]">Service tier</dt><dd className="mt-0.5 text-[var(--app-text)]">{favorite.serviceTier || 'Provider default'}</dd></div>
                        <div><dt className="text-[var(--app-text-subtle)]">Context mode</dt><dd className="mt-0.5 text-[var(--app-text)]">{favorite.contextMode || 'Provider default'}</dd></div>
                      </dl>
                    </div>
                    <div className="flex flex-wrap gap-1">
                      <Button size="sm" variant="ghost" aria-label={`Move ${favorite.name} up`} onClick={() => void run(() => onReorder(moveFavoriteIds(favorites, favorite.id, -1)))} disabled={itemBusy || index === 0}><ArrowUp size={15} /></Button>
                      <Button size="sm" variant="ghost" aria-label={`Move ${favorite.name} down`} onClick={() => void run(() => onReorder(moveFavoriteIds(favorites, favorite.id, 1)))} disabled={itemBusy || index === favorites.length - 1}><ArrowDown size={15} /></Button>
                      {!favorite.isDefault ? <Button size="sm" variant="ghost" onClick={() => void run(() => onSetDefault(favorite.id))} disabled={itemBusy}><Star size={15} />Set default</Button> : null}
                      <Button size="sm" variant="ghost" onClick={() => beginEdit(favorite)} disabled={itemBusy}><Pencil size={15} />Edit</Button>
                      <Button size="sm" variant="ghost" onClick={() => setDeleteCandidate(favorite.id)} disabled={itemBusy}><Trash2 size={15} />Delete</Button>
                    </div>
                  </div>
                  {confirmingDelete ? (
                    <div role="group" aria-label={`Confirm deletion of ${favorite.name}`} className="flex flex-wrap items-center justify-end gap-2 rounded-xl border border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] p-3 text-sm text-[var(--app-danger)]">
                      <span className="mr-auto">Delete this favorite?</span>
                      <Button size="sm" variant="ghost" onClick={() => setDeleteCandidate(null)} disabled={itemBusy}>Keep favorite</Button>
                      <Button size="sm" variant="outline" onClick={() => void run(async () => { await onDelete(favorite.id); setDeleteCandidate(null) })} disabled={itemBusy}>Delete favorite</Button>
                    </div>
                  ) : null}
                </div>
              )}
            </li>
          )
        })}
      </ol>
    </section>
  )
}
