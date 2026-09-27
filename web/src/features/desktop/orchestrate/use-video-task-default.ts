import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { getUISettings } from '../settings/swarm/queries/get-ui-settings'
import { saveVideoDefaultModel } from '../settings/swarm/mutations/save-video-models'
import { resolveQualifiedVideoModel, type TaskModalModelOption } from './videoTaskSettings'

import { persistVideoDefault, resolveVideoDefault } from './video-default-state'

export function useVideoTaskDefault(open: boolean, options: TaskModalModelOption[]) {
  const client = useQueryClient()
  const settings = useQuery({ queryKey: ['ui-settings'], queryFn: getUISettings })
  const [selected, setSelected] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const dirty = useRef(false)
  const saveInFlight = useRef(false)
  const [refreshing, setRefreshing] = useState(false)
  const configured = settings.data?.tools?.video?.default_model || ''
  const defaultModel = resolveVideoDefault(options, configured)

  useEffect(() => {
    if (!open) return
    let cancelled = false
    dirty.current = false
    setSelected('')
    setSaveError(null)
    setRefreshing(true)
    // A reopened modal reads the same canonical cache as /media, but refreshes it.
    void settings.refetch().finally(() => { if (!cancelled) setRefreshing(false) })
    return () => { cancelled = true }
  }, [open])

  useEffect(() => {
    if (open && !refreshing && !settings.isFetching && !settings.isError && !dirty.current) {
      setSelected(defaultModel)
    }
  }, [open, refreshing, settings.isFetching, settings.isError, defaultModel])

  const select = (model: string) => {
    dirty.current = true
    setSelected(model)
  }
  const save = async (model: string) => {
    if (saveInFlight.current) return
    const option = options.find(item => item.id === model)
    const qualified = option && resolveQualifiedVideoModel(option, model)
    if (!qualified || !option?.ready || !settings.data) {
      setSaveError('Select an available model and load settings before saving.')
      return
    }
    saveInFlight.current = true
    setSaving(true)
    setSaveError(null)
    try {
      const current = settings.data
      await persistVideoDefault(client, () => saveVideoDefaultModel({ current, defaultModel: qualified }))
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : 'Failed to save video default.')
    } finally {
      saveInFlight.current = false
      setSaving(false)
    }
  }
  return {
    selected, select, defaultModel, configured, save, saving,
    loading: refreshing || settings.isPending || settings.isFetching,
    error: saveError || (settings.isError ? 'Unable to load video defaults. Retry before submitting.' : null),
    loadFailed: settings.isError,
    retry: () => settings.refetch(),
  }
}
