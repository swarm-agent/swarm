//! Background work inside a session: the task bar above the composer.
//!
//! An Orchestrator session hands work out as tasks. They stay out of the
//! conversation; the bar says what runs and what waits on you, and opens into
//! the full list with the background sessions behind each task.

use gpui::{AnyElement, ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, PaneId, alpha};
use crate::data::{ProgressState, RunState, Task, TaskState, TaskStatus};

/// Open tasks the tray lists before "+ N more".
const TRAY_ROWS: usize = 5;
use crate::icons::Icon;

impl Console {
    fn toggle_expanded(&mut self, si: usize, ti: usize) {
        if !self.expanded.remove(&(si, ti)) {
            self.expanded.insert((si, ti));
        }
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
                        RunState::Waiting => "waiting",
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

    /// The color of a task's state as Swarm derives it; a Swarm that derives
    /// none falls back to its tracked progress, then its run status.
    fn progress_color(&self, task: &Task) -> gpui::Hsla {
        let t = &self.theme;
        match task.progress.task_state {
            TaskState::NeedsYou | TaskState::NotIntegrated => return t.warn,
            TaskState::Running => return t.ok,
            TaskState::Waiting | TaskState::Stopped => return t.faint,
            TaskState::Landed | TaskState::Done => return t.muted,
            TaskState::Unknown => {}
        }
        match task.progress.state {
            ProgressState::Failed => t.err,
            ProgressState::NotIntegrated => t.warn,
            ProgressState::Workflow => t.accent,
            ProgressState::Working => t.ok,
            ProgressState::Waiting => t.faint,
            ProgressState::Integrated | ProgressState::Delivered | ProgressState::Done => t.muted,
            ProgressState::Unknown => self.task_glyph(task.status).1,
        }
    }

    /// What the row says about the task: the state Swarm derives and its
    /// reason ("landed · pushed 5711ae410abc"); from a Swarm that derives
    /// none, its summary, else its tracked state, else its run status.
    fn progress_line(task: &Task) -> String {
        let state = task.progress.task_state.label();
        if !state.is_empty() {
            let reason: &str = task.progress.task_state_reason.as_ref();
            let reason = reason.trim();
            return if reason.is_empty() { state.to_string() } else { format!("{state} · {reason}") };
        }
        if !task.progress.summary.is_empty() {
            task.progress.summary.to_string()
        } else if !task.progress.state.label().is_empty() {
            task.progress.state.label().to_string()
        } else {
            task.status.label().to_string()
        }
    }

    /// One task as a row: its title, who does it, where it is now; opened,
    /// what happened to it, its latest activity and background sessions.
    fn task_row(&self, si: usize, ti: usize, key: String, detail: bool, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let task = &self.sessions[si].tasks[ti];
        let color = self.progress_color(task);
        let live = task.status == TaskStatus::Running;
        let many_machines = self.machines.len() > 1;
        let open = self.expanded.contains(&(si, ti));
        let hover = t.hover;
        let now = super::now_ms();
        div()
            .flex()
            .flex_col()
            .gap(px(2.))
            .py(px(3.))
            .child(
                div()
                    .id(SharedString::from(format!("{key}-row")))
                    .flex()
                    .items_center()
                    .gap_2()
                    .rounded(px(t.radius))
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.toggle_expanded(si, ti);
                        cx.notify();
                    }))
                    .child(self.icon(if open { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(task.title.clone()))
                    .child(div().flex_none().text_size(px(10.5)).text_color(t.faint).child(if many_machines {
                        format!("{} · {}", task.role, self.machines[task.machine].name)
                    } else {
                        task.role.to_string()
                    }))
                    .child(
                        div()
                            .flex_none()
                            .max_w(px(320.))
                            .truncate()
                            .px_2()
                            .rounded(px(t.radius))
                            .bg(alpha(color, 0.14))
                            .text_size(px(11.))
                            .text_color(color)
                            .child(Self::progress_line(task)),
                    )
                    .child(self.task_actions(si, ti, task.status, &key, cx)),
            )
            .when(open && !task.progress.history.is_empty(), |d| {
                d.child(div().ml(px(18.)).pl_2().border_l_1().border_color(t.border_strong).flex().flex_col().gap(px(2.)).children(
                    task.progress.history.iter().map(|(at, what)| {
                        div()
                            .flex()
                            .gap_2()
                            .text_size(px(11.))
                            .child(div().w(px(40.)).flex_none().text_color(t.faint).child(if *at > 0 { super::ago(now.saturating_sub(*at)) } else { "—".into() }))
                            .child(div().flex_1().min_w_0().text_color(t.muted).child(what.clone()))
                    }),
                ))
            })
            .when(detail && open && (!task.activity.is_empty() || !task.runs.is_empty() || !task.report.is_empty()), |d| {
                d.child(
                    div()
                        .pl(px(18.))
                        .flex()
                        .items_center()
                        .gap_2()
                        .text_size(px(11.))
                        .child(div().text_color(if live { alpha(t.ok, 0.9) } else { t.faint }).child(if live { "›" } else { " " }))
                        .child(div().flex_1().min_w_0().truncate().text_color(t.faint).child(task.activity.clone())),
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
        let media: Vec<usize> = (0..self.media.len()).filter(|i| self.media[*i].session == Some(si) && !self.media[*i].failed).collect();
        if session.tasks.is_empty() && media.is_empty() {
            return None;
        }
        let count = |f: &dyn Fn(ProgressState) -> bool| session.tasks.iter().filter(|x| f(x.progress.state)).count();
        let working = count(&|s| s == ProgressState::Working);
        let workflow = count(&|s| s == ProgressState::Workflow);
        let not_integrated = count(&|s| s == ProgressState::NotIntegrated);
        let failed = count(&|s| s == ProgressState::Failed);
        let waiting = count(&|s| s == ProgressState::Waiting);
        let finished = count(&|s| s.finished());
        let needs = session.tasks.iter().filter(|x| x.status.needs_you()).count();
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
                    .child(self.icon(Icon::Tasks, 12., if working + workflow > 0 { t.accent } else { t.faint }))
                    .child(div().text_color(t.text).child(format!("{total} task{}", if total == 1 { "" } else { "s" })))
                    .children(part(failed > 0, "✕", t.err, format!("{failed} failed")))
                    .children(part(not_integrated > 0, "◆", t.warn, format!("{not_integrated} not integrated")))
                    .children(part(workflow > 0, "●", t.accent, format!("{workflow} in workflow")))
                    .children(part(working > 0, "●", t.ok, format!("{working} working")))
                    .children(part(waiting > 0, "○", t.faint, format!("{waiting} waiting")))
                    .children(part(finished > 0, "✓", t.muted, format!("{finished} done"))),
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

        // Tasks: closed, only a task held for approval (Start tasks: approve);
        // open, the open tasks (what went wrong first, then most recently
        // changed), a handful unless asked for all, and the finished ones
        // folded into one line.
        let mut rows: Vec<AnyElement> = Vec::new();
        if !media_open {
            let mut open_tasks: Vec<usize> = (0..total)
                .filter(|ti| {
                    let task = &session.tasks[*ti];
                    if tasks_open { !task.progress.state.finished() } else { task.status.needs_you() }
                })
                .collect();
            open_tasks.sort_by_key(|ti| {
                let p = &session.tasks[*ti].progress;
                (p.state.rank(), std::cmp::Reverse(p.updated_at))
            });
            let all = self.tray_all.contains(&si);
            let more = if tasks_open && !all { open_tasks.len().saturating_sub(TRAY_ROWS) } else { 0 };
            for ti in open_tasks.iter().take(open_tasks.len() - more) {
                rows.push(self.task_row(si, *ti, format!("task-{pid}-{ti}"), tasks_open, cx).into_any_element());
            }
            if tasks_open && (more > 0 || (all && open_tasks.len() > TRAY_ROWS)) {
                rows.push(
                    div()
                        .id(SharedString::from(format!("tray-more-{pid}")))
                        .py(px(3.))
                        .pl(px(18.))
                        .text_size(px(11.))
                        .text_color(t.muted)
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            if !this.tray_all.remove(&si) {
                                this.tray_all.insert(si);
                            }
                            cx.notify();
                        }))
                        .child(if more > 0 { format!("+ {more} more") } else { "show fewer".into() })
                        .into_any_element(),
                );
            }
            if tasks_open && finished > 0 {
                let unfolded = self.tray_done.contains(&si);
                let of = |s: ProgressState| session.tasks.iter().filter(|x| x.progress.state == s).count();
                let parts: Vec<String> = [(ProgressState::Integrated, "integrated"), (ProgressState::Delivered, "delivered"), (ProgressState::Done, "done")]
                    .into_iter()
                    .filter_map(|(s, word)| (of(s) > 0).then(|| format!("{} {word}", of(s))))
                    .collect();
                rows.push(
                    div()
                        .id(SharedString::from(format!("tray-done-{pid}")))
                        .flex()
                        .items_center()
                        .gap_2()
                        .mt_1()
                        .py(px(3.))
                        .border_t_1()
                        .border_color(t.border)
                        .text_size(px(11.))
                        .text_color(t.muted)
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            if !this.tray_done.remove(&si) {
                                this.tray_done.insert(si);
                            }
                            cx.notify();
                        }))
                        .child(self.icon(if unfolded { Icon::ChevronDown } else { Icon::ChevronRight }, 10., t.faint))
                        .child(format!("{finished} done · {}", parts.join(" · ")))
                        .into_any_element(),
                );
                if unfolded {
                    let mut done: Vec<usize> = (0..total).filter(|ti| session.tasks[*ti].progress.state.finished()).collect();
                    done.sort_by_key(|ti| std::cmp::Reverse(session.tasks[*ti].progress.updated_at));
                    for ti in done {
                        rows.push(self.task_row(si, ti, format!("task-{pid}-{ti}"), true, cx).into_any_element());
                    }
                }
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
