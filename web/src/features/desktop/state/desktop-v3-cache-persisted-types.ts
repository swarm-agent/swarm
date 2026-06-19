import type {
  MessageSnapshot,
  SessionSnapshot,
  SyncScopeCache,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
} from './desktop-v3-cache-types'
import { isDesktopV3CacheOwner, type DesktopV3CacheOwner } from './desktop-v3-cache-owner'

export const DESKTOP_V3_CACHE_SCHEMA_VERSION = 1

export interface PersistedDesktopV3OwnerV1 {
  schemaVersion: 1
  owner: DesktopV3CacheOwner
  savedAt: number
  syncScopesById: Record<string, SyncScopeCache>
  sessionsById: Record<string, SessionSnapshot>
  projectionsBySession: Record<string, V3SessionProjection>
  sessionOrderByScope: Record<string, string[]>
  tombstonesBySession: Record<string, V3SessionTombstone>
  runIntentsBySession: Record<string, Record<string, V3SessionRunIntent>>
  currentRunIntentBySession: Record<string, V3SessionRunIntent | undefined>
  selectedSessionId?: string
}

export interface PersistedDesktopV3MessageTailV1 {
  schemaVersion: 1
  ownerKey: string
  sessionId: string
  savedAt: number
  messages: MessageSnapshot[]
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
  source: 'network' | 'persisted'
}

export type PersistedDesktopV3ValidationResult<T> =
  | { ok: true; value: T }
  | { ok: false; delete: true; reason: string }

export function validatePersistedDesktopV3OwnerV1(value: unknown): PersistedDesktopV3ValidationResult<PersistedDesktopV3OwnerV1> {
  if (!isRecord(value)) return invalidPersistedRecord('owner record is not an object')
  if (value.schemaVersion !== DESKTOP_V3_CACHE_SCHEMA_VERSION) return invalidSchemaVersion(value.schemaVersion)
  if (!isDesktopV3CacheOwner(value.owner)) return invalidPersistedRecord('owner identity is invalid')
  if (!isFiniteNumber(value.savedAt)) return invalidPersistedRecord('savedAt is invalid')
  if (!isRecord(value.syncScopesById)) return invalidPersistedRecord('syncScopesById is invalid')
  if (!isSessionRecord(value.sessionsById)) return invalidPersistedRecord('sessionsById is invalid')
  if (!isRecord(value.projectionsBySession)) return invalidPersistedRecord('projectionsBySession is invalid')
  if (!isStringArrayRecord(value.sessionOrderByScope)) return invalidPersistedRecord('sessionOrderByScope is invalid')
  if (!isRecord(value.tombstonesBySession)) return invalidPersistedRecord('tombstonesBySession is invalid')
  if (!isRecord(value.runIntentsBySession)) return invalidPersistedRecord('runIntentsBySession is invalid')
  if (!isRecord(value.currentRunIntentBySession)) return invalidPersistedRecord('currentRunIntentBySession is invalid')
  if (value.selectedSessionId !== undefined && typeof value.selectedSessionId !== 'string') {
    return invalidPersistedRecord('selectedSessionId is invalid')
  }
  return { ok: true, value: value as unknown as PersistedDesktopV3OwnerV1 }
}

export function validatePersistedDesktopV3MessageTailV1(value: unknown): PersistedDesktopV3ValidationResult<PersistedDesktopV3MessageTailV1> {
  if (!isRecord(value)) return invalidPersistedRecord('message tail record is not an object')
  if (value.schemaVersion !== DESKTOP_V3_CACHE_SCHEMA_VERSION) return invalidSchemaVersion(value.schemaVersion)
  if (typeof value.ownerKey !== 'string' || value.ownerKey.trim() === '') return invalidPersistedRecord('ownerKey is invalid')
  if (typeof value.sessionId !== 'string' || value.sessionId.trim() === '') return invalidPersistedRecord('sessionId is invalid')
  if (!isFiniteNumber(value.savedAt)) return invalidPersistedRecord('savedAt is invalid')
  if (!Array.isArray(value.messages) || !value.messages.every(isMessageSnapshot)) return invalidPersistedRecord('messages are invalid')
  if (!isOptionalFiniteNumber(value.sourceMessageCount)) return invalidPersistedRecord('sourceMessageCount is invalid')
  if (!isOptionalFiniteNumber(value.sourceLastMessageAt)) return invalidPersistedRecord('sourceLastMessageAt is invalid')
  if (!isOptionalFiniteNumber(value.sourceProjectionHighWatermarkSeq)) {
    return invalidPersistedRecord('sourceProjectionHighWatermarkSeq is invalid')
  }
  if (!isOptionalFiniteNumber(value.hydratedAt)) return invalidPersistedRecord('hydratedAt is invalid')
  if (value.source !== 'network' && value.source !== 'persisted') return invalidPersistedRecord('source is invalid')
  return { ok: true, value: value as unknown as PersistedDesktopV3MessageTailV1 }
}

function invalidSchemaVersion(version: unknown): PersistedDesktopV3ValidationResult<never> {
  const label = typeof version === 'number' && version > DESKTOP_V3_CACHE_SCHEMA_VERSION ? 'future schema version' : 'schema version is invalid'
  return invalidPersistedRecord(label)
}

function invalidPersistedRecord(reason: string): PersistedDesktopV3ValidationResult<never> {
  return { ok: false, delete: true, reason }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function isOptionalFiniteNumber(value: unknown): value is number | undefined {
  return value === undefined || isFiniteNumber(value)
}

function isStringArrayRecord(value: unknown): value is Record<string, string[]> {
  return isRecord(value) && Object.values(value).every((entry) => Array.isArray(entry) && entry.every((item) => typeof item === 'string'))
}

function isSessionRecord(value: unknown): value is Record<string, SessionSnapshot> {
  return isRecord(value) && Object.values(value).every(isSessionSnapshot)
}

function isSessionSnapshot(value: unknown): value is SessionSnapshot {
  if (!isRecord(value)) return false
  return typeof value.id === 'string'
    && typeof value.workspace_path === 'string'
    && typeof value.workspace_name === 'string'
    && typeof value.title === 'string'
    && typeof value.mode === 'string'
    && isFiniteNumber(value.created_at)
    && isFiniteNumber(value.updated_at)
    && isFiniteNumber(value.message_count)
    && isFiniteNumber(value.last_message_at)
}

function isMessageSnapshot(value: unknown): value is MessageSnapshot {
  if (!isRecord(value)) return false
  return typeof value.id === 'string'
    && typeof value.session_id === 'string'
    && isFiniteNumber(value.global_seq)
    && typeof value.role === 'string'
    && typeof value.content === 'string'
    && isFiniteNumber(value.created_at)
}
