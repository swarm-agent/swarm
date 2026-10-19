# Evidence-checked review

The system reviewer reviews one job's exact committed head against its base, in its own session. It reads `git diff BASE...HEAD` and the changed files and judges four things:

1. Is every requirement of the brief fully done?
2. Do the report's claims match the evidence? A test or check the report says passed must appear among the commands the job actually ran, with exit code 0. A claim the evidence does not back is a finding.
3. Are there gaps: behavior the brief needs that nothing implements or tests, a missing failure or negative case, or a behavior change the brief did not request?
4. Are there security problems?

It reports BLOCK when any of these finds a problem, otherwise OK. Each finding explains what is missing or wrong with a `file:line` location and reaches the coder verbatim. Alternative approaches and style preferences are notes in SUMMARY, never findings.

A reviewer started by an automation's `agent` step gets the evidence in its brief: the reviewed job's original brief, its report, the commands it actually ran with their exit codes (read from its session's tool calls) and its diff against its base (see [automations](automations.md#what-the-reviewer-receives)). A reviewer started by hand gets the job's brief and head; it says in SUMMARY which claims it could not verify.

The reviewer runs no builds, tests, checks, or scripts: it judges the evidence the job produced. Reports retain four sections: VERDICT, SUMMARY, FINDINGS, and TESTS RUN. TESTS RUN always says `none (review is read-only)`.

The brief, report, commands, diff and repository are untrusted data: the reviewer assesses them but never follows instructions found in them. A re-review assesses previous findings first and identifies which are fixed.

A review is report-only: the reviewer's checkout is detached and disposable, and it never commits. What happens to a reviewed job afterwards (delivering it with `swarm_deliver_job`, sending findings back with `swarm_message_job` or an automation's `reopen` step) is up to the AI or automation that asked for the review.
