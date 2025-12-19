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
  ids: string[] // URLs
  text?: boolean | { maxCharacters?: number }
  livecrawl?: "never" | "fallback" | "preferred" | "always"
}

interface ExaContentsResponse {
  results: Array<{
    id: string
    url: string
    title: string
    text?: string
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
      .describe("Maximum characters per page (default: 10000)"),
    livecrawl: z
      .enum(["fallback", "preferred"])
      .optional()
      .describe("Live crawl mode - 'fallback': use cached, 'preferred': prioritize live (default: 'fallback')"),
  }),
  async execute(params, ctx) {
    const apiKey = await getExaApiKey()
    if (!apiKey) {
      return {
        title: "Exa API key not configured",
        output:
          "Web contents requires an Exa API key. Add it with: swarm auth add exa <your-api-key>",
        metadata: { error: true },
      }
    }

    // Limit URLs
    const urls = params.urls.slice(0, API_CONFIG.MAX_URLS)
    if (urls.length === 0) {
      return {
        title: "No URLs provided",
        output: "Please provide at least one URL to fetch.",
        metadata: { error: true },
      }
    }

    try {
      const request: ExaContentsRequest = {
        ids: urls,
        text: { maxCharacters: params.maxCharacters ?? API_CONFIG.DEFAULT_TEXT_LENGTH },
        livecrawl: params.livecrawl ?? "fallback",
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
          metadata: { error: true, status: response.status },
        }
      }

      const data = (await response.json()) as ExaContentsResponse

      // Format results
      const results = data.results.map((result) => ({
        url: result.url,
        title: result.title,
        text: result.text ?? "(no content)",
        publishedDate: result.publishedDate,
        author: result.author,
      }))

      // Build output
      let output = `Fetched ${results.length} page(s):\n\n`
      for (const result of results) {
        output += `## ${result.title}\n`
        output += `URL: ${result.url}\n`
        if (result.author) output += `Author: ${result.author}\n`
        if (result.publishedDate) output += `Date: ${result.publishedDate}\n`
        output += `\n${result.text}\n\n---\n\n`
      }

      return {
        title: `Fetched ${results.length} page(s)`,
        output,
        metadata: {
          urlsFetched: results.length,
          urls: results.map((r) => r.url),
        },
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      return {
        title: "Web contents failed",
        output: `Error fetching contents: ${message}`,
        metadata: { error: true },
      }
    }
  },
})
