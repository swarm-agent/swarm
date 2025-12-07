import { Show, createMemo } from "solid-js"
import { useGit } from "@tui/context/git"
import { useTheme } from "@tui/context/theme"
import { useTerminalDimensions } from "@opentui/solid"
import { TextAttributes } from "@opentui/core"

export function GitStatus() {
  const git = useGit()
  const { theme } = useTheme()
  const dimensions = useTerminalDimensions()

  const status = git.status

  // Calculate available width for responsiveness
  const width = createMemo(() => dimensions().width)

  // Determine what to show based on width
  const showFiles = createMemo(() => width() >= 80)
  const showAheadBehind = createMemo(() => width() >= 60)
  const showGit = createMemo(() => width() >= 50 && status().enabled)

  // Format file list (show first 3 files)
  const fileDisplay = createMemo(() => {
    if (!status().dirty || !showFiles()) return ""
    if (status().dirty <= 3) return ` (${status().dirty})`
    return ` (+${status().dirty})`
  })

  // Format ahead/behind
  const aheadBehindDisplay = createMemo(() => {
    if (!showAheadBehind()) return ""
    const parts = []
    if (status().ahead > 0) parts.push(`󰄿${status().ahead}`)
    if (status().behind > 0) parts.push(`󰄼${status().behind}`)
    return parts.length > 0 ? ` ${parts.join(" ")}` : ""
  })

  return (
    <Show when={showGit()}>
      <box flexDirection="row" gap={1}>
        <text fg={theme.accent} attributes={TextAttributes.BOLD}>
          {status().branch}
        </text>
        <Show when={fileDisplay()}>
          <text fg={theme.textMuted}>{fileDisplay()}</text>
        </Show>
        <Show when={aheadBehindDisplay()}>
          <text fg={theme.primary}>{aheadBehindDisplay()}</text>
        </Show>
      </box>
    </Show>
  )
}
