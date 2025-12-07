import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { Show } from "solid-js"

export function SwarmIndicator() {
  const sync = useSync()
  const { theme } = useTheme()

  const getColor = () => {
    switch (sync.data.swarm.status) {
      case "active":
        return theme.success
      case "failed":
        return theme.error
      case "inactive":
      default:
        return theme.textMuted
    }
  }

  const getSymbol = () => {
    switch (sync.data.swarm.status) {
      case "active":
        return "●"
      case "failed":
        return "◌"
      case "inactive":
      default:
        return "○"
    }
  }

  return (
    <box flexDirection="row" gap={1}>
      <text fg={getColor()}>{getSymbol()}</text>
    </box>
  )
}
