/**
 * Monitor an agent running
 *
 * Run with:
 *   cd packages/sdk/js && bun run example/monitor.ts "your prompt here"
 */

import { createOpencode, type SpawnEvent } from "../src/index.js"

const prompt = process.argv[2] || "What files are in this directory? Just list them."

console.log("🚀 Starting swarm server...")
const { spawn, server } = await createOpencode()

console.log(`📡 Server running at ${server.url}`)
console.log(`\n💬 Prompt: ${prompt}\n`)
console.log("─".repeat(60))

const handle = spawn(prompt)

let currentText = ""

for await (const event of handle.stream()) {
  switch (event.type) {
    case "text":
      // Print incremental text (delta if available, otherwise full text diff)
      if (event.delta) {
        process.stdout.write(event.delta)
        currentText += event.delta
      } else if (event.text.length > currentText.length) {
        const newText = event.text.slice(currentText.length)
        process.stdout.write(newText)
        currentText = event.text
      }
      break

    case "tool.start":
      console.log(`\n\n🔧 [${event.name}]`)
      // Show truncated input
      const inputStr = JSON.stringify(event.input)
      console.log(`   ${inputStr.length > 100 ? inputStr.slice(0, 100) + "..." : inputStr}`)
      break

    case "tool.end":
      // Show truncated output
      const output = event.output
      const lines = output.split("\n")
      if (lines.length > 5) {
        console.log(`   → ${lines.slice(0, 3).join("\n     ")}`)
        console.log(`   ... (${lines.length - 3} more lines)`)
      } else {
        console.log(`   → ${output.slice(0, 200)}${output.length > 200 ? "..." : ""}`)
      }
      console.log("")
      break

    case "todo":
      console.log(`\n📋 Todos: ${event.todos.map((t) => `[${t.status}] ${t.content}`).join(", ")}`)
      break

    case "permission":
      console.log(`\n⚠️  Permission needed: ${event.title}`)
      break

    case "completed":
      console.log("\n" + "─".repeat(60))
      console.log("✅ Completed!")
      break

    case "aborted":
      console.log("\n" + "─".repeat(60))
      console.log("⛔ Aborted!")
      break

    case "error":
      console.log("\n" + "─".repeat(60))
      console.log("❌ Error:", event.error.message)
      break
  }
}

// Clean up
server.close()
console.log("\n👋 Done")
