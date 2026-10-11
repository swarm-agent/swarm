---
name: setup
description: Set up and onboard the person's Swarm end to end — check it is running and reachable, connect a model provider safely, confirm models and a workspace, and prove it works with a first small task. Use when the person says "help me with Swarm", "set up Swarm", "get Swarm working" or is new to Swarm.
---

# Set up Swarm with the person

Follow the `using-swarm` skill for how each tool works. Go step by step, say
what you are doing in one line each, and stop to ask only when the person must
act.

1. **Reachable?** Call `swarm_list_providers`. If the swarm tools are missing
   or fail to connect, Swarm is not installed or not running on this machine:
   tell the person to install it from https://swarmagent.dev (one line, Linux)
   and start it with `swarm install --service`, then reconnect. If the error
   says the owner is not set up yet, ask them to run `swarmctl setup identity`
   once.
2. **Model access.** If no provider is `ready`, ask which they use (an API key
   from Anthropic, OpenAI, Google, ... or a ChatGPT plan) and connect it:
   `swarm_request_provider_key` (give them the link; never take a key in
   chat) then wait with `swarm_list_providers`, or `swarm_connect_chatgpt`.
3. **Models.** Show the role defaults from `swarm_list_models` in a short
   table. Change nothing unless they ask.
4. **Workspace.** List workspaces; if none fits what they want to do, create
   one with `swarm_create_workspace` and say where it is.
5. **First task.** Offer one small real task in that workspace (for example
   "write a README for this folder" or what they came for). Run it, handle any
   approvals with them, check the result with `swarm_get_changes`, and show
   them.
6. **Finish** with a three-line summary: what is set up, where their work
   lives, and what they can ask next (bigger projects with the orchestrator,
   scheduled workers, building their own app on Swarm).
