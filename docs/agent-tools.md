# Native agent tools: jobs

`manage_jobs` lets Swarm and custom agents inspect the same jobs as Swarm
Control, without an MCP key. Custom agents must explicitly include
`manage_jobs` in their tool contract. Calls use the running session's principal
and account; arguments cannot select another identity.

| Action | Inputs | Behavior |
| --- | --- | --- |
| `list_runs` | `since`, `until` (RFC 3339), `status` array, `worker`, `limit`, `cursor` | Shared `swarm_list_runs` handler; newest runs, with its continuation cursor. |
| `get_job` | `job_id` | Shared `swarm_get_job` handler, including workflow steps and blocked reasons. |
| `deliveries` | `id` (workflow ID), optional `job_id`, `limit` | Latest deliveries, using the same delivery views as Control, including states, reasons and attempts. |
| `rerun_failed_steps` | `job_id` | Shared `swarm_deliver_job automation=true`; not Git integration. |
| `give_job` | `worker`, `brief`, `project`, optional `folder`, `idempotency_key` | Shared `swarm_give_job`; starts an existing worker. |
| `message_job` | `job_id`, `message`, optional `client_request_id` | Shared `swarm_message_job`; follow up or reopen a finished job. |

Limits are 1–50 (default 50). Oversized reads return `json_page`,
`page_digest`, and `next_page_offset`. Repeat the identical read with
`page_offset` set to that offset and the returned `page_digest`; concatenate
JSON text pages. A changed result refuses continuation: restart at zero.
Each response stays below 64 KiB; no host spill files are created. Large
mutation responses tell the agent to read the job/deliveries, not repeat the
mutation.

For `deliveries`, `id` accepts a workflow ID or name within the session's
account. Unknown references return an error; omit `id` to leave workflows unfiltered.

## Owner action grants

Tool enablement allows reads, not actions. The owner must add an account
permission rule of kind `tool`, decision `allow`, with the exact tool key:

```
manage_jobs.<agent-name>.rerun_failed_steps
manage_jobs.<agent-name>.give_job
manage_jobs.<agent-name>.message_job
```

For workers, `<agent-name>` is the custom agent name when configured; otherwise
use `worker_<worker-id>` (the `worker_` prefix followed by the full worker ID).
Shared base agents such as `system-worker-coder` never grant worker actions.
Non-worker sessions use their session agent name.

For example, `manage_jobs.daily-watch.message_job` permits that action for
workers using custom agent `daily-watch`. For a worker without a custom agent
whose ID is `example-worker`, use `manage_jobs.worker_example-worker.give_job`.
The refusal message gives the exact required key. A generic `manage_jobs` allow, permission bypass, or an
allow for another agent does not substitute for this grant. A matching deny
(or a `manage_jobs` deny) wins. Use the existing owner permission-rule API;
this tool cannot change grants. Normal tool permission checks still apply.

Self-targeting is refused even with a grant: an agent cannot message/rerun
its own job, start its own worker (including by name), or message/rerun the
job whose workflow delivery contains it as a step or fan-out item.

# Native agent tools: background commands (`bash`)

Long commands an agent starts with `bash` are not lost when they outrun the
call's time limit. This applies to every Swarm agent in a conversation.

- **At the limit.** A command still running when the call reaches
  `timeout_ms` moves to the background instead of being stopped. The call
  answers with `backgrounded: true`, `status: "running"`, a `background_id`,
  and the output so far. It carries no `exit_code`: the command is neither a
  pass nor a failure yet.
- **Explicitly.** `background: true` starts the command and returns its
  `background_id` at once. The agent keeps working.
- **Report back.** When a background command ends, its exit code and its
  retained output, bounded by the tool output cap below, are added to the
  conversation that started it. A conversation that is running sees the update on its next step. An idle
  conversation is woken by it (a new run starts). If the run ends before the
  update reaches the model, the update wakes the conversation again. A task's
  conversation is never restarted outside its task lifecycle: there the update
  is recorded without waking it.
- **Read or stop by id.** Call `bash` with `background_id` and
  `command: "status"` to read its state (`running`, `exited`, `timed_out`,
  `killed`, `failed`), exit code and retained output, or `command: "kill"` to
  stop it. A killed command does not report back.

Limits:

| Limit | Value |
| --- | --- |
| Running background commands per conversation | 4 |
| Running background commands per account | 16 |
| Retained output per command | last 256 KiB; earlier output dropped, `truncated: true` |
| Longest run of a background command | 4 hours, then stopped and reported as timed out |
| Finished commands kept for reading by id | 16 per conversation |

Over a cap, `background: true` is refused with the reason and nothing starts.
A command that reaches its limit over a cap is stopped as before
(`timed_out: true`, with `background_error` giving the reason).

Stopping a conversation's run, or archiving or deleting the conversation,
kills its background commands and their processes (host, sandbox or lease).
None of them reports back. A background command keeps its lease while it
runs. Nothing crosses conversations: another conversation, even of the same
account, cannot read, kill or receive one.

Background commands live in the daemon's memory. A daemon restart stops them
without a report. They are not available for `committed_source` validation (a
validation passes only when it ends), for disposable reviews, or in managed
execution; those stay foreground and report `timed_out` at the limit.

# Native agent tools: tool output cap

One setting bounds the text a single tool result gives the model, so a long
log is not re-sent to the model on every later turn. It is
`tool_output_cap_bytes` in the daemon's startup config (`0`, the default,
means 32768 bytes; otherwise 4096 to 1048576). An invalid value stops the
daemon at startup.

| Tool | Over the cap |
| --- | --- |
| `bash` (foreground, moved to the background, `command: "status"`, and the report when a background command ends) | Keeps the head and the tail of the output around `[N bytes omitted; full output saved to <path>; read it with read/search]`. The result adds `output_capped`, `output_original_bytes`, `output_omitted_bytes` and `full_output_path` (or `full_output_error` when it could not be saved). |
| `read` without `max_lines` | Stops at the last whole line within the cap, with `truncated`, `output_capped` and `next_line_start` to continue. An explicit `max_lines` (up to 2000) is the caller's own bound. |
| `search` | Keeps the leading results that fit, with `output_omitted_results` and the full answer saved at `full_output_path`. |
| `webfetch`, `websearch` contents | Each page's text is limited to `text.max_characters` (Exa's `maxCharacters` spelling is accepted too; giving both with different values is refused). Without one, each page gets an equal share of the cap (at least 1000 characters). The limit is sent to Exa and applied again to the returned text (`text_truncated: true`). |

Full outputs are saved in the workspace under `.swarm/tool-output/` (files
mode 0600, the newest 50 kept). The directory holds a `.gitignore` of `*`,
so saved output never shows up as a change or gets committed. Each directory
level is opened as an `os.Root` and must be a real directory (not a symlink);
writes, listing and pruning happen through the opened handles, so a level
swapped for a symlink mid-save cannot redirect them, and the save is refused
(the written file removed) when the path no longer leads to the directory
written in. In managed execution, `bash` output over the cap is not saved:
the marker reads `[N bytes omitted; full output unavailable: ...]`, with
`full_output_error` and no `full_output_path`, because `read` runs in the
lease checkout and could not reach a host file.
Command output past the 4 MiB in-memory capture keeps its end, so the saved
file and the tail show how the command finished; for a background command
the saved file holds its retained output (the last 256 KiB).
