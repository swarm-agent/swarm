# Provider manual test matrix

Use this document to run provider checks one case at a time. Update each row in place as testing proceeds; do not infer a provider result from another provider.

## Status values

- `NOT STARTED` — no manual test has been run.
- `IN PROGRESS` — test started but evidence or a final result is incomplete.
- `PASS` — observed behavior matched the check.
- `FAIL` — observed behavior did not match the check; record the issue and reproduction evidence.
- `BLOCKED` — an external requirement prevented the check; record the exact requirement.
- `N/A` — the provider intentionally does not support the behavior; record the authoritative reason.

## Execution order

| Order | Provider ID | Provider | Overall status | Last updated | Summary evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| **1** | `anthropic` | **Anthropic — start here** | `NOT STARTED` | — | — |
| 2 | `codex` | Codex | `NOT STARTED` | — | — |
| 3 | `openai` | OpenAI | `NOT STARTED` | — | — |
| 4 | `google` | Google | `NOT STARTED` | — | — |
| 5 | `fireworks` | Fireworks | `NOT STARTED` | — | — |
| 6 | `openrouter` | OpenRouter | `NOT STARTED` | — | — |

The list reflects the launch AI providers registered in `swarmd/internal/runtime/daemon.go` and exposed by provider onboarding defaults. Exa is a tool integration rather than an AI session runner. Copilot is intentionally dormant and excluded until its paid-plan flow can be validated end to end.

## 1. Anthropic (`anthropic`) — first provider

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| A1 | Onboarding | Complete Anthropic setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| A2 | Regular flow | Start a normal session, send a prompt, receive a complete response, and continue with a second turn. | `NOT STARTED` | — | — |
| A3 | Plan → action | Enter plan mode, create and approve an actionable plan, then confirm execution transitions to action/auto mode and completes the selected checkpoint. | `NOT STARTED` | — | — |
| A4 | `/task` | Launch a standard `/task`; confirm the child run starts, returns a durable result, and the parent receives the handoff. | `NOT STARTED` | — | — |
| A5 | `/task plan` | Launch `/task plan`; confirm the child follows the plan-oriented path and returns its durable result/handoff. | `NOT STARTED` | — | — |
| A6 | `/new` | Create a new session with `/new`; confirm a distinct durable session opens with the expected provider/model selection. | `NOT STARTED` | — | — |
| A7 | Priority | Select Anthropic priority service tier, run a request, and confirm the preference is preserved and applied without falling back or being rejected. | `FAILED` | 2026-08-04 / Swarm | The profile and provider diagnostic preserved `priority`; Anthropic mapping sent `service_tier=auto`; API returned HTTP 200, but authoritative Anthropic usage returned `service_tier=standard`. Accepted, but served on standard rather than priority capacity. |

## 2. Codex (`codex`)

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| C1 | Onboarding | Complete Codex setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| C2 | Regular flow | Start a normal session, complete an initial prompt, and continue with a second turn. | `NOT STARTED` | — | — |
| C3 | Plan → action | Create and approve an actionable plan, then confirm the selected checkpoint executes and completes. | `NOT STARTED` | — | — |
| C4 | `/task` | Launch a standard `/task`; confirm durable child execution and parent handoff. | `NOT STARTED` | — | — |
| C5 | `/task plan` | Launch `/task plan`; confirm the plan-oriented child path and durable handoff. | `NOT STARTED` | — | — |
| C6 | `/new` | Create a distinct durable session and confirm the expected provider/model selection. | `NOT STARTED` | — | — |
| C7 | Priority | Select the provider's priority/fast service option, run a request, and confirm the resolved tier is applied or that an intentional unsupported result is clearly surfaced. | `NOT STARTED` | — | — |

## 3. OpenAI (`openai`)

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| O1 | Onboarding | Complete OpenAI setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| O2 | Regular flow | Start a normal session, complete an initial prompt, and continue with a second turn. | `NOT STARTED` | — | — |
| O3 | Plan → action | Create and approve an actionable plan, then confirm the selected checkpoint executes and completes. | `NOT STARTED` | — | — |
| O4 | `/task` | Launch a standard `/task`; confirm durable child execution and parent handoff. | `NOT STARTED` | — | — |
| O5 | `/task plan` | Launch `/task plan`; confirm the plan-oriented child path and durable handoff. | `NOT STARTED` | — | — |
| O6 | `/new` | Create a distinct durable session and confirm the expected provider/model selection. | `NOT STARTED` | — | — |
| O7 | Priority | Select the provider's priority/fast service option, run a request, and confirm the resolved tier is applied or that an intentional unsupported result is clearly surfaced. | `NOT STARTED` | — | — |

## 4. Google (`google`)

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| G1 | Onboarding | Complete Google setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| G2 | Regular flow | Start a normal session, complete an initial prompt, and continue with a second turn. | `NOT STARTED` | — | — |
| G3 | Plan → action | Create and approve an actionable plan, then confirm the selected checkpoint executes and completes. | `NOT STARTED` | — | — |
| G4 | `/task` | Launch a standard `/task`; confirm durable child execution and parent handoff. | `NOT STARTED` | — | — |
| G5 | `/task plan` | Launch `/task plan`; confirm the plan-oriented child path and durable handoff. | `NOT STARTED` | — | — |
| G6 | `/new` | Create a distinct durable session and confirm the expected provider/model selection. | `NOT STARTED` | — | — |
| G7 | Priority | Select the provider's priority/fast service option, run a request, and confirm the resolved tier is applied or that an intentional unsupported result is clearly surfaced. | `NOT STARTED` | — | — |

## 5. Fireworks (`fireworks`)

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| F1 | Onboarding | Complete Fireworks setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| F2 | Regular flow | Start a normal session, complete an initial prompt, and continue with a second turn. | `NOT STARTED` | — | — |
| F3 | Plan → action | Create and approve an actionable plan, then confirm the selected checkpoint executes and completes. | `NOT STARTED` | — | — |
| F4 | `/task` | Launch a standard `/task`; confirm durable child execution and parent handoff. | `NOT STARTED` | — | — |
| F5 | `/task plan` | Launch `/task plan`; confirm the plan-oriented child path and durable handoff. | `NOT STARTED` | — | — |
| F6 | `/new` | Create a distinct durable session and confirm the expected provider/model selection. | `NOT STARTED` | — | — |
| F7 | Priority | Select the provider's priority/fast service option, run a request, and confirm the resolved tier is applied or that an intentional unsupported result is clearly surfaced. | `NOT STARTED` | — | — |

## 6. OpenRouter (`openrouter`)

| # | Scenario | Manual check | Status | Date / tester | Evidence / notes |
| ---: | --- | --- | --- | --- | --- |
| R1 | Onboarding | Complete OpenRouter setup; confirm credentials are saved, status becomes ready, and the configured default assignments are usable. | `NOT STARTED` | — | — |
| R2 | Regular flow | Start a normal session, complete an initial prompt, and continue with a second turn. | `NOT STARTED` | — | — |
| R3 | Plan → action | Create and approve an actionable plan, then confirm the selected checkpoint executes and completes. | `NOT STARTED` | — | — |
| R4 | `/task` | Launch a standard `/task`; confirm durable child execution and parent handoff. | `NOT STARTED` | — | — |
| R5 | `/task plan` | Launch `/task plan`; confirm the plan-oriented child path and durable handoff. | `NOT STARTED` | — | — |
| R6 | `/new` | Create a distinct durable session and confirm the expected provider/model selection. | `NOT STARTED` | — | — |
| R7 | Priority | Select the provider's priority/fast service option, run a request, and confirm the resolved tier is applied or that an intentional unsupported result is clearly surfaced. | `NOT STARTED` | — | — |

## Evidence convention

For each completed row, replace the placeholders with:

- the UTC date and tester;
- the tested model, thinking level, and service tier;
- the relevant session URL or durable session ID;
- a short observed-result note;
- an issue link or exact blocker when the result is `FAIL` or `BLOCKED`.

Run the rows sequentially, beginning with **A1 (Anthropic onboarding)**. Provider tests are deliberately not executed as part of creating this matrix.
