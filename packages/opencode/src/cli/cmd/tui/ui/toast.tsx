import { createContext, useContext, type ParentProps, Show, createSignal, onCleanup } from "solid-js"
import { createStore } from "solid-js/store"
import { useTheme } from "@tui/context/theme"
import { RoundedBorder } from "../component/border"
import { TextAttributes, RGBA } from "@opentui/core"
import z from "zod"
import { TuiEvent } from "../event"

export type ToastOptions = z.infer<typeof TuiEvent.ToastShow.properties>

// Icons for each variant
const TOAST_ICONS = {
  success: "✓",
  error: "✗",
  warning: "⚠",
  info: "●",
}

// Animated icon component with breathing effect
function ToastIcon(props: { variant: "info" | "success" | "warning" | "error"; color: RGBA }) {
  const [opacity, setOpacity] = createSignal(0.7)

  const interval = setInterval(() => {
    const now = Date.now()
    const phase = ((now / 1200) * Math.PI * 2) % (Math.PI * 2)
    setOpacity(0.6 + (Math.sin(phase) + 1) * 0.2)
  }, 50)

  onCleanup(() => clearInterval(interval))

  const color = () => RGBA.fromInts(props.color.r * 255, props.color.g * 255, props.color.b * 255, opacity() * 255)

  return (
    <text>
      <span style={{ fg: color(), bold: true }}>{TOAST_ICONS[props.variant]}</span>
    </text>
  )
}

// Shimmer text effect for toast messages
function ShimmerText(props: { text: string; color: RGBA }) {
  const [phase, setPhase] = createSignal(0)

  const interval = setInterval(() => {
    setPhase((p) => (p + 1) % 100)
  }, 30)

  onCleanup(() => clearInterval(interval))

  const chars = () => props.text.split("")

  return (
    <text>
      {(() => {
        return chars().map((ch, i) => {
          const charPhase = (phase() + i * 3) % 100
          const wave = Math.sin((charPhase / 100) * Math.PI * 2)
          const opacity = 0.7 + wave * 0.3
          const c = RGBA.fromInts(props.color.r * 255, props.color.g * 255, props.color.b * 255, opacity * 255)
          return <span style={{ fg: c }}>{ch}</span>
        })
      })()}
    </text>
  )
}

export function Toast() {
  const toast = useToast()
  const { theme } = useTheme()

  return (
    <Show when={toast.currentToast}>
      {(current) => (
        <box
          position="absolute"
          justifyContent="center"
          alignItems="flex-start"
          top={1}
          right={2}
          paddingLeft={2}
          paddingRight={2}
          paddingTop={1}
          paddingBottom={1}
          backgroundColor={theme.backgroundPanel}
          borderColor={theme[current().variant]}
          border={["left", "right", "top", "bottom"]}
          customBorderChars={RoundedBorder.customBorderChars}
        >
          <box flexDirection="row" gap={1} alignItems="center">
            <ToastIcon variant={current().variant} color={theme[current().variant]} />
            <box flexDirection="column">
              <Show when={current().title}>
                <text attributes={TextAttributes.BOLD} fg={theme.text}>
                  {current().title}
                </text>
              </Show>
              <ShimmerText text={current().message} color={theme.text} />
            </box>
          </box>
        </box>
      )}
    </Show>
  )
}

// ============================================================================
// PREVIEW COMPONENTS - For displaying samples in home.tsx
// ============================================================================

/**
 * Toast Preview - Static preview of a toast notification for sampling
 */
export function ToastPreview(props: {
  variant: "info" | "success" | "warning" | "error"
  message: string
  title?: string
}) {
  const { theme } = useTheme()

  return (
    <box
      flexDirection="row"
      paddingLeft={2}
      paddingRight={2}
      paddingTop={1}
      paddingBottom={1}
      backgroundColor={theme.backgroundPanel}
      borderColor={theme[props.variant]}
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
    >
      <box flexDirection="row" gap={1} alignItems="center">
        <ToastIcon variant={props.variant} color={theme[props.variant]} />
        <box flexDirection="column">
          <Show when={props.title}>
            <text attributes={TextAttributes.BOLD} fg={theme.text}>
              {props.title}
            </text>
          </Show>
          <ShimmerText text={props.message} color={theme.text} />
        </box>
      </box>
    </box>
  )
}

// ============================================================================
// ERROR BOX - Inline error display styled like toast (for aborted messages etc)
// ============================================================================

/**
 * ErrorBox - Inline error display that looks like a toast
 * Used for displaying error messages like "The operation was aborted"
 */
export function ErrorBox(props: { message: string }) {
  const { theme } = useTheme()

  return (
    <box
      flexDirection="row"
      paddingLeft={2}
      paddingRight={2}
      paddingTop={1}
      paddingBottom={1}
      marginTop={1}
      backgroundColor={theme.backgroundPanel}
      borderColor={theme.error}
      border={["left", "right", "top", "bottom"]}
      customBorderChars={RoundedBorder.customBorderChars}
    >
      <box flexDirection="row" gap={1} alignItems="center">
        <ToastIcon variant="error" color={theme.error} />
        <text fg={theme.textMuted}>{props.message}</text>
      </box>
    </box>
  )
}

// ============================================================================
// TOAST CONTEXT & PROVIDER
// ============================================================================

function init() {
  const [store, setStore] = createStore({
    currentToast: null as ToastOptions | null,
  })

  let timeoutHandle: NodeJS.Timeout | null = null

  const toast = {
    show(options: ToastOptions) {
      const parsedOptions = TuiEvent.ToastShow.properties.parse(options)
      const { duration, ...currentToast } = parsedOptions
      setStore("currentToast", currentToast)
      if (timeoutHandle) clearTimeout(timeoutHandle)
      timeoutHandle = setTimeout(() => {
        setStore("currentToast", null)
      }, duration).unref()
    },
    error: (err: any) => {
      if (err instanceof Error)
        return toast.show({
          variant: "error",
          message: err.message,
        })
      toast.show({
        variant: "error",
        message: "An unknown error has occurred",
      })
    },
    get currentToast(): ToastOptions | null {
      return store.currentToast
    },
  }
  return toast
}

export type ToastContext = ReturnType<typeof init>

const ctx = createContext<ToastContext>()

export function ToastProvider(props: ParentProps) {
  const value = init()
  return <ctx.Provider value={value}>{props.children}</ctx.Provider>
}

export function useToast() {
  const value = useContext(ctx)
  if (!value) {
    throw new Error("useToast must be used within a ToastProvider")
  }
  return value
}
