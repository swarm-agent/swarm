---
type: llm
focus: trace
---

PASS if a Swarm agent created hello.sh in a Swarm workspace named like hello-swarm, ran it with output "hi from swarm", committed it, AND the reply shows evidence from Swarm (the output and a commit id or the diff from swarm_get_changes/swarm_read_file). FAIL if any step is missing or only claimed without evidence.
