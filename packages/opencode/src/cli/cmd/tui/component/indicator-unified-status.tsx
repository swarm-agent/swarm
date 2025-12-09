import { For, Show, createMemo, createSignal, onMount, onCleanup } from "solid-js"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { useLocal } from "@tui/context/local"
import { useGit } from "@tui/context/git"
import { Hyprland, type SessionEntry } from "@/hyprland"
import { RoundedBorder } from "@tui/component/border"
import { RGBA } from "@opentui/core"
import type { AssistantMessage } from "@opencode-ai/sdk"

// Session block - floating pill with rounded border
function SessionBlock(props: {
  session: SessionEntry
  isCurrent: boolean
}) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()

  // Status indicator
  const statusSymbol = () => {
    switch (props.session.status) {
      case "blocked": return "!"
      case "working": return "●"
      default: return "○"
    }
  }

  const statusColor = () => {
    switch (props.session.status) {
      case "blocked": return theme.warning
      case "working": return theme.success
      default: return theme.textMuted
    }
  }

  // Agent color
  const agentColor = () => {
    const agent = props.session.agent
    if (!agent) return theme.primary
    return local.agent.color(agent)
  }

  // Border color: agent color if focused, status-tinted if working/blocked, muted otherwise
  const borderColor = () => {
    if (props.isCurrent) return agentColor()
    if (props.session.status === "blocked") return theme.warning
    if (props.session.status === "working") return theme.success
    return theme.border
  }

  // Context % for this session
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

  // Context color gradient: blue -> yellow -> red
  const ctxColor = () => {
    const pct = contextPct()
    if (pct === undefined) return theme.textMuted
    const t = Math.min(pct, 100) / 100
    const r = Math.round(74 + t * 181)
    const g = Math.round(158 - t * 84)
    const b = Math.round(255 - t * 148)
    return RGBA.fromInts(r, g, b)
  }

  // Short model name for focused session
  const modelShort = () => {
    const model = props.session.model
    if (!model) return undefined
    // Remove "Claude " prefix and "(latest)" suffix
    let name = model.replace(/\s*\([^)]*\)\s*/g, "").trim()
    name = name.replace(/^Claude\s+/i, "")
    // Shorten common names
    if (name.toLowerCase().includes("opus")) return "opus"
    if (name.toLowerCase().includes("sonnet")) return "sonnet"
    if (name.toLowerCase().includes("haiku")) return "haiku"
    return name.toLowerCase().slice(0, 8)
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
        {/* Status dot */}
        <text fg={statusColor()}>{statusSymbol()}</text>
        {/* Workspace */}
        <text fg={theme.text}>W{props.session.hyprWorkspace ?? "?"}</text>
        {/* Agent name */}
        <Show when={props.session.agent}>
          <text fg={agentColor()}>{props.session.agent}</text>
        </Show>
        {/* Model (focused only) */}
        <Show when={props.isCurrent && modelShort()}>
          <text fg={theme.textMuted}>{modelShort()}</text>
        </Show>
        {/* Context % */}
        <Show when={contextPct() !== undefined}>
          <text fg={ctxColor()}>{contextPct()}%</text>
        </Show>
      </box>
    </box>
  )
}

// Git status block - floating pill
function GitBlock() {
  const git = useGit()
  const { theme } = useTheme()

  const status = () => git.status()

  return (
    <Show when={status().enabled}>
      <box
        border={["left", "right", "top", "bottom"]}
        customBorderChars={RoundedBorder.customBorderChars}
        borderColor={theme.border}
        paddingLeft={1}
        paddingRight={1}
        flexShrink={0}
      >
        <box flexDirection="row" gap={1}>
          <text fg={theme.primary}>{status().branch}</text>
          <Show when={status().dirty > 0}>
            <text fg={theme.warning}>~{status().dirty}</text>
          </Show>
          <Show when={status().untracked.length > 0}>
            <text fg={theme.warning}>?{status().untracked.length}</text>
          </Show>
          <Show when={status().ahead > 0}>
            <text fg={theme.success}>↑{status().ahead}</text>
          </Show>
          <Show when={status().behind > 0}>
            <text fg={theme.error}>↓{status().behind}</text>
          </Show>
        </box>
      </box>
    </Show>
  )
}

// Background agents indicator - floating pill
function BackgroundAgentsBlock() {
  const sync = useSync()
  const { theme } = useTheme()

  const running = createMemo(() => sync.data.backgroundAgent.filter((a) => a.status === "running"))

  return (
    <Show when={running().length > 0}>
      <box
        border={["left", "right", "top", "bottom"]}
        customBorderChars={RoundedBorder.customBorderChars}
        borderColor={theme.accent}
        paddingLeft={1}
        paddingRight={1}
        flexShrink={0}
      >
        <box flexDirection="row" gap={1}>
          <text fg={theme.accent}>⚡</text>
          <text fg={theme.text}>{running().length}</text>
        </box>
      </box>
    </Show>
  )
}

// Swarm connection indicator - floating pill
function SwarmBlock() {
  const sync = useSync()
  const { theme } = useTheme()

  const swarmStatus = () => sync.data.swarm.status

  const borderColor = () => {
    switch (swarmStatus()) {
      case "active": return theme.success
      case "failed": return theme.error
      default: return theme.border
    }
  }

  const statusSymbol = () => {
    switch (swarmStatus()) {
      case "active": return "●"
      case "failed": return "✗"
      default: return "○"
    }
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
        <text fg={theme.textMuted}>swarm</text>
        <text fg={borderColor()}>{statusSymbol()}</text>
      </box>
    </box>
  )
}

// Main unified status bar - floating waybar style
export function UnifiedStatusBar() {
  const sync = useSync()
  const [sessions, setSessions] = createSignal<SessionEntry[]>([])
  const currentPid = process.pid

  // Check if hyprland multi-session is enabled
  const isHyprlandEnabled = () => sync.data.config.hyprland === true

  // Poll for session updates
  onMount(() => {
    const update = async () => {
      if (!isHyprlandEnabled()) {
        setSessions([])
        return
      }
      const allSessions = await Hyprland.getSessions()
      const sorted = allSessions.sort(
        (a, b) => (a.hyprWorkspace ?? 999) - (b.hyprWorkspace ?? 999)
      )
      setSessions(sorted)
    }

    update()
    const interval = setInterval(update, 2000)
    onCleanup(() => clearInterval(interval))
  })

  return (
    <box height={3} flexDirection="row" justifyContent="space-between" gap={1} flexShrink={0}>
      {/* LEFT: Swarm + Background agents + Session blocks */}
      <box flexDirection="row" gap={1} alignItems="center">
        <SwarmBlock />
        <BackgroundAgentsBlock />
        
        {/* Session blocks - only if hyprland enabled */}
        <Show when={sessions().length > 0}>
          <For each={sessions()}>
            {(session) => (
              <SessionBlock
                session={session}
                isCurrent={session.pid === currentPid}
              />
            )}
          </For>
        </Show>
      </box>

      {/* RIGHT: Git info */}
      <box flexDirection="row" gap={1} alignItems="center">
        <GitBlock />
      </box>
    </box>
  )
}
