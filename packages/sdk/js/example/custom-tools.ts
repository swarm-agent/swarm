/**
 * Custom Tools Example
 * 
 * This example demonstrates how to create custom tools and profiles
 * for the Swarm SDK, similar to Claude Agent SDK's approach.
 * 
 * Features shown:
 * - Creating custom tools with tool()
 * - Creating MCP servers with createSwarmMcpServer()
 * - Creating profiles with envFile support
 * - Using profiles in spawn()
 */

import { z } from "zod"
import {
  createOpencode,
  createSwarmProfile,
  createSwarmMcpServer,
  tool,
  loadEnvFile,
} from "../src/index.js"

// ============================================================================
// 1. Define Custom Tools
// ============================================================================

/**
 * Weather tool - fetches weather data from an API
 * Uses API key from environment
 */
const getWeatherTool = tool(
  "get_weather",
  "Get current weather for a city. Use when user asks about weather conditions.",
  {
    city: z.string().describe("City name (e.g., 'New York', 'London')"),
    units: z.enum(["celsius", "fahrenheit"]).optional().default("celsius")
      .describe("Temperature units"),
  },
  async (args) => {
    // In real usage, you'd call an actual weather API
    // The API key comes from the profile's envFile
    const apiKey = process.env.WEATHER_API_KEY
    if (!apiKey) {
      return "Error: WEATHER_API_KEY not set in environment"
    }

    // Simulated API call
    console.log(`[Weather Tool] Fetching weather for ${args.city} (key: ${apiKey.slice(0, 4)}...)`)
    
    // Return result to the agent
    return `Weather in ${args.city}: Sunny, 72°${args.units === "celsius" ? "C" : "F"}, Humidity: 45%`
  }
)

/**
 * Create ticket tool - creates a JIRA/issue ticket
 */
const createTicketTool = tool(
  "create_ticket",
  "Create a support or development ticket. Use when user wants to track work or report issues.",
  {
    title: z.string().describe("Ticket title"),
    description: z.string().optional().describe("Detailed description"),
    priority: z.enum(["low", "medium", "high", "critical"]).default("medium")
      .describe("Ticket priority"),
    labels: z.array(z.string()).optional().describe("Labels to apply"),
  },
  async (args) => {
    const ticketId = `TICKET-${Date.now().toString(36).toUpperCase()}`
    
    console.log(`[Ticket Tool] Creating ticket: ${ticketId}`)
    console.log(`  Title: ${args.title}`)
    console.log(`  Priority: ${args.priority}`)
    
    // In real usage, you'd call JIRA/GitHub/Linear API
    return `Created ticket ${ticketId}: "${args.title}" (${args.priority} priority)`
  }
)

/**
 * Search docs tool - searches internal documentation
 */
const searchDocsTool = tool(
  "search_docs",
  "Search internal documentation and knowledge base. Use when user asks about company processes, APIs, or standards.",
  {
    query: z.string().describe("Search query"),
    limit: z.number().optional().default(5).describe("Maximum number of results"),
    category: z.enum(["all", "api", "process", "guide"]).optional().default("all")
      .describe("Category to search in"),
  },
  async (args) => {
    console.log(`[Docs Tool] Searching for: "${args.query}" in ${args.category}`)
    
    // Simulated search results
    const results = [
      { title: "API Authentication Guide", snippet: "OAuth2 flow for API access..." },
      { title: "Rate Limiting Policy", snippet: "API rate limits are 100 req/min..." },
      { title: "Error Handling Standards", snippet: "All errors return JSON with code..." },
    ].slice(0, args.limit)
    
    return `Found ${results.length} results for "${args.query}":\n` +
      results.map((r, i) => `${i + 1}. ${r.title}\n   ${r.snippet}`).join("\n")
  }
)

// ============================================================================
// 2. Create MCP Servers (group related tools)
// ============================================================================

/**
 * Internal tools server - company-specific tools
 */
const internalToolsServer = createSwarmMcpServer({
  name: "internal-tools",
  version: "1.0.0",
  tools: [
    createTicketTool,
    searchDocsTool,
  ],
})

/**
 * External API tools server - third-party integrations
 */
const externalApisServer = createSwarmMcpServer({
  name: "external-apis",
  version: "1.0.0",
  tools: [
    getWeatherTool,
  ],
})

// ============================================================================
// 3. Create Custom Profiles
// ============================================================================

/**
 * Backend developer profile
 * - Full permissions (for running commands)
 * - Internal tools enabled
 * - Uses .env.backend for secrets
 */
const backendDevProfile = createSwarmProfile({
  name: "backend-dev",
  description: "Backend development with internal tools",
  base: "full",
  envFile: ".env.backend", // Loads API keys, DB URLs, etc.
  mcpServers: {
    "internal-tools": internalToolsServer,
    "external-apis": externalApisServer,
  },
  // Additional tool overrides
  tools: {
    websearch: true, // Enable web search
  },
})

/**
 * Support agent profile
 * - Edit permissions only (no shell)
 * - Ticket tools enabled
 * - Uses .env.support for secrets
 */
const supportProfile = createSwarmProfile({
  name: "support",
  description: "Customer support with ticket management",
  base: "edit", // No bash access
  envFile: ".env.support",
  mcpServers: {
    "internal-tools": internalToolsServer,
  },
})

/**
 * Simple profile with just env file (no custom tools)
 */
const simpleProfile = createSwarmProfile({
  name: "simple-api",
  description: "Simple API access with secrets from env",
  base: "full",
  envFile: ".env", // Uses standard .env file
  // No custom MCP servers
})

// ============================================================================
// 4. Usage Example
// ============================================================================

async function main() {
  console.log("=== Custom Tools Example ===\n")

  // Show what's in the profile
  console.log("Backend Dev Profile:")
  console.log("  Name:", backendDevProfile.name)
  console.log("  Description:", backendDevProfile.description)
  console.log("  MCP Servers:", Object.keys(backendDevProfile.mcpServers))
  console.log("  Tools enabled:", Object.entries(backendDevProfile.tools)
    .filter(([, v]) => v).map(([k]) => k).join(", "))
  console.log()

  // List tools in MCP servers
  console.log("Available Custom Tools:")
  for (const [serverName, server] of Object.entries(backendDevProfile.mcpServers)) {
    console.log(`  Server: ${serverName}`)
    for (const tool of server.listTools()) {
      console.log(`    - ${tool.name}: ${tool.description.slice(0, 60)}...`)
    }
  }
  console.log()

  // Load env manually (for demonstration)
  console.log("Loading env from profile...")
  const env = await backendDevProfile.loadEnv()
  console.log("  Loaded vars:", Object.keys(env).length > 0 ? Object.keys(env).join(", ") : "(none - file not found)")
  console.log()

  // Example spawn usage (commented out - requires running server)
  /*
  const { spawn, server } = await createOpencode()

  try {
    // Use custom profile with spawn
    const handle = spawn({
      prompt: "Search our docs for authentication and create a ticket for any issues found",
      profile: backendDevProfile,
      mode: "interactive",
      onPermission: async (p) => {
        console.log(`Permission requested: ${p.title}`)
        return "approve"
      },
    })

    // Stream events
    for await (const event of handle.stream()) {
      switch (event.type) {
        case "text":
          process.stdout.write(event.delta ?? "")
          break
        case "tool.start":
          console.log(`\n[Tool: ${event.name}]`)
          break
        case "tool.end":
          console.log(`[Output: ${event.output.slice(0, 100)}...]`)
          break
        case "completed":
          console.log("\n\nCompleted!")
          break
      }
    }
  } finally {
    server.close()
  }
  */

  console.log("Example complete! See code for spawn() usage.")
}

// Run if executed directly
main().catch(console.error)
