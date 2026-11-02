//! The plan review panel: a waiting task's complete plan, where it is and
//! its exact version, with the person's decision (swarm::plan_review).
//!
//! It opens from a task card in a conversation or in the Companion's work,
//! for a task on this Swarm or on a linked machine. Accept names the version
//! shown; when Swarm refuses because the plan changed or the task moved, the
//! panel shows Swarm's current review and the person decides again. A
//! successful decision closes nothing by itself: the panel shows what Swarm
//! says now (started, revising) and the card follows Swarm's records.
//!
//! Every read and decision is a request with its own number and its task
//! (PlanReviews): its answer goes only to the panel that asked, while it is
//! still open on that task and that request is still the latest of its kind.
//! A closed, switched or reopened panel never takes an older request's
//! answer. A decision still on its way blocks another decision on the same
//! task, from any panel, until Swarm answers it; when it answers to a panel
//! opened after it, that panel reads the task again.

use std::collections::HashMap;

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};
use serde_json::Value;

use super::{Console, alpha};
use crate::data::{ReviewRef, Text};
use crate::swarm::live::Command;
use crate::swarm::plan_review::{self, Answer, Binding, Decided, Decision, Review};

/// Selection keys for the plan's text, apart from every pane's.
const REVIEW_PANE: usize = usize::MAX - 3;

/// A decision sent from a panel and not answered yet.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct Pending {
    pub op: u64,
    pub accept: bool,
    /// The exact version the decision named (None: an ordinary start).
    pub binding: Option<Binding>,
}

/// What the panel holds: the task it is for and Swarm's last answer.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct PlanReviewPanel {
    pub target: ReviewRef,
    /// The card's title, until Swarm's review names the task.
    pub title: Text,
    pub review: Option<Review>,
    /// The read in flight: only its answer is taken.
    pub reading: Option<u64>,
    /// This panel's decision in flight.
    pub deciding: Option<Pending>,
    /// Swarm's refusal or a read that failed, in its words.
    pub error: Option<String>,
    /// What Swarm did with the decision.
    pub note: Option<String>,
    /// The person is writing what to change.
    pub revising: bool,
}

impl PlanReviewPanel {
    fn open(target: ReviewRef, title: Text, op: u64) -> PlanReviewPanel {
        PlanReviewPanel { target, title, review: None, reading: Some(op), deciding: None, error: None, note: None, revising: false }
    }

    pub fn loading(&self) -> bool {
        self.reading.is_some()
    }

    /// Nothing shown may be decided until the task is read again.
    fn needs_reading(&mut self, problem: &str) {
        if let Some(review) = self.review.as_mut() {
            review.decision = Decision::Reload;
            review.problem = problem.to_string();
        }
    }

    /// A review read came back. One for another task is not this panel's.
    fn read(&mut self, result: Result<Value, String>) {
        match result {
            Ok(answer) => match plan_review::parse(&answer) {
                Some(review) if review.task_id == self.target.task_id.as_ref() && review.machine == self.target.machine.as_ref() => {
                    self.review = Some(review);
                    self.error = None;
                }
                Some(_) => self.error = Some("Swarm answered for another task; read again.".into()),
                None => self.error = Some("Swarm answered no review for this task.".into()),
            },
            // The last review stays, marked as not current.
            Err(e) => {
                self.error = Some(e);
                self.needs_reading("The plan could not be read; read it again before deciding.");
            }
        }
    }

    /// A decision's answer, as Swarm said it (plan_review::Decided).
    fn decided(&mut self, outcome: Decided, pending: &Pending) {
        let version = pending.binding.as_ref().map(|b| format!("version {}", b.revision));
        let what = match (pending.accept, &version) {
            (true, Some(v)) => format!("Accepted {v}."),
            (true, None) => "Started.".to_string(),
            (false, Some(v)) => format!("Sent {v} back for changes."),
            (false, None) => "Sent back for changes.".to_string(),
        };
        match outcome {
            Decided::Done { review: Some(review), .. } => {
                let now = review.run_status.replace('_', " ");
                self.review = Some(review);
                self.error = None;
                self.revising = false;
                self.note = Some(if pending.accept {
                    format!("{what} Swarm says the task is {now} now.")
                } else {
                    format!("{what} Swarm says the task is {now} now; a new version comes for review.")
                });
            }
            // Carried out, and the read after it failed: its state is not
            // known. Nothing is decidable until it is read again.
            Decided::Done { review: None, refresh_error, .. } => {
                self.error = None;
                self.revising = false;
                self.note = Some(format!("{what} Swarm confirmed it, but could not read the task after it ({refresh_error}). Its current state is unknown: read it again."));
                if let Some(review) = self.review.as_mut() {
                    review.run_status = "unknown".into();
                }
                self.needs_reading("Your decision was carried out; read the task again to see its state.");
            }
            Decided::Refused { error, review } => {
                self.note = None;
                self.error = Some(if error.is_empty() { "Swarm refused the decision.".into() } else { error });
                match review {
                    // Swarm's current review: the person decides on it again.
                    Some(review) => self.review = Some(review),
                    None => self.needs_reading("The task changed and could not be read again; read it before deciding."),
                }
            }
            Decided::Denied { status, error } => {
                self.note = None;
                self.error = Some(format!("Refused (HTTP {status}): {error} Nothing was done."));
                self.needs_reading("Swarm refused the decision; read the task again.");
            }
            Decided::Unknown { error } => {
                self.note = None;
                self.error = Some(format!("{error} Swarm did not say whether your decision was carried out, and it was not sent again. Read the task to see."));
                if let Some(review) = self.review.as_mut() {
                    review.run_status = "unknown".into();
                }
                self.needs_reading("The decision's outcome is unknown; read the task again before deciding.");
            }
        }
    }
}

/// A request the console sends for the panel.
#[derive(Clone, Debug, PartialEq)]
pub(super) enum Send {
    Read { op: u64, target: ReviewRef },
    Decide { op: u64, target: ReviewRef, body: Value },
}

impl Send {
    fn command(self) -> Command {
        match self {
            Send::Read { op, target } => Command::ReadPlanReview { op, target },
            Send::Decide { op, target, body } => Command::DecidePlan { op, target, body },
        }
    }
}

fn task_key(target: &ReviewRef) -> (String, String, String) {
    (target.machine.to_string(), target.project_id.to_string(), target.task_id.to_string())
}

/// The open panel and the plan review requests in flight.
#[derive(Default)]
pub(super) struct PlanReviews {
    pub panel: Option<PlanReviewPanel>,
    next: u64,
    /// Decisions sent and not answered, by task: their request.
    deciding: HashMap<(String, String, String), u64>,
}

impl PlanReviews {
    fn op(&mut self) -> u64 {
        self.next += 1;
        self.next
    }

    /// Opens a task's review (replacing any open one) and reads it.
    pub fn open(&mut self, target: ReviewRef, title: Text) -> Send {
        let op = self.op();
        self.panel = Some(PlanReviewPanel::open(target.clone(), title, op));
        Send::Read { op, target }
    }

    /// Reads the open task again; an older read's answer is then ignored.
    pub fn reload(&mut self) -> Option<Send> {
        let op = self.op();
        let panel = self.panel.as_mut()?;
        panel.reading = Some(op);
        Some(Send::Read { op, target: panel.target.clone() })
    }

    pub fn close(&mut self) {
        self.panel = None;
    }

    /// A decision on this task, from any panel, is still on its way.
    pub fn task_deciding(&self) -> bool {
        self.panel.as_ref().is_some_and(|p| self.deciding.contains_key(&task_key(&p.target)))
    }

    /// The person's decision on what the panel shows: exactly that version.
    /// Refused here while a read or a decision on the task is in flight, or
    /// when nothing shown can be decided.
    pub fn decide(&mut self, accept: bool, feedback: &str) -> Result<Send, String> {
        let task_deciding = self.task_deciding();
        let op = self.next + 1;
        let panel = self.panel.as_mut().ok_or("No review is open.")?;
        if panel.reading.is_some() {
            return Err("The plan is being read; decide when it shows.".into());
        }
        if panel.deciding.is_some() || task_deciding {
            return Err("A decision on this task is on its way; wait for Swarm's answer.".into());
        }
        let review = panel.review.as_ref().ok_or("The plan has not been read.")?;
        let body = plan_review::decision_body(review, accept, feedback)?;
        let binding = if review.decision == Decision::AcceptPlan { review.binding.clone() } else { None };
        self.next = op;
        panel.deciding = Some(Pending { op, accept, binding });
        panel.error = None;
        panel.note = None;
        let target = panel.target.clone();
        self.deciding.insert(task_key(&target), op);
        Ok(Send::Decide { op, target, body })
    }

    /// Swarm answered request `op` about `target`. Only the panel that asked
    /// takes it; a panel opened on the same task after a decision was sent
    /// reads the task again when that decision is answered.
    pub fn answer(&mut self, op: u64, target: &ReviewRef, answer: Answer) -> Option<Send> {
        let key = task_key(target);
        let decision = matches!(answer, Answer::Decided(_));
        if decision && self.deciding.get(&key) == Some(&op) {
            self.deciding.remove(&key);
        }
        let panel = self.panel.as_mut().filter(|p| task_key(&p.target) == key)?;
        match answer {
            Answer::Read(result) if panel.reading == Some(op) => {
                panel.reading = None;
                panel.read(result);
                None
            }
            Answer::Decided(outcome) if panel.deciding.as_ref().is_some_and(|d| d.op == op) => {
                let pending = panel.deciding.take().unwrap();
                panel.decided(outcome, &pending);
                None
            }
            // An earlier panel's decision on this task was answered: what
            // this one shows may be stale.
            Answer::Decided(_) => {
                panel.note = Some("A decision sent earlier on this task was answered; reading the current version.".into());
                panel.needs_reading("Reading the current version after an earlier decision.");
                self.reload()
            }
            // An older read, or one for a closed panel: not this panel's.
            Answer::Read(_) => None,
        }
    }
}

impl Console {
    fn send_plan(&self, send: Send) {
        if let Some(live) = &self.live {
            live.run(send.command());
        }
    }

    /// Opens a task's review and reads it from Swarm.
    pub(super) fn open_plan_review(&mut self, target: ReviewRef, title: Text, cx: &mut Context<Self>) {
        if self.live.is_none() {
            return;
        }
        if let Some(input) = &self.plan_feedback {
            input.update(cx, |input, cx| input.set_text("", cx));
        }
        let send = self.plan_reviews.open(target, title);
        self.send_plan(send);
        cx.notify();
    }

    fn reload_plan_review(&mut self, cx: &mut Context<Self>) {
        if let Some(send) = self.plan_reviews.reload() {
            self.send_plan(send);
        }
        cx.notify();
    }

    /// The person's decision: accept what is shown, or send it back with
    /// what to change.
    fn decide_plan(&mut self, accept: bool, cx: &mut Context<Self>) {
        let feedback = self.plan_feedback.as_ref().map(|i| i.read(cx).text()).unwrap_or_default();
        match self.plan_reviews.decide(accept, &feedback) {
            Ok(send) => self.send_plan(send),
            Err(why) => {
                if let Some(panel) = self.plan_reviews.panel.as_mut() {
                    panel.error = Some(why);
                }
            }
        }
        cx.notify();
    }

    pub(super) fn plan_review_answer(&mut self, op: u64, target: &ReviewRef, answer: Answer) {
        if let Some(send) = self.plan_reviews.answer(op, target, answer) {
            self.send_plan(send);
        }
    }

    /// The panel over the window, while a review is open.
    pub(super) fn render_plan_review(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let panel = self.plan_reviews.panel.as_ref()?;
        let t = &self.theme;
        let close = self.button("plan-review-close", "close", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
            this.plan_reviews.close();
            cx.notify();
        }));
        let reload = self.button("plan-review-reload", "read again", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.reload_plan_review(cx)));
        let review = panel.review.as_ref();
        let title = review.map(|r| r.title.clone()).filter(|t| !t.is_empty()).unwrap_or_else(|| panel.title.to_string());
        let place = match review {
            Some(r) => r.place(),
            None => {
                let machine = if panel.target.machine.is_empty() { "this Swarm".to_string() } else { format!("machine {}", panel.target.machine) };
                format!("{machine} · project {} · task {}", panel.target.project_id, panel.target.task_id)
            }
        };
        let status = review.map(|r| format!("{} · {}", r.run_status.replace('_', " "), r.version())).unwrap_or_default();
        let mut body = div().flex().flex_col().gap_3().min_w_0();
        if panel.loading() && review.is_none() {
            body = body.child(div().text_color(t.muted).child("Reading the plan from Swarm…"));
        }
        if let Some(r) = review {
            if !r.markdown.trim().is_empty() && r.decision != Decision::Start {
                body = body.child(self.markdown(&r.markdown, REVIEW_PANE, 0, cx));
            }
            if r.decision == Decision::Start {
                body = body.child(div().text_color(t.text).line_height(px(19.)).child("This task has no plan to accept: it waits to start."));
            }
            if !r.problem.is_empty() {
                body = body.child(div().text_size(px(11.5)).text_color(t.warn).child(r.problem.clone()));
            }
            if !r.last_error.is_empty() {
                body = body.child(div().text_size(px(11.5)).text_color(t.muted).child(format!("last error: {}", r.last_error)));
            }
        }
        let decision = review.map(|r| r.decision).unwrap_or(Decision::Reload);
        let task_deciding = self.plan_reviews.task_deciding();
        let busy = panel.deciding.is_some() || panel.loading() || task_deciding;
        let version = review.and_then(|r| r.binding.as_ref()).map(|b| b.revision).unwrap_or(0);
        let accept_label: SharedString = match decision {
            Decision::AcceptPlan => format!("accept version {version}").into(),
            _ => "start task".into(),
        };
        let can_accept = !busy && matches!(decision, Decision::AcceptPlan | Decision::Start);
        let can_revise = !busy && decision == Decision::AcceptPlan;
        let mut actions = div().flex().items_center().gap_2();
        if can_accept {
            actions = actions.child(self.button("plan-review-accept", accept_label, true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.decide_plan(true, cx))));
        }
        if can_revise && !panel.revising {
            actions = actions.child(self.button("plan-review-revise", "request changes", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                if let Some(p) = this.plan_reviews.panel.as_mut() {
                    p.revising = true;
                }
                cx.notify();
            })));
        }
        actions = actions.child(reload).child(div().flex_1()).child(close);
        let revise = (panel.revising && can_revise).then(|| {
            div()
                .flex()
                .flex_col()
                .gap_1()
                .child(div().text_size(px(11.)).text_color(t.muted).child("What should change? The plan goes back for a new version; nothing starts."))
                .children(
                    self.plan_feedback.clone().map(|input| div().min_h(px(60.)).p_2().rounded(px(t.radius)).border_1().border_color(t.border_strong).bg(t.surface).child(input)),
                )
                .child(
                    div()
                        .flex()
                        .gap_2()
                        .child(self.button("plan-review-send-changes", "send changes", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.decide_plan(false, cx))))
                        .child(self.button("plan-review-cancel-changes", "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                            if let Some(p) = this.plan_reviews.panel.as_mut() {
                                p.revising = false;
                            }
                            cx.notify();
                        }))),
                )
        });
        let consent = match decision {
            Decision::AcceptPlan => {
                "Only you accept a plan. Start tasks and linked-machine rules set to auto do not accept it; this version starts only if it is still the current one."
            }
            Decision::Start => "Starting this task is your decision.",
            _ => "",
        };
        Some(
            div()
                .id("plan-review-backdrop")
                .absolute()
                .inset_0()
                .occlude()
                .bg(alpha(t.bg, 0.6))
                .flex()
                .justify_center()
                .items_center()
                .child(
                    div()
                        .id("plan-review")
                        .w(px(760.))
                        .max_h(gpui::relative(0.9))
                        .flex()
                        .flex_col()
                        .rounded(px(t.radius + 2.))
                        .border_1()
                        .border_color(t.border_strong)
                        .bg(t.panel)
                        .shadow_md()
                        .on_click(|_, _, cx| cx.stop_propagation())
                        .child(
                            div()
                                .px_4()
                                .pt_3()
                                .pb_2()
                                .flex()
                                .flex_col()
                                .gap_1()
                                .border_b_1()
                                .border_color(t.border)
                                .child(div().text_size(px(10.5)).text_color(t.warn).child(match decision {
                                    Decision::AcceptPlan => "PLAN WAITS FOR YOUR ACCEPTANCE",
                                    Decision::Start => "TASK WAITS TO START",
                                    Decision::Reload => "PLAN NOT READ",
                                    Decision::None => "NOTHING WAITS FOR YOU",
                                }))
                                .child(div().text_color(t.text).text_size(px(14.)).child(title))
                                .child(div().text_size(px(11.)).text_color(t.muted).child(place))
                                .when(!status.trim_matches([' ', '·']).is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).child(status))),
                        )
                        .child(div().id("plan-review-body").flex_1().min_h_0().overflow_y_scroll().px_4().py_3().child(body))
                        .child(
                            div()
                                .px_4()
                                .py_2()
                                .flex()
                                .flex_col()
                                .gap_2()
                                .border_t_1()
                                .border_color(t.border)
                                .when_some(panel.error.clone(), |d, e| d.child(div().text_size(px(11.5)).text_color(t.warn).child(e)))
                                .when_some(panel.note.clone(), |d, n| d.child(div().text_size(px(11.5)).text_color(t.accent).child(n)))
                                .when(panel.deciding.is_some(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child("Sending your decision…")))
                                .when(panel.deciding.is_none() && task_deciding, |d| {
                                    d.child(div().text_size(px(11.)).text_color(t.muted).child("A decision on this task sent earlier is still on its way."))
                                })
                                .when(panel.loading() && review.is_some(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child("Reading the current version…")))
                                .children(revise)
                                .child(actions)
                                .when(!consent.is_empty(), |d| d.child(div().text_size(px(10.5)).text_color(t.faint).child(consent))),
                        ),
                )
                .into_any_element(),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn review_for(task: &str, revision: u64, status: &str, decision: &str) -> Value {
        json!({"review": {"machine": "forge", "project_id": "rp", "task_id": task, "title": "Remote plan", "run_status": status, "plan_bound": true,
            "plan_binding": {"session_id": "s", "plan_id": "plan-a", "definition_revision": revision}, "plan_document": {"title": "Plan"},
            "plan_markdown": format!("# Plan v{revision}\n"), "decision": decision}})
    }

    fn review(revision: u64) -> Value {
        review_for("rt", revision, "pending_approval", "accept_plan")
    }

    fn target_for(task: &str) -> ReviewRef {
        ReviewRef { machine: "forge".into(), project_id: "rp".into(), task_id: task.to_string().into(), plan: false }
    }

    fn target() -> ReviewRef {
        target_for("rt")
    }

    fn read_op(send: &Send) -> u64 {
        match send {
            Send::Read { op, .. } => *op,
            other => panic!("expected a read: {other:?}"),
        }
    }

    fn decide_op(send: &Send) -> (u64, Value) {
        match send {
            Send::Decide { op, body, .. } => (*op, body.clone()),
            other => panic!("expected a decision: {other:?}"),
        }
    }

    fn accepted(task: &str, revision: u64) -> Answer {
        Answer::Decided(Decided::Done {
            binding: Some(Binding { session_id: "s".into(), plan_id: "plan-a".into(), revision }),
            review: plan_review::parse(&review_for(task, revision, "in_progress", "none")),
            refresh_error: String::new(),
        })
    }

    // Purpose: the panel keeps the person on a current, readable plan. A
    // refusal because the plan changed shows Swarm's new version (and its
    // reason) for a new decision; a decision whose outcome is unknown leaves
    // nothing decidable until the plan is read again and is not resent; an
    // accepted one shows Swarm's state after it. Threat: accepting an old
    // version after a refusal, or a stale panel looking decidable after a
    // lost answer. Owner: PlanReviews::decide/answer, PlanReviewPanel.
    // Narrowest layer: the pure panel state the GPUI panel renders.
    #[test]
    fn a_refused_decision_shows_the_current_version() {
        let mut r = PlanReviews::default();
        let open = r.open(target(), "Remote plan".into());
        assert!(r.answer(read_op(&open), &target(), Answer::Read(Ok(review(2)))).is_none());
        let p = r.panel.as_ref().unwrap();
        assert!(!p.loading());
        assert_eq!(p.review.as_ref().unwrap().binding.as_ref().unwrap().revision, 2);

        // 409: the plan changed. The new version shows with Swarm's reason.
        let (op, body) = decide_op(&r.decide(true, "").unwrap());
        assert_eq!(body["plan_binding"]["definition_revision"], 2, "the decision names the version shown");
        assert_eq!(r.decide(true, "").unwrap_err(), "A decision on this task is on its way; wait for Swarm's answer.", "a double click sends nothing");
        r.answer(op, &target(), Answer::Decided(Decided::Refused { error: "the plan changed (current definition_revision 3)".into(), review: plan_review::parse(&review(3)) }));
        let p = r.panel.as_ref().unwrap();
        assert_eq!(p.error.as_deref(), Some("the plan changed (current definition_revision 3)"));
        let shown = p.review.as_ref().unwrap();
        assert_eq!((shown.binding.as_ref().unwrap().revision, shown.decision), (3, Decision::AcceptPlan));

        // Unknown outcome: nothing is decidable until read again; not resent.
        let (op, body) = decide_op(&r.decide(true, "").unwrap());
        assert_eq!(body["plan_binding"]["definition_revision"], 3, "the next accept names the version now shown");
        assert!(r.answer(op, &target(), Answer::Decided(Decided::Unknown { error: "Swarm is not answering.".into() })).is_none(), "nothing is sent again");
        let p = r.panel.as_ref().unwrap();
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::Reload);
        assert_eq!(p.review.as_ref().unwrap().run_status, "unknown", "never shown as still waiting");
        assert!(p.error.as_deref().unwrap().contains("did not say whether"));
        assert!(r.decide(true, "").is_err());

        // Read again, then accepted: Swarm's state after it.
        let reread = r.reload().unwrap();
        r.answer(read_op(&reread), &target(), Answer::Read(Ok(review(3))));
        let (op, _) = decide_op(&r.decide(true, "").unwrap());
        r.answer(op, &target(), accepted("rt", 3));
        let p = r.panel.as_ref().unwrap();
        assert_eq!(p.error, None);
        assert_eq!(p.note.as_deref(), Some("Accepted version 3. Swarm says the task is in progress now."));
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::None, "a second click has nothing to accept");
        assert!(r.decide(true, "").is_err());
    }

    // Purpose: an accepted decision whose follow-up read failed, or one Swarm
    // refused for permission, never looks like a plan still to accept.
    // Requirements: Done without a review keeps the exact version decided in
    // the note, marks the state unknown and blocks deciding until read
    // again; Denied keeps Swarm's status and reason and blocks the same way.
    // Threat: "Accepted ... pending approval" with accept offered again.
    // Owner: PlanReviewPanel::decided. Narrowest layer: the panel state.
    #[test]
    fn an_accept_with_unknown_state_offers_nothing() {
        let mut r = PlanReviews::default();
        let open = r.open(target(), "Remote plan".into());
        r.answer(read_op(&open), &target(), Answer::Read(Ok(review(2))));
        let (op, _) = decide_op(&r.decide(true, "").unwrap());
        r.answer(
            op,
            &target(),
            Answer::Decided(Decided::Done {
                binding: Some(Binding { session_id: "s".into(), plan_id: "plan-a".into(), revision: 2 }),
                review: None,
                refresh_error: "forge did not answer".into(),
            }),
        );
        let p = r.panel.as_ref().unwrap();
        assert!(p.note.as_deref().unwrap().starts_with("Accepted version 2. Swarm confirmed it"), "{:?}", p.note);
        assert!(p.note.as_deref().unwrap().contains("forge did not answer"));
        let shown = p.review.as_ref().unwrap();
        assert_eq!((shown.decision, shown.run_status.as_str()), (Decision::Reload, "unknown"));
        assert!(r.decide(true, "").is_err(), "nothing to accept until read again");

        let reread = r.reload().unwrap();
        r.answer(read_op(&reread), &target(), Answer::Read(Ok(review(2))));
        let (op, _) = decide_op(&r.decide(true, "").unwrap());
        r.answer(op, &target(), Answer::Decided(Decided::Denied { status: 403, error: "linked work on forge is off".into() }));
        let p = r.panel.as_ref().unwrap();
        assert_eq!(p.error.as_deref(), Some("Refused (HTTP 403): linked work on forge is off Nothing was done."));
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::Reload);
    }

    // Purpose: a read or decision answers only the panel and request that
    // asked. Requirements: (1) review A's read answering after B opened
    // changes nothing in B; (2) an older read of the same panel answering
    // after a newer one is ignored; (3) A's decision answering after the
    // person closed A and opened B leaves B untouched; (4) reopening A while
    // A's decision is on its way refuses a second decision on A until that
    // answer comes, and the reopened panel then reads A again instead of
    // taking the old panel's answer; (5) revise follows the same rule.
    // Threat (review of 62ffb10): answers applied to whichever panel is open,
    // and a global accept flag shared between panels. Owner: PlanReviews.
    // Narrowest layer: the pure request/answer state machine.
    #[test]
    fn answers_go_only_to_the_panel_that_asked() {
        let (a, b) = (target_for("task-a"), target_for("task-b"));
        let mut r = PlanReviews::default();

        // (1) A's read arrives after B opened.
        let read_a = read_op(&r.open(a.clone(), "A".into()));
        let read_b = read_op(&r.open(b.clone(), "B".into()));
        assert!(r.answer(read_a, &a, Answer::Read(Ok(review_for("task-a", 7, "pending_approval", "accept_plan")))).is_none());
        let p = r.panel.as_ref().unwrap();
        assert_eq!((p.target.task_id.as_ref(), p.review.is_none(), p.loading()), ("task-b", true, true), "A's read is not B's");
        r.answer(read_b, &b, Answer::Read(Ok(review_for("task-b", 2, "pending_approval", "accept_plan"))));
        assert_eq!(r.panel.as_ref().unwrap().review.as_ref().unwrap().binding.as_ref().unwrap().revision, 2);

        // (2) Out-of-order reads of one panel: the older answer is dropped.
        let older = read_op(&r.reload().unwrap());
        let newer = read_op(&r.reload().unwrap());
        r.answer(newer, &b, Answer::Read(Ok(review_for("task-b", 4, "pending_approval", "accept_plan"))));
        r.answer(older, &b, Answer::Read(Ok(review_for("task-b", 3, "pending_approval", "accept_plan"))));
        assert_eq!(r.panel.as_ref().unwrap().review.as_ref().unwrap().binding.as_ref().unwrap().revision, 4, "the older read never replaces the newer");

        // (3) Accept A, close, open B; A's answer arrives.
        let read_a = read_op(&r.open(a.clone(), "A".into()));
        r.answer(read_a, &a, Answer::Read(Ok(review_for("task-a", 7, "pending_approval", "accept_plan"))));
        let (accept_a, body) = decide_op(&r.decide(true, "").unwrap());
        assert_eq!((body["task_id"].as_str(), body["plan_binding"]["definition_revision"].as_u64()), (Some("task-a"), Some(7)));
        r.close();
        let read_b = read_op(&r.open(b.clone(), "B".into()));
        r.answer(read_b, &b, Answer::Read(Ok(review_for("task-b", 4, "pending_approval", "accept_plan"))));
        let before = r.panel.clone();
        assert!(r.answer(accept_a, &a, accepted("task-a", 7)).is_none());
        assert_eq!(r.panel, before, "A's decision changes nothing in B");
        // B's own decision is not blocked by A's.
        let (accept_b, body) = decide_op(&r.decide(true, "").unwrap());
        assert_eq!((body["task_id"].as_str(), body["plan_binding"]["definition_revision"].as_u64()), (Some("task-b"), Some(4)));
        r.answer(accept_b, &b, accepted("task-b", 4));
        assert!(r.panel.as_ref().unwrap().note.as_deref().unwrap().starts_with("Accepted version 4."));

        // (4) Reopen A while A's decision is on its way.
        let read_a = read_op(&r.open(a.clone(), "A".into()));
        r.answer(read_a, &a, Answer::Read(Ok(review_for("task-a", 8, "pending_approval", "accept_plan"))));
        let (accept_a, _) = decide_op(&r.decide(true, "").unwrap());
        r.close();
        let read_a2 = read_op(&r.open(a.clone(), "A".into()));
        r.answer(read_a2, &a, Answer::Read(Ok(review_for("task-a", 8, "pending_approval", "accept_plan"))));
        assert!(r.task_deciding());
        assert_eq!(r.decide(true, "").unwrap_err(), "A decision on this task is on its way; wait for Swarm's answer.", "no second decision on A");
        let follow = r.answer(accept_a, &a, accepted("task-a", 8)).expect("the reopened panel reads A again");
        assert!(matches!(&follow, Send::Read { target, .. } if *target == a));
        let p = r.panel.as_ref().unwrap();
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::Reload, "the old panel's answer is not shown as this one's");
        assert!(!r.task_deciding());
        r.answer(read_op(&follow), &a, Answer::Read(Ok(review_for("task-a", 8, "in_progress", "none"))));
        assert_eq!(r.panel.as_ref().unwrap().review.as_ref().unwrap().decision, Decision::None);

        // (5) Revise: same binding, same rule.
        let read_b = read_op(&r.open(b.clone(), "B".into()));
        r.answer(read_b, &b, Answer::Read(Ok(review_for("task-b", 5, "pending_approval", "accept_plan"))));
        assert!(r.decide(false, "  ").is_err(), "revise needs what to change");
        let (revise_b, body) = decide_op(&r.decide(false, "split step two").unwrap());
        assert_eq!((body["decision"].as_str(), body["plan_binding"]["definition_revision"].as_u64()), (Some("revise"), Some(5)));
        r.close();
        assert!(
            r.answer(
                revise_b,
                &b,
                Answer::Decided(Decided::Done { binding: None, review: plan_review::parse(&review_for("task-b", 5, "planning", "none")), refresh_error: String::new() })
            )
            .is_none()
        );
        assert!(r.panel.is_none(), "a closed panel is not reopened by its answer");
        assert!(!r.deciding.contains_key(&task_key(&b)), "the answered decision no longer blocks B");
    }
}
