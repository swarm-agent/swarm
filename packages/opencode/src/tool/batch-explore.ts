/**
 * BatchExplore Tool - Parallel codebase exploration
 *
 * This tool replaces sequential read/grep/glob calls with a single batch request.
 * It executes all operations in parallel and returns results with token tracking.
 *
 * Usage by explore agent:
 * 1. Agent receives pre-computed codebase analysis (file tree, symbols, imports)
 * 2. Agent plans ALL reads upfront in one batch request
 * 3. Tool executes everything in parallel
 * 4. Agent may submit 1-2 more batches for gaps
 * 5. Agent synthesizes response
 *
 * This reduces LLM inference calls from 10-15 to 3-4.
 */

import { z } from "zod"
import { Tool } from "./tool"
import path from "path"
import { Instance } from "../project/instance"
import { Ripgrep } from "../file/ripgrep"
import { Token } from "../util/token"
import { Log } from "../util/log"

// Helper to get working directory (with cwd fallback for tests)
function getWorkingDirectory(): string {
  try {
    return Instance.directory
  } catch {
    // Fall back to cwd when Instance context not available (e.g., tests)
    return process.cwd()
  }
}

const log = Log.create({ service: "batch-explore" })

// ============================================
// Schema
// ============================================

const GrepRequestSchema = z.object({
  pattern: z.string().describe("Regex pattern to search for"),
  paths: z.array(z.string()).optional().describe("Limit search to these paths/directories"),
  maxMatches: z.number().optional().default(50).describe("Max matches per pattern (default 50)"),
})

const BatchExploreSchema = z.object({
  // File content requests
  readFiles: z
    .array(z.string())
    .optional()
    .describe("Files to read in full (subject to line limits)"),
  readSymbolsOnly: z
    .array(z.string())
    .optional()
    .describe("Large files to read signatures only (function/class names)"),

  // Search requests
  grepPatterns: z
    .array(GrepRequestSchema)
    .optional()
    .describe("Content search patterns to run in parallel"),
  globPatterns: z
    .array(z.string())
    .optional()
    .describe("File discovery patterns (e.g., 'src/**/*.ts')"),
})

type BatchExploreInput = z.infer<typeof BatchExploreSchema>

// ============================================
// Limits Configuration
// ============================================

interface ExplorationLimits {
  maxFileLines: number
  maxFileTokens: number
  totalContextBudget: number
}

const DEFAULT_LIMITS: ExplorationLimits = {
  maxFileLines: 500,
  maxFileTokens: 4000,
  totalContextBudget: 50000,
}

// ============================================
// Result Types
// ============================================

interface FileContent {
  content: string
  lines: number
  tokens: number
  wasTruncated: boolean
  symbolsOnly: boolean
}

interface GrepMatch {
  file: string
  line: number
  text: string
  match: string
}

interface BatchExploreResult {
  files: Map<string, FileContent>
  grepMatches: Map<string, GrepMatch[]>
  globResults: string[]
  tokensUsed: number
  tokensRemaining: number
  truncatedFiles: string[]
  errors: string[]
}

// ============================================
// Implementation
// ============================================

async function readFileWithLimits(
  filepath: string,
  limits: ExplorationLimits,
  symbolsOnly: boolean,
  cwd: string,
): Promise<FileContent | null> {
  const fullPath = path.isAbsolute(filepath)
    ? filepath
    : path.join(cwd, filepath)

  try {
    const file = Bun.file(fullPath)
    if (!(await file.exists())) {
      return null
    }

    const content = await file.text()
    const lines = content.split("\n")
    const estimatedTokens = Token.estimate(content)

    // If within limits and not symbols-only, return full content
    if (!symbolsOnly && lines.length <= limits.maxFileLines && estimatedTokens <= limits.maxFileTokens) {
      // Add line numbers like the Read tool
      const numbered = lines.map((line, i) => `${String(i + 1).padStart(5)}| ${line}`).join("\n")
      return {
        content: numbered,
        lines: lines.length,
        tokens: Token.estimate(numbered),
        wasTruncated: false,
        symbolsOnly: false,
      }
    }

    // Extract symbols for large files or symbols-only requests
    if (symbolsOnly || estimatedTokens > limits.maxFileTokens) {
      const symbols = extractSymbolsFromContent(content, path.extname(filepath))
      const symbolContent = symbols.length > 0
        ? `// Symbols extracted (${lines.length} lines, ~${estimatedTokens} tokens):\n${symbols.join("\n")}`
        : `// File too large: ${lines.length} lines, ~${estimatedTokens} tokens (no symbols extracted)`
      return {
        content: symbolContent,
        lines: lines.length,
        tokens: Token.estimate(symbolContent),
        wasTruncated: true,
        symbolsOnly: true,
      }
    }

    // Truncate with head + tail
    const headLines = Math.floor(limits.maxFileLines * 0.7)
    const tailLines = limits.maxFileLines - headLines
    const truncated = [
      ...lines.slice(0, headLines).map((line, i) => `${String(i + 1).padStart(5)}| ${line}`),
      `      | ... [${lines.length - headLines - tailLines} lines truncated] ...`,
      ...lines.slice(-tailLines).map((line, i) => `${String(lines.length - tailLines + i + 1).padStart(5)}| ${line}`),
    ].join("\n")

    return {
      content: truncated,
      lines: lines.length,
      tokens: Token.estimate(truncated),
      wasTruncated: true,
      symbolsOnly: false,
    }
  } catch (err) {
    log.error("failed to read file", { filepath, error: err })
    return null
  }
}

function extractSymbolsFromContent(content: string, ext: string): string[] {
  const patterns: Record<string, RegExp[]> = {
    ".ts": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\([^)]*\)/g,
      /(?:export\s+)?class\s+(\w+)(?:\s+extends\s+\w+)?(?:\s+implements\s+[\w,\s]+)?\s*\{/g,
      /(?:export\s+)?interface\s+(\w+)/g,
      /(?:export\s+)?type\s+(\w+)\s*=/g,
      /(?:export\s+)?namespace\s+(\w+)/g,
      /(?:export\s+)?enum\s+(\w+)/g,
    ],
    ".tsx": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\([^)]*\)/g,
      /(?:export\s+)?class\s+(\w+)/g,
      /(?:export\s+)?interface\s+(\w+)/g,
      /(?:export\s+)?const\s+(\w+)\s*=/g,
    ],
    ".js": [
      /(?:export\s+)?(?:async\s+)?function\s+(\w+)\s*\([^)]*\)/g,
      /(?:export\s+)?class\s+(\w+)/g,
    ],
    ".py": [
      /^class\s+(\w+)/gm,
      /^def\s+(\w+)\s*\([^)]*\)/gm,
      /^async\s+def\s+(\w+)/gm,
    ],
    ".rs": [
      /(?:pub\s+)?fn\s+(\w+)\s*(?:<[^>]*>)?\s*\([^)]*\)/g,
      /(?:pub\s+)?struct\s+(\w+)/g,
      /(?:pub\s+)?enum\s+(\w+)/g,
      /(?:pub\s+)?trait\s+(\w+)/g,
    ],
    ".go": [
      /func\s+(?:\([^)]+\)\s+)?(\w+)\s*\([^)]*\)/g,
      /type\s+(\w+)\s+struct/g,
      /type\s+(\w+)\s+interface/g,
    ],
  }

  // Also match .jsx to .js patterns
  const extPatterns = patterns[ext] || patterns[ext.replace("x", "")] || []
  if (extPatterns.length === 0) return []

  const symbols: string[] = []
  const lines = content.split("\n")

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    for (const pattern of extPatterns) {
      pattern.lastIndex = 0
      const match = pattern.exec(line)
      if (match && match[1]) {
        // Include the full line for context
        symbols.push(`${String(i + 1).padStart(5)}| ${line.trim()}`)
        break // Only match once per line
      }
    }
  }

  return symbols
}

async function executeGrep(
  pattern: string,
  paths: string[] | undefined,
  maxMatches: number,
  cwd: string,
): Promise<GrepMatch[]> {
  try {
    const results = await Ripgrep.search({
      cwd,
      pattern,
      glob: paths,
      limit: maxMatches,
    })

    return results.map((r) => ({
      file: r.path.text,
      line: r.line_number,
      text: r.lines.text.trim(),
      match: r.submatches[0]?.match.text || "",
    }))
  } catch (err) {
    log.error("grep failed", { pattern, error: err })
    return []
  }
}

async function executeGlob(pattern: string, cwd: string): Promise<string[]> {
  try {
    // Use ripgrep's file listing with glob patterns
    const results: string[] = []
    for await (const file of Ripgrep.files({ cwd, glob: [pattern] })) {
      results.push(file)
      if (results.length >= 100) break // Limit results
    }
    return results
  } catch (err) {
    log.error("glob failed", { pattern, error: err })
    return []
  }
}

async function batchExplore(
  input: BatchExploreInput,
  limits: ExplorationLimits,
  cwd?: string,
): Promise<BatchExploreResult> {
  const startTime = Date.now()
  const workingDir = cwd || getWorkingDirectory()

  const result: BatchExploreResult = {
    files: new Map(),
    grepMatches: new Map(),
    globResults: [],
    tokensUsed: 0,
    tokensRemaining: limits.totalContextBudget,
    truncatedFiles: [],
    errors: [],
  }

  // Execute all operations in parallel
  const [fileResults, symbolResults, grepResults, globResults] = await Promise.all([
    // Read files in parallel
    Promise.all(
      (input.readFiles || []).map(async (filepath) => {
        const content = await readFileWithLimits(filepath, limits, false, workingDir)
        return { filepath, content }
      }),
    ),
    // Read symbols-only files in parallel
    Promise.all(
      (input.readSymbolsOnly || []).map(async (filepath) => {
        const content = await readFileWithLimits(filepath, limits, true, workingDir)
        return { filepath, content }
      }),
    ),
    // Execute grep patterns in parallel
    Promise.all(
      (input.grepPatterns || []).map(async (req) => {
        const matches = await executeGrep(req.pattern, req.paths, req.maxMatches || 50, workingDir)
        return { pattern: req.pattern, matches }
      }),
    ),
    // Execute glob patterns in parallel
    Promise.all(
      (input.globPatterns || []).map(async (pattern) => {
        const files = await executeGlob(pattern, workingDir)
        return { pattern, files }
      }),
    ),
  ])

  // Process file results
  for (const { filepath, content } of [...fileResults, ...symbolResults]) {
    if (content) {
      result.files.set(filepath, content)
      result.tokensUsed += content.tokens
      if (content.wasTruncated) {
        result.truncatedFiles.push(filepath)
      }
    } else {
      result.errors.push(`File not found: ${filepath}`)
    }
  }

  // Process grep results
  for (const { pattern, matches } of grepResults) {
    result.grepMatches.set(pattern, matches)
    // Estimate tokens for grep output
    const grepTokens = matches.reduce((sum, m) => sum + Token.estimate(m.text), 0)
    result.tokensUsed += grepTokens
  }

  // Process glob results
  for (const { files } of globResults) {
    result.globResults.push(...files)
  }
  result.tokensUsed += Token.estimate(result.globResults.join("\n"))

  result.tokensRemaining = Math.max(0, limits.totalContextBudget - result.tokensUsed)

  const elapsed = Date.now() - startTime
  log.info("batch explore complete", {
    files: result.files.size,
    grepPatterns: result.grepMatches.size,
    globResults: result.globResults.length,
    tokensUsed: result.tokensUsed,
    timeMs: elapsed,
  })

  return result
}

function formatBatchResult(result: BatchExploreResult): string {
  const sections: string[] = []

  // Summary
  sections.push(`## Batch Explore Results`)
  sections.push(`- Files read: ${result.files.size}`)
  sections.push(`- Grep patterns: ${result.grepMatches.size}`)
  sections.push(`- Glob results: ${result.globResults.length}`)
  sections.push(`- Tokens used: ${result.tokensUsed.toLocaleString()}`)
  sections.push(`- Tokens remaining: ${result.tokensRemaining.toLocaleString()}`)
  if (result.truncatedFiles.length > 0) {
    sections.push(`- Truncated files: ${result.truncatedFiles.join(", ")}`)
  }
  if (result.errors.length > 0) {
    sections.push(`- Errors: ${result.errors.join(", ")}`)
  }
  sections.push("")

  // File contents
  if (result.files.size > 0) {
    sections.push(`### File Contents`)
    for (const [filepath, content] of result.files) {
      const marker = content.symbolsOnly ? " (symbols only)" : content.wasTruncated ? " (truncated)" : ""
      sections.push(`\n#### ${filepath}${marker}`)
      sections.push("```")
      sections.push(content.content)
      sections.push("```")
    }
    sections.push("")
  }

  // Grep results
  if (result.grepMatches.size > 0) {
    sections.push(`### Grep Results`)
    for (const [pattern, matches] of result.grepMatches) {
      sections.push(`\n#### Pattern: \`${pattern}\` (${matches.length} matches)`)
      for (const match of matches.slice(0, 20)) {
        sections.push(`- ${match.file}:${match.line}: ${match.text.slice(0, 100)}`)
      }
      if (matches.length > 20) {
        sections.push(`  ... and ${matches.length - 20} more`)
      }
    }
    sections.push("")
  }

  // Glob results
  if (result.globResults.length > 0) {
    sections.push(`### Glob Results (${result.globResults.length} files)`)
    for (const file of result.globResults.slice(0, 50)) {
      sections.push(`- ${file}`)
    }
    if (result.globResults.length > 50) {
      sections.push(`... and ${result.globResults.length - 50} more`)
    }
  }

  return sections.join("\n")
}

// ============================================
// Tool Definition
// ============================================

const DESCRIPTION = `Batch multiple codebase exploration operations into a single tool call.
Executes file reads, grep searches, and glob patterns in parallel.
Returns all results with token usage tracking.

## When to Use
- Use this tool for comprehensive codebase exploration
- Replaces sequential read/grep/glob calls with a single batch request
- Plan ALL reads upfront in one batch request

## Parameters
- readFiles: Array of file paths to read in full (subject to line limits)
- readSymbolsOnly: Array of file paths to return only function/class signatures
- grepPatterns: Array of {pattern, paths?, maxMatches?} for content search
- globPatterns: Array of glob patterns for file discovery

## Token Budget
- Your budget is shown in the result as tokensRemaining
- Use readSymbolsOnly for large files (>500 lines)
- Prioritize: entry points > core logic > utilities > tests

## Example
{
  "readFiles": ["src/index.ts", "src/auth/login.ts"],
  "readSymbolsOnly": ["src/utils/helpers.ts"],
  "grepPatterns": [
    {"pattern": "authenticate", "paths": ["src/"]},
    {"pattern": "import.*session", "maxMatches": 20}
  ],
  "globPatterns": ["src/**/*.test.ts"]
}`

export const BatchExploreTool = Tool.define<typeof BatchExploreSchema, { summary: string }>(
  "batch-explore",
  async () => ({
    description: DESCRIPTION,
    parameters: BatchExploreSchema,
    async execute(input, ctx) {
      const limits = DEFAULT_LIMITS

      ctx.metadata({
        title: "Batch exploring...",
        metadata: { summary: "Starting batch exploration" },
      })

      const result = await batchExplore(input, limits)

      ctx.metadata({
        title: `Explored ${result.files.size} files, ${result.grepMatches.size} patterns`,
        metadata: {
          summary: `${result.files.size} files, ${result.tokensUsed.toLocaleString()} tokens used`,
        },
      })

      return {
        title: `Batch explore: ${result.files.size} files, ${result.grepMatches.size} patterns`,
        metadata: {
          summary: `${result.files.size} files, ${result.tokensUsed.toLocaleString()} tokens used`,
        },
        output: formatBatchResult(result),
      }
    },
  }),
)
