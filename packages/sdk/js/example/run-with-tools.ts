#!/usr/bin/env bun
/**
 * Run Agent with Custom Tools
 * 
 * Real production example - spawns an agent with custom MCP tools.
 * Shows permission levels: allow, ask, pin, deny
 * 
 * Usage:
 *   bun run example/run-with-tools.ts "What's the weather in Tokyo?"
 *   bun run example/run-with-tools.ts "Create a ticket for the login bug"
 *   bun run example/run-with-tools.ts "Delete record 123"
 */

import { z } from "zod"
import { createOpencode, tool, createSwarmMcpServer, type ToolPermissionRequest } from "../src/index.js"
import { serve } from "bun"
import * as readline from "readline"

// =============================================================================
// DEFINE YOUR CUSTOM TOOLS (with permissions!)
// =============================================================================

// permission: "allow" (default) - executes immediately
const getWeather = tool(
  "get_weather",
  "Get current weather for a city. Use when user asks about weather.",
  {
    city: z.string().describe("City name"),
  },
  async (args) => {
    console.log(`\n🌤️  [get_weather] Called with: ${args.city}`)
    return `Weather in ${args.city}: Sunny, 72°F, Humidity 45%`
  }
  // permission defaults to "allow"
)

// permission: "allow" - safe read operation
const searchDocs = tool(
  "search_docs", 
  "Search documentation. Use when user asks how something works.",
  {
    query: z.string().describe("Search query"),
  },
  async (args) => {
    console.log(`\n📚 [search_docs] Query: "${args.query}"`)
    return `Found 3 results for "${args.query}":\n1. Getting Started\n2. API Reference\n3. FAQ`
  },
  { permission: "allow" }
)

// permission: "ask" - requires user approval
const createTicket = tool(
  "create_ticket",
  "Create a support ticket. Use when user wants to track an issue.",
  {
    title: z.string().describe("Ticket title"),
    priority: z.enum(["low", "medium", "high"]).default("medium"),
  },
  async (args) => {
    const id = `TICKET-${Date.now().toString(36).toUpperCase()}`
    console.log(`\n🎫 [create_ticket] Created: ${id}`)
    return `Created ticket ${id}: "${args.title}" (${args.priority} priority)`
  },
  { permission: "ask" }  // Requires approval before creating
)

// permission: "ask" - destructive operation needs approval
const deleteRecord = tool(
  "delete_record",
  "Delete a record from the database. Use when user wants to remove data.",
  {
    id: z.string().describe("Record ID to delete"),
  },
  async (args) => {
    console.log(`\n🗑️  [delete_record] Deleted: ${args.id}`)
    return `Successfully deleted record: ${args.id}`
  },
  { permission: "ask" }  // Destructive - requires approval
)

// permission: "pin" - sensitive operation requires PIN
const transferFunds = tool(
  "transfer_funds",
  "Transfer funds between accounts. Use for financial transactions.",
  {
    amount: z.number().describe("Amount to transfer"),
    to: z.string().describe("Destination account"),
  },
  async (args) => {
    console.log(`\n💸 [transfer_funds] Transferred $${args.amount} to ${args.to}`)
    return `Transferred $${args.amount} to account ${args.to}`
  },
  { permission: "pin" }  // Requires PIN verification
)

// permission: "deny" - blocked tool (for testing/safety)
const dangerousAction = tool(
  "dangerous_action",
  "This action is blocked for safety.",
  {},
  async () => {
    return "This should never execute"
  },
  { permission: "deny" }  // Always blocked
)

// =============================================================================
// CREATE MCP SERVER
// =============================================================================

const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  version: "1.0.0",
  tools: [getWeather, searchDocs, createTicket, deleteRecord, transferFunds, dangerousAction],
})

// =============================================================================
// PERMISSION HANDLER (prompts user for approval)
// =============================================================================

async function promptForPermission(request: ToolPermissionRequest): Promise<boolean> {
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  })

  return new Promise((resolve) => {
    const prefix = request.permission === "pin" ? "🔐 PIN required" : "⚠️  Approval required"
    console.log(`\n${prefix} for tool: ${request.tool}`)
    console.log(`   Description: ${request.description}`)
    console.log(`   Arguments: ${JSON.stringify(request.args)}`)
    
    if (request.permission === "pin") {
      rl.question("   Enter PIN to approve (or 'n' to reject): ", (answer) => {
        rl.close()
        // For demo: accept "1234" as valid PIN
        if (answer === "1234") {
          console.log("   ✅ PIN verified")
          resolve(true)
        } else if (answer.toLowerCase() === "n") {
          console.log("   ❌ Rejected by user")
          resolve(false)
        } else {
          console.log("   ❌ Invalid PIN")
          resolve(false)
        }
      })
    } else {
      rl.question("   Approve? (y/n): ", (answer) => {
        rl.close()
        const approved = answer.toLowerCase() === "y" || answer.toLowerCase() === "yes"
        console.log(approved ? "   ✅ Approved" : "   ❌ Rejected")
        resolve(approved)
      })
    }
  })
}

// =============================================================================
// HOST MCP HTTP SERVER (for opencode to connect to)
// =============================================================================

function startMcpHttpServer(server: ReturnType<typeof createSwarmMcpServer>, port: number) {
  return serve({
    port,
    async fetch(req) {
      if (req.method !== "POST") {
        return new Response("MCP Server", { status: 200 })
      }

      const body = await req.json() as { method: string; params?: any; id?: number }
      console.log(`[MCP] ${body.method}`)

      // MCP Protocol handlers
      if (body.method === "initialize") {
        return Response.json({
          jsonrpc: "2.0",
          id: body.id,
          result: {
            protocolVersion: "2024-11-05",
            capabilities: { tools: {} },
            serverInfo: { name: server.name, version: server.version },
          },
        })
      }

      if (body.method === "tools/list") {
        const tools = server.listTools().map(t => ({
          name: t.name,
          description: t.description,
          inputSchema: t.inputSchema,
        }))
        return Response.json({
          jsonrpc: "2.0",
          id: body.id,
          result: { tools },
        })
      }

      if (body.method === "tools/call") {
        const { name, arguments: args } = body.params
        // Execute with permission handler
        const result = await server.executeTool(name, args || {}, {
          onPermission: promptForPermission,
        })
        return Response.json({
          jsonrpc: "2.0",
          id: body.id,
          result,
        })
      }

      return Response.json({
        jsonrpc: "2.0",
        id: body.id,
        error: { code: -32601, message: "Method not found" },
      })
    },
  })
}

// =============================================================================
// MAIN
// =============================================================================

async function main() {
  const prompt = process.argv[2] || "Use the get_weather tool to check the weather in Tokyo"
  const MCP_PORT = 19876

  console.log("🚀 Custom Tools Agent with Permissions\n")
  console.log("Tools available:")
  const toolDefs = [getWeather, searchDocs, createTicket, deleteRecord, transferFunds, dangerousAction]
  for (const t of toolDefs) {
    const permIcon = {
      allow: "✅",
      ask: "⚠️",
      pin: "🔐",
      deny: "🚫",
    }[t.permission]
    console.log(`  ${permIcon} ${t.name} (${t.permission}): ${t.description.slice(0, 50)}...`)
  }
  console.log()

  // 1. Start MCP HTTP server
  console.log(`📡 Starting MCP server on port ${MCP_PORT}...`)
  const httpServer = startMcpHttpServer(mcpServer, MCP_PORT)

  // 2. Start opencode
  console.log("🔧 Starting opencode...")
  const { client, spawn, server: opencodeServer } = await createOpencode()

  try {
    // 3. Register our MCP server with opencode
    console.log("🔗 Registering MCP tools with opencode...")
    await client.mcp.add({
      body: {
        name: "my-tools",
        config: {
          type: "remote",
          url: `http://localhost:${MCP_PORT}`,
        },
      },
    })
    
    // Give it a moment to connect
    await new Promise(r => setTimeout(r, 1000))
    
    // Check status
    const status = await client.mcp.status()
    console.log("   MCP Status:", status.data)

    // 4. Run the prompt
    console.log(`\n📝 Prompt: "${prompt}"\n`)
    console.log("─".repeat(60))

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
          console.log(`\n⚡ Tool: ${event.name}`)
          break
        case "tool.end":
          console.log(`✅ Result: ${event.output.slice(0, 200)}`)
          break
        case "completed":
          console.log("\n" + "─".repeat(60))
          console.log("✅ Done!")
          break
        case "error":
          console.error("\n❌ Error:", event.error.message)
          break
      }
    }
  } finally {
    httpServer.stop()
    opencodeServer.close()
  }
}

main().catch(console.error)
