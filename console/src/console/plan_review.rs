//! The plan review panel: a waiting task's complete plan, where it is and
//! its exact version, with the person's decision (swarm::plan_review).
//!
//! It opens from a task card in a conversation or in the Companion's work,
//! for a task on this Swarm or on a linked machine. Accept names the version
//! shown; when Swarm refuses because the plan changed or the task moved, the
//! panel shows Swarm's current review and the person decides again. A
//! successful decision closes nothing by itself: the panel shows what Swarm
//! says now (started, revising) and the card follows Swarm's records.

use gpui::{AnyElement, ClickEvent, Context, SharedString, div, prelude::*, px};
use serde_json::Value;

use super::{Console, alpha};
use crate::data::{ReviewRef, Text};
use crate::swarm::live::Command;
use crate::swarm::plan_review::{self, Decided, Decision, Review};

/// Selection keys for the plan's text, apart from every pane's.
const REVIEW_PANE: usize = usize::MAX - 3;

/// What the panel holds: the task it is for and Swarm's last answer.
#[derive(Clone, Debug, PartialEq)]
pub(super) struct PlanReviewPanel {
    pub target: ReviewRef,
    /// The card's title, until Swarm's review names the task.
    pub title: Text,
    pub review: Option<Review>,
    pub loading: bool,
    pub deciding: bool,
    /// Swarm's refusal or a read that failed, in its words.
    pub error: Option<String>,
    /// What Swarm did with the decision.
    pub note: Option<String>,
    /// The person is writing what to change.
    pub revising: bool,
}

impl PlanReviewPanel {
    pub fn open(target: ReviewRef, title: Text) -> PlanReviewPanel {
        PlanReviewPanel { target, title, review: None, loading: true, deciding: false, error: None, note: None, revising: false }
    }

    /// A review read came back. A read for another task is ignored.
    pub fn read(&mut self, result: Result<Value, String>) {
        self.loading = false;
        match result {
            Ok(answer) => match plan_review::parse(&answer) {
                Some(review) if review.task_id == self.target.task_id.as_ref() && review.machine == self.target.machine.as_ref() => {
                    self.review = Some(review);
                    self.error = None;
                }
                Some(_) => {}
                None => self.error = Some("Swarm answered no review for this task.".into()),
            },
            // The last review stays, marked as not current.
            Err(e) => self.error = Some(e),
        }
    }

    /// A decision's answer. Accepted: Swarm's review after it (the task
    /// started). Refused: Swarm's reason and its current review, which the
    /// person reads and decides on again; the old version is never kept as
    /// decidable once Swarm named a new one. Failed without an answer: the
    /// review stays and must be read again before deciding.
    pub fn decided(&mut self, result: Result<Value, String>, accepted: bool) {
        self.deciding = false;
        match result {
            Ok(answer) => {
                let Decided { ok, error, review } = plan_review::decided(&answer);
                if let Some(review) = review {
                    self.review = Some(review);
                }
                if ok {
                    self.error = None;
                    self.revising = false;
                    let now = self.review.as_ref().map(|r| r.run_status.replace('_', " ")).unwrap_or_default();
                    self.note = Some(if accepted {
                        format!("Accepted. The task is {now} now.")
                    } else {
                        format!("Sent back for changes. The task is {now} now; a new version comes for review.")
                    });
                } else {
                    self.note = None;
                    self.error = Some(if error.is_empty() { "Swarm refused the decision.".into() } else { error });
                }
            }
            Err(e) => {
                self.note = None;
                self.error = Some(format!("{e} Read the plan again before deciding."));
                if let Some(review) = self.review.as_mut() {
                    review.decision = Decision::Reload;
                    review.problem = "The decision did not reach Swarm; read the plan again.".into();
                }
            }
        }
    }
}

impl Console {
    /// Opens a task's review and reads it from Swarm.
    pub(super) fn open_plan_review(&mut self, target: ReviewRef, title: Text, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        if let Some(input) = &self.plan_feedback {
            input.update(cx, |input, cx| input.set_text("", cx));
        }
        live.run(Command::ReadPlanReview { target: target.clone() });
        self.plan_review = Some(PlanReviewPanel::open(target, title));
        cx.notify();
    }

    fn reload_plan_review(&mut self, cx: &mut Context<Self>) {
        let (Some(live), Some(panel)) = (&self.live, self.plan_review.as_mut()) else { return };
        panel.loading = true;
        live.run(Command::ReadPlanReview { target: panel.target.clone() });
        cx.notify();
    }

    /// The person's decision: accept what is shown, or send it back with
    /// what to change. Refused here when nothing current can be decided.
    fn decide_plan(&mut self, accept: bool, cx: &mut Context<Self>) {
        let feedback = self.plan_feedback.as_ref().map(|i| i.read(cx).text()).unwrap_or_default();
        let (Some(live), Some(panel)) = (&self.live, self.plan_review.as_mut()) else { return };
        if panel.deciding || panel.loading {
            return;
        }
        let Some(review) = panel.review.as_ref() else { return };
        match plan_review::decision_body(review, accept, &feedback) {
            Ok(body) => {
                panel.deciding = true;
                panel.error = None;
                panel.note = None;
                self.plan_accepting = accept;
                live.run(Command::DecidePlan { body });
            }
            Err(why) => panel.error = Some(why),
        }
        cx.notify();
    }

    pub(super) fn plan_review_done(&mut self, what: &str, result: Result<Value, String>) {
        let Some(panel) = self.plan_review.as_mut() else { return };
        if what == "plan review" {
            panel.read(result);
        } else {
            panel.decided(result, self.plan_accepting);
        }
    }

    /// The panel over the window, while a review is open.
    pub(super) fn render_plan_review(&self, cx: &mut Context<Self>) -> Option<AnyElement> {
        let panel = self.plan_review.as_ref()?;
        let t = &self.theme;
        let close = self.button("plan-review-close", "close", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
            this.plan_review = None;
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
        if panel.loading && review.is_none() {
            body = body.child(div().text_color(t.muted).child("Reading the plan from Swarm…"));
        }
        if let Some(r) = review {
            match r.decision {
                Decision::AcceptPlan | Decision::None if !r.markdown.trim().is_empty() => {
                    body = body.child(self.markdown(&r.markdown, REVIEW_PANE, 0, cx));
                }
                Decision::Start => {
                    body = body.child(div().text_color(t.text).line_height(px(19.)).child("This task has no plan to accept: it waits to start."));
                }
                _ => {}
            }
            if !r.problem.is_empty() {
                body = body.child(div().text_size(px(11.5)).text_color(t.warn).child(r.problem.clone()));
            }
            if !r.last_error.is_empty() {
                body = body.child(div().text_size(px(11.5)).text_color(t.muted).child(format!("last error: {}", r.last_error)));
            }
        }
        let decision = review.map(|r| r.decision).unwrap_or(Decision::Reload);
        let busy = panel.deciding || panel.loading;
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
                if let Some(p) = this.plan_review.as_mut() {
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
                            if let Some(p) = this.plan_review.as_mut() {
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
                                .when(panel.deciding, |d| d.child(div().text_size(px(11.)).text_color(t.muted).child("Sending your decision…")))
                                .when(panel.loading && review.is_some(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child("Reading the current version…")))
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

    fn review(revision: u64) -> Value {
        json!({"review": {"machine": "forge", "project_id": "rp", "task_id": "rt", "title": "Remote plan", "run_status": "pending_approval", "plan_bound": true,
            "plan_binding": {"session_id": "s", "plan_id": "plan-a", "definition_revision": revision}, "plan_document": {"title": "Plan"},
            "plan_markdown": format!("# Plan v{revision}\n"), "decision": "accept_plan"}})
    }

    fn target() -> ReviewRef {
        ReviewRef { machine: "forge".into(), project_id: "rp".into(), task_id: "rt".into(), plan: false }
    }

    // Purpose: the panel keeps the person on a current, readable plan. A
    // refusal because the plan changed shows Swarm's new version (and its
    // reason) for a new decision; a decision that never reached Swarm
    // leaves nothing decidable until the plan is read again; an accepted one
    // shows Swarm's state after it. A read for another task never replaces
    // the shown one. Threat: accepting an old version after a refusal, or a
    // stale panel looking decidable after a lost answer. Owner:
    // PlanReviewPanel::read and ::decided. Narrowest layer: the panel state.
    #[test]
    fn a_refused_decision_shows_the_current_version() {
        let mut p = PlanReviewPanel::open(target(), "Remote plan".into());
        p.read(Ok(review(2)));
        assert!(!p.loading);
        assert_eq!(p.review.as_ref().unwrap().binding.as_ref().unwrap().revision, 2);

        // Another task's answer is not this panel's.
        let mut other = review(9);
        other["review"]["task_id"] = json!("another");
        p.read(Ok(other));
        assert_eq!(p.review.as_ref().unwrap().binding.as_ref().unwrap().revision, 2);

        // 409: the plan changed. The new version shows with Swarm's reason.
        p.deciding = true;
        let mut refused = review(3);
        refused["ok"] = json!(false);
        refused["error"] = json!("the plan changed (current definition_revision 3)");
        p.decided(Ok(refused), true);
        assert!(!p.deciding);
        assert_eq!(p.error.as_deref(), Some("the plan changed (current definition_revision 3)"));
        let shown = p.review.as_ref().unwrap();
        assert_eq!((shown.binding.as_ref().unwrap().revision, shown.decision), (3, Decision::AcceptPlan));
        assert_eq!(plan_review::decision_body(shown, true, "").unwrap()["plan_binding"]["definition_revision"], 3, "the next accept names the version now shown");

        // Lost answer: nothing is decidable until read again.
        p.decided(Err("Swarm is not answering.".into()), true);
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::Reload);
        assert!(plan_review::decision_body(p.review.as_ref().unwrap(), true, "").is_err());
        assert!(p.error.as_deref().unwrap().contains("Read the plan again"));

        // Read again, then accepted: Swarm's state after it.
        p.read(Ok(review(3)));
        let mut accepted = review(3);
        accepted["ok"] = json!(true);
        accepted["review"]["run_status"] = json!("in_progress");
        accepted["review"]["decision"] = json!("none");
        p.decided(Ok(accepted), true);
        assert_eq!(p.error, None);
        assert_eq!(p.note.as_deref(), Some("Accepted. The task is in progress now."));
        assert_eq!(p.review.as_ref().unwrap().decision, Decision::None, "a second click has nothing to accept");
    }
}
