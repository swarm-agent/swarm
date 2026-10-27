//! Calls the Orchestrator makes to your machine. With full access this app
//! answers them at once; with "access, but ask" each waits on a card with run
//! and deny; a request for access shows the Orchestrator's reason and lets you
//! choose full, ask or deny. Swarm refuses them all while the shield says no
//! access, so none of them reaches this app then.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::data::{BrowserAccess, BrowserLease, ClientCall, MachineAccess};
use crate::fleet_config::Level;
use crate::machine;
use crate::swarm::live::Command;

impl Console {
    /// The session's shield for your machine (none when never chosen).
    fn machine_level(&self, si: usize) -> MachineAccess {
        self.sessions[si].access.map(|a| a.machine).unwrap_or(MachineAccess::None)
    }

    /// Sends one command for a call, once.
    fn answer_call(&mut self, si: usize, call: &ClientCall, command: Command, cx: &mut Context<Self>) {
        if !self.answered.insert(call.id.clone()) {
            return;
        }
        self.holds.remove(&call.id);
        if let Some(live) = &self.live {
            self.acting = Some(si);
            self.failure = None;
            live.run(command);
        }
        cx.notify();
    }

    /// A call to your PC with its working folder filled in: a command without
    /// one starts in your first allowed folder.
    fn with_folder(&self, call: &ClientCall) -> serde_json::Value {
        let mut args = call.args.clone();
        if call.tool.as_ref() == "machine_run" && args.get("cwd").and_then(|v| v.as_str()).is_none_or(str::is_empty) {
            if let Some(first) = self.fleet_config.folders.first() {
                args["cwd"] = serde_json::json!(first);
            }
        }
        args
    }

    /// Whether a call to your PC stays in the folders you allowed (always,
    /// when you allowed none). A command is judged by where it starts.
    fn inside_folders(&self, call: &ClientCall) -> bool {
        if self.fleet_config.folders.is_empty() {
            return true;
        }
        let args = self.with_folder(call);
        let key = if call.tool.as_ref() == "machine_run" { "cwd" } else { "path" };
        args.get(key).and_then(|v| v.as_str()).is_some_and(|p| self.fleet_config.allows(p))
    }

    /// The command that carries out a call at once, if it needs nothing from
    /// you: a call to your machine with full access, inside your folders.
    fn automatic(&self, si: usize, session_id: &str, call: &ClientCall) -> Option<Command> {
        let id = call.id.to_string();
        // A window on a linked machine: that machine's agents never reach
        // this computer or its browser, whatever they ask.
        if let Some(name) = &self.via {
            return Some(Command::Refuse {
                session_id: session_id.to_string(),
                permission_id: id,
                reason: format!("This conversation runs on {name}, a machine linked to the person's home Swarm; the person's own computer and browser are not reachable from it."),
            });
        }
        match call.tool.as_ref() {
            "request_machine_access" | "request_workspace_setup" | "request_browser_access" => None,
            "manage_browser" => self.automatic_browser(si, session_id, call),
            _ => (self.machine_level(si) == MachineAccess::Full && self.inside_folders(call)).then(|| Command::Client {
                session_id: session_id.to_string(),
                permission_id: id,
                tool: call.tool.to_string(),
                args: self.with_folder(call),
            }),
        }
    }

    /// Calls that need nothing from you run now.
    pub(super) fn answer_calls(&mut self, cx: &mut Context<Self>) {
        let mut runs = Vec::new();
        for (si, s) in self.sessions.iter().enumerate() {
            let Some(l) = &s.live else { continue };
            for call in &s.calls {
                if self.answered.contains(&call.id) {
                    continue;
                }
                if let Some(command) = self.automatic(si, &l.session_id, call) {
                    runs.push((si, call.clone(), command));
                }
            }
        }
        for (si, call, command) in runs {
            self.notice = Some((format!("Running {}", describe(&call)).into(), false));
            self.answer_call(si, &call, command, cx);
        }
    }

    /// The cards for calls that wait on you, in the conversation.
    pub(super) fn render_calls(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let s = &self.sessions[si];
        let Some(session_id) = s.live.as_ref().map(|l| l.session_id.to_string()) else { return vec![] };
        let t = &self.theme;
        s.calls
            .iter()
            .filter(|c| !self.answered.contains(&c.id) && self.automatic(si, &session_id, c).is_none())
            .filter(|c| match c.tool.as_ref() {
                "request_machine_access" | "request_workspace_setup" | "request_browser_access" | "manage_browser" => true,
                _ => match self.machine_level(si) {
                    MachineAccess::Ask => true,
                    MachineAccess::Full => !self.inside_folders(c),
                    MachineAccess::None => false,
                },
            })
            .map(|call| {
                let key = format!("call-{pid}-{}", call.id);
                let button = |label: &'static str, strong: bool, command: Command| {
                    let call = call.clone();
                    self.button(SharedString::from(format!("{key}-{label}")), label, strong)
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.answer_call(si, &call, command.clone(), cx)))
                };
                let id = call.id.to_string();
                let refuse = |reason: &str| Command::Refuse { session_id: session_id.clone(), permission_id: id.clone(), reason: reason.to_string() };
                let (heading, actions, note): (String, gpui::Div, Option<&str>) = match call.tool.as_ref() {
                    "request_machine_access" => {
                        let grant = |level| Command::Grant { session_id: session_id.clone(), permission_id: id.clone(), level };
                        (
                            format!("THE ORCHESTRATOR ASKS TO USE YOUR COMPUTER ({})", crate::swarm::target::this_computer()),
                            div()
                                .flex()
                                .gap_1()
                                .child(button("full access", true, grant(MachineAccess::Full)))
                                .child(button("access, but ask", false, grant(MachineAccess::Ask)))
                                .child(button("deny", false, refuse("The person denied access to their machine."))),
                            Some("Full access runs its commands as you without asking; with ask, you run or deny each one. You can change this in the shield."),
                        )
                    }
                    "request_workspace_setup" => {
                        let path = call.args.get("workspace_path").and_then(|v| v.as_str()).unwrap_or("").to_string();
                        let deny = button("deny", false, refuse("The person declined to ready the folder."));
                        match self.reviews.get(&call.id) {
                            // The folder has files: the person picks the first commit here.
                            Some((review, picked)) => {
                                let count = picked.iter().filter(|p| **p).count();
                                let selected: Vec<String> = review.files.iter().zip(picked).filter(|(_, on)| **on).map(|(f, _)| f.path.clone()).collect();
                                let commit = Command::Commit { session_id: session_id.clone(), permission_id: id.clone(), review: review.clone(), selected };
                                let call_id = call.id.clone();
                                let rows = review.files.iter().zip(picked).enumerate().map(|(i, (file, on))| {
                                    let (on, selectable, hover, call_id) = (*on, file.selectable, t.hover, call_id.clone());
                                    div()
                                        .id(SharedString::from(format!("{key}-file-{i}")))
                                        .h(px(24.))
                                        .px_1()
                                        .flex()
                                        .items_center()
                                        .gap_2()
                                        .rounded(px(t.radius))
                                        .when(selectable, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
                                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.toggle_review(&call_id, i, cx)))
                                        .child(
                                            div()
                                                .size(px(13.))
                                                .flex()
                                                .items_center()
                                                .justify_center()
                                                .rounded(px(3.))
                                                .border_1()
                                                .border_color(if on { t.accent } else { t.border_strong })
                                                .bg(if on { t.accent } else { alpha(t.accent, 0.0) })
                                                .text_size(px(9.))
                                                .text_color(t.accent_text)
                                                .child(if on { "✓" } else { "" }),
                                        )
                                        .child(div().flex_1().min_w_0().truncate().text_color(if selectable { t.text } else { t.faint }).child(file.path.clone()))
                                        .child(div().text_size(px(11.)).text_color(t.faint).child(if selectable { format!("{} B", file.size) } else { "cannot be committed".into() }))
                                });
                                let label: &'static str = if count == 1 { "commit 1 file as the first commit" } else { "commit these as the first commit" };
                                (
                                    "THE ORCHESTRATOR ASKS TO READY A FOLDER".into(),
                                    div()
                                        .flex()
                                        .flex_col()
                                        .gap_2()
                                        .child(div().text_size(px(11.)).text_color(t.faint).child(format!("{path} has files but no Git history. Agents work only in a committed repository: tick what its first commit holds.")))
                                        .child(div().flex().flex_col().children(rows))
                                        .child(div().text_size(px(11.)).text_color(t.faint).child(review.warning.clone()))
                                        .child(div().flex().gap_1().when(count > 0, |d| d.child(button(label, true, commit))).child(deny)),
                                    None,
                                )
                            }
                            None => {
                                let ready = Command::Ready { session_id: session_id.clone(), permission_id: id.clone(), path };
                                (
                                    "THE ORCHESTRATOR ASKS TO READY A FOLDER".into(),
                                    div().flex().gap_1().child(button("set it up", true, ready)).child(deny),
                                    Some("Agents work only in a Git repository with a first commit. An empty folder gets an empty first commit from Swarm; a folder with files shows them here so you pick the first commit."),
                                )
                            }
                        }
                    }
                    "request_browser_access" => {
                        let wanted: Vec<String> = call.args.get("sites").and_then(|v| v.as_array()).map(|a| a.iter().filter_map(|x| x.as_str()).map(|x| x.trim().to_ascii_lowercase()).filter(|x| !x.is_empty()).collect()).unwrap_or_default();
                        // Asked during a live lease: the new sites join the
                        // ones it has. Any site, asked or chosen, covers all.
                        let current = self.lease_of(si);
                        let any = self.lease_any.contains(&call.id) || wanted.iter().any(|s| s == "*") || current.as_ref().is_some_and(|l| l.sites.iter().any(|s| s == "*"));
                        let sites: Vec<String> = if any {
                            vec!["*".to_string()]
                        } else {
                            let mut all = current.as_ref().map(|l| l.sites.clone()).unwrap_or_default();
                            for s in &wanted {
                                if !all.contains(s) {
                                    all.push(s.clone());
                                }
                            }
                            all
                        };
                        let asked = call.args.get("minutes").and_then(|v| v.as_u64()).filter(|m| *m > 0).unwrap_or(30);
                        let minutes = self.lease_minutes.get(&call.id).copied().unwrap_or(asked);
                        let added: Vec<String> = wanted.iter().filter(|s| !current.as_ref().is_some_and(|l| l.covers(s))).cloned().collect();
                        let sites_line = match (&current, any) {
                            (_, true) => "Sites: any site (it may go anywhere on the web; risky actions still follow the level)".to_string(),
                            (Some(_), false) => format!("Sites: {} (adds {})", sites.join(", "), if added.is_empty() { "nothing new".to_string() } else { added.join(", ") }),
                            (None, false) => format!("Sites: {}", if sites.is_empty() { "none named".to_string() } else { sites.join(", ") }),
                        };
                        let any_toggle = {
                            let call_id = call.id.clone();
                            let asked_any = wanted.iter().any(|s| s == "*");
                            self.button(SharedString::from(format!("{key}-any")), if any { "only the sites asked" } else { "allow any site" }, false)
                                .when(asked_any, |b| b.opacity(0.5))
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    if !asked_any && !this.lease_any.remove(&call_id) {
                                        this.lease_any.insert(call_id.clone());
                                    }
                                    cx.notify();
                                }))
                        };
                        let grant = |level| Command::GrantBrowser { session_id: session_id.clone(), permission_id: id.clone(), level, sites: sites.clone(), minutes };
                        let status = crate::browser::status();
                        let link = if status.connected {
                            format!("Chrome is connected (extension {}).", status.version)
                        } else {
                            "Chrome is not connected to this app yet (Settings → Browser). You can grant now and connect after; its actions wait until then.".to_string()
                        };
                        let step = |label: &'static str, delta: i64| {
                            let call_id = call.id.clone();
                            self.button(SharedString::from(format!("{key}-min-{label}")), label, false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                let next = (minutes as i64 + delta).max(1) as u64;
                                this.lease_minutes.insert(call_id.clone(), next);
                                cx.notify();
                            }))
                        };
                        (
                            if current.is_some() { "THE ORCHESTRATOR ASKS FOR MORE OF YOUR BROWSER".into() } else { "THE ORCHESTRATOR ASKS TO USE YOUR BROWSER".into() },
                            div()
                                .flex()
                                .flex_col()
                                .gap_2()
                                .child(div().flex().items_center().gap_2().child(div().text_size(px(11.)).text_color(if any { t.warn } else { t.muted }).child(sites_line)).child(any_toggle))
                                .child(
                                    div()
                                        .flex()
                                        .items_center()
                                        .gap_1()
                                        .text_size(px(11.))
                                        .text_color(t.muted)
                                        .child("For")
                                        .child(step("−5", -5))
                                        .child(div().text_color(t.text).child(format!("{minutes} min")))
                                        .child(step("+5", 5))
                                        .child(div().text_color(t.faint).child(if minutes == asked { "(as asked)".to_string() } else { format!("(it asked {asked})") })),
                                )
                                .child(div().text_size(px(11.)).text_color(if status.connected { t.faint } else { t.warn }).child(link))
                                .child(
                                    div()
                                        .flex()
                                        .flex_wrap()
                                        .gap_1()
                                        .child(button("ask each action", true, grant(BrowserAccess::Ask)))
                                        .child(button("confirm the critical ones", false, grant(BrowserAccess::Confirm)))
                                        .child(button("full (dangerous)", false, grant(BrowserAccess::Full)))
                                        .child(button("deny", false, refuse("The person denied the use of their browser."))),
                                ),
                            Some("Only those sites (or any, if you allow it), in the Swarm tab group it opens in Chrome, until the lease ends; it reads what those pages show. Ask: every action waits for you. Confirm: reading, moving and typing run; paying, publishing, keys and account settings wait for you. Full: everything runs. Stop any time from the shield or the extension."),
                        )
                    }
                    "manage_browser" => {
                        let held = self.holds.get(&call.id).cloned();
                        let run = Command::Browser { session_id: session_id.clone(), permission_id: id.clone(), args: call.args.clone(), confirmed: true };
                        let lease = self.lease_of(si);
                        let scope = match &lease {
                            Some(l) => format!("Lease: {} · {} · ends {}", l.level.as_str(), l.sites.join(", "), crate::swarm::live::ends_at_utc(l.until)),
                            None => "No live lease is known for this session; the action fails unless Swarm has one.".to_string(),
                        };
                        (
                            if held.is_some() { "IN YOUR BROWSER: THIS LOOKS CRITICAL".into() } else { "IN YOUR BROWSER".into() },
                            div()
                                .flex()
                                .flex_col()
                                .gap_2()
                                .children(held.map(|why| div().text_size(px(11.)).text_color(t.warn).child(why)))
                                .child(div().text_size(px(11.)).text_color(t.faint).child(scope))
                                .child(div().flex().gap_1().child(button("do it", true, run)).child(button("deny", false, refuse("The person denied this action in their browser.")))),
                            None,
                        )
                    }
                    _ => {
                        let run = Command::Client { session_id: session_id.clone(), permission_id: id.clone(), tool: call.tool.to_string(), args: self.with_folder(call) };
                        (
                            if self.inside_folders(call) { format!("ON YOUR COMPUTER ({}), AS YOU", crate::swarm::target::this_computer()) } else { format!("ON YOUR COMPUTER ({}), OUTSIDE YOUR ALLOWED FOLDERS", crate::swarm::target::this_computer()) },
                            div().flex().gap_1().child(button("run", true, run)).child(button("deny", false, refuse("The person denied this call on their machine."))),
                            None,
                        )
                    }
                };
                div()
                    .mx_1()
                    .px_3()
                    .py_2()
                    .flex()
                    .flex_col()
                    .gap_2()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(alpha(t.warn, 0.5))
                    .bg(t.panel)
                    .child(div().text_size(px(11.)).text_color(t.warn).child(heading))
                    .child(div().text_color(t.text).line_height(px(19.)).child(describe(call)))
                    .children(note.map(|n| div().text_size(px(11.)).text_color(t.faint).child(n)))
                    .child(actions)
                    .into_any_element()
            })
            .collect()
    }
}

impl Console {
    /// Ticks or unticks one file of a folder's first commit.
    fn toggle_review(&mut self, call_id: &crate::data::Text, i: usize, cx: &mut Context<Self>) {
        if let Some((review, picked)) = self.reviews.get_mut(call_id) {
            if review.files.get(i).is_some_and(|f| f.selectable) {
                if let Some(p) = picked.get_mut(i) {
                    *p = !*p;
                }
            }
        }
        cx.notify();
    }
}

/// One line saying what a call does.
fn describe(call: &ClientCall) -> String {
    machine::describe(call.tool.as_ref(), &call.args)
}

impl Console {
    /// Each fleet machine's level as Swarm stores it (none, ask, full).
    pub(super) fn fleet_levels(&self) -> std::collections::BTreeMap<String, String> {
        self.machines
            .iter()
            .skip(1)
            .map(|m| {
                let level = match self.fleet_config.level(&m.name) {
                    Level::Full => "full",
                    Level::Ask => "ask",
                    Level::Off => "none",
                };
                (m.name.to_string(), level.to_string())
            })
            .collect()
    }

    /// Tells the open conversations their fleet when it changed: machines
    /// came or went, or you changed a level.
    pub(super) fn sync_fleet(&mut self, cx: &mut Context<Self>) {
        if self.via.is_some() {
            return;
        }
        let Some(live) = &self.live else { return };
        let fleet = self.fleet_levels();
        let folders = self.fleet_config.folders.clone();
        if fleet.is_empty() && folders.is_empty() {
            return;
        }
        let told = format!("{fleet:?} {folders:?}");
        for p in &self.panes {
            let Some(l) = &self.sessions[p.session].live else { continue };
            if self.fleet_told.get(&l.session_id) != Some(&told) {
                self.fleet_told.insert(l.session_id.clone(), told.clone());
                live.run(Command::SetFleet { session_id: l.session_id.to_string(), fleet: fleet.clone(), folders: folders.clone() });
            }
        }
        cx.notify();
    }
}

impl Console {
    /// A session's browser lease: Swarm's record on the session, or what the
    /// app granted a moment ago (the record follows by event).
    pub(super) fn lease_of(&self, si: usize) -> Option<BrowserLease> {
        let now = super::now_ms();
        if let Some(l) = self.sessions[si].browser.as_ref().filter(|l| l.live(now)) {
            return Some(l.clone());
        }
        let id = self.sessions[si].live.as_ref()?.session_id.to_string();
        crate::browser::leases().into_iter().find(|(sid, l)| *sid == id && l.live(now)).map(|(_, l)| l)
    }

    /// The command that carries out a browser action at once, if the lease
    /// lets it: full runs it; confirm runs it unless the extension held it
    /// as critical; ask and an unknown lease wait for you.
    fn automatic_browser(&self, si: usize, session_id: &str, call: &ClientCall) -> Option<Command> {
        let level = self.lease_of(si)?.level;
        let confirmed = match level {
            BrowserAccess::Full => true,
            BrowserAccess::Confirm if !self.holds.contains_key(&call.id) => false,
            _ => return None,
        };
        Some(Command::Browser { session_id: session_id.to_string(), permission_id: call.id.to_string(), args: call.args.clone(), confirmed })
    }
}
