//! Search: every conversation Swarm has, active or archived, by words in
//! their titles and messages (POST /v3/sessions:search), as you type. An
//! archived one can come back (POST /v3/sessions:unarchive); an active one
//! opens in a pane.

use gpui::{AnyElement, ClickEvent, Context, Focusable, SharedString, div, prelude::*, px};
use serde_json::json;

use super::{Console, Open, Page, SearchRun, ago, alpha, now_ms};
use crate::swarm::live::Command;
use crate::swarm::map;

/// Which conversations the search covers.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(super) enum SearchMode {
    Active,
    Archived,
    All,
}

impl SearchMode {
    const ALL: [SearchMode; 3] = [SearchMode::Active, SearchMode::Archived, SearchMode::All];

    fn label(self) -> &'static str {
        match self {
            SearchMode::Active => "active",
            SearchMode::Archived => "archived",
            SearchMode::All => "all",
        }
    }

    /// Swarm's archived_mode for it.
    fn wire(self) -> &'static str {
        match self {
            SearchMode::Active => "exclude",
            SearchMode::Archived => "only",
            SearchMode::All => "include",
        }
    }
}

impl Console {
    /// Opens the Search page with the field focused; an empty field lists
    /// the newest conversations.
    pub(super) fn open_search(&mut self, window: &mut gpui::Window, cx: &mut Context<Self>) {
        self.page = Page::Search;
        if let Some(input) = &self.search_input {
            window.focus(&input.focus_handle(cx));
        }
        self.run_search(cx);
        cx.notify();
    }

    pub(super) fn go_search(&mut self, _: &super::GoSearch, window: &mut gpui::Window, cx: &mut Context<Self>) {
        self.open_search(window, cx);
    }

    pub(super) fn search_run(&mut self, _: &SearchRun, _: &mut gpui::Window, cx: &mut Context<Self>) {
        self.run_search(cx);
    }

    /// Asks Swarm, with the field's words and the mode; an older answer that
    /// arrives after a newer question is dropped.
    pub(super) fn run_search(&mut self, cx: &mut Context<Self>) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        let query = self.search_input.as_ref().map(|i| i.read(cx).text().trim().to_string()).unwrap_or_default();
        let mode = self.search_mode;
        self.search_seq += 1;
        let seq = self.search_seq;
        self.search_busy = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move {
                    let mut body = json!({"archived_mode": mode.wire(), "global": true, "limit": 50});
                    if !query.is_empty() {
                        body["query"] = json!(query);
                    }
                    client.post("/v3/sessions:search", body).map_err(|e| e.to_string())
                })
                .await;
            this.update(cx, |this, cx| {
                if seq != this.search_seq {
                    return;
                }
                this.search_busy = false;
                match result {
                    Ok(answer) => {
                        this.search_results = map::found(&answer, &this.projects);
                        this.search_error = None;
                    }
                    Err(e) => this.search_error = Some(e),
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn set_search_mode(&mut self, mode: SearchMode, cx: &mut Context<Self>) {
        if self.search_mode != mode {
            self.search_mode = mode;
            self.run_search(cx);
        }
    }

    /// Opens a found conversation: one the sidebar has opens at once; one it
    /// does not (older than the recent list) is read from Swarm first, then
    /// opened when it arrives.
    fn open_found(&mut self, id: String, cx: &mut Context<Self>) {
        if let Some(i) = self.sessions.iter().position(|s| s.live.as_ref().is_some_and(|l| l.session_id.as_ref() == id)) {
            self.open(i, Open::Here);
            self.page = Page::Sessions;
        } else if let Some(live) = &self.live {
            self.pending_open = Some(id.clone());
            live.run(Command::Reveal { session_id: id });
        }
        cx.notify();
    }

    /// After a snapshot: a conversation asked for from Search opens once it
    /// is in the list.
    pub(super) fn open_pending(&mut self) {
        let Some(id) = self.pending_open.clone() else { return };
        if let Some(i) = self.sessions.iter().position(|s| s.live.as_ref().is_some_and(|l| l.session_id.as_ref() == id)) {
            self.pending_open = None;
            self.open(i, Open::Here);
            self.page = Page::Sessions;
        }
    }

    fn unarchive_found(&mut self, id: String, project_id: String, cx: &mut Context<Self>) {
        let Some(live) = &self.live else { return };
        self.notice = Some(("Bringing it back…".into(), false));
        live.run(Command::Unarchive { session_id: id, project_id });
        cx.notify();
    }

    pub(super) fn render_search(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;
        let hover = t.hover;
        let field = div()
            .key_context("Search")
            .on_action(cx.listener(Self::search_run))
            .flex_1()
            .min_w_0()
            .flex()
            .items_center()
            .gap_2()
            .px_3()
            .py(px(6.))
            .rounded(px(t.radius))
            .border_1()
            .border_color(alpha(t.accent, 0.55))
            .bg(t.surface)
            .text_color(t.text)
            .child(self.icon(crate::icons::Icon::Search, 13., t.faint))
            .children(self.search_input.clone());
        let modes = div().flex().gap_1().children(SearchMode::ALL.into_iter().map(|mode| {
            let on = mode == self.search_mode;
            div()
                .id(SharedString::from(format!("search-mode-{}", mode.label())))
                .px_2()
                .py(px(3.))
                .rounded(px(t.radius))
                .border_1()
                .border_color(if on { alpha(t.accent, 0.7) } else { t.border })
                .when(on, |d| d.bg(alpha(t.accent, 0.15)))
                .cursor_pointer()
                .hover(move |s| s.bg(hover))
                .text_size(px(11.))
                .text_color(if on { t.text } else { t.muted })
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.set_search_mode(mode, cx)))
                .child(mode.label())
        }));
        let n = self.search_results.len();
        let line = match (&self.search_error, self.search_busy, n) {
            (Some(e), _, _) => (format!("Swarm refused: {e}"), t.err),
            (None, true, _) => ("Asking Swarm…".to_string(), t.faint),
            (None, false, 0) => ("Nothing found.".to_string(), t.faint),
            (None, false, 1) => ("1 conversation".to_string(), t.faint),
            (None, false, n) => (format!("{n} conversations"), t.faint),
        };
        let rows: Vec<AnyElement> = self.search_results.iter().map(|f| self.render_found(f, cx)).collect();
        div()
            .id("search-page")
            .size_full()
            .flex()
            .flex_col()
            .p_5()
            .gap_3()
            .max_w(px(820.))
            .child(div().text_size(px(16.)).text_color(t.text).child("Search"))
            .child(div().flex().items_center().gap_2().child(field).child(modes))
            .child(div().text_size(px(11.)).text_color(line.1).child(line.0))
            .child(
                div()
                    .id("search-results")
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .flex()
                    .flex_col()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .children(rows),
            )
    }

    /// One found conversation: title, where and when, the matching words,
    /// and what you can do with it.
    fn render_found(&self, f: &crate::data::Found, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let hover = t.hover;
        let id = f.id.to_string();
        let mut meta: Vec<String> = Vec::new();
        if !f.project.is_empty() {
            meta.push(f.project.to_string());
        }
        if f.updated_at > 0 {
            meta.push(ago(now_ms().saturating_sub(f.updated_at)));
        }
        let action = if f.archived {
            let (id, project_id) = (id.clone(), f.project_id.to_string());
            self.button(SharedString::from(format!("found-{}-unarchive", f.id)), "unarchive", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                cx.stop_propagation();
                this.unarchive_found(id.clone(), project_id.clone(), cx);
            }))
        } else {
            let id = id.clone();
            self.button(SharedString::from(format!("found-{}-open", f.id)), "open", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                cx.stop_propagation();
                this.open_found(id.clone(), cx);
            }))
        };
        let archived = f.archived;
        let open_id = id.clone();
        div()
            .id(SharedString::from(format!("found-{}", f.id)))
            .w_full()
            .flex()
            .items_center()
            .gap_3()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(alpha(t.border, 0.7))
            .when(!archived, |d| d.cursor_pointer().hover(move |s| s.bg(hover)))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                if !archived {
                    this.open_found(open_id.clone(), cx);
                }
            }))
            .child(div().text_color(if archived { t.faint } else { t.muted }).child(if archived { "▫" } else { "○" }))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(
                        div()
                            .flex()
                            .items_center()
                            .gap_2()
                            .child(div().truncate().text_color(if archived { t.muted } else { t.text }).child(f.title.clone()))
                            .when(archived, |d| d.child(div().text_size(px(10.5)).text_color(t.warn).child("archived")))
                            .child(div().text_size(px(10.5)).text_color(t.faint).child(meta.join(" · "))),
                    )
                    .when(!f.snippet.is_empty(), |d| d.child(div().truncate().text_size(px(11.)).text_color(t.faint).child(f.snippet.clone()))),
            )
            .child(action)
            .into_any_element()
    }
}

impl Default for SearchMode {
    fn default() -> Self {
        SearchMode::Active
    }
}
