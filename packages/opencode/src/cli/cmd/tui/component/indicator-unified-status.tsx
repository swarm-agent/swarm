import { For, Show, createMemo, createSignal, onMount, onCleanup } from "solid-js"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { useLocal } from "@tui/context/local"
import { useGit } from "@tui/context/git"
import { useRoute } from "@tui/context/route"
import { Hyprland, type SessionEntry } from "@/hyprland"
import { RoundedBorder } from "@tui/component/border"
import { RGBA } from "@opentui/core"
import type { AssistantMessage } from "@opencode-ai/sdk"

// Status icons (nerd fonts)
const STATUS_ICONS = {
  working: "",   // fa-angle_right
  blocked: "󰔷",  // md-triangle_outline
  idle: "",      // cod-dash
}

// Current session block - uses live app data (instant, no lag)
function CurrentSessionBlock(props: { hyprWorkspace?: number }) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()
  const route = useRoute()

  // Live data from app state
  const agent = () => local.agent.current().name
  const model = () => local.model.parsed().model
  const sessionId = () => route.data.type === "session" ? route.data.sessionID : undefined

  // Agent color - live
  const agentColor = () => {
    const a = agent()
    if (!a) return theme.primary
    return local.agent.color(a)
  }

  // Context % for current session
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

  // Short model name
  const modelShort = () => {
    const m = model()
    if (!m) return undefined
    let name = m.replace(/\s*\([^)]*\)\s*/g, "").trim()
    name = name.replace(/^Claude\s+/i, "")
    if (name.toLowerCase().includes("opus")) return "opus"
    if (name.toLowerCase().includes("sonnet")) return "sonnet"
    if (name.toLowerCase().includes("haiku")) return "haiku"
    return name.toLowerCase().slice(0, 8)
  }

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
        {/* Workspace */}
        <text fg={theme.text}>W{props.hyprWorkspace ?? "?"}</text>
        {/* Agent name */}
        <Show when={agent()}>
          <text fg={agentColor()}>{agent()}</text>
        </Show>
        {/* Model */}
        <Show when={modelShort()}>
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

// Other session block - reads from JSON (for other instances)
function OtherSessionBlock(props: { session: SessionEntry }) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()

  // Agent color
  const agentColor = () => {
    const agent = props.session.agent
    if (!agent) return theme.primary
    return local.agent.color(agent)
  }

  // Status indicator
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

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
      borderColor={theme.border}
      paddingLeft={1}
      paddingRight={1}
      flexShrink={0}
    >
      <box flexDirection="row" gap={1}>
        {/* Status icon */}
        <text fg={statusColor()}>{statusSymbol()}</text>
        {/* Workspace */}
        <text fg={theme.text}>W{props.session.hyprWorkspace ?? "?"}</text>
        {/* Agent name */}
        <Show when={props.session.agent}>
          <text fg={agentColor()}>{props.session.agent}</text>
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
  const [otherSessions, setOtherSessions] = createSignal<SessionEntry[]>([])
  const [currentWorkspace, setCurrentWorkspace] = createSignal<number | undefined>()
  const currentPid = process.pid

  // Check if hyprland multi-session is enabled
  const isHyprlandEnabled = () => sync.data.config.hyprland === true

  // Poll for OTHER sessions only (current session uses live app data)
  onMount(() => {
    const update = async () => {
      if (!isHyprlandEnabled()) {
        setOtherSessions([])
        return
      }
      const allSessions = await Hyprland.getSessions()
      // Find current session's workspace
      const current = allSessions.find(s => s.pid === currentPid)
      setCurrentWorkspace(current?.hyprWorkspace ?? undefined)
      // Filter out current session, sort by workspace
      const others = allSessions
        .filter(s => s.pid !== currentPid)
        .sort((a, b) => (a.hyprWorkspace ?? 999) - (b.hyprWorkspace ?? 999))
      setOtherSessions(others)
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
        
        {/* Current session - always shown if hyprland enabled, uses LIVE data */}
        <Show when={isHyprlandEnabled()}>
          <CurrentSessionBlock hyprWorkspace={currentWorkspace()} />
        </Show>
        
        {/* Other sessions - from JSON */}
        <For each={otherSessions()}>
          {(session) => <OtherSessionBlock session={session} />}
        </For>
      </box>

      {/* RIGHT: Git info */}
      <box flexDirection="row" gap={1} alignItems="center">
        <GitBlock />
      </box>
    </box>
  )
}
