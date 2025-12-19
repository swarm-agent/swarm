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

type SessionFilter = "primary" | "background" | "sdk"

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
  const [sessionFilter, setSessionFilter] = createSignal<SessionFilter>("primary")

  const mcpError = createMemo(() => {
    return Object.values(sync.data.mcp).some((x) => x.status === "failed")
  })

  // Helper to get tag for a session
  function getSessionTag(session: typeof sync.data.session[0]): string | null {
    if (session.source === "sdk") return "SDK"
    if (session.source === "background") return "BG"
    if (session.parentID) return "Sub"
    return null
  }

  const sessions = createMemo(() => {
    const filter = sessionFilter()
    
    // Filter sessions based on source
    // Primary: no parentID and source is undefined or "tui"
    // Background: in-app background agents (source=background or has parentID but not SDK)
    // SDK: source is "sdk"
    const filteredSessions = sync.data.session
      .filter((x) => {
        if (filter === "primary") {
          return x.parentID === undefined && (x.source === undefined || x.source === "tui")
        } else if (filter === "sdk") {
          return x.source === "sdk"
        } else {
          // Background: in-app background agents and subagents (not SDK)
          return (x.source === "background" || x.parentID !== undefined) && x.source !== "sdk"
        }
      })
      .sort((a, b) => b.time.updated - a.time.updated)

    const childrenMap = new Map<string, typeof sync.data.session>()
    for (const session of sync.data.session) {
      if (session.parentID) {
        const existing = childrenMap.get(session.parentID) ?? []
        existing.push(session)
        childrenMap.set(session.parentID, existing)
      }
    }

    return filteredSessions.slice(0, 5).map((session) => {
      const children = childrenMap.get(session.id) ?? []
      const tag = getSessionTag(session)
      
      // Build display title with tag
      let displayTitle = session.title
      if (tag && filter !== "primary") {
        displayTitle = `[${tag}] ${session.title}`
      }

      if (children.length === 0) {
        return { ...session, displayTitle }
      }

      const agentNames = children
        .map((child) => {
          const match = child.title.match(/@(\w+)\s+subagent/)
          return match ? match[1] : null
        })
        .filter((name) => name !== null)

      if (agentNames.length === 0) {
        return { ...session, displayTitle }
      }

      const uniqueAgents = [...new Set(agentNames)]
      const childrenSummary = ` • ${children.length} subagent${children.length > 1 ? "s" : ""} (${uniqueAgents.join(", ")})`

      return {
        ...session,
        displayTitle: displayTitle + childrenSummary,
      }
    })
  })
  
  // Count sessions in each category for the UI
  const sessionCounts = createMemo(() => {
    let primary = 0
    let background = 0
    let sdk = 0
    for (const x of sync.data.session) {
      if (x.parentID === undefined && (x.source === undefined || x.source === "tui")) {
        primary++
      } else if (x.source === "sdk") {
        sdk++
      } else {
        background++
      }
    }
    return { primary, background, sdk }
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
        if (index === 0) {
          // At top of list, unfocus
          setListFocused(false)
          setSelectedIndex(-1)
          prompt.focus()
        } else {
          setSelectedIndex(Math.max(index - 1, 0))
        }
      } else if (evt.name === "left") {
        // Cycle filters: primary -> sdk -> background -> primary
        const current = sessionFilter()
        const newFilter = current === "primary" ? "sdk" : current === "sdk" ? "background" : "primary"
        setSessionFilter(newFilter)
        setSelectedIndex(0)
      } else if (evt.name === "right") {
        // Cycle filters reverse: primary -> background -> sdk -> primary
        const current = sessionFilter()
        const newFilter = current === "primary" ? "background" : current === "background" ? "sdk" : "primary"
        setSessionFilter(newFilter)
        setSelectedIndex(0)
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

      <Show when={sync.data.session.length > 0}>
        <box width="100%" maxWidth={75} flexDirection="column">
          <box flexDirection="row" gap={2} marginBottom={1}>
            <text
              fg={sessionFilter() === "primary" ? theme.primary : theme.textMuted}
              bold={sessionFilter() === "primary"}
            >
              Sessions ({sessionCounts().primary})
            </text>
            <text fg={theme.textMuted}>|</text>
            <text
              fg={sessionFilter() === "background" ? theme.primary : theme.textMuted}
              bold={sessionFilter() === "background"}
            >
              Background ({sessionCounts().background})
            </text>
            <text fg={theme.textMuted}>|</text>
            <text
              fg={sessionFilter() === "sdk" ? theme.primary : theme.textMuted}
              bold={sessionFilter() === "sdk"}
            >
              SDK ({sessionCounts().sdk})
            </text>
            <Show when={listFocused()}>
              <text fg={theme.textMuted}> ← →</text>
            </Show>
          </box>
          <For each={sessions()}>
            {(session, index) => {
              const isSelected = () => listFocused() && selectedIndex() === index()
              return (
                <box
                  flexDirection="row"
                  justifyContent="space-between"
                  onMouseUp={() => route.navigate({ type: "session", sessionID: session.id })}
                >
                  <text fg={isSelected() ? theme.primary : theme.text}>{Locale.truncate(session.displayTitle, 50)}</text>
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
    </box>
  )
}
