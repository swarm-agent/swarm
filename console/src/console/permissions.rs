//! The Permissions page: the account's rules as Swarm stores and enforces
//! them, grouped the way control flows (this device, other devices, home,
//! linked machines). Every value shown is one Swarm returned; a click shows
//! "saving…" on its choice until Swarm answers, then the page reads again.

use gpui::{ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha, now_ms};
use crate::swarm::live::Command;
use crate::swarm::permissions::{self, Row, Rule};

impl Console {
    /// Reads the rules from Swarm (on opening the page, after a change, and
    /// when Swarm says they changed).
    pub(super) fn load_permissions(&mut self) {
        if self.via.is_some() {
            return;
        }
        let Some(live) = &self.live else { return };
        self.permissions_busy = true;
        live.run(Command::ReadPermissions);
    }

    /// Asks Swarm to set one rule, at the revision the page shows.
    fn set_permission(&mut self, rule: Rule, value: &'static str, cx: &mut Context<Self>) {
        if self.permission_saving.is_some() {
            return;
        }
        let Some(revision) = self.permissions.as_ref().and_then(|v| v.get("revision")).and_then(|r| r.as_u64()) else { return };
        let Some(live) = &self.live else { return };
        self.permissions_error = None;
        self.permission_saving = Some((rule.clone(), value.to_string()));
        live.run(Command::SetPermission { rule, value: value.to_string(), revision });
        cx.notify();
    }

    /// A read or a change came back. A refused change shows Swarm's reason
    /// and reads the stored record again.
    pub(super) fn permissions_done(&mut self, what: &str, result: Result<serde_json::Value, String>) {
        let saved = what == "permission";
        if saved {
            self.permission_saving = None;
        } else {
            self.permissions_busy = false;
        }
        match result {
            Ok(record) => {
                self.permissions = Some(record);
                // A refused change keeps its reason over the read that follows it.
                if saved || !self.permissions_error.as_ref().is_some_and(|(from_save, _)| *from_save) {
                    self.permissions_error = None;
                }
                if saved {
                    self.notice = Some(("Swarm saved the rule.".into(), false));
                }
            }
            Err(e) => {
                self.permissions_error = Some(if saved { (true, format!("Swarm did not change the rule: {e}")) } else { (false, format!("Could not read the permissions: {e}")) });
                if saved {
                    self.load_permissions();
                }
            }
        }
    }

    pub(super) fn render_permissions(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let page = div()
            .id("permissions-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_5()
            .max_w(px(760.))
            .child(
                div()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .child(div().text_size(px(16.)).text_color(t.text).child("Permissions"))
                    .child(div().text_size(px(11.)).text_color(t.faint).child(
                        "What agents may use, for every conversation, task and AI key on this Swarm. Swarm stores these rules and enforces them; this page shows what it returns.",
                    )),
            )
            .children(self.permissions_error.clone().map(|(_, e)| div().text_size(px(11.5)).text_color(t.err).child(e)));
        // A linked machine's own rules never cross the link: they are set on
        // that machine. Its work rule from home is on home's page.
        if let Some(name) = &self.via {
            return page.child(div().text_size(px(12.)).text_color(t.muted).child(format!(
                "{name}'s own rules are set on {name} itself; they never cross the link. What your home may do on {name} is its \"Hand it work\" rule on your home's Permissions page."
            )));
        }
        let board = match self.permissions.as_ref().map(|v| permissions::board(v, now_ms())) {
            None if self.live.is_none() => return page.child(div().text_color(t.faint).child("Not connected to Swarm.")),
            None => return page.child(div().text_color(t.faint).child(if self.permissions_busy { "Reading the rules from Swarm…" } else { "Not read yet." })),
            Some(Err(e)) => return page.child(div().text_color(t.err).child(e)),
            Some(Ok(board)) => board,
        };
        let groups: Vec<Div> = board
            .groups
            .into_iter()
            .map(|g| {
                let rows: Vec<Div> = if g.rows.is_empty() {
                    vec![div().px_3().py_2().text_size(px(11.)).text_color(t.faint).child(g.empty)]
                } else {
                    g.rows.into_iter().map(|row| self.permission_row(row, cx)).collect()
                };
                self.settings_group(g.title, g.note, rows)
            })
            .collect();
        page.children(groups).children(self.devices_section(cx))
    }

    /// One rule: its title and choices, what it protects, right now, and
    /// who changed it.
    fn permission_row(&self, row: Row, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let hover = t.hover;
        let saving = self.permission_saving.as_ref();
        let busy = saving.is_some();
        let id = match &row.rule {
            Rule::Tasks => "tasks".to_string(),
            Rule::Files(d) => format!("files-{d}"),
            Rule::Browser(d) => format!("browser-{d}"),
            Rule::Work(m) => format!("work-{m}"),
        };
        let choices = div().flex().flex_none().gap_1().children(row.choices.iter().map(|&(value, label)| {
            let on = row.value == value;
            let pending = saving.is_some_and(|(r, v)| *r == row.rule && v == value);
            let enabled = !row.read_only && !busy && !on;
            let rule = row.rule.clone();
            div()
                .id(SharedString::from(format!("permission-{id}-{value}")))
                .px_2()
                .py(px(2.))
                .flex()
                .justify_center()
                .rounded(px(t.radius))
                .border_1()
                .border_color(if on { alpha(t.accent, 0.7) } else { t.border })
                .when(on, |d| d.bg(alpha(t.accent, 0.15)))
                .when(enabled, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
                .when(row.read_only, |d| d.opacity(0.6))
                .text_size(px(11.))
                .text_color(if on || pending { t.text } else { t.muted })
                .when(enabled, |d| d.on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.set_permission(rule.clone(), value, cx))))
                .child(if pending { "saving…" } else { label })
        }));
        div()
            .w_full()
            .flex()
            .flex_col()
            .gap(px(3.))
            .px_3()
            .py_2()
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(row.title))
                    .child(choices),
            )
            .child(div().text_size(px(11.)).text_color(t.faint).child(row.protects))
            .child(div().text_size(px(11.5)).text_color(if row.unavailable { t.warn } else { t.muted }).child(row.now))
            .child(div().text_size(px(10.5)).text_color(t.faint).child(row.changed))
            .when(row.read_only, |d| d.child(div().text_size(px(10.5)).text_color(t.faint).child("change these on that device")))
    }
}
