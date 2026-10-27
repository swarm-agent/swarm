//! Providers on the Settings page: which model providers Swarm can use, each
//! one's keys (several per provider, named, one active) and the ChatGPT
//! sign-in. Everything shown is Swarm's record, read again after each action;
//! a key goes to Swarm once and is never kept here.

use std::time::Duration;

use gpui::{ClickEvent, Context, Div, Focusable, SharedString, div, prelude::*, px};
use serde_json::{Value, json};

use super::{Console, alpha};
use crate::data::Credential;
use crate::swarm::client::Client;
use crate::swarm::map;

impl Console {
    /// Runs one call to Swarm off the main thread, then `done` with its
    /// result. `busy` names the wait in the page while it runs.
    fn provider_call<R: Send + 'static>(
        &mut self,
        cx: &mut Context<Self>,
        busy: Option<&str>,
        work: impl FnOnce(Client) -> R + Send + 'static,
        done: impl FnOnce(&mut Self, R, &mut Context<Self>) + 'static,
    ) {
        let Some(client) = self.live.as_ref().map(|l| l.client()) else { return };
        if let Some(busy) = busy {
            self.provider_busy = Some(busy.to_string());
            self.provider_error = None;
        }
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { work(client) }).await;
            this.update(cx, |this, cx| {
                this.provider_busy = None;
                done(this, result, cx);
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    /// Reads the providers and every stored key.
    pub(super) fn load_providers(&mut self, cx: &mut Context<Self>) {
        self.provider_call(
            cx,
            Some("Reading providers…"),
            |client| {
                let providers = client.get("/v1/providers").map_err(|e| e.to_string())?;
                let keys = client.get("/v1/auth/credentials?limit=200").map_err(|e| e.to_string())?;
                Ok::<_, String>(map::providers(&providers, &keys))
            },
            |this, result, _| match result {
                Ok(providers) => {
                    this.providers = providers;
                    this.providers_loaded = true;
                }
                Err(e) => this.provider_error = Some(e),
            },
        );
    }

    /// One action on a key, then the list is read again.
    fn provider_action(&mut self, cx: &mut Context<Self>, busy: &str, path: &'static str, body: Value) {
        self.provider_call(
            cx,
            Some(busy),
            move |client| client.post(path, body).map_err(|e| e.to_string()),
            |this, result, cx| match result {
                Ok(_) => this.load_providers(cx),
                Err(e) => this.provider_error = Some(e),
            },
        );
    }

    /// Asks Swarm to check a key with its provider. Swarm answers once and
    /// keeps no record of it, so the answer is shown as a note.
    fn verify_key(&mut self, provider: String, id: String, label: String, cx: &mut Context<Self>) {
        self.provider_note = None;
        self.provider_call(
            cx,
            Some("Checking the key with its provider…"),
            move |client| client.post("/v1/auth/credentials/verify", json!({"provider": provider, "id": id})).map_err(|e| e.to_string()),
            move |this, result, _| match result {
                Ok(v) => {
                    let connection = v.get("connection").cloned().unwrap_or(Value::Null);
                    let connected = connection.get("connected").and_then(Value::as_bool).unwrap_or(false);
                    let message = connection.get("message").and_then(Value::as_str).unwrap_or("").to_string();
                    this.provider_note = Some(match (connected, message.is_empty()) {
                        (true, true) => format!("{label}: the provider accepted it."),
                        (true, false) => format!("{label}: the provider accepted it. {message}"),
                        (false, true) => format!("{label}: the provider did not accept it."),
                        (false, false) => format!("{label}: {message}"),
                    });
                }
                Err(e) => this.provider_error = Some(e),
            },
        );
    }

    fn open_key_form(&mut self, provider: String, window: &mut gpui::Window, cx: &mut Context<Self>) {
        self.key_for = Some(provider);
        self.key_named = false;
        self.provider_error = None;
        if let Some(input) = &self.key_label {
            input.update(cx, |i, cx| i.set_text("", cx));
        }
        if let Some(input) = &self.key_value {
            input.update(cx, |i, cx| i.set_text("", cx));
            window.focus(&input.focus_handle(cx));
        }
        cx.notify();
    }

    /// Sends the typed key to Swarm, named as typed (or after its provider).
    /// It becomes the active key when the provider has none yet; otherwise
    /// the person picks it with "use".
    fn save_key(&mut self, cx: &mut Context<Self>) {
        let Some(provider) = self.key_for.clone() else { return };
        let key = self.key_value.as_ref().map(|i| i.read(cx).text().trim().to_string()).unwrap_or_default();
        if key.is_empty() {
            self.provider_error = Some("Paste the key first.".into());
            cx.notify();
            return;
        }
        // A name only when the person chose to give one; Swarm names the
        // rest itself.
        let label = self.key_label.as_ref().filter(|_| self.key_named).map(|i| i.read(cx).text().trim().to_string()).unwrap_or_default();
        let active = !self.providers.iter().any(|p| p.id.as_ref() == provider && p.keys.iter().any(|k| k.active));
        let mut body = json!({"provider": provider, "type": "api", "api_key": key, "active": active});
        if !label.is_empty() {
            body["label"] = json!(label);
        }
        // Swarm checks the key with its provider before keeping it; a refused
        // key stays in the form for the person to correct.
        self.provider_call(
            cx,
            Some("Sending the key to Swarm, which checks it with the provider…"),
            move |client| client.post("/v1/auth/credentials", body).map_err(|e| e.to_string()),
            |this, result, cx| match result {
                Ok(_) => {
                    this.key_for = None;
                    if let Some(input) = &this.key_value {
                        input.update(cx, |i, cx| i.set_text("", cx));
                    }
                    this.load_providers(cx);
                }
                Err(e) => this.provider_error = Some(e),
            },
        );
    }

    /// Starts the ChatGPT device sign-in and waits for Swarm to see it done.
    fn chatgpt_start(&mut self, cx: &mut Context<Self>) {
        self.provider_call(
            cx,
            Some("Asking OpenAI for a code…"),
            |client| client.post("/v1/auth/codex/oauth/start", json!({"provider": "codex", "method": "device", "active": true})).map_err(|e| e.to_string()),
            |this, result, cx| match result {
                Ok(v) => {
                    let text = |k: &str| v.get(k).and_then(Value::as_str).unwrap_or("").to_string();
                    this.chatgpt = Some((text("session_id"), text("verification_url"), text("user_code")));
                    this.chatgpt_poll(cx);
                }
                Err(e) => this.provider_error = Some(e),
            },
        );
    }

    fn chatgpt_poll(&mut self, cx: &mut Context<Self>) {
        let Some((login, _, _)) = self.chatgpt.clone() else { return };
        self.provider_call(
            cx,
            None,
            move |client| {
                std::thread::sleep(Duration::from_secs(2));
                client.get(&format!("/v1/auth/codex/oauth/status?session_id={login}")).map_err(|e| e.to_string())
            },
            |this, result, cx| {
                if this.chatgpt.is_none() {
                    return; // Cancelled meanwhile.
                }
                match result {
                    Ok(v) => match v.get("status").and_then(Value::as_str).unwrap_or("") {
                        "success" => {
                            this.chatgpt = None;
                            this.load_providers(cx);
                        }
                        "error" => {
                            this.chatgpt = None;
                            this.provider_error = Some(format!("Sign-in did not finish: {}", v.get("error").and_then(Value::as_str).unwrap_or("")));
                        }
                        _ => this.chatgpt_poll(cx),
                    },
                    Err(e) => {
                        this.chatgpt = None;
                        this.provider_error = Some(e);
                    }
                }
            },
        );
    }

    fn copy(&mut self, text: String, cx: &mut Context<Self>) {
        cx.write_to_clipboard(gpui::ClipboardItem::new_string(text.clone()));
        cx.write_to_primary(gpui::ClipboardItem::new_string(text));
        self.notice = Some(("Copied.".into(), false));
        cx.notify();
    }

    /// The Providers group: a provider, then its keys, then the add-key form
    /// or the sign-in code when one is open.
    pub(super) fn render_providers(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let mut rows: Vec<Div> = Vec::new();
        if !self.providers_loaded {
            rows.push(self.setting_row("Reading providers…", "", div()));
        }
        // What is happening, or what Swarm refused, first: it is read before
        // the list, which can run past the window.
        if let Some(busy) = &self.provider_busy {
            rows.push(div().px_3().py_2().text_size(px(11.5)).text_color(t.muted).child(busy.clone()));
        }
        if let Some(error) = &self.provider_error {
            rows.push(div().px_3().py_2().text_size(px(11.5)).line_height(px(16.)).text_color(t.err).child(error.clone()));
        }
        if let Some(note) = &self.provider_note {
            rows.push(div().px_3().py_2().text_size(px(11.5)).line_height(px(16.)).text_color(t.muted).child(note.clone()));
        }
        for p in &self.providers {
            let id = p.id.to_string();
            let detail = if p.ready {
                if p.default_model.is_empty() { "connected".to_string() } else { format!("connected · {}", p.default_model) }
            } else {
                p.reason.to_string()
            };
            let mut controls = div().flex().gap_1();
            if p.oauth {
                let pid = id.clone();
                let signing = self.chatgpt.is_some();
                controls = controls.child(
                    self.button(SharedString::from(format!("provider-{pid}-chatgpt")), if p.ready { "sign in again" } else { "sign in with ChatGPT" }, !p.ready)
                        .when(signing, |b| b.opacity(0.5))
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            if this.chatgpt.is_none() {
                                this.chatgpt_start(cx);
                            }
                        })),
                );
            }
            if p.accepts_key {
                let pid = id.clone();
                controls = controls.child(self.button(SharedString::from(format!("provider-{pid}-add")), "add key", !p.ready && !p.oauth).on_click(cx.listener(
                    move |this, _: &ClickEvent, window, cx| this.open_key_form(pid.clone(), window, cx),
                )));
            }
            rows.push(
                self.setting_row(id.clone(), detail, controls)
                    .child(div().text_color(if p.ready { t.ok } else { t.faint }).child(if p.ready { "●" } else { "○" })),
            );
            for k in &p.keys {
                rows.push(self.render_key(&id, k, cx));
            }
            if self.key_for.as_deref() == Some(id.as_str()) {
                rows.push(self.render_key_form(cx));
            }
            if let Some((_, url, code)) = self.chatgpt.clone().filter(|_| p.oauth) {
                rows.push(self.render_chatgpt(url, code, cx));
            }
        }
        self.settings_group(
            "Providers",
            "The model accounts Swarm's agents use. A key goes to Swarm once and stays there; this app never keeps one. One key per provider is active.",
            rows,
        )
    }

    /// One key: name, what it is, when it came, how it last verified.
    fn render_key(&self, provider: &str, k: &Credential, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let mut meta: Vec<String> = vec![k.auth_type.to_string()];
        if !k.last4.is_empty() {
            meta.push(format!("ends {}", k.last4));
        }
        if k.created_at > 0 {
            meta.push(format!("added {}", day(k.created_at)));
        }
        if k.expires_at > 0 {
            meta.push(format!("expires {}", day(k.expires_at)));
        }
        match &k.connection {
            Some((true, _, at)) => meta.push(if *at > 0 { format!("verified {}", day(*at)) } else { "verified".into() }),
            Some((false, message, _)) => meta.push(if message.is_empty() { "verify failed".into() } else { format!("verify failed: {message}") }),
            None => {}
        }
        let (provider, id) = (provider.to_string(), k.id.to_string());
        let key = format!("key-{provider}-{id}");
        let action = |label: &'static str, strong: bool, path: &'static str, busy: &'static str| {
            let (provider, id) = (provider.clone(), id.clone());
            self.button(SharedString::from(format!("{key}-{label}")), label, strong).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.provider_action(cx, busy, path, json!({"provider": provider, "id": id}));
            }))
        };
        let mut controls = div().flex().gap_1();
        if !k.active {
            controls = controls.child(action("use", true, "/v1/auth/credentials/active", "Switching the key…"));
        }
        let (vp, vid, vlabel) = (provider.clone(), id.clone(), k.label.to_string());
        controls = controls
            .child(self.button(SharedString::from(format!("{key}-verify")), "verify", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.verify_key(vp.clone(), vid.clone(), vlabel.clone(), cx)
            })))
            .child(action("remove", false, "/v1/auth/credentials/delete", "Removing the key…"));
        div()
            .w_full()
            .flex()
            .items_center()
            .gap_3()
            .pl(px(28.))
            .pr_3()
            .py(px(6.))
            .child(div().text_color(if k.active { t.accent } else { t.faint }).child(if k.active { "●" } else { "○" }))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(div().flex().gap_2().child(div().truncate().text_color(t.text).child(k.label.clone())).when(k.active, |d| d.child(div().text_size(px(11.)).text_color(t.accent).child("active"))))
                    .child(div().text_size(px(11.)).text_color(t.faint).child(meta.join(" · "))),
            )
            .child(controls)
    }

    /// The add-key form: a name and the key.
    fn render_key_form(&self, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let field = |input: Option<gpui::Entity<crate::text_input::TextInput>>, width: Option<f32>| {
            div()
                .when_some(width, |d, w| d.w(px(w)))
                .when(width.is_none(), |d| d.flex_1().min_w_0())
                .px_2()
                .py(px(4.))
                .rounded(px(t.radius))
                .border_1()
                .border_color(alpha(t.accent, 0.5))
                .bg(t.surface)
                .text_color(t.text)
                .children(input)
        };
        div()
            .w_full()
            .flex()
            .items_center()
            .gap_2()
            .pl(px(28.))
            .pr_3()
            .py(px(6.))
            .child(field(self.key_value.clone(), Some(380.)))
            .when(self.key_named, |d| d.child(field(self.key_label.clone(), Some(180.))))
            .when(!self.key_named, |d| {
                d.child(self.button("key-name", "name it", false).on_click(cx.listener(|this, _: &ClickEvent, window, cx| {
                    this.key_named = true;
                    if let Some(input) = &this.key_label {
                        window.focus(&input.focus_handle(cx));
                    }
                    cx.notify();
                })))
            })
            .child(self.button("key-save", "save", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.save_key(cx))))
            .child(self.button("key-cancel", "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                this.key_for = None;
                if let Some(input) = &this.key_value {
                    input.update(cx, |i, cx| i.set_text("", cx));
                }
                cx.notify();
            })))
    }

    /// The ChatGPT device code and link, copyable, while Swarm waits.
    fn render_chatgpt(&self, url: String, code: String, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let (copy_code, copy_url) = (code.clone(), url.clone());
        div()
            .w_full()
            .flex()
            .items_center()
            .gap_3()
            .pl(px(28.))
            .pr_3()
            .py(px(6.))
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(div().flex().items_center().gap_2().child(div().text_size(px(15.)).text_color(t.text).child(code)).child(div().text_size(px(11.)).text_color(t.faint).child("enter this code on the page, signed in to your ChatGPT plan")))
                    .child(div().truncate().text_size(px(11.)).text_color(t.accent).child(url)),
            )
            .child(self.button("chatgpt-copy-code", "copy code", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.copy(copy_code.clone(), cx))))
            .child(self.button("chatgpt-copy-link", "copy link", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.copy(copy_url.clone(), cx))))
            .child(self.button("chatgpt-cancel", "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                this.chatgpt = None;
                cx.notify();
            })))
    }
}

/// A day from milliseconds since the epoch, "2026-10-24" (UTC).
fn day(ms: u64) -> String {
    let days = (ms / 86_400_000) as i64;
    // Civil date from days since 1970-01-01 (Howard Hinnant's algorithm).
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}")
}

#[cfg(test)]
mod tests {
    use super::day;

    #[test]
    fn days_from_milliseconds() {
        assert_eq!(day(0), "1970-01-01");
        assert_eq!(day(1_792_812_974_545), "2026-10-24");
        assert_eq!(day(951_782_400_000), "2000-02-29");
    }
}
