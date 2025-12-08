import { useTheme } from "@tui/context/theme"
import { For, Show, createSignal, onMount, onCleanup } from "solid-js"
import { Hyprland, type SessionEntry } from "@/hyprland"

export function HyprlandIndicator() {
  const { theme } = useTheme()
  const [sessions, setSessions] = createSignal<SessionEntry[]>([])
  const [currentWorkspace, setCurrentWorkspace] = createSignal<number | null>(null)

  // Poll for session updates
  onMount(() => {
    const update = async () => {
      const [allSessions, workspace] = await Promise.all([
        Hyprland.getSessions(),
        Hyprland.getWorkspace(),
      ])
      // Filter to only show OTHER sessions (not current PID), sorted by workspace
      const currentPid = Hyprland.getCurrentSession()?.pid
      const others = allSessions
        .filter((s) => s.pid !== currentPid)
        .sort((a, b) => (a.hyprWorkspace ?? 999) - (b.hyprWorkspace ?? 999))
      setSessions(others)
      setCurrentWorkspace(workspace)
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
              W{session.hyprWorkspace ?? "?"}{getStatusSymbol(session.status)}
            </text>
          )}
        </For>
      </box>
    </Show>
  )
}
