import { describe, test, expect } from "bun:test"
import * as path from "path"
import * as fs from "fs"

// Test the raw parallel read performance
describe("batch-read performance", () => {
  const testFiles = [
    "src/tool/read.ts",
    "src/tool/glob.ts", 
    "src/tool/grep.ts",
    "src/tool/bash.ts",
    "src/tool/edit.ts",
    "src/tool/task.ts",
    "src/tool/write.ts",
    "src/session/prompt.ts",
    "src/session/system.ts",
    "src/agent/agent.ts",
  ].map(f => path.join(import.meta.dir, "..", f))

  test("sequential reads", async () => {
    const start = performance.now()
    
    const results = []
    for (const file of testFiles) {
      const content = await Bun.file(file).text()
      results.push({ file, lines: content.split("\n").length })
    }
    
    const elapsed = performance.now() - start
    console.log(`Sequential: ${elapsed.toFixed(2)}ms for ${testFiles.length} files`)
    console.log(`  Files read: ${results.map(r => r.lines + " lines").join(", ")}`)
    
    expect(results.length).toBe(testFiles.length)
  })

  test("parallel reads with Promise.all", async () => {
    const start = performance.now()
    
    const results = await Promise.all(
      testFiles.map(async (file) => {
        const content = await Bun.file(file).text()
        return { file, lines: content.split("\n").length }
      })
    )
    
    const elapsed = performance.now() - start
    console.log(`Parallel: ${elapsed.toFixed(2)}ms for ${testFiles.length} files`)
    console.log(`  Files read: ${results.map(r => r.lines + " lines").join(", ")}`)
    
    expect(results.length).toBe(testFiles.length)
  })

  test("parallel with chunking (5 at a time)", async () => {
    const start = performance.now()
    const chunkSize = 5
    const results = []
    
    for (let i = 0; i < testFiles.length; i += chunkSize) {
      const chunk = testFiles.slice(i, i + chunkSize)
      const chunkResults = await Promise.all(
        chunk.map(async (file) => {
          const content = await Bun.file(file).text()
          return { file, lines: content.split("\n").length }
        })
      )
      results.push(...chunkResults)
    }
    
    const elapsed = performance.now() - start
    console.log(`Chunked (5): ${elapsed.toFixed(2)}ms for ${testFiles.length} files`)
    
    expect(results.length).toBe(testFiles.length)
  })

  test("streaming read vs full read", async () => {
    const bigFile = path.join(import.meta.dir, "..", "src/session/prompt.ts")
    
    // Full read
    const start1 = performance.now()
    const full = await Bun.file(bigFile).text()
    const fullLines = full.split("\n").slice(0, 500)
    const elapsed1 = performance.now() - start1
    
    // Streaming read (first 500 lines)
    const start2 = performance.now()
    const stream = Bun.file(bigFile).stream()
    const reader = stream.getReader()
    const decoder = new TextDecoder()
    let buffer = ""
    let lines: string[] = []
    
    while (lines.length < 500) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const newLines = buffer.split("\n")
      buffer = newLines.pop() || ""
      lines.push(...newLines)
    }
    reader.cancel()
    lines = lines.slice(0, 500)
    const elapsed2 = performance.now() - start2
    
    console.log(`Full read + slice: ${elapsed1.toFixed(2)}ms (${fullLines.length} lines)`)
    console.log(`Stream read: ${elapsed2.toFixed(2)}ms (${lines.length} lines)`)
    
    expect(fullLines.length).toBe(500)
    expect(lines.length).toBe(500)
  })

  test("Bun.file vs fs.promises", async () => {
    const file = testFiles[0]
    
    // Bun.file
    const start1 = performance.now()
    for (let i = 0; i < 100; i++) {
      await Bun.file(file).text()
    }
    const elapsed1 = performance.now() - start1
    
    // fs.promises
    const start2 = performance.now()
    for (let i = 0; i < 100; i++) {
      await fs.promises.readFile(file, "utf-8")
    }
    const elapsed2 = performance.now() - start2
    
    console.log(`Bun.file x100: ${elapsed1.toFixed(2)}ms`)
    console.log(`fs.promises x100: ${elapsed2.toFixed(2)}ms`)
  })
})
