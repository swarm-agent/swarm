import z from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./websearch.txt"
import { Config } from "../config/config"
import { Permission } from "../permission"
import { Auth } from "../auth"

const API_CONFIG = {
  BASE_URL: "https://api.exa.ai",
  ENDPOINTS: {
    SEARCH: "/search",
  },
  DEFAULT_NUM_RESULTS: 8,
  DEFAULT_CONTEXT_CHARS: 10000,
  // Rate limiting - generous for API key users
  MAX_REQUESTS_PER_MINUTE: 10,
  RATE_LIMIT_WINDOW_MS: 60_000,
} as const

/**
 * Get the Exa API key from auth.json or environment variable
 * Returns undefined if not configured (tool will be disabled via registry)
 */
async function getExaApiKey(): Promise<string | undefined> {
  // Check auth.json first (preferred)
  const authInfo = await Auth.get("exa")
  if (authInfo?.type === "api" && authInfo.key) {
    return authInfo.key
  }
  // Fallback to environment variable
  return process.env.EXA_API_KEY
}

/**
 * Check if Exa API key is configured (used by registry for opt-in)
 */
export async function isExaConfigured(): Promise<boolean> {
  const key = await getExaApiKey()
  return !!key
}

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

/**
 * Exa Search API Request (per OpenAPI spec)
 * @see https://docs.exa.ai
 */
interface ExaSearchRequest {
  query: string
  type?: "neural" | "fast" | "auto" | "deep"
  numResults?: number
  contents?: {
    text?: boolean | { maxCharacters?: number }
    livecrawl?: "never" | "fallback" | "preferred" | "always"
  }
  context?: boolean | { maxCharacters?: number }
}

/**
 * Exa Search API Response
 */
interface ExaSearchResponse {
  requestId: string
  resolvedSearchType?: string
  results: Array<{
    title: string
    url: string
    publishedDate?: string | null
    author?: string | null
    id: string
    text?: string
    highlights?: string[]
    highlightScores?: number[]
    summary?: string
  }>
  context?: string
  costDollars?: {
    total: number
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

    // Get API key (should always exist since registry disables tool without it)
    const apiKey = await getExaApiKey()
    if (!apiKey) {
      throw new Error(
        "Exa API key not configured. Add your API key with:\n" +
        "  swarm auth login → Other → exa → paste key\n" +
        "Or set EXA_API_KEY environment variable.\n" +
        "Get your key at: https://exa.ai"
      )
    }

    // Record this request for rate limiting
    rateLimiter.record()

    // Build request per Exa API spec
    const searchRequest: ExaSearchRequest = {
      query: params.query,
      type: params.type || "auto",
      numResults: Math.min(params.numResults || API_CONFIG.DEFAULT_NUM_RESULTS, 100),
      // Use context mode - combines all results into one LLM-friendly string
      context: {
        maxCharacters: params.contextMaxCharacters || API_CONFIG.DEFAULT_CONTEXT_CHARS,
      },
      contents: {
        text: true,
        livecrawl: params.livecrawl || "fallback",
      },
    }

    const controller = new AbortController()
    const timeoutId = setTimeout(() => controller.abort(), 30000)

    try {
      const response = await fetch(
        `${API_CONFIG.BASE_URL}${API_CONFIG.ENDPOINTS.SEARCH}`,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "x-api-key": apiKey,
          },
          body: JSON.stringify(searchRequest),
          signal: AbortSignal.any([controller.signal, ctx.abort]),
        }
      )

      clearTimeout(timeoutId)

      if (!response.ok) {
        const errorText = await response.text()
        throw new Error(`Exa API error (${response.status}): ${errorText}`)
      }

      const data: ExaSearchResponse = await response.json()

      // Prefer context string (best for LLM consumption)
      if (data.context) {
        return {
          output: data.context,
          title: `Web search: ${params.query}`,
          metadata: {
            requestId: data.requestId,
            resultCount: data.results?.length || 0,
            searchType: data.resolvedSearchType,
            rateLimitRemaining: rateLimiter.remaining(),
            cost: data.costDollars?.total,
          },
        }
      }

      // Fallback: format individual results
      if (data.results && data.results.length > 0) {
        const formattedResults = data.results.map((result, i) => {
          const parts = [`Title: ${result.title}`, `URL: ${result.url}`]
          if (result.publishedDate) {
            parts.push(`Published Date: ${result.publishedDate}`)
          }
          if (result.author) {
            parts.push(`Author: ${result.author}`)
          }
          if (result.text) {
            parts.push(`Text: ${result.text}`)
          }
          return parts.join("\n")
        }).join("\n\n---\n\n")

        return {
          output: formattedResults,
          title: `Web search: ${params.query}`,
          metadata: {
            requestId: data.requestId,
            resultCount: data.results.length,
            searchType: data.resolvedSearchType,
            rateLimitRemaining: rateLimiter.remaining(),
            cost: data.costDollars?.total,
          },
        }
      }

      return {
        output: "No search results found. Please try a different query.",
        title: `Web search: ${params.query}`,
        metadata: {
          requestId: data.requestId,
          rateLimitRemaining: rateLimiter.remaining(),
        },
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
