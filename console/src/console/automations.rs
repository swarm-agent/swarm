//! The Automations page: what Swarm's workers are doing. First what runs
//! now (with each run's latest action), then runs that failed, then recent
//! runs, then the workers and automations with their schedules. A run opens
//! on its own view: its status, error and deliverables, and every action
//! its agent took, live while it runs.
//!
//! Swarm's records only: workers and runs from `/v3/workers…`, automations
//! from `/v3/automations/v2`, a run's actions from its session. The page
//! reads again when Swarm says something changed (the realtime stream's
//! `worker.updated` and `automation.updated`); a run's view follows its
//! session over the same stream. Making and changing them is the
//! Orchestrator's (manage_workers, manage_automation).

use std::collections::HashSet;

use gpui::{AnyElement, ClickEvent, Context, Div, IntoElement, SharedString, div, prelude::*, px};

use super::{Console, alpha, now_ms};
use crate::data::Entry;
use crate::swarm::automations::{self, Automation, Board, Run, Worker};
use crate::swarm::workflows::{self, Workflow};

/// The run whose own view is open.
#[derive(Clone, PartialEq)]
pub(super) struct RunOpen {
    pub run_id: String,
    pub worker_id: String,
    pub session_id: String,
}

impl Console {
    /// Reads workers, their runs and automations; a read asked for while one
    /// is under way follows it.
    pub(super) fn load_automations(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        if self.automations_busy {
            self.automations_again = true;
            return;
        }
        self.automations_busy = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { automations::read(&client) }).await;
            this.update(cx, |this, cx| {
                this.automations_busy = false;
                if let Ok(board) = &result {
                    this.announce_failures(board);
                }
                this.automations = Some(result);
                this.automations_at = now_ms();
                if std::mem::take(&mut this.automations_again) {
                    this.load_automations(cx);
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// A run that failed since the last read is said in the status bar,
    /// wherever the person is (failures already there at the first read are
    /// only shown on the page).
    fn announce_failures(&mut self, board: &Board) {
        let failed: HashSet<String> = board.runs.iter().filter(|r| r.status == "failed").map(|r| r.id.clone()).collect();
        if let Some(seen) = &self.runs_failed_seen {
            if let Some(r) = board.runs.iter().find(|r| r.status == "failed" && !seen.contains(&r.id)) {
                let why = if r.error.is_empty() { String::new() } else { format!(": {}", r.error) };
                self.notice = Some((format!("{} failed{why}", r.worker).into(), true));
            }
        }
        self.runs_failed_seen = Some(failed);
    }

    /// Opens a run's own view and follows its session.
    fn open_run(&mut self, run: &Run, cx: &mut Context<Self>) {
        let open = RunOpen { run_id: run.id.clone(), worker_id: run.worker_id.clone(), session_id: run.session_id.clone() };
        self.run_entries = None;
        if let Some(live) = &self.live {
            live.watch_run(Some(open.session_id.clone()).filter(|s| !s.is_empty()));
        }
        self.run_open = Some(open);
        self.load_run(cx);
    }

    pub(super) fn close_run(&mut self, cx: &mut Context<Self>) {
        self.run_open = None;
        self.run_entries = None;
        if let Some(live) = &self.live {
            live.watch_run(None);
        }
        cx.notify();
    }

    /// Reads the open run's actions from its session.
    pub(super) fn load_run(&mut self, cx: &mut Context<Self>) {
        let (Some(client), Some(open)) = (self.live.as_ref().map(|l| l.client()), self.run_open.clone()) else { return };
        if open.session_id.is_empty() {
            self.run_entries = Some(Err("Swarm has not given this run a session yet.".into()));
            cx.notify();
            return;
        }
        cx.spawn(async move |this, cx| {
            let session = open.session_id.clone();
            let result = cx.background_executor().spawn(async move { automations::transcript(&client, &session) }).await;
            this.update(cx, |this, cx| {
                if this.run_open.as_ref() == Some(&open) {
                    this.run_entries = Some(result);
                    cx.notify();
                }
            })
            .ok();
        })
        .detach();
    }

    /// Asks Swarm to stop a run; the page follows Swarm's answer.
    fn stop_run(&mut self, worker_id: String, run_id: String, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        self.notice = Some(("Stopping the run…".into(), false));
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { automations::cancel(&client, &worker_id, &run_id) }).await;
            this.update(cx, |this, cx| {
                this.notice = Some(match result {
                    Ok(_) => ("Swarm is stopping the run.".into(), false),
                    Err(e) => (format!("Could not stop the run: {e}").into(), true),
                });
                this.load_automations(cx);
            })
            .ok();
        })
        .detach();
    }

    /// Workers whose latest finished run failed: what needs the person.
    pub(super) fn failing(board: &Board) -> Vec<&Run> {
        let mut seen = HashSet::new();
        board
            .runs
            .iter()
            .filter(|r| !r.active())
            .filter(|r| seen.insert(r.worker_id.clone()))
            .filter(|r| r.status == "failed")
            .collect()
    }

    pub(super) fn render_automations(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let body: AnyElement = match (&self.run_open, &self.automations) {
            (Some(open), Some(Ok(board))) => self.render_run(open, board, cx).into_any_element(),
            _ => self.render_overview(cx).into_any_element(),
        };
        div().id("automations-page").size_full().overflow_y_scroll().p_5().bg(t.bg).child(div().max_w(px(900.)).child(body))
    }

    fn render_overview(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let read = match (&self.automations, self.automations_busy) {
            (_, true) => "reading…".to_string(),
            (Some(_), false) => match super::ago(now_ms().saturating_sub(self.automations_at)).as_str() {
                "now" => "read just now".to_string(),
                ago => format!("read {ago} ago"),
            },
            (None, false) => String::new(),
        };
        let sections: Vec<Div> = self
            .machines
            .iter()
            .enumerate()
            .map(|(i, m)| {
                let body: Div = if i == 0 {
                    match &self.automations {
                        None => self.note_line("Reading…"),
                        Some(Err(e)) => div().text_size(px(12.)).text_color(t.err).child(format!("Swarm did not answer: {e}")),
                        Some(Ok(board)) => self.render_board(board, cx),
                    }
                } else {
                    self.note_line("This machine's workers show in its own window: open it from its card on the Agents page.")
                };
                div()
                    .flex()
                    .flex_col()
                    .gap_2()
                    .child(
                        div()
                            .flex()
                            .items_baseline()
                            .gap_2()
                            .child(div().text_size(px(14.)).text_color(t.text).child(m.name.clone()))
                            .child(div().text_size(px(11.)).text_color(t.faint).child(if i == 0 { "home" } else { "fleet" })),
                    )
                    .child(body)
            })
            .collect();
        div()
            .flex()
            .flex_col()
            .gap_5()
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .child(div().text_size(px(16.)).text_color(t.text).child("Automations"))
                    .child(div().flex_1().text_size(px(11.)).text_color(t.faint).child(read))
                    .child(self.button("automations-read", "read again", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.load_automations(cx)))),
            )
            .child(self.note_line("What your workers run on their own: what is running now, what failed, what they handed back, and their schedules. Open a run to see every action its agent takes. Ask the Orchestrator to make or change one."))
            .children(sections)
    }

    fn note_line(&self, text: impl Into<SharedString>) -> Div {
        div().text_size(px(11.)).text_color(self.theme.faint).line_height(px(17.)).child(text.into())
    }

    fn heading(&self, text: String, color: gpui::Hsla) -> Div {
        div().pt_2().text_size(px(10.5)).text_color(color).child(text)
    }

    fn run_card(&self) -> Div {
        let t = &self.theme;
        div().flex().flex_col().rounded(px(t.radius)).border_1().border_color(t.border).bg(t.panel)
    }

    fn render_board(&self, board: &Board, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let running: Vec<&Run> = board.runs.iter().filter(|r| r.active()).collect();
        let failing = Self::failing(board);
        let failing_ids: HashSet<&str> = failing.iter().map(|r| r.id.as_str()).collect();
        let recent: Vec<&Run> = board.runs.iter().filter(|r| !r.active() && !failing_ids.contains(r.id.as_str())).collect();
        let workers: Div = if board.workers.is_empty() {
            self.note_line("No workers on this machine.")
        } else {
            self.run_card().children(board.workers.iter().enumerate().map(|(i, w)| self.worker_row(w, i > 0, cx)))
        };
        let live: Vec<&Automation> = board.automations.iter().filter(|a| !a.archived).collect();
        let archived = board.automations.len() - live.len();
        let autos: Div = if live.is_empty() {
            self.note_line(if archived > 0 { format!("No automations running ({archived} archived).") } else { "No automations on this machine.".to_string() })
        } else {
            self.run_card().children(live.iter().enumerate().map(|(i, a)| self.automation_row(a, i > 0)))
        };
        div()
            .flex()
            .flex_col()
            .gap_2()
            .child(self.heading(format!("RUNNING NOW · {}", running.len()), if running.is_empty() { t.faint } else { t.accent }))
            .child(if running.is_empty() {
                self.note_line("Nothing is running.")
            } else {
                self.run_card().children(running.iter().enumerate().map(|(i, r)| self.run_row(r, i > 0, cx)))
            })
            .when(board.workers.iter().any(|w| w.waiting()) || board.workflows.iter().any(|w| w.state == "proposed"), |d| {
                let waiting: Vec<&Worker> = board.workers.iter().filter(|w| w.waiting()).collect();
                let proposed: Vec<&Workflow> = board.workflows.iter().filter(|w| w.state == "proposed").collect();
                let names = |id: &str| board.workers.iter().find(|w| w.id == id).map(|w| w.name.clone()).unwrap_or_else(|| id.to_string());
                d.child(self.heading(format!("WAITS FOR YOU · {}", waiting.len() + proposed.len()), t.warn))
                    .child(div().flex().flex_col().gap_2().children(waiting.into_iter().map(|w| self.worker_review(w, "page", cx))))
                    .children(proposed.into_iter().map(|w| self.workflow_card(w, &names, cx)))
            })
            .when(!failing.is_empty(), |d| {
                d.child(self.heading(format!("FAILED · {}", failing.len()), t.err))
                    .child(self.run_card().border_color(alpha(t.err, 0.5)).children(failing.iter().enumerate().map(|(i, r)| self.run_row(r, i > 0, cx))))
            })
            .child(self.heading(format!("RECENT RUNS · {}", recent.len()), t.faint))
            .child(if recent.is_empty() {
                self.note_line("No finished runs yet.")
            } else {
                self.run_card().children(recent.iter().enumerate().map(|(i, r)| self.run_row(r, i > 0, cx)))
            })
            .child(self.heading(format!("WORKERS · {}", board.workers.len()), t.faint))
            .child(workers)
            .child(self.heading(format!("AUTOMATIONS · {}", live.len()), t.faint))
            .child(autos)
            .when(archived > 0 && !live.is_empty(), |d| d.child(self.note_line(format!("{archived} archived, not shown."))))
            .child(self.heading(format!("WORKFLOWS · {}", board.workflows.len()), t.faint))
            .child(self.render_workflows(board, cx))
    }

    /// Each workflow as Swarm runs it: what starts it, its steps in order
    /// with who does each and when, and how its latest runs went.
    fn render_workflows(&self, board: &Board, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        if let Some(e) = &board.workflows_error {
            return div().text_size(px(12.)).text_color(t.err).child(format!("Swarm did not answer for workflows: {e}"));
        }
        if board.workflows.is_empty() {
            return self.note_line("No workflows on this machine. Ask the Orchestrator for one, for example a reviewer that checks every Coder task.");
        }
        let names = |id: &str| board.workers.iter().find(|w| w.id == id).map(|w| w.name.clone()).unwrap_or_else(|| id.to_string());
        div()
            .flex()
            .flex_col()
            .gap_2()
            .when(board.workflows_stopped, |d| d.child(div().text_size(px(11.)).text_color(t.warn).child("Swarm's stop switch is on: no workflow runs until it is turned off.")))
            .children(board.workflows.iter().map(|w| self.workflow_card(w, &names, cx)))
    }

    fn workflow_card(&self, w: &Workflow, names: &dyn Fn(&str) -> String, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let steps = w.steps.iter().enumerate().map(|(i, s)| {
            let brief: String = s.brief.split_whitespace().collect::<Vec<_>>().join(" ");
            let brief = if brief.chars().count() > 180 { format!("{}…", brief.chars().take(180).collect::<String>()) } else { brief };
            div()
                .px_3()
                .py_2()
                .flex()
                .flex_col()
                .gap(px(3.))
                .border_t_1()
                .border_color(t.border)
                .child(
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .child(div().text_size(px(11.)).text_color(t.faint).child(format!("{}", i + 1)))
                        .child(div().text_color(t.text).child(s.id.clone()))
                        .child(div().text_size(px(11.)).text_color(t.muted).child(workflows::describe(s, names)))
                        .child(div().flex_1())
                        .children(s.conditions.iter().map(|c| pill(self, c, t.warn))),
                )
                .when(!brief.is_empty(), |d| d.child(div().pl_4().text_size(px(11.)).text_color(t.faint).line_height(px(16.)).child(brief)))
        });
        let now = now_ms();
        let runs = w.deliveries.iter().take(5).map(|d| {
            let good = d.state == "succeeded";
            let color = match d.state.as_str() {
                "succeeded" => t.ok,
                "running" | "pending" => t.accent,
                _ => t.err,
            };
            let when = match super::ago(now.saturating_sub(d.created_at)).as_str() {
                "now" => "just now".to_string(),
                ago => format!("{ago} ago"),
            };
            let job = d.job_id.rsplit('/').next().unwrap_or(&d.job_id).to_string();
            div()
                .flex()
                .items_center()
                .gap_2()
                .text_size(px(11.))
                .child(div().text_color(color).child(if good { "●" } else { "○" }))
                .child(div().text_color(t.muted).child(workflows::chain(d)))
                .when(d.round > 0, |x| x.child(div().text_color(t.faint).child(format!("round {}", d.round))))
                .when(!d.blocked_reason.is_empty(), |x| x.child(div().text_color(t.err).child(d.blocked_reason.clone())))
                .child(div().flex_1())
                .child(div().text_color(t.faint).child(format!("{job} · {when}")))
        });
        self.run_card()
            .child(
                div()
                    .px_3()
                    .py_2()
                    .flex()
                    .flex_col()
                    .gap(px(3.))
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_2()
                            .child(div().text_color(t.text).child(w.name.clone()))
                            .child(match w.state.as_str() {
                                "proposed" => pill(self, "waits for you", t.warn),
                                _ if w.enabled => pill(self, "on", t.ok),
                                _ => pill(self, "off", t.faint),
                            })
                            .child(div().flex_1())
                            .child(div().text_size(px(11.)).text_color(t.faint).child(format!("revision {}", w.revision)))
                            .when(w.state == "proposed", |d| {
                                let (id, rev) = (w.id.clone(), w.revision);
                                let id2 = w.id.clone();
                                d.child(self.button(SharedString::from(format!("workflow-accept-{}", w.id)), "accept workflow", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.decide_workflow(id.clone(), rev, true, cx);
                                })))
                                .child(self.button(SharedString::from(format!("workflow-discard-{}", w.id)), "discard", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                    this.decide_workflow(id2.clone(), rev, false, cx);
                                })))
                            }),
                    )
                    .child(div().text_size(px(11.)).text_color(t.muted).child(workflows::trigger(w, names)))
                    .when_some(w.on.as_ref().filter(|on| !on.brief.is_empty()), |d, on| {
                        let brief: String = on.brief.split_whitespace().collect::<Vec<_>>().join(" ");
                        let brief = if brief.chars().count() > 180 { format!("{}…", brief.chars().take(180).collect::<String>()) } else { brief };
                        d.child(div().pl_4().text_size(px(11.)).text_color(t.faint).line_height(px(16.)).child(brief))
                    }),
            )
            .when(w.on.is_some(), |card| {
                let events = w.events.iter().take(5).map(|e| {
                    let color = match e.state.as_str() {
                        "started" => t.ok,
                        "queued" => t.accent,
                        _ => t.err,
                    };
                    let when = match super::ago(now.saturating_sub(e.received_at)).as_str() {
                        "now" => "just now".to_string(),
                        ago => format!("{ago} ago"),
                    };
                    let job = e.job_id.rsplit('/').next().unwrap_or(&e.job_id).to_string();
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .text_size(px(11.))
                        .child(div().text_color(color).child("●"))
                        .child(div().text_color(t.muted).child(format!("{} {}", e.item_id, e.state)))
                        .when(!e.error.is_empty(), |x| x.child(div().text_color(t.err).child(e.error.clone())))
                        .child(div().flex_1())
                        .child(div().text_color(t.faint).child(if job.is_empty() { when } else { format!("{job} · {when}") }))
                });
                card.child(
                    div()
                        .px_3()
                        .py_2()
                        .flex()
                        .flex_col()
                        .gap(px(3.))
                        .border_t_1()
                        .border_color(t.border)
                        .child(div().text_size(px(10.5)).text_color(t.faint).child("RECENT EVENTS"))
                        .children(events)
                        .when(w.events.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child("No event yet."))),
                )
            })
            .children(steps)
            .child(
                div()
                    .px_3()
                    .py_2()
                    .flex()
                    .flex_col()
                    .gap(px(3.))
                    .border_t_1()
                    .border_color(t.border)
                    .child(div().text_size(px(10.5)).text_color(t.faint).child("LATEST RUNS"))
                    .children(runs)
                    .when(w.deliveries.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child("It has not run yet."))),
            )
    }

    /// When a run started or ended, and how long it took, in words.
    fn run_times(r: &Run) -> String {
        let now = now_ms();
        if r.active() {
            return match (r.started_at, super::ago(now.saturating_sub(r.started_at)).as_str()) {
                (0, _) => "starting".into(),
                (_, "now") => "started just now".into(),
                (_, ago) => format!("started {ago} ago"),
            };
        }
        let ended = match (r.completed_at, super::ago(now.saturating_sub(r.completed_at)).as_str()) {
            (0, _) => String::new(),
            (_, "now") => "ended just now".into(),
            (_, ago) => format!("ended {ago} ago"),
        };
        let took = if r.completed_at > r.started_at && r.started_at > 0 { format!("took {}", took(r.completed_at - r.started_at)) } else { String::new() };
        [ended, took].into_iter().filter(|s| !s.is_empty()).collect::<Vec<_>>().join(" · ")
    }

    fn run_row(&self, r: &Run, rule: bool, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let (label, color) = status_of(self, r);
        let open = r.clone();
        let stop = (r.worker_id.clone(), r.id.clone());
        div()
            .id(SharedString::from(format!("run-{}", r.id)))
            .px_3()
            .py_2()
            .flex()
            .flex_col()
            .gap(px(3.))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .when(rule, |d| d.border_t_1().border_color(t.border))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_run(&open, cx)))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(t.text).child(r.worker.clone()))
                    .child(pill(self, label, color))
                    .child(div().flex_1().text_size(px(11.)).text_color(t.faint).child(format!("{} · {}", r.source, Self::run_times(r))))
                    .when(r.active() && !r.cancel_requested, |d| {
                        d.child(self.button(SharedString::from(format!("run-stop-{}", r.id)), "stop", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            cx.stop_propagation();
                            this.stop_run(stop.0.clone(), stop.1.clone(), cx);
                        })))
                    }),
            )
            .when(r.active() && !r.latest.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.info).truncate().child(format!("└ {}", r.latest))))
            .when(!r.error.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(if r.status == "failed" { t.err } else { t.faint }).child(r.error.clone())))
            .when(!r.deliverables.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child(format!("handed back: {}", r.deliverables.join(", ")))))
            .into_any_element()
    }

    /// A run's own view: its record, then every action from its session.
    fn render_run(&self, open: &RunOpen, board: &Board, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let back = self.button("run-back", "← all runs", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.close_run(cx)));
        let Some(r) = board.runs.iter().find(|r| r.id == open.run_id) else {
            return div().flex().flex_col().gap_3().child(back).child(self.note_line("Swarm no longer lists this run."));
        };
        let (label, color) = status_of(self, r);
        let stop = (r.worker_id.clone(), r.id.clone());
        let entries: Div = match &self.run_entries {
            None => self.note_line("Reading its actions…"),
            Some(Err(e)) => div().text_size(px(12.)).text_color(t.err).child(format!("Could not read its actions: {e}")),
            Some(Ok(list)) if list.is_empty() => self.note_line("No actions yet."),
            Some(Ok(list)) => div().flex().flex_col().gap(px(6.)).children(list.iter().map(|e| self.run_entry(e))),
        };
        div()
            .flex()
            .flex_col()
            .gap_3()
            .child(div().flex().child(back))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_size(px(16.)).text_color(t.text).child(r.worker.clone()))
                    .child(pill(self, label, color))
                    .child(div().flex_1())
                    .when(r.active() && !r.cancel_requested, |d| {
                        d.child(self.button("run-view-stop", "stop", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.stop_run(stop.0.clone(), stop.1.clone(), cx))))
                    }),
            )
            .child(self.note_line(format!("{} · {}", r.source, Self::run_times(r))))
            .when(!r.error.is_empty() && r.status != "failed", |d| d.child(self.note_line(r.error.clone())))
            .when(!r.error.is_empty() && r.status == "failed", |d| {
                d.child(
                    div()
                        .px_3()
                        .py_2()
                        .rounded(px(t.radius))
                        .border_1()
                        .border_color(alpha(t.err, 0.5))
                        .bg(alpha(t.err, 0.08))
                        .text_color(t.err)
                        .line_height(px(18.))
                        .child(r.error.clone()),
                )
            })
            .when(!r.deliverables.is_empty(), |d| d.child(div().text_size(px(12.)).text_color(t.muted).child(format!("Handed back: {}", r.deliverables.join(", ")))))
            .child(self.heading(if r.active() { "ACTIONS · LIVE".into() } else { "ACTIONS".into() }, if r.active() { t.accent } else { t.faint }))
            .child(entries)
    }

    fn run_entry(&self, e: &Entry) -> Div {
        let t = &self.theme;
        match e {
            Entry::User(text) => div().px_3().py_2().rounded(px(t.radius)).bg(t.raised).text_color(t.text).line_height(px(19.)).child(text.clone()),
            Entry::Assistant(text) => div().flex().gap_2().px_3().child(div().text_color(t.faint).child("◆")).child(div().flex_1().min_w_0().text_color(t.text).line_height(px(19.)).child(text.clone())),
            Entry::Tool { name, state, .. } => {
                let failed = *state == crate::data::ToolState::Failed || name.as_ref() == "error";
                let line = e.tool_line(crate::data::now_ms()).map(|l| l.text).unwrap_or_default();
                let (head, rest) = line.split_at(name.len().min(line.len()));
                div()
                    .flex()
                    .gap_2()
                    .px_3()
                    .text_size(px(11.5))
                    .child(div().text_color(t.faint).child("└"))
                    .child(div().flex_none().text_color(if failed { t.err } else { t.info }).child(head.to_string()))
                    .child(div().flex_1().min_w_0().truncate().text_color(if failed { t.err } else { t.faint }).child(rest.trim_start().to_string()))
            }
        }
    }

    fn worker_row(&self, w: &Worker, rule: bool, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let mut detail: Vec<String> = Vec::new();
        if !w.mode.is_empty() {
            detail.push(if w.mode == "plan" { "waits for your approval".into() } else { "runs on its own".into() });
        }
        if !w.folders.is_empty() {
            detail.push(format!("{} {}", if w.folders.len() == 1 { "folder" } else { "folders" }, w.folders.join(", ")));
        }
        let state = if w.state == "pending" { "waits for your review" } else { w.state.as_str() };
        let good = w.state == "active" || w.state == "idle";
        div()
            .px_3()
            .py_2()
            .flex()
            .flex_col()
            .gap(px(3.))
            .when(rule, |d| d.border_t_1().border_color(t.border))
            .child({
                let (id, rev) = (w.id.clone(), w.revision);
                let can = matches!(w.state.as_str(), "active" | "idle" | "paused");
                let pause = w.state != "paused";
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(t.text).child(w.name.clone()))
                    .child(pill(self, state, if good { t.ok } else { t.faint }))
                    .child(div().flex_1())
                    .when(can, |d| {
                        d.child(self.button(SharedString::from(format!("worker-pause-{}", w.id)), if pause { "pause" } else { "resume" }, false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.pause_worker(id.clone(), rev, pause, cx);
                        })))
                    })
            })
            .when_some(w.pending.as_ref(), |d, p| {
                let names: Vec<String> = p.iter().map(|a| format!("{} ({})", a.name, a.when)).collect();
                d.child(div().text_size(px(11.)).text_color(t.warn).child(if names.is_empty() { "A change waits for your review in Swarm.".to_string() } else { format!("A change waits for your review in Swarm: {}", names.join(", ")) }))
            })
            .when(!detail.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child(detail.join(" · "))))
            .when(!w.description.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child(w.description.clone())))
            .children(w.automations.iter().map(|a| {
                div()
                    .pl_3()
                    .flex()
                    .gap_2()
                    .text_size(px(11.))
                    .child(div().text_color(if a.enabled { t.accent } else { t.faint }).child(if a.enabled { "●" } else { "○" }))
                    .child(div().text_color(t.muted).child(a.name.clone()))
                    .child(div().text_color(t.faint).child(if a.enabled { a.when.clone() } else { format!("{} · off", a.when) }))
            }))
    }

    fn automation_row(&self, a: &Automation, rule: bool) -> Div {
        let t = &self.theme;
        let (state, good) = match (a.cancelled, a.enabled) {
            (true, _) => ("cancelled", false),
            (false, true) => ("on", true),
            (false, false) => ("paused", false),
        };
        let mut detail = vec![a.when.clone()];
        let now = now_ms();
        if let Some(next) = a.next_due_at.filter(|_| a.enabled && !a.cancelled) {
            detail.push(if next > now { format!("next in {}", super::ago(next - now)) } else { "due now".into() });
        }
        if let Some(end) = a.expires_at {
            detail.push(if end > now { format!("ends in {}", super::ago(end - now)) } else { "ended".into() });
        }
        if let Some(p) = self.projects.iter().find(|p| !a.project_id.is_empty() && p.id.as_ref() == a.project_id) {
            detail.push(p.name.to_string());
        }
        let title = if a.title.is_empty() { a.id.clone() } else { a.title.clone() };
        div()
            .px_3()
            .py_2()
            .flex()
            .flex_col()
            .gap(px(3.))
            .when(rule, |d| d.border_t_1().border_color(t.border))
            .child(div().flex().items_center().gap_2().child(div().text_color(t.text).child(title)).child(pill(self, state, if good { t.ok } else { t.faint })))
            .child(div().text_size(px(11.)).text_color(t.faint).child(detail.join(" · ")))
            .when(!a.goal.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child(a.goal.clone())))
    }
}

impl Console {
    /// Accepts or discards a waiting worker at the revision shown; the page
    /// and the card follow Swarm's answer.
    /// Accepts or discards a workflow the Orchestrator proposed, at the revision shown.
    fn decide_workflow(&mut self, id: String, revision: u64, accept: bool, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        self.notice = Some((if accept { "Accepting the workflow…" } else { "Discarding the workflow…" }.into(), false));
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { if accept { workflows::accept(&client, &id, revision) } else { workflows::discard(&client, &id) } }).await;
            this.update(cx, |this, cx| {
                this.notice = Some(match result {
                    Ok(_) if accept => ("Workflow accepted: it runs from now on.".into(), false),
                    Ok(_) => ("Workflow discarded.".into(), false),
                    Err(e) => (format!("Swarm refused: {e}").into(), true),
                });
                this.load_automations(cx);
            })
            .ok();
        })
        .detach();
    }

    fn decide_worker(&mut self, worker_id: String, revision: u64, accept: bool, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        self.notice = Some((if accept { "Accepting the worker…" } else { "Discarding the worker…" }.into(), false));
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { if accept { automations::accept(&client, &worker_id, revision) } else { automations::discard(&client, &worker_id, revision) } })
                .await;
            this.update(cx, |this, cx| {
                this.notice = Some(match result {
                    Ok(_) if accept => ("Worker accepted: its schedules run now.".into(), false),
                    Ok(_) => ("Worker discarded.".into(), false),
                    Err(e) => (format!("Swarm refused: {e}").into(), true),
                });
                this.load_automations(cx);
            })
            .ok();
        })
        .detach();
    }

    /// Workflows the Orchestrator proposed that wait for the person.
    pub(super) fn proposed_workflows(&self) -> usize {
        match &self.automations {
            Some(Ok(board)) => board.workflows.iter().filter(|w| w.state == "proposed").count(),
            _ => 0,
        }
    }

    /// Workers waiting for the person, from Swarm's last read.
    pub(super) fn waiting_workers(&self) -> Vec<&Worker> {
        match &self.automations {
            Some(Ok(board)) => board.workers.iter().filter(|w| w.waiting()).collect(),
            _ => vec![],
        }
    }

    /// In a conversation: the workers it proposed that wait for the person.
    pub(super) fn render_worker_reviews(&self, si: usize, cx: &mut Context<Self>) -> Vec<AnyElement> {
        let Some(session_id) = self.sessions[si].live.as_ref().map(|l| l.session_id.to_string()) else { return vec![] };
        self.waiting_workers().into_iter().filter(|w| w.source_session == session_id).map(|w| self.worker_review(w, "chat", cx)).collect()
    }

    /// A waiting worker: what it will do and when, and accept or discard.
    pub(super) fn worker_review(&self, w: &Worker, place: &str, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let change = w.pending.is_some() && w.state != "pending";
        let schedules: Vec<String> = match (&w.pending, change) {
            (Some(p), true) => p.iter().map(|a| format!("{} · {}{}", a.name, a.when, if a.enabled { "" } else { " (off)" })).collect(),
            _ => w.automations.iter().map(|a| format!("{} · {}{}", a.name, a.when, if a.enabled { "" } else { " (off)" })).collect(),
        };
        let (id, rev) = (w.id.clone(), w.revision);
        let (id2, rev2) = (w.id.clone(), w.revision);
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
            .bg(alpha(t.warn, 0.06))
            .child(div().text_size(px(11.)).text_color(t.warn).child(if change { "A CHANGE TO A WORKER WAITS FOR YOU" } else { "A NEW WORKER WAITS FOR YOU" }))
            .child(div().text_color(t.text).child(w.name.clone()))
            .when(!w.description.is_empty() && w.description.trim() != w.instructions.trim(), |d| d.child(div().text_size(px(12.)).text_color(t.muted).child(w.description.clone())))
            .when(!w.instructions.is_empty(), |d| d.child(div().text_size(px(12.)).text_color(t.text).line_height(px(18.)).child(format!("It is told: {}", w.instructions))))
            .child(div().text_size(px(12.)).text_color(t.muted).child(if schedules.is_empty() { "No schedule: it runs only when asked.".to_string() } else { format!("Runs: {}", schedules.join("; ")) }))
            .when(!w.folders.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child(format!("In: {}", w.folders.join(", ")))))
            .child(div().text_size(px(11.)).text_color(t.faint).child("Nothing runs until you accept. Accepting starts its schedules; it runs as Swarm's agents do, in its folders."))
            .child(
                div()
                    .flex()
                    .gap_1()
                    .child(self.button(SharedString::from(format!("worker-accept-{place}-{}", w.id)), if change { "accept the change" } else { "accept worker" }, true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        cx.stop_propagation();
                        this.decide_worker(id.clone(), rev, true, cx);
                    })))
                    .when(!change, |d| {
                        d.child(self.button(SharedString::from(format!("worker-discard-{place}-{}", w.id)), "discard", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            cx.stop_propagation();
                            this.decide_worker(id2.clone(), rev2, false, cx);
                        })))
                    }),
            )
            .into_any_element()
    }
}

impl Console {
    /// Pauses or resumes a worker in Swarm; the page follows its answer.
    fn pause_worker(&mut self, worker_id: String, revision: u64, pause: bool, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        self.notice = Some((if pause { "Pausing the worker…" } else { "Resuming the worker…" }.into(), false));
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { automations::set_paused(&client, &worker_id, revision, pause) }).await;
            this.update(cx, |this, cx| {
                this.notice = Some(match result {
                    Ok(_) if pause => ("Worker paused: its schedules stop. A run already going finishes; stop it on its row.".into(), false),
                    Ok(_) => ("Worker resumed: its schedules run again.".into(), false),
                    Err(e) => (format!("Swarm refused: {e}").into(), true),
                });
                this.load_automations(cx);
            })
            .ok();
        })
        .detach();
    }
}

/// A run's status in a word and its color.
fn status_of(c: &Console, r: &Run) -> (&'static str, gpui::Hsla) {
    let t = &c.theme;
    match r.status.as_str() {
        "running" if r.cancel_requested => ("stopping", t.warn),
        "running" => ("running", t.accent),
        "admitted" => ("starting", t.accent),
        "succeeded" => ("succeeded", t.ok),
        "failed" => ("failed", t.err),
        "cancelled" => ("stopped", t.faint),
        _ => ("unknown", t.faint),
    }
}

fn pill(c: &Console, label: &str, color: gpui::Hsla) -> Div {
    let t = &c.theme;
    div().px(px(5.)).rounded(px(t.radius)).bg(alpha(color, 0.14)).text_color(color).text_size(px(10.5)).child(label.to_string())
}

/// A duration in milliseconds, in words: 42s, 3m 10s, 1h 5m.
fn took(ms: u64) -> String {
    let s = ms / 1000;
    match s {
        s if s < 60 => format!("{s}s"),
        s if s < 3600 => format!("{}m {}s", s / 60, s % 60),
        s => format!("{}h {}m", s / 3600, (s % 3600) / 60),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn run(id: &str, worker: &str, status: &str, at: u64) -> Run {
        Run { id: id.into(), worker_id: worker.into(), worker: worker.into(), status: status.into(), error: String::new(), source: String::new(), session_id: String::new(), started_at: at, completed_at: at, cancel_requested: false, deliverables: vec![], latest: String::new() }
    }

    /// A worker needs the person while its latest finished run failed; a
    /// later success clears it, a run in progress does not.
    #[test]
    fn failing_is_the_latest_finished_run() {
        let board = Board {
            workers: vec![],
            automations: vec![],
            runs: vec![run("a3", "a", "running", 30), run("a2", "a", "failed", 20), run("b2", "b", "succeeded", 25), run("b1", "b", "failed", 10)],
            ..Default::default()
        };
        let ids: Vec<&str> = Console::failing(&board).iter().map(|r| r.id.as_str()).collect();
        assert_eq!(ids, vec!["a2"]);
    }
}
