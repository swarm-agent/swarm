export interface DesktopV3CacheOwner {
  origin: string
  accountScopeId: string
  userId: string
  surface: string
  key: string
}

export interface DesktopV3CacheOwnerInput {
  origin: string
  accountScopeId: string
  userId: string
  surface?: string
}

export const DESKTOP_V3_CACHE_OWNER_KEY_PREFIX = 'desktop-v3-cache:v1'

export function createDesktopV3CacheOwner(input: DesktopV3CacheOwnerInput): DesktopV3CacheOwner {
  const origin = requireNonEmptyOwnerField(input.origin, 'origin')
  const accountScopeId = requireNonEmptyOwnerField(input.accountScopeId, 'accountScopeId')
  const userId = requireNonEmptyOwnerField(input.userId, 'userId')
  const surface = normalizeOwnerSurface(input.surface)
  const key = buildDesktopV3CacheOwnerKey({ origin, accountScopeId, userId, surface })
  return { origin, accountScopeId, userId, surface, key }
}

export function buildDesktopV3CacheOwnerKey(input: Omit<DesktopV3CacheOwnerInput, 'surface'> & { surface: string }): string {
  const origin = requireNonEmptyOwnerField(input.origin, 'origin')
  const accountScopeId = requireNonEmptyOwnerField(input.accountScopeId, 'accountScopeId')
  const userId = requireNonEmptyOwnerField(input.userId, 'userId')
  const surface = normalizeOwnerSurface(input.surface)
  return [
    DESKTOP_V3_CACHE_OWNER_KEY_PREFIX,
    encodeOwnerKeySegment(origin),
    encodeOwnerKeySegment(accountScopeId),
    encodeOwnerKeySegment(userId),
    encodeOwnerKeySegment(surface),
  ].join(':')
}

export function isDesktopV3CacheOwner(value: unknown): value is DesktopV3CacheOwner {
  if (!isRecord(value)) return false
  if (typeof value.origin !== 'string' || value.origin.trim() === '') return false
  if (typeof value.accountScopeId !== 'string' || value.accountScopeId.trim() === '') return false
  if (typeof value.userId !== 'string' || value.userId.trim() === '') return false
  if (typeof value.surface !== 'string' || value.surface.trim() === '') return false
  if (typeof value.key !== 'string' || value.key.trim() === '') return false
  try {
    return value.key === buildDesktopV3CacheOwnerKey({
      origin: value.origin,
      accountScopeId: value.accountScopeId,
      userId: value.userId,
      surface: value.surface,
    })
  } catch {
    return false
  }
}

function requireNonEmptyOwnerField(value: string, field: 'origin' | 'accountScopeId' | 'userId'): string {
  const normalized = String(value ?? '').trim()
  if (!normalized) {
    throw new Error(`desktop v3 cache owner ${field} is required`)
  }
  return normalized
}

function normalizeOwnerSurface(surface: string | undefined): string {
  const normalized = String(surface ?? 'desktop').trim()
  if (!normalized) {
    throw new Error('desktop v3 cache owner surface is required')
  }
  return normalized
}

function encodeOwnerKeySegment(value: string): string {
  return encodeURIComponent(value)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
