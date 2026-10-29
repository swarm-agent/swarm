//! A live view of one Swarm machine, the way Swarm's own web UI follows it:
//! one sync snapshot (POST /v3/sync/bootstrap), then its realtime stream
//! (the /v3/realtime/stream WebSocket) for everything that happens, with the
//! agent's text as it is produced. Open conversations are hydrated in full
//! (POST /v3/sync/hydrate). A background thread keeps the model and hands the
//! console snapshots; commands (start a session, send, approve, merge, stop)
//! run on their own threads and report back the same way.

use std::collections::{HashMap, HashSet};
use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::{Value, json};

use super::client::{Client, Refusal};
use super::map::{self, list, s};
use super::models::{self, Catalog, Choice};
use super::permissions::Rule;
use super::realtime::Stream;
use super::repository::{self, Prepared};
use crate::data::{Assignment, Attachment, ClientCall, Entry, Machine, Pending, Project, Role, RunState, Session, Task, ToolState, Usage};

/// The machine, roles and usage change slowly: reread this often.
const SLOW_EVERY: Duration = Duration::from_secs(30);
/// Frames that arrive within this window become one update: one paint.
const COALESCE: Duration = Duration::from_millis(16);
/// Swarm hydrates at most this many sessions per request with their views.
const HYDRATE_CHUNK: usize = 8;
/// How long to wait before reconnecting after the stream fails, at first
/// and at most.
const RETRY_FIRST: Duration = Duration::from_secs(2);
const RETRY_MOST: Duration = Duration::from_secs(30);

pub struct Snapshot {
    pub projects: Vec<Project>,
    pub sessions: Vec<Session>,
    /// None when this reading did not include them; keep what you have.
    pub machine: Option<Machine>,
    pub roles: Option<Vec<Role>>,
    pub usage: Option<Usage>,
    /// Themes by id as Swarm's catalog returns them ({id, name, palette}).
    pub themes: Option<HashMap<String, Value>>,
    /// The media of the sessions read so far.
    pub media: Vec<crate::data::Media>,
    /// Models to pick from, with favorites; None when not read this time.
    pub catalog: Option<Catalog>,
}

/// The slow-changing facts about the machine, read on their own thread so
/// conversations never wait for them.
#[derive(Default)]
pub struct Facts {
    pub machine: Option<Machine>,
    pub roles: Option<Vec<Role>>,
    pub usage: Option<Usage>,
    pub themes: Option<HashMap<String, Value>>,
    pub catalog: Option<Catalog>,
}

pub enum Update {
    Snapshot(Snapshot),
    /// The machine, roles, usage, themes and models, read on their own.
    Facts(Facts),
    /// One conversation changed (its text streamed, a request came): only it.
    Session(Session),
    /// Swarm did not answer; the text says why.
    Lost(String),
    /// A command finished. `draft` names the new session it started.
    Done { draft: Option<u64>, what: &'static str, result: Result<Value, String> },
    /// A folder the Orchestrator asked to ready has files: the person picks
    /// its first commit (the request stays open until then).
    Review { permission_id: String, review: repository::Review },
    /// Workers or automations changed in Swarm: read them again.
    AutomationsChanged,
    /// The account's permissions changed in Swarm (permissions.updated).
    PermissionsChanged,
    /// The watched run's session moved (a message, an action): read it again.
    RunActivity(String),
    /// An action in your browser the extension found critical: it waits for
    /// you (the request stays open until you do it or deny it).
    Hold { permission_id: String, why: String },
}

#[derive(Clone)]
pub enum Command {
    /// Start an Orchestrator conversation in a project with the person's first message.
    Start { draft: u64, project_id: String, workspace_path: String, text: String, model: Option<Choice>, files: Vec<Attachment> },
    /// The model for one conversation only.
    SetModel { session_id: String, choice: Choice },
    /// A role's default model for every conversation (plan, action, system-coder, …).
    SetDefault { role: String, model: Assignment },
    /// Save a favorite (a named model, thinking and priority), or remove one.
    SaveFavorite { name: String, model: Assignment },
    RemoveFavorite { id: String },
    /// Read the account's permissions (GET /v3/permissions).
    ReadPermissions,
    /// Change one rule (PATCH /v3/permissions with the revision it was read
    /// at), then read the record again.
    SetPermission { rule: Rule, value: String, revision: u64 },
    /// Carry out a call to your machine here and answer it with the result.
    Client { session_id: String, permission_id: String, tool: String, args: serde_json::Value },
    /// Ready a project folder for agents on the Orchestrator's request: Swarm's
    /// empty first commit, or the review (then Commit) for a folder with files.
    Ready { session_id: String, permission_id: String, path: String },
    /// The picked files become the folder's first commit; the request is answered.
    Commit { session_id: String, permission_id: String, review: repository::Review, selected: Vec<String> },
    /// Refuse a call to your machine, or a request for access.
    Refuse { session_id: String, permission_id: String, reason: String },
    /// Home redeems a machine's link code (made on the machine with
    /// `sudo swarmctl fleet invite ADDRESS`) and becomes its home.
    LinkMachine { code: String },
    /// Home lets a linked machine go (the machine is told and ends home's session there).
    UnlinkMachine { name: String },
    /// Carry out a manage_browser call in your Chrome and answer it; with
    /// `confirmed` false an action the extension finds critical is held.
    Browser { session_id: String, permission_id: String, args: serde_json::Value, confirmed: bool },
    Send { session_id: String, text: String, files: Vec<Attachment> },
    /// Answer the session's pending question, or allow or deny its pending tool call.
    Resolve { session_id: String, permission_id: String, allow: bool, answer: Option<String> },
    Approve { project_id: String, task_id: String, approve: bool },
    /// Archive sessions (Swarm's batch route); each then leaves the list by
    /// its session.archived event.
    Archive { session_ids: Vec<String> },
    /// Rename a session: Swarm keeps the title and stops titling it itself.
    Rename { session_id: String, title: String },
    /// Pin or unpin a session: Swarm metadata `pinned`.
    Pin { session_id: String, pinned: bool },
    /// Bring an archived session back (Swarm wants its tombstone's version,
    /// read from the project's archive first).
    Unarchive { session_id: String, project_id: String },
    /// Read one session into the list (a search result older than the
    /// recent list) so it can open.
    Reveal { session_id: String },
    /// Change a project's record (name, project.md): PATCH /v3/projects/{id}.
    PatchProject { project_id: String, body: Value },
    OrderProjects { project_ids: Vec<String> },
    DeleteProject { project_id: String },
    RefreshProjectContext { project_id: String, expected_attempt: u64 },
    /// `waiting_run_id`: the run asleep on tasks ("" for none). Swarm's
    /// session detail lists only an active run, so a wait is stopped by this id.
    Stop { session_id: String, waiting_run_id: String },
    /// Read a conversation's context (see `context::read`).
    ReadContext { session_id: String },
    /// Compact a conversation now, setting its auto-compact threshold first
    /// when one is given (see `context::compact`).
    Compact { session_id: String, note: String, threshold: Option<f64> },
}

pub struct Live {
    client: Client,
    watched: Arc<Mutex<HashSet<String>>>,
    /// Wakes the follow loop at once: a watch change, or a command that ran
    /// (then the facts are read again too). Shares the stream's frame channel.
    kick: Sender<Value>,
    tx: Sender<Update>,
    /// Until the console takes it to wait on (take_updates).
    rx: Option<Receiver<Update>>,
    models_dirty: Arc<std::sync::atomic::AtomicBool>,
    /// Asks the facts thread to read again now.
    facts_kick: Sender<()>,
    /// The worker run whose session the Automations page shows: subscribed
    /// to, so its actions arrive live.
    run_watch: Arc<Mutex<Option<String>>>,
}

/// One conversation as the model holds it.
#[derive(Clone, Default)]
struct Detail {
    entries: Vec<Entry>,
    /// The agent's text of the run in progress, as it streams.
    live: String,
    model: String,
    pending: Option<Pending>,
    calls: Vec<ClientCall>,
    hydrated: bool,
}

impl Live {
    /// `kind` says how the console reaches the machine: "local" or "ssh".
    pub fn start(client: Client, kind: &'static str) -> Live {
        let watched = Arc::new(Mutex::new(HashSet::new()));
        let (tx, rx) = mpsc::channel();
        let (kick, kicked) = mpsc::channel::<Value>();
        let models_dirty = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let (facts_kick, facts_kicked) = mpsc::channel();
        // Chrome's extension talks to the app over its socket from now on;
        // a change of link repaints, and a stop there ends every lease.
        {
            let kick = kick.clone();
            crate::browser::start(Box::new(move |what| {
                let _ = kick.send(json!({"kind": "console.browser", "event": what}));
            }));
        }
        {
            let (c, t, dirty) = (client.clone(), tx.clone(), models_dirty.clone());
            std::thread::spawn(move || facts_loop(c, kind, t, facts_kicked, dirty));
        }
        let run_watch: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));
        let (c, w, t, fk, frames_tx, rw) = (client.clone(), watched.clone(), tx.clone(), facts_kick.clone(), kick.clone(), run_watch.clone());
        std::thread::spawn(move || {
            let mut wait = RETRY_FIRST;
            loop {
                let started = Instant::now();
                let why = match follow(&c, &w, &t, &kicked, &frames_tx, kind, &fk, &rw) {
                    Ok(()) => return, // the console is gone
                    Err(e) => e,
                };
                if started.elapsed() > Duration::from_secs(60) {
                    wait = RETRY_FIRST;
                }
                if t.send(Update::Lost(why)).is_err() {
                    return;
                }
                match kicked.recv_timeout(wait) {
                    Err(RecvTimeoutError::Disconnected) => return,
                    _ => {}
                }
                while kicked.try_recv().is_ok() {}
                wait = (wait * 2).min(RETRY_MOST);
            }
        });
        Live { client, watched, kick, tx, rx: Some(rx), models_dirty, facts_kick, run_watch }
    }

    /// The updates, to wait on from a background task; once.
    /// A handle on Swarm for reads and actions a page does on its own
    /// (the providers page), with the same session.
    /// Follows one worker run's session live (None stops).
    pub fn watch_run(&self, session_id: Option<String>) {
        let mut w = self.run_watch.lock().unwrap();
        if *w != session_id {
            *w = session_id;
            let _ = self.kick.send(json!({"kind": "console.kick", "command": false}));
        }
    }

    pub fn client(&self) -> Client {
        self.client.clone()
    }

    pub fn take_updates(&mut self) -> Option<Receiver<Update>> {
        self.rx.take()
    }

    /// The sessions open in panes: their conversations are followed in full.
    pub fn watch(&self, ids: impl IntoIterator<Item = String>) {
        if let Ok(mut w) = self.watched.lock() {
            let next: HashSet<String> = ids.into_iter().collect();
            if *w != next {
                *w = next;
                let _ = self.kick.send(json!({"kind": "console.kick", "command": false}));
            }
        }
    }

    /// Updates that arrived since the last call (while the console has not
    /// taken the receiver).
    pub fn drain(&self) -> Vec<Update> {
        self.rx.as_ref().map(|rx| rx.try_iter().collect()).unwrap_or_default()
    }

    pub fn run(&self, command: Command) {
        if matches!(command, Command::SetDefault { .. } | Command::SaveFavorite { .. } | Command::RemoveFavorite { .. }) {
            self.models_dirty.store(true, std::sync::atomic::Ordering::Relaxed);
        }
        let (client, tx, kick) = (self.client.clone(), self.tx.clone(), self.kick.clone());
        std::thread::spawn(move || {
            // Readying a folder may end in a review the person must do first:
            // then the request stays open and the console gets the review.
            if let Command::Ready { session_id, permission_id, path } = &command {
                let outcome = match repository::prepare(&client, path) {
                    Ok(Prepared::NeedsReview(review)) => {
                        let _ = tx.send(Update::Review { permission_id: permission_id.clone(), review });
                        let _ = kick.send(json!({"kind": "console.kick", "command": true}));
                        return;
                    }
                    Ok(Prepared::Ready { note }) => ready_answer(&client, session_id, permission_id, path, note),
                    Ok(Prepared::Invisible(why)) | Err(why) => Err(why),
                };
                let _ = tx.send(Update::Done { draft: None, what: "ready", result: outcome });
                let _ = kick.send(json!({"kind": "console.kick", "command": true}));
                return;
            }
            // An action in the browser may come back held (critical, not yet
            // confirmed): then the request stays open and the console shows it.
            if let Command::Browser { session_id, permission_id, args, confirmed } = &command {
                if let Err(why) = require_ready_device_call(&client, session_id, permission_id) {
                    let _ = tx.send(Update::Done { draft: None, what: "browser", result: Err(why) });
                    return;
                }
                let outcome = match crate::browser::act(session_id, args, *confirmed) {
                    Ok(result) if result.get("needs_confirm").is_some() => {
                        let why = result.get("needs_confirm").and_then(Value::as_str).unwrap_or("").to_string();
                        let _ = tx.send(Update::Hold { permission_id: permission_id.clone(), why });
                        let _ = kick.send(json!({"kind": "console.kick", "command": true}));
                        return;
                    }
                    Ok(result) => resolve(&client, session_id, permission_id, json!({"action": "allow_once", "approved_arguments": {"result": result}})),
                    // The agent is told what went wrong rather than left waiting.
                    Err(e) => resolve(&client, session_id, permission_id, json!({"action": "allow_once", "approved_arguments": {"result": {"error": e}}})).and(Err(e)),
                };
                let _ = tx.send(Update::Done { draft: None, what: "browser", result: outcome });
                let _ = kick.send(json!({"kind": "console.kick", "command": true}));
                return;
            }
            // A pin changes the session's record without an event: the follow
            // thread reads that session again once the change is in.
            let reread = match &command {
                Command::Pin { session_id, .. } | Command::Unarchive { session_id, .. } | Command::Reveal { session_id } => Some(session_id.clone()),
                _ => None,
            };
            // The rules change nothing the model holds: no reread after them.
            let quiet = matches!(command, Command::ReadPermissions | Command::SetPermission { .. } | Command::ReadContext { .. });
            let (draft, what, result) = perform(&client, command);
            let ok = result.is_ok();
            let _ = tx.send(Update::Done { draft, what, result });
            if quiet {
                return;
            }
            if let Some(session_id) = reread.filter(|_| ok) {
                let _ = kick.send(json!({"kind": "console.session", "session_id": session_id}));
            }
            let _ = kick.send(json!({"kind": "console.kick", "command": true}));
        });
    }
}

/// Answers a folder request with the folder's state, now that it is ready.
fn ready_answer(client: &Client, session_id: &str, permission_id: &str, path: &str, note: Option<String>) -> Result<Value, String> {
    let state = repository::inspect(client, path)?;
    let mut result = json!({"state": state.state, "workspace_path": path, "head_commit": state.head});
    if let Some(note) = note {
        result["note"] = json!(note);
    }
    resolve(client, session_id, permission_id, json!({"action": "allow_once", "approved_arguments": {"result": result}}))
}

/// A request id no other request shares.
fn request_id(what: &str) -> String {
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
    format!("swarm-console:{what}:{now}")
}

fn err(e: Refusal) -> String {
    e.to_string()
}

fn perform(client: &Client, command: Command) -> (Option<u64>, &'static str, Result<Value, String>) {
    match command {
        Command::Start { draft, project_id, workspace_path, text, model, files } => {
            // A project conversation lives in its project; Swarm binds no
            // workspace to it (the project's folders are its scope).
            let _ = workspace_path;
            let mut body = json!({
                "client_request_id": request_id("session"),
                "project_id": project_id,
                "agent_name": "system-orchestrator",
            });
            if let Some(model) = model {
                body["model_profile"] = models::new_session_profile(&model);
            }
            let result = client.post("/v3/sessions", body).map_err(err).and_then(|created| {
                let id = s(&created, "session_id").to_string();
                if id.is_empty() {
                    return Err("Swarm created no session".into());
                }
                send_message(client, &id, &text, &files)?;
                Ok(json!({"session_id": id}))
            });
            (Some(draft), "start", result)
        }
        Command::Send { session_id, text, files } => {
            (None, "send", send_message(client, &session_id, &text, &files))
        }
        Command::Resolve { session_id, permission_id, allow, answer } => {
            let mut body = json!({"action": if allow { "allow_once" } else { "deny_once" }});
            if let Some(answer) = answer {
                body["reason"] = json!(answer);
            }
            (None, if allow { "answer" } else { "deny" }, resolve(client, &session_id, &permission_id, body))
        }
        Command::Client { session_id, permission_id, tool, args } => {
            let result = require_ready_device_call(client, &session_id, &permission_id).and_then(|_| {
                let result = crate::machine::answer(&tool, &args);
                resolve(client, &session_id, &permission_id, json!({"action": "allow_once", "approved_arguments": {"result": result}}))
            });
            (None, "machine", result)
        }
        Command::Commit { session_id, permission_id, review, selected } => {
            let result = repository::baseline(client, &review, &selected).and_then(|state| {
                let result = json!({"state": state.state, "workspace_path": review.path, "head_commit": state.head, "committed": selected});
                resolve(client, &session_id, &permission_id, json!({"action": "allow_once", "approved_arguments": {"result": result}}))
            });
            (None, "commit", result)
        }
        Command::Ready { .. } => (None, "ready", Err("handled before perform".into())),
        Command::Refuse { session_id, permission_id, reason } => {
            (None, "refuse", resolve(client, &session_id, &permission_id, json!({"action": "deny_once", "reason": reason})))
        }
        Command::Browser { .. } => (None, "browser", Err("handled before perform".into())),
        Command::ReadPermissions => (None, "permissions", client.get("/v3/permissions").map_err(err)),
        // Swarm's answer is the stored record; the page shows a fresh read of
        // it. A refusal (409 stale revision, 403 another device's rule, 400)
        // comes back as Swarm's message; the console then reads again.
        Command::SetPermission { rule, value, revision } => {
            let body = rule.patch(&value, revision, &request_id("permissions"));
            let result = client.patch("/v3/permissions", body).map_err(err).map(|saved| client.get("/v3/permissions").unwrap_or(saved));
            // Browser on again for this device lifts a stop pressed in the
            // extension (only this device's own rows can be changed here).
            if result.is_ok() && matches!(rule, super::permissions::Rule::Browser(_)) && value != "off" {
                crate::browser::resume();
            }
            (None, "permission", result)
        }
        Command::LinkMachine { code } => (None, "link", client.post("/v3/fleet/machines", json!({"code": code})).map_err(err)),
        Command::UnlinkMachine { name } => (None, "unlink", client.delete(&format!("/v3/fleet/machines/{name}")).map_err(err)),
        Command::Approve { project_id, task_id, approve } => (
            None,
            if approve { "approve" } else { "reject" },
            client.post(&format!("/v3/projects/{project_id}/tasks/{task_id}/{}", if approve { "approve" } else { "reject" }), json!({})).map_err(err),
        ),
        Command::Stop { session_id, waiting_run_id } => (None, "stop", stop(client, &session_id, &waiting_run_id)),
        // Both answers name their session, a failure included (as `error`).
        Command::ReadContext { session_id } => (None, "context", Ok(super::context::read(client, &session_id))),
        Command::Compact { session_id, note, threshold } => (None, "compact", Ok(super::context::compact(client, &session_id, &note, threshold))),
        Command::Archive { session_ids } => (None, "archive", client.post("/v3/sessions:archive", json!({"session_ids": session_ids})).map_err(err)),
        Command::Rename { session_id, title } => (None, "rename", client.post(&format!("/v3/sessions/{session_id}/title"), json!({"title": title, "client_request_id": request_id("title")})).map_err(err)),
        Command::Pin { session_id, pinned } => (None, if pinned { "pin" } else { "unpin" }, client.post(&format!("/v3/sessions/{session_id}/metadata"), json!({"metadata": {"pinned": pinned}, "client_request_id": request_id("pin")})).map_err(err)),
        Command::Unarchive { session_id, project_id } => (None, "unarchive", unarchive(client, &session_id, &project_id)),
        Command::Reveal { .. } => (None, "reveal", Ok(json!({}))),
        Command::RefreshProjectContext { project_id, expected_attempt } => (None, "project", client.post(&format!("/v3/projects/{project_id}/context:retry"), json!({"expected_attempt": expected_attempt})).map_err(err)),
        Command::DeleteProject { project_id } => (None, "delete project", client.delete(&format!("/v3/projects/{project_id}")).map_err(err)),
        Command::OrderProjects { project_ids } => (None, "project", client.put("/v3/projects:order", json!({"project_ids": project_ids, "client_request_id": request_id("project-order")})).map_err(err)),
        Command::PatchProject { project_id, body } => (None, "project", client.patch(&format!("/v3/projects/{project_id}"), body).map_err(err)),
        Command::SetModel { session_id, choice } => (None, "model", models::set_session_model(client, &session_id, &choice, &request_id("model")).map_err(err)),
        Command::SetDefault { role, model } => (None, "default", models::set_default(client, &role, &model).map_err(err)),
        Command::SaveFavorite { name, model } => (None, "favorite", models::save_favorite(client, &name, &model).map_err(err)),
        Command::RemoveFavorite { id } => (None, "unfavorite", models::remove_favorite(client, &id).map_err(err)),
    }
}

fn send_message(client: &Client, session_id: &str, text: &str, files: &[Attachment]) -> Result<Value, String> {
    let (content, media) = attach(client, session_id, text, files)?;
    let mut body = json!({"client_request_id": request_id("message"), "role": "user", "content": content});
    if !media.is_empty() {
        body["media"] = json!(media);
    }
    client.post(&format!("/v3/sessions/{session_id}/messages"), body).map_err(err)
}

/// Swarm's web UI limits for text and code files put into a message.
const TEXT_FILE_MAX: usize = 1 << 20;
const TEXT_TOTAL_MAX: usize = 4 << 20;

/// Text and code files the web UI puts into the message instead of uploading.
const TEXT_FILE_TYPES: &[&str] = &[
    "bash", "c", "cc", "cfg", "conf", "cpp", "cs", "css", "csv", "cxx", "diff", "go", "graphql", "h", "hpp", "html", "ini", "java", "js", "json", "jsx", "kt", "kts",
    "log", "lua", "md", "mdx", "mjs", "php", "properties", "py", "rb", "rs", "sh", "sql", "svelte", "swift", "toml", "ts", "tsx", "txt", "vue", "xml", "yaml", "yml", "zsh",
];

/// A text or code file's type ("md", "rs"), or None for a file to upload.
fn text_file(name: &str) -> Option<String> {
    let lower = name.trim().to_ascii_lowercase();
    if lower.ends_with(".env.example") {
        return Some(String::new());
    }
    let (_, ext) = lower.rsplit_once('.')?;
    TEXT_FILE_TYPES.contains(&ext).then(|| ext.to_string())
}

/// Files sent with a message, the way Swarm's web UI sends them: a text or
/// code file goes into the message as a fenced block; any other file is
/// uploaded to the session on Swarm, which keeps it, detects its type, and
/// shows images to the model. Only an image's type is declared (the bytes
/// agree with it); Swarm names every other type itself.
fn attach(client: &Client, session_id: &str, text: &str, files: &[Attachment]) -> Result<(String, Vec<Value>), String> {
    let mut content = text.to_string();
    let mut media = Vec::new();
    let mut inlined = 0;
    for file in files {
        if let Some(language) = text_file(&file.name) {
            if file.data.len() > TEXT_FILE_MAX {
                return Err(format!("{} is over the 1 MB limit for a text file.", file.name));
            }
            inlined += file.data.len();
            if inlined > TEXT_TOTAL_MAX {
                return Err("The text files are over 4 MB together.".into());
            }
            let body = String::from_utf8_lossy(&file.data).replace("\r\n", "\n").replace('\r', "\n");
            let block = format!("File: {}\n\n```{language}\n{body}\n```", file.name);
            content = if content.trim_end().is_empty() { block } else { format!("{}\n\n{block}", content.trim_end()) };
            continue;
        }
        let declared = if file.mime.starts_with("image/") { file.mime.as_str() } else { "application/octet-stream" };
        let header_name: String = file.name.chars().map(|c| if c.is_ascii_graphic() || c == ' ' { c } else { '_' }).collect();
        let answer = client
            .post_bytes(&format!("/v3/sessions/{session_id}/media"), &file.data, &[("Content-Type", declared), ("X-Swarm-Media-Filename", &header_name)])
            .map_err(|e| format!("{}: {e}", file.name))?;
        let asset = answer.get("asset").cloned().unwrap_or(Value::Null);
        if s(&asset, "id").is_empty() {
            return Err(format!("{}: Swarm kept no file", file.name));
        }
        media.push(json!({
            "asset_id": asset["id"], "modality": asset["modality"], "mime_type": asset["detected_mime_type"], "file_type": asset["file_type"],
            "file_name": asset["file_name"], "size": asset["size"], "digest_sha256": asset["digest_sha256"], "contract_hash": asset["contract_hash"],
        }));
    }
    Ok((content, media))
}

// Revalidate immediately before executing on this computer. Neither a stale
// card nor another device's pending call grants local execution authority.
fn require_ready_device_call(client: &Client, session_id: &str, permission_id: &str) -> Result<(), String> {
    let permissions = client.get("/v3/permissions").map_err(err)?;
    let device = list(&permissions, "devices").iter()
        .find(|d| d.get("this_device").and_then(Value::as_bool) == Some(true))
        .map(|d| s(d, "id").to_string())
        .filter(|id| !id.is_empty())
        .ok_or("Swarm has not identified this client as a device")?;
    let pending = client.get(&format!("/v3/sessions/{session_id}/permissions")).map_err(err)?;
    let ready = list(&pending, "permissions").iter().any(|p|
        s(p, "id") == permission_id && s(p, "status") == "pending"
        && s(p, "execution") == "ready" && s(p, "target_device") == device);
    if !ready { return Err("Swarm has not approved this call for execution on this device".into()); }
    Ok(())
}

fn resolve(client: &Client, session_id: &str, permission_id: &str, body: Value) -> Result<Value, String> {
    client.post(&format!("/v3/sessions/{session_id}/permissions/{permission_id}/resolve"), body).map_err(err)
}


/// Brings an archived session back. Swarm checks the tombstone's version,
/// which the project's archive lists.
fn unarchive(client: &Client, session_id: &str, project_id: &str) -> Result<Value, String> {
    let archive = client.get(&format!("/v3/sessions?project_id={project_id}&archived_mode=only&limit=500")).map_err(err)?;
    let version = list(&archive, "tombstones")
        .iter()
        .find(|t| s(t, "session_id") == session_id)
        .and_then(|t| t.get("updated_at").and_then(Value::as_i64))
        .ok_or_else(|| "Swarm's archive of the project does not list this conversation".to_string())?;
    client
        .post("/v3/sessions:unarchive", json!({"session_ids": [session_id], "expected_updated_at_by_id": {session_id: version}}))
        .map_err(err)
}

/// Stops a session's run: Swarm wants the run and the Swarm it runs on.
/// The person pressed stop in the extension: this device's Browser rule turns
/// off in Swarm (from Swarm's current record and revision).
fn browser_off(client: &Client) {
    let Ok(record) = client.get("/v3/permissions") else { return };
    let revision = record.get("revision").and_then(Value::as_u64).unwrap_or(0);
    let mine = record.get("devices").and_then(Value::as_array).and_then(|d| d.iter().find(|d| d.get("this_device").and_then(Value::as_bool) == Some(true)));
    let Some(id) = mine.map(|d| s(d, "id").to_string()).filter(|id| !id.is_empty()) else { return };
    let body = super::permissions::Rule::Browser(id).patch("off", revision, &request_id("browser-stop"));
    let _ = client.patch("/v3/permissions", body);
}

fn stop(client: &Client, session_id: &str, waiting_run_id: &str) -> Result<Value, String> {
    let detail = client.get(&format!("/v3/sessions/{session_id}?message_limit=0")).map_err(err)?;
    let Some(body) = stop_body(&detail, waiting_run_id) else {
        return Ok(json!({"run_status": "not_running", "summary": "Nothing is running."}));
    };
    let answer = client.post(&format!("/v3/sessions/{session_id}/run/stop"), body).map_err(err)?;
    Ok(json!({"run_status": s(&answer, "status"), "summary": ""}))
}

/// The `run/stop` body for a session: its active run (from the detail), else
/// the run asleep on tasks the console knows from the run state (Swarm's
/// stop cancels a `waiting_tasks` run by its id). None when neither exists.
fn stop_body(detail: &Value, waiting_run_id: &str) -> Option<Value> {
    let run = detail.get("active_run_intent").cloned().unwrap_or(Value::Null);
    let run_id = Some(s(&run, "run_id")).filter(|id| !id.is_empty()).or(Some(waiting_run_id.trim()).filter(|id| !id.is_empty()))?;
    let meta = detail.get("session").and_then(|x| x.get("metadata")).cloned().unwrap_or(Value::Null);
    Some(json!({"type": "run.stop", "run_id": run_id, "target_swarm_id": s(&meta, "swarm_v3_runtime_swarm_id"), "reason": "stopped from Swarm Console"}))
}

/// The model of one machine while the stream is up.
struct Model<'a> {
    client: &'a Client,
    kind: &'static str,
    projects: Vec<Project>,
    /// Orchestrator conversations by session id, in Swarm's order.
    order: Vec<String>,
    shells: HashMap<String, Value>,
    runs: HashMap<String, Value>,
    /// Sessions with a request waiting (from permission summaries).
    waiting: HashSet<String>,
    details: HashMap<String, Detail>,
    /// Each project's tasks, by project id.
    tasks: HashMap<String, Vec<Value>>,
    /// What the media tasks made, and their files as last read.
    media: Vec<crate::data::Media>,
    media_reads: HashMap<String, super::media::Read>,
    /// Files asked of the download thread, by cache name.
    media_fetching: HashSet<String>,
    media_fetch: Sender<(String, String)>,
    /// Task details read for review, by task id with the revision they are of.
    assessed: HashMap<String, (i64, Value)>,
    /// Asks the facts thread to read again (usage changed, a command ran).
    facts_kick: Sender<()>,
}

#[allow(clippy::too_many_arguments)]
fn follow(client: &Client, watched: &Mutex<HashSet<String>>, tx: &Sender<Update>, frames: &Receiver<Value>, frames_tx: &Sender<Value>, kind: &'static str, facts_kick: &Sender<()>, run_watch: &Mutex<Option<String>>) -> Result<(), String> {
    // The run session subscribed to on this connection.
    let mut run_sub: Option<String> = None;
    // Media files download on their own thread; each one wakes the model,
    // whose next read finds it in the cache.
    let (media_fetch, media_wanted) = mpsc::channel::<(String, String)>();
    {
        let (client, frames_tx) = (client.clone(), frames_tx.clone());
        std::thread::spawn(move || {
            for (url, stem) in media_wanted {
                super::media::download(&client, &url, &stem);
                if frames_tx.send(json!({"kind": "console.media_ready"})).is_err() {
                    return;
                }
            }
        });
    }
    let mut m = Model {
        client,
        kind,
        projects: Vec::new(),
        order: Vec::new(),
        shells: HashMap::new(),
        runs: HashMap::new(),
        waiting: HashSet::new(),
        details: HashMap::new(),
        tasks: HashMap::new(),
        media: Vec::new(),
        media_reads: HashMap::new(),
        media_fetching: HashSet::new(),
        media_fetch,
        assessed: HashMap::new(),
        facts_kick: facts_kick.clone(),
    };
    let t0 = Instant::now();
    let boot = m.bootstrap()?;
    trace(&format!("bootstrap {:?} ({} sessions, {} projects)", t0.elapsed(), m.order.len(), m.projects.len()));
    let cursor = s(&boot, "snapshot_endpoint_cursor").to_string();
    let stream = Stream::open(client.base(), client.token(), &cursor)?;
    // Stale kicks from before this connection are nothing now.
    while frames.try_recv().is_ok() {}
    let frames_tx = frames_tx.clone();
    let (commands, commands_rx) = mpsc::channel();
    // Swarm hands back the resume frame that matches the snapshot's scope.
    let mut resume = boot.get("realtime").and_then(|r| r.get("resume")).cloned().unwrap_or_else(|| json!({"kind": "resume", "endpoint_cursor": cursor}));
    resume["capabilities"] = json!(["live_patch_v1"]);
    resume["protocol"] = json!("v3.realtime");
    resume["protocol_version"] = json!(1);
    let _ = commands.send(resume);
    std::thread::spawn(move || {
        let why = stream.pump(frames_tx.clone(), commands_rx);
        let _ = frames_tx.send(json!({"kind": "console.stream_ended", "error": why}));
    });

    let mut subscribed: HashSet<String> = HashSet::new();
    // The list of conversations first, so the console shows something at
    // once; then the open ones in full; the slow reads last.
    tx.send(Update::Snapshot(m.snapshot())).map_err(|_| "the console is gone".to_string())?;
    let t0 = Instant::now();
    m.hydrate_watched(watched, &mut subscribed, &commands)?;
    trace(&format!("hydrate first {:?}", t0.elapsed()));
    tx.send(Update::Snapshot(m.snapshot())).map_err(|_| "the console is gone".to_string())?;
    loop {
        // Wait for a frame, then keep draining for one paint's worth of time
        // (COALESCE), so a burst of patches becomes one update. A frame about
        // one conversation updates only that one; anything else sends the whole.
        let mut dirty: HashSet<String> = HashSet::new();
        let mut automations_changed = false;
        let mut permissions_changed = false;
        let mut run_moved = false;
        let mut whole = false;
        let first = match frames.recv_timeout(Duration::from_millis(250)) {
            Ok(f) => Some(f),
            Err(RecvTimeoutError::Timeout) => None,
            Err(RecvTimeoutError::Disconnected) => return Err("the stream ended".into()),
        };
        let batch = Instant::now();
        let mut pending = first;
        loop {
            let Some(frame) = pending.take() else { break };
            let kind = s(&frame, "kind").to_string();
            let sid = s(&frame, "session_id").to_string();
            // Workers and automations changed (a run started, moved or
            // ended): the Automations page reads again.
            if kind == "worker.updated" || kind == "automation.updated" {
                automations_changed = true;
            }
            // A rule changed (here or anywhere): the console reads them again.
            if kind == "permissions.updated" {
                permissions_changed = true;
            }
            if !sid.is_empty() && run_sub.as_deref() == Some(sid.as_str()) {
                run_moved = true;
            }
            if m.frame(&frame, &commands)? {
                if (kind == "event" || kind == "live.patch") && !sid.is_empty() && m.shells.contains_key(&sid) {
                    dirty.insert(sid);
                } else {
                    whole = true;
                }
            }
            let left = COALESCE.saturating_sub(batch.elapsed());
            pending = match frames.recv_timeout(left) {
                Ok(f) => Some(f),
                Err(RecvTimeoutError::Timeout) => None,
                Err(RecvTimeoutError::Disconnected) => return Err("the stream ended".into()),
            };
        }
        // A command finished: models, roles and usage may have changed.
        whole |= m.hydrate_watched(watched, &mut subscribed, &commands)?;
        if automations_changed && tx.send(Update::AutomationsChanged).is_err() {
            return Ok(());
        }
        if permissions_changed && tx.send(Update::PermissionsChanged).is_err() {
            return Ok(());
        }
        if run_moved {
            if let Some(id) = run_sub.clone() {
                if tx.send(Update::RunActivity(id)).is_err() {
                    return Ok(());
                }
            }
        }
        let wanted = run_watch.lock().unwrap().clone();
        if wanted != run_sub {
            if let Some(old) = run_sub.take() {
                let _ = commands.send(protocol(json!({"kind": "unsubscribe.session", "session_id": old})));
            }
            if let Some(id) = wanted {
                let _ = commands.send(protocol(json!({"kind": "subscribe.session", "session_id": id, "subscription_id": format!("swarm-console:run:{id}")})));
                run_sub = Some(id);
            }
        }
        if whole {
            if tx.send(Update::Snapshot(m.snapshot())).is_err() {
                return Ok(());
            }
        } else {
            for sid in dirty {
                if let Some(session) = m.session_of(&sid) {
                    if tx.send(Update::Session(session)).is_err() {
                        return Ok(());
                    }
                }
            }
        }
    }
}

/// Timing and frame notes on stderr when SWARM_CONSOLE_TRACE is set.
fn trace(line: &str) {
    if std::env::var_os("SWARM_CONSOLE_TRACE").is_some() {
        let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0);
        eprintln!("live {now} {line}");
    }
}

fn protocol(frame: Value) -> Value {
    let mut f = frame;
    f["protocol"] = json!("v3.realtime");
    f["protocol_version"] = json!(1);
    f
}

impl Model<'_> {
    /// Projects, then the sync snapshot the web UI starts from.
    fn bootstrap(&mut self) -> Result<Value, String> {
        let t0 = Instant::now();
        self.projects = map::projects(&self.client.get("/v3/projects?limit=500").map_err(err)?);
        trace(&format!("bootstrap: projects {:?}", t0.elapsed()));
        let t0 = Instant::now();
        let body = json!({
            "surface": "desktop",
            "selector": {"kind": "recent", "global": true, "recent": {"limit": 50}, "attention": {"pending_permissions": true}},
            "history": {"mode": "none"},
            "resources": {"messages": false, "events": false, "run_intents": false, "current_run_state": true, "session_view": false,
                          "active_plan": false, "plan_revisions": false, "permission_summaries": true, "notifications": false,
                          "notification_summary": false, "tasks": false},
            "include_active": true
        });
        let boot = self.client.post("/v3/sync/bootstrap", body).map_err(err)?;
        trace(&format!("bootstrap: sync {:?}", t0.elapsed()));
        self.order.clear();
        self.shells.clear();
        self.runs.clear();
        self.waiting.clear();
        // Archived and deleted sessions come with a tombstone; they are not
        // conversations to show.
        let gone: HashSet<String> = boot
            .get("tombstones_by_session")
            .and_then(Value::as_object)
            .map(|t| t.iter().filter(|(_, v)| matches!(s(v, "kind"), "archived" | "deleted")).map(|(id, _)| id.clone()).collect())
            .unwrap_or_default();
        for id in list(&boot, "session_order").iter().filter_map(Value::as_str).filter(|id| !gone.contains(*id)) {
            self.order.push(id.to_string());
        }
        if let Some(shells) = boot.get("sessions_by_id").and_then(Value::as_object) {
            for (id, shell) in shells.iter().filter(|(id, _)| !gone.contains(*id)) {
                if !self.order.iter().any(|x| x == id) {
                    self.order.push(id.clone());
                }
                self.shells.insert(id.clone(), shell.clone());
            }
        }
        if let Some(runs) = boot.get("current_run_state_by_session").and_then(Value::as_object) {
            for (id, run) in runs {
                self.runs.insert(id.clone(), run.clone());
            }
        }
        if let Some(summaries) = boot.get("permission_summaries_by_session").and_then(Value::as_object) {
            for (id, summary) in summaries {
                if summary.get("pending_approval_count").and_then(Value::as_u64).unwrap_or(0) > 0 {
                    self.waiting.insert(id.clone());
                }
            }
        }
        let ids: Vec<String> = self.projects.iter().map(|p| p.id.to_string()).collect();
        for id in ids {
            self.read_tasks(&id)?;
        }
        Ok(boot)
    }

    /// Reads a project's tasks. The list carries a stale delivery assessment
    /// (what may be merged); Swarm computes the real one on the task's detail
    /// route, so a task waiting on review is read there, once per revision.
    fn read_tasks(&mut self, project_id: &str) -> Result<(), String> {
        let answer = self.client.get(&format!("/v3/projects/{project_id}/tasks")).map_err(err)?;
        let mut tasks = list(&answer, "tasks").to_vec();
        for task in tasks.iter_mut() {
            if s(task, "status") != "needs_review" {
                continue;
            }
            // Only a task the console shows needs its live assessment: one
            // proposed in a conversation it lists. A busy machine has hundreds
            // of other waiting tasks (workers', workflow reviews').
            let origin = s(task, "origin_session_id");
            if origin.is_empty() || !self.shells.contains_key(origin) {
                continue;
            }
            let id = s(task, "id").to_string();
            let revision = task.get("revision").and_then(Value::as_i64).unwrap_or(0);
            let fresh = match self.assessed.get(&id) {
                Some((r, detail)) if *r == revision => detail.clone(),
                _ => {
                    let detail = self.client.get(&format!("/v3/projects/{project_id}/tasks/{id}")).map_err(err)?;
                    let detail = detail.get("task").cloned().unwrap_or(Value::Null);
                    self.assessed.insert(id, (revision, detail.clone()));
                    detail
                }
            };
            if !fresh.is_null() {
                *task = fresh;
            }
        }
        self.tasks.insert(project_id.to_string(), tasks);
        self.read_media();
        Ok(())
    }

    /// The media tasks of every project, newest first, and what they made.
    fn read_media(&mut self) {
        let mut found: Vec<(String, String, super::media::MediaTask)> = self
            .tasks
            .iter()
            .flat_map(|(project_id, tasks)| tasks.iter().map(move |t| (project_id, t)))
            .filter(|(_, t)| super::media::is_media_agent(s(t, "agent")) && !t.get("archived").and_then(Value::as_bool).unwrap_or(false))
            .map(|(project_id, t)| {
                (
                    s(t, "origin_session_id").to_string(),
                    project_id.clone(),
                    super::media::MediaTask {
                        task_id: s(t, "id").to_string(),
                        agent: s(t, "agent").to_string(),
                        status: s(t, "status").to_string(),
                        title: s(t, "title").to_string(),
                        created_at: t.get("created_at").and_then(Value::as_u64).unwrap_or(0),
                        deliverables: t.get("deliverables").and_then(Value::as_array).cloned().unwrap_or_default(),
                    },
                )
            })
            .collect();
        found.sort_by(|a, b| b.2.created_at.cmp(&a.2.created_at));
        let mut wanted = Vec::new();
        self.media = super::media::collect(&mut self.media_reads, &found, &mut wanted);
        for (url, stem) in wanted {
            if self.media_fetching.insert(stem.clone()) {
                let _ = self.media_fetch.send((url, stem));
            }
        }
    }


    /// Reads the open conversations that are not read yet, and subscribes to
    /// them. True when something was read.
    fn hydrate_watched(&mut self, watched: &Mutex<HashSet<String>>, subscribed: &mut HashSet<String>, commands: &Sender<Value>) -> Result<bool, String> {
        let open: HashSet<String> = watched.lock().map(|w| w.clone()).unwrap_or_default();
        // A conversation asleep on tasks stays subscribed, so its wake shows.
        let mut active: Vec<String> = self.runs.iter().filter(|(_, r)| map::run_clock(r, 0).active || map::task_wait(r).is_some()).map(|(id, _)| id.clone()).collect();
        active.retain(|id| self.shells.contains_key(id));
        let mut wanted: Vec<String> = open.iter().filter(|id| self.shells.contains_key(*id)).cloned().collect();
        for id in active {
            if !wanted.contains(&id) {
                wanted.push(id);
            }
        }
        for id in &wanted {
            if subscribed.insert(id.clone()) {
                let _ = commands.send(protocol(json!({"kind": "subscribe.session", "session_id": id, "subscription_id": format!("swarm-console:session:{id}")})));
            }
        }
        for id in subscribed.clone() {
            if !wanted.contains(&id) {
                subscribed.remove(&id);
                let _ = commands.send(protocol(json!({"kind": "unsubscribe.session", "session_id": id})));
            }
        }
        let fresh: Vec<String> = wanted.into_iter().filter(|id| !self.details.get(id).is_some_and(|d| d.hydrated)).collect();
        for chunk in fresh.chunks(HYDRATE_CHUNK) {
            let t0 = Instant::now();
            self.hydrate(chunk)?;
            trace(&format!("hydrate {} sessions {:?}", chunk.len(), t0.elapsed()));
        }
        Ok(!fresh.is_empty())
    }

    /// Reads conversations in full: their messages, views (pending requests,
    /// model).
    fn hydrate(&mut self, ids: &[String]) -> Result<(), String> {
        let body = json!({
            "surface": "desktop",
            "session_ids": ids,
            // Swarm's tail is at most 200 messages per session; older ones are in its history chunks.
            "history": {"mode": "tail", "max_messages_per_session": 200, "max_events_per_session": 0, "manifest_policy": "manifest"},
            "resources": {"messages": true, "events": false, "run_intents": false, "current_run_state": true, "session_view": true,
                          "active_plan": false, "plan_revisions": false, "permission_summaries": false},
            "include_active": true
        });
        let snap = self.client.post("/v3/sync/hydrate", body).map_err(err)?;
        for id in ids {
            let messages = snap.get("messages_by_session").and_then(|m| m.get(id)).and_then(Value::as_array).cloned().unwrap_or_default();
            let view = snap.get("session_views_by_id").and_then(|v| v.get(id)).cloned().unwrap_or(Value::Null);
            let permissions = list(&view, "pending_permissions").to_vec();
            if let Some(run) = snap.get("current_run_state_by_session").and_then(|r| r.get(id)) {
                self.runs.insert(id.clone(), run.clone());
            }
            if let Some(shell) = snap.get("sessions_by_id").and_then(|x| x.get(id)) {
                self.shells.insert(id.clone(), shell.clone());
            }
            let d = self.details.entry(id.clone()).or_default();
            d.entries = map::entries(&messages);
            d.model = map::model_line(&view);
            d.pending = map::pending(&permissions);
            d.calls = map::client_calls(&permissions);
            d.hydrated = true;
            if d.pending.is_some() || !d.calls.is_empty() {
                self.waiting.insert(id.clone());
            } else {
                self.waiting.remove(id);
            }
        }
        Ok(())
    }

    /// Re-reads one conversation's view: its model (and pending requests),
    /// after Swarm says its model profile, preference or agent changed.
    fn refresh_view(&mut self, id: &str) -> Result<(), String> {
        let body = json!({
            "surface": "desktop",
            "session_ids": [id],
            "history": {"mode": "none"},
            "resources": {"messages": false, "events": false, "run_intents": false, "current_run_state": true, "session_view": true,
                          "active_plan": false, "plan_revisions": false, "permission_summaries": false},
            "include_active": true
        });
        let snap = self.client.post("/v3/sync/hydrate", body).map_err(err)?;
        let view = snap.get("session_views_by_id").and_then(|v| v.get(id)).cloned().unwrap_or(Value::Null);
        if let Some(d) = self.details.get_mut(id) {
            let model = map::model_line(&view);
            if !model.is_empty() {
                d.model = model;
            }
        }
        Ok(())
    }

    /// Reads one session's record again and puts it in the list: after a
    /// change without an event (a pin), one brought back from the archive,
    /// or one asked for from Search.
    fn reread_session(&mut self, id: &str) -> Result<bool, String> {
        let answer = self.client.get(&format!("/v3/sessions/{id}?message_limit=0")).map_err(err)?;
        let Some(shell) = answer.get("session").filter(|v| v.is_object()) else { return Ok(false) };
        self.shells.insert(id.to_string(), shell.clone());
        if !self.order.iter().any(|x| x == id) {
            self.order.insert(0, id.to_string());
        }
        Ok(true)
    }

    fn read_pending(&mut self, id: &str) -> Result<(), String> {
        let answer = self.client.get(&format!("/v3/sessions/{id}/permissions?status=pending&limit=50")).map_err(err)?;
        let permissions = list(&answer, "permissions").to_vec();
        let account = self.client.get("/v3/permissions").map_err(err)?;
        let device = list(&account, "devices").iter()
            .find(|d| d.get("this_device").and_then(Value::as_bool) == Some(true))
            .map(|d| s(d, "id").to_string()).unwrap_or_default();
        let d = self.details.entry(id.to_string()).or_default();
        d.pending = map::pending(&permissions);
        d.calls = map::client_calls(&permissions);
        for call in &mut d.calls {
            call.this_device = !device.is_empty() && call.target_device.as_ref() == device.as_str();
        }
        if d.pending.is_some() || !d.calls.is_empty() {
            self.waiting.insert(id.to_string());
        } else {
            self.waiting.remove(id);
        }
        Ok(())
    }

    /// One frame from the stream. True when the console should hear of it.
    /// Err when the stream must be reopened from a fresh snapshot.
    fn frame(&mut self, frame: &Value, _commands: &Sender<Value>) -> Result<bool, String> {
        let sid = s(frame, "session_id").to_string();
        if std::env::var_os("SWARM_CONSOLE_TRACE").is_some() {
            let hydrated = self.details.get(&sid).is_some_and(|d| d.hydrated);
            eprintln!(
                "frame {} {} sid={} hydrated={} entries={}",
                s(frame, "kind"),
                s(frame, "event_type"),
                &sid[..sid.len().min(8)],
                hydrated,
                self.details.get(&sid).map(|d| d.entries.len()).unwrap_or(0)
            );
        }
        match s(frame, "kind") {
            "hello" | "keepalive" | "endpoint.watermark" | "replay.started" | "replay.complete" => Ok(false),
            "console.stream_ended" => Err(s(frame, "error").to_string()),
            // A media file finished downloading.
            "console.media_ready" => {
                self.read_media();
                Ok(true)
            }
            // The console woke us: a watch change, or a command that ran (then
            // the facts may have changed too).
            "console.kick" => {
                if frame.get("command").and_then(Value::as_bool) == Some(true) {
                    let _ = self.facts_kick.send(());
                }
                Ok(true)
            }
            // Chrome's link changed (repaint), or the person pressed stop in
            // the extension: this device's Browser rule turns off in Swarm,
            // so Swarm's record says what the browser will do.
            "console.browser" => {
                if s(frame, "event") == "stopped" {
                    browser_off(self.client);
                }
                Ok(true)
            }
            // A session whose record changed without an event (a pin): read again.
            "console.session" => self.reread_session(&sid),
            "cursor.error" => Err(format!("the stream lost its place ({})", s(frame, "error_code"))),
            "slow_consumer.reconnect_required" => Err("Swarm asked this app to reconnect".into()),
            "auth.denied" => Err(format!("Swarm refused the stream ({})", s(frame, "error_code"))),
            "live.patch" => {
                let live = frame.get("live").cloned().unwrap_or(Value::Null);
                let Some(d) = self.details.get_mut(&sid) else { return Ok(false) };
                let start = live.get("offset_start").and_then(Value::as_u64).unwrap_or(0) as usize;
                let text = s(&live, "text");
                if start == d.live.len() {
                    d.live.push_str(text);
                } else if start < d.live.len() && start + text.len() > d.live.len() {
                    d.live.push_str(&text[d.live.len() - start..]);
                }
                Ok(true)
            }
            "workset.session.discovered" | "workset.session.updated" | "workset.session.removed" => {
                if sid.is_empty() {
                    return Ok(false);
                }
                if s(frame, "kind") == "workset.session.removed" {
                    self.shells.remove(&sid);
                    self.order.retain(|x| x != &sid);
                    self.details.remove(&sid);
                    return Ok(true);
                }
                if let Some(shell) = frame.get("session").filter(|x| !x.is_null()) {
                    if !self.shells.contains_key(&sid) {
                        self.order.insert(0, sid.clone());
                    }
                    self.shells.insert(sid.clone(), shell.clone());
                }
                if let Some(run) = frame.get("current_run_state").filter(|x| !x.is_null()) {
                    self.runs.insert(sid.clone(), run.clone());
                }
                if let Some(summary) = frame.get("permission_summary").filter(|x| !x.is_null()) {
                    if summary.get("pending_approval_count").and_then(Value::as_u64).unwrap_or(0) > 0 {
                        self.waiting.insert(sid.clone());
                    } else {
                        self.waiting.remove(&sid);
                    }
                }
                Ok(true)
            }
            "project.updated" => {
                let pid = s(frame, "project_id").to_string();
                if pid.is_empty() {
                    let ids: Vec<String> = self.projects.iter().map(|p| p.id.to_string()).collect();
                    self.projects = map::projects(&self.client.get("/v3/projects?limit=500").map_err(err)?);
                    for id in ids {
                        self.read_tasks(&id)?;
                    }
                } else {
                    // The record changed (a name, project.md, folders): read the list again.
                    self.projects = map::projects(&self.client.get("/v3/projects?limit=500").map_err(err)?);
                    if self.projects.iter().any(|p| p.id.as_ref() == pid.as_str()) {
                        self.read_tasks(&pid)?;
                    } else {
                        self.tasks.remove(&pid);
                    }
                }
                Ok(true)
            }
            "usage.scope.updated" => {
                let _ = self.facts_kick.send(());
                Ok(false)
            }
            "event" => self.event(&sid, s(frame, "event_type"), frame.get("event").and_then(|e| e.get("payload")).cloned().unwrap_or(Value::Null)),
            _ => Ok(false),
        }
    }

    /// One durable session event.
    fn event(&mut self, sid: &str, kind: &str, payload: Value) -> Result<bool, String> {
        if sid.is_empty() {
            return Ok(false);
        }
        if let Some(run) = payload.get("run_intent").filter(|r| !r.is_null()) {
            self.runs.insert(sid.to_string(), run.clone());
        }
        if let Some(shell) = payload.get("session").filter(|x| x.is_object()) {
            if map::orchestrator_project(shell).is_some() {
                if !self.shells.contains_key(sid) {
                    self.order.insert(0, sid.to_string());
                }
                self.shells.insert(sid.to_string(), shell.clone());
            }
        }
        let watched = self.details.get(sid).is_some_and(|d| d.hydrated);
        match kind {
            // A wait's registration and its wake commit their message (the
            // wait line, the outcome) in the same event as the run intent.
            "session.message.appended" | "session.run.waiting_tasks" | "session.run.tasks_ready" => {
                let Some(message) = payload.get("message").filter(|m| m.is_object()) else { return Ok(true) };
                if watched {
                    let d = self.details.get_mut(sid).unwrap();
                    let first = d.entries.is_empty();
                    if s(message, "role") == "assistant" {
                        d.live.clear();
                    }
                    map::apply(&mut d.entries, message, first);
                }
                Ok(true)
            }
            "session.assistant.delta" => {
                if let Some(d) = self.details.get_mut(sid) {
                    let start = payload.get("offset_start").and_then(Value::as_u64).unwrap_or(0) as usize;
                    let delta = s(&payload, "delta");
                    if start == d.live.len() {
                        d.live.push_str(delta);
                    } else if start < d.live.len() && start + delta.len() > d.live.len() {
                        d.live.push_str(&delta[d.live.len() - start..]);
                    }
                }
                Ok(true)
            }
            "session.assistant.completed" => {
                if let Some(d) = self.details.get_mut(sid) {
                    d.live.clear();
                    if let Some(message) = payload.get("message") {
                        map::apply(&mut d.entries, message, false);
                    }
                }
                Ok(true)
            }
            "session.tool.started" | "session.tool.completed" | "session.tool.failed" | "session.tool.cancelled" | "session.tool.canceled" => {
                if let Some(d) = self.details.get_mut(sid) {
                    if !s(&payload, "tool_name").is_empty() {
                        // The text before a tool call is its own segment; what follows streams anew.
                        if !d.live.is_empty() {
                            let text = std::mem::take(&mut d.live);
                            d.entries.push(Entry::Assistant(text.into()));
                        }
                        if kind == "session.tool.started" {
                            tool_started(&mut d.entries, &payload);
                        } else {
                            tool_ended(&mut d.entries, kind, &payload);
                        }
                    }
                }
                Ok(true)
            }
            // A running call's output so far: the line keeps showing it runs.
            "session.tool.delta" => Ok(payload.get("run_intent").is_some()),
            "session.run.failed" => {
                if let Some(d) = self.details.get_mut(sid) {
                    d.live.clear();
                    run_ended(&mut d.entries);
                    let why = s(&payload, "error");
                    if !why.is_empty() {
                        d.entries.push(Entry::tool("error", why.lines().next().unwrap_or("").to_string()));
                    }
                }
                Ok(true)
            }
            "session.run.cancelled" | "session.run.interrupted" => {
                if let Some(d) = self.details.get_mut(sid) {
                    d.live.clear();
                    run_ended(&mut d.entries);
                }
                Ok(true)
            }
            "session.run.completed" => {
                if let Some(d) = self.details.get_mut(sid) {
                    run_ended(&mut d.entries);
                }
                Ok(true)
            }
            "permission.requested" | "permission.updated" | "permission.summary.updated" => {
                if watched {
                    self.read_pending(sid)?;
                } else if kind == "permission.requested" {
                    self.waiting.insert(sid.to_string());
                }
                Ok(true)
            }
            "session.title.updated" => {
                if let Some(shell) = self.shells.get_mut(sid) {
                    if !s(&payload, "title").is_empty() {
                        shell["title"] = payload["title"].clone();
                    }
                }
                Ok(true)
            }
            "session.model_profile.updated" | "session.preference.updated" | "session.agent.updated" => {
                if watched {
                    self.refresh_view(sid)?;
                }
                Ok(true)
            }
            "session.metadata.updated" => Ok(true),
            "session.reactivated" => self.reread_session(sid),
            "session.archived" | "session.deleted" => {
                self.shells.remove(sid);
                self.order.retain(|x| x != sid);
                self.details.remove(sid);
                Ok(true)
            }
            _ => Ok(payload.get("run_intent").is_some()),
        }
    }

    /// One conversation as the console shows it.
    fn session_of(&self, id: &str) -> Option<Session> {
        let shell = self.shells.get(id)?;
        let run = self.runs.get(id).cloned().unwrap_or(Value::Null);
        let mut session = map::session(shell, &run, self.waiting.contains(id), &self.projects)?;
        if let Some(d) = self.details.get(id) {
            session.entries = d.entries.clone();
            if !d.live.is_empty() {
                session.entries.push(Entry::Assistant(d.live.clone().into()));
            }
            session.model = d.model.clone().into();
            session.pending = d.pending.clone();
            session.calls = d.calls.clone();
            if session.state != RunState::Running && (d.pending.is_some() || !d.calls.is_empty()) {
                session.state = RunState::NeedsYou;
            }
        }
        if let Some(project) = self.projects.get(session.project) {
            let tasks: Vec<Task> = self.tasks.get(project.id.as_ref()).map(|t| map::session_tasks(t, id)).unwrap_or_default();
            session.tasks = tasks;
        }
        if let Some(wait) = session.waiting_on.as_mut() {
            let project: &[Value] = self.tasks.get(wait.project_id.as_ref()).map(Vec::as_slice).unwrap_or(&[]);
            name_waited_tasks(wait, &session.tasks, project);
        }
        Some(session)
    }

    fn snapshot(&self) -> Snapshot {
        let sessions: Vec<Session> = self.order.iter().filter_map(|id| self.session_of(id)).collect();
        Snapshot {
            projects: self.projects.clone(),
            sessions,
            machine: None,
            roles: None,
            usage: None,
            themes: None,
            media: self.media.clone(),
            catalog: None,
        }
    }
}

/// Titles for the tasks a conversation waits on, from its own tasks or its
/// wait's project. A wait on a linked machine names that machine's tasks,
/// which this machine's records do not hold: those keep their ids.
fn name_waited_tasks(wait: &mut crate::data::TaskWait, own: &[Task], project: &[Value]) {
    if !wait.machine.is_empty() {
        return;
    }
    for (id, title) in wait.tasks.iter_mut() {
        let id: &str = id.as_ref();
        let found = own
            .iter()
            .find(|t| { let key: &str = t.key.as_ref(); key == id })
            .map(|t| t.title.to_string())
            .or_else(|| project.iter().find(|t| s(t, "id") == id).map(|t| s(t, "title").to_string()))
            .unwrap_or_default();
        if !found.is_empty() {
            *title = found.into();
        }
    }
}

/// A tool event's call id; Swarm writes "tool_call" when the provider gave none.
fn call_id(payload: &Value) -> &str {
    match s(payload, "call_id") {
        "tool_call" => "",
        id => id,
    }
}

/// What a tool call works on: from its arguments, else Swarm's display line.
fn call_detail(payload: &Value) -> String {
    let detail = map::tool_detail(payload.get("arguments").unwrap_or(&Value::Null));
    if !detail.is_empty() {
        return detail;
    }
    let display = s(payload, "tool_display");
    if display == s(payload, "tool_name") { String::new() } else { display.to_string() }
}

/// The newest running line of a call: by its id, or by its name when Swarm
/// gave it none.
fn running_call(entries: &[Entry], id: &str, tool: &str) -> Option<usize> {
    entries.iter().rposition(|e| match e {
        Entry::Tool { call_id, name, state: ToolState::Running, .. } => {
            if id.is_empty() { call_id.is_empty() && name.as_ref() == tool } else { call_id.as_ref() == id }
        }
        _ => false,
    })
}

/// session.tool.started: a running line, once per call.
fn tool_started(entries: &mut Vec<Entry>, payload: &Value) {
    let (id, name) = (call_id(payload), s(payload, "tool_name"));
    if !id.is_empty() && running_call(entries, id, name).is_some() {
        return;
    }
    entries.push(Entry::Tool {
        name: name.to_string().into(),
        detail: call_detail(payload).into(),
        call_id: id.to_string().into(),
        state: ToolState::Running,
        summary: None,
        duration_ms: None,
        started_at: Some(payload.get("recorded_at").and_then(Value::as_u64).unwrap_or_else(crate::data::now_ms)),
    });
}

/// session.tool.completed, failed or cancelled: the call's running line
/// ends with its summary and duration (a new line when it had none). An end
/// repeated for a call whose line already ended changes nothing.
fn tool_ended(entries: &mut Vec<Entry>, kind: &str, payload: &Value) {
    let (id, name) = (call_id(payload), s(payload, "tool_name"));
    if !id.is_empty() && running_call(entries, id, name).is_none() && entries.iter().any(|e| matches!(e, Entry::Tool { call_id, .. } if call_id.as_ref() == id)) {
        return;
    }
    let (new_state, new_summary, duration) = map::tool_event_end(kind, payload);
    let ended_at = payload.get("recorded_at").and_then(Value::as_u64);
    // Media it asked for: a card under its line, once (history has it too).
    let card = (new_state == ToolState::Done)
        .then(|| map::media_card(name, payload.get("arguments").unwrap_or(&Value::Null), s(payload, "output")))
        .flatten()
        .filter(|card| !entries.iter().any(|e| matches!((e, card), (Entry::Media { task_id: a, .. }, Entry::Media { task_id: b, .. }) if a == b)));
    if let Some(i) = running_call(entries, id, name) {
        if let Entry::Tool { detail, state, summary, duration_ms, started_at, .. } = &mut entries[i] {
            // The finished call's own arguments say what it did better than a
            // display label it started with ("manage_projects x2").
            let finished = map::tool_detail(payload.get("arguments").unwrap_or(&Value::Null));
            if !finished.is_empty() {
                *detail = finished.into();
            } else if detail.is_empty() {
                *detail = call_detail(payload).into();
            }
            *state = new_state;
            *summary = new_summary;
            *duration_ms = duration.or_else(|| Some(ended_at?.checked_sub((*started_at)?)?));
        }
        if let Some(card) = card {
            entries.insert(i + 1, card);
        }
        return;
    }
    entries.push(Entry::Tool {
        name: name.to_string().into(),
        detail: call_detail(payload).into(),
        call_id: id.to_string().into(),
        state: new_state,
        summary: new_summary,
        duration_ms: duration,
        started_at: None,
    });
    entries.extend(card);
}

/// The run ended: a call Swarm never finished is no longer running.
fn run_ended(entries: &mut [Entry]) {
    for e in entries {
        if let Entry::Tool { state, .. } = e {
            if *state == ToolState::Running {
                *state = ToolState::Cancelled;
            }
        }
    }
}

/// The facts thread: the machine, roles, usage, themes and models, read every
/// SLOW_EVERY and when asked (a command ran, usage changed), on their own so a
/// conversation never waits for them. The catalog is big and changes only
/// when the person changes it: read once, and again when a command did.
fn facts_loop(client: Client, kind: &'static str, tx: Sender<Update>, kick: Receiver<()>, models_dirty: Arc<std::sync::atomic::AtomicBool>) {
    let mut catalog: Option<Catalog> = None;
    loop {
        let t0 = Instant::now();
        let mut facts = Facts::default();
        let status = client.get("/v3/box/summary").unwrap_or(Value::Null);
        if !status.is_null() {
            facts.machine = Some(map::machine(&status, kind));
        }
        facts.roles = client.get("/v1/agent-model-settings").ok().map(|v| map::roles(&v));
        if catalog.is_none() || models_dirty.swap(false, std::sync::atomic::Ordering::Relaxed) {
            if let Ok(c) = models::read(&client) {
                catalog = Some(c);
            }
        }
        facts.catalog = catalog.clone();
        let today = client.get("/v3/usage?time_range=today").unwrap_or(Value::Null);
        let month = client.get("/v3/usage?time_range=30d").unwrap_or(Value::Null);
        let chatgpt = client.get("/v1/codex/account/usage").map_err(|e| e.to_string());
        facts.usage = Some(map::usage(&today, &month, &status, chatgpt.as_ref().map_err(String::as_str)));
        if let Ok(settings) = client.get("/v1/ui/settings") {
            let themes_in = settings.get("theme").cloned().unwrap_or(Value::Null);
            let mut themes = HashMap::new();
            let active = s(&themes_in, "active_id").to_string();
            let default = s(&themes_in, "default_theme_id").to_string();
            for key in ["custom_themes", "builtin_themes"] {
                for theme in list(&themes_in, key) {
                    let id = s(theme, "id").to_string();
                    let entry = json!({"id": id, "name": theme.get("name"), "palette": theme.get("palette"), "custom": key == "custom_themes"});
                    if id == active || (active.is_empty() && id == default) {
                        themes.insert(String::new(), entry.clone());
                    }
                    themes.insert(id, entry);
                }
            }
            facts.themes = Some(themes);
        }
        trace(&format!("facts {:?}", t0.elapsed()));
        if tx.send(Update::Facts(facts)).is_err() {
            return;
        }
        match kick.recv_timeout(SLOW_EVERY) {
            Err(RecvTimeoutError::Disconnected) => return,
            _ => {}
        }
        // Several asks in a row are one read.
        while kick.try_recv().is_ok() {}
    }
}

#[cfg(test)]
mod tests {
    use super::text_file;

    // Purpose: `■ stop wait` really stops a run asleep on tasks. Requirement:
    // Swarm's session detail lists only an active run (`active_run_intent` is
    // empty for `waiting_tasks`), so the stop body falls back to the waiting
    // run's id the console holds from the run state; an active run still wins;
    // with neither, nothing is posted. Threat: the button reporting "Nothing
    // is running." and never reaching `run/stop`. Authority: stop_body, the
    // pure part of stop(); the HTTP call only sends what it returns.
    #[test]
    fn stop_sends_the_waiting_run_when_none_is_active() {
        use super::stop_body;
        use serde_json::json;
        let waiting = json!({"active_run_intent": null, "session": {"metadata": {"swarm_v3_runtime_swarm_id": "swarm-1"}}});
        let body = stop_body(&waiting, "run-1").expect("a waiting run is stopped");
        assert_eq!((body["type"].as_str(), body["run_id"].as_str(), body["target_swarm_id"].as_str()), (Some("run.stop"), Some("run-1"), Some("swarm-1")));
        let active = json!({"active_run_intent": {"run_id": "run-2"}, "session": {"metadata": {}}});
        assert_eq!(stop_body(&active, "run-1").unwrap()["run_id"].as_str(), Some("run-2"));
        assert_eq!(stop_body(&active, "").unwrap()["run_id"].as_str(), Some("run-2"));
        assert!(stop_body(&waiting, "").is_none());
        assert!(stop_body(&waiting, "  ").is_none());
    }

    #[test]
    fn text_files_go_in_the_message() {
        assert_eq!(text_file("notes.MD").as_deref(), Some("md"));
        assert_eq!(text_file("main.rs").as_deref(), Some("rs"));
        assert_eq!(text_file("app.env.example").as_deref(), Some(""));
        assert_eq!(text_file("take.wav"), None);
        assert_eq!(text_file("photo.png"), None);
        assert_eq!(text_file("Makefile"), None);
    }

    mod tools {
        use super::super::{run_ended, tool_ended, tool_started};
        use crate::data::{Entry, ToolState};
        use serde_json::json;

        fn only(entries: &[Entry]) -> (&str, &str, ToolState, Option<&str>, Option<u64>) {
            match entries {
                [Entry::Tool { name, detail, state, summary, duration_ms, .. }] => (name.as_ref(), detail.as_ref(), *state, summary.as_ref().map(|s| s.as_ref()), *duration_ms),
                _ => panic!("expected one tool line, got {}", entries.len()),
            }
        }

        #[test]
        fn a_call_runs_then_ends_on_its_line() {
            let mut e = Vec::new();
            let args = json!({"command": "go test ./..."}).to_string();
            tool_started(&mut e, &json!({"tool_name": "bash", "call_id": "c1", "arguments": args, "recorded_at": 1000}));
            tool_started(&mut e, &json!({"tool_name": "bash", "call_id": "c1", "arguments": args, "recorded_at": 1000}));
            assert_eq!(only(&e), ("bash", "go test ./...", ToolState::Running, None, None));
            let output = json!({"exit_code": 0, "output": "ok  all\n"}).to_string();
            tool_ended(&mut e, "session.tool.completed", &json!({"tool_name": "bash", "call_id": "c1", "arguments": args, "output": output, "duration_ms": 1234, "exit_code": 0}));
            assert_eq!(only(&e), ("bash", "go test ./...", ToolState::Done, Some("exit 0 · ok all"), Some(1234)));
            assert_eq!(e[0].tool_line(0).unwrap().text, "bash go test ./... · exit 0 · ok all · 1.2s");
        }

        #[test]
        fn a_failure_without_a_start_is_its_own_line() {
            let mut e = Vec::new();
            tool_ended(&mut e, "session.tool.failed", &json!({"tool_name": "read", "call_id": "c2", "arguments": "{\"path\":\"/x/a.md\"}", "error": "no such file\nmore"}));
            assert_eq!(only(&e), ("read", "/x/a.md", ToolState::Failed, Some("no such file"), None));
        }

        #[test]
        fn duration_from_swarm_times_when_not_given() {
            let mut e = Vec::new();
            tool_started(&mut e, &json!({"tool_name": "search", "call_id": "c3", "tool_display": "search", "recorded_at": 5000}));
            assert_eq!(e[0].tool_line(9500).unwrap().text, "search · 4s");
            tool_ended(&mut e, "session.tool.completed", &json!({"tool_name": "search", "call_id": "c3", "output": "{\"count\":2}", "recorded_at": 7500}));
            assert_eq!(only(&e).4, Some(2500));
        }

        // Purpose: a manage_media generate call that finishes live gets its
        // media card right under its line, once, matching what history
        // replay draws. Threat: no card until a reload, or a second card or
        // a second finished line when the end event repeats. Authority:
        // tool_ended with map::media_card.
        #[test]
        fn a_finished_media_call_gets_its_card_once() {
            let mut e = Vec::new();
            let args = json!({"action": "generate", "kind": "sound", "brief": "a chime"}).to_string();
            tool_started(&mut e, &json!({"tool_name": "manage_media", "call_id": "m1", "arguments": args, "recorded_at": 1}));
            tool_started(&mut e, &json!({"tool_name": "read", "call_id": "r1", "arguments": "{\"path\":\"a.md\"}", "recorded_at": 2}));
            let end = json!({"tool_name": "manage_media", "call_id": "m1", "arguments": args, "output": "{\"job_id\":\"media/p1/task_s1\",\"status\":\"running\"}"});
            tool_ended(&mut e, "session.tool.completed", &end);
            tool_ended(&mut e, "session.tool.completed", &end);
            assert_eq!(e.len(), 3);
            assert!(matches!(&e[0], Entry::Tool { name, state: ToolState::Done, .. } if name.as_ref() == "manage_media"));
            assert!(matches!(&e[1], Entry::Media { task_id, kind: crate::data::MediaKind::Sound, .. } if task_id.as_ref() == "task_s1"));
            assert!(matches!(&e[2], Entry::Tool { name, .. } if name.as_ref() == "read"));
        }

        #[test]
        fn a_run_that_ends_stops_its_running_calls() {
            let mut e = Vec::new();
            tool_started(&mut e, &json!({"tool_name": "bash", "call_id": "tool_call", "arguments": "{\"command\":\"sleep 9\"}", "recorded_at": 1}));
            run_ended(&mut e);
            assert_eq!(only(&e).2, ToolState::Cancelled);
            assert_eq!(e[0].tool_line(0).unwrap().text, "bash sleep 9 · stopped");
        }
    }

    // Purpose: an Orchestrator asleep on wait_tasks shows as Waiting, not
    // Running, whether the console learns it from Swarm's snapshot (after a
    // reconnect or restart) or from the live event that pends the run, and it
    // shows Running again once the run resumes. Threat: a pended conversation
    // looking busy, or the wait being lost when the console reconnects.
    // Authority: Model::event and Model::session_of over map::session; the
    // model is the narrowest layer that joins run state, tasks and titles.
    // None of these events reach Swarm, so the client is never contacted.
    mod waiting {
        use super::super::{Client, Detail, Model, map};
        use crate::data::{Entry, RunState, Text};
        use serde_json::{Value, json};
        use std::collections::{HashMap, HashSet};
        use std::sync::mpsc;

        fn model(client: &Client, run: Value) -> Model<'_> {
            let projects = map::projects(&json!({"projects": [{"id": "proj_1", "name": "App", "workspaces": [{"path": "/p/app"}]}]}));
            let mut m = Model {
                client,
                kind: "home",
                projects,
                order: vec!["s1".into()],
                shells: HashMap::new(),
                runs: HashMap::new(),
                waiting: HashSet::new(),
                details: HashMap::new(),
                tasks: HashMap::new(),
                media: Vec::new(),
                media_reads: HashMap::new(),
                media_fetching: HashSet::new(),
                media_fetch: mpsc::channel().0,
                assessed: HashMap::new(),
                facts_kick: mpsc::channel().0,
            };
            m.shells.insert("s1".into(), json!({"id": "s1", "title": "Ship it", "metadata": {"role": "project_orchestrator", "project_id": "proj_1"}}));
            m.tasks.insert("proj_1".into(), vec![json!({"id": "t1", "title": "Fix the build", "origin_session_id": "s1", "agent": "coder", "status": "running"})]);
            m.runs.insert("s1".into(), run);
            m
        }

        fn waiting_state(machine: &str) -> Value {
            json!({"run_id": "run-1", "active": false, "status": "waiting_tasks", "task_wait": {"project_id": "proj_1", "machine": machine, "tasks": [{"task_id": "t1", "attempt_id": "a1", "session_id": "c1"}]}})
        }

        #[test]
        fn a_snapshot_waiting_on_tasks_reads_as_waiting() {
            let client = Client::new("http://127.0.0.1:9", "unused");
            let m = model(&client, waiting_state(""));
            let s = m.session_of("s1").unwrap();
            assert_eq!(s.state, RunState::Waiting);
            assert!(!s.run.unwrap().active, "a waiting run's clock does not tick");
            let wait = s.waiting_on.expect("what it waits on");
            assert_eq!(wait.tasks, vec![(Text::from("t1"), Text::from("Fix the build"))]);
            assert_eq!(wait.line(), "waiting for tasks: Fix the build");
        }

        #[test]
        fn a_wait_on_a_linked_machine_names_the_machine() {
            let client = Client::new("http://127.0.0.1:9", "unused");
            let m = model(&client, waiting_state("forge"));
            let s = m.session_of("s1").unwrap();
            assert_eq!(s.state, RunState::Waiting);
            // Its tasks live on that machine: this machine's same-id task does not name them.
            assert_eq!(s.waiting_on.unwrap().line(), "waiting for tasks: t1 · on forge");
        }

        #[test]
        fn live_events_move_a_run_into_the_wait_and_out_again() {
            let client = Client::new("http://127.0.0.1:9", "unused");
            let mut m = model(&client, json!({"run_id": "run-1", "active": true, "status": "running", "started_at": 1000}));
            assert_eq!(m.session_of("s1").unwrap().state, RunState::Running);

            m.details.insert("s1".into(), Detail { hydrated: true, ..Default::default() });
            // The run pends on wait_tasks: the event carries its run intent (no
            // `active`) and the wait line Swarm records for the person.
            let intent = json!({"run_id": "run-1", "status": "waiting_tasks", "task_wait": {"project_id": "proj_1", "tasks": [{"task_id": "t1"}]}});
            let line = json!({"id": "run-1-wait-notice", "role": "system", "content": "This conversation is now waiting on 1 task(s): Fix the build. It wakes when they settle; a new message ends the wait.", "metadata": {"source": "task_wait_notice", "task_wait_notice": "waiting"}});
            assert!(m.event("s1", "session.run.waiting_tasks", json!({"run_intent": intent, "message": line})).unwrap());
            let s = m.session_of("s1").unwrap();
            assert_eq!(s.state, RunState::Waiting);
            assert!(matches!(s.entries.last(), Some(Entry::Notice(t)) if t.starts_with("This conversation is now waiting on 1 task(s): Fix the build.")));
            let wait = s.waiting_on.unwrap();
            assert_eq!(wait.run_id.as_ref(), "run-1");
            assert_eq!(wait.tasks[0].1.as_ref(), "Fix the build");
            assert_eq!(crate::data::composer_badge(s.state, Some(&wait)).as_deref(), Some("⏳ Waiting · 1 task"));

            // The tasks settle and the run resumes: Running again, nothing awaited.
            let resumed = json!({"run_id": "run-1:resume", "status": "running", "created_at": 2000});
            assert!(m.event("s1", "session.run_intent.updated", json!({"run_intent": resumed})).unwrap());
            let s = m.session_of("s1").unwrap();
            assert_eq!(s.state, RunState::Running);
            assert!(s.waiting_on.is_none());

            // And it finishes: Idle.
            let done = json!({"run_id": "run-1:resume", "status": "completed", "created_at": 2000});
            assert!(m.event("s1", "session.run.completed", json!({"run_intent": done})).unwrap());
            assert_eq!(m.session_of("s1").unwrap().state, RunState::Idle);
        }
    }
}
