import type { AuthCredential, AuthCredentialListResponse } from '../../settings/types/auth'

function normalizeProviderID(value: string): string {
  return value.trim().toLowerCase()
}

function credentialUsable(record: AuthCredential): boolean {
  if (!record.active) return false
  if (record.connection && !record.connection.connected) return false
  return true
}

export function desktopProviderNeedsAuth(provider: string, credentials: AuthCredentialListResponse | null | undefined): boolean {
  if (!credentials) return false

  const records = credentials.records
  const normalizedProvider = normalizeProviderID(provider)
  if (!normalizedProvider) {
    return records.length === 0
  }

  const providerRecords = records.filter((record) => normalizeProviderID(record.provider) === normalizedProvider)
  if (providerRecords.length === 0) return true
  return !providerRecords.some(credentialUsable)
}
