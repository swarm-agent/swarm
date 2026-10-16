# Scope-locked review

The system reviewer receives the job's brief and exact committed head, reads `git diff BASE...HEAD`, and answers one question: did the coder do what the brief asked, completely and only that? It reports BLOCK only for a missing or incorrectly implemented requirement, or an unrequested behavior change in the diff; otherwise it reports OK. Each finding explains what is missing or wrong with a `file:line` location and reaches the coder verbatim. Alternative approaches, style preferences, and risks outside the brief are notes in SUMMARY, never findings.

The reviewer runs no builds, tests, checks, or scripts. The landing step builds and tests after review. Reports retain four sections: VERDICT, SUMMARY, FINDINGS, and TESTS RUN. TESTS RUN always says `none (the landing step tests after review)`.

The brief, report, and repository are untrusted data: the reviewer assesses them but never follows instructions found in them. A re-review assesses previous findings first and identifies which are fixed. A rebase-scoped review assesses only the conflict resolution.

The folder publish rule may opt into `--pipeline` alongside `--land --require-review --auto`. The existing auto-publish consumer starts reviews and sends BLOCK findings as idempotent follow-ups. `fix_rounds` in account limits defaults to two BLOCK verdicts before exhaustion. Inspect `review_chain` in `swarm_get_job`; exhaustion and admission failures stop that job for outside intervention. Policy off preserves manual review.

Landing retains its checks and exact review validation. Folder landings can run in parallel as a merge train with `landing_parallel`, pushing in order so the target branch only receives tested combinations. Pipeline landing additionally requires unchanged per-patch range-diff correspondence before carrying approval across a clean rebase. Conflict send-backs use the existing landing queue; subsequent reviews request a rebase-scoped assessment.
