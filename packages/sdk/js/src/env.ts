/**
 * Environment Variable Loading
 * 
 * Load environment variables from .env files for SDK profiles.
 * Supports per-profile env files for secret isolation.
 * 
 * @example
 * ```typescript
 * import { loadEnvFile, loadEnvString } from "@opencode-ai/sdk"
 * 
 * // Load from file
 * const vars = await loadEnvFile(".env.production")
 * 
 * // Parse env string
 * const vars = loadEnvString(`
 *   API_KEY=secret123
 *   DEBUG=true
 * `)
 * ```
 */

import { readFile } from "fs/promises"
import { resolve, dirname } from "path"
import { existsSync } from "fs"

/**
 * Parse a .env file content string into key-value pairs
 * 
 * Supports:
 * - KEY=value
 * - KEY="quoted value"
 * - KEY='single quoted'
 * - # comments
 * - Empty lines
 * - Multi-line values with quotes
 */
export function loadEnvString(content: string): Record<string, string> {
  const result: Record<string, string> = {}
  const lines = content.split("\n")

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim()

    // Skip empty lines and comments
    if (!line || line.startsWith("#")) continue

    // Find the first = sign
    const eqIndex = line.indexOf("=")
    if (eqIndex === -1) continue

    const key = line.slice(0, eqIndex).trim()
    let value = line.slice(eqIndex + 1).trim()

    // Handle quoted values
    if ((value.startsWith('"') && value.endsWith('"')) ||
        (value.startsWith("'") && value.endsWith("'"))) {
      // Remove surrounding quotes
      value = value.slice(1, -1)
    } else if (value.startsWith('"') || value.startsWith("'")) {
      // Multi-line quoted value - find closing quote
      const quote = value[0]
      value = value.slice(1) // Remove opening quote

      // Look for closing quote in subsequent lines
      while (i + 1 < lines.length && !value.includes(quote)) {
        i++
        value += "\n" + lines[i]
      }

      // Remove closing quote and anything after
      const closeIndex = value.indexOf(quote)
      if (closeIndex !== -1) {
        value = value.slice(0, closeIndex)
      }
    }

    // Handle escape sequences in quoted values
    value = value
      .replace(/\\n/g, "\n")
      .replace(/\\r/g, "\r")
      .replace(/\\t/g, "\t")
      .replace(/\\\\/g, "\\")

    result[key] = value
  }

  return result
}

/**
 * Load environment variables from a .env file
 * 
 * @param filePath - Path to the .env file (absolute or relative to cwd)
 * @param options - Loading options
 * @returns Parsed environment variables
 * @throws If file doesn't exist and throwOnMissing is true
 * 
 * @example
 * ```typescript
 * // Load from specific file
 * const vars = await loadEnvFile(".env.production")
 * console.log(vars.API_KEY)
 * 
 * // Load with fallback
 * const vars = await loadEnvFile(".env.local", { throwOnMissing: false })
 * ```
 */
export async function loadEnvFile(
  filePath: string,
  options: {
    /** Throw error if file doesn't exist (default: true) */
    throwOnMissing?: boolean
    /** Base directory for relative paths (default: process.cwd()) */
    baseDir?: string
  } = {}
): Promise<Record<string, string>> {
  const { throwOnMissing = true, baseDir = process.cwd() } = options

  // Resolve path
  const resolvedPath = resolve(baseDir, filePath)

  // Check if file exists
  if (!existsSync(resolvedPath)) {
    if (throwOnMissing) {
      throw new Error(`Env file not found: ${resolvedPath}`)
    }
    return {}
  }

  // Read and parse
  const content = await readFile(resolvedPath, "utf-8")
  return loadEnvString(content)
}

/**
 * Load env file and inject into process.env
 * 
 * @param filePath - Path to the .env file
 * @param options - Loading options
 * @returns The loaded variables (also injected into process.env)
 * 
 * @example
 * ```typescript
 * // Load and inject
 * await injectEnvFile(".env")
 * console.log(process.env.API_KEY) // now available
 * ```
 */
export async function injectEnvFile(
  filePath: string,
  options: {
    /** Throw error if file doesn't exist (default: true) */
    throwOnMissing?: boolean
    /** Base directory for relative paths (default: process.cwd()) */
    baseDir?: string
    /** Override existing env vars (default: false) */
    override?: boolean
  } = {}
): Promise<Record<string, string>> {
  const { override = false, ...loadOptions } = options
  const vars = await loadEnvFile(filePath, loadOptions)

  for (const [key, value] of Object.entries(vars)) {
    if (override || !(key in process.env)) {
      process.env[key] = value
    }
  }

  return vars
}

/**
 * Create an env loader bound to a specific file
 * Useful for profile-specific env files
 * 
 * @param filePath - Path to the .env file
 * @returns Object with load() and inject() methods
 * 
 * @example
 * ```typescript
 * const profileEnv = createEnvLoader(".env.production")
 * 
 * // Load without injecting
 * const vars = await profileEnv.load()
 * 
 * // Or load and inject
 * await profileEnv.inject()
 * ```
 */
export function createEnvLoader(filePath: string, baseDir?: string) {
  return {
    /** Load env vars from file */
    load: (options?: { throwOnMissing?: boolean }) => 
      loadEnvFile(filePath, { ...options, baseDir }),
    
    /** Load and inject into process.env */
    inject: (options?: { throwOnMissing?: boolean; override?: boolean }) =>
      injectEnvFile(filePath, { ...options, baseDir }),
    
    /** Check if file exists */
    exists: () => {
      const resolvedPath = resolve(baseDir ?? process.cwd(), filePath)
      return existsSync(resolvedPath)
    },

    /** Get resolved path */
    path: () => resolve(baseDir ?? process.cwd(), filePath),
  }
}

/**
 * Resolve environment variable interpolation in strings
 * Supports ${VAR} and {env:VAR} syntax
 * 
 * @param str - String with env var references
 * @param env - Environment to use (default: process.env)
 * @returns String with variables resolved
 * 
 * @example
 * ```typescript
 * const url = resolveEnvVars("https://api.example.com?key=${API_KEY}")
 * const url2 = resolveEnvVars("https://api.example.com?key={env:API_KEY}")
 * ```
 */
export function resolveEnvVars(
  str: string,
  env: Record<string, string | undefined> = process.env
): string {
  // Support both ${VAR} and {env:VAR} syntax
  return str
    .replace(/\$\{([^}]+)\}/g, (_, varName) => env[varName] ?? "")
    .replace(/\{env:([^}]+)\}/g, (_, varName) => env[varName] ?? "")
}
