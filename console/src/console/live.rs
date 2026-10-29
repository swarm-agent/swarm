//! The console on a real Swarm machine: connecting, applying what the poller
//! reads, and the commands the panes send (start, follow up, approve, merge,
//! stop).

use std::collections::HashMap;
use std::time::Duration;

use gpui::{Context, Focusable, SharedString, Window, prelude::*};

use super::{Console, Link, Open, Page};
use crate::data::{Entry, LiveRef, Machine, Project, RunState, Session, Text};
use crate::onboarding::input_colors;
use crate::swarm::client::Client;
use crate::swarm::live::{Command, Live, Snapshot, Update};
use crate::swarm::target::Target;
use crate::text_input::{Pasted, TextInput};

/// What a session's header buttons ask Swarm to do.
#[derive(Clone, Copy)]
pub(super) enum Act {
    Approve,
    Reject,
    Stop,
}

pub(super) fn draft(project: usize) -> Session {
    Session {
        live: None,
        title: "New session".into(),
        project,
        machine: 0,
        model: "".into(),
        state: RunState::Idle,
        age: "".into(),
        elapsed: "".into(),
        tokens: "".into(),
        entries: vec![],
        tasks: vec![],
        activity: vec![],
        status: None,
        pending: None,
        model_choice: None,
        model_favorite: None,
        pinned: false,
        calls: vec![],
        outbox: vec![],
        waiting_on: None,
        run: None,
    }
}

/// The machine until Swarm answers: nothing known yet.
pub(super) fn placeholder_machine(target: &Target) -> Machine {
    Machine::unread(target.label().into(), if matches!(target, Target::Local) { "local".into() } else { "ssh".into() }, "owner".into())
}

pub(super) fn placeholder_project() -> Project {
    Project { name: "connecting…".into(), machines: vec![0], id: "".into(), workspaces: vec![], roles: vec![], context: "".into(), context_generation: Default::default(), theme_id: "".into() }
}

impl Console {
    /// Connects to the machine with the stored key and follows it.
    pub(super) fn connect_live(&mut self, cx: &mut Context<Self>) {
        let target = Target::from_env().unwrap_or(Target::Local);
        let via = self.via.clone();
        let kind = if via.is_some() {
            "linked"
        } else if matches!(target, Target::Local) {
            "local"
        } else {
            "ssh"
        };
        self.link = Link::Connecting;
        self._live = Some(cx.spawn(async move |this, cx| {
            let started = cx
                .background_executor()
                .spawn(async move {
                    let token = target.stored_token().ok_or("This app has no session for the machine. Restart it to connect again.")?;
                    let home = Client::new(target.api(), token.clone());
                    // A linked machine is home's route to its API; the same
                    // session, the same routes after the prefix.
                    let client = match &via {
                        Some(name) => Client::new(crate::swarm::fleet::machine_base(home.base(), name), token),
                        None => home.clone(),
                    };
                    let mut live = Live::start(client, kind);
                    let updates = live.take_updates();
                    Ok::<_, String>((live, updates, home))
                })
                .await;
            let mut updates = None;
            let ok = this
                .update(cx, |c, cx| {
                    match started {
                        Ok((live, rx, home)) => {
                            c.live = Some(live);
                            updates = rx;
                            // Only home's window follows home's fleet.
                            if c.via.is_none() {
                                c.fleet = Some(crate::swarm::fleet::Fleet::start(home.clone()));
                            }
                            c.home = Some(home);
                        }
                        Err(e) => c.link = Link::Lost(e.into()),
                    }
                    cx.notify();
                })
                .is_ok();
            let Some(mut rx) = updates.filter(|_| ok) else { return };
            // Wake on every update (no polling); once a second otherwise, so a
            // running session's clock counts up.
            loop {
                let (back, got) = cx
                    .background_executor()
                    .spawn(async move {
                        let got = rx.recv_timeout(Duration::from_secs(1));
                        let mut batch = Vec::new();
                        if let Ok(first) = got {
                            batch.push(first);
                            batch.extend(rx.try_iter());
                        }
                        (rx, batch)
                    })
                    .await;
                rx = back;
                if !got.is_empty() && std::env::var_os("SWARM_CONSOLE_TRACE").is_some() {
                    eprintln!("woke {}", std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0));
                }
                if this.update(cx, |c, cx| c.pump(got, cx)).is_err() {
                    return;
                }
            }
        }));
    }

    fn pump(&mut self, updates: Vec<Update>, cx: &mut Context<Self>) {
        if let Some(reading) = self.fleet.as_ref().and_then(|f| f.latest()) {
            let home = self.machines.first().map(|m| m.name.clone());
            match reading.machines {
                Some(fleet) => {
                    self.machines.truncate(1);
                    self.machines.extend(fleet.into_iter().filter(|m| Some(&m.name) != home.as_ref()));
                    self.needs = reading.needs;
                }
                // Home's list was not read: the machines stand, and so does
                // what was read from them; home's own part is new.
                None => match (self.needs.first_mut(), reading.needs.into_iter().next()) {
                    (Some(first), Some(here)) => *first = here,
                    (None, Some(here)) => self.needs = vec![here],
                    _ => {}
                },
            }
            // The fleet's keys are read again with each fleet read while the
            // Fleet page shows (a key entered on Swarm's key page appears).
            if self.page == Page::Fleet {
                self.load_fleet_keys(cx);
            }
            cx.notify();
        }
        if self.live.is_none() {
            return;
        }
        if updates.is_empty() {
            // A running session's timer counts up between updates.
            let running = self.panes.iter().any(|p| {
                let s = &self.sessions[p.session];
                s.run.is_some_and(|r| r.active)
                    || s.entries.iter().rev().take(64).any(|e| matches!(e, Entry::Tool { state: crate::data::ToolState::Running, .. }))
            });
            if running && self.ticked.elapsed() >= Duration::from_secs(1) {
                self.ticked = std::time::Instant::now();
                cx.notify();
            }
            return;
        }
        if std::env::var_os("SWARM_CONSOLE_TRACE").is_some() {
            let kinds: Vec<&str> = updates.iter().map(|u| match u { Update::Snapshot(_) => "snapshot", Update::Session(_) => "session", Update::Facts(_) => "facts", Update::Lost(_) => "lost", Update::Done { .. } => "done", Update::Review { .. } => "review", Update::Hold { .. } => "hold", Update::AutomationsChanged => "automations", Update::PermissionsChanged => "permissions", Update::RunActivity(_) => "run" }).collect();
            eprintln!("pump {:?} {}", std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0), kinds.join(","));
        }
        for update in updates {
            match update {
                Update::Snapshot(s) => {
                    self.apply(s);
                    // Runs are read once connected, so a failure shows on any page.
                    if self.automations.is_none() && !self.automations_busy {
                        self.load_automations(cx);
                    }
                    // The Permissions page open from the start reads once connected.
                    if self.page == Page::Permissions && self.permissions.is_none() {
                        self.load_permissions();
                    }
                    self.open_pending();
                    // A machine window opened for one item opens it now.
                    if let Some(target) = self.focus_on_connect.take() {
                        self.open_target(target, cx);
                    }
                    self.select_pending_project(cx);
                    self.answer_calls(cx);
                    self.follow_theme(cx);
                }
                Update::Session(s) => {
                    self.apply_session(s);
                    self.answer_calls(cx);
                }
                Update::Facts(f) => {
                    self.apply_facts(f);
                    self.follow_theme(cx);
                }
                Update::Lost(e) => self.link = Link::Lost(e.into()),
                Update::Done { what: what @ ("permissions" | "permission"), result, .. } => self.permissions_done(what, result),
                Update::Done { draft, what, result } => {
                    let linked = what == "link" && result.is_ok();
                    self.done(draft, what, result);
                    // A conversation brought back belongs in the list: ask again.
                    if what == "unarchive" {
                        self.run_search(cx);
                    }
                    if linked {
                        if let Some(code) = &self.link_code {
                            code.update(cx, |input, cx| input.set_text("", cx));
                        }
                    }
                }
                Update::AutomationsChanged => self.load_automations(cx),
                // Read again once the page has shown them; otherwise on opening it.
                Update::PermissionsChanged => {
                    if self.permissions.is_some() || self.page == Page::Permissions {
                        self.load_permissions();
                    }
                }
                Update::RunActivity(session_id) => {
                    if self.run_open.as_ref().is_some_and(|r| r.session_id == session_id) {
                        self.load_run(cx);
                    }
                }
                Update::Hold { permission_id, why } => {
                    let id: Text = permission_id.into();
                    self.answered.remove(&id);
                    self.holds.insert(id, why);
                }
                Update::Review { permission_id, review } => {
                    let id: Text = permission_id.into();
                    self.answered.remove(&id);
                    // Hidden files (.env and the like) start unticked.
                    let picked = review.files.iter().map(|f| f.selectable && !f.path.starts_with('.') && !f.path.contains("/.")).collect();
                    self.reviews.insert(id, (review, picked));
                }
            }
        }
        if let Some(live) = &self.live {
            live.watch(self.panes.iter().filter_map(|p| self.sessions[p.session].live.as_ref()).map(|l| l.session_id.to_string()));
        }
        self.want_contexts();
        cx.notify();
    }

    /// The machine, roles, usage, themes and models as the facts thread read them.
    fn apply_facts(&mut self, f: crate::swarm::live::Facts) {
        if let Some(m) = f.machine {
            match self.machines.first_mut() {
                Some(home) => *home = m,
                None => self.machines.push(m),
            }
        }
        if let Some(r) = f.roles.filter(|r| !r.is_empty()) {
            self.roles = r;
        }
        if let Some(t) = f.themes {
            self.project_themes = t;
        }
        if let Some(u) = f.usage {
            self.usage = u;
        }
        if let Some(c) = f.catalog {
            self.catalog = c;
        }
    }

    /// One conversation as Swarm now has it; the rest stays as it is.
    fn apply_session(&mut self, mut next: Session) {
        let Some(l) = next.live.clone() else { return };
        match self.sessions.iter().position(|s| s.live.as_ref() == Some(&l)) {
            Some(i) => {
                let s = &mut self.sessions[i];
                if next.entries.is_empty() {
                    next.entries = std::mem::take(&mut s.entries);
                    next.tasks = std::mem::take(&mut s.tasks);
                    next.model = s.model.clone();
                }
                next.outbox = std::mem::take(&mut s.outbox);
                next.settle_outbox();
                *s = next;
            }
            None => {
                self.rank.entry(l.session_id.clone()).or_insert(0);
                self.sessions.push(next);
            }
        }
    }

    fn apply(&mut self, mut snap: Snapshot) {
        let first = self.link != Link::Live;
        self.link = Link::Live;
        let mut media = std::mem::take(&mut snap.media);
        // Home is the first machine; the rest are the fleet (fleet::Fleet).
        if let Some(m) = snap.machine {
            match self.machines.first_mut() {
                Some(home) => *home = m,
                None => self.machines.push(m),
            }
        }
        if let Some(r) = snap.roles.filter(|r| !r.is_empty()) {
            self.roles = r;
        }
        if let Some(t) = snap.themes {
            self.project_themes = t;
        }
        if let Some(u) = snap.usage {
            self.usage = u;
        }
        if let Some(c) = snap.catalog {
            self.catalog = c;
        }
        {
            // Snapshots contain Swarm's complete project list, including an empty
            // list after deleting the last project (the event may precede the reply).
            // Sessions keep their project when the list changes.
            let ids: Vec<Text> = self.projects.iter().map(|p| p.id.clone()).collect();
            let current = ids.get(self.project).cloned();
            for s in self.sessions.iter_mut() {
                s.project = ids
                    .get(s.project)
                    .and_then(|id| snap.projects.iter().position(|p| &p.id == id))
                    .unwrap_or(0);
            }
            self.projects = snap.projects;
            self.project = current.and_then(|id| self.projects.iter().position(|p| p.id == id)).unwrap_or(0);
            if self.deleted_project.as_ref().is_some_and(|id| !self.projects.iter().any(|p| p.id.as_ref() == id.as_str())) {
                self.deleted_project = None;
                self.project = 0;
                self.projects_tab = self.projects.first().map(|p| p.id.to_string());
                self.page = Page::Projects;
            }
        }

        // Sessions keep their place (panes point at them by index).
        let known: Vec<Text> = self.rank.keys().cloned().collect();
        self.rank.clear();
        let mut incoming: HashMap<Text, Session> = HashMap::new();
        for (rank, s) in snap.sessions.into_iter().enumerate() {
            if let Some(id) = s.live.as_ref().map(|l| l.session_id.clone()) {
                self.rank.insert(id.clone(), rank);
                self.gone.remove(id.as_ref());
                incoming.insert(id, s);
            }
        }
        // A session Swarm listed before and lists no more was archived or
        // deleted: it leaves the sidebar; a pane showing it keeps its text.
        for id in known.into_iter().filter(|id| !self.rank.contains_key(id)) {
            self.gone.insert(id.to_string());
        }
        for s in self.sessions.iter_mut() {
            let Some(l) = &s.live else { continue };
            if let Some(mut next) = incoming.remove(&l.session_id) {
                // Not read yet: keep what the pane already shows.
                if next.entries.is_empty() {
                    next.entries = std::mem::take(&mut s.entries);
                    next.tasks = std::mem::take(&mut s.tasks);
                    next.model = s.model.clone();
                }
                next.outbox = std::mem::take(&mut s.outbox);
                next.settle_outbox();
                *s = next;
            }
        }
        let mut new: Vec<Session> = incoming.into_values().collect();
        new.sort_by_key(|s| s.live.as_ref().and_then(|l| self.rank.get(&l.session_id)).copied().unwrap_or(usize::MAX));
        self.sessions.extend(new);
        // A session that started here can arrive from Swarm before its draft
        // learns its id: one session, one entry. The first stays; panes follow.
        let mut seen: HashMap<Text, usize> = HashMap::new();
        let mut drop = Vec::new();
        for (i, s) in self.sessions.iter().enumerate() {
            if let Some(l) = &s.live {
                match seen.get(&l.session_id) {
                    Some(first) => drop.push((i, *first)),
                    None => {
                        seen.insert(l.session_id.clone(), i);
                    }
                }
            }
        }
        for (dup, first) in drop.iter().rev() {
            let (dup, first) = (*dup, *first);
            // Keep what the draft knew that the record does not show yet.
            let gone = self.sessions.remove(dup);
            if !gone.outbox.is_empty() {
                let keep = &mut self.sessions[first];
                keep.outbox.extend(gone.outbox);
                keep.settle_outbox();
            }
            for p in self.panes.iter_mut() {
                if p.session == dup {
                    p.session = first;
                } else if p.session > dup {
                    p.session -= 1;
                }
            }
            if let Some(a) = self.acting {
                self.acting = Some(if a == dup { first } else if a > dup { a - 1 } else { a });
            }
            for si in self.drafts.values_mut() {
                if *si == dup {
                    *si = first;
                } else if *si > dup {
                    *si -= 1;
                }
            }
        }

        for m in media.iter_mut() {
            m.session = self.sessions.iter().position(|s| s.live.as_ref().is_some_and(|l| l.session_id == m.session_id));
            m.project = m.session.map(|si| self.sessions[si].project).unwrap_or(0);
        }
        self.media = media;
        if self.preview.is_some_and(|i| i >= self.media.len()) {
            self.preview = None;
        }

        // On connecting, the first pane shows the newest session of the project.
        if first {
            let newest = self.ordered(self.project).into_iter().find(|i| self.sessions[*i].live.is_some());
            if let (Some(i), Some(pane)) = (newest, self.panes.first_mut()) {
                if self.sessions[pane.session].live.is_none() && self.sessions[pane.session].entries.is_empty() {
                    pane.session = i;
                }
            }
        }
    }

    fn done(&mut self, draft: Option<u64>, what: &'static str, result: Result<serde_json::Value, String>) {
        match (draft.and_then(|d| self.drafts.remove(&d)), result) {
            (Some(si), Ok(receipt)) => {
                let field = |k: &str| Text::from(receipt.get(k).and_then(|v| v.as_str()).unwrap_or("").to_string());
                let s = &mut self.sessions[si];
                let session = receipt.get("session").and_then(|s| s.get("id")).and_then(|v| v.as_str()).map(|v| Text::from(v.to_string()));
                s.live = Some(LiveRef { task_id: field("task_id"), session_id: session.unwrap_or_else(|| field("session_id")) });
                s.activity.clear();
            }
            (Some(si), Err(e)) => {
                let s = &mut self.sessions[si];
                s.entries.push(Entry::tool("error", e.clone()));
                s.state = RunState::Failed;
                s.activity.clear();
                self.notice = Some((format!("Could not start the session: {e}").into(), true));
            }
            (None, Ok(_)) if what == "machine" => self.notice = Some(("Answered from your computer.".into(), false)),
            (None, Ok(_)) if what == "browser" => self.notice = Some(("Done in your browser.".into(), false)),
            (None, Ok(_)) if what == "refuse" => self.notice = Some(("Refused.".into(), false)),
            (None, Ok(answer)) if what == "link" || what == "unlink" => {
                let name = answer.get("machine").and_then(|m| m.get("name")).and_then(|v| v.as_str()).unwrap_or("the machine");
                let text = if what == "link" { format!("Linked {name}: home controls it now.") } else { format!("Unlinked {name}.") };
                if what == "unlink" {
                    self.agents_focus = super::AgentsFocus::Fleet;
                }
                // Read home's list again now instead of at the next turn.
                if let Some(home) = self.home.clone() {
                    self.fleet = Some(crate::swarm::fleet::Fleet::start(home));
                }
                self.notice = Some((text.into(), false));
            }
            (None, Ok(answer)) if what == "context" || what == "compact" => self.context_done(what, answer),
            (None, Ok(_)) if what == "model" => self.notice = Some(("This chat uses that model now.".into(), false)),
            (None, Ok(answer)) if what == "delete project" => {
                self.deleted_project = answer.get("id").and_then(|v| v.as_str())
                    .filter(|id| self.projects.iter().any(|p| p.id.as_ref() == *id))
                    .map(str::to_string);
                self.project = 0;
                self.projects_tab = None;
                self.page = Page::Projects;
                self.notice = Some(("Project deleted. Folders and files remain on disk.".into(), false));
            }
            (None, Ok(_)) if what == "default" => self.notice = Some(("Default saved for every conversation.".into(), false)),
            (None, Ok(_)) if what == "favorite" => self.notice = Some(("Favorite saved.".into(), false)),
            (None, Ok(_)) if what == "unfavorite" => self.notice = Some(("Favorite removed.".into(), false)),
            (None, Ok(answer)) if what == "stop" => {
                // Swarm says how the run ended, or that it is still winding down.
                let summary = answer.get("summary").and_then(|v| v.as_str()).unwrap_or("");
                let status = answer.get("run_status").and_then(|v| v.as_str()).unwrap_or("stop_requested");
                let text = if summary.is_empty() { format!("Stop: {status}.") } else { summary.to_string() };
                self.notice = Some((text.into(), false));
            }
            (None, Ok(_)) => {
                let text = match what {
                    "approve" => "Approved; it starts now.",
                    "reject" => "Rejected.",
                    "deny" => "Denied.",
                    "archive" => "Archived.",
                    "rename" => "Renamed.",
                    "pin" => "Pinned.",
                    "unpin" => "Unpinned.",
                    "unarchive" => "Brought back.",
                    "project" => "Project saved.",
                    _ => return,
                };
                self.notice = Some((text.into(), false));
            }
            (None, Err(e)) => {
                if what == "send" {
                    // Swarm did not take it: it leaves the outbox, the error says why.
                    if let Some(si) = self.acting {
                        self.sessions[si].outbox.pop();
                    }
                }
                // A refusal in your browser was answered to the agent already;
                // it is a notice here, not a failure of the conversation.
                if what == "browser" {
                    self.notice = Some((format!("Your browser refused it: {e}").into(), true));
                    return;
                }
                let text: SharedString = format!("Could not {what}: {e}").into();
                if let Some(si) = self.acting {
                    self.failure = Some((si, text.clone()));
                }
                self.notice = Some((text, true));
            }
        }
    }

    /// The project's sessions as the sidebar lists them: unsent new sessions
    /// first, then newest first.
    pub(super) fn ordered(&self, project: usize) -> Vec<usize> {
        let mut list: Vec<usize> = (0..self.sessions.len())
            .filter(|i| {
                let s = &self.sessions[*i];
                // An empty new session shows only while a pane has it open.
                s.project == project
                    && !s.live.as_ref().is_some_and(|l| self.gone.contains(l.session_id.as_ref()))
                    && (s.live.is_some() || !s.entries.is_empty() || self.is_open(*i))
            })
            .collect();
        // Drafts first, then pinned sessions, then the rest, newest first.
        list.sort_by_key(|i| {
            let s = &self.sessions[*i];
            match &s.live {
                None => (0, 0),
                Some(l) => (if s.pinned { 1 } else { 2 }, 1 + self.rank.get(&l.session_id).copied().unwrap_or(usize::MAX - 1)),
            }
        });
        list
    }

    /// Opens an empty session in the project, in the active pane.
    pub(super) fn new_session(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let project = self.project;
        let existing = (0..self.sessions.len())
            .find(|i| self.sessions[*i].live.is_none() && self.sessions[*i].entries.is_empty() && self.sessions[*i].project == project);
        let si = existing.unwrap_or_else(|| {
            self.sessions.push(draft(project));
            self.sessions.len() - 1
        });
        self.open(si, Open::Here);
        self.page = super::Page::Sessions;
        if let Some(input) = self.composers.get(&self.active.0) {
            window.focus(&input.focus_handle(cx));
        }
        cx.notify();
    }

    /// Every pane has its message box.
    pub(super) fn ensure_composers(&mut self, cx: &mut Context<Self>) {
        let colors = input_colors(&self.theme);
        if self.picker_query.is_none() {
            let input = cx.new(|cx| TextInput::new("Search models…", colors, cx));
            cx.observe(&input, |_, _, cx| cx.notify()).detach();
            self.picker_query = Some(input);
            let name = cx.new(|cx| TextInput::new("Name it, like \"Luna fast\"", colors, cx));
            cx.observe(&name, |_, _, cx| cx.notify()).detach();
            self.picker_name = Some(name);
        }
        if self.search_input.is_none() {
            let field = cx.new(|cx| TextInput::new("Words in a title or a message", colors, cx).one_line());
            // Each keystroke asks Swarm again (older answers are dropped).
            cx.observe(&field, |this, _, cx| {
                if this.page == Page::Search {
                    this.run_search(cx);
                }
                cx.notify();
            })
            .detach();
            self.search_input = Some(field);
        }
        if self.context_input.is_none() {
            let field = cx.new(|cx| TextInput::new("What agents should know about this project", colors, cx));
            cx.observe(&field, |_, _, cx| cx.notify()).detach();
            self.context_input = Some(field);
        }
        if self.link_code.is_none() {
            let field = cx.new(|cx| TextInput::new("Paste the machine's link code", colors, cx).one_line());
            cx.observe(&field, |_, _, cx| cx.notify()).detach();
            self.link_code = Some(field);
        }
        if self.rename_input.is_none() {
            let name = cx.new(|cx| TextInput::new("Name", colors, cx).one_line());
            cx.observe(&name, |_, _, cx| cx.notify()).detach();
            self.rename_input = Some(name);
        }
        if self.key_label.is_none() {
            let label = cx.new(|cx| TextInput::new("Name, like \"work\"", colors, cx));
            cx.observe(&label, |_, _, cx| cx.notify()).detach();
            self.key_label = Some(label);
            let value = cx.new(|cx| TextInput::new("Paste the key", colors, cx).secret());
            cx.observe(&value, |_, _, cx| cx.notify()).detach();
            self.key_value = Some(value);
        }
        for p in &self.panes {
            let pid = p.id.0;
            self.composers.entry(pid).or_insert_with(|| {
                let input = cx.new(|cx| TextInput::new("Tell Swarm what you want done…", colors, cx));
                cx.observe(&input, |_, _, cx| cx.notify()).detach();
                cx.subscribe(&input, move |this, _, pasted: &Pasted, cx| match pasted {
                    Pasted::Image(image) => this.attach_image(pid, image, cx),
                    Pasted::Files(paths) => this.attach_files(pid, paths, cx),
                })
                .detach();
                input
            });
        }
        let open: Vec<usize> = self.panes.iter().map(|p| p.id.0).collect();
        self.composers.retain(|id, _| open.contains(id));
        self.composer_widths.borrow_mut().retain(|id, _| open.contains(id));
    }

    /// Sends the pane's message: the first one starts the session in Swarm.
    pub(super) fn send(&mut self, pid: usize, cx: &mut Context<Self>) {
        let Some(si) = self.panes.iter().find(|p| p.id.0 == pid).map(|p| p.session) else { return };
        let Some(input) = self.composers.get(&pid).cloned() else { return };
        let mut text = input.read(cx).text().trim().to_string();
        let has_files = self.attachments.get(&pid).is_some_and(|f| !f.is_empty());
        if text.is_empty() && !has_files {
            return;
        }
        if text.starts_with('/') {
            self.slash(pid, &text, cx);
            return;
        }
        if self.live.is_none() {
            self.notice = Some(("Not connected to Swarm yet.".into(), true));
            cx.notify();
            return;
        }
        // A pending question takes the message as its answer: to the question
        // whose "Custom response" was picked, else the first unanswered one.
        if let Some(p) = self.sessions[si].pending.clone().filter(|p| p.question) {
            let answered = self.answers.get(&p.id).cloned().unwrap_or_default();
            let qid = self
                .custom_for
                .clone()
                .filter(|(perm, _)| *perm == p.id)
                .map(|(_, q)| q)
                .or_else(|| p.questions.iter().find(|q| !answered.contains_key(&q.id)).map(|q| q.id.clone()))
                .or_else(|| p.questions.first().map(|q| q.id.clone()));
            if let Some(qid) = qid {
                self.custom_for = None;
                self.choose(si, qid, text, cx);
            }
            input.update(cx, |i, cx| i.set_text("", cx));
            return;
        }
        let files = self.attachments.remove(&pid).unwrap_or_default();
        if text.is_empty() {
            text = format!("Attached: {}", files.iter().map(|f| f.name.as_str()).collect::<Vec<_>>().join(", "));
        }
        let command = match self.sessions[si].live.clone() {
            // A pending question takes the message as its answer.
            Some(l) => Command::Send { session_id: l.session_id.to_string(), text: text.clone(), files: files.clone() },
            None => {
                let p = &self.projects[self.sessions[si].project];
                if p.id.is_empty() {
                    self.notice = Some(("There is no project yet. Set one up in onboarding first.".into(), true));
                    cx.notify();
                    return;
                }
                let d = self.next_draft;
                self.next_draft += 1;
                self.drafts.insert(d, si);
                let workspace_path = p.workspaces.first().map(|w| w.to_string()).unwrap_or_default();
                let project_id = p.id.to_string();
                let s = &mut self.sessions[si];
                s.title = text.lines().next().unwrap_or("").chars().take(60).collect::<String>().into();
                s.activity = vec![("".into(), "starting…".into())];
                Command::Start {
                    draft: d,
                    project_id,
                    workspace_path,
                    text: text.clone(),
                    model: match (&s.model_favorite, &s.model_choice) {
                        (Some(id), _) => Some(crate::swarm::models::Choice::Favorite(id.clone())),
                        (None, Some(a)) => Some(crate::swarm::models::Choice::Temporary(a.clone())),
                        (None, None) => None,
                    },
                    files: files.clone(),
                }
            }
        };
        let s = &mut self.sessions[si];
        let before = s.sent_count(&text);
        s.outbox.push((text.clone().into(), before));
        self.acting = Some(si);
        self.picker = None;
        if let Some(live) = &self.live {
            live.run(command);
        }
        input.update(cx, |i, cx| i.set_text("", cx));
        cx.notify();
    }

    /// Records an answer to one of the pending questions; one question is
    /// answered at once, several once every required one has an answer.
    pub(super) fn choose(&mut self, si: usize, qid: Text, value: String, cx: &mut Context<Self>) {
        let Some(p) = self.sessions[si].pending.clone() else { return };
        self.answers.entry(p.id.clone()).or_default().insert(qid, value);
        if p.structured {
            cx.notify();
        } else {
            self.send_answers(si, cx);
        }
    }

    pub(super) fn send_answers(&mut self, si: usize, cx: &mut Context<Self>) {
        let (Some(p), Some(l)) = (self.sessions[si].pending.clone(), self.sessions[si].live.clone()) else { return };
        let given = self.answers.get(&p.id).cloned().unwrap_or_default();
        let Some(reason) = crate::swarm::map::answer(&p, &given) else {
            self.notice = Some(("Answer every required question first.".into(), true));
            cx.notify();
            return;
        };
        self.acting = Some(si);
        self.failure = None;
        if let Some(live) = &self.live {
            live.run(Command::Resolve {
                session_id: l.session_id.to_string(),
                permission_id: p.id.to_string(),
                allow: true,
                answer: Some(reason),
            });
        }
        self.notice = Some(("Answer sent.".into(), false));
        cx.notify();
    }

    /// A session's own buttons: stop its run, or allow or deny the tool call it waits on.
    pub(super) fn act(&mut self, si: usize, act: Act, cx: &mut Context<Self>) {
        let (Some(live), Some(l)) = (&self.live, self.sessions[si].live.clone()) else { return };
        let session_id = l.session_id.to_string();
        let pending = self.sessions[si].pending.clone();
        // A run asleep on tasks is stopped by its own id (see Command::Stop).
        let waiting_run_id = self.sessions[si].waiting_on.as_ref().filter(|_| self.sessions[si].state == RunState::Waiting).map(|w| w.run_id.to_string()).unwrap_or_default();
        let command = match (act, pending) {
            (Act::Stop, _) => Command::Stop { session_id, waiting_run_id },
            (Act::Approve, Some(p)) => Command::Resolve { session_id, permission_id: p.id.to_string(), allow: true, answer: None },
            (Act::Reject, Some(p)) => Command::Resolve { session_id, permission_id: p.id.to_string(), allow: false, answer: None },
            _ => return,
        };
        self.acting = Some(si);
        self.failure = None;
        live.run(command);
        self.notice = Some((if matches!(act, Act::Stop) { "Stopping…" } else { "Sent." }.into(), false));
        cx.notify();
    }

    /// A task card's buttons: approve or reject a proposed task, merge finished work.
    pub(super) fn card_act(&mut self, si: usize, ti: usize, act: Act, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        let Some(task) = self.sessions[si].tasks.get(ti) else { return };
        if let Act::Stop = act {
            if task.session.is_empty() {
                return;
            }
            let session_id = task.session.to_string();
            self.acting = Some(si);
            self.failure = None;
            live.run(Command::Stop { session_id, waiting_run_id: String::new() });
            self.notice = Some(("Stopping the task…".into(), false));
            cx.notify();
            return;
        }
        let task_id = task.key.to_string();
        let project_id = self.projects.get(self.sessions[si].project).map(|p| p.id.to_string()).unwrap_or_default();
        if task_id.is_empty() || project_id.is_empty() {
            return;
        }
        self.acting = Some(si);
        self.failure = None;
        live.run(match act {
            Act::Approve => Command::Approve { project_id, task_id, approve: true },
            Act::Reject => Command::Approve { project_id, task_id, approve: false },
            Act::Stop => return,
        });
        self.notice = Some((
            match act {
                Act::Approve => "Approving…",
                _ => "Rejecting…",
            }
            .into(),
            false,
        ));
        cx.notify();
    }

    /// The status bar's account of the connection.
    pub(super) fn link_text(&self) -> (SharedString, bool) {
        match &self.link {
            Link::Demo => ("demo data".into(), true),
            Link::Connecting => (format!("connecting to {}…", self.machines[0].name).into(), true),
            Link::Live => (format!("{} connected", self.machines[0].name).into(), false),
            Link::Lost(e) => (e.clone(), true),
        }
    }
}
