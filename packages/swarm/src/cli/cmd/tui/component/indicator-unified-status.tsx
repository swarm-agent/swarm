import { For, Show, createMemo, createSignal, onMount, onCleanup } from "solid-js"
import os from "os"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { useLocal } from "@tui/context/local"
import { useGit } from "@tui/context/git"
import { useRoute } from "@tui/context/route"
import { Hyprland, type SessionEntry } from "@/hyprland"
import { RoundedBorder } from "@tui/component/border"
import { RGBA } from "@opentui/core"
import { useTerminalDimensions } from "@opentui/solid"
import type { AssistantMessage } from "@swarm-ai/sdk"

// Truncate path intelligently - keeps last N directories
function truncatePath(path: string, maxLen: number): string {
  if (path.length <= maxLen) return path
  const parts = path.split("/")
  if (parts.length <= 2) return path.slice(-maxLen)
  const prefix = parts[0] || ""
  let result = prefix
  for (let i = parts.length - 1; i > 0; i--) {
    const candidate = prefix + "/…/" + parts.slice(i).join("/")
    if (candidate.length <= maxLen) {
      result = candidate
    } else {
      break
    }
  }
  return result.length > maxLen ? result.slice(-maxLen) : result
}

// Status icons
const STATUS_ICONS = {
  working: "›",
  blocked: "!",
  idle: "-",
}

// Check if running in SSH session
const IS_SSH = !!(process.env.SSH_CLIENT || process.env.SSH_TTY || process.env.SSH_CONNECTION)

// ============================================================================
// SMART LAYOUT SYSTEM - measures content, only reduces when needed
// ============================================================================

type LayoutConfig = {
  showPath: boolean
  pathMaxLen: number
  showModel: boolean
  showContext: boolean
  showWorkspace: boolean
  otherSessionCount: number
  otherShowPath: boolean
  otherPathMaxLen: number
  otherShowContext: boolean
  showGit: boolean
  gitShowCounts: boolean
  gitShowAheadBehind: boolean
  gitBranchMaxLen: number
  showBgAgents: boolean
  showBgAgentCount: boolean
}

// Estimate width of current session block
function estimateCurrentWidth(
  cfg: LayoutConfig,
  data: { path: string; agent: string; model?: string; contextPct?: number; workspace?: number }
): number {
  let w = 6 // borders + padding + gaps
  if (cfg.showWorkspace && data.workspace !== undefined) w += 3
  if (cfg.showPath) w += Math.min(data.path.length, cfg.pathMaxLen) + 1
  w += (data.agent?.length || 5) + 1
  if (cfg.showModel && data.model) w += data.model.length + 1
  if (cfg.showContext && data.contextPct !== undefined) w += 5
  return w
}

// Estimate width of other session block  
function estimateOtherWidth(cfg: LayoutConfig, session: SessionEntry): number {
  let w = 8 // borders + padding + status + workspace
  if (session.remote) w += 2
  if (cfg.otherShowPath && session.cwd) {
    w += Math.min(session.cwd.replace(os.homedir(), "~").length, cfg.otherPathMaxLen) + 1
  }
  if (session.agent) w += session.agent.length + 1
  if (cfg.otherShowContext) w += 5
  return w
}

// Estimate git block width
function estimateGitWidth(
  cfg: LayoutConfig,
  data: { branch: string; dirty: number; untracked: number; ahead: number; behind: number }
): number {
  if (!cfg.showGit || !data.branch) return 0
  let w = 6
  w += Math.min(data.branch.length, cfg.gitBranchMaxLen) + 1
  if (cfg.gitShowCounts) {
    if (data.dirty > 0) w += 4
    if (data.untracked > 0) w += 4
  }
  if (cfg.gitShowAheadBehind) {
    if (data.ahead > 0) w += 4
    if (data.behind > 0) w += 4
  }
  return w
}

// Estimate bg agents block width
function estimateBgWidth(cfg: LayoutConfig, count: number): number {
  if (!cfg.showBgAgents || count === 0) return 0
  return cfg.showBgAgentCount ? 8 : 6
}

// Calculate total width
function estimateTotal(
  cfg: LayoutConfig,
  data: {
    currentPath: string
    agent: string
    model?: string
    contextPct?: number
    workspace?: number
    otherSessions: SessionEntry[]
    gitBranch: string
    gitDirty: number
    gitUntracked: number
    gitAhead: number
    gitBehind: number
    bgAgentCount: number
  }
): number {
  let total = 4
  total += estimateBgWidth(cfg, data.bgAgentCount)
  total += estimateCurrentWidth(cfg, {
    path: data.currentPath,
    agent: data.agent,
    model: data.model,
    contextPct: data.contextPct,
    workspace: data.workspace,
  })
  for (let i = 0; i < cfg.otherSessionCount && i < data.otherSessions.length; i++) {
    total += estimateOtherWidth(cfg, data.otherSessions[i]) + 1
  }
  if (data.otherSessions.length > cfg.otherSessionCount) total += 4
  total += estimateGitWidth(cfg, {
    branch: data.gitBranch,
    dirty: data.gitDirty,
    untracked: data.gitUntracked,
    ahead: data.gitAhead,
    behind: data.gitBehind,
  })
  return total
}

// Calculate optimal layout - starts with max, reduces only when needed
function calculateLayout(
  availableWidth: number,
  data: {
    currentPath: string
    agent: string
    model?: string
    contextPct?: number
    workspace?: number
    otherSessions: SessionEntry[]
    gitBranch: string
    gitDirty: number
    gitUntracked: number
    gitAhead: number
    gitBehind: number
    bgAgentCount: number
  }
): LayoutConfig {
  // Start with everything visible
  const cfg: LayoutConfig = {
    showPath: true,
    pathMaxLen: 50,
    showModel: true,
    showContext: data.contextPct !== undefined,
    showWorkspace: data.workspace !== undefined,
    otherSessionCount: data.otherSessions.length,
    otherShowPath: true,
    otherPathMaxLen: 25,
    otherShowContext: true,
    showGit: !!data.gitBranch,
    gitShowCounts: true,
    gitShowAheadBehind: true,
    gitBranchMaxLen: 30,
    showBgAgents: data.bgAgentCount > 0,
    showBgAgentCount: true,
  }

  // If it fits, done
  if (estimateTotal(cfg, data) <= availableWidth) return cfg

  // Progressive reductions - least important first
  const reductions: Array<() => boolean> = [
    // 1-2. Remove extras from other sessions
    () => { if (cfg.otherShowPath) { cfg.otherShowPath = false; return true } return false },
    () => { if (cfg.otherShowContext) { cfg.otherShowContext = false; return true } return false },
    // 3-5. Reduce git details
    () => { if (cfg.gitBranchMaxLen > 15) { cfg.gitBranchMaxLen = 15; return true } return false },
    () => { if (cfg.gitShowAheadBehind) { cfg.gitShowAheadBehind = false; return true } return false },
    () => { if (cfg.gitShowCounts) { cfg.gitShowCounts = false; return true } return false },
    // 6. Start shortening current path
    () => { if (cfg.pathMaxLen > 35) { cfg.pathMaxLen = 35; return true } return false },
    // 7-11. Remove other sessions one by one
    () => { if (cfg.otherSessionCount > 4) { cfg.otherSessionCount = 4; return true } return false },
    () => { if (cfg.otherSessionCount > 3) { cfg.otherSessionCount = 3; return true } return false },
    () => { if (cfg.otherSessionCount > 2) { cfg.otherSessionCount = 2; return true } return false },
    () => { if (cfg.otherSessionCount > 1) { cfg.otherSessionCount = 1; return true } return false },
    () => { if (cfg.otherSessionCount > 0) { cfg.otherSessionCount = 0; return true } return false },
    // 12-14. Continue shortening path
    () => { if (cfg.pathMaxLen > 25) { cfg.pathMaxLen = 25; return true } return false },
    () => { if (cfg.gitBranchMaxLen > 10) { cfg.gitBranchMaxLen = 10; return true } return false },
    () => { if (cfg.pathMaxLen > 18) { cfg.pathMaxLen = 18; return true } return false },
    // 15-16. Remove current session details
    () => { if (cfg.showContext) { cfg.showContext = false; return true } return false },
    () => { if (cfg.showModel) { cfg.showModel = false; return true } return false },
    // 17-19. Further reductions
    () => { if (cfg.pathMaxLen > 12) { cfg.pathMaxLen = 12; return true } return false },
    () => { if (cfg.showWorkspace) { cfg.showWorkspace = false; return true } return false },
    () => { if (cfg.showGit) { cfg.showGit = false; return true } return false },
    // 20-22. Minimal mode
    () => { if (cfg.pathMaxLen > 8) { cfg.pathMaxLen = 8; return true } return false },
    () => { if (cfg.showBgAgentCount) { cfg.showBgAgentCount = false; return true } return false },
    () => { if (cfg.showPath) { cfg.showPath = false; return true } return false },
  ]

  for (const reduce of reductions) {
    if (estimateTotal(cfg, data) <= availableWidth) break
    reduce()
  }

  return cfg
}

// ============================================================================
// COMPONENTS
// ============================================================================

function CurrentSessionBlock(props: { hyprWorkspace?: number; layout: LayoutConfig }) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()
  const route = useRoute()

  const agent = () => local.agent.current().name
  const model = () => local.model.parsed().model
  const sessionId = () => route.data.type === "session" ? route.data.sessionID : undefined

  const agentColor = () => {
    const a = agent()
    return a ? local.agent.color(a) : theme.primary
  }

  const contextPct = createMemo(() => {
    const sid = sessionId()
    if (!sid) return undefined
    const messages = sync.data.message[sid] ?? []
    const last = messages.findLast((x) => x.role === "assistant" && x.tokens.output > 0) as AssistantMessage
    if (!last) return undefined
    const total = last.tokens.input + last.tokens.output + last.tokens.reasoning + last.tokens.cache.read + last.tokens.cache.write
    const m = sync.data.provider.find((x) => x.id === last.providerID)?.models[last.modelID]
    if (!m?.limit.context) return undefined
    return Math.round((total / m.limit.context) * 100)
  })

  const ctxColor = () => {
    const pct = contextPct()
    if (pct === undefined) return theme.textMuted
    const t = Math.min(pct, 100) / 100
    return RGBA.fromInts(
      Math.round(74 + t * 181),
      Math.round(158 - t * 84),
      Math.round(255 - t * 148)
    )
  }

  const modelShort = () => {
    const m = model()
    if (!m) return undefined
    let name = m.replace(/\s*\([^)]*\)\s*/g, "").trim().replace(/^Claude\s+/i, "")
    const lower = name.toLowerCase()
    // Extract version number for Claude models (e.g., "4.5", "4-5", "4.0", "4-0")
    const versionMatch = lower.match(/(\d+)[.-](\d+)/)
    const version = versionMatch ? ` ${versionMatch[1]}.${versionMatch[2]}` : ""
    if (lower.includes("opus")) return `opus${version}`
    if (lower.includes("sonnet")) return `sonnet${version}`
    if (lower.includes("haiku")) return `haiku${version}`
    return name.toLowerCase().slice(0, 10)
  }

  const cwd = () => truncatePath(process.cwd().replace(os.homedir(), "~"), props.layout.pathMaxLen)

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
      borderColor={agentColor()}
      paddingLeft={1}
      paddingRight={1}
      flexShrink={0}
    >
      <box flexDirection="row" gap={1}>
        <Show when={props.layout.showWorkspace && props.hyprWorkspace !== undefined}>
          <text fg={theme.text}>W{props.hyprWorkspace}</text>
        </Show>
        <Show when={props.layout.showPath}>
          <text fg={theme.textMuted}>{cwd()}</text>
        </Show>
        <Show when={agent()}>
          <text fg={agentColor()}>{agent()}</text>
        </Show>
        <Show when={props.layout.showModel && modelShort()}>
          <text fg={theme.textMuted}>{modelShort()}</text>
        </Show>
        <Show when={props.layout.showContext && contextPct() !== undefined}>
          <text fg={ctxColor()}>{contextPct()}%</text>
        </Show>
      </box>
    </box>
  )
}

function OtherSessionBlock(props: { session: SessionEntry; layout: LayoutConfig }) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()

  const agentColor = () => {
    const agent = props.session.agent
    return agent ? local.agent.color(agent) : theme.primary
  }

  const statusSymbol = () => {
    switch (props.session.status) {
      case "blocked": return STATUS_ICONS.blocked
      case "working": return STATUS_ICONS.working
      default: return STATUS_ICONS.idle
    }
  }

  const statusColor = () => {
    switch (props.session.status) {
      case "blocked": return theme.error
      case "working": return agentColor()
      default: return theme.textMuted
    }
  }

  const borderColor = () => props.session.status === "blocked" ? theme.error : theme.border

  const projectPath = () => {
    const cwd = props.session.cwd
    if (!cwd) return undefined
    return truncatePath(cwd.replace(os.homedir(), "~"), props.layout.otherPathMaxLen)
  }

  const contextPct = createMemo(() => {
    const sid = props.session.sessionId
    if (!sid) return undefined
    const messages = sync.data.message[sid] ?? []
    const last = messages.findLast((x) => x.role === "assistant" && x.tokens.output > 0) as AssistantMessage
    if (!last) return undefined
    const total = last.tokens.input + last.tokens.output + last.tokens.reasoning + last.tokens.cache.read + last.tokens.cache.write
    const model = sync.data.provider.find((x) => x.id === last.providerID)?.models[last.modelID]
    if (!model?.limit.context) return undefined
    return Math.round((total / model.limit.context) * 100)
  })

  const ctxColor = () => {
    const pct = contextPct()
    if (pct === undefined) return theme.textMuted
    const t = Math.min(pct, 100) / 100
    return RGBA.fromInts(
      Math.round(74 + t * 181),
      Math.round(158 - t * 84),
      Math.round(255 - t * 148)
    )
  }

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
      borderColor={borderColor()}
      paddingLeft={1}
      paddingRight={1}
      flexShrink={0}
    >
      <box flexDirection="row" gap={1}>
        <text fg={statusColor()}>{statusSymbol()}</text>
        <Show when={props.session.remote}>
          <text fg={theme.warning}>R</text>
        </Show>
        <text fg={theme.text}>W{props.session.hyprWorkspace ?? "?"}</text>
        <Show when={props.layout.otherShowPath && projectPath()}>
          <text fg={theme.textMuted}>{projectPath()}</text>
        </Show>
        <Show when={props.session.agent}>
          <text fg={agentColor()}>{props.session.agent}</text>
        </Show>
        <Show when={props.layout.otherShowContext && contextPct() !== undefined}>
          <text fg={ctxColor()}>{contextPct()}%</text>
        </Show>
      </box>
    </box>
  )
}

function GitBlock(props: { layout: LayoutConfig }) {
  const git = useGit()
  const { theme } = useTheme()
  const status = () => git.status()

  const branchName = () => {
    const branch = status().branch
    if (!branch) return ""
    if (branch.length <= props.layout.gitBranchMaxLen) return branch
    return branch.slice(0, props.layout.gitBranchMaxLen - 1) + "…"
  }

  return (
    <Show when={props.layout.showGit && status().enabled}>
      <box
        border={["left", "right", "top", "bottom"]}
        customBorderChars={RoundedBorder.customBorderChars}
        borderColor={theme.border}
        paddingLeft={1}
        paddingRight={1}
        flexShrink={0}
      >
        <box flexDirection="row" gap={1}>
          <text fg={theme.primary}>{branchName()}</text>
          <Show when={props.layout.gitShowCounts && status().dirty > 0}>
            <text fg={theme.warning}>~{status().dirty}</text>
          </Show>
          <Show when={props.layout.gitShowCounts && status().untracked.length > 0}>
            <text fg={theme.warning}>?{status().untracked.length}</text>
          </Show>
          <Show when={props.layout.gitShowAheadBehind && status().ahead > 0}>
            <text fg={theme.success}>↑{status().ahead}</text>
          </Show>
          <Show when={props.layout.gitShowAheadBehind && status().behind > 0}>
            <text fg={theme.error}>↓{status().behind}</text>
          </Show>
        </box>
      </box>
    </Show>
  )
}

// Connection indicator - shows if current session is local or SSH
function ConnectionBlock() {
  const { theme } = useTheme()

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
      borderColor={theme.border}
      paddingLeft={1}
      paddingRight={1}
      flexShrink={0}
    >
      <text fg={theme.textMuted}>{IS_SSH ? "ssh" : "local"}</text>
    </box>
  )
}

function BackgroundAgentsBlock(props: { layout: LayoutConfig }) {
  const sync = useSync()
  const { theme } = useTheme()
  // Show all running background agents globally
  const running = createMemo(() => sync.data.backgroundAgent.filter((a) => a.status === "running"))

  return (
    <Show when={props.layout.showBgAgents && running().length > 0}>
      <box
        border={["left", "right", "top", "bottom"]}
        customBorderChars={RoundedBorder.customBorderChars}
        borderColor={theme.textMuted}
        paddingLeft={1}
        paddingRight={1}
        flexShrink={0}
      >
        <box flexDirection="row" gap={1}>
          <text fg={theme.textMuted}>⚡</text>
          <Show when={props.layout.showBgAgentCount}>
            <text fg={theme.textMuted}>{running().length}</text>
          </Show>
        </box>
      </box>
    </Show>
  )
}

function HiddenSessionsIndicator(props: { count: number }) {
  const { theme } = useTheme()
  return (
    <box paddingLeft={1}>
      <text fg={theme.textMuted}>+{props.count}</text>
    </box>
  )
}

// ============================================================================
// MAIN STATUS BAR
// ============================================================================

export function UnifiedStatusBar() {
  const [otherSessions, setOtherSessions] = createSignal<SessionEntry[]>([])
  const [currentWorkspace, setCurrentWorkspace] = createSignal<number | undefined>()
  const currentPid = process.pid
  const dimensions = useTerminalDimensions()
  const sync = useSync()
  const local = useLocal()
  const route = useRoute()
  const git = useGit()

  onMount(() => {
    const update = async () => {
      const allSessions = await Hyprland.getSessions()
      const current = allSessions.find(s => s.pid === currentPid)
      setCurrentWorkspace(current?.hyprWorkspace ?? undefined)
      // Sort ALL sessions by workspace number, current session included in the flow
      const sorted = allSessions.sort((a, b) => {
        const wsA = a.hyprWorkspace ?? 999
        const wsB = b.hyprWorkspace ?? 999
        if (wsA !== wsB) return wsA - wsB
        // Current session first within same workspace
        if (a.pid === currentPid) return -1
        if (b.pid === currentPid) return 1
        // Within same workspace, blocked sessions first
        if (a.status === "blocked" && b.status !== "blocked") return -1
        if (b.status === "blocked" && a.status !== "blocked") return 1
        return 0
      })
      const others = sorted.filter(s => s.pid !== currentPid)
      setOtherSessions(others)
    }
    update()
    const interval = setInterval(update, 2000)
    onCleanup(() => clearInterval(interval))
  })

  const currentContextPct = createMemo(() => {
    const sessionId = route.data.type === "session" ? route.data.sessionID : undefined
    if (!sessionId) return undefined
    const messages = sync.data.message[sessionId] ?? []
    const last = messages.findLast((x) => x.role === "assistant" && x.tokens.output > 0) as AssistantMessage
    if (!last) return undefined
    const total = last.tokens.input + last.tokens.output + last.tokens.reasoning + last.tokens.cache.read + last.tokens.cache.write
    const m = sync.data.provider.find((x) => x.id === last.providerID)?.models[last.modelID]
    if (!m?.limit.context) return undefined
    return Math.round((total / m.limit.context) * 100)
  })

  const modelShort = createMemo(() => {
    const m = local.model.parsed().model
    if (!m) return undefined
    let name = m.replace(/\s*\([^)]*\)\s*/g, "").trim().replace(/^Claude\s+/i, "")
    const lower = name.toLowerCase()
    // Extract version number for Claude models (e.g., "4.5", "4-5", "4.0", "4-0")
    const versionMatch = lower.match(/(\d+)[.-](\d+)/)
    const version = versionMatch ? ` ${versionMatch[1]}.${versionMatch[2]}` : ""
    if (lower.includes("opus")) return `opus${version}`
    if (lower.includes("sonnet")) return `sonnet${version}`
    if (lower.includes("haiku")) return `haiku${version}`
    return name.toLowerCase().slice(0, 10)
  })

  // Smart layout calculation
  const layout = createMemo(() => {
    const gitStatus = git.status()
    const bgAgentCount = sync.data.backgroundAgent.filter((a) => a.status === "running").length

    return calculateLayout(dimensions().width, {
      currentPath: process.cwd().replace(os.homedir(), "~"),
      agent: local.agent.current().name,
      model: modelShort(),
      contextPct: currentContextPct(),
      workspace: currentWorkspace(),
      otherSessions: otherSessions(),
      gitBranch: gitStatus.branch || "",
      gitDirty: gitStatus.dirty,
      gitUntracked: gitStatus.untracked.length,
      gitAhead: gitStatus.ahead,
      gitBehind: gitStatus.behind,
      bgAgentCount,
    })
  })

  // Build unified list: all sessions sorted by workspace, current marked
  const allSessionsOrdered = createMemo(() => {
    const others = otherSessions()
    const ws = currentWorkspace()
    const all: Array<{ type: "current" } | { type: "other"; session: SessionEntry }> = []

    // Add current session placeholder
    const currentEntry = { type: "current" as const, ws: ws ?? 999 }

    // Combine and sort
    const combined = [
      ...others.map(s => ({ type: "other" as const, session: s, ws: s.hyprWorkspace ?? 999 })),
      currentEntry,
    ].sort((a, b) => a.ws - b.ws)

    return combined
  })

  const visibleSessions = createMemo(() => {
    const all = allSessionsOrdered()
    // Count how many "other" sessions we can show
    const maxOthers = layout().otherSessionCount
    let othersShown = 0
    const visible: typeof all = []

    for (const item of all) {
      if (item.type === "current") {
        visible.push(item)
      } else if (othersShown < maxOthers) {
        visible.push(item)
        othersShown++
      }
    }
    return visible
  })

  const hiddenSessionCount = createMemo(() => {
    const totalOthers = otherSessions().length
    const maxOthers = layout().otherSessionCount
    return Math.max(0, totalOthers - maxOthers)
  })

  return (
    <box height={3} flexDirection="row" justifyContent="space-between" gap={1} flexShrink={0}>
      <box flexDirection="row" gap={1} alignItems="center">
        <ConnectionBlock />
        <BackgroundAgentsBlock layout={layout()} />
        <For each={visibleSessions()}>
          {(item) => (
            item.type === "current"
              ? <CurrentSessionBlock hyprWorkspace={currentWorkspace()} layout={layout()} />
              : <OtherSessionBlock session={item.session} layout={layout()} />
          )}
        </For>
        <Show when={hiddenSessionCount() > 0}>
          <HiddenSessionsIndicator count={hiddenSessionCount()} />
        </Show>
      </box>
      <box flexDirection="row" gap={1} alignItems="center">
        <GitBlock layout={layout()} />
      </box>
    </box>
  )
}
