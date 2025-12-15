/**
 * Codebase Index - Pre-computation layer for batched exploration
 *
 * This module analyzes a codebase BEFORE spawning explore subagents,
 * providing them with:
 * - File tree with token estimates
 * - Symbol index (function/class names per file)
 * - Import graph (what files import what)
 * - Entry points (main files)
 *
 * This runs in <100ms for most codebases and eliminates the need
 * for multiple LLM inference cycles during exploration.
 */

import path from "path"
import { Ripgrep } from "../file/ripgrep"
import { Token } from "./token"
import { Log } from "./log"

const log = Log.create({ service: "codebase-index" })

export namespace CodebaseIndex {
  // ============================================
  // Types
  // ============================================

  export interface FileEstimate {
    path: string
    relativePath: string
    lines: number
    estimatedTokens: number
    extension: string
    isEntryPoint: boolean
  }

  export interface SectionAnalysis {
    rootPath: string
    fileCount: number
    totalLines: number
    estimatedTokens: number
    files: FileEstimate[]
    symbols: Map<string, string[]> // path → symbol names
    importGraph: Map<string, string[]> // path → imported paths
    entryPoints: string[]
    analysisTimeMs: number
  }

  // ============================================
  // Constants
  // ============================================

  const ENTRY_PATTERNS = [
    /^main\.(ts|tsx|js|jsx|rs|go|py|c|cpp|java)$/,
    /^index\.(ts|tsx|js|jsx)$/,
    /^mod\.rs$/,
    /^__main__\.py$/,
    /^app\.(ts|tsx|js|jsx|py)$/,
    /^server\.(ts|tsx|js|jsx)$/,
    /^cli\.(ts|tsx|js|jsx)$/,
    /^run\.(ts|tsx|js|jsx|py)$/,
  ]

  const IMPORT_PATTERNS: Record<string, RegExp[]> = {
    // TypeScript/JavaScript
    ".ts": [
      /import\s+.*?from\s+["']([^"']+)["']/g,
      /import\s*\(\s*["']([^"']+)["']\s*\)/g,
      /require\s*\(\s*["']([^"']+)["']\s*\)/g,
      /export\s+.*?from\s+["']([^"']+)["']/g,
    ],
    ".tsx": [
      /import\s+.*?from\s+["']([^"']+)["']/g,
      /import\s*\(\s*["']([^"']+)["']\s*\)/g,
      /require\s*\(\s*["']([^"']+)["']\s*\)/g,
    ],
    ".js": [
      /import\s+.*?from\s+["']([^"']+)["']/g,
      /require\s*\(\s*["']([^"']+)["']\s*\)/g,
    ],
    ".jsx": [
      /import\s+.*?from\s+["']([^"']+)["']/g,
      /require\s*\(\s*["']([^"']+)["']\s*\)/g,
    ],
    // Python
    ".py": [/from\s+(\S+)\s+import/g, /import\s+(\S+)/g],
    // Rust
    ".rs": [/use\s+(\S+)/g, /mod\s+(\w+)/g],
    // Go
    ".go": [/import\s+(?:\(\s*)?["']([^"']+)["']/g],
  }

  const SYMBOL_PATTERNS: Record<string, RegExp[]> = {
    // TypeScript/JavaScript
    ".ts": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)/g,
      /(?:export\s+)?class\s+(\w+)/g,
      /(?:export\s+)?interface\s+(\w+)/g,
      /(?:export\s+)?type\s+(\w+)\s*=/g,
      /(?:export\s+)?const\s+(\w+)\s*=/g,
      /(?:export\s+)?namespace\s+(\w+)/g,
      /(?:export\s+)?enum\s+(\w+)/g,
    ],
    ".tsx": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)/g,
      /(?:export\s+)?class\s+(\w+)/g,
      /(?:export\s+)?interface\s+(\w+)/g,
      /(?:export\s+)?const\s+(\w+)\s*=/g,
    ],
    ".js": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)/g,
      /(?:export\s+)?class\s+(\w+)/g,
      /(?:export\s+)?const\s+(\w+)\s*=/g,
    ],
    ".jsx": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)/g,
      /(?:export\s+)?class\s+(\w+)/g,
      /(?:export\s+)?const\s+(\w+)\s*=/g,
    ],
    // Python
    ".py": [/^class\s+(\w+)/gm, /^def\s+(\w+)/gm, /^async\s+def\s+(\w+)/gm],
    // Rust
    ".rs": [
      /(?:pub\s+)?fn\s+(\w+)/g,
      /(?:pub\s+)?struct\s+(\w+)/g,
      /(?:pub\s+)?enum\s+(\w+)/g,
      /(?:pub\s+)?trait\s+(\w+)/g,
      /(?:pub\s+)?impl\s+(\w+)/g,
    ],
    // Go
    ".go": [/func\s+(?:\([^)]+\)\s+)?(\w+)/g, /type\s+(\w+)\s+struct/g, /type\s+(\w+)\s+interface/g],
  }

  // Skip these directories entirely
  const SKIP_DIRS = new Set([
    "node_modules",
    ".git",
    "dist",
    "build",
    ".next",
    "target",
    "vendor",
    "__pycache__",
    ".venv",
    "venv",
    "coverage",
    ".turbo",
    ".cache",
  ])

  // Skip these file patterns
  const SKIP_FILES = [
    /\.min\.(js|css)$/,
    /\.map$/,
    /\.lock$/,
    /package-lock\.json$/,
    /yarn\.lock$/,
    /bun\.lockb$/,
    /\.d\.ts$/,
    /\.test\.(ts|tsx|js|jsx)$/,
    /\.spec\.(ts|tsx|js|jsx)$/,
    /__tests__/,
  ]

  // ============================================
  // Main Analysis Function
  // ============================================

  export async function analyze(cwd: string, options?: { maxFiles?: number }): Promise<SectionAnalysis> {
    const startTime = Date.now()
    const maxFiles = options?.maxFiles ?? 1000

    log.info("starting codebase analysis", { cwd, maxFiles })

    // Collect all files via ripgrep
    const allFiles: string[] = []
    for await (const file of Ripgrep.files({ cwd })) {
      // Skip directories we don't care about
      const parts = file.split(path.sep)
      if (parts.some((p) => SKIP_DIRS.has(p))) continue

      // Skip files we don't care about
      if (SKIP_FILES.some((pattern) => pattern.test(file))) continue

      allFiles.push(file)
      if (allFiles.length >= maxFiles) break
    }

    log.info("collected files", { count: allFiles.length })

    // Analyze files in parallel batches
    const BATCH_SIZE = 50
    const files: FileEstimate[] = []
    const symbols = new Map<string, string[]>()
    const importGraph = new Map<string, string[]>()
    const entryPoints: string[] = []

    for (let i = 0; i < allFiles.length; i += BATCH_SIZE) {
      const batch = allFiles.slice(i, i + BATCH_SIZE)
      const results = await Promise.all(
        batch.map(async (relativePath) => {
          const fullPath = path.join(cwd, relativePath)
          const ext = path.extname(relativePath)
          const basename = path.basename(relativePath)

          try {
            const content = await Bun.file(fullPath).text()
            const lines = content.split("\n").length
            const estimatedTokens = Token.estimate(content)
            const isEntryPoint = ENTRY_PATTERNS.some((p) => p.test(basename))

            // Extract symbols
            const fileSymbols = extractSymbols(content, ext)
            if (fileSymbols.length > 0) {
              symbols.set(relativePath, fileSymbols)
            }

            // Extract imports
            const fileImports = extractImports(content, ext, relativePath, cwd)
            if (fileImports.length > 0) {
              importGraph.set(relativePath, fileImports)
            }

            if (isEntryPoint) {
              entryPoints.push(relativePath)
            }

            return {
              path: fullPath,
              relativePath,
              lines,
              estimatedTokens,
              extension: ext,
              isEntryPoint,
            } satisfies FileEstimate
          } catch {
            // Binary or unreadable file
            return null
          }
        }),
      )

      files.push(...(results.filter(Boolean) as FileEstimate[]))
    }

    // Calculate totals
    const totalLines = files.reduce((sum, f) => sum + f.lines, 0)
    const estimatedTokens = files.reduce((sum, f) => sum + f.estimatedTokens, 0)
    const analysisTimeMs = Date.now() - startTime

    log.info("analysis complete", {
      fileCount: files.length,
      totalLines,
      estimatedTokens,
      entryPoints: entryPoints.length,
      symbolCount: symbols.size,
      importGraphSize: importGraph.size,
      timeMs: analysisTimeMs,
    })

    return {
      rootPath: cwd,
      fileCount: files.length,
      totalLines,
      estimatedTokens,
      files,
      symbols,
      importGraph,
      entryPoints,
      analysisTimeMs,
    }
  }

  // ============================================
  // Symbol Extraction
  // ============================================

  function extractSymbols(content: string, ext: string): string[] {
    const patterns = SYMBOL_PATTERNS[ext]
    if (!patterns) return []

    const symbols = new Set<string>()
    for (const pattern of patterns) {
      // Reset regex state
      pattern.lastIndex = 0
      let match
      while ((match = pattern.exec(content)) !== null) {
        if (match[1]) {
          symbols.add(match[1])
        }
      }
    }

    return Array.from(symbols)
  }

  // ============================================
  // Import Graph Extraction
  // ============================================

  function extractImports(content: string, ext: string, currentFile: string, cwd: string): string[] {
    const patterns = IMPORT_PATTERNS[ext]
    if (!patterns) return []

    const imports = new Set<string>()
    const currentDir = path.dirname(currentFile)

    for (const pattern of patterns) {
      pattern.lastIndex = 0
      let match
      while ((match = pattern.exec(content)) !== null) {
        const importPath = match[1]
        if (!importPath) continue

        // Skip node_modules imports
        if (!importPath.startsWith(".") && !importPath.startsWith("/")) {
          continue
        }

        // Resolve relative imports
        let resolved = importPath
        if (importPath.startsWith(".")) {
          resolved = path.join(currentDir, importPath)
          // Normalize and remove leading ./
          resolved = path.normalize(resolved)
        }

        // Try to resolve to an actual file
        const possibleExtensions = [".ts", ".tsx", ".js", ".jsx", "/index.ts", "/index.tsx", "/index.js"]
        for (const tryExt of ["", ...possibleExtensions]) {
          const tryPath = resolved + tryExt
          // Just add the resolved path - we'll validate later
          if (!tryPath.includes("node_modules")) {
            imports.add(tryPath)
            break
          }
        }
      }
    }

    return Array.from(imports)
  }

  // ============================================
  // Subagent Planning
  // ============================================

  export interface SubagentPlan {
    id: string
    section: string[] // assigned file paths
    tokenBudget: number
    estimatedTokens: number
    entryPoints: string[]
    oversizedFiles: string[] // files that exceed budget (should use symbols-only)
  }

  export function planSubagents(
    analysis: SectionAnalysis,
    options?: {
      targetTokensPerAgent?: number
      maxAgents?: number
    },
  ): SubagentPlan[] {
    const targetTokens = options?.targetTokensPerAgent ?? 50000
    const maxAgents = options?.maxAgents ?? 4

    // If total tokens fit in one agent, return single plan
    if (analysis.estimatedTokens <= targetTokens) {
      return [
        {
          id: "explore-0",
          section: analysis.files.map((f) => f.relativePath),
          tokenBudget: targetTokens,
          estimatedTokens: analysis.estimatedTokens,
          entryPoints: analysis.entryPoints,
          oversizedFiles: [],
        },
      ]
    }

    // Separate oversized files (> budget) - these need symbols-only reading
    const oversizedFiles: FileEstimate[] = []
    const normalFiles: FileEstimate[] = []
    for (const file of analysis.files) {
      if (file.estimatedTokens > targetTokens) {
        oversizedFiles.push(file)
      } else {
        normalFiles.push(file)
      }
    }

    // Group files by directory for locality
    const dirGroups = new Map<string, FileEstimate[]>()
    for (const file of normalFiles) {
      const dir = path.dirname(file.relativePath)
      const group = dirGroups.get(dir) || []
      group.push(file)
      dirGroups.set(dir, group)
    }

    // Sort directories by total tokens (largest first)
    const sortedDirs = Array.from(dirGroups.entries()).sort((a, b) => {
      const tokensA = a[1].reduce((sum, f) => sum + f.estimatedTokens, 0)
      const tokensB = b[1].reduce((sum, f) => sum + f.estimatedTokens, 0)
      return tokensB - tokensA
    })

    // Bin-pack directories into agents
    const plans: SubagentPlan[] = []
    let currentPlan: SubagentPlan = {
      id: `explore-${plans.length}`,
      section: [],
      tokenBudget: targetTokens,
      estimatedTokens: 0,
      entryPoints: [],
      oversizedFiles: [],
    }

    for (const [_dir, files] of sortedDirs) {
      const dirTokens = files.reduce((sum, f) => sum + f.estimatedTokens, 0)

      // If this directory alone exceeds budget, split it
      if (dirTokens > targetTokens) {
        // Add files one by one
        for (const file of files) {
          if (currentPlan.estimatedTokens + file.estimatedTokens > targetTokens && currentPlan.section.length > 0) {
            plans.push(currentPlan)
            if (plans.length >= maxAgents) break
            currentPlan = {
              id: `explore-${plans.length}`,
              section: [],
              tokenBudget: targetTokens,
              estimatedTokens: 0,
              entryPoints: [],
              oversizedFiles: [],
            }
          }
          currentPlan.section.push(file.relativePath)
          currentPlan.estimatedTokens += file.estimatedTokens
          if (file.isEntryPoint) {
            currentPlan.entryPoints.push(file.relativePath)
          }
        }
      } else {
        // Try to fit entire directory
        if (currentPlan.estimatedTokens + dirTokens > targetTokens && currentPlan.section.length > 0) {
          plans.push(currentPlan)
          if (plans.length >= maxAgents) break
          currentPlan = {
            id: `explore-${plans.length}`,
            section: [],
            tokenBudget: targetTokens,
            estimatedTokens: 0,
            entryPoints: [],
            oversizedFiles: [],
          }
        }
        for (const file of files) {
          currentPlan.section.push(file.relativePath)
          currentPlan.estimatedTokens += file.estimatedTokens
          if (file.isEntryPoint) {
            currentPlan.entryPoints.push(file.relativePath)
          }
        }
      }

      if (plans.length >= maxAgents) break
    }

    // Don't forget the last plan
    if (currentPlan.section.length > 0 && plans.length < maxAgents) {
      plans.push(currentPlan)
    }

    // Distribute oversized files across plans (they'll use symbols-only)
    // Each oversized file counts as ~500 tokens (just for signatures)
    const SYMBOLS_ONLY_ESTIMATE = 500
    for (const file of oversizedFiles) {
      // Find the plan with most room
      const bestPlan = plans.reduce((best, plan) => {
        const bestRoom = best.tokenBudget - best.estimatedTokens
        const planRoom = plan.tokenBudget - plan.estimatedTokens
        return planRoom > bestRoom ? plan : best
      }, plans[0])

      if (bestPlan) {
        bestPlan.section.push(file.relativePath)
        bestPlan.oversizedFiles.push(file.relativePath)
        bestPlan.estimatedTokens += SYMBOLS_ONLY_ESTIMATE // Only count symbols estimate
        if (file.isEntryPoint) {
          bestPlan.entryPoints.push(file.relativePath)
        }
      }
    }

    return plans
  }

  // ============================================
  // Formatting for Agent Context
  // ============================================

  export function formatForAgent(analysis: SectionAnalysis, plan?: SubagentPlan): string {
    const sections: string[] = []

    // Header
    sections.push(`## Codebase Analysis (Pre-computed in ${analysis.analysisTimeMs}ms)`)
    sections.push("")

    // Summary
    sections.push(`### Summary`)
    sections.push(`- **Files:** ${analysis.fileCount}`)
    sections.push(`- **Total Lines:** ${analysis.totalLines.toLocaleString()}`)
    sections.push(`- **Estimated Tokens:** ${analysis.estimatedTokens.toLocaleString()}`)
    if (plan) {
      sections.push(`- **Your Budget:** ${plan.tokenBudget.toLocaleString()} tokens`)
      sections.push(`- **Your Section:** ${plan.section.length} files (~${plan.estimatedTokens.toLocaleString()} tokens)`)
      if (plan.oversizedFiles.length > 0) {
        sections.push(`- **Oversized Files:** ${plan.oversizedFiles.length} (use readSymbolsOnly for these)`)
      }
    }
    sections.push("")

    // Entry Points
    if (analysis.entryPoints.length > 0) {
      sections.push(`### Entry Points`)
      for (const entry of analysis.entryPoints.slice(0, 10)) {
        sections.push(`- ${entry}`)
      }
      sections.push("")
    }

    // File Tree (grouped by directory)
    sections.push(`### File Structure`)
    const filesByDir = new Map<string, FileEstimate[]>()
    const filesToShow = plan ? analysis.files.filter((f) => plan.section.includes(f.relativePath)) : analysis.files

    for (const file of filesToShow.slice(0, 100)) {
      const dir = path.dirname(file.relativePath)
      const group = filesByDir.get(dir) || []
      group.push(file)
      filesByDir.set(dir, group)
    }

    for (const [dir, files] of filesByDir) {
      sections.push(`\n**${dir || "."}/**`)
      for (const file of files) {
        const symbols = analysis.symbols.get(file.relativePath)
        const symbolStr = symbols ? ` [${symbols.slice(0, 5).join(", ")}${symbols.length > 5 ? "..." : ""}]` : ""
        sections.push(`- ${path.basename(file.relativePath)} (~${file.estimatedTokens} tokens)${symbolStr}`)
      }
    }

    if (filesToShow.length > 100) {
      sections.push(`\n... and ${filesToShow.length - 100} more files`)
    }
    sections.push("")

    // Symbol Index (top symbols)
    sections.push(`### Symbol Index (Top Files)`)
    const symbolEntries = Array.from(analysis.symbols.entries())
      .filter(([p]) => !plan || plan.section.includes(p))
      .sort((a, b) => b[1].length - a[1].length)
      .slice(0, 20)

    for (const [filePath, syms] of symbolEntries) {
      sections.push(`- **${filePath}**: ${syms.slice(0, 10).join(", ")}${syms.length > 10 ? "..." : ""}`)
    }
    sections.push("")

    // Import Graph (key connections)
    const importEntries = Array.from(analysis.importGraph.entries())
      .filter(([p]) => !plan || plan.section.includes(p))
      .slice(0, 15)

    if (importEntries.length > 0) {
      sections.push(`### Import Graph (Key Dependencies)`)
      for (const [filePath, imports] of importEntries) {
        sections.push(`- **${filePath}** imports: ${imports.slice(0, 5).join(", ")}${imports.length > 5 ? "..." : ""}`)
      }
      sections.push("")
    }

    return sections.join("\n")
  }
}
