import { DialogPrompt } from "@tui/ui/dialog-prompt"
import { useDialog } from "@tui/ui/dialog"
import { useWorkspace } from "@tui/context/workspace"
import { useToast } from "@tui/ui/toast"
import { For, Show, createSignal } from "solid-js"
import { useTheme } from "@tui/context/theme"
import { useKeyboard } from "@opentui/solid"
import * as fs from "fs"
import * as path from "path"

export function DialogWorkspaceAdd() {
  const dialog = useDialog()
  const workspace = useWorkspace()
  const toast = useToast()

  return (
    <DialogPrompt
      title="Add Workspace Directory (enter absolute path)"
      onConfirm={(value) => {
        const resolved = path.resolve(value)
        
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
            message: `Directory already in workspace: ${resolved}`,
            duration: 3000,
          })
          dialog.clear()
          return
        }

        workspace.add(resolved)
        toast.show({
          variant: "success",
          message: `Added to workspace: ${resolved}`,
          duration: 3000,
        })
        dialog.clear()
      }}
      onCancel={() => dialog.clear()}
    />
  )
}

export function DialogWorkspaceRemove() {
  const dialog = useDialog()
  const workspace = useWorkspace()
  const toast = useToast()
  const { theme } = useTheme()
  const [selectedIndex, setSelectedIndex] = createSignal(0)

  const dirs = () => workspace.list()

  useKeyboard((evt) => {
    if (evt.name === "escape") {
      dialog.clear()
      evt.preventDefault()
      return
    }

    if (evt.name === "up" || evt.name === "k") {
      setSelectedIndex((i) => Math.max(0, i - 1))
      evt.preventDefault()
    }
    if (evt.name === "down" || evt.name === "j") {
      setSelectedIndex((i) => Math.min(dirs().length - 1, i + 1))
      evt.preventDefault()
    }
    if (evt.name === "return") {
      const dir = dirs()[selectedIndex()]
      if (dir) {
        workspace.remove(dir)
        toast.show({
          variant: "success",
          message: `Removed from workspace: ${dir}`,
          duration: 3000,
        })
        if (dirs().length === 0) {
          dialog.clear()
        } else {
          setSelectedIndex((i) => Math.min(i, dirs().length - 1))
        }
      }
      evt.preventDefault()
    }
  })

  return (
    <box flexDirection="column" padding={2} gap={1}>
      <text fg={theme.text}>
        <b>Remove Workspace Directory</b>
      </text>
      <Show
        when={dirs().length > 0}
        fallback={
          <text fg={theme.textMuted}>No workspace directories to remove.</text>
        }
      >
        <text fg={theme.textMuted}>Select a directory to remove (↑↓ to navigate, Enter to remove, Esc to cancel)</text>
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
              </box>
            )}
          </For>
        </box>
      </Show>
    </box>
  )
}
