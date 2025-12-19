import { createContext, createSignal, onCleanup, onMount, useContext, type ParentProps } from "solid-js"

export type GitStatusFile = {
  path: string
  status: "added" | "modified" | "deleted" | "renamed" | "copied"
  added: number
  removed: number
}

export type GitCommit = {
  hash: string
  message: string
  author: string
  date: string
  isHead: boolean
}

export type GitStatus = {
  branch: string | null
  dirty: number
  files: string[]
  ahead: number
  behind: number
  enabled: boolean
  // Full status info
  staged: GitStatusFile[]
  unstaged: GitStatusFile[]
  untracked: string[]
  conflicted: string[]
  isClean: boolean
  isRepo: boolean
  // Commit history
  commits: GitCommit[]
}

const context = createContext<{
  status: () => GitStatus
  refresh: () => Promise<void>
}>()

export function useGit() {
  const ctx = useContext(context)
  if (!ctx) throw new Error("useGit must be used within a GitProvider")
  return ctx
}

async function execGit(args: string[]): Promise<string> {
  try {
    const proc = Bun.spawn(["git", ...args], {
      stdout: "pipe",
      stderr: "ignore",
    })
    const text = await new Response(proc.stdout).text()
    await proc.exited
    return text.trim()
  } catch {
    return ""
  }
}

function parseStatusChar(char: string): GitStatusFile["status"] {
  switch (char) {
    case "A":
      return "added"
    case "D":
      return "deleted"
    case "M":
      return "modified"
    case "R":
      return "renamed"
    case "C":
      return "copied"
    default:
      return "modified"
  }
}

async function fetchCommitHistory(): Promise<GitCommit[]> {
  const log = await execGit(["log", "--oneline", "--format=%h|%s|%an|%ar", "-n", "15"])
  if (!log) return []

  const head = await execGit(["rev-parse", "--short", "HEAD"])
  const commits: GitCommit[] = []

  for (const line of log.split("\n").filter(Boolean)) {
    const [hash, message, author, date] = line.split("|")
    if (hash && message) {
      commits.push({
        hash,
        message,
        author: author || "Unknown",
        date: date || "",
        isHead: hash === head,
      })
    }
  }

  return commits
}

async function fetchGitStatus(): Promise<GitStatus> {
  const disabled: GitStatus = {
    branch: null,
    dirty: 0,
    files: [],
    ahead: 0,
    behind: 0,
    enabled: false,
    staged: [],
    unstaged: [],
    untracked: [],
    conflicted: [],
    isClean: true,
    isRepo: false,
    commits: [],
  }

  try {
    // Check if in git repo by trying to get branch
    const branch = await execGit(["branch", "--show-current"])
    if (!branch) {
      return disabled
    }

    // Get ahead/behind counts
    const revListResult = await execGit(["rev-list", "--left-right", "--count", "HEAD...@{u}"])
    const [ahead = 0, behind = 0] = revListResult.split(/\s+/).map((x) => parseInt(x) || 0)

    // Parse git status --porcelain
    const statusResult = await execGit(["status", "--porcelain"])

    const staged: GitStatusFile[] = []
    const unstaged: GitStatusFile[] = []
    const untracked: string[] = []
    const conflicted: string[] = []

    for (const line of statusResult.split("\n").filter(Boolean)) {
      const indexStatus = line[0]
      const worktreeStatus = line[1]
      const filePath = line.slice(3)

      // Untracked files
      if (indexStatus === "?" && worktreeStatus === "?") {
        untracked.push(filePath)
        continue
      }

      // Conflicts
      if (indexStatus === "U" || worktreeStatus === "U" || (indexStatus === "A" && worktreeStatus === "A")) {
        conflicted.push(filePath)
        continue
      }

      // Staged changes
      if (indexStatus !== " " && indexStatus !== "?") {
        staged.push({
          path: filePath,
          status: parseStatusChar(indexStatus),
          added: 0,
          removed: 0,
        })
      }

      // Unstaged changes
      if (worktreeStatus !== " " && worktreeStatus !== "?") {
        unstaged.push({
          path: filePath,
          status: parseStatusChar(worktreeStatus),
          added: 0,
          removed: 0,
        })
      }
    }

    // Get diff stats for staged files
    if (staged.length > 0) {
      const stagedStats = await execGit(["diff", "--cached", "--numstat"])
      for (const line of stagedStats.split("\n").filter(Boolean)) {
        const [addedStr, removedStr, filePath] = line.split("\t")
        const file = staged.find((f) => f.path === filePath)
        if (file) {
          file.added = addedStr === "-" ? 0 : parseInt(addedStr) || 0
          file.removed = removedStr === "-" ? 0 : parseInt(removedStr) || 0
        }
      }
    }

    // Get diff stats for unstaged files
    if (unstaged.length > 0) {
      const unstagedStats = await execGit(["diff", "--numstat"])
      for (const line of unstagedStats.split("\n").filter(Boolean)) {
        const [addedStr, removedStr, filePath] = line.split("\t")
        const file = unstaged.find((f) => f.path === filePath)
        if (file) {
          file.added = addedStr === "-" ? 0 : parseInt(addedStr) || 0
          file.removed = removedStr === "-" ? 0 : parseInt(removedStr) || 0
        }
      }
    }

    // Backward compat: dirty count and files list
    const dirtyLines = statusResult.split("\n").filter((line) => line.trim() && line.match(/^ ?M/))
    const dirty = dirtyLines.length
    const files = dirtyLines
      .map(
        (line) =>
          line
            .replace(/^ ?M\s+/, "")
            .split("/")
            .pop() || "",
      )
      .filter(Boolean)
      .slice(0, 3)

    const isClean = staged.length === 0 && unstaged.length === 0 && untracked.length === 0 && conflicted.length === 0

    // Fetch commit history
    const commits = await fetchCommitHistory()

    return {
      branch,
      dirty,
      files,
      ahead,
      behind,
      enabled: true,
      staged,
      unstaged,
      untracked,
      conflicted,
      isClean,
      isRepo: true,
      commits,
    }
  } catch {
    return disabled
  }
}

export function GitProvider(props: ParentProps) {
  const [status, setStatus] = createSignal<GitStatus>({
    branch: null,
    dirty: 0,
    files: [],
    ahead: 0,
    behind: 0,
    enabled: false,
    staged: [],
    unstaged: [],
    untracked: [],
    conflicted: [],
    isClean: true,
    isRepo: false,
    commits: [],
  })

  const refresh = async () => {
    try {
      const result = await fetchGitStatus()
      setStatus(result)
    } catch {
      // Silently fail
    }
  }

  onMount(() => {
    // Initial fetch
    refresh()

    // Poll every 3 seconds
    const interval = setInterval(refresh, 3000)

    onCleanup(() => {
      clearInterval(interval)
    })
  })

  return <context.Provider value={{ status, refresh }}>{props.children}</context.Provider>
}
