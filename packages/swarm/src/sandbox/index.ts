import { SandboxManager, type SandboxRuntimeConfig } from "@anthropic-ai/sandbox-runtime"
import { Config } from "@/config/config"
import { Instance } from "@/project/instance"
import { Log } from "@/util/log"
import { Permission } from "@/permission"
import fs from "fs/promises"

const log = Log.create({ service: "sandbox" })

// Track spawned socat processes for socket bridges
const socketBridgeProcesses: Map<string, Bun.Subprocess> = new Map()

async function startSocketBridges(bridges: Record<string, string>): Promise<void> {
  for (const [socketPath, tcpEndpoint] of Object.entries(bridges)) {
    // Clean up stale socket file if exists
    await fs.unlink(socketPath).catch(() => {})

    // Spawn socat: UNIX-LISTEN:socket,fork TCP:endpoint
    const proc = Bun.spawn(["socat", `UNIX-LISTEN:${socketPath},fork`, `TCP:${tcpEndpoint}`], {
      stdout: "ignore",
      stderr: "pipe",
    })

    // Wait a moment and check if process started successfully
    await new Promise((r) => setTimeout(r, 100))

    if (proc.exitCode !== null) {
      const stderr = await new Response(proc.stderr).text()
      log.error("socket bridge failed to start", { socket: socketPath, endpoint: tcpEndpoint, exitCode: proc.exitCode, stderr })
      continue
    }

    socketBridgeProcesses.set(socketPath, proc)
    log.info("started socket bridge", { socket: socketPath, endpoint: tcpEndpoint, pid: proc.pid })
  }
}

async function stopSocketBridges(): Promise<void> {
  for (const [socketPath, proc] of socketBridgeProcesses) {
    proc.kill()
    await fs.unlink(socketPath).catch(() => {})
    log.info("stopped socket bridge", { socket: socketPath })
  }
  socketBridgeProcesses.clear()
}

export namespace Sandbox {
  let initialized = false
  let supported = true
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
    const config = await Config.get()
    if (!config.sandbox?.enabled) {
      log.info("sandbox disabled by config")
      return false
    }

    const platform = process.platform === "darwin" ? "macos" : process.platform === "linux" ? "linux" : "unsupported"

    if (!SandboxManager.isSupportedPlatform(platform as any)) {
      log.warn("sandbox not supported on this platform", { platform })
      supported = false
      return false
    }

    if (!SandboxManager.checkDependencies()) {
      log.warn("sandbox dependencies not available")
      supported = false
      return false
    }

    const networkConfig = config.sandbox.network ?? { allowedDomains: [], deniedDomains: [] }
    const filesystemConfig = config.sandbox.filesystem ?? {
      denyRead: ["~/.ssh", "~/.gnupg"],
      allowWrite: ["."],
      denyWrite: [],
    }

    const runtimeConfig: SandboxRuntimeConfig = {
      network: {
        allowedDomains: networkConfig.allowedDomains,
        deniedDomains: networkConfig.deniedDomains,
        allowUnixSockets: networkConfig.allowUnixSockets,
        allowLocalBinding: networkConfig.allowLocalBinding,
      },
      filesystem: {
        denyRead: filesystemConfig.denyRead,
        allowWrite: filesystemConfig.allowWrite,
        denyWrite: filesystemConfig.denyWrite,
      },
      enableWeakerNestedSandbox: config.sandbox.enableWeakerNestedSandbox,
    }

    try {
      await SandboxManager.initialize(
        runtimeConfig,
        async ({ host, port }) => {
          // Ask permission for network access to unknown hosts
          if (!currentSessionID || !currentMessageID) {
            log.warn("no session context for network permission")
            return false
          }

          try {
            await Permission.ask({
              type: "network",
              pattern: host,
              sessionID: currentSessionID,
              messageID: currentMessageID,
              callID: currentCallID,
              title: `Network access to ${host}${port ? `:${port}` : ""}`,
              metadata: { host, port },
            })
            return true
          } catch {
            return false
          }
        },
        false, // don't enable log monitor
      )

      initialized = true
      log.info("sandbox initialized", { platform })

      // Start socket bridges if configured
      const socketBridges = config.sandbox?.network?.socketBridges
      if (socketBridges && Object.keys(socketBridges).length > 0) {
        await startSocketBridges(socketBridges)
      }

      return true
    } catch (err) {
      log.error("failed to initialize sandbox", { error: err })
      supported = false
      return false
    }
  }

  export function isEnabled(): boolean {
    return initialized && SandboxManager.isSandboxingEnabled()
  }

  export function isSupported(): boolean {
    return supported
  }

  export async function wrapCommand(command: string): Promise<string> {
    if (!isEnabled()) return command

    try {
      const wrapped = await SandboxManager.wrapWithSandbox(command)
      log.info("wrapped command with sandbox", {
        originalLength: command.length,
        wrappedLength: wrapped.length,
      })
      return wrapped
    } catch (err) {
      log.error("failed to wrap command with sandbox", { error: err })
      return command
    }
  }

  export function annotateStderr(command: string, stderr: string): string {
    if (!isEnabled()) return stderr
    return SandboxManager.annotateStderrWithSandboxFailures(command, stderr)
  }

  export async function reset(): Promise<void> {
    // Stop socket bridges first
    await stopSocketBridges()

    if (initialized) {
      try {
        await SandboxManager.reset()
        initialized = false
        log.info("sandbox reset")
      } catch (err) {
        log.error("failed to reset sandbox", { error: err })
      }
    }
  }

  /**
   * Re-initialize sandbox with fresh config.
   * Call this after config changes to apply new sandbox settings.
   */
  export async function reinitialize(): Promise<boolean> {
    log.info("reinitializing sandbox")
    await reset()
    // Reset supported flag so we can retry
    supported = true
    return initialize()
  }

  export type SandboxStatus = {
    enabled: boolean
    initialized: boolean
    supported: boolean
    platform: "macos" | "linux" | "unsupported"
  }

  /**
   * Get current sandbox status for API/UI
   */
  export function status(): SandboxStatus {
    const platform = process.platform === "darwin" ? "macos" : process.platform === "linux" ? "linux" : "unsupported"
    return {
      enabled: isEnabled(),
      initialized,
      supported,
      platform: platform as SandboxStatus["platform"],
    }
  }

  export function getConfig() {
    return SandboxManager.getConfig()
  }

  export function getGlobPatternWarnings(): string[] {
    if (!isEnabled()) return []
    return SandboxManager.getLinuxGlobPatternWarnings()
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
