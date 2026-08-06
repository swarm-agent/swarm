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
  success: string
  warning: string
  error: string
  codeBackground?: string
  codeText?: string
  codeKeyword?: string
  codeString?: string
  codeNumber?: string
  codeComment?: string
  codeFunction?: string
  codeType?: string
  codeOperator?: string
  codePath?: string
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
  codeBackground: string
  codeText: string
  codeKeyword: string
  codeString: string
  codeNumber: string
  codeComment: string
  codeFunction: string
  codeType: string
  codeOperator: string
  codePath: string
}

export type { WorkspaceThemeOption }

interface WorkspaceThemePaletteWire {
  background?: string
  panel?: string
  element?: string
  border?: string
  text?: string
  text_muted?: string
  primary?: string
  accent?: string
  success?: string
  warning?: string
  error?: string
  code_background?: string
  code_text?: string
  code_keyword?: string
  code_string?: string
  code_number?: string
  code_comment?: string
  code_function?: string
  code_type?: string
  code_operator?: string
  code_path?: string
}

interface WorkspaceThemeWire {
  id?: string
  name?: string
  palette?: WorkspaceThemePaletteWire
}

export interface WorkspaceThemeCatalogWire {
  default_theme_id?: string
  builtin_themes?: Array<Record<string, unknown>>
  custom_themes?: Array<Record<string, unknown>>
}

export const WORKSPACE_THEME_OPTIONS: WorkspaceThemeOption[] = []

const BUILTIN_WORKSPACE_THEME_IDS = new Set<string>()
const THEME_LABELS = new Map<string, string>()
const THEME_PALETTES: Record<string, WorkspaceThemeBasePalette> = {}
let defaultWorkspaceThemeId = ''

function themeBasePalette(raw: WorkspaceThemePaletteWire | null | undefined, allowDefaults: boolean): WorkspaceThemeBasePalette | null {
  const background = typeof raw?.background === 'string' ? raw.background : allowDefaults ? '#2E3440' : ''
  const panel = typeof raw?.panel === 'string'
    ? raw.panel
    : allowDefaults && typeof raw?.element === 'string'
      ? raw.element
      : allowDefaults
        ? background
        : ''
  const text = typeof raw?.text === 'string' ? raw.text : allowDefaults ? '#E5E9F0' : ''
  const textMuted = typeof raw?.text_muted === 'string' ? raw.text_muted : allowDefaults ? '#9AA3B2' : ''
  const primary = typeof raw?.primary === 'string'
    ? raw.primary
    : allowDefaults && typeof raw?.accent === 'string'
      ? raw.accent
      : allowDefaults
        ? '#88C0D0'
        : ''
  const border = typeof raw?.border === 'string' ? raw.border : allowDefaults ? textMuted : ''
  const success = typeof raw?.success === 'string' ? raw.success : allowDefaults ? '#A3BE8C' : ''
  const warning = typeof raw?.warning === 'string' ? raw.warning : allowDefaults ? '#EBCB8B' : ''
  const error = typeof raw?.error === 'string' ? raw.error : allowDefaults ? '#BF616A' : ''
  if (![background, panel, border, text, textMuted, primary, success, warning, error].every(Boolean)) {
    return null
  }
  return {
    background,
    panel,
    border,
    text,
    textMuted,
    primary,
    success,
    warning,
    error,
    codeBackground: typeof raw?.code_background === 'string' ? raw.code_background : undefined,
    codeText: typeof raw?.code_text === 'string' ? raw.code_text : undefined,
    codeKeyword: typeof raw?.code_keyword === 'string' ? raw.code_keyword : undefined,
    codeString: typeof raw?.code_string === 'string' ? raw.code_string : undefined,
    codeNumber: typeof raw?.code_number === 'string' ? raw.code_number : undefined,
    codeComment: typeof raw?.code_comment === 'string' ? raw.code_comment : undefined,
    codeFunction: typeof raw?.code_function === 'string' ? raw.code_function : undefined,
    codeType: typeof raw?.code_type === 'string' ? raw.code_type : undefined,
    codeOperator: typeof raw?.code_operator === 'string' ? raw.code_operator : undefined,
    codePath: typeof raw?.code_path === 'string' ? raw.code_path : undefined,
  }
}

function clearWorkspaceThemeCatalog() {
  WORKSPACE_THEME_OPTIONS.splice(0, WORKSPACE_THEME_OPTIONS.length)
  BUILTIN_WORKSPACE_THEME_IDS.clear()
  THEME_LABELS.clear()
  for (const key of Object.keys(THEME_PALETTES)) {
    delete THEME_PALETTES[key]
  }
}

function registerWorkspaceTheme(raw: WorkspaceThemeWire, builtin: boolean, seen: Set<string>) {
  const id = normalizeThemeId(typeof raw.id === 'string' ? raw.id : '')
  const palette = themeBasePalette(raw.palette, !builtin)
  if (!id || !palette || seen.has(id) || (!builtin && BUILTIN_WORKSPACE_THEME_IDS.has(id))) {
    return
  }
  const label = typeof raw.name === 'string' && raw.name.trim() ? raw.name.trim() : formatThemeId(id)
  seen.add(id)
  if (builtin) {
    BUILTIN_WORKSPACE_THEME_IDS.add(id)
  }
  WORKSPACE_THEME_OPTIONS.push({ id, label })
  THEME_LABELS.set(id, label)
  THEME_PALETTES[id] = palette
}

export function setWorkspaceThemeCatalog(theme: WorkspaceThemeCatalogWire | null | undefined) {
  clearWorkspaceThemeCatalog()
  defaultWorkspaceThemeId = normalizeThemeId(theme?.default_theme_id)

  const seen = new Set<string>()
  for (const raw of theme?.builtin_themes ?? []) {
    registerWorkspaceTheme(raw as WorkspaceThemeWire, true, seen)
  }
  for (const raw of theme?.custom_themes ?? []) {
    registerWorkspaceTheme(raw as WorkspaceThemeWire, false, seen)
  }
}

export function workspaceThemeDefaultId(): string {
  return defaultWorkspaceThemeId
}

function formatThemeId(themeId: string): string {
  return themeId
    .split('-')
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ')
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

function relativeLuminance(hex: string): number | null {
  const rgb = parseHex(hex)
  if (!rgb) {
    return null
  }

  const [red, green, blue] = rgb.map((channel) => {
    const value = channel / 255
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue
}

function contrastRatio(left: string, right: string): number {
  const leftLuminance = relativeLuminance(left)
  const rightLuminance = relativeLuminance(right)
  if (leftLuminance === null || rightLuminance === null) {
    return 1
  }

  return (Math.max(leftLuminance, rightLuminance) + 0.05) / (Math.min(leftLuminance, rightLuminance) + 0.05)
}

function resolveAccentText(accentColors: string[]): string {
  const candidates = ['#020617', '#F8FAFC']
  return candidates.reduce((best, candidate) => {
    const candidateContrast = Math.min(...accentColors.map((accent) => contrastRatio(candidate, accent)))
    const bestContrast = Math.min(...accentColors.map((accent) => contrastRatio(best, accent)))
    return candidateContrast > bestContrast ? candidate : best
  })
}

function completePalette(base: WorkspaceThemeBasePalette): WorkspaceThemePalette {
  const lightBackground = isLightHex(base.background)
  const accentAnchor = lightBackground ? '#000000' : '#ffffff'
  const textInverse = lightBackground ? '#0F172A' : '#F8FAFC'
  const success = base.success
  const info = mixHex(base.primary, textInverse, lightBackground ? 0.08 : 0.12)
  const accentHover = mixHex(base.primary, accentAnchor, lightBackground ? 0.16 : 0.2)
  const accentActive = mixHex(base.primary, accentAnchor, lightBackground ? 0.28 : 0.32)
  const accentText = resolveAccentText([base.primary, accentHover, accentActive])

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
    accentHover,
    accentActive,
    accentText,
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
    codeBackground: base.codeBackground ?? mixHex(base.background, accentAnchor, lightBackground ? 0.03 : 0.08),
    codeText: base.codeText ?? base.text,
    codeKeyword: base.codeKeyword ?? mixHex(base.primary, base.warning, 0.32),
    codeString: base.codeString ?? base.warning,
    codeNumber: base.codeNumber ?? mixHex(base.warning, base.primary, 0.25),
    codeComment: base.codeComment ?? mixHex(base.textMuted, base.background, lightBackground ? 0.18 : 0.2),
    codeFunction: base.codeFunction ?? base.primary,
    codeType: base.codeType ?? mixHex(base.primary, accentAnchor, lightBackground ? 0.1 : 0.22),
    codeOperator: base.codeOperator ?? mixHex(base.textMuted, base.primary, 0.24),
    codePath: base.codePath ?? mixHex(base.primary, base.text, 0.24),
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
    [`${prefix}-code-bg`]: palette.codeBackground,
    [`${prefix}-code-text`]: palette.codeText,
    [`${prefix}-code-keyword`]: palette.codeKeyword,
    [`${prefix}-code-string`]: palette.codeString,
    [`${prefix}-code-number`]: palette.codeNumber,
    [`${prefix}-code-comment`]: palette.codeComment,
    [`${prefix}-code-function`]: palette.codeFunction,
    [`${prefix}-code-type`]: palette.codeType,
    [`${prefix}-code-operator`]: palette.codeOperator,
    [`${prefix}-code-path`]: palette.codePath,
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
  '--app-code-bg',
  '--app-code-text',
  '--app-code-keyword',
  '--app-code-string',
  '--app-code-number',
  '--app-code-comment',
  '--app-code-function',
  '--app-code-type',
  '--app-code-operator',
  '--app-code-path',
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
    '--app-code-bg': palette.codeBackground,
    '--app-code-text': palette.codeText,
    '--app-code-keyword': palette.codeKeyword,
    '--app-code-string': palette.codeString,
    '--app-code-number': palette.codeNumber,
    '--app-code-comment': palette.codeComment,
    '--app-code-function': palette.codeFunction,
    '--app-code-type': palette.codeType,
    '--app-code-operator': palette.codeOperator,
    '--app-code-path': palette.codePath,
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
