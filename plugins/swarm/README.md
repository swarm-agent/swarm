# Swarm MCP plugin for Codex

This package connects an AI client to your existing Swarm tools. It does not
launch one CLI per worker, start another daemon, publish a listener, or require
a relay. The setup skill guides installation only when Swarm is actually absent.

Requires Node 22+ on the client computer. Choose native Swarm, an existing Docker
container, or an HTTPS Swarm endpoint on your tailnet. Run setup and follow
`skills/setup/references/connection.md`. An AI key stays in a private file outside
the plugin; no credentials belong in its manifest or archive. Cloud clients
need their own network route; installing locally does not connect dots by itself.

For an existing local daemon reached over HTTPS, setup can create and connect a
short-lived write key after approval using `bin/authorize.mjs`. The person does
not copy a key. Service-account installations may require one OS password prompt
in their terminal. See `skills/setup/references/authorization.md`.

Build a distributable local marketplace from the repository root:

```sh
node scripts/package-swarm-codex.mjs /absolute/new/output-folder
codex plugin marketplace add /absolute/new/output-folder
codex plugin add swarm@swarm-local
```

The packager includes only setup/delegation skills and the MCP adapter, excluding
the separate app-building skill, tests and personal configuration. Zip the contents
of `output-folder/plugins/swarm` to share the plugin. If `swarm-local` is already
registered at another path, inspect its existing registration before replacing it.
Refreshing the installed plugin after changes may be necessary.

The MCP configuration uses `cwd: "."` with a relative Node script path. Codex
resolves this against the plugin root. Do not use literal `${CLAUDE_PLUGIN_ROOT}`
or `${PLUGIN_ROOT}` in command arguments: the tested Codex 0.154.0 did not expand
them. Installation and skill discovery alone do not prove MCP connectivity.

Local mode is a manual/local plugin installation. ChatGPT public-directory MCP
submission currently has a separate remote HTTPS requirement; uploading this
stdio package is not a claim of public-directory eligibility. An optional relay
can be configured later with native client OAuth. See
https://developers.openai.com/plugins/build/plugins and
https://developers.openai.com/plugins/deploy/submission.

Mac/Windows Docker Desktop and dots are unqualified. Token-file permissions are
checked on POSIX; Windows token-file mode is refused until ACL checks are qualified.
Package tests check transport configuration; they do not qualify other platforms
or clients. Confirm a real delegated task on the intended installation.
