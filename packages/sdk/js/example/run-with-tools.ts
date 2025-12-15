#!/usr/bin/env bun
/**
 * Run Agent with Custom Tools
 * 
 * Real production example - spawns an agent with custom MCP tools.
 * 
 * Usage:
 *   bun run example/run-with-tools.ts "What's the weather in Tokyo?"
 *   bun run example/run-with-tools.ts "Create a ticket for the login bug"
 */

import { z } from "zod"
import { createOpencode, tool, createSwarmMcpServer } from "../src/index.js"
import { serve } from "bun"

// =============================================================================
// DEFINE YOUR CUSTOM TOOLS
// =============================================================================

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
)

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
  }
)

const searchDocs = tool(
  "search_docs", 
  "Search documentation. Use when user asks how something works.",
  {
    query: z.string().describe("Search query"),
  },
  async (args) => {
    console.log(`\n📚 [search_docs] Query: "${args.query}"`)
    return `Found 3 results for "${args.query}":\n1. Getting Started\n2. API Reference\n3. FAQ`
  }
)

// =============================================================================
// CREATE MCP SERVER
// =============================================================================

const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  version: "1.0.0",
  tools: [getWeather, createTicket, searchDocs],
})

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
        const result = await server.executeTool(name, args || {}, {})
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

  console.log("🚀 Custom Tools Agent\n")
  console.log("Tools available:")
  for (const t of mcpServer.listTools()) {
    console.log(`  • ${t.name}: ${t.description}`)
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
