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
  // Context limits optimized for LLM consumption
  // Exa recommends 10000+ for best RAG results, but we balance with context window
  DEFAULT_CONTEXT_CHARS: 10000,
  COMPACT_CONTEXT_CHARS: 5000,
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
    highlights?: boolean | { query?: string; numSentences?: number; highlightsPerUrl?: number }
    summary?: boolean | { query?: string }
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
    mode: z
      .enum(["smart", "detailed"])
      .optional()
      .describe(
        "Content mode - 'smart': highlights + summaries for compact results (default), 'detailed': full text context"
      ),
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

    const mode = params.mode || "smart"
    const numResults = Math.min(params.numResults || API_CONFIG.DEFAULT_NUM_RESULTS, 100)

    // Build request per Exa API spec
    // "smart" mode: Use highlights + summary for compact, high-quality extraction
    // "detailed" mode: Use full context string for comprehensive results
    const searchRequest: ExaSearchRequest = {
      query: params.query,
      type: params.type || "auto",
      numResults,
      contents: {
        livecrawl: params.livecrawl || "fallback",
        ...(mode === "smart"
          ? {
              // Smart mode: Exa extracts key highlights and generates summaries
              // Much more compact but retains the most relevant information
              highlights: { highlightsPerUrl: 3, numSentences: 2 },
              summary: true,
              text: { maxCharacters: 1500 }, // Backup text, limited
            }
          : {
              // Detailed mode: Full text with context string
              text: true,
            }),
      },
      // Context string for RAG - only in detailed mode or as backup
      ...(mode === "detailed" && {
        context: {
          maxCharacters: params.contextMaxCharacters || API_CONFIG.DEFAULT_CONTEXT_CHARS,
        },
      }),
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

      // Detailed mode: prefer context string (best for comprehensive LLM consumption)
      if (mode === "detailed" && data.context) {
        return {
          output: data.context,
          title: `Web search: ${params.query}`,
          metadata: {
            mode: "detailed",
            requestId: data.requestId,
            resultCount: data.results?.length || 0,
            searchType: data.resolvedSearchType,
            rateLimitRemaining: rateLimiter.remaining(),
            cost: data.costDollars?.total,
          },
        }
      }

      // Smart mode: format with highlights + summaries (compact but high quality)
      if (data.results && data.results.length > 0) {
        const formattedResults = data.results
          .map((result) => {
            const parts = [`**${result.title}**`, `URL: ${result.url}`]

            if (result.publishedDate) {
              parts.push(`Published: ${result.publishedDate}`)
            }
            if (result.author) {
              parts.push(`Author: ${result.author}`)
            }

            // Prefer summary (LLM-generated, very compact)
            if (result.summary) {
              parts.push(`\nSummary: ${result.summary}`)
            }

            // Add highlights (key excerpts)
            if (result.highlights && result.highlights.length > 0) {
              parts.push(`\nKey excerpts:\n${result.highlights.map((h) => `• ${h}`).join("\n")}`)
            }

            // Fallback to truncated text if no summary/highlights
            if (!result.summary && (!result.highlights || result.highlights.length === 0) && result.text) {
              const truncated = result.text.length > 500 ? result.text.slice(0, 500) + "..." : result.text
              parts.push(`\n${truncated}`)
            }

            return parts.join("\n")
          })
          .join("\n\n---\n\n")

        return {
          output: formattedResults,
          title: `Web search: ${params.query}`,
          metadata: {
            mode,
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
          mode,
          requestId: data.requestId,
          resultCount: 0,
          searchType: data.resolvedSearchType,
          rateLimitRemaining: rateLimiter.remaining(),
          cost: data.costDollars?.total,
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
