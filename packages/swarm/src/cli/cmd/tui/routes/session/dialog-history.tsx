import { ScrollBoxRenderable, TextAttributes } from "@opentui/core"
import { useTheme } from "../../context/theme"
import { useDialog } from "../../ui/dialog"
import { createStore } from "solid-js/store"
import { createMemo, createResource, For, onMount, Show } from "solid-js"
import { useKeyboard, useTerminalDimensions } from "@opentui/solid"
import { Locale } from "@/util/locale"
import { useSDK } from "../../context/sdk"
import { useSync } from "../../context/sync"
import type { AssistantMessage, UserMessage } from "@swarm-ai/sdk"
import type { PromptRef } from "../../component/prompt"
import type { PromptInfo } from "../../component/prompt/history"
import path from "path"
import { parsePatch } from "diff"
import { LANGUAGE_EXTENSIONS } from "@/lsp/language"

export type DialogHistoryProps = {
  sessionID: string
  prompt: PromptRef
}

export function DialogHistory(props: DialogHistoryProps) {
  const dialog = useDialog()
  const sdk = useSDK()
  const sync = useSync()
  const { theme, syntax } = useTheme()
  const dimensions = useTerminalDimensions()

  const [store, setStore] = createStore({
    selectedIndex: 0,
    focusedPanel: "left" as "left" | "right", // Track which panel has focus
  })

  let messageScrollBox: ScrollBoxRenderable
  let changesScrollBox: ScrollBoxRenderable

  // Get all messages in the conversation
  const messages = createMemo(() => sync.data.message[props.sessionID] ?? [])

  // Group messages by turn (user message + following assistant messages)
  type HistoryTurn = {
    userMessage: UserMessage
    assistantMessages: AssistantMessage[]
    timestamp: number
    userPreview: string
    assistantPreview: string
    filesChanged: string[]
    snapshotFrom?: string
    snapshotTo?: string
  }

  const historyTurns = createMemo(() => {
    const turns: HistoryTurn[] = []
    const msgs = messages()

    let currentTurn: HistoryTurn | null = null

    for (const msg of msgs) {
      if (msg.role === "user") {
        // Save previous turn if exists
        if (currentTurn) {
          turns.push(currentTurn)
        }

        // Start new turn
        const parts = sync.data.part[msg.id] ?? []
        let preview = ""
        for (const part of parts) {
          if (part.type === "text" && !part.synthetic) {
            preview = part.text
            break
          }
        }

        currentTurn = {
          userMessage: msg as UserMessage,
          assistantMessages: [],
          timestamp: msg.time?.created ?? 0,
          userPreview: preview || "(no text)",
          assistantPreview: "",
          filesChanged: [],
        }
      } else if (msg.role === "assistant" && currentTurn) {
        // Add assistant message to current turn
        currentTurn.assistantMessages.push(msg as AssistantMessage)

        // Accumulate preview text from all assistant messages
        const parts = sync.data.part[msg.id] ?? []
        for (const part of parts) {
          if (part.type === "text" && !part.synthetic) {
            const text = part.text.trim()
            if (text && currentTurn.assistantPreview) {
              currentTurn.assistantPreview += " "
            }
            currentTurn.assistantPreview += text
          }
          // Collect file changes from patches
          if (part.type === "patch") {
            const patchPart = part as any
            if (patchPart.files) {
              currentTurn.filesChanged.push(...patchPart.files)
            }
          }
          // Track snapshots for diff computation
          if (part.type === "step-start" && part.snapshot && !currentTurn.snapshotFrom) {
            currentTurn.snapshotFrom = part.snapshot
          }
          if (part.type === "step-finish" && part.snapshot) {
            currentTurn.snapshotTo = part.snapshot
          }
        }
      }
    }

    // Add last turn
    if (currentTurn) {
      turns.push(currentTurn)
    }

    return turns
  })

  const selectedTurn = createMemo(() => {
    const turns = historyTurns()
    if (turns.length === 0) return null
    const index = Math.min(store.selectedIndex, turns.length - 1)
    return turns[index]
  })

  // Parse and format diff for display - unified format showing only changes
  const parsedDiffs = createMemo(() => {
    const turn = selectedTurn()
    if (!turn) return null

    // Check if there are any file changes
    if (turn.filesChanged.length === 0) return null

    // Check if summary diffs are available for the user message
    const userMsg = sync.data.message[props.sessionID]?.find((m) => m.id === turn.userMessage.id)
    if (userMsg && "summary" in userMsg && typeof userMsg.summary === "object" && userMsg.summary?.diffs) {
      const allDiffs = userMsg.summary.diffs

      // Process each diff into a unified format showing changes with context
      return allDiffs.map((diffData: any) => {
        const beforeLines = diffData.before.split("\n")
        const afterLines = diffData.after.split("\n")

        // Build a unified diff showing changes with 3 lines of context
        const CONTEXT_LINES = 3
        const unifiedLines: string[] = []
        let i = 0
        let j = 0
        let contextBuffer: string[] = []
        let hasChanges = false

        while (i < beforeLines.length || j < afterLines.length) {
          const beforeLine = beforeLines[i]
          const afterLine = afterLines[j]

          // Lines match - add to context buffer
          if (beforeLine === afterLine && beforeLine !== undefined) {
            const contextLine = `  ${beforeLine}`

            if (hasChanges) {
              // We've seen changes, add context after changes
              contextBuffer.push(contextLine)
              if (contextBuffer.length > CONTEXT_LINES) {
                // Flush buffer with only last N lines
                unifiedLines.push(...contextBuffer.slice(0, CONTEXT_LINES))
                contextBuffer = []
                hasChanges = false
              }
            } else {
              // No changes yet, buffer context before changes
              contextBuffer.push(contextLine)
              if (contextBuffer.length > CONTEXT_LINES) {
                contextBuffer.shift()
              }
            }
            i++
            j++
          }
          // Lines differ - show the change
          else {
            // Flush context buffer before showing changes
            if (contextBuffer.length > 0) {
              unifiedLines.push(...contextBuffer)
              contextBuffer = []
            }
            hasChanges = true

            // Show removed lines
            if (beforeLine !== undefined && beforeLine !== afterLine) {
              unifiedLines.push(`- ${beforeLine}`)
              i++
            }
            // Show added lines
            if (afterLine !== undefined && afterLine !== beforeLine) {
              unifiedLines.push(`+ ${afterLine}`)
              j++
            }
          }
        }

        // Calculate line numbers for hunk header
        const totalLines = beforeLines.length
        const changedLines = unifiedLines.filter((l) => l.startsWith("+") || l.startsWith("-")).length

        // Detect file type for syntax highlighting
        const ext = path.extname(diffData.file)
        const language = LANGUAGE_EXTENSIONS[ext]
        let filetype = language || "diff"
        if (["typescriptreact", "javascriptreact", "javascript"].includes(language)) {
          filetype = "typescript"
        }

        return {
          file: diffData.file,
          content: unifiedLines.join("\n"),
          filetype,
          additions: diffData.additions || 0,
          deletions: diffData.deletions || 0,
          totalLines,
          changedLines,
        }
      })
    }

    return null
  })

  const turnDiffFallback = createMemo(() => {
    const turn = selectedTurn()
    if (!turn) return "No turn selected"
    if (turn.filesChanged.length === 0) return "No file changes in this turn"
    return `Files changed:\n${turn.filesChanged.map((f) => `  ${path.basename(f)}`).join("\n")}`
  })

  // Calculate responsive heights
  const fixedOverhead = 8 // header (3), actions (4), minimal padding
  const availableHeight = createMemo(() => Math.max(20, dimensions().height - fixedOverhead))

  // Calculate 30/70 split for left (turns) and right (changes) columns with responsive breakpoints
  const leftWidth = createMemo(() => {
    const width = dimensions().width
    // Responsive breakpoints for left column (turns list)
    if (width < 80) return Math.max(20, Math.floor(width * 0.35)) // Very narrow: 35%
    if (width < 120) return Math.max(30, Math.floor(width * 0.32)) // Narrow: 32%
    return Math.max(35, Math.floor(width * 0.3)) // Normal: 30%
  })

  const rightWidth = createMemo(() => {
    const width = dimensions().width
    const left = leftWidth()
    const padding = 6 // Account for padding between columns
    return Math.max(40, width - left - padding)
  })

  onMount(() => {
    dialog.setSize("large")

    // Start at the last turn (most recent)
    const turns = historyTurns()
    if (turns.length > 0) {
      setStore("selectedIndex", turns.length - 1)
    }
  })

  useKeyboard((evt) => {
    const turns = historyTurns()
    if (turns.length === 0) return

    // Left/Right navigation between panels
    if (evt.name === "left" || evt.name === "h") {
      setStore("focusedPanel", "left")
      evt.preventDefault()
      return
    }

    if (evt.name === "right" || evt.name === "l") {
      setStore("focusedPanel", "right")
      evt.preventDefault()
      return
    }

    // Up/Down navigation depends on focused panel
    if (evt.name === "up" || evt.name === "k") {
      if (store.focusedPanel === "left") {
        // Navigate turns list
        const prev = Math.max(0, store.selectedIndex - 1)
        setStore("selectedIndex", prev)

        // Auto-scroll message list
        if (messageScrollBox) {
          messageScrollBox.scrollTo(prev)
        }
      } else {
        // Scroll changes panel up
        if (changesScrollBox) {
          changesScrollBox.scrollBy(-1)
        }
      }
      evt.preventDefault()
      return
    }

    if (evt.name === "down" || evt.name === "j") {
      if (store.focusedPanel === "left") {
        // Navigate turns list
        const next = Math.min(turns.length - 1, store.selectedIndex + 1)
        setStore("selectedIndex", next)

        // Auto-scroll message list
        if (messageScrollBox) {
          messageScrollBox.scrollTo(next)
        }
      } else {
        // Scroll changes panel down
        if (changesScrollBox) {
          changesScrollBox.scrollBy(1)
        }
      }
      evt.preventDefault()
      return
    }

    // Confirm revert
    if (evt.name === "return") {
      const turn = selectedTurn()
      if (!turn) return

      revertToCheckpoint(turn)
      evt.preventDefault()
      return
    }

    if (evt.name === "escape") {
      dialog.clear()
      evt.preventDefault()
      return
    }
  })

  function revertToCheckpoint(turn: HistoryTurn) {
    // Call the session revert API
    sdk.client.session.revert({
      path: { id: props.sessionID },
      body: { messageID: turn.userMessage.id },
    })

    // Restore the message text to the prompt for editing
    const parts = sync.data.part[turn.userMessage.id]
    if (parts) {
      props.prompt.set(
        parts.reduce(
          (agg, part) => {
            if (part.type === "text" && !part.synthetic) {
              agg.input += part.text
            }
            if (part.type === "file") {
              agg.parts.push(part)
            }
            return agg
          },
          { input: "", parts: [] as PromptInfo["parts"] },
        ),
      )
    }

    // Close the dialog
    dialog.clear()
  }

  function formatTimestamp(timestamp: number): string {
    const date = new Date(timestamp)
    const now = new Date()
    const diffMs = now.getTime() - date.getTime()
    const diffMins = Math.floor(diffMs / 60000)

    if (diffMins < 1) return "just now"
    if (diffMins < 60) return `${diffMins}m ago`
    const diffHours = Math.floor(diffMins / 60)
    if (diffHours < 24) return `${diffHours}h ago`
    const diffDays = Math.floor(diffHours / 24)
    return `${diffDays}d ago`
  }

  return (
    <box flexDirection="column" height="100%">
      {/* Header */}
      <box flexDirection="row" justifyContent="space-between" paddingLeft={2} paddingRight={2} paddingTop={1}>
        <text attributes={TextAttributes.BOLD}>Conversation History</text>
        <text fg={theme.textMuted}>esc</text>
      </box>

      {/* Main content: 50/50 split */}
      <box flexDirection="row" flexGrow={1} paddingTop={1}>
        {/* LEFT SIDE: Conversation turns list */}
        <box flexDirection="column" width={leftWidth()} paddingLeft={2} paddingRight={1}>
          <text fg={store.focusedPanel === "left" ? theme.primary : theme.accent} attributes={TextAttributes.BOLD}>
            {store.focusedPanel === "left" ? "► " : ""}Turns ({historyTurns().length})
          </text>

          <scrollbox
            ref={(r: ScrollBoxRenderable) => (messageScrollBox = r)}
            backgroundColor={theme.backgroundElement}
            maxHeight={availableHeight()}
            verticalScrollbarOptions={{ visible: true }}
            horizontalScrollbarOptions={{ visible: false }}
          >
            <box paddingLeft={1} paddingRight={1} gap={1}>
              <For each={historyTurns()}>
                {(turn, idx) => {
                  const isSelected = createMemo(() => idx() === store.selectedIndex)
                  const previewWidth = leftWidth() - 6

                  return (
                    <box
                      flexDirection="column"
                      backgroundColor={isSelected() ? theme.primary : theme.backgroundElement}
                      paddingLeft={1}
                      paddingRight={1}
                      paddingTop={0}
                      paddingBottom={0}
                    >
                      <box flexDirection="row" gap={1} justifyContent="space-between">
                        <text
                          fg={isSelected() ? theme.background : theme.text}
                          attributes={isSelected() ? TextAttributes.BOLD : undefined}
                        >
                          Turn {idx() + 1}
                        </text>
                        <text fg={isSelected() ? theme.background : theme.textMuted}>
                          {formatTimestamp(turn.timestamp)}
                        </text>
                      </box>

                      <text fg={isSelected() ? theme.background : theme.accent} attributes={TextAttributes.BOLD}>
                        You:
                      </text>
                      <text fg={isSelected() ? theme.background : theme.textMuted} overflow="hidden" wrapMode="none">
                        {Locale.truncate(turn.userPreview, Math.max(40, previewWidth))}
                      </text>

                      <Show when={turn.assistantPreview}>
                        <text fg={isSelected() ? theme.background : theme.accent} attributes={TextAttributes.BOLD}>
                          Assistant:
                        </text>
                        <text fg={isSelected() ? theme.background : theme.textMuted} overflow="hidden" wrapMode="none">
                          {Locale.truncate(turn.assistantPreview, Math.max(40, previewWidth))}
                        </text>
                      </Show>

                      <Show when={turn.filesChanged.length > 0}>
                        <text fg={isSelected() ? theme.background : theme.textMuted}>
                          {turn.filesChanged.length} file{turn.filesChanged.length !== 1 ? "s" : ""} changed
                        </text>
                      </Show>
                    </box>
                  )
                }}
              </For>
            </box>
          </scrollbox>
        </box>

        {/* RIGHT SIDE: Git diff display */}
        <box flexDirection="column" width={rightWidth()} paddingLeft={1} paddingRight={2}>
          <box flexDirection="row" gap={2} alignItems="center" justifyContent="space-between">
            <text fg={store.focusedPanel === "right" ? theme.primary : theme.accent} attributes={TextAttributes.BOLD}>
              {store.focusedPanel === "right" ? "► " : ""}Changes
            </text>
            <Show when={parsedDiffs() && parsedDiffs()!.length > 0}>
              <text fg={theme.textMuted}>
                {parsedDiffs()!.length} file{parsedDiffs()!.length !== 1 ? "s" : ""} •{" "}
                <span style={{ fg: theme.success }}>+{parsedDiffs()!.reduce((sum: any, d: any) => sum + d.additions, 0)}</span>{" "}
                <span style={{ fg: theme.error }}>-{parsedDiffs()!.reduce((sum: any, d: any) => sum + d.deletions, 0)}</span>
              </text>
            </Show>
          </box>

          <Show
            when={parsedDiffs() && parsedDiffs()!.length > 0}
            fallback={
              <scrollbox
                ref={(r: ScrollBoxRenderable) => (changesScrollBox = r)}
                backgroundColor={theme.backgroundElement}
                maxHeight={availableHeight()}
                verticalScrollbarOptions={{ visible: true }}
                horizontalScrollbarOptions={{ visible: false }}
              >
                <box paddingLeft={1} paddingRight={1}>
                  <text fg={theme.text} wrapMode="char">
                    {turnDiffFallback()}
                  </text>
                </box>
              </scrollbox>
            }
          >
            <scrollbox
              ref={(r: ScrollBoxRenderable) => (changesScrollBox = r)}
              paddingTop={1}
              paddingBottom={1}
              backgroundColor={theme.backgroundElement}
              maxHeight={availableHeight()}
              verticalScrollbarOptions={{ visible: true }}
              horizontalScrollbarOptions={{ visible: false }}
            >
              <box paddingLeft={1} paddingRight={1} flexDirection="column" gap={3}>
                <For each={parsedDiffs()}>
                  {(diff) => (
                    <box flexDirection="column" gap={1}>
                      {/* File header with stats - enterprise style */}
                      <box
                        flexDirection="column"
                        backgroundColor={theme.backgroundPanel}
                        paddingLeft={1}
                        paddingRight={1}
                        paddingTop={0}
                        paddingBottom={0}
                      >
                        <box flexDirection="row" gap={2} alignItems="center" justifyContent="space-between">
                          <box flexDirection="row" gap={2} alignItems="center">
                            <text fg={theme.accent} attributes={TextAttributes.BOLD}>
                              ◉
                            </text>
                            <text fg={theme.text} attributes={TextAttributes.BOLD}>
                              {diff.file}
                            </text>
                          </box>
                          <box flexDirection="row" gap={2}>
                            <text fg={theme.textMuted}>{diff.totalLines} lines</text>
                            <text>
                              <span style={{ fg: theme.success, attributes: TextAttributes.BOLD }}>
                                +{diff.additions}
                              </span>
                            </text>
                            <text>
                              <span style={{ fg: theme.error, attributes: TextAttributes.BOLD }}>
                                -{diff.deletions}
                              </span>
                            </text>
                          </box>
                        </box>
                        {/* Visual separator bar */}
                        <box flexDirection="row" gap={0}>
                          <Show when={diff.additions > 0}>
                            <text fg={theme.success}>
                              {"▓".repeat(
                                Math.min(20, Math.ceil((diff.additions / (diff.additions + diff.deletions)) * 20)),
                              )}
                            </text>
                          </Show>
                          <Show when={diff.deletions > 0}>
                            <text fg={theme.error}>
                              {"▓".repeat(
                                Math.min(20, Math.ceil((diff.deletions / (diff.additions + diff.deletions)) * 20)),
                              )}
                            </text>
                          </Show>
                        </box>
                      </box>

                      {/* Diff content with syntax highlighting */}
                      <box paddingLeft={1}>
                        <code filetype={diff.filetype} syntaxStyle={syntax()} content={diff.content} />
                      </box>
                    </box>
                  )}
                </For>
              </box>
            </scrollbox>
          </Show>
        </box>
      </box>

      {/* Actions bar at bottom */}
      <box paddingLeft={2} paddingRight={2} paddingTop={1} paddingBottom={1} flexShrink={0}>
        <box flexDirection="row" flexWrap="wrap" gap={1}>
          <text>
            <b style={{ fg: theme.primary }}>↵</b>
            <span style={{ fg: theme.textMuted }}> revert</span>
          </text>
          <text>
            <b style={{ fg: theme.textMuted }}>↑↓/jk</b>
            <span style={{ fg: theme.textMuted }}> navigate</span>
          </text>
          <text>
            <b style={{ fg: theme.textMuted }}>←→/hl</b>
            <span style={{ fg: theme.textMuted }}> switch panel</span>
          </text>
        </box>
      </box>
    </box>
  )
}
