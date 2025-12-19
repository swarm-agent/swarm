import { Show, createMemo } from "solid-js"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { BackgroundAgentSpinner } from "@tui/ui/tool-animations"

export function BackgroundAgentIndicator() {
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
      <box flexDirection="row" gap={1}>
        <BackgroundAgentSpinner state={state()} />
        <text fg={theme.textMuted}>
          {running().length} bg{running().length > 1 ? "s" : ""}
        </text>
      </box>
    </Show>
  )
}
