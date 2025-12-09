import { For, Show, createMemo, createSignal, onMount, onCleanup } from "solid-js"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { useLocal } from "@tui/context/local"
import { useGit } from "@tui/context/git"
import { useRoute } from "@tui/context/route"
import { Hyprland, type SessionEntry } from "@/hyprland"
import { RGBA } from "@opentui/core"
import type { AssistantMessage } from "@opencode-ai/sdk"
import { BackgroundAgentSpinner, ThinkingIndicator } from "@tui/ui/tool-animations"

// Hyprland workspace session block - RICH INFO
function SessionBlock(props: {
  session: SessionEntry
  isCurrent: boolean
}) {
  const { theme } = useTheme()
  const sync = useSync()
  const local = useLocal()

  // Status colors
  const statusColor = () => {
    switch (props.session.status) {
      case "blocked": return theme.warning
      case "working": return theme.success
      default: return theme.textMuted
    }
  }

  const statusSymbol = () => {
    switch (props.session.status) {
      case "blocked": return "!"
      case "working": return "●"
      default: return "○"
    }
  }

  // Background: highlighted for current
  const bgColor = () => {
    if (props.isCurrent) {
      return RGBA.fromInts(60, 65, 85, 255) // Highlighted blue-gray
    }
    return RGBA.fromInts(40, 44, 62, 255) // Dark gray
  }

  // Calculate context % using sessionId
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

  // Context color gradient: blue -> red
  const ctxColor = () => {
    const pct = contextPct()
    if (pct === undefined) return theme.textMuted
    const t = Math.min(pct, 100) / 100
    const r = Math.round(74 + t * 181)
    const g = Math.round(158 - t * 84)
    const b = Math.round(255 - t * 148)
    return RGBA.fromInts(r, g, b)
  }

  // Agent color
  const agentColor = () => {
    const agent = props.session.agent
    if (!agent) return theme.textMuted
    return local.agent.color(agent)
  }

  return (
    <box
      backgroundColor={bgColor()}
      paddingLeft={1}
      paddingRight={1}
      flexDirection="row"
      gap={1}
      flexShrink={0}
    >
      {/* Workspace indicator */}
      <text fg={props.isCurrent ? theme.primary : theme.text}>
        {props.isCurrent ? "*" : ""}W{props.session.hyprWorkspace ?? "?"}
      </text>
      {/* Status dot */}
      <text fg={statusColor()}>{statusSymbol()}</text>
      {/* Agent name */}
      <Show when={props.session.agent}>
        <text fg={agentColor()}>{props.session.agent}</text>
      </Show>
      {/* Context % */}
      <Show when={contextPct() !== undefined}>
        <text fg={ctxColor()}>{contextPct()}%</text>
      </Show>
    </box>
  )
}

// Background agents block
function BackgroundAgentsBlock() {
  const sync = useSync()
  const { theme } = useTheme()

  const running = createMemo(() => sync.data.backgroundAgent.filter((a) => a.status === "running"))

  const state = createMemo(() => {
    const count = running().length
    if (count >= 3) return "busy" as const
    if (count >= 1) return "active" as const
    return "idle" as const
  })

  return (
    <Show when={running().length > 0}>
      <box
        backgroundColor={RGBA.fromInts(50, 55, 75, 255)}
        paddingLeft={1}
        paddingRight={1}
        flexDirection="row"
        gap={1}
        flexShrink={0}
      >
        <BackgroundAgentSpinner state={state()} />
        <text fg={theme.textMuted}>
          {running().length}bg
        </text>
      </box>
    </Show>
  )
}

// Git status block
function GitBlock() {
  const git = useGit()
  const { theme } = useTheme()

  const status = () => git.status()

  return (
    <Show when={status().enabled}>
      <box
        backgroundColor={RGBA.fromInts(45, 50, 70, 255)}
        paddingLeft={1}
        paddingRight={1}
        flexDirection="row"
        gap={1}
        flexShrink={1}
      >
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
    </Show>
  )
}

// Current session block - shows model, context%, thinking, agent
function AgentBlock() {
  const local = useLocal()
  const sync = useSync()
  const route = useRoute()
  const { theme } = useTheme()

  // Get current session ID
  const sessionID = createMemo(() => {
    if (route.data.type === "session") return route.data.sessionID
    return undefined
  })

  // Calculate context %
  const contextInfo = createMemo(() => {
    const sid = sessionID()
    if (!sid) return undefined
    const messages = sync.data.message[sid] ?? []
    const last = messages.findLast((x) => x.role === "assistant" && x.tokens.output > 0) as AssistantMessage
    if (!last) return undefined
    const total = last.tokens.input + last.tokens.output + last.tokens.reasoning + last.tokens.cache.read + last.tokens.cache.write
    const model = sync.data.provider.find((x) => x.id === last.providerID)?.models[last.modelID]
    if (!model?.limit.context) return undefined
    return Math.round((total / model.limit.context) * 100)
  })

  // Context color gradient: blue -> red
  const contextColor = () => {
    const pct = contextInfo()
    if (pct === undefined) return theme.textMuted
    const t = Math.min(pct, 100) / 100
    const r = Math.round(74 + t * 181)
    const g = Math.round(158 - t * 84)
    const b = Math.round(255 - t * 148)
    return RGBA.fromInts(r, g, b)
  }

  // Format model name (remove "Claude " prefix, "(latest)" etc)
  const modelName = () => {
    let name = local.model.parsed().model
    name = name.replace(/\s*\([^)]*\)\s*/g, "").trim()
    name = name.replace(/^Claude\s+/i, "")
    return name.toLowerCase()
  }

  return (
    <box
      backgroundColor={RGBA.fromInts(55, 60, 80, 255)}
      paddingLeft={1}
      paddingRight={1}
      flexDirection="row"
      gap={1}
      flexShrink={0}
    >
      {/* Model name */}
      <text fg={theme.textMuted}>{modelName()}</text>
      
      {/* Context % */}
      <Show when={contextInfo() !== undefined}>
        <text fg={contextColor()}>{contextInfo()}%</text>
      </Show>
      
      {/* Thinking indicator */}
      <Show when={local.thinking.enabled}>
        <text><ThinkingIndicator /></text>
      </Show>
      
      {/* Agent name */}
      <text fg={local.agent.color(local.agent.current().name)}>
        {local.agent.current().name}
      </text>
    </box>
  )
}

// Swarm status block (logo + connection)
function SwarmBlock() {
  const sync = useSync()
  const { theme } = useTheme()

  const swarmStatus = () => sync.data.swarm.status

  const statusColor = () => {
    switch (swarmStatus()) {
      case "active": return theme.success
      case "failed": return theme.error
      default: return theme.textMuted
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
      backgroundColor={RGBA.fromInts(50, 55, 75, 255)}
      paddingLeft={1}
      paddingRight={1}
      flexDirection="row"
      gap={1}
      flexShrink={0}
    >
      <text fg={theme.text}>swarm</text>
      <text fg={statusColor()}>{statusSymbol()}</text>
    </box>
  )
}

// Main unified status bar
export function UnifiedStatusBar() {
  const sync = useSync()
  const route = useRoute()
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

  // Current session ID from route
  const currentSessionId = createMemo(() => {
    if (route.data.type === "session") {
      return route.data.sessionID
    }
    return undefined
  })

  return (
    <box height={1} flexDirection="row" justifyContent="space-between" flexShrink={0}>
      {/* LEFT SIDE: Swarm + Sessions */}
      <box flexDirection="row" gap={1} flexShrink={1} minWidth={0}>
        <SwarmBlock />
        <BackgroundAgentsBlock />
        
        {/* Session blocks - only if hyprland multi-session enabled */}
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

      {/* RIGHT SIDE: Git + Agent */}
      <box flexDirection="row" gap={1} flexShrink={0}>
        <GitBlock />
        <AgentBlock />
      </box>
    </box>
  )
}
