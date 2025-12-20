#!/usr/bin/env bun
import * as path from "path"
import * as fs from "fs"

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

console.log("=== Batch Read Performance Test ===\n")
console.log(`Testing ${testFiles.length} files\n`)

// Test 1: Sequential
{
  const start = performance.now()
  const results = []
  for (const file of testFiles) {
    const content = await Bun.file(file).text()
    results.push({ file: path.basename(file), lines: content.split("\n").length })
  }
  const elapsed = performance.now() - start
  console.log(`1. Sequential reads:     ${elapsed.toFixed(2)}ms`)
}

// Test 2: Parallel Promise.all
{
  const start = performance.now()
  const results = await Promise.all(
    testFiles.map(async (file) => {
      const content = await Bun.file(file).text()
      return { file: path.basename(file), lines: content.split("\n").length }
    })
  )
  const elapsed = performance.now() - start
  console.log(`2. Parallel (all):       ${elapsed.toFixed(2)}ms`)
}

// Test 3: Parallel with processing
{
  const start = performance.now()
  const results = await Promise.all(
    testFiles.map(async (file) => {
      const content = await Bun.file(file).text()
      const lines = content.split("\n").slice(0, 500)
      const formatted = lines.map((l, i) => `${String(i+1).padStart(4)}| ${l.slice(0, 1000)}`).join("\n")
      return { file: path.basename(file), content: formatted, lines: lines.length }
    })
  )
  const elapsed = performance.now() - start
  console.log(`3. Parallel + format:    ${elapsed.toFixed(2)}ms`)
}

// Test 4: Using streams for first N lines
{
  const start = performance.now()
  const results = await Promise.all(
    testFiles.map(async (file) => {
      const stream = Bun.file(file).stream()
      const reader = stream.getReader()
      const decoder = new TextDecoder()
      let buffer = ""
      let lines: string[] = []
      const maxLines = 500
      
      while (lines.length < maxLines) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const parts = buffer.split("\n")
        buffer = parts.pop() || ""
        lines.push(...parts)
        if (lines.length >= maxLines) break
      }
      reader.cancel()
      
      return { file: path.basename(file), lines: lines.slice(0, maxLines).length }
    })
  )
  const elapsed = performance.now() - start
  console.log(`4. Streaming (500 lines): ${elapsed.toFixed(2)}ms`)
}

// Test 5: ArrayBuffer approach
{
  const start = performance.now()
  const results = await Promise.all(
    testFiles.map(async (file) => {
      const buf = await Bun.file(file).arrayBuffer()
      const text = new TextDecoder().decode(buf)
      const lines = text.split("\n").slice(0, 500)
      return { file: path.basename(file), lines: lines.length }
    })
  )
  const elapsed = performance.now() - start
  console.log(`5. ArrayBuffer decode:   ${elapsed.toFixed(2)}ms`)
}

// Test 6: 20 files
{
  const moreFiles = [...testFiles, ...testFiles].slice(0, 20)
  const start = performance.now()
  const results = await Promise.all(
    moreFiles.map(async (file) => {
      const content = await Bun.file(file).text()
      return { lines: content.split("\n").length }
    })
  )
  const elapsed = performance.now() - start
  console.log(`6. 20 files parallel:    ${elapsed.toFixed(2)}ms`)
}

console.log("\n=== Done ===")
