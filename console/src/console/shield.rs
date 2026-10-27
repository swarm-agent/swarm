//! The shield: what you let a session's Orchestrator do. Tasks start freely
//! or wait for your Approve; its access to your own machine is none, ask or
//! full. Swarm stores the choice on the session and tells the Orchestrator
//! every turn; a session not started yet takes it when it starts.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::data::{Access, MachineAccess};
use crate::data::BrowserAccess;
use crate::icons::Icon;
use crate::swarm::live::Command;

impl Console {
    /// Applies a shield choice: to Swarm for a live session (the session shows
    /// Swarm's answer), or to a session that has not started yet.
    fn set_access(&mut self, si: usize, access: Access, cx: &mut Context<Self>) {
        let session = &mut self.sessions[si];
        match (&session.live, &self.live) {
            (Some(l), Some(live)) => {
                self.acting = Some(si);
                self.failure = None;
                live.run(Command::SetAccess { session_id: l.session_id.to_string(), access });
                self.notice = Some(("Saving the shield…".into(), false));
            }
            (None, _) => session.access = Some(access),
            (Some(_), None) => {}
        }
        cx.notify();
    }

    /// The shield in the composer's footer: its current choice, and the menu.
    pub(super) fn shield_button(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let s = &self.sessions[si];
        let (label, color) = match s.access {
            None => ("Swarm default".to_string(), t.faint),
            Some(a) => (
                format!(
                    "tasks {} · {} {}",
                    if a.tasks_free { "free" } else { "ask" },
                    crate::swarm::target::this_computer(),
                    match a.machine {
                        MachineAccess::None => "off",
                        MachineAccess::Ask => "ask",
                        MachineAccess::Full => "full",
                    }
                ),
                if a.machine == MachineAccess::Full { t.warn } else { t.muted },
            ),
        };
        let lease = self.lease_of(si);
        let (label, color) = match &lease {
            Some(l) => {
                let left = l.until.saturating_sub(super::now_ms());
                (format!("{label} · browser {} {}", l.level.as_str(), super::ago(left)), if l.level == BrowserAccess::Full { t.warn } else { color })
            }
            None => (label, color),
        };
        let hover = t.hover;
        let open = self.shield_open == Some(pid);
        div()
            .id(SharedString::from(format!("shield-{pid}")))
            .flex()
            .flex_none()
            .items_center()
            .gap_1()
            .px_1()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .when(open, |d| d.bg(hover))
            .text_color(color)
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.shield_open = if this.shield_open == Some(pid) { None } else { Some(pid) };
                cx.notify();
            }))
            .child(self.icon(Icon::Shield, 11., color))
            .child(label)
            .into_any_element()
    }

    /// The shield's menu, above the composer while it is open.
    pub(super) fn render_shield_menu(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<Div> {
        if self.shield_open != Some(pid) {
            return None;
        }
        let t = &self.theme;
        // A session that never chose shows nothing chosen; a choice starts from the defaults.
        let chosen = self.sessions[si].access;
        let current = chosen.unwrap_or(Access::DEFAULT);
        let hover = t.hover;
        let option = |key: &'static str, title: &'static str, detail: &'static str, on: bool, next: Option<Access>| {
            let enabled = next.is_some();
            div()
                .id(SharedString::from(format!("shield-{pid}-{key}")))
                .flex()
                .items_center()
                .gap_2()
                .px_2()
                .py(px(3.))
                .rounded(px(t.radius))
                .when(enabled, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
                .when(!enabled, |d| d.opacity(0.5))
                .when_some(next, |d, next| {
                    d.on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.set_access(si, next, cx)))
                })
                .child(div().w(px(12.)).flex_none().text_color(if on { t.accent } else { t.faint }).child(if on { "●" } else { "○" }))
                .child(div().w(px(170.)).flex_none().whitespace_nowrap().text_color(if on { t.text } else { t.muted }).child(title))
                .child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(t.faint).child(detail))
        };
        let lease = self.lease_of(si);
        let session_id = self.sessions[si].live.as_ref().map(|l| l.session_id.to_string());
        let browser_row: gpui::AnyElement = match (&lease, session_id) {
            (Some(l), Some(session_id)) => {
                let left = l.until.saturating_sub(super::now_ms());
                div()
                    .id(SharedString::from(format!("shield-{pid}-browser-end")))
                    .flex()
                    .items_center()
                    .gap_2()
                    .px_2()
                    .py(px(3.))
                    .rounded(px(t.radius))
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.end_browser(session_id.clone(), cx)))
                    .child(div().w(px(12.)).flex_none().text_color(if l.level == BrowserAccess::Full { t.warn } else { t.accent }).child("●"))
                    .child(div().w(px(170.)).flex_none().whitespace_nowrap().text_color(t.text).child("End the lease"))
                    .child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(t.faint).child(lease_detail(l, left, crate::browser::tab_of(&self.sessions[si].live.as_ref().map(|x| x.session_id.to_string()).unwrap_or_default()))))
                    .into_any_element()
            }
            _ => div()
                .flex()
                .items_center()
                .gap_2()
                .px_2()
                .py(px(3.))
                .opacity(0.6)
                .child(div().w(px(12.)).flex_none().text_color(t.faint).child("○"))
                .child(div().w(px(170.)).flex_none().whitespace_nowrap().text_color(t.muted).child("No browser lease"))
                .child(div().flex_1().min_w_0().truncate().text_size(px(11.)).text_color(t.faint).child("it asks when it needs your Chrome"))
                .into_any_element(),
        };
        let tasks = |free: bool| Some(Access { tasks_free: free, ..current });
        let heading = |text: &'static str| div().px_2().pt_1().text_size(px(10.5)).text_color(t.faint).child(text);
        Some(
            div()
                .flex()
                .flex_col()
                .gap(px(1.))
                .p_1()
                .mb_1()
                .rounded(px(t.radius))
                .border_1()
                .border_color(alpha(t.accent, 0.4))
                .bg(t.panel)
                .child(heading("TASKS"))
                .child(option("free", "Start tasks freely", "it delegates; nothing to approve, you merge the results", chosen.is_some_and(|a| a.tasks_free), tasks(true)))
                .child(option("ask", "Ask before each task", "task cards wait for your Approve", chosen.is_some_and(|a| !a.tasks_free), tasks(false)))
                .child(div().px_2().pt_1().text_size(px(10.5)).text_color(t.faint).child(format!("YOUR COMPUTER · {}", crate::swarm::target::this_computer())))
                .child(option("full", "Full access", "its commands run as you, without asking", chosen.is_some_and(|a| a.machine == MachineAccess::Full), Some(Access { machine: MachineAccess::Full, ..current })))
                .child(option("pc-ask", "Access, but ask", "you run or deny each command", chosen.is_some_and(|a| a.machine == MachineAccess::Ask), Some(Access { machine: MachineAccess::Ask, ..current })))
                .child(option(
                    "none",
                    "No access",
                    "it asks first when it needs your computer",
                    chosen.is_some_and(|a| a.machine == MachineAccess::None),
                    Some(Access { machine: MachineAccess::None, ..current }),
                ))
                .child(heading("YOUR BROWSER"))
                .child(browser_row),
        )
    }

    /// Ends a session's browser lease in Swarm now (the extension is told).
    pub(super) fn end_browser(&mut self, session_id: String, cx: &mut Context<Self>) {
        if let Some(live) = &self.live {
            live.run(Command::EndBrowser { session_id });
            self.notice = Some(("Ending the browser lease…".into(), false));
        }
        cx.notify();
    }
}

/// A lease in a line: level, time left, sites (or any site) and the tab it
/// drives in Chrome.
pub(super) fn lease_detail(l: &crate::data::BrowserLease, left: u64, tab: Option<(String, String)>) -> String {
    let sites = if l.sites.iter().any(|s| s == "*") { "any site".to_string() } else { l.sites.join(", ") };
    let mut line = format!("{} · {} left · {sites}", l.level.as_str(), super::ago(left));
    if let Some((title, url)) = tab {
        line.push_str(&format!(" · tab: {}", if title.is_empty() { url } else { title }));
    }
    line
}
