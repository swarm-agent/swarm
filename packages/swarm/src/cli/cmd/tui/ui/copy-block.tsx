import { createSignal } from "solid-js"
import { useTheme } from "@tui/context/theme"
import { Clipboard } from "@tui/util/clipboard"
import { useToast } from "@tui/ui/toast"
import { useRenderer } from "@opentui/solid"
import { RGBA } from "@opentui/core"
import { RoundedBorder } from "../component/border"

/**
 * CopyBlock - A clickable block for commands that need manual execution (like sudo)
 * Displays a command with a copy button for easy clipboard access
 */
export function CopyBlock(props: { command: string; label?: string; icon?: string }) {
  const { theme } = useTheme()
  const toast = useToast()
  const renderer = useRenderer()
  const [hover, setHover] = createSignal(false)
  const [copied, setCopied] = createSignal(false)

  const handleCopy = async () => {
    // OSC 52 for terminal clipboard (works over SSH/tmux)
    const base64 = Buffer.from(props.command).toString("base64")
    const osc52 = `\x1b]52;c;${base64}\x07`
    const finalOsc52 = process.env["TMUX"] ? `\x1bPtmux;\x1b${osc52}\x1b\\` : osc52
    /* @ts-expect-error - writeOut is private but needed for OSC 52 */
    renderer.writeOut(finalOsc52)

    // System clipboard
    await Clipboard.copy(props.command)
      .then(() => {
        setCopied(true)
        toast.show({ message: "Command copied!", variant: "success", duration: 2000 })
        setTimeout(() => setCopied(false), 2000)
      })
      .catch(() => toast.show({ message: "Copy failed", variant: "error" }))
  }

  const label = () => props.label ?? "Run manually:"
  const icon = () => props.icon ?? "📋"

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      borderColor={theme.warning}
      customBorderChars={RoundedBorder.customBorderChars}
      paddingLeft={2}
      paddingRight={2}
      paddingTop={1}
      paddingBottom={1}
      gap={1}
    >
      {/* Label row */}
      <text>
        <span style={{ fg: theme.warning, bold: true }}>
          {icon()} {label()}
        </span>
      </text>

      {/* Command + Copy button row */}
      <box flexDirection="row" gap={2} alignItems="center">
        {/* Command box */}
        <box flexGrow={1} backgroundColor={theme.backgroundElement} paddingLeft={1} paddingRight={1}>
          <text fg={theme.text}>{props.command}</text>
        </box>

        {/* Copy Button */}
        <box
          paddingLeft={1}
          paddingRight={1}
          backgroundColor={hover() ? theme.primary : theme.backgroundElement}
          onMouseOver={() => setHover(true)}
          onMouseOut={() => setHover(false)}
          onMouseUp={handleCopy}
        >
          <text fg={hover() ? theme.background : theme.textMuted}>{copied() ? "✓ Copied" : "Copy"}</text>
        </box>
      </box>
    </box>
  )
}

/**
 * CopyBlockCompact - More minimal version without the full border
 * Just a left border accent and inline copy button
 */
export function CopyBlockCompact(props: { command: string; label?: string }) {
  const { theme } = useTheme()
  const toast = useToast()
  const renderer = useRenderer()
  const [hover, setHover] = createSignal(false)
  const [copied, setCopied] = createSignal(false)

  const handleCopy = async () => {
    const base64 = Buffer.from(props.command).toString("base64")
    const osc52 = `\x1b]52;c;${base64}\x07`
    const finalOsc52 = process.env["TMUX"] ? `\x1bPtmux;\x1b${osc52}\x1b\\` : osc52
    /* @ts-expect-error - writeOut is private but needed for OSC 52 */
    renderer.writeOut(finalOsc52)

    await Clipboard.copy(props.command)
      .then(() => {
        setCopied(true)
        toast.show({ message: "Copied!", variant: "success", duration: 1500 })
        setTimeout(() => setCopied(false), 1500)
      })
      .catch(() => toast.show({ message: "Copy failed", variant: "error" }))
  }

  return (
    <box border={["left"]} borderColor={theme.warning} paddingLeft={2} gap={1}>
      <text fg={theme.warning}>{props.label ?? "Run manually:"}</text>
      <box flexDirection="row" gap={2} alignItems="center">
        <box flexGrow={1} backgroundColor={theme.backgroundElement} paddingLeft={1} paddingRight={1}>
          <text fg={theme.text}>{props.command}</text>
        </box>
        <box
          paddingLeft={1}
          paddingRight={1}
          backgroundColor={hover() ? theme.primary : theme.backgroundElement}
          onMouseOver={() => setHover(true)}
          onMouseOut={() => setHover(false)}
          onMouseUp={handleCopy}
        >
          <text fg={hover() ? theme.background : theme.textMuted}>{copied() ? "✓" : "📋"}</text>
        </box>
      </box>
    </box>
  )
}

/**
 * MultiCopyBlock - For multiple commands that need to be copied together or separately
 */
export function MultiCopyBlock(props: { commands: string[]; label?: string }) {
  const { theme } = useTheme()
  const toast = useToast()
  const renderer = useRenderer()
  const [hoveredIndex, setHoveredIndex] = createSignal(-1)
  const [copiedIndex, setCopiedIndex] = createSignal(-1)
  const [hoverAll, setHoverAll] = createSignal(false)
  const [copiedAll, setCopiedAll] = createSignal(false)

  const copyOne = async (cmd: string, index: number) => {
    const base64 = Buffer.from(cmd).toString("base64")
    const osc52 = `\x1b]52;c;${base64}\x07`
    const finalOsc52 = process.env["TMUX"] ? `\x1bPtmux;\x1b${osc52}\x1b\\` : osc52
    /* @ts-expect-error - writeOut is private but needed for OSC 52 */
    renderer.writeOut(finalOsc52)

    await Clipboard.copy(cmd)
      .then(() => {
        setCopiedIndex(index)
        toast.show({ message: "Command copied!", variant: "success", duration: 1500 })
        setTimeout(() => setCopiedIndex(-1), 1500)
      })
      .catch(() => toast.show({ message: "Copy failed", variant: "error" }))
  }

  const copyAll = async () => {
    const allCommands = props.commands.join("\n")
    const base64 = Buffer.from(allCommands).toString("base64")
    const osc52 = `\x1b]52;c;${base64}\x07`
    const finalOsc52 = process.env["TMUX"] ? `\x1bPtmux;\x1b${osc52}\x1b\\` : osc52
    /* @ts-expect-error - writeOut is private but needed for OSC 52 */
    renderer.writeOut(finalOsc52)

    await Clipboard.copy(allCommands)
      .then(() => {
        setCopiedAll(true)
        toast.show({ message: "All commands copied!", variant: "success", duration: 2000 })
        setTimeout(() => setCopiedAll(false), 2000)
      })
      .catch(() => toast.show({ message: "Copy failed", variant: "error" }))
  }

  return (
    <box
      border={["left", "right", "top", "bottom"]}
      borderColor={theme.warning}
      customBorderChars={RoundedBorder.customBorderChars}
      paddingLeft={2}
      paddingRight={2}
      paddingTop={1}
      paddingBottom={1}
      gap={1}
    >
      {/* Header with copy all button */}
      <box flexDirection="row" justifyContent="space-between" alignItems="center">
        <text>
          <span style={{ fg: theme.warning, bold: true }}>📋 {props.label ?? "Run manually:"}</span>
        </text>
        <box
          paddingLeft={1}
          paddingRight={1}
          backgroundColor={hoverAll() ? theme.primary : theme.backgroundElement}
          onMouseOver={() => setHoverAll(true)}
          onMouseOut={() => setHoverAll(false)}
          onMouseUp={copyAll}
        >
          <text fg={hoverAll() ? theme.background : theme.textMuted}>{copiedAll() ? "✓ All Copied" : "Copy All"}</text>
        </box>
      </box>

      {/* Commands list - each explicitly rendered */}
      <box gap={1}>
        {props.commands.map((cmd, i) => (
          <box flexDirection="row" gap={2} alignItems="center">
            <box flexGrow={1} backgroundColor={theme.backgroundElement} paddingLeft={1} paddingRight={1}>
              <text fg={theme.text}>{cmd}</text>
            </box>
            <box
              paddingLeft={1}
              paddingRight={1}
              backgroundColor={hoveredIndex() === i ? theme.primary : theme.backgroundElement}
              onMouseOver={() => setHoveredIndex(i)}
              onMouseOut={() => setHoveredIndex(-1)}
              onMouseUp={() => copyOne(cmd, i)}
            >
              <text fg={hoveredIndex() === i ? theme.background : theme.textMuted}>
                {copiedIndex() === i ? "✓" : "📋"}
              </text>
            </box>
          </box>
        ))}
      </box>
    </box>
  )
}
