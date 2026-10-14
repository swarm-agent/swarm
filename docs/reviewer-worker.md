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

Reviewer results are report-only. MCP delivery and the direct V3 task
integration/recovery-integration routes refuse reviewer tasks. Task detail shows
`delivery_assessment.state: "report_only"` and no delivery actions. Once the
executor acknowledges termination, the reviewer container is closed and dirty
checkout/build outputs are removed; no commits/files are delivered.

Verdict recording, review events, stale-head evidence and the dedicated role
model setting are a separate follow-up. No landing review gate is imposed.

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
