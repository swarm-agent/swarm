import { z } from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./webcontents.txt"
import { Auth } from "../auth"

const API_CONFIG = {
  BASE_URL: "https://api.exa.ai",
  ENDPOINTS: {
    CONTENTS: "/contents",
  },
  MAX_URLS: 5,
  DEFAULT_TEXT_LENGTH: 10000,
  SMART_TEXT_LENGTH: 3000,
} as const

/**
 * Get the Exa API key from auth.json or environment variable
 */
async function getExaApiKey(): Promise<string | undefined> {
  const authInfo = await Auth.get("exa")
  if (authInfo?.type === "api" && authInfo.key) {
    return authInfo.key
  }
  return process.env.EXA_API_KEY
}

/**
 * Check if Exa API key is configured
 */
export async function isExaConfigured(): Promise<boolean> {
  const key = await getExaApiKey()
  return !!key
}

interface ExaContentsRequest {
  urls: string[]
  text?: boolean | { maxCharacters?: number }
  highlights?: boolean | { query?: string; numSentences?: number; highlightsPerUrl?: number }
  summary?: boolean | { query?: string }
  livecrawl?: "never" | "fallback" | "preferred" | "always"
}

interface ExaContentsResponse {
  results: Array<{
    id: string
    url: string
    title: string
    text?: string
    highlights?: string[]
    highlightScores?: number[]
    summary?: string
    publishedDate?: string | null
    author?: string | null
  }>
}

export const WebContentsTool = Tool.define("webcontents", {
  description: DESCRIPTION,
  parameters: z.object({
    urls: z.array(z.string()).describe("URLs to fetch content from (max 5)"),
    maxCharacters: z
      .number()
      .optional()
      .describe("Maximum characters per page (default: 10000 in full mode, 3000 in smart mode)"),
    livecrawl: z
      .enum(["fallback", "preferred"])
      .optional()
      .describe("Live crawl mode - 'fallback': use cached, 'preferred': prioritize live (default: 'fallback')"),
    mode: z
      .enum(["smart", "full"])
      .optional()
      .describe(
        "Content mode - 'smart': summaries + highlights for compact results (default), 'full': complete text"
      ),
  }),
  async execute(params, ctx) {
    const apiKey = await getExaApiKey()
    if (!apiKey) {
      return {
        title: "Exa API key not configured",
        output:
          "Web contents requires an Exa API key. Add it with: swarm auth add exa <your-api-key>",
        metadata: { error: true, mode: "smart", urlsFetched: 0, urls: [] as string[] },
      }
    }

    // Limit URLs
    const urls = params.urls.slice(0, API_CONFIG.MAX_URLS)
    if (urls.length === 0) {
      return {
        title: "No URLs provided",
        output: "Please provide at least one URL to fetch.",
        metadata: { error: true, mode: "smart", urlsFetched: 0, urls: [] as string[] },
      }
    }

    const mode = params.mode ?? "smart"

    try {
      // Smart mode: use summaries + highlights for compact, high-quality extraction
      // Full mode: fetch complete text content
      const request: ExaContentsRequest = {
        urls,
        livecrawl: params.livecrawl ?? "fallback",
        ...(mode === "smart"
          ? {
              summary: true,
              highlights: { highlightsPerUrl: 5, numSentences: 2 },
              text: { maxCharacters: params.maxCharacters ?? API_CONFIG.SMART_TEXT_LENGTH },
            }
          : {
              text: { maxCharacters: params.maxCharacters ?? API_CONFIG.DEFAULT_TEXT_LENGTH },
            }),
      }

      const response = await fetch(`${API_CONFIG.BASE_URL}${API_CONFIG.ENDPOINTS.CONTENTS}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "x-api-key": apiKey,
        },
        body: JSON.stringify(request),
      })

      if (!response.ok) {
        const errorText = await response.text()
        return {
          title: `Exa API error: ${response.status}`,
          output: `Failed to fetch contents: ${errorText}`,
          metadata: { error: true, mode, urlsFetched: 0, urls: [] as string[] },
        }
      }

      const data = (await response.json()) as ExaContentsResponse

      // Build output based on mode
      let output = `Fetched ${data.results.length} page(s):\n\n`

      for (const result of data.results) {
        output += `## ${result.title}\n`
        output += `URL: ${result.url}\n`
        if (result.author) output += `Author: ${result.author}\n`
        if (result.publishedDate) output += `Date: ${result.publishedDate}\n`

        if (mode === "smart") {
          // Smart mode: summary first, then highlights, then truncated text as fallback
          if (result.summary) {
            output += `\n**Summary:** ${result.summary}\n`
          }
          if (result.highlights && result.highlights.length > 0) {
            output += `\n**Key excerpts:**\n${result.highlights.map((h) => `• ${h}`).join("\n")}\n`
          }
          // Only include text if no summary/highlights available
          if (!result.summary && (!result.highlights || result.highlights.length === 0)) {
            const text = result.text ?? "(no content)"
            output += `\n${text}\n`
          }
        } else {
          // Full mode: complete text
          output += `\n${result.text ?? "(no content)"}\n`
        }

        output += `\n---\n\n`
      }

      return {
        title: `Fetched ${data.results.length} page(s)`,
        output,
        metadata: {
          mode,
          urlsFetched: data.results.length,
          urls: data.results.map((r) => r.url),
        },
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      return {
        title: "Web contents failed",
        output: `Error fetching contents: ${message}`,
        metadata: { error: true, mode, urlsFetched: 0, urls: [] as string[] },
      }
    }
  },
})
