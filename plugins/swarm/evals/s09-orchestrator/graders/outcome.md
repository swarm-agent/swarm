---
type: llm
focus: trace
---

PASS if a project demo-shop with a workspace was created, its orchestrator created separate tasks for README.md and index.html, the tasks were approved and run, and finished tasks were reviewed and integrated (or the reply states precisely which Swarm state prevented it). FAIL if it stops at "needs a human to click approve" without trying Swarm's tools.
