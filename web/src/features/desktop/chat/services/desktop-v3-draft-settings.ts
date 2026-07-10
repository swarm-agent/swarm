import { useSyncExternalStore } from 'react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'
import type { DesktopV3ComposerSettingsTuple } from '../../state/desktop-v3-cache-types'
import type { AgentProfileRecord, ModelOptionRecord, SessionPreferenceRecord } from '../types/chat'
import { resolveComposerSettingsFromDraft } from './agent-model-preferences'

export interface DesktopV3DraftSettingsSource {
  mode: DesktopSessionMode
  selectedAgentName: string
  agents: AgentProfileRecord[]
  preference: SessionPreferenceRecord
  modelOptions: ModelOptionRecord[]
}

interface DesktopV3DraftSettingsRecord {
  source: DesktopV3DraftSettingsSource
  tuple: DesktopV3ComposerSettingsTuple
}

const records = new Map<string, DesktopV3DraftSettingsRecord>()
const listeners = new Set<() => void>()

function emit(): void {
  listeners.forEach((listener) => listener())
}

function buildRecord(source: DesktopV3DraftSettingsSource): DesktopV3DraftSettingsRecord | null {
  const tuple = resolveComposerSettingsFromDraft({
    ready: true,
    mode: source.mode,
    selectedAgentName: source.selectedAgentName,
    agents: source.agents,
    preference: source.preference,
    modelOptions: source.modelOptions,
  })
  return tuple ? { source, tuple } : null
}

export function initializeDesktopV3DraftSettings(workspacePath: string, source: DesktopV3DraftSettingsSource): void {
  const key = workspacePath.trim()
  if (!key || records.has(key)) return
  const record = buildRecord(source)
  if (!record) return
  records.set(key, record)
  emit()
}

export function replaceDesktopV3DraftSettings(workspacePath: string, source: DesktopV3DraftSettingsSource): void {
  const key = workspacePath.trim()
  const record = buildRecord(source)
  if (!key || !record) return
  records.set(key, record)
  emit()
}

export function selectDesktopV3DraftMode(workspacePath: string, mode: DesktopSessionMode): void {
  const current = records.get(workspacePath.trim())
  if (!current) return
  const record = buildRecord({ ...current.source, mode })
  if (!record) return
  records.set(workspacePath.trim(), record)
  emit()
}

export function getDesktopV3DraftSettings(workspacePath: string): DesktopV3ComposerSettingsTuple | null {
  return records.get(workspacePath.trim())?.tuple ?? null
}

export function useDesktopV3DraftSettings(workspacePath: string): DesktopV3ComposerSettingsTuple | null {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => getDesktopV3DraftSettings(workspacePath),
    () => null,
  )
}
