import z from "zod"
import * as fs from "fs"
import * as path from "path"
import { Tool } from "./tool"
import { LSP } from "../lsp"
import { FileTime } from "../file/time"
import DESCRIPTION from "./read.txt"
import { Filesystem } from "../util/filesystem"
import { Instance } from "../project/instance"
import { Provider } from "../provider/provider"
import { Identifier } from "../id/id"
import { Permission } from "../permission"
import { Agent } from "@/agent/agent"
import { isInWorkspaceDir } from "@/cli/cmd/tui/context/workspace"

const DEFAULT_READ_LIMIT = 2000
const MAX_LINE_LENGTH = 2000
const BATCH_LINES_PER_FILE = 500
const BATCH_MAX_LINE_LENGTH = 1000
const BATCH_MAX_TOTAL_CHARS = 150000
const BATCH_MAX_FILES = 20

const BINARY_EXTS = new Set([
  ".zip", ".tar", ".gz", ".exe", ".dll", ".so", ".bin", ".wasm", ".pyc",
  ".class", ".jar", ".war", ".7z", ".doc", ".docx", ".xls", ".xlsx",
  ".ppt", ".pptx", ".odt", ".ods", ".odp", ".dat", ".obj", ".o", ".a", ".lib", ".pyo",
])

// Schema for batch mode path entries
const PathEntrySchema = z.object({
  path: z.string().describe("File path to read"),
  offset: z.coerce.number().optional().describe("Line offset (0-based)"),
  limit: z.coerce.number().optional().describe("Number of lines to read"),
})

export const ReadTool = Tool.define("read", {
  description: DESCRIPTION,
  parameters: z.object({
    filePath: z.string().describe("The path to the file to read").optional(),
    offset: z.coerce.number().describe("The line number to start reading from (0-based)").optional(),
    limit: z.coerce.number().describe("The number of lines to read (defaults to 2000)").optional(),
    paths: z.array(PathEntrySchema).max(BATCH_MAX_FILES).optional()
      .describe(`Array of files to read in parallel (max ${BATCH_MAX_FILES}). Each entry can have its own offset/limit.`),
  }),
  async execute(params, ctx) {
    // Validate: must have either filePath or paths, not both
    if (params.filePath && params.paths) {
      throw new Error("Cannot specify both 'filePath' and 'paths'. Use 'filePath' for single file or 'paths' for batch.")
    }
    if (!params.filePath && !params.paths) {
      throw new Error("Must specify either 'filePath' for single file or 'paths' for batch read.")
    }

    // BATCH MODE
    if (params.paths) {
      return executeBatch(params.paths, ctx)
    }

    // SINGLE FILE MODE (full features: images, LSP, permissions, suggestions)
    return executeSingle(params.filePath!, params.offset, params.limit, ctx)
  },
})

// Single file read with full features
async function executeSingle(
  filePath: string,
  offset: number | undefined,
  limit: number | undefined,
  ctx: Tool.Context
) {
  let filepath = filePath
  if (!path.isAbsolute(filepath)) {
    filepath = path.join(process.cwd(), filepath)
  }
  const title = path.relative(Instance.worktree, filepath)
  const agent = await Agent.get(ctx.agent, { sessionID: ctx.sessionID })

  if (!ctx.extra?.["bypassCwdCheck"] && !Filesystem.contains(Instance.directory, filepath)) {
    const inWorkspace = await isInWorkspaceDir(filepath)
    if (!inWorkspace) {
      const parentDir = path.dirname(filepath)
      if (agent.permission.external_directory === "ask") {
        await Permission.ask({
          type: "external_directory",
          pattern: parentDir,
          sessionID: ctx.sessionID,
          messageID: ctx.messageID,
          callID: ctx.callID,
          title: `Access file outside working directory: ${filepath}`,
          metadata: {
            filepath,
            parentDir,
          },
        })
      }
    }
  }

  const file = Bun.file(filepath)
  if (!(await file.exists())) {
    const dir = path.dirname(filepath)
    const base = path.basename(filepath)

    try {
      const dirEntries = fs.readdirSync(dir)
      const suggestions = dirEntries
        .filter(
          (entry) =>
            entry.toLowerCase().includes(base.toLowerCase()) || base.toLowerCase().includes(entry.toLowerCase()),
        )
        .map((entry) => path.join(dir, entry))
        .slice(0, 3)

      if (suggestions.length > 0) {
        throw new Error(`File not found: ${filepath}\n\nDid you mean one of these?\n${suggestions.join("\n")}`)
      }
    } catch (e) {
      // Directory doesn't exist either
    }

    throw new Error(`File not found: ${filepath}`)
  }

  const isImage = isImageFile(filepath)
  const supportsImages = await (async () => {
    if (!ctx.extra?.["providerID"] || !ctx.extra?.["modelID"]) return false
    const providerID = ctx.extra["providerID"] as string
    const modelID = ctx.extra["modelID"] as string
    const model = await Provider.getModel(providerID, modelID).catch(() => undefined)
    if (!model) return false
    return model.info.modalities?.input?.includes("image") ?? false
  })()
  
  if (isImage) {
    if (!supportsImages) {
      throw new Error(`Failed to read image: ${filepath}, model may not be able to read images`)
    }
    const mime = file.type
    const msg = "Image read successfully"
    return {
      title,
      output: msg,
      metadata: {
        preview: msg,
      },
      attachments: [
        {
          id: Identifier.ascending("part"),
          sessionID: ctx.sessionID,
          messageID: ctx.messageID,
          type: "file" as const,
          mime,
          url: `data:${mime};base64,${Buffer.from(await file.bytes()).toString("base64")}`,
        },
      ],
    }
  }

  const isBinary = await isBinaryFile(filepath, file)
  if (isBinary) throw new Error(`Cannot read binary file: ${filepath}`)

  const readLimit = limit ?? DEFAULT_READ_LIMIT
  const readOffset = offset || 0
  const lines = await file.text().then((text) => text.split("\n"))
  const raw = lines.slice(readOffset, readOffset + readLimit).map((line) => {
    return line.length > MAX_LINE_LENGTH ? line.substring(0, MAX_LINE_LENGTH) + "..." : line
  })
  const content = raw.map((line, index) => {
    return `${(index + readOffset + 1).toString().padStart(5, "0")}| ${line}`
  })
  const preview = raw.slice(0, 20).join("\n")

  let output = "<file>\n"
  output += content.join("\n")

  if (lines.length > readOffset + content.length) {
    output += `\n\n(File has more lines. Use 'offset' parameter to read beyond line ${readOffset + content.length})`
  }
  output += "\n</file>"

  // Warm LSP client
  LSP.touchFile(filepath, false)
  FileTime.read(ctx.sessionID, filepath)

  return {
    title,
    output,
    metadata: {
      preview,
    },
  }
}

// Batch read - parallel, fast, with per-file offset/limit
async function executeBatch(
  paths: Array<{ path: string; offset?: number; limit?: number }>,
  ctx: Tool.Context
) {
  type FileResult =
    | { path: string; error: string }
    | { path: string; content: string; lines: number; totalLines: number }

  const results = await Promise.all(
    paths.map(async (entry): Promise<FileResult> => {
      try {
        let filepath = entry.path
        if (!path.isAbsolute(filepath)) {
          filepath = path.join(process.cwd(), filepath)
        }

        // Security check
        if (!Filesystem.contains(Instance.directory, filepath)) {
          const inWorkspace = await isInWorkspaceDir(filepath)
          if (!inWorkspace) {
            return { path: entry.path, error: "File outside working directory" }
          }
        }

        // Skip binary files
        const ext = path.extname(filepath).toLowerCase()
        if (BINARY_EXTS.has(ext)) {
          return { path: entry.path, error: "Binary file skipped" }
        }

        const file = Bun.file(filepath)
        if (!(await file.exists())) {
          return { path: entry.path, error: "File not found" }
        }

        // Fast read with ArrayBuffer
        const buf = await file.arrayBuffer()
        const text = new TextDecoder().decode(buf)

        const allLines = text.split("\n")
        const totalLines = allLines.length
        
        // Apply per-file offset/limit
        const fileOffset = entry.offset ?? 0
        const fileLimit = entry.limit ?? BATCH_LINES_PER_FILE
        const slicedLines = allLines.slice(fileOffset, fileOffset + fileLimit)

        // Build formatted content
        let content = ""
        for (let i = 0; i < slicedLines.length; i++) {
          const line = slicedLines[i]
          const truncated = line.length > BATCH_MAX_LINE_LENGTH 
            ? line.slice(0, BATCH_MAX_LINE_LENGTH) + "..." 
            : line
          content += `${String(i + fileOffset + 1).padStart(5)}| ${truncated}\n`
        }

        if (totalLines > fileOffset + slicedLines.length) {
          content += `... (${totalLines - fileOffset - slicedLines.length} more lines)`
        }

        FileTime.read(ctx.sessionID, filepath)

        return {
          path: entry.path,
          content,
          lines: slicedLines.length,
          totalLines,
        }
      } catch (e) {
        return {
          path: entry.path,
          error: e instanceof Error ? e.message : "Unknown error",
        }
      }
    }),
  )

  // Build output with size tracking
  const sections: string[] = []
  let totalChars = 0
  let successCount = 0

  for (const result of results) {
    if ("error" in result) {
      sections.push(`### ${result.path}\nERROR: ${result.error}`)
    } else {
      successCount++
      const header = `### ${result.path} (${result.lines}/${result.totalLines} lines)`
      const section = `${header}\n${result.content}`

      if (totalChars + section.length > BATCH_MAX_TOTAL_CHARS) {
        sections.push(`### ${result.path}\n(skipped - output limit reached)`)
      } else {
        sections.push(section)
        totalChars += section.length
      }
    }
  }

  const title = `Read ${successCount}/${paths.length} files`

  return {
    title,
    output: sections.join("\n\n"),
    metadata: {
      preview: title,
      filesRead: successCount,
      filesRequested: paths.length,
    },
  }
}

function isImageFile(filePath: string): string | false {
  const ext = path.extname(filePath).toLowerCase()
  switch (ext) {
    case ".jpg":
    case ".jpeg":
      return "JPEG"
    case ".png":
      return "PNG"
    case ".gif":
      return "GIF"
    case ".bmp":
      return "BMP"
    case ".webp":
      return "WebP"
    default:
      return false
  }
}

async function isBinaryFile(filepath: string, file: Bun.BunFile): Promise<boolean> {
  const ext = path.extname(filepath).toLowerCase()
  if (BINARY_EXTS.has(ext)) return true

  const stat = await file.stat()
  const fileSize = stat.size
  if (fileSize === 0) return false

  const bufferSize = Math.min(4096, fileSize)
  const buffer = await file.arrayBuffer()
  if (buffer.byteLength === 0) return false
  const bytes = new Uint8Array(buffer.slice(0, bufferSize))

  let nonPrintableCount = 0
  for (let i = 0; i < bytes.length; i++) {
    if (bytes[i] === 0) return true
    if (bytes[i] < 9 || (bytes[i] > 13 && bytes[i] < 32)) {
      nonPrintableCount++
    }
  }
  return nonPrintableCount / bytes.length > 0.3
}
