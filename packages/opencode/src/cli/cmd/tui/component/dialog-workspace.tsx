import { TextareaRenderable, TextAttributes } from "@opentui/core"
import { useDialog } from "@tui/ui/dialog"
import { useWorkspace } from "@tui/context/workspace"
import { useToast } from "@tui/ui/toast"
import { For, Show, createSignal, onMount } from "solid-js"
import { useTheme } from "@tui/context/theme"
import { useKeyboard } from "@opentui/solid"
import * as fs from "fs"
import * as path from "path"

/**
 * Combined workspace management dialog:
 * - Shows existing workspace directories
 * - Allows removing them with 'x' or 'd'
 * - Has input field to add new directories
 */
export function DialogWorkspaceAdd() {
  const dialog = useDialog()
  const workspace = useWorkspace()
  const toast = useToast()
  const { theme } = useTheme()
  const [selectedIndex, setSelectedIndex] = createSignal(-1) // -1 = input focused
  let textarea: TextareaRenderable

  const dirs = () => workspace.list()

  onMount(() => {
    dialog.setSize("large")
    setTimeout(() => {
      textarea?.focus()
    }, 1)
  })

  function addDirectory(value: string) {
    if (!value.trim()) return

    const resolved = path.resolve(value.trim())

    // Check if directory exists
    try {
      const stat = fs.statSync(resolved)
      if (!stat.isDirectory()) {
        toast.show({
          variant: "error",
          message: `Not a directory: ${resolved}`,
          duration: 3000,
        })
        return
      }
    } catch {
      toast.show({
        variant: "error",
        message: `Directory not found: ${resolved}`,
        duration: 3000,
      })
      return
    }

    // Check if already added
    if (workspace.has(resolved)) {
      toast.show({
        variant: "warning",
        message: `Already in workspace: ${resolved}`,
        duration: 3000,
      })
      return
    }

    workspace.add(resolved)
    toast.show({
      variant: "success",
      message: `Added: ${resolved}`,
      duration: 3000,
    })
    textarea.clear()
  }

  function removeSelected() {
    const idx = selectedIndex()
    if (idx >= 0 && idx < dirs().length) {
      const dir = dirs()[idx]
      workspace.remove(dir)
      toast.show({
        variant: "success",
        message: `Removed: ${dir}`,
        duration: 3000,
      })
      // Adjust selection
      if (dirs().length === 0) {
        setSelectedIndex(-1)
        textarea?.focus()
      } else {
        setSelectedIndex(Math.min(idx, dirs().length - 1))
      }
    }
  }

  useKeyboard((evt) => {
    if (evt.name === "escape") {
      dialog.clear()
      evt.preventDefault()
      return
    }

    const idx = selectedIndex()

    // Navigate up
    if (evt.name === "up") {
      if (idx === -1 && dirs().length > 0) {
        // From input to last dir
        setSelectedIndex(dirs().length - 1)
        textarea?.blur()
      } else if (idx > 0) {
        setSelectedIndex(idx - 1)
      } else if (idx === 0) {
        setSelectedIndex(-1)
        textarea?.focus()
      }
      evt.preventDefault()
      return
    }

    // Navigate down
    if (evt.name === "down") {
      if (idx === -1 && dirs().length > 0) {
        setSelectedIndex(0)
        textarea?.blur()
      } else if (idx >= 0 && idx < dirs().length - 1) {
        setSelectedIndex(idx + 1)
      }
      evt.preventDefault()
      return
    }

    // When in input mode
    if (idx === -1) {
      if (evt.name === "return") {
        addDirectory(textarea?.plainText || "")
        evt.preventDefault()
      }
      return
    }

    // When directory selected - remove with x, d, or backspace
    if (idx >= 0) {
      if (evt.name === "x" || evt.name === "d" || evt.name === "backspace") {
        removeSelected()
        evt.preventDefault()
      }
      if (evt.name === "return") {
        // Enter on a dir goes back to input
        setSelectedIndex(-1)
        textarea?.focus()
        evt.preventDefault()
      }
    }
  })

  return (
    <box flexDirection="column" paddingLeft={2} paddingRight={2} gap={1}>
      <box flexDirection="row" justifyContent="space-between">
        <text attributes={TextAttributes.BOLD}>Workspace Directories</text>
        <text fg={theme.textMuted}>esc to close</text>
      </box>

      {/* Existing directories */}
      <Show when={dirs().length > 0}>
        <box flexDirection="column" paddingTop={1}>
          <text fg={theme.textMuted}>Current directories (↑↓ navigate, x/d to remove):</text>
          <box paddingTop={1}>
            <For each={dirs()}>
              {(dir, index) => (
                <box flexDirection="row" gap={1}>
                  <text fg={index() === selectedIndex() ? theme.accent : theme.textMuted}>
                    {index() === selectedIndex() ? "▶" : " "}
                  </text>
                  <text fg={index() === selectedIndex() ? theme.text : theme.textMuted}>
                    {dir}
                  </text>
                  <Show when={index() === selectedIndex()}>
                    <text fg={theme.error}> [x to remove]</text>
                  </Show>
                </box>
              )}
            </For>
          </box>
        </box>
      </Show>

      {/* Add new directory input */}
      <box flexDirection="column" paddingTop={1}>
        <text fg={selectedIndex() === -1 ? theme.text : theme.textMuted}>
          Add new directory:
        </text>
        <box flexDirection="row" gap={1}>
          <text fg={selectedIndex() === -1 ? theme.accent : theme.textMuted}>
            {selectedIndex() === -1 ? "▶" : " "}
          </text>
          <textarea
            ref={(val: TextareaRenderable) => (textarea = val)}
            placeholder="/path/to/directory"
            keyBindings={[]}
            minHeight={1}
            maxHeight={1}
          />
        </box>
        <text fg={theme.textMuted} paddingTop={1}>
          Enter absolute path, press Enter to add
        </text>
      </box>
    </box>
  )
}

// Keep DialogWorkspaceRemove as alias for backwards compatibility
export const DialogWorkspaceRemove = DialogWorkspaceAdd
