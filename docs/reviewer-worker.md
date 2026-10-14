# Reviewer worker foundation (not yet executable)

The compiled `system-reviewer` profile uses read runtime mode and an explicit
allowlist: read, search, find, list, websearch, webfetch and bash. It has no file
mutation, Git mutation, delegation or publication tools. The `reviewer` worker
role and Reviewer preset are registered; inherited model policy currently uses
the account's coder assignment. No model is hardcoded.

## Sandbox design

`RegisterReviewCheckout` registers a daemon-owned disposable linked checkout.
Commands and routed Git calls targeting that checkout select a separate
container name/spec, leaving the coder's shared container unchanged. Project
and worktree-bucket binds are read-only; only the disposable checkout is mounted
writable. Its `.git` pointer and the project's complete `.git` directory are
then overmounted read-only, including objects, refs, packed refs, config, hooks
and linked-worktree metadata. `GIT_OPTIONAL_LOCKS=0` permits observational Git
commands without optional index refreshes. Environment image and secret broker
resolution still use the original project root. The existing exec umask helper
is unchanged. Reviewer containers have their own home volume/cache namespace.

The foundation currently supports project roots whose `.git` is a directory;
foreign Git directories are rejected. Docker-backed enforcement has not been
verified by this change's hermetic mount-spec test.

## Pending integration

Reviewer dispatch deliberately fails closed: it must not execute in the coder's
writable sandbox. There is **no public API, MCP or SDK `review_job` field yet**.
The internal request validator recognizes `review_job` (a
`worker/<worker>/<run>` ID) or `review_ref` (branch/commit), only for reviewer
workers, and rejects missing or ambiguous targets.

Completion requires authorized exact-head resolution, detached checkout
allocation, restart-safe sandbox registration, review context/environment
injection, report-only delivery refusal, terminal checkout cleanup, and an atomic
review record plus job-event signal on the reviewed job. The pure
`ParseJobReview` helper fails closed for BLOCK, missing/malformed or duplicate
verdicts and missing report sections. `CurrentJobReview` returns evidence only
for an identical head, providing the future landing-policy hook; neither helper
is currently wired to durable job state. The queue does not enforce reviews.
