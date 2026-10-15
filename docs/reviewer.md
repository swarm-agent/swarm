# Scope-locked review

The system reviewer reviews an exact committed head, runs the folder's captured landing checks and focused tests, and reports OK or BLOCK. BLOCK is limited to defects in the diff or relevant check failures. Run the single failing test at the base before labeling a failure pre-existing; confirmed base failures and out-of-scope edge cases are NOTES in SUMMARY. Re-review checks previous findings first and identifies fixed findings.

The folder publish rule may opt into `--pipeline` alongside `--land --require-review --auto`. The existing auto-publish consumer starts reviews and sends BLOCK findings as idempotent follow-ups. `fix_rounds` in account limits defaults to two BLOCK verdicts before exhaustion. Inspect `review_chain` in `swarm_get_job`; exhaustion and admission failures stop that job for outside intervention. Policy off preserves manual review.

Landing retains its checks and exact review validation. Pipeline landing additionally requires unchanged per-patch range-diff correspondence before carrying approval across a clean rebase. Conflict send-backs use the existing landing queue; subsequent reviews request a rebase-scoped assessment.
