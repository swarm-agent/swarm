import {
  createContext,
  createEffect,
  createMemo,
  createSignal,
  For,
  Match,
  on,
  Show,
  Switch,
  useContext,
  type Component,
} from "solid-js"
import { Dynamic } from "solid-js/web"
import path from "path"
import { useRoute, useRouteData } from "@tui/context/route"
import { useSync } from "@tui/context/sync"
import { SplitBorder, RoundedBorder } from "@tui/component/border"
import { useTheme } from "@tui/context/theme"
import { BoxRenderable, ScrollBoxRenderable, TextAttributes, addDefaultParsers, RGBA } from "@opentui/core"
import { Prompt, type PromptRef } from "@tui/component/prompt"
import type {
  AssistantMessage,
  Part,
  ToolPart,
  UserMessage,
  TextPart,
  ReasoningPart,
  StepStartPart,
  StepFinishPart,
} from "@opencode-ai/sdk"
import { useLocal } from "@tui/context/local"
import { Locale } from "@/util/locale"
import type { Tool } from "@/tool/tool"
import type { ReadTool } from "@/tool/read"
import type { WriteTool } from "@/tool/write"
import { BashTool } from "@/tool/bash"
import type { GlobTool } from "@/tool/glob"
import { TodoWriteTool } from "@/tool/todo"
import type { GrepTool } from "@/tool/grep"
import type { ListTool } from "@/tool/ls"
import type { EditTool } from "@/tool/edit"
import type { PatchTool } from "@/tool/patch"
import type { WebFetchTool } from "@/tool/webfetch"
import type { TaskTool } from "@/tool/task"
import { ExitPlanModeTool } from "@/tool/exit-plan"
import type { AskUserTool } from "@/tool/ask-user"
import type { ManualCommandTool } from "@/tool/manual-command"
import { CopyBlock } from "@tui/ui/copy-block"
import { useKeyboard, useRenderer, useTerminalDimensions, type BoxProps, type JSX } from "@opentui/solid"
import { useSDK } from "@tui/context/sdk"
import { useCommandDialog } from "@tui/component/dialog-command"
import { Shimmer } from "@tui/ui/shimmer"
import {
  Spinner,
  PulsingDot,
  PulsingText,
  PulsingPencil,
  StreamingDots,
  SuccessCheckmark,
  ErrorX,
  PulsingBorder,
  ToolCard,
  ToolMetadata,
  ElapsedTime,
  ClaudeSpinner,
  BouncyDot,
  GenericSpinner,
  RainbowSpinner,
  RoundedSquareStreaming,
  RoundedSquareIdle,
  HexagonOutlineStreaming,
  HexagonOutlineIdle,
  SpinningDot,
  WavyDots,
  RotatingArrow,
  BashToolAnimation,
  PlanModeSpinner,
  EditDeltaMorphChain,
  WebfetchToolAnimation,
  AskUserToolAnimation,
  ReadToolAnimation,
  GlobToolAnimation,
  GrepToolAnimation,
  ListToolAnimation,
  ThinkingSpinner,
  AgentStreamingWave,
  InlinePermission,
  BackgroundAgentSpinner,
  GitToolAnimation,
  type GitOpType,
} from "@tui/ui/tool-animations"
import { getSpinner } from "@tui/ui/spinner-definitions"
import { getToolSpinner } from "@tui/ui/tool-spinner-map"
import { useKeybind } from "@tui/context/keybind"
import { Header } from "./header"
import { parsePatch } from "diff"
import { useDialog } from "../../ui/dialog"
import { DialogMessage } from "./dialog-message"
import type { PromptInfo } from "../../component/prompt/history"
import { iife } from "@/util/iife"
import { DialogConfirm } from "@tui/ui/dialog-confirm"
import { DialogTimeline } from "./dialog-timeline"
import { DialogPermission } from "./dialog-permission"
import { DialogAskUser } from "./dialog-ask-user"
import { DialogPin } from "./dialog-pin"
import type { Permission } from "@/permission"
import { DialogHistory } from "./dialog-history"
import { DialogSessionRename } from "../../component/dialog-session-rename"
import { CompactingCard } from "../../component/compacting-card"
import { Sidebar } from "./sidebar"
import { LANGUAGE_EXTENSIONS } from "@/lsp/language"
import parsers from "../../../../../../parsers-config.ts"
import { Clipboard } from "../../util/clipboard"
import { Toast, useToast, ErrorBox } from "../../ui/toast"
import { useKV } from "../../context/kv.tsx"
import { Editor } from "../../util/editor"
import { Global } from "@/global"
import fs from "fs/promises"
import stripAnsi from "strip-ansi"
import { LSP } from "@/lsp/index.ts"

addDefaultParsers(parsers.parsers)

// Tool color palette for swarm theme
const TOOL_COLORS = {
  bash: RGBA.fromInts(255, 68, 68, 255), // red (#ff4444)
  read: RGBA.fromInts(90, 255, 202, 255), // diffHighlightAdded
  write: RGBA.fromInts(255, 154, 74, 255), // darkOrange
  edit: RGBA.fromInts(255, 210, 74, 255), // darkYellow
  glob: RGBA.fromInts(184, 74, 255, 255), // darkPurple
  grep: RGBA.fromInts(255, 74, 107, 255), // darkRed
  list: RGBA.fromInts(74, 255, 255, 255), // darkCyan
  task: RGBA.fromInts(255, 210, 74, 255), // darkYellow
  webfetch: RGBA.fromInts(137, 180, 250, 255), // Catppuccin Blue (#89b4fa)
  todo: RGBA.fromInts(255, 90, 125, 255), // diffHighlightRemoved
  patch: RGBA.fromInts(184, 74, 255, 255), // darkPurple
}

// Detect git operations from bash commands
function detectGitOperation(command?: string): GitOpType | null {
  if (!command) return null
  const trimmed = command.trim()
  // Handle commands that might be chained with && or ;
  const parts = trimmed.split(/\s*(?:&&|;)\s*/)
  for (const part of parts) {
    const cmd = part.trim()
    if (cmd.startsWith("git commit")) return "commit"
    if (cmd.startsWith("git push")) return "push"
    if (cmd.startsWith("git add")) return "add"
    if (cmd.startsWith("git pull")) return "pull"
    if (cmd.startsWith("git ")) return "other"
  }
  return null
}

const context = createContext<{
  width: number
  conceal: () => boolean
  diffWrapMode: () => "word" | "none"
  sync: ReturnType<typeof useSync>
}>()

// Determine if a permission should be shown inline (in tool card) vs full modal
// Inline: small, simple permissions that don't need much space
// Modal: edits, writes, large content that needs review space
function shouldShowInlinePermission(permission: Permission.Info): boolean {
  // Subagent permissions always go to modal - they need stacking for multi-agent handling
  if (permission.metadata?.originSessionID) return false

  // Always show full modal for content-heavy types
  if (permission.type === "edit") return false
  if (permission.type === "write") return false
  if (permission.type === "ask-user") return false // Special interactive dialog
  if (permission.type === "exit-plan-mode") return false // Plan display needs space
  if (permission.type === "pin") return false // PIN entry needs dedicated dialog

  // For bash, check if command is short enough for inline
  if (permission.type === "bash") {
    const cmd = permission.metadata?.command as string
    // Short commands can be inline, long/complex ones need modal
    if (cmd && cmd.length < 100) return true
    return false
  }

  // These types are always small enough for inline
  if (permission.type === "external_directory") return true
  if (permission.type === "webfetch") return true
  if (permission.type === "network") return true // Sandbox network access

  // Default to modal for unknown types
  return false
}

function use() {
  const ctx = useContext(context)
  if (!ctx) throw new Error("useContext must be used within a Session component")
  return ctx
}

export function Session() {
  const route = useRouteData("session")
  const { navigate } = useRoute()
  const sync = useSync()
  const kv = useKV()
  const { theme } = useTheme()
  const session = createMemo(() => sync.session.get(route.sessionID)!)
  const messages = createMemo(() => sync.data.message[route.sessionID] ?? [])
  const permissions = createMemo(() => sync.data.permission[route.sessionID] ?? [])

  const pending = createMemo(() => {
    return messages().findLast((x) => x.role === "assistant" && !x.time?.completed)?.id
  })

  const dimensions = useTerminalDimensions()
  const [sidebar, setSidebar] = createSignal<"show" | "hide" | "auto">(kv.get("sidebar", "auto"))
  const [conceal, setConceal] = createSignal(true)
  const [diffWrapMode, setDiffWrapMode] = createSignal<"word" | "none">("word")

  const wide = createMemo(() => dimensions().width > 120)
  const sidebarVisible = createMemo(() => sidebar() === "show" || (sidebar() === "auto" && wide()))
  const contentWidth = createMemo(() => dimensions().width - (sidebarVisible() ? 42 : 0) - 4)
  const showHeader = createMemo(() => sync.data.config.tui?.show_header ?? true)

  createEffect(async () => {
    await sync.session
      .sync(route.sessionID)
      .then(() => {
        if (scroll) scroll.scrollBy(100_000)
      })
      .catch(() => {
        toast.show({
          message: `Session not found: ${route.sessionID}`,
          variant: "error",
        })
        return navigate({ type: "home" })
      })
  })

  const toast = useToast()

  const sdk = useSDK()

  let scroll: ScrollBoxRenderable
  let prompt: PromptRef
  const keybind = useKeybind()

  // Auto-manage permission modal based on pending permissions
  // Use a signal to track if WE are showing the permission dialog (not other dialogs like /commands)
  const [isShowingPermissionDialog, setIsShowingPermissionDialog] = createSignal(false)
  // Guard signal to prevent reopening dialog during close sequence
  const [isClosingPermissionDialog, setIsClosingPermissionDialog] = createSignal(false)

  // Filter permissions into those that need modal vs those shown inline
  const modalPermissions = createMemo(() => permissions().filter((p) => !shouldShowInlinePermission(p)))
  const inlinePermissions = createMemo(() => permissions().filter(shouldShowInlinePermission))

  // Collect tool parts that have pending inline permissions (for pinned section)
  const pinnedToolParts = createMemo(() => {
    const inline = inlinePermissions()
    if (inline.length === 0) return []

    const result: Array<{ part: ToolPart; permission: Permission.Info; message: AssistantMessage }> = []

    for (const perm of inline) {
      // Find the message containing this tool
      const msgs = messages()
      const message = msgs.find((m) => m.id === perm.messageID)
      if (!message || message.role !== "assistant") continue

      // Find the tool part with matching callID
      const parts = sync.data.part[message.id] ?? []
      const toolPart = parts.find((p): p is ToolPart => p.type === "tool" && p.callID === perm.callID)
      if (!toolPart) continue

      result.push({ part: toolPart, permission: perm, message: message as AssistantMessage })
    }

    return result
  })

  createEffect(() => {
    const modalPerms = modalPermissions()
    const hasModalPermissions = modalPerms.length > 0
    // Use our own signal instead of dialog.stack.length to prevent race conditions
    // when multiple permissions arrive in rapid succession
    const alreadyShowingDialog = isShowingPermissionDialog()
    const isClosing = isClosingPermissionDialog()

    // Show permission dialog only for modal-worthy permissions
    // Only create dialog if we haven't already shown one (prevents re-render on new permissions)
    // Also don't reopen if we're in the middle of closing (prevents infinite loop)
    if (hasModalPermissions && !alreadyShowingDialog && !isClosing) {
      setIsShowingPermissionDialog(true)
      toBottom()

      // onClose callback that marks closing state to prevent immediate reopen
      const handleClose = () => {
        setIsClosingPermissionDialog(true)
        setIsShowingPermissionDialog(false)
        // Reset closing flag after microtask to allow next permission to open
        queueMicrotask(() => setIsClosingPermissionDialog(false))
        if (prompt) prompt.focus()
      }

      // Check if this is a PIN permission - route to PIN dialog
      // Use getter function so the same component instance handles multiple sequential PINs
      const pinPerm = modalPerms.find((p) => p.type === "pin")
      if (pinPerm) {
        dialog.replace(
          () => (
            <DialogPin
              getPinPermission={() => modalPermissions().find((p) => p.type === "pin")}
              sessionID={route.sessionID}
            />
          ),
          handleClose,
          false, // Not fullscreen - PIN dialog is compact
        )
        return
      }

      // Check if this is an ask-user permission - route to special dialog
      const askUserPerm = modalPerms.find((p) => p.type === "ask-user")
      if (askUserPerm) {
        dialog.replace(() => <DialogAskUser permission={askUserPerm} sessionID={route.sessionID} />, handleClose, true)
      } else {
        dialog.replace(
          // Pass getter function so DialogPermission can reactively get updated permissions
          // without re-creating the component (preserves scroll position, selected index, etc.)
          () => <DialogPermission getPermissions={modalPermissions} sessionID={route.sessionID} />,
          handleClose,
          true, // fullScreen mode
        )
      }
    }
    // Clear permission dialog ONLY if we're the ones who showed it and modal permissions are now empty
    // Don't clear if already closing (prevents double-clear)
    else if (!hasModalPermissions && alreadyShowingDialog && !isClosing) {
      setIsClosingPermissionDialog(true)
      setIsShowingPermissionDialog(false)
      dialog.clear()
      // Reset closing flag after microtask
      queueMicrotask(() => setIsClosingPermissionDialog(false))
      // Return focus to chat prompt
      if (prompt) prompt.focus()
    }
  })

  // Force reset UI state - clears all overlays and resets permission dialog state
  function forceResetUIState() {
    setIsShowingPermissionDialog(false)
    dialog.forceClear()
    if (prompt) {
      prompt.focus()
    }
  }

  // Helper to respond to inline permissions (used by pinned section)
  function respondToInlinePermission(permissionID: string, response: Permission.Response) {
    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: {
          permissionID,
          id: route.sessionID,
        },
        body: {
          response: response as any,
        },
      })
      .catch((error) => {
        console.error("Failed to respond to inline permission:", error)
      })
  }

  // Register force-clear keyboard handler (Ctrl+Shift+Escape)
  useKeyboard((evt) => {
    if (evt.name === "escape" && evt.ctrl && evt.shift) {
      forceResetUIState()
      evt.preventDefault()
    }
  })

  function toBottom() {
    // Multiple attempts to ensure we scroll after content renders
    setTimeout(() => scroll.scrollTo(scroll.scrollHeight), 50)
    setTimeout(() => scroll.scrollTo(scroll.scrollHeight), 150)
    setTimeout(() => scroll.scrollTo(scroll.scrollHeight), 300)
  }

  // snap to bottom when revert position changes
  createEffect((old) => {
    if (old !== session()?.revert?.messageID) toBottom()
    return session()?.revert?.messageID
  })

  // Auto-scroll to bottom when Task tool summary first appears
  let lastScrolledToolId: string | null = null
  createEffect(() => {
    const msgs = messages()
    const lastMsg = msgs[msgs.length - 1]
    if (lastMsg?.role === "assistant") {
      const parts = sync.data.part[lastMsg.id] ?? []
      const taskTools = parts.filter((p: Part): p is ToolPart => p.type === "tool" && p.tool === "task")
      for (const tool of taskTools) {
        // Only scroll when a new task tool with summary appears (not on updates)
        if (tool.state.status === "running" && tool.state.metadata?.summary && tool.id !== lastScrolledToolId) {
          lastScrolledToolId = tool.id
          toBottom()
          break
        }
      }
    }
  })

  // Auto-scroll to bottom when last inline permission is accepted
  createEffect((prevCount: number) => {
    const count = inlinePermissions().length
    // Scroll when we had inline permissions and now have none
    if (prevCount > 0 && count === 0) {
      toBottom()
    }
    return count
  }, inlinePermissions().length)

  const local = useLocal()

  function moveChild(direction: number) {
    const parentID = session()?.parentID ?? session()?.id
    let children = sync.data.session
      .filter((x) => x.parentID === parentID || x.id === parentID)
      .toSorted((b, a) => a.id.localeCompare(b.id))
    if (children.length === 1) return
    let next = children.findIndex((x) => x.id === session()?.id) + direction
    if (next >= children.length) next = 0
    if (next < 0) next = children.length - 1
    if (children[next]) {
      navigate({
        type: "session",
        sessionID: children[next].id,
      })
    }
  }

  const command = useCommandDialog()
  command.register(() => [
    {
      title: "Rename session",
      value: "session.rename",
      keybind: "session_rename",
      category: "Session",
      onSelect: (dialog) => {
        dialog.replace(() => <DialogSessionRename session={route.sessionID} />)
      },
    },
    {
      title: "Jump to message",
      value: "session.timeline",
      keybind: "session_timeline",
      category: "Session",
      onSelect: (dialog) => {
        dialog.replace(() => (
          <DialogTimeline
            onMove={(messageID) => {
              const child = scroll.getChildren().find((child) => {
                return child.id === messageID
              })
              if (child) scroll.scrollBy(child.y - scroll.y - 1)
            }}
            sessionID={route.sessionID}
          />
        ))
      },
    },
    {
      title: "Compact session",
      value: "session.compact",
      keybind: "session_compact",
      category: "Session",
      onSelect: async (dialog) => {
        const model = local.model.current()
        if (!model?.providerID || !model?.modelID) {
          toast.show({ message: "No model selected", variant: "error" })
          dialog.clear()
          return
        }

        toast.show({ message: "Compacting session...", variant: "info", duration: 3000 })

        sdk.client.session
          .summarize({
            path: { id: route.sessionID },
            body: {
              modelID: model.modelID,
              providerID: model.providerID,
            },
          })
          .then(() => {
            toast.show({ message: "Session compacted!", variant: "success" })
          })
          .catch((err: any) => {
            toast.show({ message: `Compaction failed: ${err?.message ?? "Unknown error"}`, variant: "error" })
          })

        dialog.clear()
      },
    },
    ...(sync.data.config.share !== "disabled"
      ? [
          {
            title: "Share session",
            value: "session.share",
            keybind: "session_share" as const,
            disabled: !!session()?.share?.url,
            category: "Session",
            onSelect: async (dialog: any) => {
              await sdk.client.session
                .share({
                  path: {
                    id: route.sessionID,
                  },
                })
                .then((res) =>
                  Clipboard.copy(res.data!.share!.url).catch(() =>
                    toast.show({ message: "Failed to copy URL to clipboard", variant: "error" }),
                  ),
                )
                .then(() => toast.show({ message: "Share URL copied to clipboard!", variant: "success" }))
                .catch(() => toast.show({ message: "Failed to share session", variant: "error" }))
              dialog.clear()
            },
          },
        ]
      : []),
    {
      title: "Unshare session",
      value: "session.unshare",
      keybind: "session_unshare",
      disabled: !session()?.share?.url,
      category: "Session",
      onSelect: (dialog) => {
        sdk.client.session.unshare({
          path: {
            id: route.sessionID,
          },
        })
        dialog.clear()
      },
    },
    {
      title: "Undo previous message",
      value: "session.undo",
      keybind: "messages_undo",
      category: "Session",
      onSelect: (dialog) => {
        const revert = session().revert?.messageID
        const message = messages().findLast((x) => (!revert || x.id < revert) && x.role === "user")
        if (!message) return
        sdk.client.session
          .revert({
            path: {
              id: route.sessionID,
            },
            body: {
              messageID: message.id,
            },
          })
          .then(() => {
            toBottom()
          })
        const parts = sync.data.part[message.id]
        prompt.set(
          parts.reduce(
            (agg, part) => {
              if (part.type === "text") {
                if (!part.synthetic) agg.input += part.text
              }
              if (part.type === "file") agg.parts.push(part)
              return agg
            },
            { input: "", parts: [] as PromptInfo["parts"] },
          ),
        )
        dialog.clear()
      },
    },
    {
      title: "Redo",
      value: "session.redo",
      keybind: "messages_redo",
      disabled: !session()?.revert?.messageID,
      category: "Session",
      onSelect: (dialog) => {
        dialog.clear()
        const messageID = session().revert?.messageID
        if (!messageID) return
        const message = messages().find((x) => x.role === "user" && x.id > messageID)
        if (!message) {
          sdk.client.session.unrevert({
            path: {
              id: route.sessionID,
            },
          })
          prompt.set({ input: "", parts: [] })
          return
        }
        sdk.client.session.revert({
          path: {
            id: route.sessionID,
          },
          body: {
            messageID: message.id,
          },
        })
      },
    },
    {
      title: "View conversation history",
      value: "session.history",
      category: "Session",
      onSelect: (dialog) => {
        dialog.replace(() => <DialogHistory sessionID={route.sessionID} prompt={prompt} />)
      },
    },
    {
      title: sidebarVisible() ? "Hide sidebar" : "Show sidebar",
      value: "session.sidebar.toggle",
      keybind: "sidebar_toggle",
      category: "Session",
      onSelect: (dialog) => {
        setSidebar((prev) => {
          if (prev === "auto") return sidebarVisible() ? "hide" : "show"
          if (prev === "show") return "hide"
          return "show"
        })
        if (sidebar() === "show") kv.set("sidebar", "auto")
        if (sidebar() === "hide") kv.set("sidebar", "hide")
        dialog.clear()
      },
    },
    {
      title: "Toggle code concealment",
      value: "session.toggle.conceal",
      keybind: "messages_toggle_conceal" as any,
      category: "Session",
      onSelect: (dialog) => {
        setConceal((prev) => !prev)
        dialog.clear()
      },
    },
    {
      title: "Toggle diff wrapping",
      value: "session.toggle.diffwrap",
      category: "Session",
      onSelect: (dialog) => {
        setDiffWrapMode((prev) => (prev === "word" ? "none" : "word"))
        dialog.clear()
      },
    },
    {
      title: "Page up",
      value: "session.page.up",
      keybind: "messages_page_up",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollBy(-scroll.height / 2)
        dialog.clear()
      },
    },
    {
      title: "Page down",
      value: "session.page.down",
      keybind: "messages_page_down",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollBy(scroll.height / 2)
        dialog.clear()
      },
    },
    {
      title: "Half page up",
      value: "session.half.page.up",
      keybind: "messages_half_page_up",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollBy(-scroll.height / 4)
        dialog.clear()
      },
    },
    {
      title: "Half page down",
      value: "session.half.page.down",
      keybind: "messages_half_page_down",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollBy(scroll.height / 4)
        dialog.clear()
      },
    },
    {
      title: "First message",
      value: "session.first",
      keybind: "messages_first",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollTo(0)
        dialog.clear()
      },
    },
    {
      title: "Last message",
      value: "session.last",
      keybind: "messages_last",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        scroll.scrollTo(scroll.scrollHeight)
        dialog.clear()
      },
    },
    {
      title: "Copy last assistant message",
      value: "messages.copy",
      keybind: "messages_copy",
      category: "Session",
      onSelect: (dialog) => {
        const lastAssistantMessage = messages().findLast((msg) => msg.role === "assistant")
        if (!lastAssistantMessage) {
          toast.show({ message: "No assistant messages found", variant: "error" })
          dialog.clear()
          return
        }

        const parts = sync.data.part[lastAssistantMessage.id] ?? []
        const textParts = parts.filter((part) => part.type === "text")
        if (textParts.length === 0) {
          toast.show({ message: "No text parts found in last assistant message", variant: "error" })
          dialog.clear()
          return
        }

        const text = textParts
          .map((part) => part.text)
          .join("\n")
          .trim()
        if (!text) {
          toast.show({
            message: "No text content found in last assistant message",
            variant: "error",
          })
          dialog.clear()
          return
        }

        console.log(text)
        const base64 = Buffer.from(text).toString("base64")
        const osc52 = `\x1b]52;c;${base64}\x07`
        const finalOsc52 = process.env["TMUX"] ? `\x1bPtmux;\x1b${osc52}\x1b\\` : osc52
        /* @ts-expect-error */
        renderer.writeOut(finalOsc52)
        Clipboard.copy(text)
          .then(() => toast.show({ message: "Message copied to clipboard!", variant: "success" }))
          .catch(() => toast.show({ message: "Failed to copy to clipboard", variant: "error" }))
        dialog.clear()
      },
    },
    {
      title: "Next child session",
      value: "session.child.next",
      keybind: "session_child_cycle",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        moveChild(1)
        dialog.clear()
      },
    },
    {
      title: "Previous child session",
      value: "session.child.previous",
      keybind: "session_child_cycle_reverse",
      category: "Session",
      disabled: true,
      onSelect: (dialog) => {
        moveChild(-1)
        dialog.clear()
      },
    },
  ])

  const revert = createMemo(() => {
    const s = session()
    if (!s) return
    const messageID = s.revert?.messageID
    if (!messageID) return
    const reverted = messages().filter((x) => x.id >= messageID && x.role === "user")

    const diffFiles = (() => {
      const diffText = s.revert?.diff || ""
      if (!diffText) return []

      try {
        const patches = parsePatch(diffText)
        return patches.map((patch) => {
          const filename = patch.newFileName || patch.oldFileName || "unknown"
          const cleanFilename = filename.replace(/^[ab]\//, "")
          return {
            filename: cleanFilename,
            additions: patch.hunks.reduce(
              (sum, hunk) => sum + hunk.lines.filter((line) => line.startsWith("+")).length,
              0,
            ),
            deletions: patch.hunks.reduce(
              (sum, hunk) => sum + hunk.lines.filter((line) => line.startsWith("-")).length,
              0,
            ),
          }
        })
      } catch (error) {
        return []
      }
    })()

    return {
      messageID,
      reverted,
      diff: s.revert!.diff,
      diffFiles,
    }
  })

  const dialog = useDialog()
  const renderer = useRenderer()

  // snap to bottom when session changes
  createEffect(on(() => route.sessionID, toBottom))

  return (
    <context.Provider
      value={{
        get width() {
          return contentWidth()
        },
        conceal,
        diffWrapMode,
        sync,
      }}
    >
      <box flexDirection="row" paddingBottom={1} paddingTop={1} paddingLeft={2} paddingRight={2} gap={2}>
        <box flexGrow={1} gap={1}>
          <Show when={session()}>
            <Show when={session().parentID}>
              <box
                backgroundColor={theme.backgroundPanel}
                justifyContent="space-between"
                flexDirection="row"
                paddingTop={1}
                paddingBottom={1}
                flexShrink={0}
                paddingLeft={2}
                paddingRight={2}
              >
                <text fg={theme.text}>
                  Previous <span style={{ fg: theme.textMuted }}>{keybind.print("session_child_cycle_reverse")}</span>
                </text>
                <text fg={theme.text}>
                  <b>Viewing subagent session</b>
                </text>
                <text fg={theme.text}>
                  <span style={{ fg: theme.textMuted }}>{keybind.print("session_child_cycle")}</span> Next
                </text>
              </box>
            </Show>
            <Show when={!sidebarVisible() && showHeader()}>
              <Header />
            </Show>
            <scrollbox
              ref={(r) => (scroll = r)}
              scrollbarOptions={{
                paddingLeft: 2,
                visible: false,
                trackOptions: {
                  backgroundColor: theme.backgroundElement,
                  foregroundColor: theme.border,
                },
              }}
              stickyScroll={true}
              stickyStart="bottom"
              flexGrow={1}
            >
              <For each={messages()}>
                {(message, index) => (
                  <Switch>
                    <Match when={message.id === revert()?.messageID}>
                      {(function () {
                        const command = useCommandDialog()
                        const [hover, setHover] = createSignal(false)
                        const dialog = useDialog()

                        const handleUnrevert = async () => {
                          const confirmed = await DialogConfirm.show(
                            dialog,
                            "Confirm Redo",
                            "Are you sure you want to restore the reverted messages?",
                          )
                          if (confirmed) {
                            command.trigger("session.redo")
                          }
                        }

                        return (
                          <box
                            onMouseOver={() => setHover(true)}
                            onMouseOut={() => setHover(false)}
                            onMouseUp={handleUnrevert}
                            marginTop={1}
                            flexShrink={0}
                            border={["left"]}
                            customBorderChars={SplitBorder.customBorderChars}
                            borderColor={theme.backgroundPanel}
                          >
                            <box
                              paddingTop={1}
                              paddingBottom={1}
                              paddingLeft={2}
                              backgroundColor={hover() ? theme.backgroundElement : theme.backgroundPanel}
                            >
                              <text fg={theme.textMuted}>{revert()!.reverted.length} message reverted</text>
                              <text fg={theme.textMuted}>
                                <span style={{ fg: theme.text }}>{keybind.print("messages_redo")}</span> or /redo to
                                restore
                              </text>
                              <Show when={revert()!.diffFiles?.length}>
                                <box marginTop={1}>
                                  <For each={revert()!.diffFiles}>
                                    {(file) => (
                                      <text>
                                        {file.filename}
                                        <Show when={file.additions > 0}>
                                          <span style={{ fg: theme.diffAdded }}> +{file.additions}</span>
                                        </Show>
                                        <Show when={file.deletions > 0}>
                                          <span style={{ fg: theme.diffRemoved }}> -{file.deletions}</span>
                                        </Show>
                                      </text>
                                    )}
                                  </For>
                                </box>
                              </Show>
                            </box>
                          </box>
                        )
                      })()}
                    </Match>
                    <Match when={revert()?.messageID && message.id >= revert()!.messageID}>
                      <></>
                    </Match>
                    <Match when={message.role === "user"}>
                      <UserMessage
                        index={index()}
                        onMouseUp={() => {
                          if (renderer.getSelection()?.getSelectedText()) return
                          dialog.replace(() => (
                            <DialogMessage
                              messageID={message.id}
                              sessionID={route.sessionID}
                              setPrompt={(promptInfo) => prompt.set(promptInfo)}
                            />
                          ))
                        }}
                        message={message as UserMessage}
                        parts={sync.data.part[message.id] ?? []}
                        pending={pending()}
                      />
                    </Match>
                    <Match when={message.role === "assistant"}>
                      <AssistantMessage
                        last={index() === messages().length - 1}
                        message={message as AssistantMessage}
                        parts={sync.data.part[message.id] ?? []}
                      />
                    </Match>
                  </Switch>
                )}
              </For>
            </scrollbox>
            {/* Pinned tool cards with pending inline permissions - stay visible below scrollbox */}
            <Show when={pinnedToolParts().length > 0}>
              <box flexShrink={0} flexDirection="column" gap={1} paddingLeft={1} paddingRight={1} marginTop={1}>
                <For each={pinnedToolParts()}>
                  {(item, index) => (
                    <PinnedToolCard
                      part={item.part}
                      permission={item.permission}
                      message={item.message}
                      focused={index() === 0}
                      onRespond={(response) => respondToInlinePermission(item.permission.id, response)}
                    />
                  )}
                </For>
              </box>
            </Show>
            <CompactingCard sessionID={route.sessionID} />
            <box flexShrink={0}>
              <Prompt
                ref={(r) => (prompt = r)}
                disabled={permissions().length > 0}
                onSubmit={() => {
                  toBottom()
                }}
                sessionID={route.sessionID}
              />
            </box>
          </Show>
          <Toast />
        </box>
        <Show when={sidebarVisible()}>
          <Sidebar sessionID={route.sessionID} />
        </Show>
      </box>
    </context.Provider>
  )
}

const MIME_BADGE: Record<string, string> = {
  "text/plain": "txt",
  "image/png": "img",
  "image/jpeg": "img",
  "image/gif": "img",
  "image/webp": "img",
  "application/pdf": "pdf",
  "application/x-directory": "dir",
}

function UserMessage(props: {
  message: UserMessage
  parts: Part[]
  onMouseUp: () => void
  index: number
  pending?: string
}) {
  const text = createMemo(() => props.parts.flatMap((x) => (x.type === "text" && !x.synthetic ? [x] : []))[0])
  const files = createMemo(() => props.parts.flatMap((x) => (x.type === "file" ? [x] : [])))
  const sync = useSync()
  const { theme } = useTheme()
  const [hover, setHover] = createSignal(false)
  const queued = createMemo(() => props.pending && props.message.id > props.pending)
  const color = createMemo(() => (queued() ? theme.accent : theme.secondary))

  return (
    <Show when={text()}>
      <box
        id={props.message.id}
        onMouseOver={() => {
          setHover(true)
        }}
        onMouseOut={() => {
          setHover(false)
        }}
        onMouseUp={props.onMouseUp}
        paddingTop={1}
        paddingBottom={1}
        paddingLeft={0}
        marginTop={props.index === 0 ? 0 : 1}
        flexShrink={0}
      >
        <text fg={theme.text}>{text()?.text}</text>
        <Show when={files().length}>
          <box flexDirection="row" paddingBottom={1} paddingTop={1} gap={1} flexWrap="wrap">
            <For each={files()}>
              {(file) => {
                const bg = createMemo(() => {
                  if (file.mime.startsWith("image/")) return theme.accent
                  if (file.mime === "application/pdf") return theme.primary
                  return theme.secondary
                })
                return (
                  <text fg={theme.text}>
                    <span style={{ bg: bg(), fg: theme.background }}> {MIME_BADGE[file.mime] ?? file.mime} </span>
                    <span style={{ bg: theme.backgroundElement, fg: theme.textMuted }}> {file.filename} </span>
                  </text>
                )
              }}
            </For>
          </box>
        </Show>
        <text fg={theme.text}>
          {sync.data.config.username ?? "You"}{" "}
          <Show
            when={queued()}
            fallback={<span style={{ fg: theme.textMuted }}>({Locale.time(props.message.time.created)})</span>}
          >
            <span style={{ bg: theme.accent, fg: theme.backgroundPanel, bold: true }}> QUEUED </span>
          </Show>
        </text>
      </box>
    </Show>
  )
}

type GroupedPart = { type: "single"; part: Part; index: number } | { type: "tasks-grid"; tasks: ToolPart[] }

function TaskAgentsGrid(props: { message: AssistantMessage }) {
  const { theme } = useTheme()
  const ctx = use()
  const sync = useSync()

  // Reactively extract all task tools from the message parts
  // This ensures the grid updates when new agents are added in batches
  const allTaskTools = createMemo(() => {
    const parts = sync.data.part[props.message.id] ?? []
    return parts.filter((p): p is ToolPart => p.type === "tool" && p.tool === "task")
  })

  // Cap at 8 agents
  const tasks = createMemo(() => allTaskTools().slice(0, 8))

  const truncate = (str: string | undefined | null, maxLen: number) => {
    if (!str) return ""
    return str.length > maxLen ? str.slice(0, maxLen) + "..." : str
  }

  // Agent-specific colors for subagent types
  const getAgentColor = (agentType: string): RGBA => {
    const agent = sync.data.agent.find((a) => a.name === agentType)
    if (agent?.color) {
      const hex = agent.color.replace(/^#/, "")
      const r = parseInt(hex.substring(0, 2), 16)
      const g = parseInt(hex.substring(2, 4), 16)
      const b = parseInt(hex.substring(4, 6), 16)
      return RGBA.fromInts(r, g, b, 255)
    }
    // Fallback colors by agent type
    const colorMap: Record<string, RGBA> = {
      explore: theme.success,
      deepbugfixer: theme.warning,
      strategist: theme.info,
      "git-committer": theme.secondary,
      general: theme.error,
    }
    return colorMap[agentType] ?? theme.primary
  }

  // 2-row layout with rounded borders
  // Row 1: [status] agent_name - title
  // Row 2: [tool_animation] tool: input | ✓N ●N ✗N | elapsed
  return (
    <box marginTop={1} overflow="hidden" flexShrink={0} flexGrow={0} flexDirection="column" gap={0}>
      <For each={tasks()}>
        {(task) => {
          const isRunning = createMemo(() => task.state.status === "running")
          const isCompleted = createMemo(() => task.state.status === "completed")
          const isPending = createMemo(() => task.state.status === "pending")

          const allTools = createMemo(() => {
            return task.state.status !== "pending" && task.state.metadata?.summary
              ? (task.state.metadata.summary as any[])
              : ([] as any[])
          })

          const toolsByStatus = createMemo(() => {
            const summary = allTools()
            return {
              running: summary.filter((t: any) => t.status === "running"),
              completed: summary.filter((t: any) => t.status === "completed"),
              error: summary.filter((t: any) => t.status === "error"),
              pending: summary.filter((t: any) => t.status === "pending"),
            }
          })

          // Get the currently active (running) tool or last completed
          const activeTool = createMemo(() => {
            const tools = allTools()
            const running = tools.find((t: any) => t.status === "running")
            if (running) return running
            // Fall back to last tool
            return tools[tools.length - 1]
          })

          const agentType = createMemo(() => task.state.input?.subagent_type as string)
          const agentColor = createMemo(() => getAgentColor(agentType()))
          const agentName = createMemo(() => agentType() ?? "")
          const taskTitle = createMemo(() => task.state.input?.description as string)

          // Compute border color: agent color when running, status-modified when done
          const borderColor = createMemo(() => {
            if (task.state.status === "error") return theme.error
            if (isCompleted()) return agentColor()
            return agentColor()
          })

          return (
            <box
              height={4}
              maxHeight={4}
              minHeight={4}
              flexShrink={0}
              flexGrow={0}
              overflow="hidden"
              border={["left", "right", "top", "bottom"]}
              customBorderChars={RoundedBorder.customBorderChars}
              borderColor={borderColor()}
              paddingLeft={1}
              paddingRight={1}
              flexDirection="column"
            >
              {/* Row 1: Status + Agent Name - Title */}
              <box flexDirection="row" alignItems="center" gap={1}>
                <text flexShrink={0}>
                  <Show when={isPending()}>
                    <GenericSpinner spinner="braille_fade" color={agentColor()} />
                  </Show>
                  <Show when={isRunning() && !isPending()}>
                    <GenericSpinner spinner="braille_fade" color={agentColor()} />
                  </Show>
                  <Show when={isCompleted()}>
                    <span style={{ fg: theme.primary, bold: true }}>✓</span>
                  </Show>
                  <Show when={task.state.status === "error"}>
                    <span style={{ fg: theme.error, bold: true }}>✗</span>
                  </Show>{" "}
                  <span style={{ fg: agentColor(), bold: true }}>{agentName()}</span>
                </text>

                {/* Title - use remaining width after agent name, borders, padding */}
                <Show when={taskTitle()}>
                  <text flexShrink={1} flexGrow={1} overflow="hidden" fg={theme.text}>
                    - {truncate(taskTitle(), Math.max(20, ctx.width - agentName().length - 10))}
                  </text>
                </Show>
              </box>

              {/* Row 2: Tool info (left) | Stats + Time (right) */}
              <box flexDirection="row" alignItems="center" gap={1} paddingLeft={2}>
                {/* Pending state */}
                <Show when={isPending()}>
                  <text flexShrink={1} flexGrow={1} overflow="hidden" fg={theme.textMuted}>
                    <GenericSpinner spinner="task_spinner" color={agentColor()} />
                    <span style={{ fg: theme.textMuted }}> Initializing...</span>
                  </text>
                </Show>

                {/* Active tool info (left-aligned, grows to fill) */}
                <Show when={!isPending() && activeTool()}>
                  <text flexShrink={1} flexGrow={1} overflow="hidden">
                    <Show
                      when={activeTool()?.status === "running"}
                      fallback={
                        <span
                          style={{
                            fg: activeTool()?.status === "completed" ? theme.primary : theme.error,
                          }}
                        >
                          {activeTool()?.status === "completed" ? "✓" : "✗"}
                        </span>
                      }
                    >
                      <GenericSpinner spinner={getToolSpinner(activeTool()?.tool ?? "read")} color={theme.accent} />
                    </Show>{" "}
                    <span style={{ fg: theme.text }}>{activeTool()?.tool}</span>
                    <Show when={activeTool()?.input}>
                      <span style={{ fg: theme.textMuted }}>
                        :{" "}
                        {truncate(
                          typeof activeTool()?.input === "string" ? activeTool()?.input : String(activeTool()?.input),
                          Math.max(20, ctx.width - (activeTool()?.tool?.length ?? 0) - 30),
                        )}
                      </span>
                    </Show>
                  </text>
                </Show>

                {/* Stats (right-aligned, fixed width) */}
                <text flexShrink={0} fg={theme.textMuted}>
                  <Show when={toolsByStatus().completed.length > 0}>
                    <span style={{ fg: theme.primary }}>✓{toolsByStatus().completed.length}</span>
                  </Show>
                  <Show when={toolsByStatus().running.length > 0}>
                    {toolsByStatus().completed.length > 0 ? " " : ""}
                    <span style={{ fg: theme.accent }}>●{toolsByStatus().running.length}</span>
                  </Show>
                  <Show when={toolsByStatus().error.length > 0}>
                    {" "}
                    <span style={{ fg: theme.error }}>✗{toolsByStatus().error.length}</span>
                  </Show>
                </text>

                {/* Elapsed time (right-aligned, fixed width) */}
                <Show
                  when={
                    isRunning() && !isPending() && task.state.status !== "pending" && (task.state as any).time?.start
                  }
                >
                  <text flexShrink={0} fg={theme.accent}>
                    | <ElapsedTime startTime={(task.state as any).time!.start} />
                  </text>
                </Show>
              </box>
            </box>
          )
        }}
      </For>
    </box>
  )
}

function AssistantMessage(props: { message: AssistantMessage; parts: Part[]; last: boolean }) {
  const local = useLocal()
  const { theme } = useTheme()

  // Group parts - task tools go into grid, everything else renders individually
  const groupedParts = createMemo((): GroupedPart[] => {
    const result: GroupedPart[] = []
    const allTaskTools: ToolPart[] = []

    for (let i = 0; i < props.parts.length; i++) {
      const part = props.parts[i]

      // Skip step markers
      if (part.type === "step-start" || part.type === "step-finish") {
        continue
      }

      // Collect task tools for grid rendering
      if (part.type === "tool" && part.tool === "task") {
        allTaskTools.push(part as ToolPart)
        continue
      }

      // Everything else renders individually
      result.push({ type: "single", part, index: i })
    }

    // Add all task tools as a single grid at the end
    if (allTaskTools.length > 0) {
      result.push({ type: "tasks-grid", tasks: allTaskTools })
    }

    return result
  })

  return (
    <>
      <For each={groupedParts()}>
        {(group) => (
          <Switch>
            <Match when={group.type === "tasks-grid" && group}>
              {(g) => <TaskAgentsGrid message={props.message} />}
            </Match>
            <Match when={group.type === "single" && group}>
              {(g) => {
                const singleGroup = g() as { type: "single"; part: Part; index: number }
                const component = createMemo(() => PART_MAPPING[singleGroup.part.type as keyof typeof PART_MAPPING])
                return (
                  <Show when={component()}>
                    <Dynamic
                      last={singleGroup.index === props.parts.length - 1}
                      component={component()}
                      part={singleGroup.part as any}
                      message={props.message}
                    />
                  </Show>
                )
              }}
            </Match>
          </Switch>
        )}
      </For>
      <Show when={props.message.error}>
        {(error) => <ErrorBox message={String(error().data.message || "An error occurred")} />}
      </Show>
      <Show when={!props.message.time.completed}>
        <box paddingLeft={0} marginTop={1}>
          <AgentStreamingWave color={local.agent.color(props.message.mode)} thinking={local.thinking.enabled} />
        </box>
      </Show>
    </>
  )
}

const PART_MAPPING = {
  text: TextPart,
  tool: ToolPart,
  reasoning: ReasoningPart,
}

function ReasoningPart(props: { last: boolean; part: ReasoningPart; message: AssistantMessage }) {
  const { theme, syntax } = useTheme()
  const ctx = use()
  const content = createMemo(() => props.part.text.trim())
  const isStreaming = createMemo(() => !props.part.time?.end)

  return (
    <Show when={content()}>
      <box id={"text-" + props.part.id} paddingLeft={2} marginTop={1} flexDirection="column">
        <text paddingLeft={1} marginBottom={1}>
          <ThinkingSpinner state={isStreaming() ? "streaming" : "resolved"} />{" "}
          <span style={{ fg: RGBA.fromHex("#b48cff") }}>Thinking</span>
        </text>
        <code
          filetype="markdown"
          drawUnstyledText={false}
          streaming={true}
          syntaxStyle={syntax()}
          content={content()}
          conceal={ctx.conceal()}
          fg={theme.text}
        />
      </box>
    </Show>
  )
}

function TextPart(props: { last: boolean; part: TextPart; message: AssistantMessage }) {
  const ctx = use()
  const local = useLocal()
  const { syntax, theme } = useTheme()
  const isComplete = () => props.part.time?.end !== undefined
  const isThinking = () => local.thinking.enabled
  const color = () => local.agent.color(props.message.mode)
  return (
    <Show when={props.part.text.trim()}>
      <box id={"text-" + props.part.id} flexDirection="row" marginTop={1} flexShrink={0}>
        <box width={3} flexShrink={0} alignItems="center">
          <text>
            <Show
              when={!isComplete()}
              fallback={
                <Show when={isThinking()} fallback={<RoundedSquareIdle color={color()} />}>
                  <HexagonOutlineIdle color={color()} />
                </Show>
              }
            >
              <Show when={isThinking()} fallback={<RoundedSquareStreaming color={color()} />}>
                <HexagonOutlineStreaming color={color()} />
              </Show>
            </Show>
          </text>
        </box>
        <box flexGrow={1}>
          <code
            filetype="markdown"
            drawUnstyledText={false}
            streaming={true}
            syntaxStyle={syntax()}
            content={props.part.text.trim()}
            conceal={ctx.conceal()}
          />
        </box>
      </box>
    </Show>
  )
}

// Pending messages moved to individual tool pending functions

// Pinned tool card for inline permissions - rendered outside the scrollbox
function PinnedToolCard(props: {
  part: ToolPart
  permission: Permission.Info
  message: AssistantMessage
  focused: boolean
  onRespond: (response: Permission.Response) => void
}) {
  const { theme } = useTheme()

  // Get the tool renderer
  const render = ToolRegistry.render(props.part.tool) ?? GenericTool
  const input = props.part.state.status !== "pending" ? props.part.state.input : {}
  const metadata = props.part.state.status !== "pending" ? props.part.state.metadata : {}

  return (
    <box border={["left"]} borderColor={theme.warning} paddingLeft={2} flexDirection="column" gap={1}>
      {/* Tool content */}
      <Dynamic
        component={render}
        input={input ?? {}}
        tool={props.part.tool}
        metadata={metadata ?? {}}
        permission={props.permission.metadata ?? {}}
        output={undefined}
        state={props.part.state}
      />

      {/* Inline permission UI */}
      <InlinePermission permission={props.permission} focused={props.focused} onRespond={props.onRespond} />
    </box>
  )
}

function ToolPart(props: { last: boolean; part: ToolPart; message: AssistantMessage }) {
  const { theme } = useTheme()
  const sync = useSync()
  const sdk = useSDK()
  const [margin, setMargin] = createSignal(0)

  // Helper to respond to inline permissions
  function respondToInlinePermission(permissionID: string, sessionID: string, response: Permission.Response) {
    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: {
          permissionID,
          id: sessionID,
        },
        body: {
          response: response as any,
        },
      })
      .catch((error) => {
        console.error("Failed to respond to inline permission:", error)
      })
  }

  const component = createMemo(() => {
    const render = ToolRegistry.render(props.part.tool) ?? GenericTool

    const metadata = props.part.state.status === "pending" ? {} : (props.part.state.metadata ?? {})
    const input = props.part.state.input ?? {}
    const container = ToolRegistry.container(props.part.tool)
    const permissions = sync.data.permission[props.message.sessionID] ?? []
    const permissionIndex = permissions.findIndex((x) => x.callID === props.part.callID)
    const permission = permissions[permissionIndex]

    // Check if this permission should be shown inline vs modal
    const isInline = permission && shouldShowInlinePermission(permission)
    const isModal = permission && !isInline

    // Skip rendering in timeline if this tool has a pending inline permission
    // (It will be rendered in the pinned section instead)
    if (isInline) {
      return null
    }

    const style: BoxProps =
      container === "block" || permission
        ? {
            marginTop: 1,
            gap: 1,
            // ToolCard now handles borders, padding, and colors
            // Only apply heavy border styling for modal permissions (inline has its own styling)
            ...(isModal && permissionIndex === 0
              ? {
                  // Keep border for modal permission dialogs
                  border: ["left", "right"] as const,
                  paddingTop: 1,
                  paddingBottom: 1,
                  paddingLeft: 2,
                  backgroundColor: theme.backgroundPanel,
                  customBorderChars: SplitBorder.customBorderChars,
                  borderColor: theme.warning,
                }
              : {}),
            // Lighter styling for inline permissions
            ...(isInline
              ? {
                  border: ["left"] as const,
                  paddingLeft: 2,
                  borderColor: theme.warning,
                }
              : {}),
          }
        : {
            // Inline tools - no padding since ToolCard handles it
          }

    return (
      <box
        marginTop={margin()}
        {...style}
        renderBefore={function () {
          const el = this as BoxRenderable
          const parent = el.parent
          if (!parent) {
            return
          }
          if (el.height > 1) {
            setMargin(1)
            return
          }
          const children = parent.getChildren()
          const index = children.indexOf(el)
          const previous = children[index - 1]
          if (!previous) {
            setMargin(0)
            return
          }
          if (previous.height > 1 || previous.id.startsWith("text-")) {
            setMargin(1)
            return
          }
          // Consecutive inline tools should stack tightly
          setMargin(0)
        }}
      >
        <Dynamic
          component={render}
          input={input}
          tool={props.part.tool}
          metadata={metadata}
          permission={permission?.metadata ?? {}}
          output={props.part.state.status === "completed" ? props.part.state.output : undefined}
          state={props.part.state}
        />
        {props.part.state.status === "error" && (
          <box paddingLeft={2}>
            <text fg={theme.error}>{props.part.state.error.replace("Error: ", "")}</text>
          </box>
        )}
        {/* Inline permission UI - shown directly in tool card */}
        {isInline && (
          <InlinePermission
            permission={permission}
            focused={permissionIndex === 0}
            onRespond={(response) => respondToInlinePermission(permission.id, props.message.sessionID, response)}
          />
        )}
        {/* Modal permission indicator - full dialog handles the interaction */}
        {isModal && (
          <box gap={1}>
            <text fg={theme.warning}>⚠ Permission required - see dialog</text>
          </box>
        )}
      </box>
    )
  })

  return <Show when={component()}>{component()}</Show>
}

type ToolProps<T extends Tool.Info> = {
  input: Partial<Tool.InferParameters<T>>
  metadata: Partial<Tool.InferMetadata<T>>
  permission: Record<string, any>
  tool: string
  output?: string
  state: ToolPart["state"]
}
function GenericTool(props: ToolProps<any>) {
  return (
    <ToolTitle icon="⚙" fallback="" when={true}>
      {props.tool} {input(props.input)}
    </ToolTitle>
  )
}

type ToolRegistration<T extends Tool.Info = any> = {
  name: string
  container: "inline" | "block"
  render?: Component<ToolProps<T>>
}
const ToolRegistry = (() => {
  const state: Record<string, ToolRegistration> = {}
  function register<T extends Tool.Info>(input: ToolRegistration<T>) {
    state[input.name] = input
    return input
  }
  return {
    register,
    container(name: string) {
      return state[name]?.container
    },
    render(name: string) {
      return state[name]?.render
    },
  }
})()

// Label colors that complement spinner colors
const TOOL_LABEL_COLORS = {
  write: RGBA.fromHex("#fab387"), // Peach - complements orange spinner
  edit: RGBA.fromHex("#cba6f7"), // Purple - matches EditDeltaMorphChain spinner
}

function ToolTitle(props: {
  fallback: string | JSX.Element
  when: any
  icon: string
  status?: "pending" | "running" | "completed" | "error"
  color?: RGBA
  spinner?: string // Spinner name from spinner-definitions.ts
  customSpinner?: JSX.Element // Custom spinner component (for reactive animations)
  runningLabel?: string // Label shown when pending/running (e.g. "Editing", "Writing")
  labelColor?: RGBA // Static color for the runningLabel
  children: JSX.Element
}) {
  const { theme } = useTheme()

  // Determine which spinner component to use
  const SpinnerComponent = () => {
    // Use custom spinner if provided
    if (props.customSpinner) {
      return props.customSpinner
    }
    if (props.spinner) {
      const spinnerDef = typeof props.spinner === "string" ? getSpinner(props.spinner) : props.spinner

      // Use RainbowSpinner for rgb mode spinners, GenericSpinner for others
      return spinnerDef.mode === "rgb" ? (
        <RainbowSpinner spinner={props.spinner} />
      ) : (
        <GenericSpinner spinner={props.spinner} color={props.color} />
      )
    }
    // Fallback to PulsingDot if no spinner specified
    return <PulsingDot color={props.color} />
  }

  const isActive = () => props.status === "pending" || props.status === "running"

  return (
    <text paddingLeft={3} fg={props.when ? theme.textMuted : theme.text}>
      <Show
        fallback={
          <>
            {SpinnerComponent()} <span style={{ fg: props.labelColor ?? props.color }}>{props.fallback}</span>
          </>
        }
        when={props.when}
      >
        <Show when={isActive()} fallback={<span style={{ bold: true }}>{props.icon}</span>}>
          {SpinnerComponent()}
        </Show>{" "}
        {props.children}
        <Show when={isActive() && props.runningLabel}>
          {" "}
          <span style={{ fg: props.labelColor ?? props.color }}>{props.runningLabel}</span>
        </Show>
      </Show>
    </text>
  )
}

ToolRegistry.register<typeof BashTool>({
  name: "bash",
  container: "block",
  render(props) {
    const toast = useToast()
    const command = () => props.input.command as string | undefined

    // Detect if this is a git operation
    const gitOp = createMemo(() => detectGitOperation(command()))

    // Streaming output during execution
    const streamingOutput = createMemo(() => Bun.stripANSI(props.metadata.output?.trim() ?? ""))
    // Final output when completed
    const finalOutput = createMemo(() => (props.output ? Bun.stripANSI(props.output.trim()) : undefined))

    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    const startTime = createMemo(() => {
      if (props.state.status !== "pending" && props.state.time?.start) {
        return props.state.time.start
      }
      return undefined
    })

    // Get exit code from metadata if available
    const exitCode = createMemo(() => props.metadata.exit as number | undefined)

    // Fire toast notifications for git commit/push completion
    // Use on() with defer to only trigger when status CHANGES to completed/error
    createEffect(
      on(
        () => props.state.status,
        (status, prevStatus) => {
          // Only fire when transitioning TO completed/error, not on initial render
          if (prevStatus === status) return
          if (status !== "completed" && status !== "error") return

          const op = gitOp()
          if (!op) return

          const exit = exitCode()
          const success = status === "completed" && (exit === 0 || exit === undefined)

          if (op === "commit") {
            if (success) {
              toast.show({
                message: "Changes committed",
                variant: "success",
                title: " Git Commit",
                duration: 3000,
              })
            } else {
              toast.show({
                message: "Commit failed",
                variant: "error",
                title: " Git",
                duration: 4000,
              })
            }
          } else if (op === "push") {
            if (success) {
              toast.show({
                message: "Pushed to remote",
                variant: "success",
                title: " Git Push",
                duration: 3000,
              })
            } else {
              toast.show({
                message: "Push failed",
                variant: "error",
                title: " Git",
                duration: 4000,
              })
            }
          }
        },
        { defer: true },
      ),
    )

    return (
      <ToolCard status={props.state.status} inline={false}>
        <Show
          when={gitOp()}
          fallback={
            <BashToolAnimation
              status={props.state.status}
              command={command()}
              description={props.input.description as string | undefined}
              output={finalOutput()}
              streamingOutput={streamingOutput()}
              startTime={startTime()}
              executionTime={executionTime()}
              exitCode={exitCode()}
              color={TOOL_COLORS.bash}
            />
          }
        >
          <GitToolAnimation
            status={props.state.status}
            command={command()}
            description={props.input.description as string | undefined}
            output={finalOutput()}
            streamingOutput={streamingOutput()}
            startTime={startTime()}
            executionTime={executionTime()}
            exitCode={exitCode()}
            gitOp={gitOp()!}
          />
        </Show>
      </ToolCard>
    )
  },
})

ToolRegistry.register<typeof ReadTool>({
  name: "read",
  container: "inline",
  render(props) {
    const filePath = createMemo(() => (props.input.filePath ? normalizePath(props.input.filePath) : undefined))
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })
    const startTime = createMemo(() => {
      if (props.state.status === "pending") return undefined
      return props.state.time?.start
    })
    const lineCount = createMemo(() => {
      if (!props.output) return undefined
      // Try to extract line count from output
      const output = props.output as string
      if (output) {
        const lines = output.split("\n")
        return lines.length
      }
      return undefined
    })

    return (
      <ToolCard status={props.state.status} inline={true}>
        <ReadToolAnimation
          status={props.state.status}
          filePath={filePath()}
          startTime={startTime()}
          executionTime={executionTime()}
          lineCount={lineCount()}
        />
      </ToolCard>
    )
  },
})

ToolRegistry.register<typeof WriteTool>({
  name: "write",
  container: "block",
  render(props) {
    const { theme, syntax } = useTheme()
    const lines = createMemo(
      () => (typeof props.input.content === "string" ? props.input.content.split("\n") : []),
      [] as string[],
    )
    const code = createMemo(() => {
      if (!props.input.content) return ""
      const text = props.input.content
      return text
    })

    const numbers = createMemo(() => {
      const pad = lines().length.toString().length
      return lines()
        .map((_, index) => index + 1)
        .map((x) => x.toString().padStart(pad, " "))
    })

    const isRunning = createMemo(() => !props.output && props.input.filePath)
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })
    const diagnostics = createMemo(() => props.metadata.diagnostics?.[props.input.filePath ?? ""] ?? [])

    return (
      <ToolCard status={props.state.status} inline={false}>
        <box gap={1}>
          <ToolTitle
            icon="←"
            fallback="Writing"
            when={props.input.filePath}
            status={props.state.status}
            color={TOOL_COLORS.write}
            spinner="write_hexagon_slide"
            runningLabel="Writing"
            labelColor={TOOL_LABEL_COLORS.write}
          >
            <Show when={isRunning()} fallback={<>Wrote {props.input.filePath}</>}>
              {props.input.filePath}
            </Show>{" "}
            <Show when={isRunning() && props.state.status !== "pending" && props.state.time?.start}>
              <ElapsedTime startTime={props.state.status !== "pending" ? props.state.time!.start : 0} />
            </Show>
            <Show when={props.output}>
              <SuccessCheckmark />
            </Show>
          </ToolTitle>
          <box flexDirection="row">
            <box flexShrink={0}>
              <For each={numbers()}>{(value) => <text style={{ fg: theme.textMuted }}>{value}</text>}</For>
            </box>
            <box paddingLeft={1} flexGrow={1}>
              <code filetype={filetype(props.input.filePath!)} syntaxStyle={syntax()} content={code()} />
            </box>
          </box>
          <Show when={props.output}>
            <ToolMetadata executionTime={executionTime()} metadata={props.metadata} input={props.input} />
          </Show>
          <Show when={diagnostics().length}>
            <For each={diagnostics()}>
              {(diagnostic) => (
                <text fg={theme.error}>
                  Error [{diagnostic.range.start.line}:{diagnostic.range.start.character}]: {diagnostic.message}
                </text>
              )}
            </For>
          </Show>
        </box>
      </ToolCard>
    )
  },
})

ToolRegistry.register<typeof GlobTool>({
  name: "glob",
  container: "inline",
  render(props) {
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    return (
      <GlobToolAnimation
        status={props.state.status}
        pattern={props.input.pattern}
        path={props.input.path ? normalizePath(props.input.path) : undefined}
        startTime={props.state.status !== "pending" && props.state.time?.start ? props.state.time.start : undefined}
        executionTime={executionTime()}
        matchCount={props.metadata.count}
      />
    )
  },
})

ToolRegistry.register<typeof GrepTool>({
  name: "grep",
  container: "inline",
  render(props) {
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    return (
      <GrepToolAnimation
        status={props.state.status}
        pattern={props.input.pattern}
        path={props.input.path ? normalizePath(props.input.path) : undefined}
        include={props.input.include}
        startTime={props.state.status !== "pending" && props.state.time?.start ? props.state.time.start : undefined}
        executionTime={executionTime()}
        matchCount={props.metadata.matches}
      />
    )
  },
})

ToolRegistry.register<typeof ListTool>({
  name: "list",
  container: "inline",
  render(props) {
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    return (
      <ListToolAnimation
        status={props.state.status}
        path={props.input.path ? normalizePath(props.input.path) : undefined}
        startTime={props.state.status !== "pending" && props.state.time?.start ? props.state.time.start : undefined}
        executionTime={executionTime()}
        itemCount={props.metadata.count}
      />
    )
  },
})

// Individual Task Agent Card Component
ToolRegistry.register<typeof TaskTool>({
  name: "task",
  container: "block",
  render(props) {
    const { theme } = useTheme()
    const keybind = useKeybind()
    const isRunning = createMemo(() => props.state.status === "running")
    const isCompleted = createMemo(() => props.state.status === "completed")

    // Get current running tool
    const currentTool = createMemo(() => {
      const summary = props.metadata.summary ?? []
      return summary.find((t: any) => t.status === "running")
    })

    // Get tools by status
    const toolsByStatus = createMemo(() => {
      const summary = props.metadata.summary ?? []
      return {
        running: summary.filter((t: any) => t.status === "running"),
        completed: summary.filter((t: any) => t.status === "completed"),
        error: summary.filter((t: any) => t.status === "error"),
        pending: summary.filter((t: any) => t.status === "pending"),
      }
    })

    const statusColor = createMemo(() => {
      if (props.state.status === "error") return theme.error
      if (isRunning()) return theme.accent
      if (isCompleted()) return theme.primary
      return theme.border
    })

    const truncate = (str: string | undefined | null, maxLen: number) => {
      if (!str) return ""
      return str.length > maxLen ? str.slice(0, maxLen) + "..." : str
    }

    return (
      <box
        marginTop={1}
        border={["left", "right", "top", "bottom"]}
        borderColor={statusColor()}
        paddingTop={1}
        paddingBottom={1}
        paddingLeft={2}
        paddingRight={2}
        gap={1}
      >
        {/* Agent Header - show pending state differently */}
        <Show
          when={props.state.status === "pending"}
          fallback={
            <>
              <text>
                <Show
                  when={isRunning()}
                  fallback={
                    <span style={{ fg: isCompleted() ? theme.primary : theme.error, bold: true }}>
                      {isCompleted() ? "✓" : "✗"}
                    </span>
                  }
                >
                  <GenericSpinner spinner="task_spinner" color={theme.accent} />
                </Show>{" "}
                <span style={{ fg: theme.primary, bold: true }}>{props.input.subagent_type}</span>
                <Show
                  when={
                    isRunning() && props.state.status !== "pending" && "time" in props.state && props.state.time?.start
                  }
                >
                  {" "}
                  <ElapsedTime
                    startTime={props.state.status !== "pending" && "time" in props.state ? props.state.time!.start : 0}
                  />
                </Show>
              </text>

              {/* Task Description */}
              <text fg={theme.text} paddingLeft={2}>
                {props.input.description}
              </text>
            </>
          }
        >
          {/* Pending state - single clean animation */}
          <text paddingLeft={2}>
            <GenericSpinner spinner="task_spinner" color={theme.accent} />{" "}
            <span style={{ fg: theme.accent, bold: true }}>Prompting agent...</span>
          </text>
        </Show>

        {/* Tools Activity List */}
        <Show when={props.metadata.summary?.length}>
          <box paddingLeft={2} gap={0}>
            {/* Running tools - show with pulsing indicator */}
            <For each={toolsByStatus().running}>
              {(tool: any) => (
                <text>
                  <span style={{ fg: theme.accent }}>
                    <GenericSpinner spinner="task_spinner" color={theme.accent} />
                  </span>{" "}
                  <span style={{ fg: theme.primary, bold: true }}>{tool.tool}</span>
                  <Show when={tool.input}>
                    <span style={{ fg: theme.text }}>: {truncate(tool.input, 55)}</span>
                  </Show>
                </text>
              )}
            </For>

            {/* Completed tools - show with checkmark and result */}
            <For each={toolsByStatus().completed}>
              {(tool: any) => (
                <text>
                  <span style={{ fg: theme.primary }}>✓</span> <span style={{ fg: theme.textMuted }}>{tool.tool}</span>
                  <Show when={tool.input}>
                    <span style={{ fg: theme.textMuted }}>: {truncate(tool.input, 40)}</span>
                  </Show>
                  <Show when={tool.output}>
                    <span style={{ fg: theme.primary }}> → {truncate(tool.output, 45)}</span>
                  </Show>
                </text>
              )}
            </For>

            {/* Error tools - show with X and error message */}
            <For each={toolsByStatus().error}>
              {(tool: any) => (
                <text>
                  <span style={{ fg: theme.error }}>✗</span> <span style={{ fg: theme.textMuted }}>{tool.tool}</span>
                  <Show when={tool.input}>
                    <span style={{ fg: theme.textMuted }}>: {truncate(tool.input, 40)}</span>
                  </Show>
                  <Show when={tool.output}>
                    <span style={{ fg: theme.error }}> → {truncate(tool.output, 45)}</span>
                  </Show>
                </text>
              )}
            </For>

            {/* Pending count - subtle */}
            <Show when={toolsByStatus().pending.length > 0}>
              <text fg={theme.textMuted}>{toolsByStatus().pending.length} queued</text>
            </Show>
          </box>
        </Show>

        {/* Result when completed */}
        <Show when={props.output && isCompleted()}>
          <text fg={theme.primary} paddingLeft={2}>
            <SuccessCheckmark /> Completed
          </text>
        </Show>

        {/* Initializing state - only show after pending when waiting for tools */}
        <Show when={props.state.status !== "pending" && isRunning() && !props.metadata.summary?.length}>
          <text fg={theme.textMuted} paddingLeft={2}>
            <GenericSpinner spinner="task_spinner" color={theme.accent} /> Initializing...
          </text>
        </Show>

        {/* Navigation hint */}
        <text fg={theme.textMuted}>
          {keybind.print("session_child_cycle")}, {keybind.print("session_child_cycle_reverse")} to view full session
        </text>
      </box>
    )
  },
})

ToolRegistry.register({
  name: "background-agent",
  container: "block",
  render(props) {
    const { theme } = useTheme()
    const input = props.input as { description: string; prompt: string; subagent_type?: string }
    const isRunning = () => props.state.status === "running"
    const isCompleted = () => props.state.status === "completed"
    const isError = () => props.state.status === "error"

    const statusColor = createMemo(() => {
      if (isError()) return theme.error
      if (isRunning()) return theme.accent
      if (isCompleted()) return theme.primary
      return theme.border
    })

    return (
      <box
        marginTop={1}
        border={["left", "right", "top", "bottom"]}
        borderColor={statusColor()}
        paddingTop={1}
        paddingBottom={1}
        paddingLeft={2}
        paddingRight={2}
        gap={1}
      >
        <box flexDirection="row" gap={1}>
          <Show
            when={isRunning()}
            fallback={
              <Show
                when={isCompleted()}
                fallback={
                  <Show when={isError()} fallback={<BackgroundAgentSpinner state="active" />}>
                    <text fg={theme.error} attributes={TextAttributes.BOLD}>
                      ✗
                    </text>
                  </Show>
                }
              >
                <text fg={theme.primary} attributes={TextAttributes.BOLD}>
                  ✓
                </text>
              </Show>
            }
          >
            <BackgroundAgentSpinner state="busy" />
          </Show>
          <text>
            <span style={{ fg: theme.primary, bold: true }}>background-agent</span>
            <Show when={input.subagent_type}>
              <span style={{ fg: theme.textMuted }}> ({input.subagent_type})</span>
            </Show>
          </text>
        </box>
        <text fg={theme.text} paddingLeft={2}>
          {input.description}
        </text>
        <Show when={isCompleted()}>
          <text fg={theme.textMuted} paddingLeft={2}>
            Agent spawned and running in background
          </text>
        </Show>
      </box>
    )
  },
})

ToolRegistry.register<typeof WebFetchTool>({
  name: "webfetch",
  container: "inline",
  render(props) {
    const url = (props.input as any).url

    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    const startTime = createMemo(() => {
      if (props.state.status !== "pending" && props.state.time?.start) {
        return props.state.time.start
      }
      return undefined
    })

    // Get metadata from tool output
    const bytesReceived = createMemo(() => (props.metadata as any).bytes as number | undefined)
    const contentType = createMemo(() => (props.metadata as any).contentType as string | undefined)

    return (
      <ToolCard status={props.state.status} inline={true}>
        <WebfetchToolAnimation
          status={props.state.status}
          url={url}
          startTime={startTime()}
          executionTime={executionTime()}
          bytesReceived={bytesReceived()}
          contentType={contentType()}
        />
      </ToolCard>
    )
  },
})

ToolRegistry.register<typeof EditTool>({
  name: "edit",
  container: "block",
  render(props) {
    const ctx = use()
    const { theme, syntax } = useTheme()

    const view = createMemo(() => {
      const diffStyle = ctx.sync.data.config.tui?.diff_style
      if (diffStyle === "stacked") return "unified"
      return ctx.width > 120 ? "split" : "unified"
    })

    const diffContent = createMemo(() => props.metadata.diff ?? props.permission["diff"])

    const ft = createMemo(() => filetype(props.input.filePath))
    const isRunning = createMemo(() => !props.output && props.input.filePath)
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    const diagnostics = createMemo(() => {
      const arr = props.metadata.diagnostics?.[props.input.filePath ?? ""] ?? []
      return arr.filter((x) => x.severity === 1).slice(0, 3)
    })

    return (
      <ToolCard status={props.state.status} inline={false}>
        <box gap={1}>
          <ToolTitle
            icon=">"
            fallback="Editing"
            when={props.input.filePath}
            status={props.state.status}
            color={TOOL_COLORS.edit}
            customSpinner={<EditDeltaMorphChain />}
            runningLabel="Editing"
            labelColor={TOOL_LABEL_COLORS.edit}
          >
            <Show when={isRunning()} fallback={<>Edit {normalizePath(props.input.filePath!)}</>}>
              {normalizePath(props.input.filePath!)}
            </Show>{" "}
            {input({
              replaceAll: props.input.replaceAll,
            })}{" "}
            <Show when={isRunning() && props.state.status !== "pending" && props.state.time?.start}>
              <ElapsedTime startTime={props.state.status !== "pending" ? props.state.time!.start : 0} />
            </Show>
            <Show when={props.output}>
              <SuccessCheckmark />
            </Show>
          </ToolTitle>
          <Show when={diffContent()}>
            <box paddingLeft={1}>
              <diff
                diff={diffContent()!}
                view={view()}
                filetype={ft()}
                syntaxStyle={syntax()}
                showLineNumbers={true}
                width="100%"
                wrapMode={ctx.diffWrapMode()}
              />
            </box>
          </Show>
          <Show when={props.output}>
            <ToolMetadata executionTime={executionTime()} metadata={props.metadata} input={props.input} />
          </Show>
          <Show when={diagnostics().length}>
            <box>
              <For each={diagnostics()}>
                {(diagnostic) => (
                  <text fg={theme.error}>
                    Error [{diagnostic.range.start.line + 1}:{diagnostic.range.start.character + 1}]{" "}
                    {diagnostic.message}
                  </text>
                )}
              </For>
            </box>
          </Show>
        </box>
      </ToolCard>
    )
  },
})

ToolRegistry.register<typeof PatchTool>({
  name: "patch",
  container: "block",
  render(props) {
    const { theme } = useTheme()
    return (
      <>
        <ToolTitle icon="%" fallback="Preparing patch..." when={true} color={TOOL_COLORS.patch} spinner="edit_spinner">
          Patch
        </ToolTitle>
        <Show when={props.output}>
          <box>
            <text fg={theme.text}>{props.output?.trim()}</text>
          </box>
        </Show>
      </>
    )
  },
})

ToolRegistry.register<typeof TodoWriteTool>({
  name: "todowrite",
  container: "block",
  render(props) {
    const { theme } = useTheme()
    const isRunning = createMemo(() => props.state.status === "running")
    const isCompleted = createMemo(() => props.state.status === "completed")
    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    // Opacity for fade-out effect
    const [opacity, setOpacity] = createSignal(1)

    // Fade out after completion
    createEffect(() => {
      if (isCompleted() && !isRunning()) {
        setTimeout(() => {
          setOpacity(0.5)
        }, 2500)
      }
    })

    return (
      <ToolCard status={props.state.status} inline={false}>
        <box gap={1}>
          <ToolTitle
            icon="✓"
            fallback="Updating todos..."
            when={props.input.todos?.length}
            status={isRunning() ? "running" : undefined}
            color={TOOL_COLORS.todo}
            spinner="todowrite_spinner"
          >
            <span style={{ fg: theme.accent, bold: true }}>Todo List</span>
            <Show when={isRunning()}>
              {" "}
              <StreamingDots />
            </Show>
            <Show when={isCompleted()}>
              {" "}
              <SuccessCheckmark />
            </Show>
          </ToolTitle>

          <Show when={isRunning() && props.state.status !== "pending"}>
            <box paddingLeft={1}>
              <text>
                <ElapsedTime startTime={props.state.status !== "pending" ? props.state.time!.start : 0} />
              </text>
            </box>
          </Show>

          <Show when={executionTime()}>
            <ToolMetadata executionTime={executionTime()} />
          </Show>

          <Show when={props.input.todos?.length}>
            <box>
              <For each={props.input.todos ?? []}>
                {(todo) => {
                  const color = () => {
                    switch (todo.status) {
                      case "completed":
                        return theme.textMuted
                      case "in_progress":
                        return theme.accent
                      case "pending":
                        return theme.textMuted
                      case "cancelled":
                        return theme.error
                      default:
                        return theme.textMuted
                    }
                  }
                  return (
                    <text style={{ fg: color() }}>
                      [{todo.status === "completed" ? "✓" : " "}] {todo.content}
                    </text>
                  )
                }}
              </For>
            </box>
          </Show>
        </box>
      </ToolCard>
    )
  },
})

// Register for both hyphenated and snake_case versions
const exitPlanModeRenderer = {
  container: "block" as const,
  render(props: ToolProps<typeof ExitPlanModeTool>) {
    const { theme } = useTheme()
    const isPending = createMemo(() => props.state.status === "pending")
    const isRunning = createMemo(() => props.state.status === "running")
    const isCompleted = createMemo(() => props.state.status === "completed")
    const isError = createMemo(() => props.state.status === "error")
    const isUpdate = createMemo(() => !!(props.input as any)?.planID)

    // Map tool state to spinner state
    const spinnerState = createMemo(() => {
      if (isPending() || isRunning()) return "thinking"
      if (isCompleted()) return "approved"
      return "ready"
    })

    const executionTime = createMemo(() => {
      if (props.state.status === "completed" && props.state.time) {
        return props.state.time.end - props.state.time.start
      }
      return undefined
    })

    return (
      <ToolCard status={props.state.status} inline={false}>
        <box gap={1}>
          <Show when={isError()}>
            <text paddingLeft={3}>
              <span style={{ fg: theme.error, bold: true }}>✗</span>{" "}
              <span style={{ fg: theme.error }}>Plan Rejected</span>
            </text>
            <Show when={isError() && "error" in props.state}>
              <box paddingLeft={5}>
                <text fg={theme.error}>{(props.state as any).error}</text>
              </box>
            </Show>
          </Show>

          <Show when={!isError()}>
            <box paddingLeft={3} flexDirection="row" gap={1}>
              <PlanModeSpinner state={spinnerState()} />
              <Show when={isPending() || isRunning()}>
                <text fg={RGBA.fromHex("#cba6f7")}>{isUpdate() ? "Updating plan" : "Planning"}</text>
                <Show when={isRunning() && (props.state as { time?: { start: number } }).time?.start}>
                  <text>
                    <ElapsedTime
                      startTime={(props.state as { time: { start: number } }).time.start}
                      color={theme.textMuted}
                    />
                  </text>
                </Show>
              </Show>
              <Show when={isCompleted()}>
                <text>
                  <span style={{ fg: RGBA.fromHex("#a6e3a1"), bold: true }}>
                    {isUpdate() ? "Updated Plan Approved" : "Plan Approved"}
                  </span>{" "}
                  <SuccessCheckmark />
                </text>
              </Show>
            </box>
          </Show>

          <Show when={executionTime()}>
            <ToolMetadata executionTime={executionTime()} />
          </Show>

          <Show when={props.input.plan && isCompleted()}>
            <box paddingLeft={5}>
              <text style={{ fg: theme.textMuted }}>{(props.input.plan as string).split("\n")[0]}...</text>
            </box>
          </Show>
        </box>
      </ToolCard>
    )
  },
}

// Register for both possible name formats
ToolRegistry.register({ name: "exit-plan-mode", ...exitPlanModeRenderer })
ToolRegistry.register({ name: "exit_plan_mode", ...exitPlanModeRenderer })
ToolRegistry.register({ name: "exitplanmode", ...exitPlanModeRenderer })

// Ask User Tool Renderer
ToolRegistry.register<typeof AskUserTool>({
  name: "ask-user",
  container: "block",
  render(props) {
    const title = createMemo(() => (props.input as any).title ?? "...")
    const questionCount = createMemo(() => ((props.input as any).questions as any[])?.length ?? 0)
    const answeredCount = createMemo(() => {
      if (props.state.status !== "completed" && props.state.status !== "error") return 0
      const state = props.state as any
      const answers = state.metadata?.answers as Record<string, any> | undefined
      return answers ? Object.keys(answers).length : 0
    })
    const executionTime = createMemo(() => {
      if (props.state.status !== "completed" && props.state.status !== "error") return undefined
      const state = props.state as any
      return state.time?.end ? state.time.end - state.time.start : undefined
    })
    const startTime = createMemo(() => {
      if (props.state.status === "pending") return undefined
      const state = props.state as any
      return state.time?.start
    })

    return (
      <ToolCard status={props.state.status}>
        <AskUserToolAnimation
          status={props.state.status}
          title={title()}
          questionCount={questionCount()}
          answeredCount={answeredCount()}
          startTime={startTime()}
          executionTime={executionTime()}
        />
      </ToolCard>
    )
  },
})

// Manual Command Tool - displays copy-able commands that need manual execution
ToolRegistry.register<typeof ManualCommandTool>({
  name: "manual-command",
  container: "block",
  render(props) {
    const { theme } = useTheme()
    const command = () => (props.input.command as string) ?? ""
    const reason = () => (props.input.reason as string) ?? "Run manually:"
    const isRunning = () => props.state.status === "pending" || props.state.status === "running"
    const isCompleted = () => props.state.status === "completed"

    const startTime = createMemo(() => {
      if (props.state.status !== "pending" && props.state.time?.start) {
        return props.state.time.start
      }
      return undefined
    })

    return (
      <ToolCard status={props.state.status} inline={false}>
        <Show
          when={isCompleted()}
          fallback={
            <box gap={1}>
              <text>
                <GenericSpinner spinner="braille_fade" color={RGBA.fromHex("#f9e2af")} />
                <span style={{ fg: theme.warning, bold: true }}> ⚠️ Manual command required</span>
                <Show when={isRunning()}>
                  <StreamingDots />
                </Show>
              </text>
              <Show when={command()}>
                <text>
                  <span style={{ fg: theme.textMuted }}> Command: </span>
                  <span style={{ fg: theme.text }}>{command()}</span>
                </text>
              </Show>
              <Show when={startTime()}>
                <text>
                  <span style={{ fg: theme.textMuted }}> </span>
                  <ElapsedTime startTime={startTime()!} />
                </text>
              </Show>
            </box>
          }
        >
          <CopyBlock command={command()} label={reason()} icon="⚠️" />
        </Show>
      </ToolCard>
    )
  },
})

function normalizePath(input?: string) {
  if (!input) return ""
  if (path.isAbsolute(input)) {
    return path.relative(process.cwd(), input) || "."
  }
  return input
}

function input(input: Record<string, any>, omit?: string[]): string {
  const primitives = Object.entries(input).filter(([key, value]) => {
    if (omit?.includes(key)) return false
    return typeof value === "string" || typeof value === "number" || typeof value === "boolean"
  })
  if (primitives.length === 0) return ""
  return `[${primitives.map(([key, value]) => `${key}=${value}`).join(", ")}]`
}

function filetype(input?: string) {
  if (!input) return "none"
  const ext = path.extname(input)
  const language = LANGUAGE_EXTENSIONS[ext]
  if (["typescriptreact", "javascriptreact", "javascript"].includes(language)) return "typescript"
  return language
}
