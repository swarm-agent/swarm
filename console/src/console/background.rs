//! Background work inside a session: the task bar above the composer.
//!
//! An Orchestrator session hands work out as tasks. They stay out of the
//! conversation; the bar says what runs and what waits on you, and opens into
//! the full list with the background sessions behind each task.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, PaneId, alpha};
use crate::data::{RunState, Task, TaskStatus};
use crate::icons::Icon;

impl Console {
    fn toggle_expanded(&mut self, si: usize, ti: usize) {
        if !self.expanded.remove(&(si, ti)) {
            self.expanded.insert((si, ti));
        }
    }

    /// "report · 2 sessions ▸": opens what a task delivered and the
    /// background sessions behind it.
    pub(super) fn runs_toggle(&self, si: usize, ti: usize, task: &Task, key: &str, cx: &mut Context<Self>) -> Option<AnyElement> {
        if task.runs.is_empty() && task.report.is_empty() {
            return None;
        }
        let t = &self.theme;
        let open = self.expanded.contains(&(si, ti));
        let hover = t.hover;
        let n = task.runs.len();
        Some(
            div()
                .id(SharedString::from(format!("{key}-runs")))
                .flex()
                .items_center()
                .gap_1()
                .px_1()
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .text_size(px(10.5))
                .text_color(t.muted)
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.toggle_expanded(si, ti);
                    cx.notify();
                }))
                .child(self.icon(if open { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
                .child(match (task.report.is_empty(), n) {
                    (false, 0) => "report".to_string(),
                    (false, n) => format!("report · {n} session{}", if n == 1 { "" } else { "s" }),
                    (true, n) => format!("{n} session{}", if n == 1 { "" } else { "s" }),
                })
                .into_any_element(),
        )
    }

    /// What a task delivered, in the agent's words, then the background
    /// sessions behind it, newest first.
    pub(super) fn render_runs(&self, si: usize, ti: usize, task: &Task) -> Option<Div> {
        if !self.expanded.contains(&(si, ti)) || (task.runs.is_empty() && task.report.is_empty()) {
            return None;
        }
        let t = &self.theme;
        Some(
            div()
                .ml(px(18.))
                .mt_1()
                .pl_2()
                .border_l_1()
                .border_color(t.border_strong)
                .flex()
                .flex_col()
                .gap(px(3.))
                .when(!task.report.is_empty(), |d| {
                    d.child(div().pb_1().text_size(px(11.5)).line_height(px(17.)).text_color(t.muted).child(task.report.clone()))
                })
                .children(task.runs.iter().map(|r| {
                    let (glyph, color) = self.state_glyph(r.state);
                    let state = match r.state {
                        RunState::Running => "running",
                        RunState::NeedsYou => "asks you",
                        RunState::Idle => "finished",
                        RunState::Failed => "failed",
                    };
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .text_size(px(11.))
                        .child(div().text_color(color).child(glyph))
                        .child(div().w(px(64.)).flex_none().text_color(t.muted).child(r.agent.clone()))
                        .child(div().flex_1().min_w_0().truncate().text_color(t.faint).child(r.label.clone()))
                        .child(div().text_color(t.faint).child(self.machines[r.machine].name.clone()))
                        .child(div().w(px(56.)).flex_none().text_right().text_color(color).child(state))
                        .child(div().w(px(36.)).flex_none().text_right().text_color(t.faint).child(r.age.clone()))
                        .child(div().w(px(40.)).flex_none().text_right().text_color(t.faint).child(format!("${:.2}", r.cost)))
                }))
                .when(!task.runs.is_empty(), |d| {
                    d.child(div().pt(px(2.)).text_size(px(10.5)).text_color(t.faint).child("open a session to read its transcript"))
                }),
        )
    }

    /// One task as a row: state, title, who does it, the decision it waits
    /// on; open, also its latest activity and background sessions.
    fn task_row(&self, si: usize, ti: usize, key: String, detail: bool, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let task = &self.sessions[si].tasks[ti];
        let (glyph, color) = self.task_glyph(task.status);
        let live = task.status == TaskStatus::Running;
        let many_machines = self.machines.len() > 1;
        div()
            .flex()
            .flex_col()
            .gap(px(2.))
            .py(px(3.))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(color).child(glyph))
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(task.title.clone()))
                    .child(div().flex_none().text_size(px(10.5)).text_color(t.faint).child(if many_machines {
                        format!("{} · {}", task.role, self.machines[task.machine].name)
                    } else {
                        task.role.to_string()
                    }))
                    .child(div().flex_none().text_size(px(11.)).text_color(color).child(task.status.label()))
                    .child(self.task_actions(si, ti, task.status, &key, cx)),
            )
            .when(detail && (!task.activity.is_empty() || !task.runs.is_empty() || !task.report.is_empty()), |d| {
                d.child(
                    div()
                        .pl(px(18.))
                        .flex()
                        .items_center()
                        .gap_2()
                        .text_size(px(11.))
                        .child(div().text_color(if live { alpha(t.ok, 0.9) } else { t.faint }).child(if live { "›" } else { " " }))
                        .child(div().flex_1().min_w_0().truncate().text_color(t.faint).child(task.activity.clone()))
                        .children(self.runs_toggle(si, ti, task, &key, cx)),
                )
            })
            .when(detail, |d| d.children(self.render_runs(si, ti, task)))
    }

    /// Opens a tab of the bar, or closes the bar when that tab is open.
    fn toggle_tab(&mut self, pid: usize, tab: BarTab) {
        if let Some(p) = self.panes.iter_mut().find(|p| p.id == PaneId(pid)) {
            if p.tray && p.tab == tab {
                p.tray = false;
            } else {
                p.tray = true;
                p.tab = tab;
            }
        }
    }

    /// The bar above the composer: a line with the session's tasks and media
    /// that opens into either. Tasks waiting on a decision show even when it
    /// is closed, so approving never needs a click to find.
    pub(super) fn render_background(&self, si: usize, pane: PaneId, open: bool, tab: BarTab, cx: &mut Context<Self>) -> Option<Div> {
        let t = &self.theme;
        let session = &self.sessions[si];
        let media: Vec<usize> = (0..self.media.len()).filter(|i| self.media[*i].session == Some(si)).collect();
        if session.tasks.is_empty() && media.is_empty() {
            return None;
        }
        let count = |f: &dyn Fn(TaskStatus) -> bool| session.tasks.iter().filter(|x| f(x.status)).count();
        let running = count(&|s| s == TaskStatus::Running);
        let needs = count(&|s| s.needs_you());
        let queued = count(&|s| s == TaskStatus::Queued);
        let done = count(&|s| s == TaskStatus::Completed);
        let failed = count(&|s| matches!(s, TaskStatus::Failed));
        let total = session.tasks.len();
        let hover = t.hover;
        let pid = pane.0;
        let tasks_open = open && tab == BarTab::Tasks;
        let media_open = open && tab == BarTab::Media;

        let part = |show: bool, glyph: &'static str, color, label: String| {
            show.then(|| div().flex().gap_1().child(div().text_color(color).child(glyph)).child(label))
        };
        let tab_button = |which: BarTab, on: bool| {
            div()
                .id(SharedString::from(format!("bar-{pid}-{which:?}")))
                .flex()
                .items_center()
                .gap_2()
                .px_2()
                .py(px(5.))
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .when(on, |d| d.bg(t.raised))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.toggle_tab(pid, which);
                    cx.notify();
                }))
                .child(self.icon(if on { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
        };

        let mut strip = div().flex().items_center().gap_1().px_1().text_size(px(11.)).text_color(t.muted);
        if total > 0 {
            strip = strip.child(
                tab_button(BarTab::Tasks, tasks_open)
                    .gap_3()
                    .child(self.icon(Icon::Tasks, 12., if running > 0 { t.accent } else { t.faint }))
                    .child(div().text_color(t.text).child(format!("{total} task{}", if total == 1 { "" } else { "s" })))
                    .children(part(running > 0, "●", t.ok, format!("{running} running")))
                    .children(part(needs > 0, "◆", t.warn, format!("{needs} need you")))
                    .children(part(queued > 0, "○", t.faint, format!("{queued} queued")))
                    .children(part(done > 0, "✓", t.muted, format!("{done} done")))
                    .children(part(failed > 0, "✕", t.err, format!("{failed} failed"))),
            );
        }
        if !media.is_empty() {
            let making = media.iter().filter(|i| self.media[**i].pending).count();
            strip = strip.child(
                tab_button(BarTab::Media, media_open)
                    .child(self.icon(Icon::Media, 12., if making > 0 { t.accent } else { t.faint }))
                    .child(div().text_color(t.text).child(format!("media {}", media.len())))
                    .children(part(making > 0, "●", t.ok, format!("{making} making"))),
            );
        }

        // Tasks: closed, only what waits on you; open, every task, grouped by
        // machine when the session's tasks run on more than one.
        let shown: Vec<usize> = if media_open { vec![] } else { (0..total).filter(|ti| tasks_open || session.tasks[*ti].status.needs_you()).collect() };
        let mut machines: Vec<usize> = shown.iter().map(|ti| session.tasks[*ti].machine).collect();
        machines.sort_unstable();
        machines.dedup();
        let mut rows: Vec<AnyElement> = Vec::new();
        for mi in &machines {
            if tasks_open && machines.len() > 1 {
                let m = &self.machines[*mi];
                rows.push(
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .pt_2()
                        .text_size(px(10.5))
                        .text_color(t.faint)
                        .child(self.icon(Icon::Machine, 11., self.health_color(m.health)))
                        .child(m.name.to_uppercase())
                        .into_any_element(),
                );
            }
            for ti in shown.iter().filter(|ti| session.tasks[**ti].machine == *mi) {
                rows.push(self.task_row(si, *ti, format!("task-{pid}-{ti}"), tasks_open, cx).into_any_element());
            }
        }
        if media_open {
            rows.push(
                div()
                    .flex()
                    .flex_wrap()
                    .gap_2()
                    .py_2()
                    .children(media.iter().rev().map(|i| self.media_thumb(*i, 120., 84., format!("bar-{pid}"), cx)))
                    .into_any_element(),
            );
        }
        let body = (!rows.is_empty()).then(|| {
            div()
                .id(SharedString::from(format!("bg-tray-{pid}")))
                .max_h(px(260.))
                .overflow_y_scroll()
                .px_3()
                .pb_1()
                .children(rows)
        });

        Some(
            div()
                .flex_none()
                .border_t_1()
                .border_color(if needs > 0 { alpha(t.warn, 0.45) } else { t.border })
                .bg(t.panel)
                .child(strip)
                .children(body),
        )
    }
}

/// Which part of the bar above the composer is open.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(super) enum BarTab {
    Tasks,
    Media,
}
