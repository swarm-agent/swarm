/**
 * Fast codebase tree explorer tool
 * Cross-platform, filters noise, handles large outputs
 */

import z from "zod"
import * as fs from "fs"
import * as path from "path"
import { Tool } from "./tool"
import { Instance } from "../project/instance"
import DESCRIPTION from "./explore-tree.txt"

// Default ignore patterns (common noise directories)
const DEFAULT_IGNORE = new Set([
  "node_modules",
  ".git",
  "dist",
  ".bun",
  ".turbo",
  ".next",
  ".nuxt",
  ".output",
  ".cache",
  ".npm-cache",
  "__pycache__",
  ".pytest_cache",
  "venv",
  ".venv",
  "target", // Rust
  "build",
  "out",
  ".idea",
  ".vscode",
  "coverage",
  ".nyc_output",
  ".parcel-cache",
  ".webpack",
  ".rollup.cache",
])

// File patterns to ignore
const IGNORE_FILES = new Set([
  "bun.lock",
  "package-lock.json",
  "yarn.lock",
  "pnpm-lock.yaml",
  ".DS_Store",
  "Thumbs.db",
])

interface TreeNode {
  name: string
  type: "dir" | "file"
  children?: TreeNode[]
}

interface ExploreOptions {
  root: string
  maxDepth?: number
  ignore?: string[]
  maxEntries?: number
  maxChars?: number
}

interface ExploreResult {
  tree: string
  stats: {
    directories: number
    files: number
    truncated: boolean
    truncatedReason?: string
  }
}

/**
 * Build a tree structure from a directory
 */
function buildTree(
  dir: string,
  ignoreSet: Set<string>,
  maxDepth: number,
  currentDepth: number,
  stats: { dirs: number; files: number; entries: number },
  maxEntries: number
): TreeNode[] {
  if (currentDepth > maxDepth || stats.entries >= maxEntries) {
    return []
  }

  let entries: fs.Dirent[]
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true })
  } catch {
    return []
  }

  // Sort: directories first, then files, alphabetically
  entries.sort((a, b) => {
    if (a.isDirectory() && !b.isDirectory()) return -1
    if (!a.isDirectory() && b.isDirectory()) return 1
    return a.name.localeCompare(b.name)
  })

  const nodes: TreeNode[] = []

  for (const entry of entries) {
    if (stats.entries >= maxEntries) break

    // Skip ignored patterns
    if (ignoreSet.has(entry.name)) continue
    if (IGNORE_FILES.has(entry.name)) continue
    // Allow .swarm, .env, .gitignore etc but skip hidden dirs
    if (entry.name.startsWith(".") && entry.isDirectory() && 
        !["swarm", "github", "gitlab"].some(n => entry.name === `.${n}`)) continue

    stats.entries++

    if (entry.isDirectory()) {
      stats.dirs++
      const children = buildTree(
        path.join(dir, entry.name),
        ignoreSet,
        maxDepth,
        currentDepth + 1,
        stats,
        maxEntries
      )
      nodes.push({
        name: entry.name,
        type: "dir",
        children: children.length > 0 ? children : undefined,
      })
    } else if (entry.isFile()) {
      stats.files++
      nodes.push({
        name: entry.name,
        type: "file",
      })
    }
  }

  return nodes
}

/**
 * Render tree to string format
 */
function renderTree(nodes: TreeNode[], prefix: string = ""): string {
  let result = ""

  for (let i = 0; i < nodes.length; i++) {
    const node = nodes[i]
    const isLast = i === nodes.length - 1
    const connector = isLast ? "└── " : "├── "
    const childPrefix = isLast ? "    " : "│   "

    result += prefix + connector + node.name + (node.type === "dir" ? "/" : "") + "\n"

    if (node.children && node.children.length > 0) {
      result += renderTree(node.children, prefix + childPrefix)
    }
  }

  return result
}

/**
 * Explore a codebase and return a tree structure
 */
export function exploreTree(options: ExploreOptions): ExploreResult {
  const {
    root,
    maxDepth = 10,
    ignore = [],
    maxEntries = 1000,
    maxChars = 50000,
  } = options

  // Merge ignore patterns
  const ignoreSet = new Set([...DEFAULT_IGNORE, ...ignore])

  const stats = { dirs: 0, files: 0, entries: 0 }

  const tree = buildTree(root, ignoreSet, maxDepth, 0, stats, maxEntries)

  const rootName = path.basename(root) || root
  let rendered = rootName + "/\n" + renderTree(tree)

  let truncatedReason: string | undefined

  // Check entry truncation
  if (stats.entries >= maxEntries) {
    truncatedReason = `max entries (${maxEntries})`
  }

  // Check character truncation
  if (rendered.length > maxChars) {
    rendered = rendered.slice(0, maxChars) + "\n... [truncated at " + maxChars + " chars]"
    truncatedReason = `max chars (${maxChars})`
  }

  return {
    tree: rendered,
    stats: {
      directories: stats.dirs,
      files: stats.files,
      truncated: !!truncatedReason,
      truncatedReason,
    },
  }
}

/**
 * The explore-tree tool for agents
 */
export const ExploreTreeTool = Tool.define("explore-tree", {
  description: DESCRIPTION,
  parameters: z.object({
    path: z
      .string()
      .describe("Directory to explore (absolute path, or relative to project root)")
      .optional(),
    depth: z
      .number()
      .describe("Maximum depth to traverse (default: 8)")
      .optional(),
    maxEntries: z
      .number()
      .describe("Maximum entries before truncating (default: 500)")
      .optional(),
  }),
  async execute(params) {
    const searchPath = path.resolve(Instance.directory, params.path || ".")

    const result = exploreTree({
      root: searchPath,
      maxDepth: params.depth ?? 8,
      maxEntries: params.maxEntries ?? 500,
      maxChars: 40000, // ~10K tokens safe limit
    })

    const summary = `${result.stats.directories} directories, ${result.stats.files} files${result.stats.truncated ? ` (${result.stats.truncatedReason})` : ""}`

    return {
      title: path.relative(Instance.worktree, searchPath) || ".",
      metadata: {
        directories: result.stats.directories,
        files: result.stats.files,
        truncated: result.stats.truncated,
      },
      output: result.tree + "\n" + summary,
    }
  },
})

// Quick test if run directly
if (import.meta.main) {
  const result = exploreTree({
    root: process.cwd(),
    maxDepth: 8,
    maxEntries: 500,
  })

  console.log(result.tree)
  console.log("\n---")
  console.log(`Directories: ${result.stats.directories}`)
  console.log(`Files: ${result.stats.files}`)
  console.log(`Truncated: ${result.stats.truncated}`)
}
