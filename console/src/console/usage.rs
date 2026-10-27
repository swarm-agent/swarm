//! Usage as Swarm records it: each sign-in plan's limits, tokens and spend
//! over 30 days, each day, each model and the sessions that used the most.
//!
//! Every chart is a single series (magnitude), so one hue does the work and
//! labels carry identity. Plan bars turn warn past 80% and err at the limit,
//! always beside their numbers.

use gpui::{AnyElement, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha, ago, now_ms};
use crate::data::{Plan, Tokens};
use crate::swarm::map::tokens;

/// "2h 14m", "4d 3h".
fn until(seconds: u64) -> String {
    let (d, h, m) = (seconds / 86_400, seconds % 86_400 / 3_600, seconds % 3_600 / 60);
    match (d, h) {
        (0, 0) => format!("{m}m"),
        (0, h) => format!("{h}h {m}m"),
        (d, h) => format!("{d}d {h}h"),
    }
}

fn split(t: &Tokens) -> String {
    let mut parts = vec![format!("in {}", tokens(t.input)), format!("out {}", tokens(t.output))];
    if t.cached > 0. {
        parts.push(format!("cached {}", tokens(t.cached)));
    }
    if t.thinking > 0. {
        parts.push(format!("thinking {}", tokens(t.thinking)));
    }
    parts.join(" · ")
}

impl Console {
    fn card(&self) -> Div {
        let t = &self.theme;
        div()
            .min_w_0()
            .flex_shrink_0()
            .p_3()
            .flex()
            .flex_col()
            .gap_1()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(t.border)
            .bg(t.surface)
            .overflow_hidden()
    }

    fn caption(&self, text: impl Into<SharedString>) -> Div {
        div().text_size(px(10.5)).text_color(self.theme.faint).child(text.into())
    }

    fn stat(&self, label: &'static str, value: String, detail: String) -> Div {
        let t = &self.theme;
        self.card()
            .flex_1()
            .min_w(px(170.))
            .child(self.caption(label))
            .child(div().text_size(px(20.)).text_color(t.text).truncate().child(value))
            .child(div().text_size(px(11.)).text_color(t.faint).truncate().child(detail))
    }

    /// One sign-in plan: each limit window as a bar with what is used and when it resets.
    fn plan_card(&self, plan: &Plan) -> Div {
        let t = &self.theme;
        let now = now_ms() / 1000;
        let title = if plan.kind.is_empty() { plan.name.to_uppercase() } else { format!("{} · {}", plan.name.to_uppercase(), plan.kind.to_uppercase()) };
        let card = self.card().flex_1().min_w(px(260.)).gap_2().child(
            div()
                .flex()
                .items_center()
                .gap_2()
                .child(div().flex_1().min_w_0().child(self.caption(title)))
                .when(plan.limit_reached, |d| d.child(div().text_size(px(10.5)).text_color(t.err).child("limit reached")))
                .when(!plan.connected, |d| d.child(div().text_size(px(10.5)).text_color(t.faint).child("not connected"))),
        );
        if plan.windows.is_empty() {
            let note = if plan.note.is_empty() { "No reading yet.".into() } else { plan.note.clone() };
            return card.child(div().text_size(px(11.)).text_color(t.faint).line_clamp(2).child(note));
        }
        card.children(plan.windows.iter().map(|w| {
            let color = if plan.limit_reached || w.used >= 100. {
                t.err
            } else if w.used >= 80. {
                t.warn
            } else {
                t.accent
            };
            div()
                .flex()
                .items_center()
                .gap_2()
                .text_size(px(11.5))
                .child(div().w(px(56.)).flex_none().text_color(t.muted).child(w.label.clone()))
                .child(
                    div().flex_1().min_w(px(40.)).h(px(6.)).rounded(px(3.)).bg(t.border).child(
                        div().h_full().w(gpui::relative((w.used / 100.).clamp(0.01, 1.))).rounded(px(3.)).bg(color),
                    ),
                )
                .child(div().w(px(64.)).flex_none().text_right().text_color(t.text).child(format!("{:.0}% used", w.used)))
                .child(div().w(px(124.)).flex_none().text_right().text_color(t.faint).child(if w.resets_at > now {
                    format!("resets in {}", until(w.resets_at - now))
                } else {
                    String::new()
                }))
        }))
    }

    pub(super) fn render_usage(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let u = &self.usage;
        let (ok, failed, running) = u.runs_today;

        // Tokens per day over 30 days; days without use are empty slots.
        const DAYS: usize = 30;
        let today = now_ms() / 86_400_000;
        let mut slots: Vec<Option<usize>> = vec![None; DAYS];
        for (i, d) in u.daily.iter().enumerate() {
            let back = today.saturating_sub(d.at / 86_400_000) as usize;
            if d.at > 0 && back < DAYS {
                slots[DAYS - 1 - back] = Some(i);
            }
        }
        let chart_h = 120.;
        let top = u.daily.iter().map(|d| d.tokens).fold(1., f64::max);
        let shown = self.usage_hover.and_then(|i| slots.get(i).copied().flatten()).or_else(|| slots.iter().rev().flatten().next().copied());
        let columns = slots.iter().enumerate().map(|(i, slot)| {
            let on = slot.is_some() && slot == &shown;
            let h = slot.map(|d| (chart_h * u.daily[d].tokens / top) as f32).unwrap_or(0.);
            div()
                .id(SharedString::from(format!("day-{i}")))
                .flex_1()
                .h_full()
                .flex()
                .flex_col()
                .justify_end()
                .px(px(1.))
                .on_hover(cx.listener(move |this, hovered: &bool, _, cx| {
                    let next = if *hovered { Some(i) } else { None };
                    if this.usage_hover != next {
                        this.usage_hover = next;
                        cx.notify();
                    }
                }))
                .child(div().h(px(h.max(if slot.is_some() { 2. } else { 0. }))).rounded_t(px(4.)).bg(if on { t.accent } else { alpha(t.accent, 0.55) }))
        });
        let day_text = shown
            .map(|d| {
                let day = &u.daily[d];
                let cost = if day.cost > 0. { format!(" · ${:.2}", day.cost) } else { String::new() };
                format!("{} · {} tokens{cost}", day.date, tokens(day.tokens))
            })
            .unwrap_or_else(|| "no use yet".into());

        let spend_detail = if u.nominal > 0. {
            format!("${:.2} at API prices", u.nominal)
        } else if !u.cost_note.is_empty() {
            u.cost_note.to_string()
        } else {
            "billed spend".into()
        };
        let today_detail = {
            let mut parts = vec![];
            if u.today_cost > 0. {
                parts.push(format!("${:.2}", u.today_cost));
            }
            parts.push(match (u.daily_cost_limit > 0., u.daily_tokens_limit > 0.) {
                (true, _) => format!("limit ${:.0} a day", u.daily_cost_limit),
                (_, true) => format!("limit {} a day", tokens(u.daily_tokens_limit)),
                _ => "no daily limit".into(),
            });
            parts.join(" · ")
        };

        let model_max = u.models.iter().map(|m| m.tokens.total).fold(1., f64::max);
        let models: Vec<AnyElement> = u
            .models
            .iter()
            .map(|m| {
                let money = match (m.cost > 0., m.nominal > 0.) {
                    (true, _) => format!("${:.2}", m.cost),
                    (_, true) => format!("${:.2} api", m.nominal),
                    _ => "$0".into(),
                };
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .py(px(5.))
                    .border_t_1()
                    .border_color(t.border)
                    .text_size(px(11.5))
                    .child(
                        div()
                            .w(px(170.))
                            .flex_none()
                            .min_w_0()
                            .flex()
                            .flex_col()
                            .child(div().truncate().text_color(t.text).child(m.model.clone()))
                            .child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(m.billing.clone())),
                    )
                    .child(
                        div()
                            .flex_1()
                            .min_w(px(60.))
                            .flex()
                            .flex_col()
                            .gap_1()
                            .child(div().h(px(6.)).w(gpui::relative((m.tokens.total / model_max).max(0.01) as f32)).rounded_r(px(3.)).bg(t.accent))
                            .child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(split(&m.tokens))),
                    )
                    .child(div().w(px(56.)).flex_none().text_right().text_color(t.text).child(tokens(m.tokens.total)))
                    .child(div().w(px(180.)).flex_none().text_right().whitespace_nowrap().text_color(t.faint).child(format!("{} turns · {} sessions", m.turns, m.sessions)))
                    .child(div().w(px(76.)).flex_none().text_right().text_color(t.muted).child(money))
                    .into_any_element()
            })
            .collect();

        let sessions: Vec<AnyElement> = u
            .top_sessions
            .iter()
            .map(|s| {
                let now = now_ms();
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .py(px(4.))
                    .border_t_1()
                    .border_color(t.border)
                    .text_size(px(11.5))
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(s.title.clone()))
                    .child(div().w(px(130.)).flex_none().truncate().text_color(t.faint).child(s.model.clone()))
                    .child(div().w(px(56.)).flex_none().text_right().text_color(t.text).child(tokens(s.tokens)))
                    .child(div().w(px(64.)).flex_none().text_right().text_color(t.faint).child(format!("{} turns", s.turns)))
                    .child(div().w(px(40.)).flex_none().text_right().text_color(t.faint).child(if s.last_active > 0 { ago(now.saturating_sub(s.last_active)) } else { String::new() }))
                    .into_any_element()
            })
            .collect();

        let (images, videos, sounds, media_cost) = u.media;
        div()
            .id("usage-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_4()
            .child(
                div()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .child(div().text_size(px(16.)).text_color(t.text).child("Usage"))
                    .child(div().text_color(t.faint).child("The last 30 days as Swarm records them, and how much of each sign-in plan is used.")),
            )
            .when(!u.plans.is_empty(), |d| d.child(div().flex().flex_wrap().gap_3().children(u.plans.iter().map(|p| self.plan_card(p)))))
            .child(
                div()
                    .flex()
                    .flex_wrap()
                    .gap_3()
                    .child(self.stat("TOKENS · 30 DAYS", tokens(u.month.total), split(&u.month)))
                    .child(self.stat("TOKENS TODAY", tokens(u.today.total), today_detail))
                    .child(self.stat("SPEND · 30 DAYS", format!("${:.2}", u.cost), spend_detail))
                    .child(self.stat("TURNS · 30 DAYS", u.turns.to_string(), format!("{} sessions", u.sessions)))
                    .child(self.stat("RUNS TODAY", (ok + failed + running).to_string(), format!("{ok} done · {failed} failed · {running} running"))),
            )
            .child(
                self.card()
                    .gap_2()
                    .child(
                        div()
                            .flex()
                            .gap_3()
                            .child(div().flex_1().min_w_0().child(self.caption("TOKENS PER DAY · 30 DAYS")))
                            .child(div().text_size(px(10.5)).text_color(t.text).truncate().child(day_text)),
                    )
                    .child(div().h(px(chart_h as f32)).border_b_1().border_color(t.border_strong).flex().items_end().children(columns))
                    .child(div().flex().justify_between().text_size(px(10.5)).text_color(t.faint).child("30 days ago").child("today")),
            )
            .when(!models.is_empty(), |d| d.child(self.card().child(self.caption("MODELS · 30 DAYS")).children(models)))
            .when(!sessions.is_empty(), |d| d.child(self.card().child(self.caption("RECENT SESSIONS")).children(sessions)))
            .when(images + videos + sounds > 0, |d| {
                d.child(self.card().child(self.caption("MEDIA · 30 DAYS")).child(div().text_color(t.text).child(format!(
                    "{images} images · {videos} videos · {sounds} sounds · ${media_cost:.2}"
                ))))
            })
    }
}
