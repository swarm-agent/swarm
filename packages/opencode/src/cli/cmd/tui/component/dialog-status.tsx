import { TextAttributes } from "@opentui/core"
import { useTheme } from "../context/theme"
import { useSync } from "@tui/context/sync"
import { For, Match, Switch, Show, createMemo } from "solid-js"

export type DialogStatusProps = {}

export function DialogStatus() {
  const sync = useSync()
  const { theme } = useTheme()

  const enabledFormatters = createMemo(() => sync.data.formatter.filter((f) => f.enabled))

  return (
    <box paddingLeft={2} paddingRight={2} gap={1} paddingBottom={1}>
      <box flexDirection="row" justifyContent="space-between">
        <text fg={theme.text} attributes={TextAttributes.BOLD}>
          Status
        </text>
        <text fg={theme.textMuted}>esc</text>
      </box>
      <Show when={Object.keys(sync.data.mcp).length > 0} fallback={<text>No MCP Servers</text>}>
        <box>
          <text fg={theme.text}>{Object.keys(sync.data.mcp).length} MCP Servers</text>
          <For each={Object.entries(sync.data.mcp)}>
            {([key, item]) => (
              <box flexDirection="row" gap={1}>
                <text
                  flexShrink={0}
                  style={{
                    fg: {
                      connected: theme.success,
                      failed: theme.error,
                      disabled: theme.textMuted,
                    }[item.status],
                  }}
                >
                  •
                </text>
                <text fg={theme.text} wrapMode="word">
                  <b>{key}</b>{" "}
                  <span style={{ fg: theme.textMuted }}>
                    <Switch>
                      <Match when={item.status === "connected"}>Connected</Match>
                      <Match when={item.status === "failed" && item}>{(val) => val().error}</Match>
                      <Match when={item.status === "disabled"}>Disabled in configuration</Match>
                    </Switch>
                  </span>
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>
      {sync.data.lsp.length > 0 && (
        <box>
          <text fg={theme.text}>{sync.data.lsp.length} LSP Servers</text>
          <For each={sync.data.lsp}>
            {(item) => (
              <box flexDirection="row" gap={1}>
                <text
                  flexShrink={0}
                  style={{
                    fg: {
                      connected: theme.success,
                      error: theme.error,
                    }[item.status],
                  }}
                >
                  •
                </text>
                <text fg={theme.text} wrapMode="word">
                  <b>{item.id}</b> <span style={{ fg: theme.textMuted }}>{item.root}</span>
                </text>
              </box>
            )}
          </For>
        </box>
      )}
      <Show when={enabledFormatters().length > 0} fallback={<text fg={theme.text}>No Formatters</text>}>
        <box>
          <text fg={theme.text}>{enabledFormatters().length} Formatters</text>
          <For each={enabledFormatters()}>
            {(item) => (
              <box flexDirection="row" gap={1}>
                <text
                  flexShrink={0}
                  style={{
                    fg: theme.success,
                  }}
                >
                  •
                </text>
                <text wrapMode="word" fg={theme.text}>
                  <b>{item.name}</b>
                </text>
              </box>
            )}
          </For>
        </box>
      </Show>
      <Show when={sync.data.swarm.status !== "inactive"}>
        <box>
          <text fg={theme.text}>Swarm Session Tracker</text>
          <box flexDirection="row" gap={1}>
            <text
              flexShrink={0}
              style={{
                fg: {
                  active: theme.success,
                  failed: theme.error,
                  inactive: theme.textMuted,
                }[sync.data.swarm.status],
              }}
            >
              •
            </text>
            <text fg={theme.text}>
              <b>Status:</b>{" "}
              <span style={{ fg: theme.textMuted }}>{sync.data.swarm.status === "active" ? "Active" : "Failed"}</span>
            </text>
          </box>
          <Show when={sync.data.swarm.port}>
            <box flexDirection="row" gap={1} paddingLeft={2}>
              <text fg={theme.text}>
                <b>Port:</b> <span style={{ fg: theme.textMuted }}>{sync.data.swarm.port}</span>
              </text>
            </box>
          </Show>
          <Show when={sync.data.swarm.tmuxSession}>
            <box flexDirection="row" gap={1} paddingLeft={2}>
              <text fg={theme.text}>
                <b>Tmux Session:</b> <span style={{ fg: theme.textMuted }}>{sync.data.swarm.tmuxSession}</span>
              </text>
            </box>
          </Show>
          <Show when={sync.data.swarm.timestamp}>
            <box flexDirection="row" gap={1} paddingLeft={2}>
              <text fg={theme.text}>
                <b>Last Updated:</b>{" "}
                <span style={{ fg: theme.textMuted }}>{new Date(sync.data.swarm.timestamp!).toLocaleString()}</span>
              </text>
            </box>
          </Show>
        </box>
      </Show>
    </box>
  )
}
