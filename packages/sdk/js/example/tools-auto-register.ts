#!/usr/bin/env bun
/**
 * Auto-Register Tools Example
 * 
 * This example shows the NEW simplified API where tools are automatically
 * registered with swarm - no manual MCP server setup required!
 * 
 * Compare with run-with-tools.ts which requires manual MCP server setup.
 * 
 * Usage:
 *   bun run example/tools-auto-register.ts
 *   bun run example/tools-auto-register.ts "Search for authentication"
 */

import { createOpencode, tool, z } from "../src/index.js"

// =============================================================================
// DEFINE YOUR CUSTOM TOOLS
// =============================================================================

const echoTool = tool(
  "echo",
  "Echo back a message. Use this to test the tool system.",
  {
    message: z.string().describe("The message to echo back"),
  },
  async (args) => {
    console.log(`\n[echo] Received: "${args.message}"`)
    return `You said: ${args.message}`
  }
)

const searchDocs = tool(
  "search_docs",
  "Search documentation for a query. Returns relevant documentation results.",
  {
    query: z.string().describe("Search query"),
    limit: z.number().optional().default(5).describe("Max results to return"),
  },
  async (args) => {
    console.log(`\n[search_docs] Query: "${args.query}", Limit: ${args.limit}`)
    // Simulate search
    return `Found ${args.limit} results for "${args.query}":\n` +
      `1. Getting Started with ${args.query}\n` +
      `2. ${args.query} API Reference\n` +
      `3. ${args.query} Best Practices`
  }
)

const getCurrentTime = tool(
  "get_time",
  "Get the current date and time. Use when user asks about time.",
  {},
  async () => {
    const now = new Date()
    console.log(`\n[get_time] Called at ${now.toISOString()}`)
    return `Current time: ${now.toLocaleString()}`
  }
)

// =============================================================================
// MAIN - Notice how simple this is!
// =============================================================================

async function main() {
  const prompt = process.argv[2] || "Use the echo tool to say 'Hello from auto-registered tools!'"

  console.log("=".repeat(60))
  console.log("Auto-Register Tools Example")
  console.log("=".repeat(60))
  console.log()
  console.log("Tools being registered:")
  console.log("  - echo: Echo back messages")
  console.log("  - search_docs: Search documentation")
  console.log("  - get_time: Get current time")
  console.log()

  // ONE LINE to create opencode with custom tools!
  // The SDK automatically:
  // 1. Starts an MCP HTTP server on 0.0.0.0:PORT
  // 2. Waits for swarm to start
  // 3. Registers the MCP server with swarm
  // 4. Handles cleanup on close()
  const { spawn, server } = await createOpencode({
    tools: [echoTool, searchDocs, getCurrentTime],
  })

  console.log(`Swarm server: ${server.url}`)
  console.log(`MCP server: ${server.mcpUrl} (port ${server.mcpPort})`)
  console.log()
  console.log("-".repeat(60))
  console.log(`Prompt: "${prompt}"`)
  console.log("-".repeat(60))
  console.log()

  try {
    const handle = spawn({
      prompt,
      mode: "noninteractive",
    })

    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          if (event.delta) process.stdout.write(event.delta)
          break
        case "tool.start":
          console.log(`\n[Tool Start] ${event.name}`)
          console.log(`  Input: ${JSON.stringify(event.input)}`)
          break
        case "tool.end":
          console.log(`[Tool End] ${event.name}`)
          console.log(`  Output: ${event.output.slice(0, 200)}`)
          break
        case "completed":
          console.log("\n")
          console.log("-".repeat(60))
          console.log("Done!")
          break
        case "error":
          console.error("\nError:", event.error.message)
          break
      }
    }
  } finally {
    server.close()
  }
}

main().catch(console.error)
