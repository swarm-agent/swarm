#!/usr/bin/env bun
/**
 * Tools with Container Profile Example
 * 
 * Shows how to use custom tools with a containerized agent.
 * The SDK automatically uses host.containers.internal (podman) or
 * host.docker.internal (docker) so the container can reach the MCP server.
 * 
 * Prerequisites:
 *   1. Create a container profile in swarm.json
 *   2. Start the profile: swarm profile start my-agent
 * 
 * Usage:
 *   bun run example/tools-with-container.ts
 */

import { createOpencode, tool, z } from "../src/index.js"

// =============================================================================
// TOOLS - These run on the HOST, not in the container!
// =============================================================================

const notifyTool = tool(
  "notify",
  "Send a notification to the user. Use this to communicate important information.",
  {
    message: z.string().describe("Notification message"),
    level: z.enum(["info", "warning", "error"]).default("info"),
  },
  async (args) => {
    // This runs on the HOST - can access host resources
    const timestamp = new Date().toISOString()
    console.log(`\n[HOST NOTIFICATION] [${args.level.toUpperCase()}] ${timestamp}`)
    console.log(`  ${args.message}`)
    return `Notification sent: ${args.message}`
  }
)

const hostInfoTool = tool(
  "get_host_info",
  "Get information about the host system (outside the container).",
  {},
  async () => {
    // This runs on the HOST - can see host environment
    return JSON.stringify({
      hostname: process.env.HOSTNAME || "unknown",
      user: process.env.USER || "unknown",
      home: process.env.HOME || "unknown",
      node: process.version,
      platform: process.platform,
      arch: process.arch,
    }, null, 2)
  }
)

// =============================================================================
// MAIN
// =============================================================================

async function main() {
  const profileName = process.env.PROFILE || "test-agent"
  
  console.log("=".repeat(60))
  console.log("Container Profile + Custom Tools Example")
  console.log("=".repeat(60))
  console.log()
  console.log(`Container Profile: ${profileName}`)
  console.log()
  console.log("How it works:")
  console.log("  1. SDK starts MCP server on 0.0.0.0:PORT (host)")
  console.log("  2. Swarm server starts (may run in container)")
  console.log("  3. SDK registers MCP with container-accessible URL:")
  console.log("     - Podman: http://host.containers.internal:PORT")
  console.log("     - Docker: http://host.docker.internal:PORT")
  console.log("  4. Agent in container can call tools on host!")
  console.log()

  try {
    const { spawn, server } = await createOpencode({
      profile: profileName,
      tools: [notifyTool, hostInfoTool],
      // Optional: explicitly set runtime instead of auto-detect
      // containerRuntime: "podman",
    })

    console.log(`Swarm server: ${server.url}`)
    console.log(`MCP server: ${server.mcpUrl}`)
    console.log()

    const handle = spawn({
      prompt: "First, get the host info to see what system we're on. Then send a notification saying 'Hello from container!'",
      containerProfile: profileName,
      mode: "noninteractive",
    })

    console.log("-".repeat(60))
    console.log("Agent output:")
    console.log("-".repeat(60))

    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          if (event.delta) process.stdout.write(event.delta)
          break
        case "tool.start":
          console.log(`\n[Tool] ${event.name}`)
          break
        case "tool.end":
          console.log(`[Result] ${event.output.slice(0, 300)}`)
          break
        case "completed":
          console.log("\n" + "-".repeat(60))
          console.log("Complete!")
          break
        case "error":
          console.error("\nError:", event.error.message)
          break
      }
    }

    server.close()
  } catch (err) {
    console.error("Failed:", err)
    console.log()
    console.log("Make sure the container profile exists and is running:")
    console.log(`  swarm profile start ${profileName}`)
    process.exit(1)
  }
}

main().catch(console.error)
