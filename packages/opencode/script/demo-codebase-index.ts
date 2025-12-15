#!/usr/bin/env bun
/**
 * Demo: Codebase Index
 *
 * Demonstrates the pre-computation layer for batched exploration.
 * Run with: bun run packages/opencode/script/demo-codebase-index.ts
 */

import { CodebaseIndex } from "../src/util/codebase-index"

console.log("=".repeat(60))
console.log("Codebase Index Demo - Pre-computation for Batched Exploration")
console.log("=".repeat(60))

const cwd = process.cwd()
console.log(`\nAnalyzing: ${cwd}\n`)

const startTime = Date.now()
const analysis = await CodebaseIndex.analyze(cwd, { maxFiles: 500 })
const totalTime = Date.now() - startTime

console.log("--- Performance ---")
console.log(`Total time: ${totalTime}ms`)
console.log(`Internal analysis time: ${analysis.analysisTimeMs}ms`)
console.log("")

console.log("--- Codebase Statistics ---")
console.log(`Files analyzed: ${analysis.fileCount}`)
console.log(`Total lines: ${analysis.totalLines.toLocaleString()}`)
console.log(`Estimated tokens: ${analysis.estimatedTokens.toLocaleString()}`)
console.log(`Entry points found: ${analysis.entryPoints.length}`)
console.log(`Files with symbols: ${analysis.symbols.size}`)
console.log(`Files in import graph: ${analysis.importGraph.size}`)

console.log("\n--- Entry Points ---")
for (const entry of analysis.entryPoints.slice(0, 10)) {
  console.log(`  - ${entry}`)
}
if (analysis.entryPoints.length > 10) {
  console.log(`  ... and ${analysis.entryPoints.length - 10} more`)
}

console.log("\n--- Top Files by Symbols ---")
const sortedBySymbols = Array.from(analysis.symbols.entries())
  .sort((a, b) => b[1].length - a[1].length)
  .slice(0, 10)

for (const [file, symbols] of sortedBySymbols) {
  console.log(`  ${file}: ${symbols.length} symbols`)
  console.log(`    [${symbols.slice(0, 5).join(", ")}${symbols.length > 5 ? "..." : ""}]`)
}

console.log("\n--- Sample Import Graph ---")
const sampleImports = Array.from(analysis.importGraph.entries()).slice(0, 5)
for (const [file, imports] of sampleImports) {
  console.log(`  ${file}`)
  for (const imp of imports.slice(0, 3)) {
    console.log(`    -> ${imp}`)
  }
  if (imports.length > 3) {
    console.log(`    ... and ${imports.length - 3} more`)
  }
}

console.log("\n--- Subagent Planning ---")
const plans = CodebaseIndex.planSubagents(analysis, {
  targetTokensPerAgent: 50000,
  maxAgents: 4,
})

console.log(`\nSplit into ${plans.length} subagent(s):`)
for (const plan of plans) {
  console.log(`\n  ${plan.id}:`)
  console.log(`    Files: ${plan.section.length}`)
  console.log(`    Est. Tokens: ${plan.estimatedTokens.toLocaleString()} / ${plan.tokenBudget.toLocaleString()} budget`)
  console.log(`    Entry Points: ${plan.entryPoints.slice(0, 3).join(", ") || "none"}`)

  // Show directory breakdown
  const dirs = new Map<string, number>()
  for (const f of plan.section) {
    const dir = f.split("/")[0] || "."
    dirs.set(dir, (dirs.get(dir) || 0) + 1)
  }
  const topDirs = Array.from(dirs.entries())
    .sort((a, b) => b[1] - a[1])
    .slice(0, 3)
  console.log(`    Top dirs: ${topDirs.map(([d, c]) => `${d}(${c})`).join(", ")}`)
}

console.log("\n--- Agent Context Preview ---")
const context = CodebaseIndex.formatForAgent(analysis, plans[0])
console.log(`Context length: ${context.length} chars (~${Math.round(context.length / 4)} tokens)`)
console.log("\n" + "-".repeat(40))
console.log(context.slice(0, 2500))
console.log("-".repeat(40))
console.log(`... (${context.length - 2500} more chars)`)

console.log("\n" + "=".repeat(60))
console.log("KEY INSIGHT: This analysis ran in", totalTime + "ms")
console.log("vs ~15-20 seconds with sequential LLM exploration!")
console.log("")
console.log("The main agent now has enough context to:")
console.log("  1. Understand the codebase structure")
console.log("  2. Know which files exist and their sizes")
console.log("  3. See the symbol index (function/class names)")
console.log("  4. Understand file dependencies via import graph")
console.log("  5. Delegate to subagents with pre-computed plans")
console.log("=".repeat(60))
