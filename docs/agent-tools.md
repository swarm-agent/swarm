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

## Owner action grants

Tool enablement allows reads, not actions. The owner must add an account
permission rule of kind `tool`, decision `allow`, with the exact tool key:

```
manage_jobs.<agent-name>.rerun_failed_steps
manage_jobs.<agent-name>.give_job
manage_jobs.<agent-name>.message_job
```

For example, `manage_jobs.daily-watch.message_job` permits only that action
for `daily-watch`. A generic `manage_jobs` allow, permission bypass, or an
allow for another agent does not substitute for this grant. A matching deny
(or a `manage_jobs` deny) wins. Use the existing owner permission-rule API;
this tool cannot change grants. Normal tool permission checks still apply.

Self-targeting is refused even with a grant: an agent cannot message/rerun
its own job, start its own worker (including by name), or message/rerun the
job whose workflow delivery contains it as a step or fan-out item.
