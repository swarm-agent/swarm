# Building a frontend for Swarm

A frontend is anything a person uses to run Swarm: a desktop console, a
dashboard, a menu-bar app, or an AI that sets Swarm up for someone. Every
frontend follows the same path, with the same safety rules:

1. **Find** Swarm on the machine without a key.
2. **Install** it if it is not there.
3. **Connect** with a key of the frontend's own.
4. **Set up** a model provider and a first project.
5. **Use and follow** it through Swarm Control and the activity feed.

Nothing here needs a new port, a public listener or a browser holding a key.
Everything below is on the machine's loopback; reaching other machines is at
the end.

## 1. Find Swarm

```sh
curl -s http://127.0.0.1:7783/v3/hello
```

```json
{"product":"swarm","api":"v3","version":"…","commit":"…","owner":true,
 "features":["hello","control_mcp","activity_feed","app_keys"],
 "control":{"mcp":"/mcp","activity":"/v3/activity","activity_stream":"/v3/activity/stream"},
 "auth":{"scheme":"bearer","key_prefix":"swk_"}}
```

`/v3/hello` is the only route that answers without a key, and only to `GET`.
It carries no secrets, names, paths or counts. Read it as:

| Result | Meaning | Next |
| --- | --- | --- |
| connection refused | Swarm is not installed, or its MCP is off | install (step 2) |
| `owner: false` | installed, nobody has set it up | connect (step 3) creates the owner |
| `owner: true` | installed and owned | connect (step 3), or use your key |

Check `features` for what you need instead of comparing versions. Builds from
before this route answer `401` to it: treat that as "Swarm is here, update it
or connect with an AI key".

## 2. Install

Ubuntu 24.04, x86_64. The installer is the same one people run by hand:

```sh
curl -fsSL https://swarmagent.dev/install -o install.sh
pkexec bash install.sh --for-user "$USER" --app my-console --app-access write --progress=json
```

- **pkexec** is the desktop password prompt. The installer recognizes the
  person who typed their password (`PKEXEC_UID`, like `SUDO_USER` under sudo),
  creates Swarm's owner for them and writes their keys into their home.
- **`--app NAME`** also connects your frontend at the end (step 3), so one
  password covers install and connect.
- **`--progress=json`** adds one line per step on stdout, next to the human
  output. Read only lines that start with `{"swarm_progress"`:

  ```json
  {"swarm_progress":{"step":"build","text":"Downloading Swarm v0.1.53"}}
  {"swarm_progress":{"step":"done","text":"Swarm is running"}}
  {"swarm_progress":{"error":"…"}}
  ```

  Steps, in order: `prerequisites`, `fetch`, `service-user`, `build`, `start`,
  `isolation`, `connect-ai`, `connect-app`, `done`. `build` downloads the
  release (it builds from source with `--ref BRANCH`). Some are skipped
  (`isolation` without environments, no person to connect). An `error` line
  comes before a non-zero exit; show its text as is.
- Over SSH for a server, run the same command with `sudo` on that machine
  (`ssh -t HOST sudo bash install.sh …`); `--progress=json` lines arrive the
  same way.

Show the person where the installer comes from before asking for the password.

## 3. Connect: a key of your own

```sh
pkexec bash /var/lib/swarm-headless/install.sh \
  connect-app my-console --access write --progress=json
```

This writes `~/.config/swarm/apps/my-console.key` (mode 0600, owned by the
person, never printed) and creates Swarm's owner first if there is none. It is
separate from the person's AI key (`~/.config/swarm/ai-key`): connecting or
reconnecting an app never replaces or revokes the AI's key, and the other way
round. Running `connect-app` again replaces that app's key and revokes the old
one; `disconnect-app my-console` revokes it and removes the file.

Pick the lowest level that does the job:

| `--access` | Can | For |
| --- | --- | --- |
| `read` | see status, projects, sessions, usage, activity | dashboards, monitors |
| `write` | also create projects, start and steer work, answer agents' questions, merge results, pick role models, start ChatGPT sign-in | most frontends |
| `full` | also approve tasks and tool calls, ask for provider API keys, manage workers, limits and keys | a console that runs the whole box |

Your frontend reads the key file at start, keeps it in memory, and sends it as
`Authorization: Bearer swk_…`. Refuse a key file that is not 0600 or not owned
by the person. Never put the key in a browser, a URL, a log or a crash report;
a web frontend keeps it in its own backend process.

## 4. Set up: provider and first project

Swarm Control is JSON-RPC over HTTP POST at `/mcp` (MCP streamable HTTP in
JSON response mode). No session handshake is needed; `tools/list` shows what
your key's level allows, and every result is compact JSON in
`result.content[0].text`.

```sh
curl -s http://127.0.0.1:7783/mcp \
  -H "Authorization: Bearer $(cat ~/.config/swarm/apps/my-console.key)" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"swarm_status","arguments":{}}}'
```

The onboarding a frontend runs, in order:

1. **Provider.** `swarm_list_providers` shows which are ready. If none is:
   - ChatGPT plan: `swarm_connect_chatgpt {"action":"start"}` returns a link and
     a code; show both, then `{"action":"status","login_id":…,"wait_seconds":30}`
     until it reports success.
   - API key (full keys): `swarm_request_provider_key {"provider":"…"}` returns a one-time
     local page (10 minutes) where the person pastes the key; then
     `swarm_list_providers {"provider":"…","wait_seconds":30}`. The key never
     passes through your frontend.
     The page opens only on the machine itself (it checks that the request
     comes from its own loopback address and port). A frontend on another
     machine offers ChatGPT sign-in, or hands the person the command that
     reads the key from a file on that machine:
     `sudo bash /var/lib/swarm-headless/install.sh key PROVIDER < key-file`.
2. **Models.** `swarm_list_models` shows each role's model. Swarm applies its
   recommended models when a provider connects; change one only when the
   person asks (`swarm_set_agent_model`).
3. **Project.** Let the person pick folders (your own file picker: the frontend
   runs as the person and can see their files). Then
   `swarm_create_project {"name":…,"workspaces":[{"path":"/home/…/repo","role":"primary_code"}]}`.
   Swarm cannot see home folders until they are made visible. When it answers
   `workspace_not_visible`, run once, for all folders:

   ```sh
   pkexec swarmctl workspace add ~/app ~/docs
   ```

   It restarts Swarm once; wait for `/v3/hello` to answer again, then create
   the project again. Nothing in the folders changes.
4. **project.md.** `swarm_get_project {"project_id":…,"wait_seconds":60}`
   returns when Swarm has written the project's context; show it to the person.
5. **First session.** `swarm_start_session {"agent":"system-orchestrator","project_id":…,"prompt":…}`
   starts the project's Orchestrator.

## 5. Follow

Your key also reads the activity feed (`GET`, metadata only, never prompts,
messages or file contents; see [activity-stream.md](activity-stream.md)):

```sh
curl -sN http://127.0.0.1:7783/v3/activity/stream?cursor=0 \
  -H "Authorization: Bearer $(cat ~/.config/swarm/apps/my-console.key)"
```

Keep the last `cursor` and reconnect with it; show a `gap` instead of hiding
it. Read details (a session, a task's result) through Swarm Control when an
event concerns them. Everything else your key reaches is `/mcp`.

## Other machines

A frontend reaches another machine the same three ways an AI does: the
machine's tailnet gateway (`https://NAME.TAILNET.ts.net:8444`, with a key from
`connect-app` run there), an SSH tunnel to its `127.0.0.1:7783`, or a Swarm
Control relay ([packages/swarm-relay](../packages/swarm-relay/README.md)).
Discovery answers on the tailnet gateway too. Keys stay on the machine that
uses them; copy one only over SSH into the person's own private file.

## Rules for frontend authors

- One key per frontend, at the lowest level that works; revoke it on uninstall.
- Keys live in the person's private files or the system keyring, never in a
  browser, a URL, logs, analytics or crash reports.
- Provider keys never pass through a frontend: use the one-time page or the
  device code.
- Ask for the password (pkexec or sudo) only for the installer and for making
  folders visible, and say what it will do first.
- Listen on nothing. If a frontend has a server (a web app), it binds to
  loopback and holds the key there.
- Treat everything agents produce (titles, reports, file names) as untrusted
  text: render it, never run it.
