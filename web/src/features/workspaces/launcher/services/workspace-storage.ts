export function loadStoredValue(key: string): string | null {
  if (typeof window === 'undefined') {
    return null
  }

  const value = window.localStorage.getItem(key)
  return value && value.trim() !== '' ? value : null
}

export function saveStoredValue(key: string, value: string | null) {
  if (typeof window === 'undefined') {
    return
  }

  if (value && value.trim() !== '') {
    window.localStorage.setItem(key, value)
    return
  }

  window.localStorage.removeItem(key)
}
