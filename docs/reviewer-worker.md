# Reviewer jobs

Use the box's `reviewer` worker to independently review another worker job.
Give a brief and `review_job: "worker/<worker>/<run>"`. The referenced job must
exist in the same account, project and workspace and have commits beyond its
captured base. Other roles reject `review_job`; reviewers require it.

Public surfaces:
- `POST /v3/workers/{worker_id}/direct`: optional top-level `review_job` string.
- SDK `client.workers.directRequest`: `DirectWorkerRequestParams.review_job`.
- MCP `swarm_give_job`: `review_job` string, required for reviewer workers.

Admission captures the reviewed job's immutable current head and creates a
**detached** disposable linked worktree at that commit, without a branch.
Retries preserve the original captured head. The job message includes the
reviewed brief and latest attempt report, labeled untrusted evidence (bounded
to 24 KB each). Commands receive `SWARM_REVIEW_HEAD`, `SWARM_REVIEW_BASE` and
`SWARM_REVIEW_JOB`. The base is `refs/swarm/publish/<base>` if that commit ref
exists, otherwise the captured landing branch. Reviews do not fetch or advance
the landing target. A later source commit does not change an existing review.

## Folder checks

Admission also captures `checks` and `check_timeout_minutes` from the source
folder's account-owned `/v3/sandbox-environments` definition, never the job
brief. `SWARM_REVIEW_CHECKS` runs those commands in order, each in its own
`bash -lc` shell, stopping at the first failure, just like landing.
`SWARM_REVIEW_CHECKS_TIMEOUT_SECONDS` supplies the total budget (20 minutes
when unspecified). Captured values survive retries; later definition changes
do not rewrite an existing review.

The compiled reviewer instruction runs these checks in its disposable checkout
**before** narrow tests, allowing the folder command to install dependencies.
Failure or timeout requires `VERDICT: BLOCK` and quoted failing output in
`TESTS RUN`. Without checks, both variables are empty; the reviewer explicitly
reports no folder checks and runs narrow tests. This is a reviewer instruction,
not a daemon-enforced proof that a model executed the gate. Landing still runs
its own checks against its then-current definition and rebased head.

## Sandbox boundary

The compiled `system-reviewer` profile uses read runtime mode and only read,
search, find, list, websearch, webfetch and bash. Shared project and worktree
bucket mounts are read-only in a separate reviewer container/spec. Only its
checkout is writable for build outputs; its `.git` pointer and the full common
Git directory are overmounted read-only. Objects, refs, packed refs, config,
hooks and worktree metadata are protected. `GIT_OPTIONAL_LOCKS=0` supports
observational Git. The original project selects the environment image and
secret broker policy; the coder container is unchanged. Exec and container
startup preserve `withSandboxUmask`. Reviewer home/cache volumes are separate.
Project roots with a directory `.git` are supported; foreign common Git dirs
are rejected. Sandbox registration is restored from immutable session evidence
before execution after restart. No sandbox means refusal, never host fallback.

Execution isolation is bound to the daemon-registered session identity, not a
caller-supplied cwd: Bash workspace overrides outside the checkout are refused,
routed Git and supervised exec preserve the review scope, and host file/search
tools see only the checkout. Terminal cleanup keeps registration restrictive
until removal succeeds; failed removal can be retried without a writable lane.
Verdict sections must be unique, ordered and non-empty; quoted or ambiguous
structure fails closed. Auto-publication skips report-only jobs rather than
holding the signal cursor, and publication/landing preparation refuse them.

Reviewer results are report-only. MCP delivery and the direct V3 task
integration/recovery-integration routes refuse reviewer tasks. Task detail shows
`delivery_assessment.state: "report_only"` and no delivery actions. Once the
executor acknowledges termination, the reviewer container is closed and dirty
checkout/build outputs are removed; no commits/files are delivered.

On completion the daemon parses the final assistant report and durably records
`{verdict, head, reviewer_job_id, model, at, reason}` on the reviewed worker job.
Missing/malformed reports and failed/stopped reviewers record `block`. Evidence
is latest-per-head; terminal replay does not overwrite a newer review. V3 worker
run detail, SDK `workers.getRun`, and MCP `swarm_get_job` expose the latest
`review` with `stale` computed against the job's current Git HEAD. An unavailable
checkout is stale, never a valid approval. Existing review lifecycle fields are
preserved for compatibility. `WorkerStore.CurrentValidJobReview` is the exported
exact-head hook for the future landing policy (it returns BLOCK evidence too).

Recording emits an account-scoped `job.reviewed` signal with verdict/head and
job IDs through the existing best-effort signal observer pattern; report/reason
text is not forwarded. The feed is consumed by signal sinks and swarm-watch.

`swarm_set_agent_model` accepts role `system-reviewer`; the canonical settings
PATCH accepts `system_agents.reviewer`. Fresh defaults use the provider catalog's
`review` recommendation, or the coder assignment if absent. Existing settings
without the optional reviewer slot resolve catalog review/coder defaults;
explicit reviewer assignments take precedence. No model/provider is hardcoded.
No landing review gate is imposed.

## Docker enforcement proof

Run as non-root with the configured Git/Go sandbox image installed:

```sh
cd swarmd
go test ./internal/sandbox -run '^TestReviewerDockerReadOnlyGit$' -count=1 -v -timeout=4m
```

`SWARM_REVIEW_TEST_IMAGE` optionally selects a locally installed Git/Go image.
The test skips if Docker is absent/unreachable and does not fetch an image.
It proves commit/update-ref/direct metadata writes fail while diff/log and
Go tests succeed, then verifies unchanged host state. Hermetic admission tests
use a deterministic engine fixture with real Git and prove source identity,
exact-head checkout, retry, context and cleanup, not kernel mount enforcement
or provider-backed review quality.

## Require review before landing

The headless installer can make independent review mandatory for a folder:

```sh
install.sh workspace publish FOLDER COMMAND --land --require-review --auto
```

`require-review` is stored in the root-owned publish config; `get_project`
shows `publish.require_review` on each configured workspace. It requires
`--land`; omitting it preserves existing delivery behavior.

Delivery needs a recorded `ok` verdict for the job's current exact head. Missing,
BLOCK, or stale evidence refuses delivery with `delivery_reason` naming the head
and asking you to give the reviewer `review_job`. With `--auto`, a durable OK
`job.reviewed` signal triggers another delivery attempt; unreviewed jobs do not
hold up other jobs' signals. Jobs without worker review evidence cannot land
under this policy.

A clean rebase may carry approval forward within landing preparation only when
its whole-job stable Git patch-id is unchanged. Conflicted rebases are aborted
and sent back; commit the resolution and request a fresh exact-head review.
Checks that change HEAD or leave uncommitted changes still refuse landing.
The queue durably checkpoints the reviewed head, rebased head, patch-id and
review identity, so unchanged-patch retries survive MOVED receipts and restart.
A BLOCK on the rebased head or another reviewed head with the same patch-id
wins over carried approval. Automatic publishing also scans durable OK evidence
at startup and periodically in bounded pages; a lost review signal cannot
permanently strand an approved job.

## Problem proofs

When a job declares `proof`, review whether its reproduction tests the problem
in the brief, not an easier substitute. Read `proof.base` and `proof.head` from
`swarm_get_job`: these contain daemon-observed exit codes, bounded output and
exact commits. Missing results mean verification has not happened, not success.
Landing requires non-zero at base and zero at head. Infrastructure errors do
not count as a reproduced failure. A coder can commit `.swarm-proof.json` with
`command` and `files` (new reproduction scripts copied unchanged to base).
Inspect those scripts as carefully as the fix; the daemon cannot judge whether
a command's assertion faithfully represents the brief.
