/**
 * Test: BatchExplore Tool
 *
 * Run with: bun test packages/opencode/test/tool/batch-explore.test.ts
 */

import { describe, it, expect, beforeAll } from "bun:test"
import { BatchExploreTool } from "../../src/tool/batch-explore"

describe("BatchExploreTool", () => {
  let tool: Awaited<ReturnType<typeof BatchExploreTool.init>>

  beforeAll(async () => {
    tool = await BatchExploreTool.init()
  })

  it("should have correct description", () => {
    expect(tool.description).toContain("Batch multiple codebase exploration")
    expect(tool.description).toContain("readFiles")
    expect(tool.description).toContain("grepPatterns")
  })

  it("should read multiple files in parallel", async () => {
    const startTime = Date.now()

    const result = await tool.execute(
      {
        readFiles: [
          "packages/opencode/src/util/token.ts",
          "packages/opencode/src/util/log.ts",
          "packages/opencode/src/util/error.ts",
        ],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    const elapsed = Date.now() - startTime

    console.log("\n--- Read Multiple Files ---")
    console.log(`Time: ${elapsed}ms`)
    console.log(`Output length: ${result.output.length} chars`)
    console.log(`Title: ${result.title}`)

    // Should complete quickly (parallel reads)
    expect(elapsed).toBeLessThan(500)

    // Should contain file contents
    expect(result.output).toContain("token.ts")
    expect(result.output).toContain("log.ts")
    expect(result.output).toContain("error.ts")

    // Should show token tracking
    expect(result.output).toContain("Tokens used:")
    expect(result.output).toContain("Tokens remaining:")
  })

  it("should handle symbols-only mode for large files", async () => {
    const result = await tool.execute(
      {
        readSymbolsOnly: ["packages/opencode/src/session/prompt.ts"],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    console.log("\n--- Symbols Only ---")
    console.log(`Output length: ${result.output.length} chars`)

    // Should contain symbols extraction note
    expect(result.output).toContain("symbols")

    // Output should be much smaller than full file
    expect(result.output.length).toBeLessThan(20000)
  })

  it("should execute grep patterns in parallel", async () => {
    const startTime = Date.now()

    const result = await tool.execute(
      {
        grepPatterns: [
          { pattern: "export namespace", maxMatches: 10 },
          { pattern: "async function", maxMatches: 10 },
        ],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    const elapsed = Date.now() - startTime

    console.log("\n--- Grep Patterns ---")
    console.log(`Time: ${elapsed}ms`)
    console.log(`Output preview: ${result.output.slice(0, 500)}...`)

    // Should complete quickly (parallel greps)
    expect(elapsed).toBeLessThan(1000)

    // Should contain grep results
    expect(result.output).toContain("Grep Results")
  })

  it("should execute glob patterns", async () => {
    const result = await tool.execute(
      {
        globPatterns: ["packages/opencode/src/**/*.ts"],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    console.log("\n--- Glob Patterns ---")
    console.log(`Output preview: ${result.output.slice(0, 500)}...`)

    // Should find TypeScript files
    expect(result.output).toContain("Glob Results")
    expect(result.output).toContain(".ts")
  })

  it("should handle combined batch request", async () => {
    const startTime = Date.now()

    const result = await tool.execute(
      {
        readFiles: ["packages/opencode/src/index.ts"],
        readSymbolsOnly: ["packages/opencode/src/session/index.ts"],
        grepPatterns: [{ pattern: "export", maxMatches: 5 }],
        globPatterns: ["packages/opencode/src/util/*.ts"],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    const elapsed = Date.now() - startTime

    console.log("\n--- Combined Batch ---")
    console.log(`Time: ${elapsed}ms`)
    console.log(`Metadata: ${JSON.stringify(result.metadata)}`)

    // Should complete quickly (all in parallel)
    expect(elapsed).toBeLessThan(1000)

    // Should contain all sections
    expect(result.output).toContain("File Contents")
    expect(result.output).toContain("Grep Results")
    expect(result.output).toContain("Glob Results")
  })

  it("should handle non-existent files gracefully", async () => {
    const result = await tool.execute(
      {
        readFiles: ["nonexistent/file.ts", "packages/opencode/src/index.ts"],
      },
      {
        sessionID: "test",
        messageID: "test",
        agent: "explore",
        abort: new AbortController().signal,
        metadata: () => {},
      },
    )

    console.log("\n--- Non-existent Files ---")
    console.log(`Output preview: ${result.output.slice(0, 500)}...`)

    // Should report error but still process valid files
    expect(result.output).toContain("Errors:")
    expect(result.output).toContain("nonexistent")
    expect(result.output).toContain("index.ts")
  })
})

// ============================================
// Standalone Demo
// ============================================

if (import.meta.main) {
  console.log("=".repeat(60))
  console.log("BatchExplore Tool Demo")
  console.log("=".repeat(60))

  const tool = await BatchExploreTool.init()

  console.log("\n--- Testing Combined Batch ---\n")

  const startTime = Date.now()
  const result = await tool.execute(
    {
      readFiles: [
        "packages/opencode/src/index.ts",
        "packages/opencode/src/util/token.ts",
      ],
      readSymbolsOnly: ["packages/opencode/src/session/prompt.ts"],
      grepPatterns: [
        { pattern: "export namespace", maxMatches: 5 },
        { pattern: "async function \\w+", maxMatches: 5 },
      ],
      globPatterns: ["packages/opencode/src/tool/*.ts"],
    },
    {
      sessionID: "demo",
      messageID: "demo",
      agent: "explore",
      abort: new AbortController().signal,
      metadata: (m) => console.log("Metadata update:", m),
    },
  )
  const elapsed = Date.now() - startTime

  console.log("\n" + "=".repeat(60))
  console.log(`Completed in ${elapsed}ms`)
  console.log("=".repeat(60))
  console.log("\n" + result.output.slice(0, 3000))
  console.log("\n... (truncated)")
}
