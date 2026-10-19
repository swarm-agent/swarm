# Design request execution (retired)

`manage_design` and its daemon-owned `DesignDispatcher`, admission, allocation and repair were replaced by `swarm_design` (in-session, `run/design_tool.go`) and design jobs (`api/project_design_jobs.go`), both running the one-shot `design` pipeline. Nothing dispatches stored design requests any more.

At daemon start `Store.RetirePendingDesignRequests` ends every still-pending request once: queued candidates fail with `manage_design_replaced` and an alert that names `swarm_design`; running attempts end `interrupted` and cancel-requested ones `cancelled` with the same reason code. No provider is called and nothing is published. Stored requests, revisions, selections and their read routes remain readable; `edit` on a stored design returns 410 (see `../api/design_api_contract.md`).
