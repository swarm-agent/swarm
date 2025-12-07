import { createSignal, createEffect, onCleanup, Show, createMemo } from "solid-js"
import { useSync } from "@tui/context/sync"
import { useTheme } from "@tui/context/theme"
import { ElapsedTime } from "@tui/ui/tool-animations"

const COMPRESS_ICON = "" // fa-compress
const FILE_ICON = "󰈙" // nf-md-file
const TODO_ICON = "󰄬" // nf-md-check
const GIT_ICON = "󰊢" // nf-md-git

export function CompactingCard(props: { sessionID: string }) {
  const sync = useSync()
  const { theme } = useTheme()

  const session = () => sync.session.get(props.sessionID)
  const isCompacting = () => !!session()?.time?.compacting
  const startTime = () => session()?.time?.compacting

  // Get REAL progress data from sync.data (the store)
  const progress = () => sync.data.compacting[props.sessionID]
  const step = () => progress()?.step ?? "started"
  const data = () => progress()?.data

  // Animated dots for current step
  const [dots, setDots] = createSignal("")

  createEffect(() => {
    if (!isCompacting()) return

    let count = 0
    const dotTimer = setInterval(() => {
      count = (count + 1) % 4
      setDots(".".repeat(count))
    }, 300)

    onCleanup(() => clearInterval(dotTimer))
  })

  // Current status text based on REAL step
  const statusText = createMemo(() => {
    const s = step()
    const d = data()
    switch (s) {
      case "started":
        return `Analyzing ${d?.messagesCount ?? "?"} messages (${(d?.tokensInput ?? 0).toLocaleString()} tokens)`
      case "streaming":
        return "Generating summary"
      case "context":
        return "Capturing context"
      default:
        return "Compacting"
    }
  })

  return (
    <Show when={isCompacting()}>
      <box flexShrink={0} flexDirection="column">
        {/* Compact single-line status */}
        <box flexDirection="row" gap={1} paddingLeft={1}>
          <text>
            <span style={{ fg: theme.warning }}>{COMPRESS_ICON}</span>
            <span style={{ fg: theme.warning, bold: true }}> COMPACTING</span>
            <span style={{ fg: theme.textMuted }}>
              {" "}
              {statusText()}
              {dots()}
            </span>
          </text>

          {/* Show REAL context counts when available */}
          <Show when={step() === "context"}>
            <text>
              <span style={{ fg: theme.accent }}>{FILE_ICON}</span>
              <span style={{ fg: theme.text }}>{data()?.filesCount ?? 0} </span>
              <span style={{ fg: theme.success }}>{TODO_ICON}</span>
              <span style={{ fg: theme.text }}>{data()?.todosCount ?? 0} </span>
              <span style={{ fg: theme.warning }}>{GIT_ICON}</span>
              <span style={{ fg: theme.text }}>{data()?.gitFiles ?? 0}</span>
            </text>
          </Show>

          <Show when={startTime()}>
            <text fg={theme.textMuted}>
              <ElapsedTime startTime={startTime()!} />
            </text>
          </Show>
        </box>
      </box>
    </Show>
  )
}
