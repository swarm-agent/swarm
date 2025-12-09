import { Prompt, type PromptRef } from "@tui/component/prompt"
import { createMemo, createSignal, For, Match, onMount, Show, Switch } from "solid-js"
import { useKeyboard } from "@opentui/solid"
import { useTheme } from "@tui/context/theme"
import { Locale } from "@/util/locale"
import { useSync } from "../context/sync"
import { Toast } from "../ui/toast"
import { useArgs } from "../context/args"
import { useRoute } from "../context/route"

// Nerd font icons for status indicators
const ICONS = {
  // Working
  chevron_right: "",
  angle_right: "",
  play: "",
  arrow_right: "",
  debug_start: "",
  
  // Blocked (triangles!)
  triangle: "󰔶",
  triangle_outline: "󰔷",
  alert: "󰀦",
  alert_outline: "󰀪",
  warning: "",
  error: "",
  oct_alert: "",
  
  // Idle
  circle: "",
  circle_small: "",
  circle_medium: "󰧞",
  dash: "",
  check: "",
}

let once = false

function getRelativeTime(timestamp: number): string {
  const now = Date.now()
  const diff = Math.abs(now - timestamp)
  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (seconds < 60) return "Now"
  if (minutes < 60) return `${minutes}m`
  if (hours < 24) return `${hours}h`
  return `${days}d`
}

export function Home() {
  const sync = useSync()
  const { theme } = useTheme()
  const route = useRoute()
  const [selectedIndex, setSelectedIndex] = createSignal(-1)
  const [listFocused, setListFocused] = createSignal(false)

  const mcpError = createMemo(() => {
    return Object.values(sync.data.mcp).some((x) => x.status === "failed")
  })

  const sessions = createMemo(() => {
    const parentSessions = sync.data.session
      .filter((x) => x.parentID === undefined)
      .sort((a, b) => b.time.updated - a.time.updated)

    const childrenMap = new Map<string, typeof sync.data.session>()
    for (const session of sync.data.session) {
      if (session.parentID) {
        const existing = childrenMap.get(session.parentID) ?? []
        existing.push(session)
        childrenMap.set(session.parentID, existing)
      }
    }

    return parentSessions.slice(0, 5).map((session) => {
      const children = childrenMap.get(session.id) ?? []

      if (children.length === 0) return session

      const agentNames = children
        .map((child) => {
          const match = child.title.match(/@(\w+)\s+subagent/)
          return match ? match[1] : null
        })
        .filter((name) => name !== null)

      if (agentNames.length === 0) return session

      const uniqueAgents = [...new Set(agentNames)]
      const childrenSummary = ` • ${children.length} subagent${children.length > 1 ? "s" : ""} (${uniqueAgents.join(", ")})`

      return {
        ...session,
        title: session.title + childrenSummary,
      }
    })
  })

  const Hint = (
    <Show when={Object.keys(sync.data.mcp).length > 0}>
      <box flexShrink={0} flexDirection="row" gap={1}>
        <text fg={theme.text}>
          <Switch>
            <Match when={mcpError()}>
              <span style={{ fg: theme.error }}>•</span> mcp errors{" "}
              <span style={{ fg: theme.textMuted }}>ctrl+x s</span>
            </Match>
            <Match when={true}>
              <span style={{ fg: theme.success }}>•</span>{" "}
              {Locale.pluralize(Object.values(sync.data.mcp).length, "{} mcp server", "{} mcp servers")}
            </Match>
          </Switch>
        </text>
      </box>
    </Show>
  )

  let prompt: PromptRef
  const args = useArgs()

  useKeyboard((evt) => {
    if (!prompt) return

    const focused = listFocused()
    const index = selectedIndex()
    const list = sessions()

    if (!focused && evt.name === "down" && list.length > 0) {
      setListFocused(true)
      setSelectedIndex(0)
      prompt.blur()
      return
    }

    if (focused && evt.name === "escape") {
      setListFocused(false)
      setSelectedIndex(-1)
      prompt.focus()
      return
    }

    if (focused) {
      if (evt.name === "down") {
        setSelectedIndex(Math.min(index + 1, list.length - 1))
      } else if (evt.name === "up") {
        setSelectedIndex(Math.max(index - 1, 0))
      } else if (evt.name === "return") {
        const session = list[index]
        if (session) {
          route.navigate({ type: "session", sessionID: session.id })
        }
      }
    }
  })

  onMount(() => {
    if (once) return
    if (args.prompt) {
      prompt.set({ input: args.prompt, parts: [] })
      once = true
    }
  })

  return (
    <box flexGrow={1} justifyContent="center" alignItems="center" paddingLeft={2} paddingRight={2} gap={2}>
      <box width="100%" maxWidth={75} zIndex={1000}>
        <Prompt ref={(r) => (prompt = r)} hint={Hint} />
      </box>

      <Show when={sessions().length > 0}>
        <box width="100%" maxWidth={75} flexDirection="column">
          <text fg={theme.textMuted}>Recent sessions</text>
          <For each={sessions()}>
            {(session, index) => {
              const isSelected = () => listFocused() && selectedIndex() === index()
              return (
                <box
                  flexDirection="row"
                  justifyContent="space-between"
                  onMouseUp={() => route.navigate({ type: "session", sessionID: session.id })}
                >
                  <text fg={isSelected() ? theme.primary : theme.text}>{Locale.truncate(session.title, 50)}</text>
                  <text fg={isSelected() ? theme.primary : theme.textMuted}>
                    {getRelativeTime(session.time.updated)}
                  </text>
                </box>
              )
            }}
          </For>
        </box>
      </Show>

      <Toast />

      {/* Icon Focus Groups - TEMPORARY PREVIEW */}
      <box width="100%" maxWidth={75} flexDirection="column" gap={1}>
        <text fg={theme.textMuted}>─── Status Icon Focus Groups ───</text>
        
        <box flexDirection="row" gap={2}>
          <text fg={theme.text}>SET A:</text>
          <text fg={theme.success}>{ICONS.chevron_right} working</text>
          <text fg={theme.error}>{ICONS.triangle} blocked</text>
          <text fg={theme.textMuted}>{ICONS.circle_small} idle</text>
        </box>
        
        <box flexDirection="row" gap={2}>
          <text fg={theme.text}>SET B:</text>
          <text fg={theme.success}>{ICONS.angle_right} working</text>
          <text fg={theme.error}>{ICONS.alert} blocked</text>
          <text fg={theme.textMuted}>(no idle)</text>
        </box>
        
        <box flexDirection="row" gap={2}>
          <text fg={theme.text}>SET C:</text>
          <text fg={theme.success}>{ICONS.play} working</text>
          <text fg={theme.error}>{ICONS.triangle_outline} blocked</text>
          <text fg={theme.textMuted}>{ICONS.dash} idle</text>
        </box>
        
        <box flexDirection="row" gap={2}>
          <text fg={theme.text}>SET D:</text>
          <text fg={theme.success}>{ICONS.arrow_right} working</text>
          <text fg={theme.error}>{ICONS.oct_alert} blocked</text>
          <text fg={theme.textMuted}>{ICONS.circle} idle</text>
        </box>

        <box flexDirection="row" gap={2}>
          <text fg={theme.text}>SET E:</text>
          <text fg={theme.success}>{ICONS.debug_start} working</text>
          <text fg={theme.error}>{ICONS.alert_outline} blocked</text>
          <text fg={theme.textMuted}>{ICONS.check} idle</text>
        </box>
      </box>
    </box>
  )
}
