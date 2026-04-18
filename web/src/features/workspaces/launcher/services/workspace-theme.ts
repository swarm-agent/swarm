interface WorkspaceThemeOption {
  id: string
  label: string
}

interface WorkspaceThemeBasePalette {
  background: string
  panel: string
  border: string
  text: string
  textMuted: string
  primary: string
  warning: string
  error: string
}

interface WorkspaceThemePalette {
  background: string
  backgroundAlt: string
  backgroundPanel: string
  backgroundInset: string
  backgroundOverlay: string
  surface: string
  surfaceSubtle: string
  surfaceElevated: string
  surfaceHover: string
  surfaceActive: string
  border: string
  borderMuted: string
  borderStrong: string
  borderAccent: string
  text: string
  textMuted: string
  textSubtle: string
  textInverse: string
  textAccent: string
  accent: string
  accentHover: string
  accentActive: string
  accentText: string
  selection: string
  focusRing: string
  warning: string
  warningBackground: string
  warningBorder: string
  error: string
  errorBackground: string
  errorBorder: string
  success: string
  successBackground: string
  successBorder: string
  info: string
  infoBackground: string
  infoBorder: string
  shadowColor: string
  backdrop: string
}

export type { WorkspaceThemeOption }

const BUILTIN_WORKSPACE_THEME_OPTIONS: WorkspaceThemeOption[] = [
  { id: 'black', label: 'Black' },
  { id: 'softwhite', label: 'Softwhite' },
  { id: 'nord', label: 'Nord' },
  { id: 'crimson', label: 'Crimson' },
  { id: 'solarized-dark', label: 'Solarized Dark' },
  { id: 'dracula', label: 'Dracula' },
  { id: 'gruvbox-dark', label: 'Gruvbox Dark' },
  { id: 'catppuccin-mocha', label: 'Catppuccin Mocha' },
  { id: 'tokyo-night', label: 'Tokyo Night' },
  { id: 'everforest-dark', label: 'Everforest Dark' },
  { id: 'ayu-mirage', label: 'Ayu Mirage' },
  { id: 'one-dark', label: 'One Dark' },
  { id: 'kanagawa-wave', label: 'Kanagawa Wave' },
  { id: 'rose-pine', label: 'Rose Pine' },
  { id: 'monokai', label: 'Monokai' },
  { id: 'oceanic-next', label: 'Oceanic Next' },
  { id: 'graphite', label: 'Graphite' },
  { id: 'cyberpunk', label: 'Cyberpunk' },
  { id: 'emerald-forest', label: 'Emerald Forest' },
  { id: 'paper-ink', label: 'Paper Ink' },
  { id: 'sunset-amber', label: 'Sunset Amber' },
  { id: 'neon-night', label: 'Neon Night' },
]

export const WORKSPACE_THEME_OPTIONS: WorkspaceThemeOption[] = [...BUILTIN_WORKSPACE_THEME_OPTIONS]

const THEME_LABELS = new Map(BUILTIN_WORKSPACE_THEME_OPTIONS.map((item) => [item.id, item.label]))

const THEME_PALETTES: Record<string, WorkspaceThemeBasePalette> = {
  black: {
    background: '#000000',
    panel: '#111111',
    border: '#27272A',
    text: '#F5F5F0',
    textMuted: '#A8A29A',
    primary: '#D6D0C4',
    warning: '#C9A66B',
    error: '#C46C6C',
  },
  softwhite: {
    background: '#F7F4EC',
    panel: '#FFFDF8',
    border: '#D6CFC1',
    text: '#23201B',
    textMuted: '#6F675B',
    primary: '#5F5A52',
    warning: '#9B7A44',
    error: '#A05C5C',
  },
  nord: {
    background: '#2E3440',
    panel: '#3B4252',
    border: '#434C5E',
    text: '#ECEFF4',
    textMuted: '#8B95A7',
    primary: '#88C0D0',
    warning: '#D08770',
    error: '#BF616A',
  },
  crimson: {
    background: '#1E1418',
    panel: '#26191E',
    border: '#5E3841',
    text: '#BBB2B6',
    textMuted: '#B9A8AD',
    primary: '#E36A7A',
    warning: '#E4B15A',
    error: '#FF6B6B',
  },
  'solarized-dark': {
    background: '#002B36',
    panel: '#073642',
    border: '#586E75',
    text: '#EEE8D5',
    textMuted: '#93A1A1',
    primary: '#268BD2',
    warning: '#B58900',
    error: '#DC322F',
  },
  dracula: {
    background: '#282A36',
    panel: '#303341',
    border: '#44475A',
    text: '#F8F8F2',
    textMuted: '#B0B6D0',
    primary: '#BD93F9',
    warning: '#F1FA8C',
    error: '#FF5555',
  },
  'gruvbox-dark': {
    background: '#282828',
    panel: '#32302F',
    border: '#504945',
    text: '#EBDBB2',
    textMuted: '#A89984',
    primary: '#83A598',
    warning: '#FABD2F',
    error: '#FB4934',
  },
  'catppuccin-mocha': {
    background: '#1E1E2E',
    panel: '#313244',
    border: '#45475A',
    text: '#CDD6F4',
    textMuted: '#A6ADC8',
    primary: '#89B4FA',
    warning: '#F9E2AF',
    error: '#F38BA8',
  },
  'tokyo-night': {
    background: '#1A1B26',
    panel: '#1F2335',
    border: '#3B4261',
    text: '#C0CAF5',
    textMuted: '#9AA5CE',
    primary: '#7AA2F7',
    warning: '#E0AF68',
    error: '#F7768E',
  },
  'everforest-dark': {
    background: '#2D353B',
    panel: '#343F44',
    border: '#475258',
    text: '#D3C6AA',
    textMuted: '#9DA9A0',
    primary: '#7FBBB3',
    warning: '#E69875',
    error: '#E67E80',
  },
  'ayu-mirage': {
    background: '#1F2430',
    panel: '#242936',
    border: '#3A4256',
    text: '#CCCAC2',
    textMuted: '#97A0B3',
    primary: '#73D0FF',
    warning: '#FFAD66',
    error: '#F28779',
  },
  'one-dark': {
    background: '#282C34',
    panel: '#2C313A',
    border: '#3E4451',
    text: '#ABB2BF',
    textMuted: '#8F96A3',
    primary: '#61AFEF',
    warning: '#E5C07B',
    error: '#E06C75',
  },
  'kanagawa-wave': {
    background: '#1F1F28',
    panel: '#2A2A37',
    border: '#3D3D51',
    text: '#DCD7BA',
    textMuted: '#A6A69C',
    primary: '#7E9CD8',
    warning: '#DCA561',
    error: '#E46876',
  },
  'rose-pine': {
    background: '#191724',
    panel: '#1F1D2E',
    border: '#403D52',
    text: '#E0DEF4',
    textMuted: '#908CAA',
    primary: '#C4A7E7',
    warning: '#F6C177',
    error: '#EB6F92',
  },
  monokai: {
    background: '#272822',
    panel: '#2F3129',
    border: '#49483E',
    text: '#F8F8F2',
    textMuted: '#AEAEA8',
    primary: '#66D9EF',
    warning: '#E6DB74',
    error: '#F92672',
  },
  'oceanic-next': {
    background: '#1B2B34',
    panel: '#22313A',
    border: '#405860',
    text: '#D8DEE9',
    textMuted: '#A7ADBA',
    primary: '#6699CC',
    warning: '#FAC863',
    error: '#EC5F67',
  },
  graphite: {
    background: '#202428',
    panel: '#272C31',
    border: '#3A4148',
    text: '#E4E7EB',
    textMuted: '#A7B0B8',
    primary: '#7CB7FF',
    warning: '#E6C178',
    error: '#E68A8A',
  },
  cyberpunk: {
    background: '#0B0F1A',
    panel: '#101729',
    border: '#22405E',
    text: '#C8D6FF',
    textMuted: '#7F96C9',
    primary: '#00E5FF',
    warning: '#FFD54F',
    error: '#FF5252',
  },
  'emerald-forest': {
    background: '#10231C',
    panel: '#173229',
    border: '#2D4A40',
    text: '#D6F5E5',
    textMuted: '#9ABAA9',
    primary: '#5BD6A1',
    warning: '#F5C16C',
    error: '#FF7B7B',
  },
  'paper-ink': {
    background: '#FAF7F1',
    panel: '#F0E9DD',
    border: '#B8AD9E',
    text: '#2A2A2A',
    textMuted: '#6D665F',
    primary: '#1F5E8C',
    warning: '#A06B22',
    error: '#A13333',
  },
  'sunset-amber': {
    background: '#2B1E1A',
    panel: '#352522',
    border: '#5A4037',
    text: '#F8E7DA',
    textMuted: '#C5A892',
    primary: '#FF9E64',
    warning: '#FFD166',
    error: '#FF6B6B',
  },
  'neon-night': {
    background: '#14111F',
    panel: '#1B172A',
    border: '#3A3159',
    text: '#E8E6FF',
    textMuted: '#A9A5D7',
    primary: '#7AF5FF',
    warning: '#FFE082',
    error: '#FF6E8A',
  },
}

type WorkspaceCustomThemeWire = {
  id?: string
  name?: string
  palette?: Record<string, unknown>
}

function updateWorkspaceThemeOptions(customOptions: WorkspaceThemeOption[]) {
  WORKSPACE_THEME_OPTIONS.splice(0, WORKSPACE_THEME_OPTIONS.length, ...BUILTIN_WORKSPACE_THEME_OPTIONS, ...customOptions)
  THEME_LABELS.clear()
  for (const item of WORKSPACE_THEME_OPTIONS) {
    THEME_LABELS.set(item.id, item.label)
  }
}

export function setWorkspaceThemeCustomOptions(customThemes: Array<Record<string, unknown>> | null | undefined) {
  for (const key of Object.keys(THEME_PALETTES)) {
    if (!BUILTIN_WORKSPACE_THEME_OPTIONS.some((item) => item.id === key)) {
      delete THEME_PALETTES[key]
    }
  }

  const customOptions: WorkspaceThemeOption[] = []
  const seen = new Set<string>()
  for (const raw of customThemes ?? []) {
    const item = raw as WorkspaceCustomThemeWire
    const id = normalizeThemeId(typeof item.id === 'string' ? item.id : '')
    if (!id || seen.has(id) || BUILTIN_WORKSPACE_THEME_OPTIONS.some((entry) => entry.id === id)) {
      continue
    }
    seen.add(id)
    const palette = item.palette && typeof item.palette === 'object' ? item.palette : {}
    const text = typeof palette.text === 'string' ? palette.text : '#ECEFF4'
    const textMuted = typeof palette.text_muted === 'string' ? palette.text_muted : '#8B95A7'
    const primary = typeof palette.primary === 'string'
      ? palette.primary
      : typeof palette.accent === 'string'
        ? palette.accent
        : '#88C0D0'
    THEME_PALETTES[id] = {
      background: typeof palette.background === 'string' ? palette.background : '#2E3440',
      panel: typeof palette.panel === 'string' ? palette.panel : typeof palette.element === 'string' ? palette.element : '#3B4252',
      border: typeof palette.border === 'string' ? palette.border : typeof palette.border_active === 'string' ? palette.border_active : textMuted,
      text,
      textMuted,
      primary,
      warning: typeof palette.warning === 'string' ? palette.warning : '#D08770',
      error: typeof palette.error === 'string' ? palette.error : '#BF616A',
    }
    customOptions.push({
      id,
      label: typeof item.name === 'string' && item.name.trim() ? item.name.trim() : formatWorkspaceThemeLabel(id),
    })
  }
  updateWorkspaceThemeOptions(customOptions)
}

function normalizeThemeId(themeId: string | null | undefined): string {
  return (themeId ?? '').trim().toLowerCase()
}

function parseHex(hex: string): [number, number, number] | null {
  const value = hex.replace('#', '')
  if (value.length !== 6) {
    return null
  }

  return [
    Number.parseInt(value.slice(0, 2), 16),
    Number.parseInt(value.slice(2, 4), 16),
    Number.parseInt(value.slice(4, 6), 16),
  ]
}

function clampColorChannel(value: number): number {
  return Math.max(0, Math.min(255, Math.round(value)))
}

function toHex([red, green, blue]: [number, number, number]): string {
  return `#${[red, green, blue].map((value) => clampColorChannel(value).toString(16).padStart(2, '0')).join('')}`
}

function toAlphaColor(hex: string, alpha: number): string {
  const rgb = parseHex(hex)
  if (!rgb) {
    return hex
  }

  const clampedAlpha = Math.max(0, Math.min(1, alpha))
  return `rgb(${rgb[0]} ${rgb[1]} ${rgb[2]} / ${clampedAlpha})`
}

function mixHex(left: string, right: string, ratio: number): string {
  const leftRgb = parseHex(left)
  const rightRgb = parseHex(right)
  if (!leftRgb || !rightRgb) {
    return left
  }

  const clampedRatio = Math.max(0, Math.min(1, ratio))
  return toHex([
    leftRgb[0] + (rightRgb[0] - leftRgb[0]) * clampedRatio,
    leftRgb[1] + (rightRgb[1] - leftRgb[1]) * clampedRatio,
    leftRgb[2] + (rightRgb[2] - leftRgb[2]) * clampedRatio,
  ])
}

function isLightHex(hex: string): boolean {
  const rgb = parseHex(hex)
  if (!rgb) {
    return false
  }

  const [r, g, b] = rgb
  const luminance = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
  return luminance > 0.62
}

function completePalette(base: WorkspaceThemeBasePalette): WorkspaceThemePalette {
  const lightBackground = isLightHex(base.background)
  const accentAnchor = lightBackground ? '#000000' : '#ffffff'
  const textInverse = lightBackground ? '#0F172A' : '#F8FAFC'
  const success = lightBackground ? '#1F7A4D' : '#4ADE80'
  const info = mixHex(base.primary, textInverse, lightBackground ? 0.08 : 0.12)

  return {
    background: base.background,
    backgroundAlt: mixHex(base.background, base.panel, 0.45),
    backgroundPanel: mixHex(base.background, base.panel, 0.7),
    backgroundInset: mixHex(base.background, accentAnchor, lightBackground ? 0.03 : 0.08),
    backgroundOverlay: toAlphaColor(base.background, lightBackground ? 0.9 : 0.86),
    surface: base.panel,
    surfaceSubtle: mixHex(base.background, base.panel, 0.28),
    surfaceElevated: mixHex(base.panel, accentAnchor, lightBackground ? 0.04 : 0.07),
    surfaceHover: mixHex(base.panel, accentAnchor, lightBackground ? 0.07 : 0.11),
    surfaceActive: mixHex(base.panel, accentAnchor, lightBackground ? 0.11 : 0.17),
    border: base.border,
    borderMuted: mixHex(base.border, base.background, 0.35),
    borderStrong: mixHex(base.border, base.primary, 0.38),
    borderAccent: mixHex(base.border, base.primary, 0.62),
    text: base.text,
    textMuted: base.textMuted,
    textSubtle: mixHex(base.textMuted, base.background, 0.28),
    textInverse,
    textAccent: base.primary,
    accent: base.primary,
    accentHover: mixHex(base.primary, accentAnchor, lightBackground ? 0.16 : 0.2),
    accentActive: mixHex(base.primary, accentAnchor, lightBackground ? 0.28 : 0.32),
    accentText: isLightHex(base.primary) ? '#111827' : '#F8FAFC',
    selection: mixHex(base.panel, base.primary, 0.18),
    focusRing: mixHex(base.primary, accentAnchor, lightBackground ? 0.1 : 0.16),
    warning: base.warning,
    warningBackground: mixHex(base.background, base.warning, lightBackground ? 0.14 : 0.18),
    warningBorder: mixHex(base.border, base.warning, 0.54),
    error: base.error,
    errorBackground: mixHex(base.background, base.error, lightBackground ? 0.14 : 0.18),
    errorBorder: mixHex(base.border, base.error, 0.58),
    success,
    successBackground: mixHex(base.background, success, lightBackground ? 0.12 : 0.18),
    successBorder: mixHex(base.border, success, 0.52),
    info,
    infoBackground: mixHex(base.background, info, lightBackground ? 0.12 : 0.18),
    infoBorder: mixHex(base.border, info, 0.5),
    shadowColor: toAlphaColor(accentAnchor, lightBackground ? 0.18 : 0.42),
    backdrop: toAlphaColor(base.background, lightBackground ? 0.62 : 0.74),
  }
}

function resolveThemePalette(themeId: string | null | undefined): WorkspaceThemePalette | null {
  const normalized = normalizeThemeId(themeId)
  if (normalized === '') {
    return null
  }
  const base = THEME_PALETTES[normalized]
  return base ? completePalette(base) : null
}

function resolveThemeColorScheme(themeId: string | null | undefined): 'light' | 'dark' | null {
  const palette = resolveThemePalette(themeId)
  if (!palette) {
    return null
  }
  return isLightHex(palette.background) ? 'light' : 'dark'
}

export function formatWorkspaceThemeLabel(themeId: string): string {
  const normalized = normalizeThemeId(themeId)
  return THEME_LABELS.get(normalized) ?? themeId.trim()
}

export function createWorkspaceAccentStyle(themeId: string | null | undefined, prefix: string): Record<string, string> {
  const palette = resolveThemePalette(themeId)
  if (!palette) {
    return {}
  }

  return {
    [`${prefix}-accent`]: palette.accent,
    [`${prefix}-accent-hover`]: palette.accentHover,
    [`${prefix}-accent-active`]: palette.accentActive,
    [`${prefix}-selection`]: palette.selection,
    [`${prefix}-border-strong`]: palette.borderStrong,
    [`${prefix}-border-accent`]: palette.borderAccent,
    [`${prefix}-info`]: palette.info,
    [`${prefix}-success`]: palette.success,
    [`${prefix}-warning`]: palette.warning,
    [`${prefix}-danger`]: palette.error,
  }
}

export function createWorkspaceThemeStyle(themeId: string | null | undefined, prefix: string): Record<string, string> {
  const palette = resolveThemePalette(themeId)
  if (!palette) {
    return {}
  }

  return {
    [`${prefix}-background`]: palette.background,
    [`${prefix}-background-alt`]: palette.backgroundAlt,
    [`${prefix}-background-panel`]: palette.backgroundPanel,
    [`${prefix}-background-inset`]: palette.backgroundInset,
    [`${prefix}-background-overlay`]: palette.backgroundOverlay,
    [`${prefix}-surface`]: palette.surface,
    [`${prefix}-surface-subtle`]: palette.surfaceSubtle,
    [`${prefix}-surface-elevated`]: palette.surfaceElevated,
    [`${prefix}-surface-hover`]: palette.surfaceHover,
    [`${prefix}-surface-active`]: palette.surfaceActive,
    [`${prefix}-panel`]: palette.surface,
    [`${prefix}-border`]: palette.border,
    [`${prefix}-border-muted`]: palette.borderMuted,
    [`${prefix}-border-strong`]: palette.borderStrong,
    [`${prefix}-border-accent`]: palette.borderAccent,
    [`${prefix}-text`]: palette.text,
    [`${prefix}-text-muted`]: palette.textMuted,
    [`${prefix}-text-subtle`]: palette.textSubtle,
    [`${prefix}-text-inverse`]: palette.textInverse,
    [`${prefix}-text-accent`]: palette.textAccent,
    [`${prefix}-accent`]: palette.accent,
    [`${prefix}-accent-hover`]: palette.accentHover,
    [`${prefix}-accent-active`]: palette.accentActive,
    [`${prefix}-accent-text`]: palette.accentText,
    [`${prefix}-primary`]: palette.accent,
    [`${prefix}-selection`]: palette.selection,
    [`${prefix}-focus-ring`]: palette.focusRing,
    [`${prefix}-warning`]: palette.warning,
    [`${prefix}-warning-bg`]: palette.warningBackground,
    [`${prefix}-warning-border`]: palette.warningBorder,
    [`${prefix}-danger`]: palette.error,
    [`${prefix}-danger-bg`]: palette.errorBackground,
    [`${prefix}-danger-border`]: palette.errorBorder,
    [`${prefix}-error`]: palette.error,
    [`${prefix}-success`]: palette.success,
    [`${prefix}-success-bg`]: palette.successBackground,
    [`${prefix}-success-border`]: palette.successBorder,
    [`${prefix}-info`]: palette.info,
    [`${prefix}-info-bg`]: palette.infoBackground,
    [`${prefix}-info-border`]: palette.infoBorder,
    [`${prefix}-shadow-color`]: palette.shadowColor,
    [`${prefix}-backdrop`]: palette.backdrop,
  }
}

const APP_THEME_VARIABLE_NAMES = [
  '--app-bg',
  '--app-bg-alt',
  '--app-bg-panel',
  '--app-bg-inset',
  '--app-bg-overlay',
  '--app-surface',
  '--app-surface-subtle',
  '--app-surface-elevated',
  '--app-surface-hover',
  '--app-surface-active',
  '--app-border',
  '--app-border-muted',
  '--app-border-strong',
  '--app-border-accent',
  '--app-text',
  '--app-text-muted',
  '--app-text-subtle',
  '--app-text-inverse',
  '--app-text-accent',
  '--app-primary',
  '--app-primary-hover',
  '--app-primary-active',
  '--app-primary-text',
  '--app-selection',
  '--app-focus-ring',
  '--app-warning',
  '--app-warning-bg',
  '--app-warning-border',
  '--app-danger',
  '--app-danger-bg',
  '--app-danger-border',
  '--app-success',
  '--app-success-bg',
  '--app-success-border',
  '--app-info',
  '--app-info-bg',
  '--app-info-border',
  '--app-shadow-color',
  '--app-backdrop',
  '--button-soft-background',
  '--button-soft-background-hover',
  '--button-soft-border',
  '--button-soft-border-hover',
  '--button-soft-text',
] as const

function buildAppThemeVariables(palette: WorkspaceThemePalette): Record<(typeof APP_THEME_VARIABLE_NAMES)[number], string> {
  return {
    '--app-bg': palette.background,
    '--app-bg-alt': palette.backgroundAlt,
    '--app-bg-panel': palette.backgroundPanel,
    '--app-bg-inset': palette.backgroundInset,
    '--app-bg-overlay': palette.backgroundOverlay,
    '--app-surface': palette.surface,
    '--app-surface-subtle': palette.surfaceSubtle,
    '--app-surface-elevated': palette.surfaceElevated,
    '--app-surface-hover': palette.surfaceHover,
    '--app-surface-active': palette.surfaceActive,
    '--app-border': palette.border,
    '--app-border-muted': palette.borderMuted,
    '--app-border-strong': palette.borderStrong,
    '--app-border-accent': palette.borderAccent,
    '--app-text': palette.text,
    '--app-text-muted': palette.textMuted,
    '--app-text-subtle': palette.textSubtle,
    '--app-text-inverse': palette.textInverse,
    '--app-text-accent': palette.textAccent,
    '--app-primary': palette.accent,
    '--app-primary-hover': palette.accentHover,
    '--app-primary-active': palette.accentActive,
    '--app-primary-text': palette.accentText,
    '--app-selection': palette.selection,
    '--app-focus-ring': palette.focusRing,
    '--app-warning': palette.warning,
    '--app-warning-bg': palette.warningBackground,
    '--app-warning-border': palette.warningBorder,
    '--app-danger': palette.error,
    '--app-danger-bg': palette.errorBackground,
    '--app-danger-border': palette.errorBorder,
    '--app-success': palette.success,
    '--app-success-bg': palette.successBackground,
    '--app-success-border': palette.successBorder,
    '--app-info': palette.info,
    '--app-info-bg': palette.infoBackground,
    '--app-info-border': palette.infoBorder,
    '--app-shadow-color': palette.shadowColor,
    '--app-backdrop': palette.backdrop,
    '--button-soft-background': palette.surfaceSubtle,
    '--button-soft-background-hover': palette.surfaceHover,
    '--button-soft-border': palette.border,
    '--button-soft-border-hover': palette.borderStrong,
    '--button-soft-text': palette.text,
  }
}

function clearAppliedWorkspaceTheme(root: HTMLElement) {
  root.style.removeProperty('color-scheme')
  root.removeAttribute('data-theme')
  for (const propertyName of APP_THEME_VARIABLE_NAMES) {
    root.style.removeProperty(propertyName)
  }
}

export function applyWorkspaceTheme(themeId: string | null | undefined) {
  if (typeof document === 'undefined') {
    return
  }

  const root = document.documentElement
  const palette = resolveThemePalette(themeId)
  const colorScheme = resolveThemeColorScheme(themeId)
  if (!palette || !colorScheme) {
    clearAppliedWorkspaceTheme(root)
    return
  }

  root.style.setProperty('color-scheme', colorScheme)
  root.dataset.theme = colorScheme
  for (const [propertyName, propertyValue] of Object.entries(buildAppThemeVariables(palette))) {
    root.style.setProperty(propertyName, propertyValue)
  }
}
