//! Left navigation: the page list, and the collapsed icon rail.

use gpui::{ClickEvent, Context, Div, Hsla, SharedString, div, prelude::*, px};

use super::{AgentsFocus, Console, Open, Page, alpha};
use crate::icons::Icon;
use crate::data::Health;

impl Console {
    pub(super) fn needs_you_total(&self) -> usize {
        self.sessions
            .iter()
            .map(|s| s.tasks.iter().filter(|x| x.status.needs_you()).count())
            .sum()
    }

    /// The small figure beside a page: warnings in the status color, counts quiet.
    fn nav_badge(&self, page: Page) -> Option<(String, Hsla, bool)> {
        let t = &self.theme;
        match page {
            Page::Sessions => {
                let needs = self.needs_you_total();
                (needs > 0).then(|| (needs.to_string(), t.warn, true))
            }
            Page::Media => {
                let pending = self.media.iter().filter(|m| m.pending).count();
                Some(if pending > 0 {
                    (format!("{pending} new"), t.info, false)
                } else {
                    (self.media.len().to_string(), t.faint, false)
                })
            }
            Page::Usage => {
                let (today, unknown) = crate::data::spend_today(&self.machines);
                Some((format!("${today:.2}{}", if unknown { "+?" } else { "" }), t.faint, false))
            }
            Page::Fleet => {
                let waiting = crate::swarm::needs::Count::of(self.needs.iter().map(crate::swarm::needs::Reading::waiting));
                if waiting.known > 0 {
                    return Some((format!("{} waiting", waiting.text()), t.warn, true));
                }
                let online = self.machines.iter().filter(|m| m.health != Health::Offline).count();
                Some((format!("{online}/{}", self.machines.len()), t.faint, false))
            }
            Page::Agents => None,
            Page::Automations => match &self.automations {
                Some(Ok(b)) => {
                    let failed = Console::failing(b).len();
                    let running = b.runs.iter().filter(|r| r.active()).count();
                    let waiting = b.workers.iter().filter(|w| w.waiting()).count();
                    if waiting > 0 {
                        Some((format!("{waiting} waiting"), t.warn, true))
                    } else if failed > 0 {
                        Some((format!("{failed} failed"), t.err, true))
                    } else if running > 0 {
                        Some((format!("{running} running"), t.accent, true))
                    } else {
                        None
                    }
                }
                _ => None,
            },
            Page::Settings | Page::Permissions => None,
            Page::Search | Page::Projects => None,
        }
    }

    pub(super) fn render_nav(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let hover = t.hover;
        div().px_1().flex().flex_col().gap(px(1.)).children(Page::ALL.into_iter().map(|page| {
            let on = page == self.page;
            div()
                .id(SharedString::from(format!("nav-{}", page.label())))
                .px_2()
                .py(px(5.))
                .rounded(px(t.radius))
                .cursor_pointer()
                .flex()
                .items_center()
                .gap_2()
                .when(on, |d| d.bg(t.raised))
                .hover(move |s| s.bg(hover))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.go(page, cx)))
                .child(self.icon(page.icon(), 14., if on { t.accent } else { t.faint }))
                .child(div().flex_1().text_color(if on { t.text } else { t.muted }).child(page.label()))
                .children(self.nav_badge(page).map(|(label, color, strong)| {
                    div()
                        .px(px(5.))
                        .rounded(px(t.radius))
                        .when(strong, |d| d.bg(alpha(color, 0.16)))
                        .text_size(px(10.5))
                        .text_color(color)
                        .child(label)
                }))
        }))
    }

    pub(super) fn render_rail(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let hover = t.hover;
        let square = |id: SharedString| {
            div()
                .id(id)
                .w(px(32.))
                .h(px(30.))
                .flex()
                .items_center()
                .justify_center()
                .rounded(px(t.radius))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
        };

        let nav = Page::ALL.into_iter().map(|page| {
            let on = page == self.page;
            let flagged = self.nav_badge(page).is_some_and(|(_, _, strong)| strong);
            square(SharedString::from(format!("rail-{}", page.label())))
                .relative()
                .when(on, |d| d.bg(t.raised))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.go(page, cx)))
                .child(self.icon(page.icon(), 15., if on { t.accent } else { t.muted }))
                .when(flagged, |d| {
                    d.child(
                        div()
                            .absolute()
                            .top(px(5.))
                            .right(px(5.))
                            .size(px(5.))
                            .rounded(px(3.))
                            .bg(t.warn),
                    )
                })
        });

        let project = &self.projects[self.project];
        let initial = project.name.chars().next().unwrap_or('?').to_ascii_uppercase().to_string();
        let sessions = self
            .sessions
            .iter()
            .enumerate()
            .filter(|(_, s)| s.project == self.project)
            .map(|(i, s)| {
                let (glyph, color) = self.state_glyph(s.state);
                let selected = i == self.active_session() && self.page == Page::Sessions;
                square(SharedString::from(format!("rail-session-{i}")))
                    .h(px(24.))
                    .when(selected, |d| d.bg(t.raised))
                    .text_color(color)
                    .text_size(px(11.))
                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        this.open(i, Open::Here);
                        this.page = Page::Sessions;
                        cx.notify();
                    }))
                    .child(glyph)
            });

        let fleet = self.machines.iter().enumerate().map(|(mi, m)| {
            square(SharedString::from(format!("rail-fleet-{mi}")))
                .h(px(18.))
                .text_size(px(9.))
                .text_color(self.health_color(m.health))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.page = Page::Fleet;
                    this.agents_focus = AgentsFocus::Machine(mi);
                    cx.notify();
                }))
                .child("●")
        });

        div()
            .w(px(48.))
            .flex_none()
            .h_full()
            .flex()
            .flex_col()
            .items_center()
            .gap_1()
            .py_2()
            .bg(t.panel)
            .border_r_1()
            .border_color(t.border)
            .child(
                square("expand-left".into())
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.left_collapsed = false;
                        cx.notify();
                    }))
                    .child(self.icon(Icon::Expand, 14., t.faint)),
            )
            .child(div().w(px(24.)).h(px(1.)).my_1().bg(t.border))
            .children(nav)
            .child(div().w(px(24.)).h(px(1.)).my_1().bg(t.border))
            .child(
                square("rail-project".into())
                    .border_1()
                    .border_color(alpha(t.accent, 0.5))
                    .text_color(t.accent)
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.left_collapsed = false;
                        this.project_menu = true;
                        cx.notify();
                    }))
                    .child(initial),
            )
            .children(sessions)
            .child(div().flex_1())
            .children(fleet)
    }
}
