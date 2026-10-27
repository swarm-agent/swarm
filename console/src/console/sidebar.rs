//! The sidebar's sessions: a row each, with the actions that show on hover
//! (pick, pin, rename, archive) and a bar for the picked ones. Pins and
//! names are Swarm's records (metadata and title); archiving is Swarm's.

use gpui::{ClickEvent, Context, Div, Focusable, SharedString, Stateful, div, prelude::*, px};

use super::{Console, Link, Open, Page, RenameCancel, RenameSave, ago, alpha, now_ms, panes};
use crate::data::TaskStatus;
use crate::swarm::live::Command;

impl Console {
    /// The project's sessions as rows, pinned first.
    pub(super) fn session_rows(&self, active_session: usize, cx: &mut Context<Self>) -> Vec<Stateful<Div>> {
        let t = &self.theme;
        let hover = t.hover;
        let badge = |n: usize| {
            div()
                .px(px(5.))
                .rounded(px(t.radius))
                .bg(alpha(t.warn, 0.16))
                .text_color(t.warn)
                .text_size(px(10.5))
                .child(n.to_string())
        };
        let sessions: Vec<_> = self
            .ordered(self.project)
            .into_iter()
            .map(|i| (i, &self.sessions[i]))
            .map(|(i, s)| {
                let (glyph, color) = self.state_glyph(s.state);
                let needs = s.tasks.iter().filter(|x| x.status.needs_you()).count()
                    + usize::from(s.status.is_some_and(|x| x.needs_you()))
                    + s.calls.iter().filter(|c| c.tool.starts_with("request_")).count();
                let running = s.tasks.iter().filter(|x| x.status == TaskStatus::Running).count();
                let selected = i == active_session;
                let open = self.is_open(i);
                let machine = self.machines[s.machine].name.clone();
                let lead = match (&s.live, s.status, s.age.is_empty()) {
                    (None, _, _) if self.link != Link::Demo => "not started".to_string(),
                    (None, _, true) => machine.to_string(),
                    (None, _, false) => format!("{machine} · {}", s.age),
                    (Some(_), Some(status), _) => format!("{machine} · {}", status.label()),
                    (Some(_), None, true) => match s.run.filter(|r| r.last_message_at > 0) {
                        Some(r) => format!("{machine} · {}", ago(now_ms().saturating_sub(r.last_message_at))),
                        None => machine.to_string(),
                    },
                    (Some(_), None, false) => format!("{machine} · {}", s.age),
                };
                let detail = match (s.tasks.len(), running) {
                    (0, _) => lead,
                    (n, 0) => format!("{lead} · {n} tasks"),
                    (n, r) => format!("{lead} · {r}/{n} running"),
                };
                let sid = s.live.as_ref().map(|l| l.session_id.to_string());
                let picked = sid.as_ref().is_some_and(|id| self.picked.contains(id));
                let picking = !self.picked.is_empty();
                let renaming = self.renaming == Some(i);
                let group: SharedString = format!("session-row-{i}").into();
                // The row's second line is its detail; while the row is
                // hovered (always while picking or renaming) the actions take
                // its place, in words: pick, pin, rename, archive. Same
                // height, so nothing moves. A press on one neither opens the
                // session nor starts a drag.
                let always = picking || renaming;
                let action = |label: &'static str, on: bool| {
                    div()
                        .id(SharedString::from(format!("session-{i}-{label}")))
                        .px(px(5.))
                        .py(px(1.))
                        .rounded(px(3.))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .text_color(if on { t.accent } else { t.muted })
                        .on_mouse_down(gpui::MouseButton::Left, |_, _, cx| cx.stop_propagation())
                        .child(label)
                };
                let actions = sid.clone().map(|id| {
                    let (pick_id, pin_id, arc_id) = (id.clone(), id.clone(), id);
                    let pinned = s.pinned;
                    div()
                        .absolute()
                        .left(px(14.))
                        .top(px(0.))
                        .flex()
                        .items_center()
                        .gap(px(2.))
                        .text_size(px(11.))
                        .opacity(if always { 1. } else { 0. })
                        .group_hover(group.clone(), |s| s.opacity(1.))
                        .child(action(if picked { "picked" } else { "pick" }, picked).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            cx.stop_propagation();
                            this.toggle_pick(&pick_id, cx);
                        })))
                        .child(action(if pinned { "unpin" } else { "pin" }, pinned).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            cx.stop_propagation();
                            this.pin_session(pin_id.clone(), !pinned, cx);
                        })))
                        .child(action("rename", false).on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                            cx.stop_propagation();
                            this.start_rename(i, window, cx);
                        })))
                        .child(action("archive", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            cx.stop_propagation();
                            this.archive_sessions(vec![arc_id.clone()], cx);
                        })))
                });
                let has_actions = actions.is_some();
                let title: gpui::AnyElement = if renaming {
                    div()
                        .key_context("Rename")
                        .on_action(cx.listener(|this, _: &RenameSave, _, cx| this.finish_rename(cx)))
                        .on_action(cx.listener(|this, _: &RenameCancel, _, cx| this.cancel_rename(cx)))
                        .flex_1()
                        .min_w_0()
                        .px_1()
                        .rounded(px(3.))
                        .border_1()
                        .border_color(alpha(t.accent, 0.6))
                        .bg(t.surface)
                        .text_color(t.text)
                        .children(self.rename_input.clone())
                        .into_any_element()
                } else {
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .text_color(if selected { t.text } else { t.muted })
                        .child(s.title.clone())
                        .into_any_element()
                };
                div()
                    .id(SharedString::from(format!("session-{i}")))
                    .group(group.clone())
                    .when(picked, |d| d.bg(alpha(t.accent, 0.1)))
                    .mx_1()
                    .px_2()
                    .py(px(5.))
                    .rounded(px(t.radius))
                    .cursor_pointer()
                    .border_l_2()
                    .border_color(if open { t.accent } else { alpha(t.accent, 0.0) })
                    .when(selected, |d| d.bg(t.raised))
                    .hover(move |s| s.bg(hover))
                    .flex()
                    .flex_col()
                    .gap(px(2.))
                    .on_click(cx.listener(move |this, ev: &ClickEvent, _, cx| {
                        let m = ev.modifiers();
                        let how = if m.shift {
                            Open::Right
                        } else if m.alt {
                            Open::Down
                        } else {
                            Open::Here
                        };
                        this.open(i, how);
                        this.page = Page::Sessions;
                        cx.notify();
                    }))
                    .child(
                        // The title line is the drag handle (to a pane).
                        div()
                            .id(SharedString::from(format!("session-{i}-title")))
                            .on_drag(panes::Dragged { session: i, pane: None }, {
                                let ghost = self.ghost(s.title.clone());
                                move |_, _, _, cx| cx.new(|_| ghost.clone())
                            })
                            .flex()
                            .items_center()
                            .gap_2()
                            .child(div().text_color(color).child(glyph))
                            .when(s.pinned, |d| d.child(div().text_size(px(10.)).text_color(t.accent).child("✦")))
                            .child(title)
                            .when(needs > 0, |d| d.child(badge(needs))),
                    )
                    .child(
                        div()
                            .relative()
                            .h(px(18.))
                            .child(
                                div()
                                    .pl(px(18.))
                                    .text_size(px(11.))
                                    .text_color(t.faint)
                                    .truncate()
                                    .opacity(if always && has_actions { 0. } else { 1. })
                                    .when(has_actions, |d| d.group_hover(group, |s| s.opacity(0.)))
                                    .child(detail),
                            )
                            .children(actions),
                    )
            })
            .collect();
        sessions
    }

    /// The bar above the list while sessions are picked: how many, archive
    /// them, or clear the picks.
    pub(super) fn render_picked_bar(&self, cx: &mut Context<Self>) -> Option<Div> {
        if self.picked.is_empty() {
            return None;
        }
        let t = &self.theme;
        let n = self.picked.len();
        let ids: Vec<String> = self.picked.iter().cloned().collect();
        Some(
            div()
                .mx_1()
                .mb_1()
                .px_2()
                .py(px(4.))
                .rounded(px(t.radius))
                .bg(alpha(t.accent, 0.12))
                .flex()
                .items_center()
                .gap_2()
                .text_size(px(11.))
                .child(div().flex_1().text_color(t.text).child(format!("{n} picked")))
                .child(self.button("picked-archive", "archive", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.archive_sessions(ids.clone(), cx))))
                .child(self.button("picked-clear", "clear", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    this.picked.clear();
                    cx.notify();
                }))),
        )
    }

    pub(super) fn toggle_pick(&mut self, id: &str, cx: &mut Context<Self>) {
        if !self.picked.remove(id) {
            self.picked.insert(id.to_string());
        }
        cx.notify();
    }

    /// Archives sessions in Swarm; each leaves the list by Swarm's event.
    pub(super) fn archive_sessions(&mut self, ids: Vec<String>, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        let ids: Vec<String> = ids.into_iter().filter(|id| !id.is_empty()).collect();
        if ids.is_empty() {
            return;
        }
        for id in &ids {
            self.picked.remove(id);
        }
        self.notice = Some((if ids.len() == 1 { "Archiving…".into() } else { format!("Archiving {} sessions…", ids.len()).into() }, false));
        live.run(Command::Archive { session_ids: ids });
        cx.notify();
    }

    pub(super) fn pin_session(&mut self, id: String, pinned: bool, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        live.run(Command::Pin { session_id: id, pinned });
        cx.notify();
    }

    /// Opens the name field on a session's row, with its title in it.
    pub(super) fn start_rename(&mut self, i: usize, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(title) = self.sessions.get(i).map(|s| s.title.to_string()) else { return };
        self.renaming = Some(i);
        if let Some(input) = &self.rename_input {
            input.update(cx, |field, cx| field.set_text(title, cx));
            window.focus(&input.focus_handle(cx));
        }
        cx.notify();
    }

    /// Sends the typed name to Swarm, which keeps it and stops titling the
    /// session itself.
    pub(super) fn finish_rename(&mut self, cx: &mut Context<Self>) {
        if self.finish_project_rename(cx) {
            return;
        }
        let Some(i) = self.renaming.take() else { return };
        let title = self.rename_input.as_ref().map(|f| f.read(cx).text().trim().to_string()).unwrap_or_default();
        let Some(id) = self.sessions.get(i).and_then(|s| s.live.as_ref()).map(|l| l.session_id.to_string()) else { return };
        if !title.is_empty() {
            if let Some(live) = &self.live {
                live.run(Command::Rename { session_id: id, title });
            }
        }
        cx.notify();
    }

    pub(super) fn cancel_rename(&mut self, cx: &mut Context<Self>) {
        self.cancel_project_rename();
        self.renaming = None;
        cx.notify();
    }
}
