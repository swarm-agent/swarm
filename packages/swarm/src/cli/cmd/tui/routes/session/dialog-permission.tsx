import { ScrollBoxRenderable, TextareaRenderable, TextAttributes } from "@opentui/core"
import { useTheme } from "../../context/theme"
import { useDialog } from "../../ui/dialog"
import { createStore } from "solid-js/store"
import { createMemo, For, onMount, Show } from "solid-js"
import { useKeyboard, useTerminalDimensions } from "@opentui/solid"
import { Locale } from "@/util/locale"
import type { Permission } from "@/permission"
import { useSDK } from "../../context/sdk"
import { LANGUAGE_EXTENSIONS } from "@/lsp/language"
import * as path from "path"
import { useLocal } from "../../context/local"
import { ToolCard, StreamingDots, SuccessCheckmark, ElapsedTime } from "@tui/ui/tool-animations"

// Safe stringify that handles circular references and non-serializable values
function safeStringify(value: unknown): string {
  if (typeof value === "string") return value
  if (value === null) return "null"
  if (value === undefined) return "undefined"
  if (typeof value === "function") return "[Function]"

  try {
    return JSON.stringify(value)
  } catch {
    // Circular reference or other non-serializable value
    if (typeof value === "object") {
      return "[Object]"
    }
    return String(value)
  }
}

export type DialogPermissionProps = {
  // Use getter function to allow reactive updates when new permissions arrive
  // without re-creating the dialog component (preserves scroll position, selected index, etc.)
  getPermissions: () => Permission.Info[]
  sessionID: string
}

export function DialogPermission(props: DialogPermissionProps) {
  const dialog = useDialog()
  const sdk = useSDK()
  const { theme, syntax } = useTheme()
  const dimensions = useTerminalDimensions()
  const local = useLocal()

  // Access permissions reactively through the getter
  const permissions = createMemo(() => props.getPermissions())

  const [store, setStore] = createStore({
    selectedIndex: 0,
    selectedAgentIndex: 0,
    inputMode: false,
    inputModeType: "reject" as "reject" | "approve-with-comment",
    rejectionMessage: "",
  })

  let diffScrollBox: ScrollBoxRenderable
  let rejectionTextarea: TextareaRenderable

  const currentPermission = createMemo(() => {
    const perms = permissions()
    if (!perms.length) return null
    const index = Math.min(store.selectedIndex, perms.length - 1)
    return perms[index]
  })
  const hasMultiple = createMemo(() => permissions().length > 1)

  const availableAgents = createMemo(() => {
    const current = local.agent.current()
    return local.agent.list().filter((a) => a.name !== current.name)
  })
  const selectedAgent = createMemo(() => {
    const perm = currentPermission()
    if (!perm || perm.type !== "exit-plan-mode") return null
    const agents = availableAgents()
    const defaultAgent = (perm.metadata.switchToAgent as string) || "build"
    const defaultIndex = agents.findIndex((a) => a.name === defaultAgent)
    const index = defaultIndex >= 0 ? defaultIndex : 0
    return agents[Math.min(store.selectedAgentIndex ?? index, agents.length - 1)]
  })

  onMount(() => {
    const perm = currentPermission()
    if (perm && perm.type === "exit-plan-mode") {
      const defaultAgent = (perm.metadata.switchToAgent as string) || "build"
      const agents = availableAgents()
      const index = agents.findIndex((a) => a.name === defaultAgent)
      if (index >= 0) setStore("selectedAgentIndex", index)
    }
  })

  // Get diff content for edit permissions
  const diffContent = createMemo(() => {
    const perm = currentPermission()
    if (!perm || perm.type !== "edit") return null
    return perm.metadata.diff as string | undefined
  })

  // Get content for write permissions
  const writeContent = createMemo(() => {
    const perm = currentPermission()
    if (!perm || perm.type !== "write") return null
    return perm.metadata.content as string | undefined
  })

  // File type for syntax highlighting (for edit and write permissions)
  const filetype = createMemo(() => {
    const perm = currentPermission()
    if (!perm || (perm.type !== "edit" && perm.type !== "write")) return "none"
    if (!perm.metadata.filePath) return "none"
    const filePath = perm.metadata.filePath as string
    const ext = path.extname(filePath)
    const language = LANGUAGE_EXTENSIONS[ext]
    if (["typescriptreact", "javascriptreact", "javascript"].includes(language)) return "typescript"
    return language || "none"
  })

  // Calculate max heights dynamically based on screen size
  // Reserve space for: header (3), mode indicator (2), details header (1), actions (4), padding (4) = ~14 lines fixed
  const fixedOverhead = createMemo(() => {
    const perm = currentPermission()
    // exit-plan-mode needs way less overhead (just header + bottom bar)
    if (perm?.type === "exit-plan-mode") {
      return 6
    }
    return 14
  })
  const availableHeight = createMemo(() => {
    const perm = currentPermission()
    // exit-plan-mode can use almost the entire terminal height
    const heightPercent = perm?.type === "exit-plan-mode" ? 0.95 : 0.8
    return Math.max(20, Math.floor(dimensions().height * heightPercent) - fixedOverhead())
  })

  // Dynamically scale content sections based on available height
  const detailsMaxHeight = createMemo(() => {
    const perm = currentPermission()
    // exit-plan-mode has no details to show, minimize this section
    if (perm?.type === "exit-plan-mode") {
      return 0
    }
    return Math.max(3, Math.min(8, Math.floor(availableHeight() * 0.2)))
  })
  const diffMaxHeight = createMemo(() => {
    const perm = currentPermission()
    // Give exit-plan-mode MAXIMUM space - use 98% of available height with very high cap
    if (perm?.type === "exit-plan-mode") {
      return Math.max(30, Math.min(150, Math.floor(availableHeight() * 0.98)))
    }
    // Regular permissions use 60%
    return Math.max(10, Math.min(30, Math.floor(availableHeight() * 0.6)))
  })
  const maxMetadataLines = createMemo(() => Math.max(2, Math.min(6, Math.floor(availableHeight() * 0.1))))

  onMount(() => {
    dialog.setSize("large")
  })

  useKeyboard((evt) => {
    const perm = currentPermission()

    // Input mode handling
    if (store.inputMode) {
      if (evt.name === "return") {
        const message = rejectionTextarea?.plainText?.trim() || ""
        if (store.inputModeType === "approve-with-comment") {
          // Submit approval with comment
          if (perm && message) {
            respondToPermission(perm.id, { type: "once", message: `[USER APPROVED BUT COMMENTED]: ${message}` })
          } else if (perm) {
            respondToPermission(perm.id, "once")
          }
        } else {
          // Submit rejection with message
          if (perm && message) {
            respondToPermission(perm.id, { type: "reject", message })
          } else if (perm) {
            respondToPermission(perm.id, "reject")
          }
        }
        setStore("inputMode", false)
        setStore("inputModeType", "reject")
        setStore("rejectionMessage", "")
        evt.preventDefault()
        return
      }
      if (evt.name === "escape") {
        // Exit input mode
        setStore("inputMode", false)
        setStore("inputModeType", "reject")
        setStore("rejectionMessage", "")
        evt.preventDefault()
        return
      }
      // Let textarea handle other keys
      return
    }

    // Navigate between multiple permissions using n/p (vim-style)
    // Tab is reserved for exit-plan-mode agent selection, so we use n/p for all permission types
    if (hasMultiple()) {
      const perms = permissions()
      if (evt.name === "n") {
        const next = (store.selectedIndex + 1) % perms.length
        setStore("selectedIndex", next)
        evt.preventDefault()
        return
      }
      if (evt.name === "p") {
        const prev = (store.selectedIndex - 1 + perms.length) % perms.length
        setStore("selectedIndex", prev)
        evt.preventDefault()
        return
      }
      // Also support tab/shift+tab for non-exit-plan-mode permissions
      if (perm?.type !== "exit-plan-mode") {
        if (evt.name === "tab" && !evt.shift) {
          const next = (store.selectedIndex + 1) % perms.length
          setStore("selectedIndex", next)
          evt.preventDefault()
          return
        }
        if (evt.name === "tab" && evt.shift) {
          const prev = (store.selectedIndex - 1 + perms.length) % perms.length
          setStore("selectedIndex", prev)
          evt.preventDefault()
          return
        }
      }
    }

    // Agent selection for exit-plan-mode permissions (left/right arrows or tab)
    if (perm && perm.type === "exit-plan-mode") {
      if (evt.name === "left") {
        const agents = availableAgents()
        const prev = (store.selectedAgentIndex - 1 + agents.length) % agents.length
        setStore("selectedAgentIndex", prev)
        evt.preventDefault()
        return
      }
      if (evt.name === "right" || evt.name === "tab") {
        const agents = availableAgents()
        const next = (store.selectedAgentIndex + 1) % agents.length
        setStore("selectedAgentIndex", next)
        evt.preventDefault()
        return
      }
    }

    // Scroll navigation for diff/plan content
    if (perm) {
      // For exit-plan-mode, only scroll with up/down (left/right are for agent selection)
      // For other types, allow both arrow keys and j/k vim bindings
      const isUpKey = evt.name === "up" || (perm.type !== "exit-plan-mode" && evt.name === "k")
      const isDownKey = evt.name === "down" || (perm.type !== "exit-plan-mode" && evt.name === "j")

      if (isUpKey) {
        if (diffScrollBox) {
          diffScrollBox.scrollBy(-1)
        }
        evt.preventDefault()
      }
      if (isDownKey) {
        if (diffScrollBox) {
          diffScrollBox.scrollBy(1)
        }
        evt.preventDefault()
      }
    }

    // Actions
    if (((evt.name === "return" && evt.shift) || (evt.name === "c" && evt.shift)) && perm?.type === "exit-plan-mode") {
      // Shift+Enter or Shift+C = approve with comment (for exit-plan-mode only)
      setStore("inputMode", true)
      setStore("inputModeType", "approve-with-comment")
      evt.preventDefault()
      queueMicrotask(() => {
        if (rejectionTextarea && !rejectionTextarea.isDestroyed) {
          rejectionTextarea.focus()
        }
      })
      return
    }
    if (evt.name === "return" && !evt.shift) {
      if (perm) respondToPermission(perm.id, "once")
      evt.preventDefault()
    }
    if (evt.name === "a") {
      const perm = currentPermission()
      if (perm) respondToPermission(perm.id, "always")
      evt.preventDefault()
    }
    if (evt.name === "d" && !evt.shift) {
      const perm = currentPermission()
      if (perm) respondToPermission(perm.id, "reject")
      evt.preventDefault()
    }
    if (evt.name === "i" || (evt.name === "d" && evt.shift && perm?.type === "exit-plan-mode")) {
      // i or Shift+D (exit-plan-mode) = deny with message
      setStore("inputMode", true)
      setStore("inputModeType", "reject")
      evt.preventDefault()
      // Use queueMicrotask for immediate next-tick focus (more predictable than setTimeout)
      queueMicrotask(() => {
        if (rejectionTextarea && !rejectionTextarea.isDestroyed) {
          rejectionTextarea.focus()
        }
      })
      return
    }
    if (evt.name === "r") {
      rejectAll()
      evt.preventDefault()
    }
    if (evt.name === "escape") {
      // Reset input mode before rejecting to prevent state leak
      setStore("inputMode", false)
      setStore("rejectionMessage", "")
      rejectAll()
      evt.preventDefault()
    }
  })

  function respondToPermission(permissionID: string, response: Permission.Response) {
    const perm = permissions().find((p) => p.id === permissionID)

    const normalizedResponse = typeof response === "object" && "type" in response ? response.type : response

    // IMPORTANT: Compute the selected agent directly from the permission being responded to,
    // NOT from selectedAgent() which depends on currentPermission(). This fixes the bug where
    // agent selection was lost when approving with Shift+C because currentPermission() might
    // point to a different permission than the one being responded to.
    let selectedAgentName: string | undefined = undefined
    if (perm && perm.type === "exit-plan-mode") {
      const agents = availableAgents()
      const defaultAgent = (perm.metadata.switchToAgent as string) || "build"
      const defaultIndex = agents.findIndex((a) => a.name === defaultAgent)
      const fallbackIndex = defaultIndex >= 0 ? defaultIndex : 0
      const agentIndex = Math.min(store.selectedAgentIndex ?? fallbackIndex, agents.length - 1)
      selectedAgentName = agents[agentIndex]?.name
    }

    // Switch agent BEFORE sending the response
    // This ensures the next message from the agent uses the new agent mode
    if (perm && perm.type === "exit-plan-mode" && (normalizedResponse === "once" || normalizedResponse === "always")) {
      if (selectedAgentName) local.agent.set(selectedAgentName)
    }

    // Extract message from original response if present (for approve-with-comment)
    const originalMessage = typeof response === "object" && "message" in response ? response.message : undefined

    // Prepare response body - include selected agent for exit-plan-mode permissions
    const responseBody: any =
      perm && perm.type === "exit-plan-mode" && (normalizedResponse === "once" || normalizedResponse === "always")
        ? { type: normalizedResponse, agent: selectedAgentName, ...(originalMessage && { message: originalMessage }) }
        : response

    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: {
          permissionID,
          id: props.sessionID,
        },
        body: {
          response: responseBody as any,
        },
      })
      .catch((error) => {
        // If permission response fails, force clear the dialog to prevent stuck state
        console.error("Failed to respond to permission:", error)
        // For exit-plan-mode, always clear dialog on error to prevent stuck state
        if (perm && perm.type === "exit-plan-mode") {
          dialog.clear()
        }
      })

    // Update selected index to handle the removed permission
    // The dialog will auto-close via the reactive effect in session/index.tsx
    // when permissions array becomes empty
    const currentIndex = permissions().findIndex((p) => p.id === permissionID)
    if (currentIndex !== -1 && store.selectedIndex >= currentIndex && store.selectedIndex > 0) {
      setStore("selectedIndex", Math.max(0, store.selectedIndex - 1))
    }
  }

  function rejectAll() {
    for (const permission of permissions()) {
      sdk.client
        .postSessionIdPermissionsPermissionId({
          path: {
            permissionID: permission.id,
            id: props.sessionID,
          },
          body: {
            response: "reject",
          },
        })
        .catch((error) => {
          console.error("Failed to reject permission:", error)
        })
    }
    // Note: dialog.clear() is NOT called here - the global escape handler
    // in dialog.tsx handles closing the dialog to prevent double-clear race conditions.
    // The dialog will close automatically via the reactive effect in session/index.tsx
    // when all permissions are removed from the store.
  }

  // Truncate metadata entries for responsiveness (exclude diff for edit, content for write)
  const visibleMetadata = createMemo(() => {
    const perm = currentPermission()
    if (!perm) return []
    const entries = Object.entries(perm.metadata).filter(([key]) => {
      if (key === "diff") return false
      if (key === "content" && perm.type === "write") return false
      return true
    })
    const maxLines = maxMetadataLines()
    if (entries.length <= maxLines) return entries
    return entries.slice(0, maxLines)
  })

  const hasMoreMetadata = createMemo(() => {
    const perm = currentPermission()
    if (!perm) return false
    const entries = Object.entries(perm.metadata).filter(([key]) => {
      if (key === "diff") return false
      if (key === "content" && perm.type === "write") return false
      return true
    })
    return entries.length > maxMetadataLines()
  })

  return (
    <Show when={currentPermission()}>
      <box
        gap={1}
        flexDirection="column"
        height="100%"
        padding={currentPermission()!.type === "exit-plan-mode" ? 1 : 2}
      >
        {/* Header */}
        <box
          flexDirection="row"
          justifyContent="space-between"
          paddingBottom={currentPermission()!.type === "exit-plan-mode" ? 0 : 1}
        >
          <text attributes={TextAttributes.BOLD} fg={theme.warning}>
            <Show
              when={currentPermission()!.type === "exit-plan-mode"}
              fallback={
                <>
                  ⚠ Permission Request
                  <Show when={hasMultiple()}>
                    {" "}
                    <span style={{ fg: theme.textMuted }}>
                      ({store.selectedIndex + 1}/{permissions().length}) n/p to navigate
                    </span>
                  </Show>
                </>
              }
            >
              {currentPermission()!.metadata?.isUpdate ? "Review Updated Plan" : "Exit Plan Mode"}
              <Show when={currentPermission()!.metadata?.isUpdate}>
                {" "}
                <span style={{ fg: theme.accent }}>[Updated]</span>
              </Show>
              <Show when={hasMultiple()}>
                {" "}
                <span style={{ fg: theme.textMuted }}>
                  ({store.selectedIndex + 1}/{permissions().length}) n/p to navigate
                </span>
              </Show>
            </Show>
          </text>
          <text fg={theme.textMuted}>esc to reject</text>
        </box>

        {/* Current permission details - Scrollable (hide for exit-plan-mode as we show everything in header) */}
        <Show when={currentPermission()!.type !== "exit-plan-mode"}>
          <scrollbox
            paddingTop={1}
            paddingBottom={1}
            gap={1}
            maxHeight={detailsMaxHeight()}
            scrollbarOptions={{ visible: false }}
          >
            <box paddingLeft={1} paddingRight={1} gap={1}>
              <text attributes={TextAttributes.BOLD} fg={theme.primary}>
                {currentPermission()!.title}
              </text>

              {/* Show indicator for subagent permissions */}
              <Show when={currentPermission()!.metadata?.originSessionID}>
                <text fg={theme.accent} attributes={TextAttributes.BOLD}>
                  ⚡ From subagent: {(currentPermission()!.metadata?.originSessionTitle as string) || "Unknown"}
                </text>
              </Show>

              <box flexDirection="row" gap={1}>
                <text fg={theme.textMuted}>Type:</text>
                <text fg={theme.accent} attributes={TextAttributes.BOLD}>
                  {currentPermission()!.type}
                </text>
              </box>

              {/* Show file path for edit permissions */}
              <Show when={currentPermission()!.type === "edit" && currentPermission()!.metadata.filePath}>
                <box flexDirection="row" gap={1}>
                  <text fg={theme.textMuted}>File:</text>
                  <text fg={theme.primary}>{currentPermission()!.metadata.filePath as string}</text>
                </box>
              </Show>

              <Show when={currentPermission()!.pattern}>
                <box flexDirection="row" gap={1}>
                  <text fg={theme.textMuted}>Pattern:</text>
                  <text fg={theme.primary}>
                    {Array.isArray(currentPermission()!.pattern)
                      ? (currentPermission()!.pattern as string[]).join(", ")
                      : (currentPermission()!.pattern as string)}
                  </text>
                </box>
              </Show>

              {/* Show metadata */}
              <Show when={visibleMetadata().length > 0}>
                <box paddingTop={1} gap={1}>
                  <text fg={theme.textMuted} attributes={TextAttributes.BOLD}>
                    Details:
                  </text>
                  <For each={visibleMetadata()}>
                    {([key, value]) => (
                      <box flexDirection="row" gap={1} paddingLeft={1}>
                        <text fg={theme.accent}>•</text>
                        <text fg={theme.textMuted}>
                          {Locale.truncate(`${key}: ${safeStringify(value)}`, Math.max(60, dimensions().width - 20))}
                        </text>
                      </box>
                    )}
                  </For>
                  <Show when={hasMoreMetadata()}>
                    <text fg={theme.textMuted} attributes={TextAttributes.ITALIC}>
                      ... and{" "}
                      {Object.entries(currentPermission()!.metadata).filter(([key]) => {
                        if (key === "diff") return false
                        if (key === "content" && currentPermission()!.type === "write") return false
                        return true
                      }).length - maxMetadataLines()}{" "}
                      more
                    </text>
                  </Show>
                </box>
              </Show>
            </box>
          </scrollbox>
        </Show>

        {/* Diff display for Edit permissions - using native diff component */}
        <Show when={currentPermission()!.type === "edit" && diffContent()}>
          <scrollbox
            ref={(r: ScrollBoxRenderable) => (diffScrollBox = r)}
            paddingTop={1}
            paddingBottom={1}
            maxHeight={diffMaxHeight()}
            scrollbarOptions={{ visible: false }}
          >
            <box paddingLeft={1}>
              <diff
                diff={diffContent()!}
                view={dimensions().width > 120 ? "split" : "unified"}
                filetype={filetype()}
                syntaxStyle={syntax()}
                showLineNumbers={true}
                width="100%"
                wrapMode="word"
              />
            </box>
          </scrollbox>
        </Show>

        {/* Content display for Write permissions - using code component with syntax highlighting */}
        <Show when={currentPermission()!.type === "write" && writeContent()}>
          <scrollbox
            ref={(r: ScrollBoxRenderable) => (diffScrollBox = r)}
            paddingTop={1}
            paddingBottom={1}
            maxHeight={diffMaxHeight()}
            scrollbarOptions={{ visible: false }}
          >
            <box paddingLeft={1}>
              <code filetype={filetype()} syntaxStyle={syntax()} content={writeContent()!} />
            </box>
          </scrollbox>
        </Show>

        {/* Plan display for exit-plan-mode permissions */}
        <Show when={currentPermission()!.type === "exit-plan-mode" && currentPermission()!.metadata.plan}>
          <scrollbox
            ref={(r: ScrollBoxRenderable) => (diffScrollBox = r)}
            paddingTop={0}
            paddingBottom={0}
            maxHeight={diffMaxHeight()}
            scrollbarOptions={{ visible: false }}
          >
            <box paddingLeft={1} paddingRight={1}>
              <code filetype="markdown" syntaxStyle={syntax()} content={currentPermission()!.metadata.plan as string} />
            </box>
          </scrollbox>
        </Show>

        {/* Input mode - Message textarea (for reject or approve-with-comment) */}
        <Show when={store.inputMode}>
          <box paddingTop={1} paddingBottom={1} gap={1} flexShrink={0}>
            <box paddingLeft={1} paddingRight={1}>
              <text
                attributes={TextAttributes.BOLD}
                fg={store.inputModeType === "approve-with-comment" ? theme.success : theme.warning}
              >
                {store.inputModeType === "approve-with-comment" ? "Approve with Instructions" : "Rejection Message"}
              </text>
            </box>
            <box paddingLeft={1} paddingRight={1}>
              <textarea
                ref={(r: TextareaRenderable) => (rejectionTextarea = r)}
                placeholder={
                  store.inputModeType === "approve-with-comment"
                    ? "Enter additional instructions for the agent..."
                    : "Enter rejection reason for the agent..."
                }
                initialValue={store.rejectionMessage}
                keyBindings={[]}
                minHeight={1}
                maxHeight={10}
              />
            </box>
            <box paddingLeft={1} paddingRight={1}>
              <text fg={theme.textMuted}>
                {store.inputModeType === "approve-with-comment"
                  ? "Press ↵ to approve with instructions, esc to cancel"
                  : "Press ↵ to reject with message, esc to cancel"}
              </text>
            </box>
          </box>
        </Show>

        {/* Minimal bottom row - Agent on left, actions on right */}
        <Show when={!store.inputMode}>
          {(() => {
            const isCompact = () => dimensions().width < 90
            const isVeryCompact = () => dimensions().width < 70
            return (
              <box paddingTop={1} flexShrink={0} borderColor={theme.border} borderStyle="single" border={["top"]}>
                <box
                  flexDirection="row"
                  justifyContent="space-between"
                  alignItems="center"
                  paddingLeft={1}
                  paddingRight={1}
                >
                  {/* Agent selection for exit-plan-mode - left side */}
                  <Show when={currentPermission()!.type === "exit-plan-mode"}>
                    <box flexDirection="row" gap={1} alignItems="center">
                      <For each={availableAgents()}>
                        {(agent, index) => (
                          <>
                            <Show when={index() === store.selectedAgentIndex}>
                              <text>
                                <b style={{ fg: local.agent.color(agent.name) }}>{agent.name}</b>
                              </text>
                            </Show>
                            <Show when={index() !== store.selectedAgentIndex}>
                              <text fg={theme.textMuted}>{agent.name}</text>
                            </Show>
                            <Show when={index() < availableAgents().length - 1}>
                              <text fg={theme.textMuted}>|</text>
                            </Show>
                          </>
                        )}
                      </For>
                    </box>
                  </Show>

                  {/* Spacer for non-exit-plan-mode */}
                  <Show when={currentPermission()!.type !== "exit-plan-mode"}>
                    <box />
                  </Show>

                  {/* Actions - responsive based on width */}
                  <box flexDirection="row" gap={isCompact() ? 1 : 2}>
                    <text>
                      <span style={{ fg: theme.primary, attributes: TextAttributes.BOLD }}>↵</span>
                      <Show when={!isVeryCompact()} fallback={null}>
                        <span style={{ fg: theme.text }}> once</span>
                      </Show>
                    </text>
                    <text>
                      <span style={{ fg: theme.success, attributes: TextAttributes.BOLD }}>a</span>
                      <Show when={!isVeryCompact()} fallback={null}>
                        <span style={{ fg: theme.text }}> always</span>
                      </Show>
                    </text>
                    <text>
                      <span style={{ fg: theme.error, attributes: TextAttributes.BOLD }}>d</span>
                      <Show when={!isVeryCompact()} fallback={null}>
                        <span style={{ fg: theme.text }}> deny</span>
                      </Show>
                    </text>
                    {/* For exit-plan-mode: Shift+D for deny+msg, Shift+Enter for approve+msg */}
                    {/* For other permissions: i for deny+msg */}
                    <Show when={currentPermission()!.type !== "exit-plan-mode"}>
                      <text>
                        <span style={{ fg: theme.warning, attributes: TextAttributes.BOLD }}>i</span>
                        <Show when={!isVeryCompact()} fallback={null}>
                          <span style={{ fg: theme.text }}> msg</span>
                        </Show>
                      </text>
                    </Show>
                    <Show when={currentPermission()!.type === "exit-plan-mode"}>
                      <text>
                        <span style={{ fg: theme.error, attributes: TextAttributes.BOLD }}>D</span>
                        <Show when={!isVeryCompact()} fallback={null}>
                          <span style={{ fg: theme.text }}> deny+msg</span>
                        </Show>
                      </text>
                      <text>
                        <span style={{ fg: theme.success, attributes: TextAttributes.BOLD }}>C</span>
                        <Show when={!isVeryCompact()} fallback={null}>
                          <span style={{ fg: theme.text }}> ok+msg</span>
                        </Show>
                      </text>
                    </Show>
                    <Show when={hasMultiple() && !isCompact()}>
                      <text>
                        <span style={{ fg: theme.warning, attributes: TextAttributes.BOLD }}>r</span>
                        <span style={{ fg: theme.text }}> all</span>
                      </text>
                    </Show>
                    <Show when={currentPermission()!.type === "exit-plan-mode"}>
                      <text>
                        <span style={{ fg: theme.accent, attributes: TextAttributes.BOLD }}>tab</span>
                        <Show when={!isVeryCompact()} fallback={null}>
                          <span style={{ fg: theme.text }}> agent</span>
                        </Show>
                      </text>
                      <Show when={!isCompact()}>
                        <text>
                          <span style={{ fg: theme.textMuted, attributes: TextAttributes.BOLD }}>↑↓</span>
                          <span style={{ fg: theme.text }}> scroll</span>
                        </text>
                      </Show>
                    </Show>
                    <Show when={currentPermission()!.type !== "exit-plan-mode" && !isCompact()}>
                      <text>
                        <span style={{ fg: theme.textMuted, attributes: TextAttributes.BOLD }}>↑↓</span>
                        <span style={{ fg: theme.text }}> scroll</span>
                      </text>
                    </Show>
                  </box>
                </box>
              </box>
            )
          })()}
        </Show>
      </box>
    </Show>
  )
}
