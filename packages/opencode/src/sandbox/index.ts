import { Log } from "@/util/log"

const log = Log.create({ service: "sandbox" })

export namespace Sandbox {
  let currentSessionID: string | undefined
  let currentMessageID: string | undefined
  let currentCallID: string | undefined

  export function setContext(ctx: { sessionID: string; messageID: string; callID?: string }) {
    currentSessionID = ctx.sessionID
    currentMessageID = ctx.messageID
    currentCallID = ctx.callID
  }

  export function clearContext() {
    currentSessionID = undefined
    currentMessageID = undefined
    currentCallID = undefined
  }

  export async function initialize(): Promise<boolean> {
    // Sandbox disabled - Anthropic sandbox-runtime removed
    log.info("sandbox disabled (anthropic sandbox-runtime removed)")
    return false
  }

  export function isEnabled(): boolean {
    return false
  }

  export function isSupported(): boolean {
    return false
  }

  export async function wrapCommand(command: string): Promise<string> {
    // No sandboxing - return command as-is
    return command
  }

  export function annotateStderr(_command: string, stderr: string): string {
    // No sandbox annotations
    return stderr
  }

  // Hidden/obfuscation character detection
  const ZERO_WIDTH = [
    "\u200B", // zero width space
    "\u200C", // zero width non-joiner
    "\u200D", // zero width joiner
    "\uFEFF", // byte order mark / zero width no-break space
    "\u2060", // word joiner
    "\u180E", // mongolian vowel separator
    "\u200E", // left-to-right mark
    "\u200F", // right-to-left mark
  ]

  const BIDI_OVERRIDE = [
    "\u202A", // left-to-right embedding
    "\u202B", // right-to-left embedding
    "\u202C", // pop directional formatting
    "\u202D", // left-to-right override
    "\u202E", // right-to-left override (DANGEROUS - reverses text display)
    "\u2066", // left-to-right isolate
    "\u2067", // right-to-left isolate
    "\u2068", // first strong isolate
    "\u2069", // pop directional isolate
  ]

  // Common homoglyphs: Cyrillic/Greek that look like Latin
  const HOMOGLYPHS: Record<string, string> = {
    "\u0430": "a", // cyrillic а
    "\u0435": "e", // cyrillic е
    "\u043E": "o", // cyrillic о
    "\u0440": "p", // cyrillic р
    "\u0441": "c", // cyrillic с
    "\u0445": "x", // cyrillic х
    "\u0443": "y", // cyrillic у
    "\u0456": "i", // cyrillic і
    "\u0458": "j", // cyrillic ј
    "\u04BB": "h", // cyrillic һ
    "\u0391": "A", // greek Α
    "\u0392": "B", // greek Β
    "\u0395": "E", // greek Ε
    "\u0397": "H", // greek Η
    "\u0399": "I", // greek Ι
    "\u039A": "K", // greek Κ
    "\u039C": "M", // greek Μ
    "\u039D": "N", // greek Ν
    "\u039F": "O", // greek Ο
    "\u03A1": "P", // greek Ρ
    "\u03A4": "T", // greek Τ
    "\u03A7": "X", // greek Χ
    "\u03A5": "Y", // greek Υ
    "\u0417": "3", // cyrillic З (looks like 3)
  }

  export interface SanitizeResult {
    normalized: string
    original: string
    suspicious: boolean
    warnings: string[]
  }

  /**
   * Normalize a string by removing hidden characters and replacing homoglyphs.
   * Returns the normalized string plus warnings about what was found.
   */
  export function sanitize(input: string): SanitizeResult {
    const warnings: string[] = []
    let normalized = input

    // Check for zero-width characters
    for (const char of ZERO_WIDTH) {
      if (normalized.includes(char)) {
        const count = (normalized.match(new RegExp(char, "g")) || []).length
        warnings.push(`Found ${count} zero-width character(s) [U+${char.charCodeAt(0).toString(16).toUpperCase()}]`)
        normalized = normalized.replaceAll(char, "")
      }
    }

    // Check for BiDi override characters (very suspicious)
    for (const char of BIDI_OVERRIDE) {
      if (normalized.includes(char)) {
        const count = (normalized.match(new RegExp(char, "g")) || []).length
        warnings.push(
          `DANGER: Found ${count} BiDi override character(s) [U+${char.charCodeAt(0).toString(16).toUpperCase()}] - text may display differently than executed`,
        )
        normalized = normalized.replaceAll(char, "")
      }
    }

    // Check for homoglyphs
    const homoglyphsFound: string[] = []
    for (const [fake, real] of Object.entries(HOMOGLYPHS)) {
      if (normalized.includes(fake)) {
        homoglyphsFound.push(`${fake}→${real}`)
        normalized = normalized.replaceAll(fake, real)
      }
    }
    if (homoglyphsFound.length > 0) {
      warnings.push(`Found homoglyph(s): ${homoglyphsFound.join(", ")}`)
    }

    // Check for other control characters (C0/C1 except common ones)
    const controlChars: string[] = []
    for (let i = 0; i < normalized.length; i++) {
      const code = normalized.charCodeAt(i)
      // C0 control chars (except tab, newline, carriage return)
      if (code < 32 && code !== 9 && code !== 10 && code !== 13) {
        controlChars.push(`U+${code.toString(16).toUpperCase().padStart(4, "0")}`)
      }
      // C1 control chars
      if (code >= 0x80 && code <= 0x9f) {
        controlChars.push(`U+${code.toString(16).toUpperCase().padStart(4, "0")}`)
      }
    }
    if (controlChars.length > 0) {
      warnings.push(`Found control character(s): ${[...new Set(controlChars)].join(", ")}`)
      // Remove them
      normalized = normalized.replace(/[\x00-\x08\x0B\x0C\x0E-\x1F\x80-\x9F]/g, "")
    }

    // Check if normalized differs from original (beyond what we explicitly cleaned)
    const suspicious = warnings.length > 0

    return {
      normalized,
      original: input,
      suspicious,
      warnings,
    }
  }

  /**
   * Quick check if a string contains any suspicious obfuscation characters.
   * Faster than full sanitize() when you just need a boolean.
   */
  export function hasSuspiciousChars(input: string): boolean {
    // Quick regex check for common obfuscation
    const suspiciousPattern =
      /[\u200B-\u200F\u2060\u180E\uFEFF\u202A-\u202E\u2066-\u2069\u0430\u0435\u043E\u0440\u0441\u0445\u0443\u0456\u0458\u04BB\x00-\x08\x0B\x0C\x0E-\x1F\x80-\x9F]/
    return suspiciousPattern.test(input)
  }

  /**
   * Format warnings for display to user
   */
  export function formatSanitizeWarning(result: SanitizeResult): string {
    if (!result.suspicious) return ""
    return `⚠️ OBFUSCATION DETECTED:\n${result.warnings.map((w) => `  • ${w}`).join("\n")}\nNormalized: ${result.normalized}`
  }
}
