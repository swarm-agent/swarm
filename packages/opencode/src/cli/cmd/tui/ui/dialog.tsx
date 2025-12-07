import { useKeyboard, useRenderer, useTerminalDimensions } from "@opentui/solid"
import { batch, createContext, createMemo, Show, useContext, type JSX, type ParentProps } from "solid-js"
import { useTheme } from "@tui/context/theme"
import { Renderable, RGBA } from "@opentui/core"
import { createStore } from "solid-js/store"

export function Dialog(
  props: ParentProps<{
    size?: "medium" | "large"
    fullScreen?: boolean
    onClose: () => void
  }>,
) {
  const dimensions = useTerminalDimensions()
  const { theme } = useTheme()

  // Responsive width calculation based on terminal width breakpoints
  const responsiveWidth = createMemo(() => {
    const termWidth = dimensions().width
    const isLarge = props.size === "large"

    // Breakpoints for responsive scaling
    if (termWidth >= 140) {
      // Very wide terminals: use percentage-based width
      return isLarge ? "90%" : "70%"
    } else if (termWidth >= 100) {
      // Wide terminals: slightly higher percentage for better use of space
      return isLarge ? "92%" : "75%"
    } else if (termWidth >= 80) {
      // Medium terminals: increase percentage further
      return isLarge ? "94%" : "80%"
    } else if (termWidth >= 60) {
      // Narrow terminals: maximize usable space
      return isLarge ? "96%" : "85%"
    } else {
      // Very narrow terminals (50-60): use almost full width
      return isLarge ? "98%" : "90%"
    }
  })

  // Responsive max/min width based on terminal size
  const responsiveMaxWidth = createMemo(() => {
    const termWidth = dimensions().width
    const isLarge = props.size === "large"

    // Cap at slightly less than terminal width to ensure padding
    return Math.min(
      isLarge ? 140 : 100,
      termWidth - 4, // Reserve 4 chars for padding/margins
    )
  })

  const responsiveMinWidth = createMemo(() => {
    const termWidth = dimensions().width
    const isLarge = props.size === "large"

    // Ensure minimum doesn't exceed terminal width
    return Math.min(
      isLarge ? 80 : 60,
      Math.max(termWidth - 10, 40), // At least 40, but adapt to very small terminals
    )
  })

  return (
    <box
      onMouseUp={async () => {
        props.onClose?.()
      }}
      width={dimensions().width}
      height={dimensions().height}
      alignItems={props.fullScreen ? "stretch" : "center"}
      justifyContent={props.fullScreen ? "flex-start" : "center"}
      position="absolute"
      left={0}
      top={0}
      backgroundColor={props.fullScreen ? "transparent" : RGBA.fromInts(0, 0, 0, 150)}
    >
      <box
        onMouseUp={async (e) => {
          e.stopPropagation()
        }}
        width={props.fullScreen ? "100%" : responsiveWidth()}
        height={props.fullScreen ? "100%" : undefined}
        maxWidth={props.fullScreen ? undefined : responsiveMaxWidth()}
        minWidth={props.fullScreen ? undefined : responsiveMinWidth()}
        backgroundColor={props.fullScreen ? theme.background : theme.backgroundPanel}
        paddingTop={props.fullScreen ? 0 : 1}
      >
        {props.children}
      </box>
    </box>
  )
}

function init() {
  const [store, setStore] = createStore({
    stack: [] as {
      element: JSX.Element
      onClose?: () => void
      fullScreen?: boolean
    }[],
    size: "medium" as "medium" | "large",
    fullScreen: false,
    clearing: false, // Reentrancy guard for clear()
  })

  useKeyboard((evt) => {
    // Guard: don't handle escape if already clearing or if dialog-specific handler already handled it
    if (evt.name === "escape" && store.stack.length > 0 && !store.clearing) {
      // Check if event was already handled by a dialog-specific escape handler
      if (evt.defaultPrevented) return
      const current = store.stack.at(-1)!
      current.onClose?.()
      setStore("stack", store.stack.slice(0, -1))
      evt.preventDefault()
      refocus()
    }
  })

  const renderer = useRenderer()
  let focus: Renderable | null
  function refocus() {
    setTimeout(() => {
      if (!focus) return
      if (focus.isDestroyed) return
      function find(item: Renderable) {
        for (const child of item.getChildren()) {
          if (child === focus) return true
          if (find(child)) return true
        }
        return false
      }
      const found = find(renderer.root)
      if (!found) return
      focus.focus()
    }, 1)
  }

  return {
    clear() {
      // Reentrancy guard: prevent recursive calls to clear()
      if (store.clearing) return
      setStore("clearing", true)

      // Copy stack before iterating to prevent issues if onClose modifies stack
      const items = [...store.stack]
      for (const item of items) {
        if (item.onClose) item.onClose()
      }
      batch(() => {
        setStore("size", "medium")
        setStore("fullScreen", false)
        setStore("stack", [])
        setStore("clearing", false)
      })
      refocus()
    },
    forceClear() {
      // Force clear all dialogs and reset all state without triggering onClose callbacks
      // This is useful for emergency UI reset when dialogs get stuck
      batch(() => {
        setStore("size", "medium")
        setStore("fullScreen", false)
        setStore("stack", [])
      })
      refocus()
    },
    replace(input: any, onClose?: () => void, fullScreen?: boolean) {
      if (store.stack.length === 0) {
        focus = renderer.currentFocusedRenderable
        // Blur the focused element so it doesn't capture keyboard events (e.g. Enter key)
        // This allows the dialog's useKeyboard handlers to receive events
        focus?.blur()
      }
      for (const item of store.stack) {
        if (item.onClose) item.onClose()
      }
      batch(() => {
        setStore("size", "medium")
        setStore("fullScreen", fullScreen ?? false)
        setStore("stack", [
          {
            element: input,
            onClose,
            fullScreen,
          },
        ])
      })
    },
    get stack() {
      return store.stack
    },
    get size() {
      return store.size
    },
    get fullScreen() {
      return store.fullScreen
    },
    setSize(size: "medium" | "large") {
      setStore("size", size)
    },
  }
}

export type DialogContext = ReturnType<typeof init>

const ctx = createContext<DialogContext>()

export function DialogProvider(props: ParentProps) {
  const value = init()
  return (
    <ctx.Provider value={value}>
      {props.children}
      <box position="absolute">
        <Show when={value.stack.length}>
          <Dialog onClose={() => value.clear()} size={value.size} fullScreen={value.fullScreen}>
            {value.stack.at(-1)!.element}
          </Dialog>
        </Show>
      </box>
    </ctx.Provider>
  )
}

export function useDialog() {
  const value = useContext(ctx)
  if (!value) {
    throw new Error("useDialog must be used within a DialogProvider")
  }
  return value
}
