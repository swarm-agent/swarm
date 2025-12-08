import { useTheme } from "@tui/context/theme"
import { For, Show, createSignal, onMount, onCleanup } from "solid-js"
import { Hyprland, type SessionEntry } from "@/hyprland"

export function HyprlandIndicator() {
  const { theme } = useTheme()
  const [sessions, setSessions] = createSignal<SessionEntry[]>([])
  const currentPid = process.pid

  // Poll for session updates
  onMount(() => {
    const update = async () => {
      const allSessions = await Hyprland.getSessions()
      // Show ALL sessions (including current), sorted by workspace
      const sorted = allSessions.sort(
        (a, b) => (a.hyprWorkspace ?? 999) - (b.hyprWorkspace ?? 999)
      )
      setSessions(sorted)
    }

    update()
    const interval = setInterval(update, 2000) // Poll every 2 seconds
    onCleanup(() => clearInterval(interval))
  })

  const getStatusColor = (status: SessionEntry["status"]) => {
    switch (status) {
      case "blocked":
        return theme.warning
      case "working":
        return theme.success
      case "idle":
      default:
        return theme.textMuted
    }
  }

  const getStatusSymbol = (status: SessionEntry["status"]) => {
    switch (status) {
      case "blocked":
        return "!"
      case "working":
        return "●"
      case "idle":
      default:
        return "○"
    }
  }

  return (
    <Show when={sessions().length > 0}>
      <box flexDirection="row" gap={1} paddingLeft={1}>
        <For each={sessions()}>
          {(session) => (
            <text fg={getStatusColor(session.status)}>
              {session.pid === currentPid ? "*" : ""}W{session.hyprWorkspace ?? "?"}{getStatusSymbol(session.status)}
            </text>
          )}
        </For>
      </box>
    </Show>
  )
}
