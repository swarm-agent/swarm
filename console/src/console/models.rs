//! Picking a model, in steps: the model (searched by name), then its
//! thinking, then its priority (the provider's service tier), then where it
//! applies: this chat only, a role's default for every chat, both, or saved
//! as a favorite. Favorites (Swarm's model profiles: a name, a model, its
//! thinking and priority) have their own view. Thinking and priority are
//! shown as metadata under a name, never as a row of buttons. Every change
//! goes to Swarm at once and the lists follow what Swarm says.

use gpui::{AnyElement, ClickEvent, Context, Div, Focusable, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::data::Assignment;
use crate::swarm::live::Command;
use crate::swarm::models::{Choice, Favorite, ModelOption};

/// What the picker was opened for.
#[derive(Clone, Debug, PartialEq)]
pub(super) enum PickerScope {
    /// The session in a pane: live (Swarm's model profile for this chat) or
    /// not started yet (it starts with the choice).
    Session { si: usize, pid: usize },
    /// A role's default (plan, action, system-coder, …), by index into roles.
    Role(usize),
}

#[derive(Clone, Debug, PartialEq)]
pub(super) enum Stage {
    Favorites,
    Model,
    Thinking,
    Priority,
    Apply,
}

#[derive(Clone, Debug, PartialEq)]
pub(super) struct Picker {
    pub scope: PickerScope,
    pub stage: Stage,
    /// One provider only, when chosen, in the model list.
    pub provider: Option<String>,
    pub model: Option<ModelOption>,
    pub thinking: Option<String>,
    pub tier: Option<String>,
    /// A favorite picked as is (applied by its id).
    pub favorite: Option<Favorite>,
    /// Making a favorite: the Apply step saves instead.
    pub saving: bool,
}

impl Picker {
    fn assignment(&self) -> Option<Assignment> {
        if let Some(f) = &self.favorite {
            return Some(f.assignment.clone());
        }
        let m = self.model.as_ref()?;
        Some(m.assignment(self.thinking.as_deref().unwrap_or(""), self.tier.as_deref().unwrap_or("")))
    }

    fn choice(&self) -> Option<Choice> {
        if let Some(f) = &self.favorite {
            return Some(Choice::Favorite(f.id.clone()));
        }
        self.assignment().map(Choice::Temporary)
    }
}

impl Console {
    pub(super) fn open_picker(&mut self, scope: PickerScope, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let same = self.picker.as_ref().is_some_and(|p| p.scope == scope);
        self.picker = if same {
            None
        } else {
            // Favorites first when there are any; the catalog otherwise.
            let stage = if self.catalog.favorites.is_empty() { Stage::Model } else { Stage::Favorites };
            Some(Picker { scope, stage, provider: None, model: None, thinking: None, tier: None, favorite: None, saving: false })
        };
        self.shield_open = None;
        if let Some(q) = &self.picker_query {
            q.update(cx, |q, cx| q.set_text("", cx));
            if self.picker.is_some() {
                window.focus(&q.focus_handle(cx));
            }
        }
        cx.notify();
    }

    pub(super) fn close_picker(&mut self, cx: &mut Context<Self>) {
        if self.picker.take().is_some() {
            cx.notify();
        }
    }

    fn picker_go(&mut self, stage: Stage, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let saving = self.picker.as_ref().is_some_and(|p| p.saving);
        if let Some(p) = self.picker.as_mut() {
            p.stage = stage.clone();
        }
        if let Some(q) = &self.picker_query {
            q.update(cx, |q, cx| q.set_text("", cx));
            if stage == Stage::Model {
                window.focus(&q.focus_handle(cx));
            }
        }
        if let Some(n) = &self.picker_name {
            if stage == Stage::Apply && saving {
                window.focus(&n.focus_handle(cx));
            }
        }
        cx.notify();
    }

    /// A model chosen: on to its thinking (or straight on when it has none).
    fn pick_model(&mut self, o: ModelOption, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(p) = self.picker.as_mut() else { return };
        p.favorite = None;
        p.thinking = None;
        p.tier = None;
        let next = if o.thinking_options.len() > 1 {
            Stage::Thinking
        } else {
            p.thinking = Some(o.thinking());
            if o.service_tiers.len() > 1 { Stage::Priority } else { Stage::Apply }
        };
        p.model = Some(o);
        self.picker_go(next, window, cx);
    }

    fn pick_thinking(&mut self, level: String, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(p) = self.picker.as_mut() else { return };
        p.thinking = Some(level);
        let next = if p.model.as_ref().is_some_and(|m| m.service_tiers.len() > 1) { Stage::Priority } else { Stage::Apply };
        self.picker_go(next, window, cx);
    }

    fn pick_tier(&mut self, tier: String, window: &mut gpui::Window, cx: &mut Context<Self>) {
        if let Some(p) = self.picker.as_mut() {
            p.tier = Some(tier);
        }
        self.picker_go(Stage::Apply, window, cx);
    }

    fn pick_favorite(&mut self, f: Favorite, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let model = self.catalog.find(&f.assignment.provider, &f.assignment.model).cloned();
        if let Some(p) = self.picker.as_mut() {
            p.model = model;
            p.thinking = Some(f.assignment.thinking.to_string());
            p.tier = Some(f.assignment.tier.to_string());
            p.favorite = Some(f);
            p.saving = false;
        }
        self.picker_go(Stage::Apply, window, cx);
    }

    /// Applies the choice to this chat.
    fn apply_to_chat(&mut self, si: usize) {
        let Some(p) = self.picker.clone() else { return };
        let (Some(choice), Some(a)) = (p.choice(), p.assignment()) else { return };
        match (&self.sessions[si].live, &self.live) {
            (Some(l), Some(live)) => {
                self.acting = Some(si);
                self.failure = None;
                live.run(Command::SetModel { session_id: l.session_id.to_string(), choice });
                // Shown at once; Swarm's own record follows on its event.
                self.sessions[si].model = a.short().into();
                self.notice = Some(("Setting the model for this chat…".into(), false));
            }
            (None, _) => {
                self.sessions[si].model = a.short().into();
                self.sessions[si].model_choice = Some(a);
                self.sessions[si].model_favorite = p.favorite.as_ref().map(|f| f.id.clone());
                self.notice = Some(("This chat starts with that model.".into(), false));
            }
            (Some(_), None) => {}
        }
    }

    /// Applies the choice as a role's default for every conversation.
    fn apply_to_role(&mut self, ri: usize) {
        let Some(p) = self.picker.clone() else { return };
        let Some(a) = p.assignment() else { return };
        if let (Some(role), Some(live)) = (self.roles.get(ri), &self.live) {
            live.run(Command::SetDefault { role: role.key.to_string(), model: a });
            self.notice = Some((format!("Setting the default for {}…", role.label).into(), false));
        }
    }

    fn apply_pick(&mut self, what: &'static str, cx: &mut Context<Self>) {
        let Some(p) = self.picker.clone() else { return };
        let plan = self.roles.iter().position(|r| r.key.as_ref() == "plan");
        match (&p.scope, what) {
            (PickerScope::Session { si, .. }, "chat") => self.apply_to_chat(*si),
            (PickerScope::Session { si, .. }, "both") => {
                self.apply_to_chat(*si);
                if let Some(ri) = plan {
                    self.apply_to_role(ri);
                }
            }
            (PickerScope::Session { .. }, "default") => {
                if let Some(ri) = plan {
                    self.apply_to_role(ri);
                }
            }
            (PickerScope::Role(ri), _) => self.apply_to_role(*ri),
            _ => {}
        }
        self.picker = None;
        cx.notify();
    }

    /// Saves the choice as a favorite, named in the search box.
    fn save_favorite(&mut self, window: &mut gpui::Window, cx: &mut Context<Self>) {
        let Some(p) = self.picker.clone() else { return };
        let Some(a) = p.assignment() else { return };
        let name = self.picker_name.as_ref().map(|q| q.read(cx).text().trim().to_string()).unwrap_or_default();
        let name = if name.is_empty() { a.short() } else { name };
        if let Some(live) = &self.live {
            live.run(Command::SaveFavorite { name, model: a });
            self.notice = Some(("Saving the favorite…".into(), false));
        }
        if let Some(p) = self.picker.as_mut() {
            p.saving = false;
            p.favorite = None;
        }
        self.picker_go(Stage::Favorites, window, cx);
    }

    fn remove_favorite(&mut self, id: String, cx: &mut Context<Self>) {
        if let Some(live) = &self.live {
            live.run(Command::RemoveFavorite { id: id.clone() });
            self.catalog.favorites.retain(|f| f.id != id);
            self.notice = Some(("Removing the favorite…".into(), false));
        }
        cx.notify();
    }

    /// The model in the composer's footer: click to pick one for this chat.
    pub(super) fn model_button(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let s = &self.sessions[si];
        let label = if !s.model.is_empty() {
            s.model.to_string()
        } else if s.live.is_none() {
            "default model".to_string()
        } else {
            String::new()
        };
        if label.is_empty() {
            return div().into_any_element();
        }
        let hover = t.hover;
        let open = self.picker.as_ref().is_some_and(|p| p.scope == PickerScope::Session { si, pid });
        div()
            .id(SharedString::from(format!("model-{pid}")))
            .flex()
            .flex_none()
            .items_center()
            .gap_1()
            .px_1()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .when(open, |d| d.bg(hover))
            .text_color(if s.model_choice.is_some() { t.accent } else { t.muted })
            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.open_picker(PickerScope::Session { si, pid }, window, cx)))
            .child("◆")
            .child(label)
            .into_any_element()
    }

    /// The picker for a pane's composer, above it while open.
    pub(super) fn render_model_picker_for_pane(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<Div> {
        let scope = PickerScope::Session { si, pid };
        if self.picker.as_ref().map(|p| &p.scope) != Some(&scope) {
            return None;
        }
        Some(self.render_picker(scope, cx))
    }

    /// The picker for a role, in the Agents inspector while open.
    pub(super) fn render_model_picker_for_role(&self, ri: usize, cx: &mut Context<Self>) -> Option<Div> {
        let scope = PickerScope::Role(ri);
        if self.picker.as_ref().map(|p| &p.scope) != Some(&scope) {
            return None;
        }
        Some(self.render_picker(scope, cx))
    }

    /// One selectable row: a title, metadata under it, a mark on the right.
    fn picker_row(&self, id: SharedString, title: String, meta: String, on: bool, note: Option<(&'static str, gpui::Hsla)>) -> gpui::Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        div()
            .id(id)
            .flex()
            .items_center()
            .gap_2()
            .px_2()
            .py(px(4.))
            .rounded(px(t.radius))
            .cursor_pointer()
            .when(on, |d| d.bg(alpha(t.accent, 0.1)))
            .hover(move |s| s.bg(hover))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(div().truncate().text_color(if on { t.text } else { t.muted }).child(title))
                    .when(!meta.is_empty(), |d| d.child(div().truncate().text_size(px(10.5)).text_color(t.faint).child(meta))),
            )
            .children(note.map(|(text, color)| div().flex_none().text_size(px(10.)).text_color(color).child(text)))
    }

    /// The search box (or, when saving a favorite, its name).
    fn picker_search(&self) -> Div {
        let t = &self.theme;
        div()
            .mx_1()
            .px_2()
            .py(px(4.))
            .rounded(px(t.radius))
            .border_1()
            .border_color(alpha(t.accent, 0.5))
            .bg(t.surface)
            .text_color(t.text)
            .children(self.picker_query.clone())
    }

    /// The name box of a favorite being saved.
    fn picker_name_field(&self) -> Div {
        let t = &self.theme;
        div()
            .mx_1()
            .px_2()
            .py(px(4.))
            .rounded(px(t.radius))
            .border_1()
            .border_color(alpha(t.warn, 0.5))
            .bg(t.surface)
            .text_color(t.text)
            .children(self.picker_name.clone())
    }

    fn render_picker(&self, scope: PickerScope, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let p = self.picker.clone().unwrap();
        let hover = t.hover;
        let query = self.picker_query.as_ref().map(|q| q.read(cx).text()).unwrap_or_default();
        let role_key: Option<String> = match &scope {
            PickerScope::Role(ri) => self.roles.get(*ri).map(|r| r.key.trim_start_matches("system-").to_string()),
            PickerScope::Session { .. } => Some("plan".into()),
        };
        // What is in use now, to mark it.
        let current: Option<Assignment> = match &scope {
            PickerScope::Role(ri) => self.roles.get(*ri).map(|r| r.default.clone()),
            PickerScope::Session { si, .. } => {
                let s = &self.sessions[*si];
                s.model_choice.clone().or_else(|| {
                    let mut parts = s.model.split(" · ");
                    let model = parts.next().unwrap_or("").to_string();
                    let o = self.catalog.options.iter().find(|o| o.model == model)?;
                    Some(o.assignment(parts.next().unwrap_or(""), parts.next().unwrap_or("")))
                })
            }
        };

        // The steps, as a path; a done step is a link back to it.
        let steps: Vec<(Stage, &'static str)> = vec![(Stage::Model, "Model"), (Stage::Thinking, "Thinking"), (Stage::Priority, "Priority"), (Stage::Apply, "Apply")];
        let here = steps.iter().position(|(x, _)| *x == p.stage).unwrap_or(0);
        let mut path = div().flex().flex_wrap().items_center().gap_1().px_1().text_size(px(10.5));
        if p.stage == Stage::Favorites {
            path = path.child(div().text_color(t.text).child("FAVORITES"));
        } else {
            let mut shown = 0;
            for (i, (stage, label)) in steps.iter().enumerate() {
                let skip = match stage {
                    Stage::Thinking => p.model.as_ref().is_some_and(|m| m.thinking_options.len() <= 1),
                    Stage::Priority => p.model.as_ref().is_some_and(|m| m.service_tiers.len() <= 1),
                    _ => false,
                };
                if skip && i != here {
                    continue;
                }
                let done = i < here;
                let stage2 = stage.clone();
                shown += 1;
                let crumb = div()
                    .id(SharedString::from(format!("picker-step-{i}")))
                    .px_1()
                    .rounded(px(t.radius))
                    .text_color(if i == here { t.text } else if done { t.accent } else { t.faint })
                    .when(done, |d| d.cursor_pointer().hover(move |s| s.bg(hover)).on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.picker_go(stage2.clone(), window, cx))))
                    .child(format!("{shown} {}", label.to_uppercase()));
                if shown > 1 {
                    path = path.child(div().text_color(t.faint).child("›"));
                }
                path = path.child(crumb);
            }
        }

        let header = div()
            .flex()
            .items_center()
            .gap_2()
            .px_1()
            .child(div().flex_1().min_w_0().child(path))
            .when(p.stage != Stage::Favorites, |d| {
                d.child(
                    div()
                        .id("picker-favorites")
                        .px_1()
                        .flex_none()
                        .rounded(px(t.radius))
                        .text_size(px(10.5))
                        .text_color(t.warn)
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                            if let Some(p) = this.picker.as_mut() {
                                p.saving = false;
                            }
                            this.picker_go(Stage::Favorites, window, cx)
                        }))
                        .child(format!("★ favorites {}", self.catalog.favorites.len())),
                )
            })
            .child(
                div()
                    .id("picker-close")
                    .px_1()
                    .flex_none()
                    .rounded(px(t.radius))
                    .text_color(t.faint)
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.close_picker(cx)))
                    .child("×"),
            );

        let body: Div = match p.stage {
            Stage::Favorites => {
                let rows: Vec<AnyElement> = self
                    .catalog
                    .favorites
                    .iter()
                    .map(|f| {
                        let on = current.as_ref().is_some_and(|c| *c == f.assignment);
                        let f2 = f.clone();
                        let id = f.id.clone();
                        div()
                            .flex()
                            .items_center()
                            .gap_1()
                            .child(
                                self.picker_row(
                                    SharedString::from(format!("fav-{}", f.id)),
                                    f.name.clone(),
                                    format!("{} · {}", f.assignment.provider, f.assignment.short()),
                                    on,
                                    if on { Some(("in use", t.accent)) } else { None },
                                )
                                .flex_1()
                                .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.pick_favorite(f2.clone(), window, cx))),
                            )
                            .child(
                                div()
                                    .id(SharedString::from(format!("fav-remove-{}", f.id)))
                                    .px_1()
                                    .rounded(px(t.radius))
                                    .text_size(px(10.5))
                                    .text_color(t.faint)
                                    .cursor_pointer()
                                    .hover(move |s| s.bg(hover))
                                    .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.remove_favorite(id.clone(), cx)))
                                    .child("remove"),
                            )
                            .into_any_element()
                    })
                    .collect();
                div()
                    .flex()
                    .flex_col()
                    .gap(px(1.))
                    .when(rows.is_empty(), |d| d.child(div().px_2().py_1().text_size(px(11.)).text_color(t.faint).child("No favorites yet. Make one from a model.")))
                    .children(rows)
                    .child(
                        div()
                            .id("picker-new-favorite")
                            .mx_1()
                            .mt_1()
                            .px_2()
                            .py(px(4.))
                            .rounded(px(t.radius))
                            .border_1()
                            .border_color(t.border)
                            .text_color(t.muted)
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                                if let Some(p) = this.picker.as_mut() {
                                    p.saving = true;
                                    p.favorite = None;
                                }
                                this.picker_go(Stage::Model, window, cx)
                            }))
                            .child("+ new favorite: pick a model, its thinking and priority, then name it"),
                    )
                    .child(
                        div()
                            .id("picker-browse")
                            .mx_1()
                            .px_2()
                            .py(px(3.))
                            .text_size(px(10.5))
                            .text_color(t.faint)
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                                if let Some(p) = this.picker.as_mut() {
                                    p.saving = false;
                                }
                                this.picker_go(Stage::Model, window, cx)
                            }))
                            .child("or pick any model once →"),
                    )
            }
            Stage::Model => {
                let mut chips = div().flex().flex_wrap().gap_1().px_1();
                for provider in &self.catalog.providers {
                    let on = p.provider.as_deref() == Some(provider);
                    let pv = provider.clone();
                    chips = chips.child(
                        div()
                            .id(SharedString::from(format!("picker-provider-{provider}")))
                            .px_2()
                            .py(px(2.))
                            .rounded(px(t.radius))
                            .border_1()
                            .border_color(if on { alpha(t.accent, 0.6) } else { t.border })
                            .when(on, |d| d.bg(alpha(t.accent, 0.12)))
                            .text_size(px(10.5))
                            .text_color(if on { t.text } else { t.muted })
                            .cursor_pointer()
                            .hover(move |s| s.bg(hover))
                            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                                if let Some(pk) = this.picker.as_mut() {
                                    pk.provider = if pk.provider.as_deref() == Some(pv.as_str()) { None } else { Some(pv.clone()) };
                                }
                                cx.notify();
                            }))
                            .child(provider.clone()),
                    );
                }
                let options: Vec<ModelOption> = self
                    .catalog
                    .sorted()
                    .into_iter()
                    .filter(|o| p.provider.as_deref().is_none_or(|pv| pv == o.provider))
                    .filter(|o| query.trim().is_empty() || o.matches(&query))
                    .cloned()
                    .collect();
                let rows: Vec<AnyElement> = options
                    .iter()
                    .take(100)
                    .map(|o| {
                        let on = current.as_ref().is_some_and(|c| c.provider.as_ref() == o.provider && c.model.as_ref() == o.model);
                        let fav = self.catalog.is_favorite(&o.provider, &o.model);
                        let recommended = role_key.as_ref().is_some_and(|r| o.recommended_for.iter().any(|x| x == r));
                        let mut meta = vec![o.provider.clone(), o.model.clone()];
                        if o.context_window > 0 {
                            meta.push(format!("{}k context", o.context_window / 1000));
                        }
                        if !o.thinking_options.is_empty() {
                            meta.push(format!("thinking {}", o.thinking_options.join("/")));
                        }
                        if o.service_tiers.len() > 1 {
                            meta.push(format!("priority {}", o.service_tiers.join("/")));
                        }
                        let note = if on {
                            Some(("in use", t.accent))
                        } else if recommended {
                            Some(("recommended", t.ok))
                        } else if fav {
                            Some(("★", t.warn))
                        } else {
                            None
                        };
                        let o2 = o.clone();
                        self.picker_row(SharedString::from(format!("model-{}", o.key())), o.title().to_string(), meta.join(" · "), on, note)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.pick_model(o2.clone(), window, cx)))
                            .into_any_element()
                    })
                    .collect();
                let empty = if self.catalog.options.is_empty() {
                    Some("No models yet: connect a model provider in Settings.")
                } else if rows.is_empty() {
                    Some("Nothing matches.")
                } else {
                    None
                };
                div()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .when(p.saving, |d| d.child(div().px_2().text_size(px(10.5)).text_color(t.warn).child("NEW FAVORITE · pick the model")))
                    .child(self.picker_search())
                    .when(self.catalog.providers.len() > 1, |d| d.child(chips))
                    .child(div().id("picker-list").max_h(px(300.)).overflow_y_scroll().flex().flex_col().gap(px(1.)).children(rows))
                    .children(empty.map(|e| div().px_2().py_1().text_size(px(11.)).text_color(t.faint).child(e)))
            }
            Stage::Thinking => {
                let m = p.model.clone().unwrap();
                let rows: Vec<AnyElement> = m
                    .thinking_options
                    .iter()
                    .map(|level| {
                        let on = p.thinking.as_deref() == Some(level) || (p.thinking.is_none() && *level == m.default_thinking);
                        let meta = if *level == m.default_thinking { "the model's default".to_string() } else { String::new() };
                        let l2 = level.clone();
                        self.picker_row(SharedString::from(format!("think-{level}")), level.clone(), meta, on, None)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.pick_thinking(l2.clone(), window, cx)))
                            .into_any_element()
                    })
                    .collect();
                div().flex().flex_col().gap(px(1.)).child(div().px_2().text_size(px(11.)).text_color(t.faint).child(format!("How hard {} thinks", m.title()))).children(rows)
            }
            Stage::Priority => {
                let m = p.model.clone().unwrap();
                let rows: Vec<AnyElement> = m
                    .service_tiers
                    .iter()
                    .map(|tier| {
                        let on = p.tier.as_deref() == Some(tier) || (p.tier.is_none() && *tier == m.default_service_tier);
                        let meta = if *tier == m.default_service_tier { "the provider's default".to_string() } else { String::new() };
                        let t2 = tier.clone();
                        self.picker_row(SharedString::from(format!("tier-{tier}")), tier.clone(), meta, on, None)
                            .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| this.pick_tier(t2.clone(), window, cx)))
                            .into_any_element()
                    })
                    .collect();
                div().flex().flex_col().gap(px(1.)).child(div().px_2().text_size(px(11.)).text_color(t.faint).child("Priority: the provider's service tier for this model")).children(rows)
            }
            Stage::Apply => {
                let a = p.assignment().unwrap_or_else(|| Assignment { provider: "".into(), model: "".into(), thinking: "".into(), tier: "".into() });
                let title = p.favorite.as_ref().map(|f| f.name.clone()).or_else(|| p.model.as_ref().map(|m| m.title().to_string())).unwrap_or_default();
                let summary = div()
                    .px_2()
                    .py_1()
                    .child(div().text_color(t.text).child(title))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(format!("{} · {}", a.provider, a.short())));
                let action = |id: &'static str, label: String, detail: &'static str, strong: bool, what: &'static str| {
                    div()
                        .id(id)
                        .flex()
                        .items_center()
                        .gap_2()
                        .px_2()
                        .py(px(4.))
                        .rounded(px(t.radius))
                        .border_1()
                        .border_color(if strong { alpha(t.accent, 0.6) } else { t.border })
                        .when(strong, |d| d.bg(alpha(t.accent, 0.1)))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.apply_pick(what, cx)))
                        .child(div().text_color(t.text).child(label))
                        .child(div().flex_1().min_w_0().truncate().text_size(px(10.5)).text_color(t.faint).child(detail))
                };
                let mut actions = div().flex().flex_col().gap_1().px_1();
                if p.saving {
                    actions = actions
                        .child(div().px_1().text_size(px(10.5)).text_color(t.faint).child("NAME THE FAVORITE"))
                        .child(self.picker_name_field())
                        .child(
                            div()
                                .id("picker-save-favorite")
                                .mx_1()
                                .px_2()
                                .py(px(4.))
                                .rounded(px(t.radius))
                                .border_1()
                                .border_color(alpha(t.warn, 0.6))
                                .bg(alpha(t.warn, 0.08))
                                .text_color(t.text)
                                .cursor_pointer()
                                .hover(move |s| s.bg(hover))
                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.save_favorite(window, cx)))
                                .child("★ Save favorite"),
                        );
                } else {
                    match &scope {
                        PickerScope::Session { .. } => {
                            actions = actions
                                .child(action("apply-chat", "This chat only".into(), "nothing else changes", true, "chat"))
                                .child(action("apply-default", "New default for the Orchestrator".into(), "every conversation from now on", false, "default"))
                                .child(action("apply-both", "Both".into(), "this chat, and the default", false, "both"));
                        }
                        PickerScope::Role(ri) => {
                            let label = self.roles.get(*ri).map(|r| r.label.to_string()).unwrap_or_default();
                            actions = actions.child(action("apply-role", format!("Set as the {label} default"), "every conversation from now on", true, "role"));
                        }
                    }
                    if p.favorite.is_none() {
                        actions = actions.child(
                            div()
                                .id("picker-make-favorite")
                                .px_2()
                                .py(px(3.))
                                .text_size(px(10.5))
                                .text_color(t.warn)
                                .cursor_pointer()
                                .hover(move |s| s.bg(hover))
                                .on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                                    if let Some(p) = this.picker.as_mut() {
                                        p.saving = true;
                                    }
                                    if let Some(n) = &this.picker_name {
                                        n.update(cx, |n, cx| n.set_text("", cx));
                                        window.focus(&n.focus_handle(cx));
                                    }
                                    cx.notify();
                                }))
                                .child("★ save as a favorite…"),
                        );
                    }
                }
                div().flex().flex_col().gap_1().child(summary).child(actions)
            }
        };

        div()
            .flex()
            .flex_col()
            .gap_1()
            .p_1()
            .mb_1()
            .rounded(px(t.radius))
            .border_1()
            .border_color(alpha(t.accent, 0.4))
            .bg(t.panel)
            .child(header)
            .child(body)
    }
}
