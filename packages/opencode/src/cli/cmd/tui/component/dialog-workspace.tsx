import { TextareaRenderable, TextAttributes } from "@opentui/core"
import { useDialog } from "@tui/ui/dialog"
import { useWorkspace } from "@tui/context/workspace"
import { useToast } from "@tui/ui/toast"
import { For, Show, createSignal, createMemo, onMount } from "solid-js"
import { useTheme } from "@tui/context/theme"
import { useKeyboard } from "@opentui/solid"
import * as fs from "fs"
import * as path from "path"
import * as os from "os"

function expandTilde(inputPath: string): string {
  if (inputPath.startsWith("~/")) {
    return path.join(os.homedir(), inputPath.slice(2))
  } else if (inputPath === "~") {
    return os.homedir()
  }
  return inputPath
}

function contractTilde(fullPath: string): string {
  const home = os.homedir()
  if (fullPath === home) return "~"
  if (fullPath.startsWith(home + "/")) {
    return "~" + fullPath.slice(home.length)
  }
  return fullPath
}

function getDirectorySuggestions(input: string, maxResults = 8): string[] {
  if (!input) return []

  const expanded = expandTilde(input)
  
  try {
    // If input ends with /, list contents of that directory
    if (input.endsWith("/")) {
      const stat = fs.statSync(expanded)
      if (stat.isDirectory()) {
        const entries = fs.readdirSync(expanded, { withFileTypes: true })
        return entries
          .filter(e => e.isDirectory() && !e.name.startsWith("."))
          .slice(0, maxResults)
          .map(e => contractTilde(path.join(expanded, e.name)))
      }
    }
    
    // Otherwise, find parent directory and filter by prefix
    const parentDir = path.dirname(expanded)
    const prefix = path.basename(expanded).toLowerCase()
    
    const stat = fs.statSync(parentDir)
    if (stat.isDirectory()) {
      const entries = fs.readdirSync(parentDir, { withFileTypes: true })
      return entries
        .filter(e => e.isDirectory() && e.name.toLowerCase().startsWith(prefix))
        .slice(0, maxResults)
        .map(e => contractTilde(path.join(parentDir, e.name)))
    }
  } catch {}
  
  return []
}

export function DialogWorkspaceAdd() {
  const dialog = useDialog()
  const workspace = useWorkspace()
  const toast = useToast()
  const { theme } = useTheme()
  
  const [inputValue, setInputValue] = createSignal("")
  const [selectedSuggestion, setSelectedSuggestion] = createSignal(0)
  const [selectedDir, setSelectedDir] = createSignal(-1) // -1 = not selecting existing dirs
  
  let textarea: TextareaRenderable

  const dirs = () => workspace.list()
  
  const suggestions = createMemo(() => getDirectorySuggestions(inputValue()))

  onMount(() => {
    dialog.setSize("large")
    setTimeout(() => textarea?.focus(), 1)
  })

  function addDirectory(value: string) {
    if (!value.trim()) return

    const resolved = path.resolve(expandTilde(value.trim()))

    try {
      const stat = fs.statSync(resolved)
      if (!stat.isDirectory()) {
        toast.show({ variant: "error", message: `Not a directory: ${resolved}`, duration: 3000 })
        return
      }
    } catch {
      toast.show({ variant: "error", message: `Directory not found: ${resolved}`, duration: 3000 })
      return
    }

    if (workspace.has(resolved)) {
      toast.show({ variant: "warning", message: `Already in workspace: ${resolved}`, duration: 3000 })
      return
    }

    workspace.add(resolved)
    toast.show({ variant: "success", message: `Added: ${contractTilde(resolved)}`, duration: 3000 })
    textarea.clear()
    setInputValue("")
  }

  function applySuggestion(suggestion: string) {
    textarea.clear()
    textarea.insertText(suggestion + "/")
    setInputValue(suggestion + "/")
    setSelectedSuggestion(0)
  }

  // Navigation: existing dirs (top) → input → suggestions (bottom)
  // Layout order: dirs[0..n-1], input (-1), suggestions[0..m-1]
  useKeyboard((evt) => {
    if (evt.name === "escape") {
      dialog.clear()
      evt.preventDefault()
      return
    }

    const dirIdx = selectedDir()
    const suggIdx = selectedSuggestion()
    const numDirs = dirs().length
    const numSuggs = suggestions().length
    const inDirs = dirIdx >= 0
    const inSuggs = !inDirs && numSuggs > 0 && suggIdx >= 0

    // Tab = apply current suggestion (works from input or suggestions)
    if (evt.name === "tab" && !inDirs && numSuggs > 0) {
      applySuggestion(suggestions()[suggIdx])
      evt.preventDefault()
      return
    }

    // UP navigation
    if (evt.name === "up") {
      if (inDirs) {
        // In dirs: go up or wrap to suggestions/input
        if (dirIdx > 0) {
          setSelectedDir(dirIdx - 1)
        } else {
          // At top dir, wrap to bottom (suggestions or input)
          if (numSuggs > 0) {
            setSelectedDir(-1)
            setSelectedSuggestion(numSuggs - 1)
          }
          // else stay at top dir
        }
      } else if (numSuggs > 0) {
        // In input or suggestions: cycle through suggestions (backwards)
        setSelectedSuggestion((suggIdx - 1 + numSuggs) % numSuggs)
      } else if (numDirs > 0) {
        // No suggestions: go to dirs
        setSelectedDir(numDirs - 1)
        textarea?.blur()
      }
      evt.preventDefault()
      return
    }

    // DOWN navigation
    if (evt.name === "down") {
      if (inDirs) {
        // In dirs: go down or to input
        if (dirIdx < numDirs - 1) {
          setSelectedDir(dirIdx + 1)
        } else {
          // At bottom dir, go to input/suggestions
          setSelectedDir(-1)
          setSelectedSuggestion(0)
          textarea?.focus()
        }
      } else if (numSuggs > 0) {
        // In input or suggestions: cycle through suggestions
        setSelectedSuggestion((suggIdx + 1) % numSuggs)
      } else if (numDirs > 0) {
        // No suggestions, go to dirs
        setSelectedDir(0)
        textarea?.blur()
      }
      evt.preventDefault()
      return
    }

    // Enter
    if (evt.name === "return") {
      if (inDirs) {
        // Exit dir selection, back to input
        setSelectedDir(-1)
        textarea?.focus()
      } else if (numSuggs > 0) {
        // Has suggestions: ADD the highlighted one as workspace directory
        addDirectory(suggestions()[suggIdx])
      } else {
        // No suggestions: add the typed path
        addDirectory(textarea?.plainText || "")
      }
      evt.preventDefault()
      return
    }

    // Remove selected existing dir (x, d, or backspace when dir selected)
    if (inDirs && (evt.name === "x" || evt.name === "d" || evt.name === "backspace")) {
      const dir = dirs()[dirIdx]
      workspace.remove(dir)
      toast.show({ variant: "success", message: `Removed: ${contractTilde(dir)}`, duration: 3000 })
      if (dirs().length === 0) {
        setSelectedDir(-1)
        textarea?.focus()
      } else {
        setSelectedDir(Math.min(dirIdx, dirs().length - 1))
      }
      evt.preventDefault()
    }
  })

  return (
    <box flexDirection="column" paddingLeft={2} paddingRight={2} gap={1}>
      <box flexDirection="row" justifyContent="space-between">
        <text attributes={TextAttributes.BOLD}>Workspace Directories</text>
        <text fg={theme.textMuted}>esc</text>
      </box>

      {/* Existing directories */}
      <Show when={dirs().length > 0}>
        <box flexDirection="column">
          <text fg={theme.textMuted}>Current (x to remove):</text>
          <For each={dirs()}>
            {(dir, i) => (
              <box flexDirection="row">
                <text fg={i() === selectedDir() ? theme.accent : theme.textMuted}>
                  {i() === selectedDir() ? " ▶ " : "   "}
                </text>
                <text fg={i() === selectedDir() ? theme.text : theme.textMuted}>
                  {contractTilde(dir)}
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>

      {/* Input */}
      <box flexDirection="column" paddingTop={1}>
        <text>Add directory:</text>
        <box flexDirection="row" paddingTop={1}>
          <text fg={theme.accent}>{"❯ "}</text>
          <textarea
            ref={(val: TextareaRenderable) => (textarea = val)}
            placeholder="Type path... (Tab to complete)"
            keyBindings={[]}
            minHeight={1}
            maxHeight={1}
            onContentChange={() => {
              setInputValue(textarea?.plainText || "")
              setSelectedSuggestion(0)
              setSelectedDir(-1)
            }}
          />
        </box>
      </box>

      {/* Suggestions - shown automatically as you type */}
      <Show when={suggestions().length > 0}>
        <box flexDirection="column">
          <For each={suggestions()}>
            {(suggestion, i) => (
              <box flexDirection="row">
                <text fg={i() === selectedSuggestion() ? theme.accent : theme.textMuted}>
                  {i() === selectedSuggestion() ? " ▶ " : "   "}
                </text>
                <text fg={i() === selectedSuggestion() ? theme.text : theme.textMuted}>
                  {suggestion}/
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>

      <text fg={theme.textMuted}>↑↓ navigate • Tab complete • Enter add • x remove</text>
    </box>
  )
}

export const DialogWorkspaceRemove = DialogWorkspaceAdd
