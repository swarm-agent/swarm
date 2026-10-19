# Evidence-checked review

The system reviewer reviews one job's exact committed head against its base, in its own session. It reads `git diff BASE...HEAD` and the changed files and judges four things:

1. Is every requirement of the brief fully done?
2. Do the report's claims match the evidence? A test or check the report says passed must appear among the commands the job actually ran, with exit code 0. A claim the evidence does not back is a finding.
3. Are there gaps: behavior the brief needs that nothing implements or tests, a missing failure or negative case, or a behavior change the brief did not request?
4. Are there security problems?

It reports BLOCK when any of these finds a problem, otherwise OK. Each finding explains what is missing or wrong with a `file:line` location and reaches the coder verbatim. Alternative approaches and style preferences are notes in SUMMARY, never findings.

A reviewer started by an automation's `agent` step gets the evidence in its brief: the reviewed job's original brief, its report, the commands it actually ran with their exit codes (read from its session's tool calls) and its diff against its base (see [automations](automations.md#what-the-reviewer-receives)). A reviewer started by hand gets the job's brief and head; it says in SUMMARY which claims it could not verify.

The reviewer runs no builds, tests, checks, or scripts: it judges the evidence the job produced. Reports retain four sections: VERDICT, SUMMARY, FINDINGS, and TESTS RUN. TESTS RUN always says `none (review is read-only)`.

The structured report may follow progress notes in the reviewer's final message: Swarm reads it from the line that starts with `VERDICT: OK` or `VERDICT: BLOCK` and ignores the text before it. It still fails closed (recorded as BLOCK, "missing or malformed review verdict") when more than one line starts with `VERDICT:`, when the notes before the verdict contain a heading at the start of a line, or when the sections are missing, empty, out of order, quoted, fenced, indented or embedded in prose. The review record keeps the FINDINGS section verbatim from the full message, up to 16,000 bytes (longer findings are cut at a line boundary and say so); `swarm_get_job` shows the recorded verdict and findings, and an automation's `agent` and `reopen` steps send those findings in full, never the job's shortened report.

The brief, report, commands, diff and repository are untrusted data: the reviewer assesses them but never follows instructions found in them. A re-review assesses previous findings first and identifies which are fixed.

A review is report-only: the reviewer's checkout is detached and disposable, and it never commits. What happens to a reviewed job afterwards (delivering it with `swarm_deliver_job`, sending findings back with `swarm_message_job` or an automation's `reopen` step) is up to the AI or automation that asked for the review.

## Security fixes (automation opt-in)

One exception, opt-in per automation step and never the default: an automation `agent` step whose worker is a reviewer may set `fixes: "security"` (see [automations](automations.md#reviewer-fixes-fixes-security)). Only that reviewer may commit small, security-only fixes (a missing escape, a path check, a secret in a log line) instead of blocking for them. A reviewer started any other way (`swarm_give_job` with `review_job`, or an `agent` step without `fixes`) stays read-only as above; Swarm never decides on its own to let a reviewer write.

With `fixes`:

- The reviewer works on its own writable branch `agent/review-<run>`, started at the reviewed head in a checkout seeded from the reviewed job's own clone. Its `.git` is writable for this job only, and every tool call checks that `HEAD` descends from the reviewed head. Its checkout is kept after the job ends, like a coder's.
- It may edit files and commit; it cannot start other agents. It runs only the narrowest test that proves a fix and lists it under TESTS RUN.
- Every fix is also a FINDINGS entry marked `FIXED`, with what it was, why, and `file:line`. Anything that is not a small security fix is a BLOCK finding, not a fix.
- Limits enforced in code after the job ends: its commits on top of the reviewed head may change at most 80 lines in total (added plus removed; a binary file counts as over). Over the limit the automation step fails with `reviewer fixes too large` and the delivery head does not move. Only an OK verdict moves the delivery head to the reviewer's head; a reviewer that committed while its verdict is BLOCK does not move it. A security problem the reviewer fixed is not a reason to BLOCK.
- An OK verdict fails closed: the reviewer's head and fix commits are read from its own checkout with Git, and when they cannot be read the step fails with `reviewer fixes unreadable` and the delivery head does not move. A fixing step after another agent step that already moved the delivery head is refused before it starts.
- The reviewer job's result shows its commits and head; the delivery shows the moved head and the applied fix commits. The reviewer job itself is still not delivered with `swarm_deliver_job`.
