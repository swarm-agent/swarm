export const DESKTOP_V3_CACHE_OWNER_KEY_PREFIX = 'desktop-v3-cache:v1'
export const DESKTOP_V3_CACHE_DEFAULT_SURFACE = 'desktop'

export interface DesktopV3CacheOwner {
  key: string
  origin: string
  accountScopeId: string
  userId: string
  surface: string
}

export interface DesktopV3CacheOwnerInput {
  origin: string
  accountScopeId: string
  userId: string
  surface?: string
}

export interface DesktopV3CacheOwnerIdentity {
  accountScopeId: string
  userId: string
}

export function createDesktopV3CacheOwner(input: DesktopV3CacheOwnerInput): DesktopV3CacheOwner {
  const origin = normalizeOrigin(input.origin)
  const accountScopeId = normalizeRequiredString(input.accountScopeId, 'accountScopeId')
  const userId = normalizeRequiredString(input.userId, 'userId')
  const surface = normalizeRequiredString(input.surface ?? DESKTOP_V3_CACHE_DEFAULT_SURFACE, 'surface')
  const key = createDesktopV3CacheOwnerKey({ origin, accountScopeId, userId, surface })
  return {
    key,
    origin,
    accountScopeId,
    userId,
    surface,
  }
}

export function createDesktopV3CacheOwnerFromIdentity(
  identity: DesktopV3CacheOwnerIdentity,
  input: { origin?: string; surface?: string } = {},
): DesktopV3CacheOwner {
  return createDesktopV3CacheOwner({
    origin: input.origin ?? currentDesktopV3CacheOrigin(),
    accountScopeId: identity.accountScopeId,
    userId: identity.userId,
    surface: input.surface,
  })
}

export function createDesktopV3CacheOwnerKey(input: Omit<DesktopV3CacheOwnerInput, 'surface'> & { surface: string }): string {
  return [
    DESKTOP_V3_CACHE_OWNER_KEY_PREFIX,
    encodeOwnerKeyPart(input.origin),
    encodeOwnerKeyPart(input.accountScopeId),
    encodeOwnerKeyPart(input.userId),
    encodeOwnerKeyPart(input.surface),
  ].join(':')
}

export function parseDesktopV3CacheOwnerKey(ownerKey: string): DesktopV3CacheOwner {
  const normalizedOwnerKey = normalizeRequiredString(ownerKey, 'ownerKey')
  const prefix = `${DESKTOP_V3_CACHE_OWNER_KEY_PREFIX}:`
  if (!normalizedOwnerKey.startsWith(prefix)) {
    throw new Error('desktop v3 cache owner key has unsupported prefix')
  }

  const parts = normalizedOwnerKey.slice(prefix.length).split(':')
  if (parts.length !== 4) {
    throw new Error('desktop v3 cache owner key must include origin, account scope, user, and surface')
  }

  const owner = createDesktopV3CacheOwner({
    origin: decodeOwnerKeyPart(parts[0], 'origin'),
    accountScopeId: decodeOwnerKeyPart(parts[1], 'accountScopeId'),
    userId: decodeOwnerKeyPart(parts[2], 'userId'),
    surface: decodeOwnerKeyPart(parts[3], 'surface'),
  })
  if (owner.key !== normalizedOwnerKey) {
    throw new Error('desktop v3 cache owner key is not canonical')
  }
  return owner
}

export function isDesktopV3CacheOwnerForKey(owner: DesktopV3CacheOwner, ownerKey: string): boolean {
  return owner.key === ownerKey && createDesktopV3CacheOwnerKey(owner) === ownerKey
}

export function currentDesktopV3CacheOrigin(): string {
  const origin = globalThis.location?.origin
  if (typeof origin !== 'string' || origin.trim() === '') {
    throw new Error('desktop v3 cache owner requires browser origin')
  }
  return normalizeOrigin(origin)
}

function normalizeOrigin(rawOrigin: string): string {
  const trimmed = normalizeRequiredString(rawOrigin, 'origin')
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch (error) {
    throw new Error(`desktop v3 cache owner origin must be an absolute URL: ${String(error instanceof Error ? error.message : error)}`)
  }
  if (parsed.origin === 'null') {
    throw new Error('desktop v3 cache owner origin must not be opaque')
  }
  return parsed.origin
}

function normalizeRequiredString(value: string, name: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    throw new Error(`desktop v3 cache owner ${name} is required`)
  }
  return trimmed
}

function encodeOwnerKeyPart(value: string): string {
  return encodeURIComponent(value)
}

function decodeOwnerKeyPart(value: string, label: string): string {
  try {
    return decodeURIComponent(value)
  } catch (error) {
    throw new Error(`desktop v3 cache owner key ${label} is not valid percent encoding: ${String(error instanceof Error ? error.message : error)}`)
  }
}
