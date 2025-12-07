import { InputRenderable, ScrollBoxRenderable, TextAttributes } from "@opentui/core"
import { useTheme } from "../../context/theme"
import { useDialog } from "../../ui/dialog"
import { createStore } from "solid-js/store"
import { createMemo, For, onMount, Show } from "solid-js"
import { useKeyboard, useTerminalDimensions } from "@opentui/solid"
import type { Permission } from "@/permission"
import { useSDK } from "../../context/sdk"
import type { AskUserQuestion, AskUserOption } from "@/tool/ask-user"

export type DialogAskUserProps = {
  permission: Permission.Info
  sessionID: string
}

export function DialogAskUser(props: DialogAskUserProps) {
  const dialog = useDialog()
  const sdk = useSDK()
  const { theme } = useTheme()
  const dimensions = useTerminalDimensions()

  const questions = createMemo(() => (props.permission.metadata.questions as AskUserQuestion[]) || [])
  const context = createMemo(() => props.permission.metadata.context as string | undefined)
  const total = createMemo(() => Math.min(questions().length, 5))

  // Always append a "Write custom response" option to each question
  const questionsWithCustom = createMemo(() =>
    questions().map((q) => ({
      ...q,
      options: [
        ...q.options,
        {
          value: "__custom__",
          label: "Write custom response",
          description: "Type your own answer",
          allowCustom: true,
        },
      ],
    })),
  )

  const [store, setStore] = createStore({
    current: 0,
    selected: 0,
    answers: {} as Record<string, string | string[]>,
    customText: "",
    inputMode: false,
    confirmMode: false,
  })

  let scrollBox: ScrollBoxRenderable
  let customInput: InputRenderable

  const currentQ = createMemo(() => questionsWithCustom()[store.current])
  const currentAnswer = createMemo(() => {
    const q = currentQ()
    if (!q) return undefined
    return store.answers[q.id]
  })

  const isOptionSelected = (value: string) => {
    const answer = currentAnswer()
    if (!answer) return false
    if (Array.isArray(answer)) return answer.includes(value)
    return answer === value
  }

  const currentOption = createMemo(() => currentQ()?.options[store.selected])

  const canSubmit = createMemo(() => {
    for (const q of questions()) {
      if (q.required !== false && store.answers[q.id] === undefined) return false
    }
    return true
  })

  const getAnswerDisplay = (q: AskUserQuestion) => {
    const answer = store.answers[q.id]
    if (!answer) return ""
    if (!q.options) return String(answer)
    if (Array.isArray(answer)) {
      return answer.map((a) => q.options.find((o) => o.value === a)?.label ?? a).join(", ")
    }
    return q.options.find((o) => o.value === answer)?.label ?? String(answer)
  }

  onMount(() => {
    dialog.setSize("medium")
  })

  function selectOption(option: AskUserOption, customValue?: string) {
    const q = currentQ()
    if (!q) return

    const value = customValue || option.value

    if (q.type === "multiple") {
      const current = (store.answers[q.id] as string[]) || []
      const newAnswer = current.includes(value) ? current.filter((v) => v !== value) : [...current, value]
      setStore("answers", q.id, newAnswer.length > 0 ? newAnswer : (undefined as any))
    } else {
      setStore("answers", q.id, value)
    }
  }

  function goToQuestion(idx: number) {
    if (idx < 0 || idx >= total()) return
    setStore("current", idx)
    setStore("selected", 0)
    setStore("inputMode", false)
    setStore("customText", "")
    scrollBox?.scrollTo(0)
  }

  function moveOption(dir: number) {
    if (store.inputMode) return
    const q = currentQ()
    if (!q) return
    let next = store.selected + dir
    if (next < 0) next = q.options.length - 1
    if (next >= q.options.length) next = 0
    setStore("selected", next)
  }

  function submit() {
    if (!canSubmit()) return
    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: { permissionID: props.permission.id, id: props.sessionID },
        body: { response: { type: "once", answers: store.answers } as any },
      })
      .catch((e) => console.error("Failed to submit:", e))
  }

  function cancel() {
    sdk.client
      .postSessionIdPermissionsPermissionId({
        path: { permissionID: props.permission.id, id: props.sessionID },
        body: { response: "reject" },
      })
      .catch((e) => console.error("Failed to cancel:", e))
    // Note: dialog.clear() is NOT called here - the global escape handler
    // in dialog.tsx handles closing the dialog to prevent double-clear race conditions
  }

  useKeyboard((evt) => {
    // In confirm mode
    if (store.confirmMode) {
      if (evt.name === "return") {
        submit()
        evt.preventDefault()
        return
      }
      if (evt.name === "escape" || evt.name === "n") {
        setStore("confirmMode", false)
        evt.preventDefault()
        return
      }
      // y also confirms
      if (evt.name === "y") {
        submit()
        evt.preventDefault()
        return
      }
      evt.preventDefault()
      return
    }

    // In input mode, handle escape and return specially
    if (store.inputMode) {
      if (evt.name === "escape") {
        setStore("inputMode", false)
        setStore("customText", "")
        evt.preventDefault()
        return
      }
      if (evt.name === "return") {
        // Confirm custom text
        if (store.customText.trim()) {
          const option = currentOption()
          if (option) selectOption(option, store.customText.trim())
        }
        setStore("inputMode", false)
        setStore("customText", "")
        // After custom input, advance or confirm
        if (store.current < total() - 1) {
          goToQuestion(store.current + 1)
        } else {
          setStore("confirmMode", true)
        }
        evt.preventDefault()
        return
      }
      // Let input handle other keys
      return
    }

    if (evt.name === "up" || (evt.ctrl && evt.name === "p")) {
      moveOption(-1)
      evt.preventDefault()
      return
    }
    if (evt.name === "down" || (evt.ctrl && evt.name === "n")) {
      moveOption(1)
      evt.preventDefault()
      return
    }
    // Enter to select current option AND advance (or Space to just toggle for multi-select)
    if (evt.name === "return") {
      const option = currentOption()
      if (!option) return
      // Custom option - enter input mode
      if (option.allowCustom) {
        setStore("inputMode", true)
        setTimeout(() => customInput?.focus(), 1)
        evt.preventDefault()
        return
      }
      // Regular option - select it and advance
      selectOption(option)
      // For single-select: advance to next question or confirm
      if (currentQ()?.type !== "multiple") {
        if (store.current < total() - 1) {
          goToQuestion(store.current + 1)
        } else {
          setStore("confirmMode", true)
        }
      }
      evt.preventDefault()
      return
    }
    // Space just toggles selection (useful for multi-select)
    if (evt.name === "space") {
      const option = currentOption()
      if (!option) return
      if (option.allowCustom) {
        setStore("inputMode", true)
        setTimeout(() => customInput?.focus(), 1)
        evt.preventDefault()
        return
      }
      selectOption(option)
      evt.preventDefault()
      return
    }
    // Right arrow to advance to next question (or confirm on last)
    if (evt.name === "right") {
      if (store.current < total() - 1) {
        goToQuestion(store.current + 1)
      } else if (canSubmit()) {
        setStore("confirmMode", true)
      }
      evt.preventDefault()
      return
    }
    // Left arrow to go back
    if (evt.name === "left") {
      if (store.current > 0) {
        goToQuestion(store.current - 1)
      }
      evt.preventDefault()
      return
    }
    // Number keys 1-5 jump to questions
    if (evt.name >= "1" && evt.name <= "5") {
      const idx = parseInt(evt.name, 10) - 1
      if (idx < total()) {
        goToQuestion(idx)
      }
      evt.preventDefault()
      return
    }
    // S to go to confirm mode when ready
    if (evt.name === "s" && canSubmit()) {
      setStore("confirmMode", true)
      evt.preventDefault()
      return
    }
    if (evt.name === "escape") {
      cancel()
      evt.preventDefault()
      return
    }
  })

  const maxHeight = createMemo(() => Math.max(10, Math.floor(dimensions().height * 0.5)))

  return (
    <box flexDirection="column" padding={1} paddingLeft={2} paddingRight={2}>
      {/* Header */}
      <box flexDirection="row" justifyContent="space-between" paddingBottom={1}>
        <text attributes={TextAttributes.BOLD} fg={theme.primary}>
          {props.permission.title}
        </text>
        <text fg={theme.textMuted}>esc</text>
      </box>

      {/* Context */}
      <Show when={context()}>
        <box paddingBottom={1}>
          <text fg={theme.textMuted}>{context()}</text>
        </box>
      </Show>

      {/* Progress indicator for multiple questions */}
      <Show when={total() > 1}>
        <box flexDirection="row" gap={1} paddingBottom={1}>
          <For each={questions().slice(0, 5)}>
            {(q, idx) => {
              const answered = () => store.answers[q.id] !== undefined
              const active = () => idx() === store.current
              return (
                <text
                  fg={active() ? theme.primary : answered() ? theme.success : theme.textMuted}
                  attributes={active() ? TextAttributes.BOLD : undefined}
                >
                  {answered() ? "●" : "○"}
                </text>
              )
            }}
          </For>
          <text fg={theme.textMuted}>
            {store.current + 1}/{total()}
          </text>
        </box>
      </Show>

      {/* Question text */}
      <Show when={currentQ()}>
        <box paddingBottom={1}>
          <text fg={theme.text} attributes={TextAttributes.BOLD}>
            {currentQ()!.text}
          </text>
          <Show when={currentQ()!.type === "multiple"}>
            <text fg={theme.textMuted}> (multi-select)</text>
          </Show>
        </box>

        {/* Options */}
        <scrollbox
          ref={(r: ScrollBoxRenderable) => (scrollBox = r)}
          maxHeight={maxHeight()}
          scrollbarOptions={{ visible: false }}
        >
          <For each={currentQ()!.options}>
            {(option, idx) => {
              const selected = () => isOptionSelected(option.value)
              const highlighted = () => idx() === store.selected
              const isCustom = () => option.allowCustom === true
              const showInput = () => isCustom() && store.inputMode && highlighted()
              return (
                <box
                  flexDirection="column"
                  paddingLeft={1}
                  paddingRight={1}
                  backgroundColor={highlighted() && !showInput() ? theme.primary : undefined}
                >
                  <box flexDirection="row" gap={1}>
                    <text
                      fg={
                        highlighted() && !showInput() ? theme.background : selected() ? theme.success : theme.textMuted
                      }
                    >
                      {currentQ()!.type === "multiple" ? (selected() ? "[x]" : "[ ]") : selected() ? ">" : " "}
                    </text>
                    <Show
                      when={!showInput()}
                      fallback={
                        <box flexGrow={1} backgroundColor={theme.backgroundPanel}>
                          <input
                            ref={(r: InputRenderable) => (customInput = r)}
                            placeholder="type here..."
                            onInput={(v) => setStore("customText", v)}
                            focusedBackgroundColor={theme.backgroundPanel}
                            focusedTextColor={theme.text}
                            cursorColor={theme.primary}
                            flexGrow={1}
                          />
                        </box>
                      }
                    >
                      <text
                        fg={highlighted() ? theme.background : selected() ? theme.success : theme.text}
                        attributes={selected() || highlighted() ? TextAttributes.BOLD : undefined}
                      >
                        {option.label}
                        {isCustom() && highlighted() ? " (enter to type)" : ""}
                      </text>
                    </Show>
                  </box>
                  <Show when={option.description && !showInput()}>
                    <text fg={highlighted() ? theme.background : theme.textMuted} paddingLeft={4}>
                      {option.description}
                    </text>
                  </Show>
                </box>
              )
            }}
          </For>
        </scrollbox>
      </Show>

      {/* Answers summary - shows all answered questions */}
      <Show when={Object.keys(store.answers).length > 0}>
        <box
          flexDirection="column"
          paddingTop={1}
          borderStyle="single"
          borderColor={theme.border}
          border={["top"]}
          marginTop={1}
        >
          <text fg={theme.textMuted} paddingBottom={1}>
            Answers:
          </text>
          <box flexDirection="row" flexWrap="wrap" gap={1}>
            <For each={questions().slice(0, 5)}>
              {(q, idx) => {
                const answer = () => store.answers[q.id]
                const active = () => idx() === store.current
                return (
                  <Show when={answer()}>
                    <box
                      paddingLeft={1}
                      paddingRight={1}
                      backgroundColor={active() ? theme.primary : theme.backgroundPanel}
                    >
                      <text fg={active() ? theme.background : theme.text}>
                        <span style={{ fg: active() ? theme.background : theme.textMuted }}>{idx() + 1}. </span>
                        {getAnswerDisplay(q)}
                      </text>
                    </box>
                  </Show>
                )
              }}
            </For>
          </box>
        </box>
      </Show>

      {/* Confirm mode overlay */}
      <Show when={store.confirmMode}>
        <box
          flexDirection="column"
          paddingTop={1}
          borderStyle="single"
          borderColor={theme.success}
          border={["top"]}
          marginTop={1}
        >
          <text fg={theme.success} attributes={TextAttributes.BOLD}>
            Confirm your answers?
          </text>
          <box flexDirection="row" gap={2} paddingTop={1}>
            <text>
              <span style={{ fg: theme.success, attributes: TextAttributes.BOLD }}>enter/y</span>
              <span style={{ fg: theme.text }}> confirm</span>
            </text>
            <text>
              <span style={{ fg: theme.textMuted }}>esc/n</span>
              <span style={{ fg: theme.text }}> back</span>
            </text>
          </box>
        </box>
      </Show>

      {/* Footer */}
      <Show when={!store.confirmMode}>
        <box flexDirection="row" justifyContent="space-between" paddingTop={1}>
          <box flexDirection="row" gap={2}>
            <text>
              <span style={{ fg: theme.textMuted }}>↑↓</span>
              <span style={{ fg: theme.text }}> move</span>
            </text>
            <text>
              <span style={{ fg: theme.primary }}>enter</span>
              <span style={{ fg: theme.text }}> select</span>
            </text>
            <Show when={total() > 1}>
              <text>
                <span style={{ fg: theme.textMuted }}>←→</span>
                <span style={{ fg: theme.text }}> prev/next</span>
              </text>
            </Show>
          </box>
          <Show
            when={canSubmit()}
            fallback={<text fg={theme.textMuted}>{total() - Object.keys(store.answers).length} remaining</text>}
          >
            <text fg={theme.success} attributes={TextAttributes.BOLD}>
              s confirm
            </text>
          </Show>
        </box>
      </Show>
    </box>
  )
}
