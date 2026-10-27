//! Calls the Orchestrator makes to your machine. Swarm decides each one by
//! this device's rules (the Permissions page): a call it marks "ready" for
//! this device runs here at once; one "awaiting_approval" waits on a card
//! with allow and deny. A request for access only points to the Permissions
//! page, where the rule is changed for every conversation.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};

use super::{Console, Page, alpha};
use crate::data::ClientCall;
use crate::machine;
use crate::swarm::live::Command;

impl Console {
    /// A device's name as Swarm's permissions list it ("this device" when it
    /// is this one), for a call that targets its client session.
    fn device_name(&self, client_session: &str) -> String {
        let devices = self.permissions.as_ref().and_then(|p| p.get("devices")).and_then(|d| d.as_array());
        let device = devices.and_then(|d| d.iter().find(|d| d.get("id").and_then(|v| v.as_str()) == Some(client_session)));
        match device {
            Some(d) if d.get("this_device").and_then(|v| v.as_bool()) == Some(true) => "this device".into(),
            Some(d) => d.get("name").and_then(|v| v.as_str()).unwrap_or("another device").to_string(),
            None => "another device".into(),
        }
    }

    /// Sends one command for a call, once.
    fn answer_call(&mut self, si: usize, call: &ClientCall, command: Command, cx: &mut Context<Self>) {
        // Approval and execution are distinct phases of the same record.
        if !matches!(&command, Command::Resolve { allow: true, .. }) && !self.answered.insert(call.id.clone()) {
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

    /// The command that carries out a call at once: one Swarm marked ready
    /// for this device.
    fn automatic(&self, session_id: &str, call: &ClientCall) -> Option<Command> {
        if call.execution.as_ref() != "ready" || !call.this_device {
            return None;
        }
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
            "manage_browser" => Some(Command::Browser { session_id: session_id.to_string(), permission_id: id, args: call.args.clone(), confirmed: true }),
            "machine_read" | "machine_write" | "machine_run" => Some(Command::Client {
                session_id: session_id.to_string(),
                permission_id: id,
                tool: call.tool.to_string(),
                args: call.args.clone(),
            }),
            _ => None,
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
                if let Some(command) = self.automatic(&l.session_id, call) {
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
            .filter(|c| !self.answered.contains(&c.id) && self.automatic(&session_id, c).is_none())
            .filter(|c| c.execution.as_ref() == "awaiting_approval" || matches!(c.tool.as_ref(), "request_workspace_setup" | "request_machine_access" | "request_browser_access"))
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
                    _ if call.execution.as_ref() == "awaiting_approval" => {
                        let approve = Command::Resolve { session_id: session_id.clone(), permission_id: id.clone(), allow: true, answer: None };
                        let what = if call.tool.as_ref() == "manage_browser" { "IN THE BROWSER OF" } else { "ON" };
                        (format!("{what} {}, AS YOU", self.device_name(&call.target_device).to_uppercase()),
                         div().flex().gap_1().child(button("allow", true, approve)).child(button("deny", false, refuse("The person denied this device call."))),
                         Some("Allow lets that device run this one call; your Permissions page sets when it asks."))
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
                    // request_machine_access and request_browser_access: access
                    // is a rule of this device, not a grant in a conversation.
                    _ => {
                        let what = if call.tool.as_ref() == "request_browser_access" { "YOUR BROWSER" } else { "YOUR COMPUTER" };
                        let open = self
                            .button(SharedString::from(format!("{key}-permissions")), "open Permissions", true)
                            .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.go(Page::Permissions, cx)));
                        (
                            format!("THE ORCHESTRATOR ASKS FOR {what} ({})", crate::swarm::target::this_computer()),
                            div().flex().gap_1().child(open).child(button("dismiss", false, refuse("The person dismissed the request. Device rules are changed on the Permissions page, not in a conversation."))),
                            Some("Access is not granted per conversation. This device's rules (files and terminal, browser) are changed on the Permissions page; Swarm applies them to every conversation."),
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
