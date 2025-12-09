import { TextAttributes } from "@opentui/core"
import { useTheme } from "../../context/theme"
import { useDialog } from "../../ui/dialog"
import { createSignal, createMemo, createEffect, on, onMount, Show } from "solid-js"
import { useKeyboard } from "@opentui/solid"
import type { Permission } from "@/permission"
import { useSDK } from "../../context/sdk"

export type DialogPinProps = {
  // Use getter function to allow reactive updates when permissions change
  // This allows the same component to handle multiple sequential PIN requests
  getPinPermission: () => Permission.Info | undefined
  sessionID: string
}

export function DialogPin(props: DialogPinProps) {
  const dialog = useDialog()
  const sdk = useSDK()
  const { theme } = useTheme()

  const [pin, setPin] = createSignal("")
  const [error, setError] = createSignal("")
  const [submitting, setSubmitting] = createSignal(false)
  // Track the last permission ID we submitted for to avoid double-submits
  const [lastSubmittedId, setLastSubmittedId] = createSignal<string | null>(null)

  // Access permission reactively through the getter
  const permission = createMemo(() => props.getPinPermission())

  // Reset state when permission changes (new PIN request comes in)
  createEffect(
    on(
      () => permission()?.id,
      (newId, oldId) => {
        if (newId && newId !== oldId) {
          // New permission arrived - reset all input state
          setPin("")
          setError("")
          setSubmitting(false)
        }
      },
    ),
  )

  onMount(() => {
    dialog.setSize("medium")
  })

  useKeyboard((evt) => {
    const perm = permission()
    if (!perm) return // No permission to handle
    if (submitting()) return

    // Handle printable characters for PIN input
    if (evt.name && evt.name.length === 1) {
      setPin((p) => p + evt.name)
      setError("")
      evt.preventDefault()
      return
    }

    // Backspace to delete
    if (evt.name === "backspace") {
      setPin((p) => p.slice(0, -1))
      setError("")
      evt.preventDefault()
      return
    }

    // Submit with Enter
    if (evt.name === "return") {
      if (pin().length < 4) {
        setError("PIN must be at least 4 characters")
        return
      }
      submit()
      evt.preventDefault()
      return
    }

    // Cancel with Escape
    if (evt.name === "escape") {
      cancel()
      evt.preventDefault()
      return
    }
  })

  function submit() {
    const perm = permission()
    if (!perm) return
    // Prevent double-submit for same permission
    if (lastSubmittedId() === perm.id) return

    setSubmitting(true)
    setError("")
    setLastSubmittedId(perm.id)

    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: {
          permissionID: perm.id,
          id: props.sessionID,
        },
        body: {
          response: { type: "pin", pin: pin() } as any,
        },
      })
      .catch((e) => {
        console.error("Failed to verify PIN:", e)
        setError("Failed to verify PIN")
        setPin("")
        setSubmitting(false)
        setLastSubmittedId(null) // Allow retry
      })
  }

  function cancel() {
    const perm = permission()
    if (!perm) return

    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: {
          permissionID: perm.id,
          id: props.sessionID,
        },
        body: {
          response: "reject",
        },
      })
      .catch((e) => {
        console.error("Failed to cancel PIN:", e)
      })
    // Note: dialog.clear() is NOT called here - the global escape handler
    // in dialog.tsx handles closing the dialog to prevent double-clear race conditions
  }

  // Get command from metadata - reactive to permission changes
  const command = createMemo(() => {
    const perm = permission()
    if (!perm) return ""
    return (perm.metadata.command as string) || perm.title
  })

  return (
    <Show when={permission()} fallback={<box />}>
      <box flexDirection="column" padding={2} gap={1}>
        {/* Header */}
        <box flexDirection="row" gap={1}>
          <text attributes={TextAttributes.BOLD}>
            <span style={{ fg: theme.warning }}>󰌾 </span>
            <span style={{ fg: theme.text }}>PIN Required</span>
          </text>
        </box>

        {/* Command being protected */}
        <box paddingTop={1}>
          <text fg={theme.textMuted}>Command:</text>
        </box>
        <box paddingLeft={2}>
          <text fg={theme.text}>{command()}</text>
        </box>

        {/* PIN Input Display */}
        <box paddingTop={1}>
          <text fg={theme.textMuted}>Enter PIN:</text>
        </box>
        <box paddingLeft={2} flexDirection="row">
          <text>
            <span style={{ fg: theme.primary }}>{"•".repeat(pin().length)}</span>
            <span style={{ fg: theme.primary, attributes: TextAttributes.BLINK }}>▌</span>
          </text>
        </box>

        {/* Error Message */}
        <Show when={error()}>
          <box paddingTop={1}>
            <text fg={theme.error}>{error()}</text>
          </box>
        </Show>

        {/* Submitting indicator */}
        <Show when={submitting()}>
          <box paddingTop={1}>
            <text fg={theme.textMuted}>Verifying...</text>
          </box>
        </Show>

        {/* Actions */}
        <box flexDirection="row" gap={3} paddingTop={2}>
          <text>
            <span style={{ fg: theme.primary }}>↵</span>
            <span style={{ fg: theme.textMuted }}> submit</span>
          </text>
          <text>
            <span style={{ fg: theme.textMuted }}>esc</span>
            <span style={{ fg: theme.textMuted }}> cancel</span>
          </text>
        </box>
      </box>
    </Show>
  )
}
