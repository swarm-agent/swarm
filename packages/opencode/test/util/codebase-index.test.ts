/**
 * Test: Codebase Index
 *
 * Validates the pre-computation layer for batched exploration.
 * Run with: bun test packages/opencode/test/util/codebase-index.test.ts
 */

import { describe, it, expect, beforeAll } from "bun:test"
import { CodebaseIndex } from "../../src/util/codebase-index"
import path from "path"

describe("CodebaseIndex", () => {
  const testCwd = path.resolve(__dirname, "../../../..") // swarm-cli root

  describe("analyze", () => {
    it("should analyze the codebase quickly (<500ms)", async () => {
      const startTime = Date.now()
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 200 })
      const elapsed = Date.now() - startTime

      console.log(`\n--- Analysis Results ---`)
      console.log(`Time: ${analysis.analysisTimeMs}ms (total: ${elapsed}ms)`)
      console.log(`Files: ${analysis.fileCount}`)
      console.log(`Lines: ${analysis.totalLines.toLocaleString()}`)
      console.log(`Tokens: ${analysis.estimatedTokens.toLocaleString()}`)
      console.log(`Entry Points: ${analysis.entryPoints.join(", ")}`)
      console.log(`Symbols extracted: ${analysis.symbols.size} files`)
      console.log(`Import graph: ${analysis.importGraph.size} files`)

      expect(elapsed).toBeLessThan(500)
      expect(analysis.fileCount).toBeGreaterThan(0)
      expect(analysis.estimatedTokens).toBeGreaterThan(0)
    })

    it("should identify entry points", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 200 })

      console.log(`\n--- Entry Points ---`)
      for (const entry of analysis.entryPoints) {
        console.log(`  - ${entry}`)
      }

      // Should find at least one index.ts
      const hasIndex = analysis.entryPoints.some((e) => e.includes("index.ts"))
      expect(hasIndex).toBe(true)
    })

    it("should extract symbols from TypeScript files", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 200 })

      console.log(`\n--- Sample Symbols ---`)
      let count = 0
      for (const [file, symbols] of analysis.symbols) {
        if (count++ >= 5) break
        console.log(`  ${file}: ${symbols.slice(0, 5).join(", ")}`)
      }

      // Should find symbols in at least some files
      expect(analysis.symbols.size).toBeGreaterThan(5)
    })

    it("should build import graph", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 200 })

      console.log(`\n--- Sample Import Graph ---`)
      let count = 0
      for (const [file, imports] of analysis.importGraph) {
        if (count++ >= 5) break
        console.log(`  ${file} imports:`)
        for (const imp of imports.slice(0, 3)) {
          console.log(`    - ${imp}`)
        }
      }

      // Should build import relationships
      expect(analysis.importGraph.size).toBeGreaterThan(5)
    })
  })

  describe("planSubagents", () => {
    it("should split large codebases into multiple agents", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 500 })

      // Plan with small token budget to force splitting
      const plans = CodebaseIndex.planSubagents(analysis, {
        targetTokensPerAgent: 20000,
        maxAgents: 4,
      })

      console.log(`\n--- Subagent Plans ---`)
      for (const plan of plans) {
        console.log(`\n${plan.id}:`)
        console.log(`  Files: ${plan.section.length}`)
        console.log(`  Tokens: ${plan.estimatedTokens.toLocaleString()} / ${plan.tokenBudget.toLocaleString()}`)
        console.log(`  Entry Points: ${plan.entryPoints.join(", ") || "none"}`)
        console.log(`  Oversized (symbols-only): ${plan.oversizedFiles.length}`)
        console.log(`  Sample files: ${plan.section.slice(0, 3).join(", ")}`)
      }

      // Should create multiple plans if codebase is large enough
      if (analysis.estimatedTokens > 20000) {
        expect(plans.length).toBeGreaterThan(1)
      }

      // Each plan should respect token budget (approximately)
      // Note: oversized files are counted at symbols-only estimate (~500 tokens)
      for (const plan of plans) {
        // Allow some overflow since we pack by directory
        expect(plan.estimatedTokens).toBeLessThan(plan.tokenBudget * 2)
      }

      // Should identify oversized files
      const totalOversized = plans.reduce((sum, p) => sum + p.oversizedFiles.length, 0)
      console.log(`\nTotal oversized files: ${totalOversized}`)
    })

    it("should keep related files together", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 200 })
      const plans = CodebaseIndex.planSubagents(analysis, {
        targetTokensPerAgent: 30000,
        maxAgents: 4,
      })

      // Check that files from the same directory tend to be in the same plan
      for (const plan of plans) {
        const dirs = new Set(plan.section.map((f) => path.dirname(f)))
        console.log(`\n${plan.id} covers ${dirs.size} directories:`)
        for (const dir of Array.from(dirs).slice(0, 5)) {
          const filesInDir = plan.section.filter((f) => path.dirname(f) === dir)
          console.log(`  ${dir}: ${filesInDir.length} files`)
        }
      }
    })
  })

  describe("formatForAgent", () => {
    it("should produce readable context for agents", async () => {
      const analysis = await CodebaseIndex.analyze(testCwd, { maxFiles: 100 })
      const plans = CodebaseIndex.planSubagents(analysis, {
        targetTokensPerAgent: 30000,
        maxAgents: 2,
      })

      const formatted = CodebaseIndex.formatForAgent(analysis, plans[0])

      console.log(`\n--- Formatted Agent Context ---`)
      console.log(`Length: ${formatted.length} chars (~${Math.round(formatted.length / 4)} tokens)`)
      console.log(`\n${formatted.slice(0, 2000)}...`)

      // Should include key sections
      expect(formatted).toContain("## Codebase Analysis")
      expect(formatted).toContain("### Summary")
      expect(formatted).toContain("### File Structure")
      expect(formatted).toContain("### Symbol Index")

      // Context should be reasonably sized (< 5000 tokens)
      expect(formatted.length / 4).toBeLessThan(5000)
    })
  })
})

// ============================================
// Standalone Demo (run with: bun run packages/opencode/test/util/codebase-index.test.ts)
// ============================================

if (import.meta.main) {
  console.log("=".repeat(60))
  console.log("Codebase Index Demo")
  console.log("=".repeat(60))

  const cwd = process.cwd()
  console.log(`\nAnalyzing: ${cwd}\n`)

  const analysis = await CodebaseIndex.analyze(cwd, { maxFiles: 300 })

  console.log("--- Results ---")
  console.log(`Analysis time: ${analysis.analysisTimeMs}ms`)
  console.log(`Files: ${analysis.fileCount}`)
  console.log(`Lines: ${analysis.totalLines.toLocaleString()}`)
  console.log(`Estimated tokens: ${analysis.estimatedTokens.toLocaleString()}`)
  console.log(`Entry points: ${analysis.entryPoints.length}`)
  console.log(`Files with symbols: ${analysis.symbols.size}`)
  console.log(`Files in import graph: ${analysis.importGraph.size}`)

  console.log("\n--- Entry Points ---")
  for (const entry of analysis.entryPoints) {
    console.log(`  - ${entry}`)
  }

  console.log("\n--- Subagent Plans ---")
  const plans = CodebaseIndex.planSubagents(analysis, {
    targetTokensPerAgent: 50000,
    maxAgents: 4,
  })

  for (const plan of plans) {
    console.log(`\n${plan.id}:`)
    console.log(`  Files: ${plan.section.length}`)
    console.log(`  Est. Tokens: ${plan.estimatedTokens.toLocaleString()}`)
    console.log(`  Budget: ${plan.tokenBudget.toLocaleString()}`)
  }

  console.log("\n--- Agent Context Preview ---")
  const context = CodebaseIndex.formatForAgent(analysis, plans[0])
  console.log(`Context length: ${context.length} chars (~${Math.round(context.length / 4)} tokens)`)
  console.log("\n" + context.slice(0, 3000) + "\n...\n")

  console.log("=".repeat(60))
  console.log("Demo complete!")
  console.log("=".repeat(60))
}
