import z from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./websearch.txt"
import { Config } from "../config/config"
import { Permission } from "../permission"

const API_CONFIG = {
  BASE_URL: "https://mcp.exa.ai",
  ENDPOINTS: {
    SEARCH: "/mcp",
  },
  DEFAULT_NUM_RESULTS: 8,
  // Rate limiting
  MAX_REQUESTS_PER_MINUTE: 10,
  RATE_LIMIT_WINDOW_MS: 60_000,
} as const

// Simple in-memory rate limiter to prevent API spam
const rateLimiter = {
  requests: [] as number[],
  check(): boolean {
    const now = Date.now()
    // Remove old requests outside the window
    this.requests = this.requests.filter(
      (t) => now - t < API_CONFIG.RATE_LIMIT_WINDOW_MS
    )
    return this.requests.length < API_CONFIG.MAX_REQUESTS_PER_MINUTE
  },
  record(): void {
    this.requests.push(Date.now())
  },
  remaining(): number {
    const now = Date.now()
    this.requests = this.requests.filter(
      (t) => now - t < API_CONFIG.RATE_LIMIT_WINDOW_MS
    )
    return API_CONFIG.MAX_REQUESTS_PER_MINUTE - this.requests.length
  },
}

interface McpSearchRequest {
  jsonrpc: string
  id: number
  method: string
  params: {
    name: string
    arguments: {
      query: string
      numResults?: number
      livecrawl?: "fallback" | "preferred"
      type?: "auto" | "fast" | "deep"
      contextMaxCharacters?: number
    }
  }
}

interface McpSearchResponse {
  jsonrpc: string
  result: {
    content: Array<{
      type: string
      text: string
    }>
  }
}

export const WebSearchTool = Tool.define("websearch", {
  description: DESCRIPTION,
  parameters: z.object({
    query: z.string().describe("Websearch query"),
    numResults: z
      .number()
      .optional()
      .describe("Number of search results to return (default: 8, max: 20)"),
    livecrawl: z
      .enum(["fallback", "preferred"])
      .optional()
      .describe(
        "Live crawl mode - 'fallback': use cached if available, 'preferred': prioritize live crawling (default: 'fallback')"
      ),
    type: z
      .enum(["auto", "fast", "deep"])
      .optional()
      .describe(
        "Search type - 'auto': balanced (default), 'fast': quick results, 'deep': comprehensive"
      ),
    contextMaxCharacters: z
      .number()
      .optional()
      .describe("Maximum characters for context per result (default: 10000)"),
  }),
  async execute(params, ctx) {
    // Rate limit check
    if (!rateLimiter.check()) {
      const remaining = rateLimiter.remaining()
      throw new Error(
        `Rate limit exceeded. Max ${API_CONFIG.MAX_REQUESTS_PER_MINUTE} searches per minute. ` +
          `${remaining} remaining. Please wait before searching again.`
      )
    }

    const cfg = await Config.get()
    if (cfg.permission?.webfetch === "ask")
      await Permission.ask({
        type: "websearch",
        sessionID: ctx.sessionID,
        messageID: ctx.messageID,
        callID: ctx.callID,
        title: "Search web for: " + params.query,
        metadata: {
          query: params.query,
          numResults: params.numResults,
          livecrawl: params.livecrawl,
          type: params.type,
          contextMaxCharacters: params.contextMaxCharacters,
        },
      })

    // Record this request for rate limiting
    rateLimiter.record()

    const searchRequest: McpSearchRequest = {
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: {
        name: "web_search_exa",
        arguments: {
          query: params.query,
          type: params.type || "auto",
          numResults: Math.min(params.numResults || API_CONFIG.DEFAULT_NUM_RESULTS, 20),
          livecrawl: params.livecrawl || "fallback",
          contextMaxCharacters: params.contextMaxCharacters,
        },
      },
    }

    const controller = new AbortController()
    const timeoutId = setTimeout(() => controller.abort(), 25000)

    try {
      const headers: Record<string, string> = {
        accept: "application/json, text/event-stream",
        "content-type": "application/json",
      }

      const response = await fetch(
        `${API_CONFIG.BASE_URL}${API_CONFIG.ENDPOINTS.SEARCH}`,
        {
          method: "POST",
          headers,
          body: JSON.stringify(searchRequest),
          signal: AbortSignal.any([controller.signal, ctx.abort]),
        }
      )

      clearTimeout(timeoutId)

      if (!response.ok) {
        const errorText = await response.text()
        throw new Error(`Search error (${response.status}): ${errorText}`)
      }

      const responseText = await response.text()

      // Parse SSE response
      const lines = responseText.split("\n")
      for (const line of lines) {
        if (line.startsWith("data: ")) {
          const data: McpSearchResponse = JSON.parse(line.substring(6))
          if (data.result && data.result.content && data.result.content.length > 0) {
            return {
              output: data.result.content[0].text,
              title: `Web search: ${params.query}`,
              metadata: {
                rateLimitRemaining: rateLimiter.remaining(),
              },
            }
          }
        }
      }

      return {
        output: "No search results found. Please try a different query.",
        title: `Web search: ${params.query}`,
        metadata: {},
      }
    } catch (error) {
      clearTimeout(timeoutId)

      if (error instanceof Error && error.name === "AbortError") {
        throw new Error("Search request timed out")
      }

      throw error
    }
  },
})
