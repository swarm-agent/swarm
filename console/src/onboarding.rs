//! First run: find Swarm, install or connect it, set up a provider and a
//! first project, and wait for project.md. One centered card that never
//! changes size; every wait says what it is doing; errors sit in a reserved
//! line so nothing shifts. The steps follow swarm-go's docs/frontends.md.

use std::sync::mpsc;
use std::time::{Duration, Instant};

use gpui::{
    Animation, AnimationExt, AnyElement, App, ClickEvent, ClipboardItem, Context, Div, Entity,
    EventEmitter, FocusHandle, Focusable, Hsla, PathPromptOptions, SharedString, Stateful, Task,
    Window, actions, div, ease_in_out, prelude::*, px,
};
use serde_json::{Value, json};

use crate::icons::{Icon, IconStyle};
use crate::fleet_config::{FleetConfig, THIS_PC};
use crate::swarm::client::{self, Client};
use crate::swarm::fleet;
use crate::swarm::progress::{self, Event as Progress};
use crate::swarm::repository::{self, Prepared};
use crate::swarm::target::{channel, Target, INSTALLED_INSTALLER};
use crate::text_input::{InputColors, TextInput};
use crate::theme::Theme;

actions!(onboarding, [Continue, Back]);

/// Card size: fixed, so moving between steps never moves anything else.
const CARD_W: f32 = 560.;
/// What Swarm's installer calls a machine when it is given no name.
const DEFAULT_SWARM_NAME: &str = "swarm";
const CARD_H: f32 = 580.;
/// A status shows at least this long, so fast steps never flash.
const MIN_SHOWN: Duration = Duration::from_millis(450);

pub enum OnboardingEvent {
    Finished,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum StepState {
    Waiting,
    Running,
    Done,
    Skipped,
    Failed,
}

/// What Swarm says about itself before this app is its client (GET
/// /v1/onboarding answers without a session).
#[derive(Clone, Debug, PartialEq)]
struct Hello {
    /// Swarm's own name for its machine (install.sh --name; "swarm" when
    /// none was given).
    name: String,
    owner: bool,
}

#[derive(Clone, Debug)]
struct ProviderRow {
    id: String,
    ready: bool,
    accepts_key: bool,
}

/// What creating a project came to: the project, or a folder whose first
/// commit the person must pick first.
enum Created {
    Project(Value, Vec<String>),
    Review(repository::Review),
    /// Swarm cannot see a chosen folder yet.
    Invisible,
}

#[derive(Clone, Debug)]
enum Stage {
    Welcome,
    Checking,
    Found,
    OldBuild,
    Missing { choice: usize },
    Server,
    /// The name of the machine Swarm runs on, asked before installing (then
    /// `install` is true) or of an installed Swarm still called by the
    /// installer's default.
    Name { install: bool },
    Installing { steps: Vec<StepState>, current: String },
    Connecting,
    /// Which machine your conversations live on (only with a fleet).
    Home { choice: usize },
    /// Where what agents may use is set: the Permissions page (Swarm's rules).
    Access,
    Provider { rows: Vec<ProviderRow>, loaded: bool, connected: Option<String> },
    ChatGpt { url: String, code: String },
    ApiKey { provider: String },
    Project { suggestions_loaded: bool },
    /// A chosen folder has files but no Git history: the person picks the
    /// files of its first commit (Swarm's repository review).
    Review { review: repository::Review, picked: Vec<bool> },
    SettingUp { project: String, phase: usize, context: Option<String> },
    Ready { project: String },
}

impl Stage {
    /// Which of the four steps (Swarm, provider, project, context) this is.
    fn step(&self) -> Option<usize> {
        match self {
            Stage::Welcome => None,
            Stage::Checking
            | Stage::Found
            | Stage::OldBuild
            | Stage::Missing { .. }
            | Stage::Server
            | Stage::Name { .. }
            | Stage::Installing { .. }
            | Stage::Connecting
            | Stage::Home { .. }
            | Stage::Access => Some(0),
            Stage::Provider { .. } | Stage::ChatGpt { .. } | Stage::ApiKey { .. } => Some(1),
            Stage::Project { .. } | Stage::Review { .. } => Some(2),
            Stage::SettingUp { .. } => Some(3),
            Stage::Ready { .. } => Some(4),
        }
    }
}

pub struct Onboarding {
    theme: Theme,
    icons: IconStyle,
    target: Target,
    stage: Stage,
    /// Bumps on every stage change so the card content fades in again.
    seq: usize,
    busy: bool,
    error: Option<String>,
    /// Something the app just did for the person (not an error), in the same line.
    note: Option<String>,
    hello: Option<Hello>,
    client: Option<Client>,
    project_id: Option<String>,
    folders: Vec<(String, bool)>,
    name: Entity<TextInput>,
    /// The Swarm's name (Stage::Name).
    swarm_name: Entity<TextInput>,
    /// Swarm's name was asked here already (at install, or after connecting).
    named: bool,
    server: Entity<TextInput>,
    folder: Entity<TextInput>,
    /// An API key, typed here and sent to Swarm once.
    key: Entity<TextInput>,
    /// The sign-in code and link, selectable and copyable.
    code_view: Entity<TextInput>,
    link_view: Entity<TextInput>,
    focus: FocusHandle,
    pending_focus: Option<FocusHandle>,
    task: Option<Task<()>>,
    /// What was just copied ("code", "link"), shown as "Copied" for a moment.
    copied: Option<&'static str>,
    copied_reset: Option<Task<()>>,
    /// The machines home controls (paired with it), read from home.
    fleet: Vec<fleet::Found>,
    /// The first project of a fresh Swarm, or one more (the heading says).
    first: bool,
    /// Folders are being added to this existing project instead of a new one.
    existing: Option<Existing>,
    /// Home and access as chosen here, saved before the provider step.
    draft: FleetConfig,
}

impl EventEmitter<OnboardingEvent> for Onboarding {}

impl Focusable for Onboarding {
    fn focus_handle(&self, _: &App) -> FocusHandle {
        self.focus.clone()
    }
}

pub(crate) fn input_colors(t: &Theme) -> InputColors {
    InputColors { placeholder: t.faint, cursor: t.accent, selection: Hsla { a: 0.3, ..t.accent } }
}

fn alpha(c: Hsla, a: f32) -> Hsla {
    Hsla { a, ..c }
}

fn text(v: &Value, key: &str) -> String {
    v.get(key).and_then(|x| x.as_str()).unwrap_or("").to_string()
}

/// Whether connecting failed because the machine's swarmctl has no `client`
/// command (a Swarm older than client sessions prints its usage and stops).
fn older_swarm(error: &str) -> bool {
    let e = error.to_ascii_lowercase();
    e.contains("usage") && !e.contains("client connect")
}

/// What a running Swarm says about itself without a session.
fn hello(api: &str) -> Hello {
    let v = ureq::AgentBuilder::new()
        .timeout(Duration::from_secs(5))
        .build()
        .get(&format!("{api}/v1/onboarding"))
        .call()
        .ok()
        .and_then(|r| r.into_json::<Value>().ok())
        .unwrap_or(Value::Null);
    let identity = v.get("identity").cloned().unwrap_or(Value::Null);
    Hello {
        name: text(v.get("config").unwrap_or(&Value::Null), "swarm_name"),
        owner: identity.get("bootstrapped").and_then(Value::as_bool).unwrap_or(false),
    }
}

impl Onboarding {
    pub fn new(theme: Theme, icons: IconStyle, target: Target, cx: &mut Context<Self>) -> Self {
        let colors = input_colors(&theme);
        let mut this = Onboarding {
            name: cx.new(|cx| TextInput::new("my-project", colors, cx)),
            swarm_name: cx.new(|cx| TextInput::new("office-server", colors, cx)),
            named: false,
            server: cx.new(|cx| TextInput::new("you@your-server", colors, cx)),
            folder: cx.new(|cx| TextInput::new("myhome", colors, cx)),
            key: cx.new(|cx| TextInput::new("sk-…", colors, cx)),
            code_view: cx.new(|cx| TextInput::new("", colors, cx).read_only(36.)),
            link_view: cx.new(|cx| TextInput::new("", colors, cx).read_only(20.)),
            theme,
            icons,
            target,
            stage: Stage::Welcome,
            seq: 0,
            busy: false,
            error: None,
            note: None,
            hello: None,
            client: None,
            project_id: None,
            folders: Vec::new(),
            focus: cx.focus_handle(),
            pending_focus: None,
            task: None,
            copied: None,
            copied_reset: None,
            fleet: Vec::new(),
            first: true,
            existing: None,
            draft: FleetConfig::default(),
        };
        this.pending_focus = Some(this.focus.clone());
        if let Ok(stage) = std::env::var("SWARM_CONSOLE_ONBOARDING_DEMO") {
            this.demo(&stage, cx);
        }
        this
    }

    pub fn set_theme(&mut self, theme: Theme, icons: IconStyle, cx: &mut Context<Self>) {
        let colors = input_colors(&theme);
        for input in [&self.name, &self.swarm_name, &self.server, &self.folder, &self.key, &self.code_view, &self.link_view] {
            input.update(cx, |i, _| i.set_colors(colors));
        }
        self.theme = theme;
        self.icons = icons;
        cx.notify();
    }

    fn go(&mut self, stage: Stage, cx: &mut Context<Self>) {
        self.stage = stage;
        self.seq += 1;
        self.busy = false;
        self.error = None;
        self.note = None;
        match self.stage {
            Stage::Server => self.pending_focus = Some(self.server.focus_handle(cx)),
            Stage::Name { .. } => self.pending_focus = Some(self.swarm_name.focus_handle(cx)),
            Stage::Project { .. } => self.pending_focus = Some(self.name.focus_handle(cx)),
            Stage::ApiKey { .. } => self.pending_focus = Some(self.key.focus_handle(cx)),
            _ => self.pending_focus = Some(self.focus.clone()),
        }
        cx.notify();
    }

    fn fail(&mut self, message: impl Into<String>, cx: &mut Context<Self>) {
        self.busy = false;
        self.error = Some(message.into());
        cx.notify();
    }

    /// Runs blocking work in the background, then `done` on this view. The
    /// result is held back until MIN_SHOWN has passed, so nothing flashes.
    fn run<R: Send + 'static>(
        &mut self,
        cx: &mut Context<Self>,
        work: impl FnOnce() -> R + Send + 'static,
        done: impl FnOnce(&mut Self, R, &mut Context<Self>) + 'static,
    ) {
        self.busy = true;
        self.error = None;
        cx.notify();
        self.task = Some(cx.spawn(async move |this, cx| {
            let started = Instant::now();
            let result = cx.background_executor().spawn(async move { work() }).await;
            if let Some(rest) = MIN_SHOWN.checked_sub(started.elapsed()) {
                cx.background_executor().timer(rest).await;
            }
            this.update(cx, |this, cx| {
                this.busy = false;
                done(this, result, cx);
                cx.notify();
            })
            .ok();
        }));
    }

    /// Copies to both clipboards (ctrl-v and middle click) and says so.
    fn copy(&mut self, what: &'static str, text: String, cx: &mut Context<Self>) {
        cx.write_to_clipboard(ClipboardItem::new_string(text.clone()));
        cx.write_to_primary(ClipboardItem::new_string(text));
        self.copied = Some(what);
        cx.notify();
        self.copied_reset = Some(cx.spawn(async move |this, cx| {
            cx.background_executor().timer(Duration::from_millis(1600)).await;
            this.update(cx, |this, cx| {
                this.copied = None;
                cx.notify();
            })
            .ok();
        }));
    }

    fn client(&self) -> Option<Client> {
        self.client.clone()
    }

    // Flow

    fn detect(&mut self, cx: &mut Context<Self>) {
        self.go(Stage::Checking, cx);
        let target = self.target.clone();
        self.run(
            cx,
            move || {
                let api = target.api();
                if !client::running(&api) {
                    return (None, None, Vec::new());
                }
                let hello = hello(&api);
                // A stored session that Swarm still accepts skips connecting.
                let working = target.stored_token().map(|t| Client::new(&api, t)).filter(|c| c.get("/v3/projects?limit=1").is_ok());
                // The machines home controls, when this app is already connected.
                let fleet: Vec<fleet::Found> = working.as_ref().and_then(|c| fleet::read(c).ok()).unwrap_or_default();
                (Some(hello), working, fleet)
            },
            |this, (hello, working, fleet), cx| {
                this.fleet = fleet;
                match hello {
                    None => this.go(Stage::Missing { choice: 0 }, cx),
                    Some(hello) => {
                        this.hello = Some(hello);
                        if let Some(client) = working {
                            this.client = Some(client);
                            this.after_connect(cx);
                        } else {
                            this.go(Stage::Found, cx);
                        }
                    }
                }
            },
        );
    }

    /// Makes this app a client of the machine's Swarm (one password prompt).
    fn connect(&mut self, cx: &mut Context<Self>) {
        self.go(Stage::Connecting, cx);
        let target = self.target.clone();
        self.run(
            cx,
            move || {
                let token = target.connect()?;
                let client = Client::new(target.api(), token);
                client.get("/v3/projects?limit=1").map_err(|e| e.to_string())?;
                Ok::<_, String>(client)
            },
            |this, result, cx| match result {
                Ok(client) => {
                    this.client = Some(client);
                    this.after_connect(cx);
                }
                // A Swarm from before client sessions answers `swarmctl client`
                // with its usage text: it needs an update, not another try.
                Err(e) if older_swarm(&e) => this.go(Stage::OldBuild, cx),
                Err(e) => {
                    this.go(Stage::Found, cx);
                    this.error = Some(e);
                }
            },
        );
    }

    fn install(&mut self, cx: &mut Context<Self>) {
        let steps = vec![StepState::Waiting; progress::STEPS.len()];
        self.go(Stage::Installing { steps, current: "Getting the installer".into() }, cx);
        self.busy = true;
        let target = self.target.clone();
        let name = self.swarm_name.read(cx).text().trim().to_string();
        self.named = true;
        let extra = std::env::var("SWARM_CONSOLE_INSTALL_ARGS").unwrap_or_default();
        let (tx, rx) = mpsc::channel::<Result<Progress, Result<(), String>>>();
        std::thread::spawn(move || {
            let result = (|| {
                let installer = target.stage_installer()?;
                let person = target.person();
                let mut args: Vec<String> = vec!["bash".into(), installer, "--for-user".into(), person];
                args.extend(["--progress=json"].map(String::from));
                if !name.is_empty() {
                    args.extend(["--name".to_string(), name]);
                }
                if let Some(branch) = channel() {
                    args.extend(["--ref".to_string(), branch]);
                }
                args.extend(extra.split_whitespace().map(String::from));
                let line_tx = tx.clone();
                target.run_root(&args, move |line| {
                    if let Some(event) = progress::parse_line(&line) {
                        let _ = line_tx.send(Ok(event));
                    }
                })
            })();
            let _ = tx.send(Err(result));
        });
        self.task = Some(cx.spawn(async move |this, cx| {
            loop {
                cx.background_executor().timer(Duration::from_millis(120)).await;
                let mut ended = None;
                let mut events = Vec::new();
                while let Ok(msg) = rx.try_recv() {
                    match msg {
                        Ok(e) => events.push(e),
                        Err(r) => ended = Some(r),
                    }
                }
                let finished = ended.is_some();
                let keep = this
                    .update(cx, |this, cx| {
                        for e in events {
                            this.apply_progress(e);
                        }
                        if let Some(result) = ended {
                            this.install_ended(result, cx);
                        }
                        cx.notify();
                    })
                    .is_ok();
                if finished || !keep {
                    break;
                }
            }
        }));
    }

    fn apply_progress(&mut self, event: Progress) {
        let Stage::Installing { steps, current } = &mut self.stage else { return };
        if let Some(error) = advance(steps, current, event) {
            self.error = Some(error);
        }
    }

    fn install_ended(&mut self, result: Result<(), String>, cx: &mut Context<Self>) {
        self.busy = false;
        if let Err(e) = result {
            if self.error.is_none() {
                self.error = Some(e);
            }
            return;
        }
        let target = self.target.clone();
        self.run(
            cx,
            move || {
                // Swarm starts after the install; wait for it, then become its client.
                let api = target.api();
                for _ in 0..60 {
                    if client::running(&api) {
                        break;
                    }
                    std::thread::sleep(Duration::from_secs(1));
                }
                let token = target.connect()?;
                let client = Client::new(&api, token);
                client.get("/v3/projects?limit=1").map_err(|e| e.to_string())?;
                Ok::<_, String>((client, hello(&api)))
            },
            |this, result, cx| match result {
                Ok((client, hello)) => {
                    this.client = Some(client);
                    this.hello = Some(hello);
                    this.after_connect(cx);
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    /// Connected to home: with no saved choices yet, say where what agents
    /// may use is set (the Permissions page), then the provider. Home is the
    /// Swarm this app connected to.
    fn after_connect(&mut self, cx: &mut Context<Self>) {
        // A Swarm still called by the installer's default gets its name first.
        if !self.named && self.hello.as_ref().is_some_and(|h| h.name.is_empty() || h.name == DEFAULT_SWARM_NAME) {
            return self.ask_name(false, cx);
        }
        if FleetConfig::load().is_some() {
            return self.providers(cx);
        }
        if self.draft.home.is_empty() {
            self.draft.home = match &self.target {
                Target::Local => THIS_PC.to_string(),
                other => other.label(),
            };
        }
        let Some(client) = self.client() else { return self.go(Stage::Access, cx) };
        self.run(
            cx,
            move || fleet::read(&client).unwrap_or_default(),
            |this, machines, cx| {
                this.fleet = machines;
                this.go(Stage::Access, cx);
            },
        );
    }

    /// Where Swarm is, as the screens say it: the Swarm's own name once it has
    /// one, until then this computer or the server's host.
    fn place(&self) -> String {
        self.hello.as_ref().map(|h| h.name.clone()).filter(|n| !n.is_empty() && n != DEFAULT_SWARM_NAME).unwrap_or_else(|| self.target.label())
    }

    /// Asks what to call the machine Swarm runs on, suggesting its host name.
    fn ask_name(&mut self, install: bool, cx: &mut Context<Self>) {
        self.busy = true;
        cx.notify();
        let target = self.target.clone();
        self.run(
            cx,
            move || target.hostname(),
            move |this, suggestion, cx| {
                this.swarm_name.update(cx, |i, cx| i.set_text(suggestion, cx));
                this.go(Stage::Name { install }, cx);
            },
        );
    }

    /// Renames an installed Swarm the way its own onboarding does (POST
    /// /v1/onboarding swarm_name), then goes on.
    fn save_name(&mut self, cx: &mut Context<Self>) {
        let name = self.swarm_name.read(cx).text().trim().to_string();
        if name.is_empty() {
            return self.fail("Give this Swarm a name.", cx);
        }
        let Some(client) = self.client() else { return };
        self.busy = true;
        cx.notify();
        let sent = name.clone();
        self.run(
            cx,
            move || client.post("/v1/onboarding", json!({"swarm_name": sent})).map_err(|e| e.to_string()),
            move |this, result, cx| match result {
                Ok(_) => {
                    this.named = true;
                    if let Some(h) = &mut this.hello {
                        h.name = name.clone();
                    }
                    this.after_connect(cx);
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    fn providers(&mut self, cx: &mut Context<Self>) {
        self.go(Stage::Provider { rows: Vec::new(), loaded: false, connected: None }, cx);
        let Some(client) = self.client() else { return };
        self.run(
            cx,
            move || client.get("/v1/providers").map_err(|e| e.to_string()),
            |this, result, cx| match result {
                Ok(v) => {
                    let rows: Vec<ProviderRow> = v
                        .get("providers")
                        .and_then(|p| p.as_array())
                        .map(|a| {
                            a.iter()
                                .map(|p| ProviderRow {
                                    id: text(p, "id"),
                                    ready: p.get("ready").and_then(|r| r.as_bool()).unwrap_or(false),
                                    accepts_key: p
                                        .get("auth_methods")
                                        .and_then(Value::as_array)
                                        .is_some_and(|m| m.iter().any(|a| text(a, "credential_type") == "api")),
                                })
                                .collect()
                        })
                        .unwrap_or_default();
                    let connected = rows.iter().find(|r| r.ready).map(|r| r.id.clone());
                    // Development only: SWARM_CONSOLE_DEV_SKIP_PROVIDER moves on without one, to
                    // exercise the project steps on a machine with no provider account.
                    let connected = connected.or_else(|| std::env::var("SWARM_CONSOLE_DEV_SKIP_PROVIDER").ok().map(|_| "nothing (skipped for development)".to_string()));
                    let ready = connected.is_some();
                    this.stage = Stage::Provider { rows, loaded: true, connected };
                    cx.notify();
                    if ready {
                        // Already set up: show it, then move on by itself.
                        this.task = Some(cx.spawn(async move |this, cx| {
                            cx.background_executor().timer(Duration::from_millis(900)).await;
                            this.update(cx, |this, cx| this.project(cx)).ok();
                        }));
                    }
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    fn chatgpt(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.client() else { return };
        self.run(
            cx,
            move || client.post("/v1/auth/codex/oauth/start", json!({"provider": "codex", "method": "device", "active": true})).map_err(|e| e.to_string()),
            |this, result, cx| match result {
                Ok(v) => {
                    let login = text(&v, "session_id");
                    let (url, code) = (text(&v, "verification_url"), text(&v, "user_code"));
                    this.code_view.update(cx, |c, cx| c.set_text(code, cx));
                    this.link_view.update(cx, |l, cx| l.set_text(url, cx));
                    this.go(Stage::ChatGpt { url: text(&v, "verification_url"), code: text(&v, "user_code") }, cx);
                    this.wait_chatgpt(login, cx);
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    fn wait_chatgpt(&mut self, login: String, cx: &mut Context<Self>) {
        let Some(client) = self.client() else { return };
        self.task = Some(cx.spawn(async move |this, cx| {
            loop {
                let client = client.clone();
                let login = login.clone();
                let result = cx
                    .background_executor()
                    .spawn(async move {
                        std::thread::sleep(Duration::from_secs(2));
                        client.get(&format!("/v1/auth/codex/oauth/status?session_id={login}")).map_err(|e| e.to_string())
                    })
                    .await;
                let next = this.update(cx, |this, cx| match result {
                    Ok(v) => match text(&v, "status").as_str() {
                        "success" => {
                            this.providers(cx);
                            false
                        }
                        "error" => {
                            this.fail(format!("Sign-in did not finish: {}", text(&v, "error")), cx);
                            false
                        }
                        _ => matches!(this.stage, Stage::ChatGpt { .. }),
                    },
                    Err(e) => {
                        this.fail(e, cx);
                        false
                    }
                });
                if !matches!(next, Ok(true)) {
                    break;
                }
            }
        }));
    }

    /// Asks for the provider's API key here; it goes to Swarm once, as the
    /// web UI sends it, and is never kept by this app.
    fn api_key(&mut self, provider: String, cx: &mut Context<Self>) {
        self.key.update(cx, |k, cx| k.set_text("", cx));
        self.go(Stage::ApiKey { provider }, cx);
    }

    fn save_key(&mut self, cx: &mut Context<Self>) {
        let Stage::ApiKey { provider } = self.stage.clone() else { return };
        let key = self.key.read(cx).text().trim().to_string();
        if key.is_empty() {
            return self.fail("Paste the key first.", cx);
        }
        let Some(client) = self.client() else { return };
        self.key.update(cx, |k, cx| k.set_text("", cx));
        self.run(
            cx,
            move || {
                client
                    .post("/v1/auth/credentials", json!({"provider": provider, "type": "api", "api_key": key, "label": "Swarm Console"}))
                    .map_err(|e| e.to_string())
            },
            |this, result, cx| match result {
                Ok(_) => this.providers(cx),
                Err(e) => this.fail(format!("The key was not accepted: {e}"), cx),
            },
        );
    }

    fn project(&mut self, cx: &mut Context<Self>) {
        if let Some(existing) = &self.existing {
            self.folders = existing.workspaces.iter().map(|w| (text(w, "path"), true)).collect();
            self.go(Stage::Project { suggestions_loaded: true }, cx);
            return;
        }
        self.go(Stage::Project { suggestions_loaded: false }, cx);
        let target = self.target.clone();
        self.run(
            cx,
            move || target.home_folders(),
            |this, repos, cx| {
                for repo in repos {
                    if !this.folders.iter().any(|(p, _)| *p == repo) {
                        this.folders.push((repo, false));
                    }
                }
                this.stage = Stage::Project { suggestions_loaded: true };
                cx.notify();
            },
        );
    }

    fn toggle_folder(&mut self, i: usize, cx: &mut Context<Self>) {
        if let Some((path, on)) = self.folders.get_mut(i) {
            *on = !*on;
            if *on {
                let name = self.name.read(cx).text();
                if name.trim().is_empty() {
                    let base = path.rsplit('/').next().unwrap_or("").to_string();
                    self.name.update(cx, |n, cx| n.set_text(base, cx));
                }
            }
        }
        cx.notify();
    }

    /// Adds the typed folder: a name makes (or finds) that folder in the
    /// person's home; a full path must be inside it. A folder that does not
    /// exist is created, with a README.md, and the card says so.
    fn add_folder(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let typed = self.folder.read(cx).text();
        if !typed.trim().is_empty() || !matches!(self.target, Target::Local) {
            let typed = typed.trim().trim_end_matches('/').to_string();
            if typed.is_empty() {
                self.error = Some("Type a folder name first.".into());
                cx.notify();
                return;
            }
            let home = self.target.home_dir();
            let path = if typed.starts_with('/') { typed.clone() } else { format!("{home}/{}", typed.trim_start_matches("./")) };
            if path != home && !path.starts_with(&format!("{home}/")) || path.contains("/../") || path.ends_with("/..") {
                self.error = Some(format!("Folders must be inside your home ({home}). Type a name to make one there, like myhome."));
                cx.notify();
                return;
            }
            if path == home {
                self.error = Some("That is your whole home folder; pick or make a folder inside it.".into());
                cx.notify();
                return;
            }
            if let Some(i) = self.folders.iter().position(|(f, _)| *f == path) {
                // Already listed: checked, at the top, where it can be seen.
                let (p, _) = self.folders.remove(i);
                self.folders.insert(0, (p, true));
                if self.name.read(cx).text().trim().is_empty() {
                    let base = path.rsplit('/').next().unwrap_or("").to_string();
                    self.name.update(cx, |n, cx| n.set_text(base, cx));
                }
                self.folder.update(cx, |f, cx| f.set_text("", cx));
                self.error = None;
                cx.notify();
                return;
            }
            let target = self.target.clone();
            let p = path.clone();
            self.run(
                cx,
                move || target.ensure_folder(&p),
                move |this, result, cx| match result {
                    Ok(created) => {
                        // At the top, checked, where it can be seen.
                        this.folders.insert(0, (path.clone(), true));
                        if this.name.read(cx).text().trim().is_empty() {
                            let base = path.rsplit('/').next().unwrap_or("").to_string();
                            this.name.update(cx, |n, cx| n.set_text(base, cx));
                        }
                        this.folder.update(cx, |f, cx| f.set_text("", cx));
                        this.note = created.then(|| format!("Made {path} on the machine."));
                    }
                    Err(e) => this.error = Some(e),
                },
            );
            return;
        }
        let picked = cx.prompt_for_paths(PathPromptOptions { files: false, directories: true, multiple: true, prompt: None });
        let _ = window;
        self.task = Some(cx.spawn(async move |this, cx| {
            if let Ok(Ok(Some(paths))) = picked.await {
                this.update(cx, |this, cx| {
                    for p in paths {
                        let p = p.display().to_string();
                        if !this.folders.iter().any(|(f, _)| *f == p) {
                            this.folders.push((p, true));
                        }
                    }
                    cx.notify();
                })
                .ok();
            }
        }));
    }

    fn create_project(&mut self, cx: &mut Context<Self>) {
        let name = self.name.read(cx).text().trim().to_string();
        let paths: Vec<String> = self.folders.iter().filter(|(_, on)| *on).map(|(p, _)| p.clone()).collect();
        if name.is_empty() || paths.is_empty() {
            self.fail("Name the project and pick at least one folder.", cx);
            return;
        }
        let Some(client) = self.client() else { return };
        let args = json!({
            "client_request_id": format!("swarm-console:project:{}", std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0)),
            "name": name,
            "workspaces": paths.iter().enumerate().map(|(i, p)| json!({"path": p, "role": if i == 0 { "primary_code" } else { "auxiliary" }})).collect::<Vec<_>>(),
        });
        let register = paths.clone();
        let existing = self.existing.clone();
        self.run(
            cx,
            move || {
                // A project's folders are registered workspaces: register the
                // ones Swarm does not know yet, in place (nothing in them changes).
                let known = client.get("/v1/workspace/list?limit=500").map_err(|e| e.to_string())?;
                let registered: Vec<String> = known
                    .get("workspaces")
                    .and_then(Value::as_array)
                    .map(|a| a.iter().map(|w| text(w, "path")).collect())
                    .unwrap_or_default();
                // Agents work only in a Git repository with a first commit.
                // Swarm makes one in an empty folder by itself (before the
                // folder is registered); a folder with files needs the person
                // to pick its first commit.
                let mut notes = Vec::new();
                for path in &register {
                    match repository::prepare(&client, path)? {
                        Prepared::Ready { note } => notes.extend(note),
                        Prepared::NeedsReview(review) => return Ok(Created::Review(review)),
                        Prepared::Invisible(_) => return Ok(Created::Invisible),
                    }
                }
                for path in register.iter().filter(|p| !registered.contains(p)) {
                    client.post("/v1/workspace/add", json!({"path": path, "context_only": true, "make_current": false})).map_err(|e| e.to_string())?;
                }
                match existing {
                    // More folders for a project that exists: its list, plus these.
                    Some(e) => {
                        let mut workspaces = e.workspaces.clone();
                        let more: Vec<String> = register.iter().filter(|p| !workspaces.iter().any(|w| text(w, "path") == **p)).cloned().collect();
                        for path in more {
                            workspaces.push(json!({"path": path, "role": "auxiliary"}));
                        }
                        client.patch(&format!("/v3/projects/{}", e.id), json!({"workspaces": workspaces}))
                    }
                    None => client.post("/v3/projects", args),
                }
                .map(|v| Created::Project(v, notes))
                .map_err(|e| e.to_string())
            },
            move |this, result, cx| match result {
                Ok(Created::Project(v, notes)) => {
                    let project = v.get("project").cloned().unwrap_or(Value::Null);
                    this.project_id = Some(text(&project, "id"));
                    if this.existing.is_some() {
                        // Return to the Projects page, whose durable live subscription
                        // shows generation status; do not present the old document.
                        cx.emit(OnboardingEvent::Finished);
                    } else {
                        this.setting_up(text(&project, "name"), cx);
                    }
                    if !notes.is_empty() {
                        this.note = Some(notes.join(" "));
                    }
                }
                Ok(Created::Review(review)) => {
                    // Hidden files (.env and the like) start unticked: they are the
                    // ones a first commit should not carry by accident.
                    let picked = review.files.iter().map(|f| f.selectable && !f.path.starts_with('.') && !f.path.contains("/.")).collect();
                    this.go(Stage::Review { review, picked }, cx);
                }
                // Swarm's user cannot see the folder yet: make it visible, then create again.
                Ok(Created::Invisible) => this.make_visible(paths.clone(), cx),
                Err(e) if e.contains("workspace_not_visible") || e.contains("cannot see") || e.contains("not visible") || e.contains("permission denied") || e.contains("no such file") => {
                    this.make_visible(paths.clone(), cx);
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    fn toggle_pick(&mut self, i: usize, cx: &mut Context<Self>) {
        if let Stage::Review { review, picked } = &mut self.stage {
            if review.files.get(i).is_some_and(|f| f.selectable) {
                if let Some(p) = picked.get_mut(i) {
                    *p = !*p;
                }
            }
        }
        cx.notify();
    }

    /// Commits the picked files as the folder's first commit, then goes on
    /// creating the project (the next folder may need the same).
    fn commit_baseline(&mut self, cx: &mut Context<Self>) {
        let Stage::Review { review, picked } = self.stage.clone() else { return };
        let selected: Vec<String> = review.files.iter().zip(&picked).filter(|(_, on)| **on).map(|(f, _)| f.path.clone()).collect();
        if selected.is_empty() {
            self.fail("Pick at least one file for the first commit.", cx);
            return;
        }
        let Some(client) = self.client() else { return };
        let path = review.path.clone();
        self.run(
            cx,
            move || repository::baseline(&client, &review, &selected),
            move |this, result, cx| match result {
                Ok(state) => {
                    this.go(Stage::Project { suggestions_loaded: true }, cx);
                    this.note = Some(format!("Made the first commit in {path} ({}).", &state.head[..state.head.len().min(9)]));
                    this.create_project(cx);
                }
                Err(e) => this.fail(e, cx),
            },
        );
    }

    /// Swarm runs as its own user and cannot see inside a home folder: as
    /// root (`swarmctl workspace add`), the app lets it read exactly these
    /// folders, waits for Swarm's restart, then creates the project. On this
    /// PC that is the one password prompt; on a server, nothing to answer.
    fn make_visible(&mut self, paths: Vec<String>, cx: &mut Context<Self>) {
        let target = self.target.clone();
        let note = format!("Letting Swarm read {} (Swarm restarts once)…", paths.join(", "));
        self.run(
            cx,
            move || {
                let mut args: Vec<String> = vec![crate::swarm::target::SWARMCTL.into(), "workspace".into(), "add".into()];
                args.extend(paths);
                args.extend(["--for-user".to_string(), target.person()]);
                target.run_root(&args, |_| {})?;
                // Swarm restarts to apply the folder list; wait until it answers.
                std::thread::sleep(Duration::from_secs(2));
                for _ in 0..90 {
                    if client::running(&target.api()) {
                        return Ok(());
                    }
                    std::thread::sleep(Duration::from_secs(1));
                }
                Err("Swarm did not come back after the restart.".to_string())
            },
            |this, result, cx| match result {
                Ok(()) => this.create_project(cx),
                Err(e) => this.fail(e, cx),
            },
        );
        self.note = Some(note);
        cx.notify();
    }

    fn setting_up(&mut self, project: String, cx: &mut Context<Self>) {
        self.go(Stage::SettingUp { project, phase: 1, context: None }, cx);
        let (Some(client), Some(id)) = (self.client(), self.project_id.clone()) else { return };
        self.task = Some(cx.spawn(async move |this, cx| {
            loop {
                let client = client.clone();
                let id = id.clone();
                let result = cx
                    .background_executor()
                    .spawn(async move {
                        std::thread::sleep(Duration::from_secs(2));
                        client.get(&format!("/v3/projects/{id}")).map_err(|e| e.to_string())
                    })
                    .await;
                let next = this.update(cx, |this, cx| {
                    let Ok(v) = result else {
                        this.fail("Swarm stopped answering while writing project.md.", cx);
                        return false;
                    };
                    let project = v.get("project").cloned().unwrap_or(Value::Null);
                    let status = project
                        .get("context_generation")
                        .map(|g| text(g, "status"))
                        .unwrap_or_default();
                    let Stage::SettingUp { phase, context, .. } = &mut this.stage else { return false };
                    match status.as_str() {
                        "ready" => {
                            *phase = 3;
                            *context = Some(text(&project, "project_context"));
                            cx.notify();
                            false
                        }
                        "failed" => {
                            let why = project.get("context_generation").map(|g| text(g, "error")).unwrap_or_default();
                            this.fail(format!("Swarm could not write project.md: {why}"), cx);
                            false
                        }
                        "running" | "generating" => {
                            *phase = 2;
                            cx.notify();
                            true
                        }
                        _ => true,
                    }
                });
                if !matches!(next, Ok(true)) {
                    break;
                }
            }
        }));
    }

    fn back(&mut self, _: &Back, _: &mut Window, cx: &mut Context<Self>) {
        if self.busy {
            return;
        }
        match self.stage {
            Stage::Server => self.go(Stage::Missing { choice: 1 }, cx),
            Stage::Name { install: true } => self.go(Stage::Missing { choice: 0 }, cx),
            Stage::ChatGpt { .. } | Stage::ApiKey { .. } => {
                self.task = None;
                self.providers(cx);
            }
            Stage::Review { .. } => self.go(Stage::Project { suggestions_loaded: true }, cx),
            // Opened from the console for one more project: escape goes back to it.
            Stage::Project { .. } if !self.first => cx.emit(OnboardingEvent::Finished),
            _ => {}
        }
    }

    fn primary(&mut self, _: &Continue, window: &mut Window, cx: &mut Context<Self>) {
        if self.busy {
            return;
        }
        match self.stage.clone() {
            Stage::Welcome => self.detect(cx),
            Stage::Checking | Stage::Installing { .. } | Stage::Connecting => {
                if self.error.is_some() {
                    match self.stage {
                        Stage::Installing { .. } => self.install(cx),
                        _ => self.detect(cx),
                    }
                }
            }
            Stage::Found => self.connect(cx),
            Stage::Home { choice } => {
                if choice == 0 {
                    self.draft.home = match &self.target {
                        Target::Local => THIS_PC.to_string(),
                        other => other.label(),
                    };
                    self.go(Stage::Access, cx);
                } else if let Some(machine) = self.fleet.get(choice - 1).cloned() {
                    // A remote home is reached over SSH, which also gives this app its key there.
                    self.draft.home = machine.name.clone();
                    let user = std::env::var("USER").unwrap_or_else(|_| "root".into());
                    let address = format!("{user}@{}", machine.name);
                    self.server.update(cx, |s, cx| s.set_text(address, cx));
                    self.go(Stage::Server, cx);
                }
            }
            Stage::Access => match self.draft.save() {
                Ok(()) => self.providers(cx),
                Err(e) => self.fail(format!("Could not save your choices: {e}"), cx),
            },
            Stage::OldBuild => self.detect(cx),
            Stage::Missing { choice: 0 } => self.ask_name(true, cx),
            Stage::Name { install: true } => self.install(cx),
            Stage::Name { install: false } => self.save_name(cx),
            Stage::Missing { .. } => self.go(Stage::Server, cx),
            Stage::Server => {
                let input = self.server.read(cx).text();
                match Target::parse_server(&input, None) {
                    Ok(target) => {
                        self.target = target.clone();
                        self.run(cx, move || target.check_server(), |this, r, cx| match r {
                            Ok(()) => this.detect(cx),
                            Err(e) => this.fail(e, cx),
                        });
                    }
                    Err(e) => self.fail(e, cx),
                }
            }
            Stage::Provider { connected: Some(_), .. } => self.project(cx),
            Stage::Provider { .. } => self.chatgpt(cx),
            Stage::ChatGpt { url, .. } => cx.open_url(&url),
            Stage::ApiKey { .. } => self.save_key(cx),
            Stage::Project { .. } => {
                if self.folder.focus_handle(cx).is_focused(window) {
                    self.add_folder(window, cx);
                } else {
                    self.create_project(cx);
                }
            }
            Stage::Review { .. } => self.commit_baseline(cx),
            Stage::SettingUp { project, context: Some(_), .. } => self.go(Stage::Ready { project }, cx),
            Stage::SettingUp { .. } => {}
            Stage::Ready { .. } => cx.emit(OnboardingEvent::Finished),
        }
    }

    // Demo states, for screenshots and design review without a machine.

    fn demo(&mut self, stage: &str, cx: &mut Context<Self>) {
        self.hello = Some(Hello { name: "office-server".into(), owner: true });
        let mut steps = vec![StepState::Done; 3];
        steps.extend([StepState::Running, StepState::Waiting, StepState::Waiting, StepState::Waiting, StepState::Waiting]);
        self.stage = match stage {
            "checking" => Stage::Checking,
            "found" => Stage::Found,
            "home" | "access" => {
                self.fleet = ["build-box", "media-box", "edge-1"]
                    .iter()
                    .enumerate()
                    .map(|(i, n)| fleet::Found { name: n.to_string(), address: format!("10.0.0.{}:7792", i + 5), last_contact_at: 0, last_error: String::new() })
                    .collect();
                self.draft.home = THIS_PC.into();
                if stage == "home" { Stage::Home { choice: 0 } } else { Stage::Access }
            }
            "missing" => Stage::Missing { choice: 0 },
            "server" => Stage::Server,
            "name" => {
                self.swarm_name.update(cx, |i, cx| i.set_text("office-server", cx));
                Stage::Name { install: true }
            }
            "installing" => Stage::Installing { steps, current: "Building Swarm main@1a2b3c4d5 (the first build takes several minutes)".into() },
            "connecting" => Stage::Connecting,
            "provider" => Stage::Provider {
                rows: vec![
                    ProviderRow { id: "anthropic".into(), ready: false, accepts_key: true },
                    ProviderRow { id: "codex".into(), ready: false, accepts_key: false },
                    ProviderRow { id: "google".into(), ready: false, accepts_key: true },
                    ProviderRow { id: "openai".into(), ready: false, accepts_key: true },
                ],
                loaded: true,
                connected: None,
            },
            "chatgpt" => {
                self.code_view.update(cx, |c, cx| c.set_text("KX4P-9TQM", cx));
                self.link_view.update(cx, |l, cx| l.set_text("https://auth.openai.com/codex/device", cx));
                Stage::ChatGpt { url: "https://auth.openai.com/codex/device".into(), code: "KX4P-9TQM".into() }
            }
            "project" => {
                self.folders = vec![
                    ("~/code/storefront".into(), true),
                    ("~/code/storefront-docs".into(), true),
                    ("~/code/scratch".into(), false),
                ];
                self.name.update(cx, |n, cx| n.set_text("storefront", cx));
                Stage::Project { suggestions_loaded: true }
            }
            "settingup" => Stage::SettingUp { project: "storefront".into(), phase: 2, context: None },
            "context" => Stage::SettingUp {
                project: "storefront".into(),
                phase: 3,
                context: Some("# storefront\n\nA headless shop: a Next.js storefront, a Go order API and the docs site.\n\n## Folders\n- storefront: web app (Next.js, TypeScript)\n- storefront-docs: Astro docs\n\n## Conventions\n- pnpm, Go 1.26, tests before merge".into()),
            },
            "ready" => Stage::Ready { project: "storefront".into() },
            _ => Stage::Welcome,
        };
        if std::env::var("SWARM_CONSOLE_ONBOARDING_ERROR").is_ok() {
            self.error = Some("The password prompt was closed, so nothing changed.".into());
        }
    }

    // Rendering

    fn mark(&self, size: f32, breathing: bool) -> AnyElement {
        let accent = self.theme.accent;
        let base = [0.15_f32, 0.35, 0.6, 0.85];
        let squares = move |phase: Option<f32>| {
            div().relative().size(px(size)).children((0..4).map(move |i| {
                let inset = size * (0.05 + 0.1 * i as f32);
                let side = size - 2. * inset;
                let a = match phase {
                    Some(t) => {
                        let wave = ((t - i as f32 * 0.14) * std::f32::consts::TAU).sin() * 0.5 + 0.5;
                        base[i] * (0.55 + 0.45 * wave)
                    }
                    None => base[i],
                };
                div()
                    .absolute()
                    .top(px(inset))
                    .left(px(inset))
                    .size(px(side))
                    .rounded(px(side * 0.24))
                    .bg(alpha(accent, a))
            }))
        };
        if breathing {
            div()
                .size(px(size))
                .with_animation(
                    SharedString::from(format!("mark-{size}")),
                    Animation::new(Duration::from_millis(2400)).repeat(),
                    move |el, t| el.child(squares(Some(t))),
                )
                .into_any_element()
        } else {
            squares(None).into_any_element()
        }
    }

    fn icon(&self, icon: Icon, size: f32, color: Hsla) -> AnyElement {
        match self.icons {
            IconStyle::Glyph => div().text_size(px(size)).text_color(color).child(icon.glyph()).into_any_element(),
            style => gpui::svg().path(icon.path(style)).flex_none().size(px(size)).text_color(color).into_any_element(),
        }
    }

    fn button(&self, id: &'static str, label: impl Into<SharedString>, primary: bool, enabled: bool) -> Stateful<Div> {
        let t = &self.theme;
        let (bg, fg, border) = if primary { (t.accent, t.accent_text, t.accent) } else { (alpha(t.raised, 0.0), t.muted, t.border_strong) };
        let hover = if primary { alpha(t.accent, 0.88) } else { t.hover };
        div()
            .id(id)
            .h(px(34.))
            .px_4()
            .flex()
            .items_center()
            .justify_center()
            .gap_2()
            .rounded(px(t.radius + 1.))
            .border_1()
            .border_color(border)
            .bg(bg)
            .text_color(fg)
            .when(enabled, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
            .when(!enabled, |d| d.opacity(0.45))
            .child(label.into())
    }

    fn option_card(&self, id: impl Into<SharedString>, selected: bool, title: &str, detail: &str, icon: Icon) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.border_strong;
        div()
            .id(id.into())
            .p_3()
            .flex()
            .gap_3()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(if selected { alpha(t.accent, 0.8) } else { t.border })
            .bg(if selected { alpha(t.accent, 0.06) } else { t.surface })
            .cursor_pointer()
            .hover(move |s| s.border_color(hover))
            .child(div().pt(px(2.)).child(self.icon(icon, 16., if selected { t.accent } else { t.muted })))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .flex()
                    .flex_col()
                    .gap(px(3.))
                    .child(div().text_color(t.text).child(title.to_string()))
                    .child(div().text_size(px(11.5)).text_color(t.faint).line_height(px(17.)).child(detail.to_string())),
            )
    }

    fn checklist_row(&self, state: StepState, label: &str, detail: Option<&str>) -> Div {
        let t = &self.theme;
        let (glyph, color) = match state {
            StepState::Waiting => ("○", t.faint),
            StepState::Running => ("●", t.accent),
            StepState::Done => ("✓", t.ok),
            StepState::Skipped => ("–", t.faint),
            StepState::Failed => ("✕", t.err),
        };
        div()
            .flex()
            .items_center()
            .gap_3()
            .h(px(26.))
            .child(
                div()
                    .w(px(14.))
                    .flex()
                    .justify_center()
                    .text_color(color)
                    .map(|d| {
                        if state == StepState::Running {
                            d.child(div().child(glyph).with_animation(
                                "running-dot",
                                Animation::new(Duration::from_millis(1400)).repeat().with_easing(ease_in_out),
                                |el, t| el.opacity(0.35 + 0.65 * (1. - (t * 2. - 1.).abs())),
                            ))
                        } else {
                            d.child(glyph)
                        }
                    }),
            )
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .truncate()
                    .text_color(match state {
                        StepState::Waiting | StepState::Skipped => t.faint,
                        _ => t.text,
                    })
                    .child(label.to_string()),
            )
            .children(detail.map(|d| div().flex_none().max_w(px(220.)).truncate().text_size(px(11.)).text_color(t.faint).child(d.to_string())))
    }

    fn field(&self, label: &str, input: &Entity<TextInput>, focused: bool) -> Div {
        let t = &self.theme;
        div()
            .flex()
            .flex_col()
            .gap_1()
            .child(div().text_size(px(11.)).text_color(t.faint).child(label.to_string()))
            .child(
                div()
                    .h(px(36.))
                    .px_3()
                    .flex()
                    .items_center()
                    .rounded(px(t.radius + 1.))
                    .border_1()
                    .border_color(if focused { alpha(t.accent, 0.7) } else { t.border_strong })
                    .bg(t.surface)
                    .text_color(t.text)
                    .child(input.clone()),
            )
    }

    /// Title, subtitle, body and footer for the current stage.
    fn content(&mut self, window: &mut Window, cx: &mut Context<Self>) -> (String, String, AnyElement, AnyElement) {
        let t = self.theme.clone();
        // The Swarm's own name once it has one; until then the computer or host.
        let label = self.place();
        let busy = self.busy;
        let footer = |left: Option<AnyElement>, right: AnyElement| -> AnyElement {
            div()
                .flex()
                .items_center()
                .justify_between()
                .child(left.unwrap_or_else(|| div().into_any_element()))
                .child(right)
                .into_any_element()
        };
        let primary = |this: &Self, id: &'static str, text: &str, enabled: bool, cx: &mut Context<Self>| -> AnyElement {
            this.button(id, text.to_string(), true, enabled && !busy)
                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.primary(&Continue, window, cx)))
                .into_any_element()
        };
        let waiting = |this: &Self, line: &str| -> AnyElement {
            div()
                .flex()
                .items_center()
                .gap_3()
                .child(this.mark(22., true))
                .child(div().text_color(t.muted).child(line.to_string()))
                .into_any_element()
        };

        match self.stage.clone() {
            Stage::Welcome => (
                "Let's set up your Swarm".into(),
                "Swarm runs AI agents on your own machines. This takes a few minutes: find or install Swarm, connect a model provider, then add your first project.".into(),
                div()
                    .flex()
                    .flex_col()
                    .gap_2()
                    .pt_2()
                    .child(self.checklist_row(StepState::Waiting, "Swarm on this computer or a server", None))
                    .child(self.checklist_row(StepState::Waiting, "A model provider", None))
                    .child(self.checklist_row(StepState::Waiting, "Your first project", None))
                    .child(self.checklist_row(StepState::Waiting, "project.md, written by Swarm", None))
                    .into_any_element(),
                footer(None, primary(self, "get-started", "Get started", true, cx)),
            ),
            Stage::Checking => (
                format!("Looking for Swarm on {label}"),
                "Checking whether Swarm is installed and running.".into(),
                div().pt_4().child(waiting(self, "Asking Swarm's gateway who it is…")).into_any_element(),
                footer(None, primary(self, "checking", "Continue", false, cx)),
            ),
            Stage::Found => {
                let hello = self.hello.clone();
                let token_name = self.target.token_path().file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
                (
                    format!("Swarm is running on {label}"),
                    "Connect this app to it. Your password is asked once, to give the app a session of its own.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_2()
                        .pt_2()
                        .child(self.checklist_row(StepState::Done, "Swarm answers", hello.as_ref().filter(|h| !h.name.is_empty()).map(|h| format!("named {}", h.name)).as_deref()))
                        .child(self.checklist_row(
                            if hello.as_ref().is_some_and(|h| h.owner) { StepState::Done } else { StepState::Waiting },
                            if hello.as_ref().is_some_and(|h| h.owner) { "It has an owner" } else { "It has no owner yet: you will be its owner" },
                            None,
                        ))
                        .child(self.checklist_row(StepState::Waiting, "A session for this app", Some(token_name.as_str())))
                        .children((!self.fleet.is_empty()).then(|| {
                            div()
                                .pt_3()
                                .flex()
                                .flex_col()
                                .gap_1()
                                .child(div().text_size(px(11.)).text_color(t.faint).child("ALSO ON YOUR NETWORK"))
                                .children(self.fleet.iter().map(|f| self.checklist_row(StepState::Done, &f.name, Some("paired with home"))))
                        }))
                        .child(
                            div()
                                .pt_3()
                                .text_size(px(11.5))
                                .text_color(t.faint)
                                .line_height(px(17.))
                                .child("The session is a file only you can read, in ~/.config. It gives this app what Swarm's own web page has, nothing more; swarmctl client list and revoke on the machine show and end it."),
                        )
                        .into_any_element(),
                    footer(None, primary(self, "connect", "Connect", true, cx)),
                )
            }
            Stage::Home { choice } => {
                let mut options: Vec<(String, String)> = vec![(
                    self.hello.as_ref().map(|h| h.name.clone()).filter(|n| !n.is_empty()).unwrap_or_else(|| label.clone()),
                    "Swarm runs here and you are connected. Recommended.".to_string(),
                )];
                options.extend(self.fleet.iter().map(|f| (f.name.clone(), "Paired with this Swarm.".to_string())));
                (
                    "Where should your conversations live?".into(),
                    "That machine's Orchestrator is the one you talk to, and it keeps your conversations. The others stay reachable for it.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_2()
                        .pt_2()
                        .children(options.into_iter().enumerate().map(|(i, (title, detail))| {
                            self.option_card(format!("home-{i}"), choice == i, &title, &detail, if i == 0 { Icon::Machine } else { Icon::Agents })
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.stage = Stage::Home { choice: i };
                                    cx.notify();
                                }))
                        }))
                        .into_any_element(),
                    footer(None, primary(self, "home-continue", "Continue", true, cx)),
                )
            }
            Stage::Access => {
                let line = |text: &'static str| div().py(px(3.)).text_color(t.muted).child(text);
                (
                    "What may agents use?".into(),
                    "Swarm keeps these rules for every conversation and enforces them. Change them any time on the Permissions page.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_1()
                        .pt_2()
                        .child(line("This computer: its files and terminal, and its browser, each off or on."))
                        .child(line("This Swarm: whether it asks you before sending a task, or sends it on its own."))
                        .child(line("Linked machines: off, ask before sending it a task, or auto."))
                        .into_any_element(),
                    footer(None, primary(self, "access-continue", "Continue", true, cx)),
                )
            }
            Stage::OldBuild => (
                "Swarm needs an update".into(),
                format!("The Swarm on {label} is older than this app. Update it, then check again."),
                div()
                    .pt_2()
                    .child(
                        div()
                            .p_3()
                            .rounded(px(t.radius + 1.))
                            .bg(t.surface)
                            .border_1()
                            .border_color(t.border)
                            .text_color(t.text)
                            .child(format!("sudo bash {INSTALLED_INSTALLER} update")),
                    )
                    .into_any_element(),
                footer(None, primary(self, "check-again", "Check again", true, cx)),
            ),
            Stage::Missing { choice } => {
                let local = matches!(self.target, Target::Local);
                let install_title = if local { "Install on this computer".to_string() } else { format!("Install on {label}") };
                (
                    format!("Swarm isn't on {label} yet"),
                    "Install it here, or use a server where Swarm runs or should run.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_2()
                        .pt_2()
                        .child(
                            self.option_card("opt-install", choice == 0, &install_title, "Ubuntu 24.04. About 10 minutes the first time. Your password is asked once.", Icon::Machine)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.stage = Stage::Missing { choice: 0 };
                                    cx.notify();
                                })),
                        )
                        .child(
                            self.option_card("opt-server", choice == 1, "Use a server", "Over SSH. Swarm installs there if it isn't yet; your computer only runs this app.", Icon::Agents)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                    this.stage = Stage::Missing { choice: 1 };
                                    cx.notify();
                                })),
                        )
                        .into_any_element(),
                    footer(None, primary(self, "missing-continue", if choice == 0 { "Install" } else { "Continue" }, true, cx)),
                )
            }
            Stage::Server => {
                let focused = self.server.focus_handle(cx).is_focused(window);
                (
                    "Which server?".into(),
                    "Swarm signs in with your SSH key and uses sudo there. Nothing is opened to the internet.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_3()
                        .pt_2()
                        .child(self.field("SSH ADDRESS", &self.server, focused))
                        .child(div().text_size(px(11.5)).text_color(t.faint).child("For example dev@my-server or dev@10.0.0.4:2222"))
                        .into_any_element(),
                    footer(
                        Some(self.button("server-back", "Back", false, !busy).on_click(cx.listener(|this, _: &ClickEvent, w, cx| this.back(&Back, w, cx))).into_any_element()),
                        primary(self, "server-check", "Check", true, cx),
                    ),
                )
            }
            Stage::Name { install } => {
                let focused = self.swarm_name.focus_handle(cx).is_focused(window);
                (
                    if install { format!("What should the Swarm on {label} be called?") } else { "What should this Swarm be called?".to_string() },
                    "It names the machine Swarm runs on: in this app, on its setup page, and when you link Swarms together. You can rename it later.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap_3()
                        .pt_2()
                        .child(self.field("NAME", &self.swarm_name, focused))
                        .child(div().text_size(px(11.5)).text_color(t.faint).child("For example the machine's own name, or what it is for: office-server, roy-desktop"))
                        .into_any_element(),
                    footer(
                        install.then(|| self.button("name-back", "Back", false, !busy).on_click(cx.listener(|this, _: &ClickEvent, w, cx| this.back(&Back, w, cx))).into_any_element()),
                        primary(self, "name-continue", if install { "Install" } else { "Continue" }, true, cx),
                    ),
                )
            }
            Stage::Installing { steps, current } => {
                let failed = self.error.is_some();
                let rows = progress::STEPS
                    .iter()
                    .zip(steps.iter())
                    .map(|((_, label), state)| self.checklist_row(*state, label, None).into_any_element())
                    .collect::<Vec<_>>();
                (
                    format!("Installing Swarm on {label}"),
                    "The first build takes a few minutes. Swarm listens on this machine only.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .gap(px(2.))
                        .pt_1()
                        .children(rows)
                        .child(div().pt_2().h(px(40.)).text_size(px(11.5)).line_height(px(17.)).text_color(t.faint).child(current.clone()))
                        .into_any_element(),
                    footer(None, primary(self, "install-retry", if failed { "Try again" } else { "Installing…" }, failed, cx)),
                )
            }
            Stage::Connecting => (
                format!("Connecting to {label}"),
                "Type your password in the prompt that opens. It gives this app a key of its own.".into(),
                div().pt_4().child(waiting(self, "Waiting for the password prompt…")).into_any_element(),
                footer(None, primary(self, "connecting", if self.error.is_some() { "Try again" } else { "Connecting…" }, self.error.is_some(), cx)),
            ),
            Stage::Provider { rows, loaded, connected } => {
                if let Some(provider) = connected {
                    (
                        "A model provider is connected".into(),
                        format!("Swarm on {label} already uses {provider}. Its recommended models are set."),
                        div().pt_2().child(self.checklist_row(StepState::Done, &provider, Some("ready"))).into_any_element(),
                        footer(None, primary(self, "provider-next", "Continue", true, cx)),
                    )
                } else if !loaded {
                    (
                        "Connect a model provider".into(),
                        "Checking which providers Swarm already knows.".into(),
                        div().pt_4().child(waiting(self, "Reading providers…")).into_any_element(),
                        footer(None, primary(self, "provider-wait", "Continue", false, cx)),
                    )
                } else {
                    let keyed: Vec<ProviderRow> = rows.into_iter().filter(|r| r.accepts_key).collect();
                    let remote = matches!(self.target, Target::Ssh { .. });
                    let chips = keyed.iter().enumerate().map(|(i, r)| {
                        let id = r.id.clone();
                        let hover = t.hover;
                        div()
                            .id(("provider", i))
                            .px_2()
                            .py(px(3.))
                            .rounded(px(t.radius))
                            .border_1()
                            .border_color(t.border_strong)
                            .text_size(px(11.5))
                            .text_color(t.muted)
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.api_key(id.clone(), cx)))
                            .child(r.id.clone())
                    });
                    (
                        "Connect a model provider".into(),
                        "Swarm's agents need a model. Your keys stay inside Swarm; this app never sees them.".into(),
                        div()
                            .flex()
                            .flex_col()
                            .gap_2()
                            .pt_2()
                            .child(
                                self.option_card("opt-chatgpt", true, "Sign in with your ChatGPT plan", "Recommended. No API key: you confirm a short code on OpenAI's page.", Icon::Sessions)
                                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.chatgpt(cx))),
                            )
                            .child(
                                div()
                                    .p_3()
                                    .flex()
                                    .flex_col()
                                    .gap_2()
                                    .rounded(px(t.radius + 2.))
                                    .border_1()
                                    .border_color(t.border)
                                    .child(div().text_color(t.text).child("Or use an API key"))
                                    .child(div().text_size(px(11.5)).text_color(t.faint).child(if remote {
                                        "On a server, a provider copies the command that sets its key there."
                                    } else {
                                        "Opens a one-time Swarm page in your browser where you paste the key."
                                    }))
                                    .child(div().flex().flex_wrap().gap_1().pt_1().children(chips)),
                            )
                            .into_any_element(),
                        footer(None, primary(self, "provider-chatgpt", "Sign in with ChatGPT", true, cx)),
                    )
                }
            }
            Stage::ChatGpt { url, code } => {
                let code_copied = self.copied == Some("code");
                let link_copied = self.copied == Some("link");
                let (c2, u1, u2) = (code.clone(), url.clone(), url.clone());
                let link_hover = t.hover;
                (
                    "Confirm the code on OpenAI".into(),
                    "Open the page, sign in, and enter this code. You can select and copy the code and the link. This screen moves on by itself.".into(),
                    div()
                        .flex()
                        .flex_col()
                        .items_center()
                        .gap_3()
                        .pt_2()
                        .child(
                            // A read-only field: select it with the mouse, ctrl-c, or use the button.
                            div()
                                .w(px(224.))
                                .px(px(26.))
                                .py_2()
                                .rounded(px(t.radius + 2.))
                                .border_1()
                                .border_color(if code_copied { t.ok } else { alpha(t.accent, 0.5) })
                                .bg(alpha(t.accent, 0.06))
                                .text_size(px(28.))
                                .text_color(t.text)
                                .child(self.code_view.clone()),
                        )
                        .child(
                            div()
                                .w(px(400.))
                                .h(px(30.))
                                .px_2()
                                .flex()
                                .items_center()
                                .gap_2()
                                .rounded(px(t.radius))
                                .border_1()
                                .border_color(t.border)
                                .bg(t.surface)
                                .child(div().flex_1().min_w_0().text_size(px(12.)).text_color(t.accent).child(self.link_view.clone()))
                                .child(
                                    div()
                                        .id("signin-open")
                                        .px_2()
                                        .rounded(px(t.radius))
                                        .text_size(px(11.5))
                                        .text_color(t.muted)
                                        .cursor_pointer()
                                        .hover(move |s| s.bg(link_hover))
                                        .on_click(cx.listener(move |_, _: &ClickEvent, _, cx| cx.open_url(&u2)))
                                        .child("open ↗"),
                                ),
                        )
                        .child(
                            div()
                                .flex()
                                .gap_2()
                                .child(
                                    self.button("copy-code", if code_copied { "✓ Copied" } else { "Copy code" }, false, true)
                                        .when(code_copied, |b| b.border_color(t.ok).text_color(t.ok))
                                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.copy("code", c2.clone(), cx))),
                                )
                                .child(
                                    self.button("copy-link", if link_copied { "✓ Copied" } else { "Copy link" }, false, true)
                                        .when(link_copied, |b| b.border_color(t.ok).text_color(t.ok))
                                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.copy("link", u1.clone(), cx))),
                                ),
                        )
                        .child(div().pt_1().child(waiting(self, "Waiting for you to finish signing in…")))
                        .into_any_element(),
                    footer(
                        Some(self.button("chatgpt-back", "Back", false, true).on_click(cx.listener(|this, _: &ClickEvent, w, cx| this.back(&Back, w, cx))).into_any_element()),
                        primary(self, "open-signin", "Open sign-in page", true, cx),
                    ),
                )
            }
            Stage::ApiKey { provider } => (
                format!("Paste your {provider} key"),
                "It goes to Swarm on the machine once and is kept there, not in this app.".into(),
                div().pt_4().child(self.field("API key", &self.key, self.key.focus_handle(cx).is_focused(window))).into_any_element(),
                footer(
                    Some(self.button("key-back", "Back", false, true).on_click(cx.listener(|this, _: &ClickEvent, w, cx| this.back(&Back, w, cx))).into_any_element()),
                    primary(self, "save-key", "Save key", true, cx),
                ),
            ),
            Stage::Project { suggestions_loaded } => {
                let name_focused = self.name.focus_handle(cx).is_focused(window);
                let folder_focused = self.folder.focus_handle(cx).is_focused(window);
                let local = matches!(self.target, Target::Local);
                let any = self.folders.iter().any(|(_, on)| *on);
                let rows = self.folders.iter().enumerate().map(|(i, (path, on))| {
                    let hover = t.hover;
                    div()
                        .id(("folder", i))
                        .h(px(28.))
                        .px_2()
                        .flex()
                        .items_center()
                        .gap_2()
                        .rounded(px(t.radius))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_folder(i, cx)))
                        .child(
                            div()
                                .size(px(14.))
                                .flex()
                                .items_center()
                                .justify_center()
                                .rounded(px(3.))
                                .border_1()
                                .border_color(if *on { t.accent } else { t.border_strong })
                                .bg(if *on { t.accent } else { alpha(t.accent, 0.0) })
                                .text_size(px(10.))
                                .text_color(t.accent_text)
                                .child(if *on { "✓" } else { "" }),
                        )
                        .child(div().flex_1().min_w_0().truncate().text_color(if *on { t.text } else { t.muted }).child(path.clone()))
                });
                (
                    match &self.existing {
                        Some(e) => format!("Add folders to {}", e.name),
                        None if self.first => "Add your first project".into(),
                        None => "Add a project".into(),
                    },
                    format!("Pick the folders it works in, on {label}. Nothing in them changes."),
                    div()
                        .flex()
                        .flex_col()
                        .gap_3()
                        // Folders for an existing project: its name is not up for change here.
                        .when(self.existing.is_none(), |d| d.child(self.field("PROJECT NAME", &self.name, name_focused)))
                        .child(
                            div()
                                .flex()
                                .flex_col()
                                .gap_1()
                                .child(div().text_size(px(11.)).text_color(t.faint).child("FOLDERS"))
                                .child(
                                    div()
                                        .id("folder-list")
                                        .h(px(104.))
                                        .overflow_y_scroll()
                                        .p_1()
                                        .rounded(px(t.radius + 1.))
                                        .border_1()
                                        .border_color(t.border)
                                        .bg(t.surface)
                                        .when(!suggestions_loaded, |d| d.child(div().p_2().child(waiting(self, "Looking at your home folder…"))))
                                        .when(suggestions_loaded && self.folders.is_empty(), |d| {
                                            d.child(div().p_2().text_color(t.faint).child("Your home folder is empty. Type a name below to make a folder."))
                                        })
                                        .children(rows),
                                ),
                        )
                        .child(
                            div()
                                .flex()
                                .gap_2()
                                .items_end()
                                .child(div().flex_1().child(self.field(
                                    &if local { "OR A NEW OR EXISTING FOLDER (NAME OR PATH)".to_string() } else { format!("NEW OR EXISTING FOLDER IN {}", self.target.home_dir()) },
                                    &self.folder,
                                    folder_focused,
                                )))
                                .child(
                                    self.button("add-folder", if local && self.folder.read(cx).text().trim().is_empty() { "Choose…" } else { "Add" }, false, true)
                                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.add_folder(window, cx))),
                                ),
                        )
                        .into_any_element(),
                    footer(
                        // Opened from the console (not the first run): a way back without a project.
                        (!self.first).then(|| {
                            self.button("back-to-console", "back to the console".to_string(), false, !busy)
                                .on_click(cx.listener(|_, _: &ClickEvent, _, cx| cx.emit(OnboardingEvent::Finished)))
                                .into_any_element()
                        }),
                        primary(self, "create-project", if self.existing.is_some() { "Add folders" } else { "Create project" }, any, cx),
                    ),
                )
            }
            Stage::Review { review, picked } => {
                let count = picked.iter().filter(|p| **p).count();
                let rows = review.files.iter().zip(picked.iter()).enumerate().map(|(i, (file, on))| {
                    let hover = t.hover;
                    let on = *on;
                    let size = if file.size < 1024 { format!("{} B", file.size) } else { format!("{} KB", file.size / 1024) };
                    div()
                        .id(("pick", i))
                        .h(px(28.))
                        .px_2()
                        .flex()
                        .items_center()
                        .gap_2()
                        .rounded(px(t.radius))
                        .when(file.selectable, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_pick(i, cx)))
                        .child(
                            div()
                                .size(px(14.))
                                .flex()
                                .items_center()
                                .justify_center()
                                .rounded(px(3.))
                                .border_1()
                                .border_color(if on { t.accent } else { t.border_strong })
                                .bg(if on { t.accent } else { alpha(t.accent, 0.0) })
                                .text_size(px(10.))
                                .text_color(t.accent_text)
                                .child(if on { "✓" } else { "" }),
                        )
                        .child(div().flex_1().min_w_0().truncate().text_color(if file.selectable { t.text } else { t.faint }).child(file.path.clone()))
                        .child(div().text_size(px(11.)).text_color(t.faint).child(if file.selectable { size } else { "cannot be committed".into() }))
                });
                (
                    "Choose the first commit".into(),
                    format!("{} has files but no Git history. Agents work only in a committed repository, so pick what its first commit holds.", review.path),
                    div()
                        .flex()
                        .flex_col()
                        .gap_2()
                        .child(
                            div()
                                .id("pick-list")
                                .h(px(150.))
                                .overflow_y_scroll()
                                .p_1()
                                .rounded(px(t.radius + 1.))
                                .border_1()
                                .border_color(t.border)
                                .bg(t.surface)
                                .when(review.files.is_empty(), |d| d.child(div().p_2().text_color(t.faint).child("No files Swarm can commit were found.")))
                                .children(rows),
                        )
                        .child(div().text_size(px(11.)).text_color(t.faint).child(review.warning.clone()))
                        .into_any_element(),
                    footer(
                        Some(
                            self.button("review-back", "Back", false, !busy)
                                .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.go(Stage::Project { suggestions_loaded: true }, cx)))
                                .into_any_element(),
                        ),
                        primary(self, "commit-baseline", &format!("Commit {count} file{}", if count == 1 { "" } else { "s" }), count > 0, cx),
                    ),
                )
            }
            Stage::SettingUp { project, phase, context } => {
                let state = |n: usize| if phase > n { StepState::Done } else if phase == n { StepState::Running } else { StepState::Waiting };
                let mut body = div()
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .pt_1()
                    .child(self.checklist_row(StepState::Done, "Folders registered", None))
                    .child(self.checklist_row(state(1), "Reading AGENTS.md, READMEs and the file tree", None))
                    .child(self.checklist_row(state(2), "Writing project.md", None));
                if let Some(md) = &context {
                    body = body.child(
                        div()
                            .id("project-md")
                            .mt_3()
                            .h(px(170.))
                            .overflow_y_scroll()
                            .p_3()
                            .rounded(px(t.radius + 1.))
                            .border_1()
                            .border_color(t.border)
                            .bg(t.surface)
                            .text_size(px(11.5))
                            .line_height(px(17.))
                            .text_color(t.muted)
                            .children(md.lines().take(40).map(|l| div().min_h(px(17.)).child(l.to_string()))),
                    );
                }
                (
                    format!("Getting to know {project}"),
                    "Swarm reads your folders and writes project.md: the context every agent and the Orchestrator work from.".into(),
                    body.into_any_element(),
                    footer(None, primary(self, "settingup-next", if context.is_some() { "Continue" } else { "Writing…" }, context.is_some(), cx)),
                )
            }
            Stage::Ready { project } => (
                "You're set".into(),
                format!("{project} is ready on {label}. Tell the Orchestrator what you want done, and it hands the work to your agents."),
                div()
                    .flex()
                    .flex_col()
                    .gap_2()
                    .pt_2()
                    .child(self.checklist_row(StepState::Done, "Swarm", Some(&label)))
                    .child(self.checklist_row(StepState::Done, "Model provider", None))
                    .child(self.checklist_row(StepState::Done, &project, None))
                    .child(self.checklist_row(StepState::Done, "project.md", None))
                    .into_any_element(),
                footer(None, primary(self, "open-swarm", "Open Swarm", true, cx)),
            ),
        }
    }
}

impl Render for Onboarding {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        if let Some(handle) = self.pending_focus.take() {
            window.focus(&handle);
        }
        let t = self.theme.clone();
        let step = self.stage.step();
        let breathing = matches!(self.stage, Stage::Welcome) || self.busy;
        let (title, subtitle, body, footer) = self.content(window, cx);
        let big_mark = matches!(self.stage, Stage::Welcome | Stage::Ready { .. }).then(|| self.mark(52., true));

        let segments = (0..4).map(|i| {
            let done = step.is_some_and(|s| s > i);
            let current = step == Some(i);
            div()
                .flex_1()
                .h(px(3.))
                .rounded(px(2.))
                .bg(if done { t.accent } else if current { alpha(t.accent, 0.45) } else { t.border })
        });
        let step_label = match step {
            None => String::new(),
            Some(4) => "Done".into(),
            Some(s) => format!("Step {} of 4", s + 1),
        };

        // The error line has a reserved height, so a short one moves nothing;
        // a long message (Git's, a refusal of Swarm's) wraps so all of it is read.
        let error_line = div().min_h(px(40.)).flex().items_center().children(self.error.clone().map(|e| {
            div()
                .w_full()
                .px_3()
                .py_2()
                .rounded(px(t.radius + 1.))
                .border_1()
                .border_color(alpha(t.err, 0.45))
                .bg(alpha(t.err, 0.08))
                .text_size(px(11.5))
                .line_height(px(16.))
                .text_color(t.err)
                .child(e.split_whitespace().collect::<Vec<_>>().join(" "))
        }))
        .children(self.note.clone().filter(|_| self.error.is_none()).map(|n| {
            div()
                .w_full()
                .px_3()
                .py_2()
                .rounded(px(t.radius + 1.))
                .border_1()
                .border_color(t.border)
                .bg(t.surface)
                .text_size(px(11.5))
                .text_color(t.muted)
                .truncate()
                .child(n)
        }));

        let seq = self.seq;
        let card = div()
            .w(px(CARD_W))
            .h(px(CARD_H))
            .flex()
            .flex_col()
            .p(px(28.))
            .rounded(px(t.radius * 2. + 6.))
            .border_1()
            .border_color(t.border)
            .bg(t.panel)
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .child(self.mark(26., breathing))
                    .child(div().text_color(t.text).text_size(px(12.)).child("S W A R M"))
                    .child(div().flex_1())
                    .child(div().text_size(px(11.)).text_color(t.faint).child(step_label)),
            )
            .child(div().pt_4().flex().gap_1().children(segments))
            .child(
                div()
                    .flex_1()
                    .min_h_0()
                    .flex()
                    .flex_col()
                    .pt(px(26.))
                    .children(big_mark.map(|m| div().pb_4().child(m)))
                    .child(div().text_size(px(20.)).text_color(t.text).child(title))
                    .child(div().pt_2().text_color(t.muted).line_height(px(19.)).child(subtitle))
                    .child(div().flex_1().min_h_0().pt_3().child(body))
                    .with_animation(
                        SharedString::from(format!("stage-{seq}")),
                        Animation::new(Duration::from_millis(200)).with_easing(ease_in_out),
                        |el, t| el.opacity(t),
                    ),
            )
            .child(error_line)
            .child(footer);

        let hint = format!(
            "{} · enter to continue{}",
            self.place(),
            if matches!(self.stage, Stage::Server | Stage::ChatGpt { .. } | Stage::ApiKey { .. }) { " · esc to go back" } else { "" }
        );

        div()
            .key_context("Onboarding")
            .track_focus(&self.focus)
            .on_action(cx.listener(Self::primary))
            .on_action(cx.listener(Self::back))
            .size_full()
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .gap_4()
            .bg(t.bg)
            .text_color(t.text)
            .font_family(t.mono)
            .text_size(px(13.))
            .child(card)
            .child(div().text_size(px(11.)).text_color(t.faint).child(hint))
    }
}


/// Moves the install checklist on by one progress event. Steps before the
/// current one that never reported were skipped (an existing Docker, no AI to
/// connect); a running step finishes when the next one starts. Returns the
/// error text for an error event.
fn advance(steps: &mut [StepState], current: &mut String, event: Progress) -> Option<String> {
    match event {
        Progress::Step { id, text } => {
            if let Some(i) = progress::STEPS.iter().position(|(s, _)| *s == id) {
                for state in steps.iter_mut().take(i) {
                    *state = match *state {
                        StepState::Waiting => StepState::Skipped,
                        StepState::Running => StepState::Done,
                        s => s,
                    };
                }
                steps[i] = StepState::Running;
            }
            *current = text;
            None
        }
        Progress::Done(text) => {
            for state in steps.iter_mut() {
                *state = match *state {
                    StepState::Running => StepState::Done,
                    StepState::Waiting => StepState::Skipped,
                    s => s,
                };
            }
            *current = text;
            None
        }
        Progress::Error(text) => {
            for state in steps.iter_mut() {
                if *state == StepState::Running {
                    *state = StepState::Failed;
                }
            }
            Some(text)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use StepState::*;

    fn step(id: &str) -> Progress {
        Progress::Step { id: id.into(), text: id.into() }
    }

    #[test]
    fn checklist_follows_the_installer() {
        let mut steps = vec![Waiting; progress::STEPS.len()];
        let mut current = String::new();
        advance(&mut steps, &mut current, step("prerequisites"));
        advance(&mut steps, &mut current, step("build"));
        assert_eq!(&steps[..4], &[Done, Skipped, Skipped, Running]);
        assert_eq!(current, "build");
        advance(&mut steps, &mut current, step("connect-app"));
        advance(&mut steps, &mut current, Progress::Done("Swarm is running".into()));
        assert_eq!(steps, vec![Done, Skipped, Skipped, Done, Skipped, Skipped, Skipped, Done]);
        assert_eq!(current, "Swarm is running");
    }

    #[test]
    fn errors_mark_the_running_step() {
        let mut steps = vec![Waiting; progress::STEPS.len()];
        let mut current = String::new();
        advance(&mut steps, &mut current, step("fetch"));
        assert_eq!(advance(&mut steps, &mut current, Progress::Error("no network".into())), Some("no network".into()));
        assert_eq!(&steps[..3], &[Skipped, Failed, Waiting]);
        // Unknown steps change only the line under the list.
        advance(&mut steps, &mut current, step("something-new"));
        assert_eq!(current, "something-new");
    }
}

/// A project that gets more folders through the project step.
#[derive(Clone, Debug)]
pub struct Existing {
    pub id: String,
    pub name: String,
    /// Its folders as Swarm lists them ({workspace_id, path, role}), kept.
    pub workspaces: Vec<Value>,
}

impl Onboarding {
    /// Not the first project: the heading says so.
    pub fn another_project(&mut self, cx: &mut Context<Self>) {
        self.first = false;
        self.detect(cx);
    }
}

impl Onboarding {
    /// The project this run made or changed, once it has.
    pub fn project_id(&self) -> Option<String> {
        self.project_id.clone()
    }
}
