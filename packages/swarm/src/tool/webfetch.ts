import z from "zod"
import { Tool } from "./tool"
import TurndownService from "turndown"
import DESCRIPTION from "./webfetch.txt"
import { Config } from "../config/config"
import { Permission } from "../permission"

const MAX_RESPONSE_SIZE = 5 * 1024 * 1024 // 5MB
const DEFAULT_TIMEOUT = 30 * 1000 // 30 seconds
const MAX_TIMEOUT = 120 * 1000 // 2 minutes
const DEFAULT_MAX_CHARS = 50000 // Default output limit to prevent context blowup
const MAX_CHARS_LIMIT = 200000 // Absolute max to prevent extreme outputs

export const WebFetchTool = Tool.define("webfetch", {
  description: DESCRIPTION,
  parameters: z.object({
    url: z.string().describe("The URL to fetch content from"),
    format: z
      .enum(["text", "markdown", "html"])
      .describe("The format to return the content in (text, markdown, or html)"),
    timeout: z.number().describe("Optional timeout in seconds (max 120)").optional(),
    maxCharacters: z
      .number()
      .optional()
      .describe("Maximum characters to return (default: 50000, max: 200000). Use lower values for faster processing."),
  }),
  async execute(params, ctx) {
    // Validate URL
    if (!params.url.startsWith("http://") && !params.url.startsWith("https://")) {
      throw new Error("URL must start with http:// or https://")
    }

    const cfg = await Config.get()
    if (cfg.permission?.webfetch === "ask")
      await Permission.ask({
        type: "webfetch",
        sessionID: ctx.sessionID,
        messageID: ctx.messageID,
        callID: ctx.callID,
        title: "Fetch content from: " + params.url,
        metadata: {
          url: params.url,
          format: params.format,
          timeout: params.timeout,
        },
      })

    const timeout = Math.min((params.timeout ?? DEFAULT_TIMEOUT / 1000) * 1000, MAX_TIMEOUT)

    const controller = new AbortController()
    const timeoutId = setTimeout(() => controller.abort(), timeout)

    // Build Accept header based on requested format with q parameters for fallbacks
    let acceptHeader = "*/*"
    switch (params.format) {
      case "markdown":
        acceptHeader = "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
        break
      case "text":
        acceptHeader = "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
        break
      case "html":
        acceptHeader = "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
        break
      default:
        acceptHeader =
          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
    }

    const response = await fetch(params.url, {
      signal: AbortSignal.any([controller.signal, ctx.abort]),
      headers: {
        "User-Agent":
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
        Accept: acceptHeader,
        "Accept-Language": "en-US,en;q=0.9",
      },
    })

    clearTimeout(timeoutId)

    if (!response.ok) {
      throw new Error(`Request failed with status code: ${response.status}`)
    }

    // Check content length
    const contentLength = response.headers.get("content-length")
    if (contentLength && parseInt(contentLength) > MAX_RESPONSE_SIZE) {
      throw new Error("Response too large (exceeds 5MB limit)")
    }

    const arrayBuffer = await response.arrayBuffer()
    if (arrayBuffer.byteLength > MAX_RESPONSE_SIZE) {
      throw new Error("Response too large (exceeds 5MB limit)")
    }

    const rawContent = new TextDecoder().decode(arrayBuffer)
    const contentType = response.headers.get("content-type") || ""

    const title = `${params.url} (${contentType})`

    const bytes = arrayBuffer.byteLength
    const maxChars = Math.min(params.maxCharacters ?? DEFAULT_MAX_CHARS, MAX_CHARS_LIMIT)

    // Helper to truncate content intelligently (try to break at sentence/paragraph)
    const truncate = (text: string, limit: number): { text: string; truncated: boolean } => {
      if (text.length <= limit) return { text, truncated: false }

      // Try to break at paragraph
      let breakPoint = text.lastIndexOf("\n\n", limit)
      if (breakPoint < limit * 0.7) {
        // Try sentence break
        breakPoint = text.lastIndexOf(". ", limit)
      }
      if (breakPoint < limit * 0.7) {
        // Try any newline
        breakPoint = text.lastIndexOf("\n", limit)
      }
      if (breakPoint < limit * 0.7) {
        // Fall back to hard cut
        breakPoint = limit
      }

      return {
        text: text.slice(0, breakPoint) + "\n\n[Content truncated - " + (text.length - breakPoint).toLocaleString() + " more characters available]",
        truncated: true,
      }
    }

    // Handle content based on requested format and actual content type
    let processedContent: string
    switch (params.format) {
      case "markdown":
        processedContent = contentType.includes("text/html")
          ? convertHTMLToMarkdown(rawContent)
          : rawContent
        break

      case "text":
        processedContent = contentType.includes("text/html")
          ? await extractTextFromHTML(rawContent)
          : rawContent
        break

      case "html":
      default:
        processedContent = rawContent
        break
    }

    const { text: finalOutput, truncated } = truncate(processedContent, maxChars)

    return {
      output: finalOutput,
      title,
      metadata: {
        bytes,
        contentType,
        originalLength: processedContent.length,
        truncated,
        ...(truncated && { maxCharacters: maxChars }),
      },
    }
  },
})

async function extractTextFromHTML(html: string) {
  let text = ""
  let skipContent = false

  const rewriter = new HTMLRewriter()
    .on("script, style, noscript, iframe, object, embed", {
      element() {
        skipContent = true
      },
      text() {
        // Skip text content inside these elements
      },
    })
    .on("*", {
      element(element) {
        // Reset skip flag when entering other elements
        if (!["script", "style", "noscript", "iframe", "object", "embed"].includes(element.tagName)) {
          skipContent = false
        }
      },
      text(input) {
        if (!skipContent) {
          text += input.text
        }
      },
    })
    .transform(new Response(html))

  await rewriter.text()
  return text.trim()
}

function convertHTMLToMarkdown(html: string): string {
  const turndownService = new TurndownService({
    headingStyle: "atx",
    hr: "---",
    bulletListMarker: "-",
    codeBlockStyle: "fenced",
    emDelimiter: "*",
  })
  turndownService.remove(["script", "style", "meta", "link"])
  return turndownService.turndown(html)
}
