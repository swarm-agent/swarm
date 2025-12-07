import { useSync } from "@tui/context/sync"
import { createMemo, For, Show, Switch, Match, createSignal, type Accessor } from "solid-js"
import { useTheme } from "../../context/theme"
import { useGit, type GitStatusFile } from "../../context/git"
import path from "path"
import type { RGBA } from "@opentui/core"

// Nerd Font Icons - Git
const ICON_SOURCE_CONTROL = "\u{ea68}" // cod-source_control
const ICON_BRANCH = "\u{e725}" // dev-git_branch
const ICON_STAGED = "\u{eadc}" // cod-diff_added
const ICON_UNSTAGED = "\u{eade}" // cod-diff_modified
const ICON_UNTRACKED = "\u{ea7f}" // cod-new_file
const ICON_CONFLICT = "\u{ea6c}" // cod-warning
const ICON_COMMIT = "\u{eafc}" // cod-git_commit
const ICON_HISTORY = "\u{ea82}" // cod-history

// Nerd Font Icons - File Types
const ICON_FILE = "\u{ea7b}" // cod-file
const ICON_TS = "\u{e8ca}" // dev-typescript
const ICON_TSX = "\u{e7ba}" // dev-react
const ICON_JS = "\u{e781}" // dev-javascript
const ICON_JSON = "\u{eb0f}" // cod-json
const ICON_MD = "\u{e73e}" // dev-markdown
const ICON_CSS = "\u{e749}" // dev-css3
const ICON_HTML = "\u{e736}" // dev-html5
const ICON_PY = "\u{e73c}" // dev-python
const ICON_GO = "\u{e724}" // dev-go
const ICON_RS = "\u{e7a8}" // dev-rust
const ICON_YAML = "\u{e8eb}" // dev-yaml
const ICON_SH = "\u{e760}" // dev-bash
const ICON_SQL = "\u{e706}" // dev-database
const ICON_GIT = "\u{e702}" // dev-git

function truncatePath(filePath: string, maxLen: number): string {
  if (filePath.length <= maxLen) return filePath
  const parts = filePath.split(path.sep)
  const filename = parts.at(-1)!
  if (filename.length >= maxLen - 3) {
    return "…" + filename.slice(-(maxLen - 3))
  }
  const remaining = maxLen - filename.length - 2
  const pathPart = parts.slice(0, -1).join(path.sep)
  if (pathPart.length <= remaining) return filePath
  return "…" + pathPart.slice(-remaining) + "/" + filename
}

function truncateMessage(msg: string, maxLen: number): string {
  if (msg.length <= maxLen) return msg
  return msg.slice(0, maxLen - 1) + "…"
}

function getFileIcon(filePath: string): { icon: string; color?: string } {
  const ext = path.extname(filePath).toLowerCase()
  const basename = path.basename(filePath).toLowerCase()

  // Special files
  if (basename === ".gitignore" || basename === ".gitattributes") return { icon: ICON_GIT, color: "#f14e32" }
  if (basename.endsWith(".config.ts") || basename.endsWith(".config.js")) return { icon: ICON_TS, color: "#3178c6" }

  switch (ext) {
    case ".ts":
      return { icon: ICON_TS, color: "#3178c6" }
    case ".tsx":
    case ".jsx":
      return { icon: ICON_TSX, color: "#61dafb" }
    case ".js":
    case ".mjs":
    case ".cjs":
      return { icon: ICON_JS, color: "#f7df1e" }
    case ".json":
      return { icon: ICON_JSON, color: "#cbcb41" }
    case ".md":
    case ".mdx":
      return { icon: ICON_MD, color: "#519aba" }
    case ".css":
    case ".scss":
    case ".sass":
    case ".less":
      return { icon: ICON_CSS, color: "#563d7c" }
    case ".html":
    case ".htm":
      return { icon: ICON_HTML, color: "#e34c26" }
    case ".py":
      return { icon: ICON_PY, color: "#3572a5" }
    case ".go":
      return { icon: ICON_GO, color: "#00add8" }
    case ".rs":
      return { icon: ICON_RS, color: "#dea584" }
    case ".yaml":
    case ".yml":
      return { icon: ICON_YAML, color: "#cb171e" }
    case ".sh":
    case ".bash":
    case ".zsh":
      return { icon: ICON_SH, color: "#89e051" }
    case ".sql":
      return { icon: ICON_SQL, color: "#e38c00" }
    default:
      return { icon: ICON_FILE }
  }
}

function getStatusBadge(status: GitStatusFile["status"], staged: boolean): { letter: string; color: string } {
  switch (status) {
    case "added":
      return { letter: "A", color: staged ? "#73c991" : "#73c991" }
    case "modified":
      return { letter: "M", color: staged ? "#73c991" : "#e2c08d" }
    case "deleted":
      return { letter: "D", color: "#f14c4c" }
    case "renamed":
      return { letter: "R", color: "#73c991" }
    case "copied":
      return { letter: "C", color: "#73c991" }
    default:
      return { letter: "M", color: "#e2c08d" }
  }
}

function FileEntry(props: { file: GitStatusFile; staged: boolean; maxWidth?: number }) {
  const { theme } = useTheme()
  const filename = () => path.basename(props.file.path)
  const directory = () => {
    const dir = path.dirname(props.file.path)
    return dir === "." ? "" : dir
  }
  const fileIcon = () => getFileIcon(props.file.path)
  const badge = () => getStatusBadge(props.file.status, props.staged)
  const hasChanges = () => props.file.added > 0 || props.file.removed > 0

  return (
    <box>
      {/* Line 1: icon, filename, badge, +/- stats */}
      <box flexDirection="row" gap={1} justifyContent="space-between">
        <box flexDirection="row" gap={1} flexShrink={1}>
          <text style={{ fg: fileIcon().color ?? theme.textMuted }}>{fileIcon().icon}</text>
          <text fg={theme.text}>
            <b>{filename()}</b>
          </text>
        </box>
        <box flexDirection="row" gap={1} flexShrink={0}>
          <text style={{ fg: badge().color }}>{badge().letter}</text>
          <Show when={hasChanges()}>
            <text fg={theme.diffAdded}>+{props.file.added}</text>
            <text fg={theme.diffRemoved}>-{props.file.removed}</text>
          </Show>
        </box>
      </box>
      {/* Line 2: directory path (dimmed) */}
      <Show when={directory()}>
        <text fg={theme.textMuted}> {truncatePath(directory(), 28)}</text>
      </Show>
    </box>
  )
}

function GitFileGroup(props: {
  title: string
  icon: string
  files: GitStatusFile[]
  color: RGBA
  expanded: Accessor<boolean>
  setExpanded: (v: boolean) => void
  isStaged: boolean
}) {
  const { theme } = useTheme()
  const totalAdded = () => props.files.reduce((sum, f) => sum + f.added, 0)
  const totalRemoved = () => props.files.reduce((sum, f) => sum + f.removed, 0)
  const hasStats = () => totalAdded() > 0 || totalRemoved() > 0

  return (
    <box paddingLeft={1}>
      {/* Section Header with summary */}
      <box
        flexDirection="row"
        gap={1}
        justifyContent="space-between"
        onMouseDown={() => props.setExpanded(!props.expanded())}
      >
        <box flexDirection="row" gap={1}>
          <text fg={theme.textMuted}>{props.expanded() ? "▼" : "▶"}</text>
          <text fg={props.color}>{props.icon}</text>
          <text fg={theme.text}>
            {props.title} ({props.files.length})
          </text>
        </box>
        <Show when={hasStats()}>
          <box flexDirection="row" gap={1}>
            <text fg={theme.diffAdded}>+{totalAdded()}</text>
            <text fg={theme.diffRemoved}>-{totalRemoved()}</text>
          </box>
        </Show>
      </box>
      {/* File List */}
      <Show when={props.expanded()}>
        <box paddingLeft={2} gap={1}>
          <For each={props.files.slice(0, 12)}>{(file) => <FileEntry file={file} staged={props.isStaged} />}</For>
          <Show when={props.files.length > 12}>
            <text fg={theme.textMuted}>… +{props.files.length - 12} more files</text>
          </Show>
        </box>
      </Show>
    </box>
  )
}

export function Sidebar(props: { sessionID: string }) {
  const sync = useSync()
  const { theme } = useTheme()
  const git = useGit()
  const session = createMemo(() => sync.session.get(props.sessionID)!)
  const todos = createMemo(() => sync.data.todo[props.sessionID] ?? [])

  // Todo filtering - active (pending/in_progress) vs completed
  const activeTodos = createMemo(() => todos().filter((t) => t.status === "pending" || t.status === "in_progress"))
  const completedCount = createMemo(() => todos().filter((t) => t.status === "completed").length)

  const [mcpExpanded, setMcpExpanded] = createSignal(true)
  const [todoExpanded, setTodoExpanded] = createSignal(true)
  const [lspExpanded, setLspExpanded] = createSignal(true)

  // Git section states
  const [gitExpanded, setGitExpanded] = createSignal(true)
  const [stagedExpanded, setStagedExpanded] = createSignal(true)
  const [unstagedExpanded, setUnstagedExpanded] = createSignal(true)
  const [untrackedExpanded, setUntrackedExpanded] = createSignal(false)
  const [historyExpanded, setHistoryExpanded] = createSignal(true)

  // Total changes count for Source Control badge
  const totalChanges = createMemo(() => git.status().staged.length + git.status().unstaged.length)

  return (
    <Show when={session()}>
      <scrollbox width={40}>
        <box flexShrink={0} gap={1} paddingRight={1}>
          <box>
            <text fg={theme.text}>
              <b>{session().title}</b>
            </text>
            <Show when={session().share?.url}>
              <text fg={theme.textMuted}>{session().share!.url}</text>
            </Show>
          </box>

          {/* Todo Section - Shows first when available */}
          <Show when={todos().length > 0}>
            <box>
              <box flexDirection="row" gap={1} onMouseDown={() => setTodoExpanded(!todoExpanded())}>
                <text fg={theme.text}>{todoExpanded() ? "▼" : "▶"}</text>
                <text fg={theme.text}>
                  <b>Todo</b>
                </text>
              </box>
              <Show when={todoExpanded()}>
                <Show when={activeTodos().length > 0} fallback={<text fg={theme.success}>✓ All todos done</text>}>
                  <For each={activeTodos()}>
                    {(item) => (
                      <text style={{ fg: item.status === "in_progress" ? theme.success : theme.textMuted }}>
                        [{item.status === "in_progress" ? "•" : " "}] {item.content}
                      </text>
                    )}
                  </For>
                  <Show when={completedCount() > 0}>
                    <text fg={theme.textMuted}>
                      {activeTodos().length} left, {completedCount()} done
                    </text>
                  </Show>
                </Show>
              </Show>
            </box>
          </Show>

          {/* Git Status Section - VS Code Style */}
          <Show when={git.status().isRepo}>
            <box>
              {/* Source Control Header */}
              <box flexDirection="row" gap={1} onMouseDown={() => setGitExpanded(!gitExpanded())}>
                <text fg={theme.textMuted}>{gitExpanded() ? "▼" : "▶"}</text>
                <text style={{ fg: "#60a5fa" }}>{ICON_SOURCE_CONTROL}</text>
                <text style={{ fg: "#60a5fa" }}>
                  <b>Source Control</b>
                </text>
                <Show when={totalChanges() > 0}>
                  <text style={{ fg: "#fbbf24" }}>({totalChanges()})</text>
                </Show>
              </box>

              <Show when={gitExpanded()}>
                {/* Branch Info */}
                <box paddingLeft={1} flexDirection="row" gap={1}>
                  <text style={{ fg: "#a78bfa" }}>{ICON_BRANCH}</text>
                  <text style={{ fg: "#c4b5fd" }}>{git.status().branch || "HEAD"}</text>
                  <Show when={git.status().ahead > 0}>
                    <text style={{ fg: "#4ade80" }}> 󰄿{git.status().ahead}</text>
                  </Show>
                  <Show when={git.status().behind > 0}>
                    <text style={{ fg: "#fb923c" }}> 󰄼{git.status().behind}</text>
                  </Show>
                  <Show when={git.status().isClean}>
                    <text style={{ fg: "#4ade80" }}>✓</text>
                  </Show>
                </box>

                {/* Staged Files */}
                <Show when={git.status().staged.length > 0}>
                  <GitFileGroup
                    title="Staged"
                    icon={ICON_STAGED}
                    files={git.status().staged}
                    color={theme.success}
                    expanded={stagedExpanded}
                    setExpanded={setStagedExpanded}
                    isStaged={true}
                  />
                </Show>

                {/* Unstaged Files */}
                <Show when={git.status().unstaged.length > 0}>
                  <GitFileGroup
                    title="Changes"
                    icon={ICON_UNSTAGED}
                    files={git.status().unstaged}
                    color={theme.warning}
                    expanded={unstagedExpanded}
                    setExpanded={setUnstagedExpanded}
                    isStaged={false}
                  />
                </Show>

                {/* Untracked Files */}
                <Show when={git.status().untracked.length > 0}>
                  <box paddingLeft={1}>
                    <box flexDirection="row" gap={1} onMouseDown={() => setUntrackedExpanded(!untrackedExpanded())}>
                      <text fg={theme.textMuted}>{untrackedExpanded() ? "▼" : "▶"}</text>
                      <text fg={theme.textMuted}>{ICON_UNTRACKED}</text>
                      <text fg={theme.textMuted}>Untracked ({git.status().untracked.length})</text>
                    </box>
                    <Show when={untrackedExpanded()}>
                      <box paddingLeft={2} gap={1}>
                        <For each={git.status().untracked.slice(0, 8)}>
                          {(file) => {
                            const fileIcon = getFileIcon(file)
                            const filename = path.basename(file)
                            const dir = path.dirname(file)
                            return (
                              <box>
                                <box flexDirection="row" gap={1}>
                                  <text style={{ fg: fileIcon.color ?? theme.textMuted }}>{fileIcon.icon}</text>
                                  <text fg={theme.textMuted}>{filename}</text>
                                  <text fg={theme.textMuted}>U</text>
                                </box>
                                <Show when={dir !== "."}>
                                  <text fg={theme.textMuted}> {truncatePath(dir, 28)}</text>
                                </Show>
                              </box>
                            )
                          }}
                        </For>
                        <Show when={git.status().untracked.length > 8}>
                          <text fg={theme.textMuted}>… +{git.status().untracked.length - 8} more files</text>
                        </Show>
                      </box>
                    </Show>
                  </box>
                </Show>

                {/* Conflicts */}
                <Show when={git.status().conflicted.length > 0}>
                  <box paddingLeft={1}>
                    <box flexDirection="row" gap={1}>
                      <text fg={theme.error}>{ICON_CONFLICT}</text>
                      <text fg={theme.error}>
                        <b>Merge Conflicts ({git.status().conflicted.length})</b>
                      </text>
                    </box>
                    <box paddingLeft={2} gap={1}>
                      <For each={git.status().conflicted}>
                        {(file) => {
                          const fileIcon = getFileIcon(file)
                          const filename = path.basename(file)
                          const dir = path.dirname(file)
                          return (
                            <box>
                              <box flexDirection="row" gap={1}>
                                <text style={{ fg: fileIcon.color ?? theme.error }}>{fileIcon.icon}</text>
                                <text fg={theme.error}>
                                  <b>{filename}</b>
                                </text>
                                <text fg={theme.error}>!</text>
                              </box>
                              <Show when={dir !== "."}>
                                <text fg={theme.textMuted}> {truncatePath(dir, 28)}</text>
                              </Show>
                            </box>
                          )
                        }}
                      </For>
                    </box>
                  </box>
                </Show>

                {/* Clean State */}
                <Show when={git.status().isClean}>
                  <box paddingLeft={1} flexDirection="row" gap={1}>
                    <text fg={theme.success}>✓</text>
                    <text fg={theme.textMuted}>No changes</text>
                  </box>
                </Show>

                {/* Commit History */}
                <Show when={git.status().commits.length > 0}>
                  <box paddingLeft={1}>
                    {/* History Header */}
                    <box flexDirection="row" gap={1} onMouseDown={() => setHistoryExpanded(!historyExpanded())}>
                      <text fg={theme.textMuted}>{historyExpanded() ? "▼" : "▶"}</text>
                      <text style={{ fg: "#f97316" }}>{ICON_HISTORY}</text>
                      <text style={{ fg: "#f97316" }}>
                        <b>History</b>
                      </text>
                    </box>

                    {/* Commits List */}
                    <Show when={historyExpanded()}>
                      <box paddingLeft={2} gap={1}>
                        <For each={git.status().commits.slice(0, 5)}>
                          {(commit) => (
                            <box flexDirection="row" gap={1}>
                              <text style={{ fg: commit.isHead ? "#22c55e" : "#6b7280" }}>
                                {commit.isHead ? ICON_COMMIT : "○"}
                              </text>
                              <text style={{ fg: "#fbbf24" }}>{commit.hash.slice(0, 7)}</text>
                              <text style={{ fg: "#93c5fd" }}>{commit.message}</text>
                            </box>
                          )}
                        </For>
                        <Show when={git.status().commits.length > 5}>
                          <text fg={theme.textMuted}>… +{git.status().commits.length - 5} more</text>
                        </Show>
                      </box>
                    </Show>
                  </box>
                </Show>
              </Show>
            </box>
          </Show>

          <Show when={Object.keys(sync.data.mcp).length > 0}>
            <box>
              <box flexDirection="row" gap={1} onMouseDown={() => setMcpExpanded(!mcpExpanded())}>
                <text fg={theme.text}>{mcpExpanded() ? "▼" : "▶"}</text>
                <text fg={theme.text}>
                  <b>MCP</b>
                </text>
              </box>
              <Show when={mcpExpanded()}>
                <For each={Object.entries(sync.data.mcp)}>
                  {([key, item]) => (
                    <box flexDirection="row" gap={1}>
                      <text
                        flexShrink={0}
                        style={{
                          fg: {
                            connected: theme.success,
                            failed: theme.error,
                            disabled: theme.textMuted,
                          }[item.status],
                        }}
                      >
                        •
                      </text>
                      <text fg={theme.text} wrapMode="word">
                        {key}{" "}
                        <span style={{ fg: theme.textMuted }}>
                          <Switch>
                            <Match when={item.status === "connected"}>Connected</Match>
                            <Match when={item.status === "failed" && item}>{(val) => <i>{val().error}</i>}</Match>
                            <Match when={item.status === "disabled"}>Disabled in configuration</Match>
                          </Switch>
                        </span>
                      </text>
                    </box>
                  )}
                </For>
              </Show>
            </box>
          </Show>
          <Show when={sync.data.lsp.length > 0}>
            <box>
              <box flexDirection="row" gap={1} onMouseDown={() => setLspExpanded(!lspExpanded())}>
                <text fg={theme.text}>{lspExpanded() ? "▼" : "▶"}</text>
                <text fg={theme.text}>
                  <b>LSP</b>
                </text>
              </box>
              <Show when={lspExpanded()}>
                <For each={sync.data.lsp}>
                  {(item) => (
                    <box flexDirection="row" gap={1}>
                      <text
                        flexShrink={0}
                        style={{
                          fg: {
                            connected: theme.success,
                            error: theme.error,
                          }[item.status],
                        }}
                      >
                        •
                      </text>
                      <text fg={theme.textMuted}>
                        {item.id} {item.root}
                      </text>
                    </box>
                  )}
                </For>
              </Show>
            </box>
          </Show>
        </box>
      </scrollbox>
    </Show>
  )
}
