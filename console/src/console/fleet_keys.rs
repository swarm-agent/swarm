//! Provider keys on the Fleet page: the home's fleet key manager
//! (swarm::fleet_keys). The list is the home's record, read on opening the
//! page, after every action and on each fleet read while the page shows. A
//! new key is entered on Swarm's own key page, opened in the person's
//! browser; it never passes through this app. Install and revoke are the
//! home's routes, and a refusal is shown in Swarm's words.

use std::collections::BTreeSet;

use gpui::{ClickEvent, Context, Div, Entity, Hsla, SharedString, Stateful, div, prelude::*, px};

use super::{Console, Link, Page, alpha, ago, now_ms};
use crate::onboarding::input_colors;
use crate::swarm::client::Client;
use crate::swarm::fleet_keys::{self, Key, List, Requested, State, Target};
use crate::text_input::TextInput;

/// The "add a key" form.
#[derive(Clone, Default)]
pub(super) struct Form {
    provider: Option<String>,
    home: bool,
    machines: BTreeSet<String>,
    sending: bool,
}

/// Machines picked for one key's install or revoke.
#[derive(Clone)]
pub(super) struct Pick {
    id: String,
    home: bool,
    machines: BTreeSet<String>,
}

#[derive(Default)]
pub(super) struct KeysPage {
    /// The home's last answer, and whether a read was ever made.
    list: Option<List>,
    read_error: Option<String>,
    busy: bool,
    again: bool,
    at: u64,
    form: Option<Form>,
    providers: Option<Result<Vec<String>, String>>,
    label: Option<Entity<TextInput>>,
    /// The last key page Swarm gave.
    requested: Option<Requested>,
    /// A key an install or revoke runs on, and the word for it.
    acting: Option<(String, &'static str)>,
    /// Swarm's refusal of the last action, as it said it.
    error: Option<String>,
    note: Option<String>,
    install: Option<Pick>,
    revoke: Option<Pick>,
}

impl Console {
    /// Home's client, only in home's own window.
    fn keys_home(&self) -> Option<Client> {
        if self.via.is_some() || self.link == Link::Demo { None } else { self.home.clone() }
    }

    /// The linked machines, in home's order.
    fn linked_names(&self) -> Vec<String> {
        self.machines.iter().skip(1).map(|m| m.name.to_string()).collect()
    }

    fn home_name(&self) -> String {
        match self.keys_page.list.as_ref().map(|l| l.home.clone()).filter(|h| !h.is_empty()) {
            Some(h) => h,
            None => self.machines.first().map(|m| m.name.to_string()).unwrap_or_else(|| "home".into()),
        }
    }

    /// On the Fleet page in home's window: the list is read once on
    /// opening, and the label field exists.
    pub(super) fn sync_fleet_keys(&mut self, cx: &mut Context<Self>) {
        if self.page != Page::Fleet || self.keys_home().is_none() {
            return;
        }
        if self.keys_page.label.is_none() {
            let colors = input_colors(&self.theme);
            let field = cx.new(|cx| TextInput::new("Name, like \"team\" (optional)", colors, cx).one_line());
            cx.observe(&field, |_, _, cx| cx.notify()).detach();
            self.keys_page.label = Some(field);
        }
        if self.keys_page.list.is_none() && self.keys_page.read_error.is_none() && !self.keys_page.busy {
            self.load_fleet_keys(cx);
        }
    }

    /// Reads GET /v3/fleet/keys; a read asked for while one runs follows it.
    pub(super) fn load_fleet_keys(&mut self, cx: &mut Context<Self>) {
        let Some(home) = self.keys_home() else { return };
        if self.keys_page.busy {
            self.keys_page.again = true;
            return;
        }
        self.keys_page.busy = true;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { fleet_keys::read(&home) }).await;
            this.update(cx, |this, cx| {
                let p = &mut this.keys_page;
                p.busy = false;
                p.at = now_ms();
                match result {
                    Ok(list) => {
                        p.list = Some(list);
                        p.read_error = None;
                    }
                    Err(e) => p.read_error = Some(e),
                }
                if std::mem::take(&mut this.keys_page.again) {
                    this.load_fleet_keys(cx);
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn load_key_providers(&mut self, cx: &mut Context<Self>) {
        let Some(home) = self.keys_home() else { return };
        self.keys_page.providers = None;
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { fleet_keys::read_providers(&home) }).await;
            this.update(cx, |this, cx| {
                this.keys_page.providers = Some(result);
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn open_fleet_key_form(&mut self, cx: &mut Context<Self>) {
        self.keys_page.form = Some(Form { home: true, ..Default::default() });
        self.keys_page.error = None;
        self.keys_page.note = None;
        self.load_key_providers(cx);
        cx.notify();
    }

    /// Asks the home for a one-time key page and opens it in the browser.
    fn request_key(&mut self, cx: &mut Context<Self>) {
        let Some(home) = self.keys_home() else { return };
        let Some(form) = self.keys_page.form.clone() else { return };
        let Some(provider) = form.provider.clone() else { return };
        if form.sending || (!form.home && form.machines.is_empty()) {
            return;
        }
        let label = self.keys_page.label.as_ref().map(|i| i.read(cx).text()).unwrap_or_default();
        let machines: Vec<String> = form.machines.iter().cloned().collect();
        if let Some(f) = &mut self.keys_page.form {
            f.sending = true;
        }
        self.keys_page.error = None;
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { fleet_keys::request(&home, &provider, &label, &machines, form.home) }).await;
            this.update(cx, |this, cx| {
                match result {
                    Ok(requested) => {
                        cx.open_url(&requested.url);
                        this.keys_page.requested = Some(requested);
                        this.keys_page.form = None;
                        if let Some(label) = &this.keys_page.label {
                            label.update(cx, |input, cx| input.set_text("", cx));
                        }
                    }
                    Err(e) => {
                        if let Some(f) = &mut this.keys_page.form {
                            f.sending = false;
                        }
                        this.keys_page.error = Some(e);
                    }
                }
                this.load_fleet_keys(cx);
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn open_install(&mut self, key: &Key, cx: &mut Context<Self>) {
        self.keys_page.revoke = None;
        self.keys_page.error = None;
        self.keys_page.install = Some(Pick { id: key.id.clone(), home: false, machines: BTreeSet::new() });
        cx.notify();
    }

    fn open_revoke(&mut self, key: &Key, cx: &mut Context<Self>) {
        self.keys_page.install = None;
        self.keys_page.error = None;
        let targets = key.revocable();
        self.keys_page.revoke = Some(Pick {
            id: key.id.clone(),
            home: targets.iter().any(|t| t.home),
            machines: targets.iter().filter(|t| !t.home).map(|t| t.machine.clone()).collect(),
        });
        cx.notify();
    }

    /// Install (`revoke` false) or revoke the picked machines; Swarm answers
    /// once every machine did, then the list is read again.
    fn run_pick(&mut self, revoke: bool, cx: &mut Context<Self>) {
        let Some(home) = self.keys_home() else { return };
        let pick = if revoke { self.keys_page.revoke.take() } else { self.keys_page.install.take() };
        let Some(pick) = pick else { return };
        if self.keys_page.acting.is_some() || (!pick.home && pick.machines.is_empty()) {
            return;
        }
        let machines: Vec<String> = pick.machines.iter().cloned().collect();
        self.keys_page.acting = Some((pick.id.clone(), if revoke { "revoking…" } else { "installing…" }));
        self.keys_page.error = None;
        self.keys_page.note = None;
        cx.notify();
        let id = pick.id.clone();
        cx.spawn(async move |this, cx| {
            let result = cx
                .background_executor()
                .spawn(async move { if revoke { fleet_keys::revoke(&home, &id, &machines, pick.home) } else { fleet_keys::install(&home, &id, &machines, pick.home) } })
                .await;
            this.update(cx, |this, cx| {
                this.keys_page.acting = None;
                match result {
                    Ok(answer) if revoke && answer.get("value_deleted").and_then(|v| v.as_bool()) == Some(true) => {
                        this.keys_page.note = Some("Swarm removed the key everywhere and deleted the value the home kept.".into());
                    }
                    Ok(_) => this.keys_page.note = Some("Swarm answered; each machine's outcome is listed with the key.".into()),
                    Err(e) => this.keys_page.error = Some(e),
                }
                this.load_fleet_keys(cx);
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    // Rendering

    /// A tick box with its label.
    pub(super) fn tick(&self, id: impl Into<SharedString>, label: String, on: bool) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        div()
            .id(id.into())
            .flex()
            .items_center()
            .gap_1()
            .px_1()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .text_size(px(11.))
            .child(div().text_color(if on { t.accent } else { t.faint }).child(if on { "☑" } else { "☐" }))
            .child(div().text_color(if on { t.text } else { t.muted }).child(label))
    }

    /// A button that does nothing now, with why next to it.
    pub(super) fn off_button(&self, label: impl Into<SharedString>) -> Div {
        let t = &self.theme;
        div().px_2().py(px(1.)).rounded(px(t.radius)).border_1().border_color(t.border).text_size(px(11.)).text_color(t.faint).child(label.into())
    }

    fn state_look(&self, target: &Target, now: u64) -> (&'static str, Hsla, String) {
        let t = &self.theme;
        let when = |at: u64| if at > 0 { format!(" · {} ago", ago(now.saturating_sub(at))) } else { String::new() };
        match &target.state {
            State::Installed => (
                "●",
                t.ok,
                format!("installed{}{}", if target.verified { " · verified" } else { " · not verified" }, when(target.installed_at.max(target.updated_at))),
            ),
            State::Pending => ("○", t.info, format!("pending{}", when(target.updated_at))),
            State::Failed => ("✕", t.err, if target.error.is_empty() { format!("failed{}", when(target.updated_at)) } else { format!("failed: {}", target.error) }),
            State::Removed => ("–", t.faint, format!("removed{}", when(target.removed_at.max(target.updated_at)))),
            State::Other(_) => ("?", t.faint, target.state.label()),
        }
    }

    fn key_card(&self, ki: usize, key: &Key, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let now = now_ms();
        let home = self.home_name();
        let acting = self.keys_page.acting.as_ref().filter(|(id, _)| *id == key.id).map(|(_, w)| *w);
        let busy = self.keys_page.acting.is_some();
        let linked = self.linked_names();
        let (home_open, machines_open) = key.offer_install(&linked);

        let targets = key.targets.iter().map(|target| {
            let (glyph, color, line) = self.state_look(target, now);
            let name = if target.home { format!("{} (home)", if target.machine.is_empty() { home.clone() } else { target.machine.clone() }) } else { target.machine.clone() };
            let extra = (!target.error.is_empty() && target.state != State::Failed).then(|| target.error.clone());
            div()
                .flex()
                .items_center()
                .gap_2()
                .pl_3()
                .py(px(1.))
                .text_size(px(11.))
                .child(div().text_color(color).child(glyph))
                .child(div().w(px(140.)).flex_none().truncate().text_color(t.muted).child(name))
                .child(div().flex_1().min_w_0().truncate().text_color(if target.state == State::Failed { t.err } else { t.faint }).child(line))
                .children(extra.map(|e| div().max_w(px(280.)).truncate().text_color(t.warn).child(e)))
        });

        let id = key.id.clone();
        let mut card = div()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(alpha(t.border, 0.6))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(t.text).child(key.provider.clone()))
                    .when(!key.label.is_empty(), |d| d.child(div().text_color(t.muted).child(key.label.clone())))
                    .child(div().text_size(px(11.)).text_color(t.faint).child(format!(
                        "fingerprint {}{}",
                        if key.fingerprint.is_empty() { "—".to_string() } else { key.fingerprint.clone() },
                        if key.last4.is_empty() { String::new() } else { format!(" · …{}", key.last4) }
                    )))
                    .child(div().flex_1())
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(format!(
                        "added {}{}",
                        if key.created_at > 0 { format!("{} ago", ago(now.saturating_sub(key.created_at))) } else { "—".into() },
                        if key.created_by.is_empty() { String::new() } else { format!(" by {}", key.created_by) }
                    ))),
            )
            .children(targets)
            .when(key.targets.is_empty(), |d| d.child(div().pl_3().text_size(px(11.)).text_color(t.faint).child("Sent to no machine yet.")));

        if key.revoked_at != 0 {
            return card.child(div().pl_3().text_size(px(11.)).text_color(t.faint).child(format!(
                "Revoked everywhere {} ago; the home deleted the value it kept. Ask for a new key to use it again.",
                ago(now.saturating_sub(key.revoked_at))
            )));
        }

        // Actions, or what runs on this key now.
        card = card.child(match acting {
            Some(word) => div().pl_3().text_size(px(11.)).text_color(t.info).child(word),
            None => {
                let (a, b) = (key.clone(), key.clone());
                div()
                    .flex()
                    .gap_1()
                    .pl_3()
                    .pt_1()
                    .child(if busy { self.off_button("install on…") } else { div().child(self.button(SharedString::from(format!("key-install-{ki}")), "install on…", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_install(&a, cx)))) })
                    .child(if busy || key.revocable().is_empty() {
                        self.off_button("revoke…")
                    } else {
                        div().child(self.button(SharedString::from(format!("key-revoke-{ki}")), "revoke…", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.open_revoke(&b, cx))))
                    })
            }
        });

        // Install on more machines.
        if let Some(pick) = self.keys_page.install.as_ref().filter(|p| p.id == id) {
            let mut boxes: Vec<Stateful<Div>> = Vec::new();
            if home_open {
                boxes.push(self.tick(SharedString::from(format!("key-install-home-{ki}")), format!("{home} (home)"), pick.home).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                    if let Some(p) = &mut this.keys_page.install {
                        p.home = !p.home;
                    }
                    cx.notify();
                })));
            }
            for (mi, name) in machines_open.iter().enumerate() {
                let n = name.clone();
                boxes.push(self.tick(SharedString::from(format!("key-install-m-{ki}-{mi}")), name.clone(), pick.machines.contains(name)).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    if let Some(p) = &mut this.keys_page.install {
                        if !p.machines.remove(&n) {
                            p.machines.insert(n.clone());
                        }
                    }
                    cx.notify();
                })));
            }
            let chosen = pick.home || !pick.machines.is_empty();
            let empty = boxes.is_empty();
            card = card.child(
                div()
                    .ml_3()
                    .mt_1()
                    .p_2()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.panel)
                    .child(div().text_size(px(11.)).text_color(t.muted).child(if empty {
                        "It is installed or on its way on the home and every linked machine."
                    } else {
                        "Install this key on:"
                    }))
                    .child(div().flex().flex_wrap().gap_2().children(boxes))
                    .child(
                        div()
                            .flex()
                            .gap_1()
                            .child(if chosen {
                                div().child(self.button(SharedString::from(format!("key-install-go-{ki}")), "install", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.run_pick(false, cx))))
                            } else {
                                self.off_button("install")
                            })
                            .child(self.button(SharedString::from(format!("key-install-cancel-{ki}")), "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                this.keys_page.install = None;
                                cx.notify();
                            }))),
                    ),
            );
        }

        // Revoke, after the person sees exactly where from.
        if let Some(pick) = self.keys_page.revoke.as_ref().filter(|p| p.id == id) {
            let mut boxes: Vec<Stateful<Div>> = Vec::new();
            for (ti, target) in key.revocable().into_iter().enumerate() {
                if target.home {
                    boxes.push(self.tick(SharedString::from(format!("key-revoke-home-{ki}")), format!("{home} (home)"), pick.home).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        if let Some(p) = &mut this.keys_page.revoke {
                            p.home = !p.home;
                        }
                        cx.notify();
                    })));
                } else {
                    let n = target.machine.clone();
                    boxes.push(self.tick(SharedString::from(format!("key-revoke-m-{ki}-{ti}")), n.clone(), pick.machines.contains(&n)).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                        if let Some(p) = &mut this.keys_page.revoke {
                            if !p.machines.remove(&n) {
                                p.machines.insert(n.clone());
                            }
                        }
                        cx.notify();
                    })));
                }
            }
            let count = pick.machines.len() + usize::from(pick.home);
            let all = count == key.revocable().len();
            card = card.child(
                div()
                    .ml_3()
                    .mt_1()
                    .p_2()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(alpha(t.err, 0.5))
                    .bg(t.panel)
                    .child(div().text_size(px(11.)).text_color(t.text).child(format!("Revoke this {} key ({}) from:", key.provider, key.fingerprint)))
                    .child(div().flex().flex_wrap().gap_2().children(boxes))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(if all {
                        "Swarm removes it from each machine ticked; once it is gone everywhere, the home deletes the value it keeps."
                    } else {
                        "Swarm removes it from each machine ticked; the home keeps the value while any machine still has it."
                    }))
                    .child(
                        div()
                            .flex()
                            .gap_1()
                            .child(if count > 0 {
                                div().child(
                                    self.button(SharedString::from(format!("key-revoke-go-{ki}")), format!("revoke from {count}"), true)
                                        .on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.run_pick(true, cx))),
                                )
                            } else {
                                self.off_button("revoke")
                            })
                            .child(self.button(SharedString::from(format!("key-revoke-cancel-{ki}")), "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                this.keys_page.revoke = None;
                                cx.notify();
                            }))),
                    ),
            );
        }
        card
    }

    fn key_form(&self, form: &Form, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let providers: Div = match &self.keys_page.providers {
            None => self.note_line("Reading the home's providers…"),
            Some(Err(e)) => div().text_size(px(11.)).text_color(t.err).child(format!("Could not read the providers: {e}")),
            Some(Ok(list)) if list.is_empty() => self.note_line("The home has no provider that takes an API key."),
            Some(Ok(list)) => div().flex().flex_wrap().gap_1().children(list.iter().enumerate().map(|(pi, p)| {
                let chosen = form.provider.as_deref() == Some(p.as_str());
                let id = p.clone();
                self.button(SharedString::from(format!("key-provider-{pi}")), p.clone(), chosen).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    if let Some(f) = &mut this.keys_page.form {
                        f.provider = Some(id.clone());
                    }
                    cx.notify();
                }))
            })),
        };
        let home = self.home_name();
        let mut boxes: Vec<Stateful<Div>> = vec![self.tick("key-form-home", format!("{home} (home)"), form.home).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
            if let Some(f) = &mut this.keys_page.form {
                f.home = !f.home;
            }
            cx.notify();
        }))];
        for (mi, name) in self.linked_names().into_iter().enumerate() {
            let on = form.machines.contains(&name);
            let n = name.clone();
            boxes.push(self.tick(SharedString::from(format!("key-form-m-{mi}")), name, on).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                if let Some(f) = &mut this.keys_page.form {
                    if !f.machines.remove(&n) {
                        f.machines.insert(n.clone());
                    }
                }
                cx.notify();
            })));
        }
        let ready = form.provider.is_some() && (form.home || !form.machines.is_empty()) && !form.sending;
        let label = |s: &'static str| div().w(px(84.)).flex_none().text_size(px(11.)).text_color(t.faint).child(s);
        div()
            .mx_3()
            .my_2()
            .p_3()
            .flex()
            .flex_col()
            .gap_2()
            .rounded(px(t.radius))
            .border_1()
            .border_color(t.border_strong)
            .bg(t.panel)
            .child(div().text_color(t.text).child("Add a key"))
            .child(div().flex().items_start().gap_2().child(label("provider")).child(div().flex_1().child(providers)))
            .child(
                div().flex().items_center().gap_2().child(label("name")).child(
                    div().flex_1().px_2().py_1().rounded(px(t.radius)).border_1().border_color(t.border).bg(t.surface).children(self.keys_page.label.clone()),
                ),
            )
            .child(div().flex().items_start().gap_2().child(label("install on")).child(div().flex_1().flex().flex_wrap().gap_2().children(boxes)))
            .child(self.note_line("Swarm opens its own key page in your browser. Enter the key on that page; it never passes through this app."))
            .child(
                div()
                    .flex()
                    .gap_1()
                    .child(if form.sending {
                        self.off_button("asking Swarm…")
                    } else if ready {
                        div().child(self.button("key-form-go", "open the key page", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.request_key(cx))))
                    } else {
                        self.off_button("open the key page")
                    })
                    .child(self.button("key-form-cancel", "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.keys_page.form = None;
                        cx.notify();
                    }))),
            )
    }

    fn key_requested(&self, r: &Requested, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let listed = self.keys_page.list.as_ref().is_some_and(|l| l.keys.iter().any(|k| k.id == r.id));
        let url = r.url.clone();
        let minutes = r.expires_in_seconds / 60;
        div()
            .mx_3()
            .my_2()
            .p_3()
            .flex()
            .flex_col()
            .gap_1()
            .rounded(px(t.radius))
            .border_1()
            .border_color(alpha(t.accent, 0.5))
            .bg(t.panel)
            .child(div().text_color(t.text).child(format!("Swarm's key page for {} opened in your browser.", if r.provider.is_empty() { "the provider" } else { r.provider.as_str() })))
            .child(div().text_size(px(11.)).text_color(t.text).child("Enter the key on that page; it never passes through this app."))
            .when(minutes > 0, |d| d.child(self.note_line(format!("The link works once and expires in {minutes} minutes."))))
            .child(div().text_size(px(10.5)).text_color(t.faint).truncate().child(r.url.clone()))
            .child(self.note_line(if listed {
                "The home has the key: its progress on each machine is listed below."
            } else {
                "The key is listed below once you enter it; this list is read again with each fleet read while this page is open."
            }))
            .child(
                div()
                    .flex()
                    .gap_1()
                    .pt_1()
                    .child(self.button("key-page-again", "open the page again", false).on_click(cx.listener(move |_, _: &ClickEvent, _, cx| cx.open_url(&url))))
                    .child(self.button("key-page-done", "dismiss", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.keys_page.requested = None;
                        cx.notify();
                    }))),
            )
    }

    /// The PROVIDER KEYS section of the Fleet page.
    pub(super) fn fleet_keys_section(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let p = &self.keys_page;
        let count = p.list.as_ref().map(|l| l.keys.len());
        let head = self.section("PROVIDER KEYS", count).px_0();
        let card = div().rounded(px(t.radius + 2.)).border_1().border_color(t.border).bg(t.surface);
        let body = if self.via.is_some() {
            card.child(div().p_3().child(self.note_line("Provider keys for the fleet are managed from the home: open the home's window.")))
        } else if self.link == Link::Demo {
            card.child(div().p_3().child(self.note_line("The demo set reads no Swarm.")))
        } else if self.home.is_none() {
            card.child(div().p_3().child(self.note_line("Connecting to Swarm…")))
        } else {
            let now = now_ms();
            let status = if p.busy {
                "reading…".to_string()
            } else if p.at > 0 {
                format!("read {} ago", ago(now.saturating_sub(p.at)))
            } else {
                String::new()
            };
            let keys: Vec<Div> = p.list.as_ref().map(|l| l.keys.iter().enumerate().map(|(ki, k)| self.key_card(ki, k, cx)).collect()).unwrap_or_default();
            let empty: Option<Div> = match (&p.list, &p.read_error) {
                (None, None) => Some(self.note_line("Reading the home's keys…")),
                (Some(l), _) if l.keys.is_empty() => Some(self.note_line("The home manages no provider keys yet.")),
                _ => None,
            };
            card.child(
                div()
                    .flex()
                    .flex_wrap()
                    .items_center()
                    .gap_2()
                    .px_3()
                    .py_2()
                    .border_b_1()
                    .border_color(t.border)
                    .child(div().flex_1().min_w_0().text_size(px(11.)).text_color(t.faint).child(
                        "Provider API keys the home keeps sealed and installs on its machines, as the home reports them. Never a key value.",
                    ))
                    .child(div().text_size(px(10.5)).text_color(t.faint).child(status))
                    .child(self.button("keys-refresh", "refresh", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.load_fleet_keys(cx))))
                    .child(if p.form.is_some() {
                        self.off_button("add a key")
                    } else {
                        div().child(self.button("keys-add", "add a key", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.open_fleet_key_form(cx))))
                    }),
            )
            .children(p.read_error.as_ref().map(|e| div().px_3().pt_2().text_size(px(11.)).text_color(t.err).child(format!("Could not read the keys: {e}"))))
            .children(p.error.as_ref().map(|e| div().px_3().pt_2().text_size(px(11.)).text_color(t.err).child(format!("Swarm refused: {e}"))))
            .children(p.note.as_ref().map(|n| div().px_3().pt_2().text_size(px(11.)).text_color(t.muted).child(n.clone())))
            .children(p.form.as_ref().map(|f| self.key_form(f, cx)))
            .children(p.requested.as_ref().map(|r| self.key_requested(r, cx)))
            .children(empty.map(|e| div().px_3().py_2().child(e)))
            .children(keys)
        };
        div().flex().flex_col().gap_4().child(head).child(body)
    }
}
