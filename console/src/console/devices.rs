//! Connected apps on the Permissions page: every app that holds an owner
//! session on this Swarm (swarm::clients), when Swarm last saw it, and
//! disconnecting one after the person confirms. Read on opening the page
//! and after each disconnect. Home's own window only: a linked machine's
//! sessions never cross the link.

use gpui::{ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, Link, alpha, ago, now_ms};
use crate::swarm::client::Client;
use crate::swarm::clients::{self, App, Standing};

#[derive(Default)]
pub(super) struct DevicesPage {
    apps: Option<Vec<App>>,
    read_error: Option<String>,
    busy: bool,
    /// The session the person asked to disconnect, waiting for "yes".
    confirm: Option<String>,
    /// The session a disconnect runs on.
    revoking: Option<String>,
    /// Swarm's refusal of the last disconnect, as it said it.
    error: Option<String>,
}

impl Console {
    fn devices_home(&self) -> Option<Client> {
        if self.via.is_some() || self.link == Link::Demo { None } else { self.home.clone() }
    }

    /// This app's own client session, as Swarm names it in the permissions.
    fn own_session(&self) -> Option<String> {
        let devices = self.permissions.as_ref()?.get("devices")?.as_array()?;
        devices
            .iter()
            .find(|d| d.get("this_device").and_then(|v| v.as_bool()).unwrap_or(false))
            .and_then(|d| d.get("id")?.as_str())
            .map(str::to_string)
    }

    /// On the Permissions page in home's window: read once on showing.
    pub(super) fn sync_devices(&mut self, cx: &mut Context<Self>) {
        if self.page == super::Page::Permissions && self.devices.apps.is_none() && self.devices.read_error.is_none() {
            self.load_devices(cx);
        }
    }

    pub(super) fn load_devices(&mut self, cx: &mut Context<Self>) {
        let Some(home) = self.devices_home() else { return };
        if self.devices.busy {
            return;
        }
        self.devices.busy = true;
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { clients::read(&home) }).await;
            this.update(cx, |this, cx| {
                this.devices.busy = false;
                match result {
                    Ok(apps) => {
                        this.devices.apps = Some(apps);
                        this.devices.read_error = None;
                    }
                    Err(e) => this.devices.read_error = Some(e),
                }
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    fn disconnect_app(&mut self, id: String, cx: &mut Context<Self>) {
        let Some(home) = self.devices_home() else { return };
        if self.devices.revoking.is_some() {
            return;
        }
        self.devices.confirm = None;
        self.devices.error = None;
        self.devices.revoking = Some(id.clone());
        cx.notify();
        cx.spawn(async move |this, cx| {
            let result = cx.background_executor().spawn(async move { clients::revoke(&home, &id) }).await;
            this.update(cx, |this, cx| {
                this.devices.revoking = None;
                match result {
                    Ok(()) => this.notice = Some(("Swarm disconnected the app.".into(), false)),
                    Err(e) => this.devices.error = Some(e),
                }
                this.load_devices(cx);
                this.load_permissions();
                cx.notify();
            })
            .ok();
        })
        .detach();
    }

    pub(super) fn devices_section(&self, cx: &mut Context<Self>) -> Option<Div> {
        self.devices_home()?;
        let t = &self.theme;
        let now = now_ms();
        let own = self.own_session();
        let mut rows: Vec<Div> = Vec::new();
        match (&self.devices.apps, &self.devices.read_error) {
            (_, Some(e)) => rows.push(div().px_3().py_2().text_size(px(11.)).text_color(t.err).child(format!("Could not read the connected apps: {e}"))),
            (None, None) => rows.push(div().px_3().py_2().text_size(px(11.)).text_color(t.faint).child("Reading the connected apps from Swarm…")),
            (Some(apps), None) if apps.is_empty() => rows.push(div().px_3().py_2().text_size(px(11.)).text_color(t.faint).child("No app holds a session on this Swarm.")),
            (Some(apps), None) => {
                for (i, a) in apps.iter().enumerate() {
                    rows.push(self.app_row(i, a, own.as_deref() == Some(a.session_id.as_str()), now, cx));
                }
            }
        }
        if let Some(e) = &self.devices.error {
            rows.push(div().px_3().py_1().text_size(px(11.)).text_color(t.err).child(format!("Swarm did not disconnect it: {e}")));
        }
        rows.push(div().px_3().py_2().text_size(px(10.5)).text_color(t.faint).child(
            "An app is connected on the machine itself: sudo swarmctl client connect NAME --device COMPUTER. Disconnecting stops Swarm accepting its token at once.",
        ));
        Some(self.settings_group("Connected apps", "Apps that act as you, the owner, on this Swarm: this console and any other client you connected.", rows))
    }

    fn app_row(&self, i: usize, a: &App, own: bool, now: u64, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let standing = a.standing(now);
        let title = match (a.name.is_empty(), a.device.is_empty()) {
            (true, true) => "unnamed app".to_string(),
            (true, false) => a.device.clone(),
            (false, true) => a.name.clone(),
            (false, false) => format!("{} · {}", a.name, a.device),
        };
        let since = |at: u64| match (at, ago(now.saturating_sub(at))) {
            (0, _) => "—".to_string(),
            (_, a) if a == "now" => "just now".to_string(),
            (_, a) => format!("{a} ago"),
        };
        let line = match standing {
            Standing::Connected => format!(
                "connected {} · last seen {} · {}",
                since(a.created_at),
                since(a.last_seen_at),
                if a.expires_at == 0 { "no expiry".to_string() } else { format!("expires in {}", ago(a.expires_at.saturating_sub(now))) }
            ),
            Standing::Expired => format!("expired {} · last seen {}", since(a.expires_at), since(a.last_seen_at)),
            Standing::Disconnected => format!("disconnected {} · last seen {}", since(a.revoked_at), since(a.last_seen_at)),
        };
        let id = a.session_id.clone();
        let action: Div = if standing != Standing::Connected {
            div()
        } else if self.devices.revoking.as_deref() == Some(id.as_str()) {
            div().text_size(px(11.)).text_color(t.info).child("disconnecting…")
        } else if self.devices.revoking.is_some() {
            self.off_button("disconnect…")
        } else {
            let id = id.clone();
            div().child(self.button(SharedString::from(format!("app-disconnect-{i}")), "disconnect…", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                this.devices.confirm = Some(id.clone());
                this.devices.error = None;
                cx.notify();
            })))
        };
        let mut row = div()
            .w_full()
            .flex()
            .flex_col()
            .gap(px(3.))
            .px_3()
            .py_2()
            .when(standing != Standing::Connected, |d| d.opacity(0.6))
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .child(div().flex_1().min_w_0().truncate().text_color(t.text).child(title))
                    .when(own, |d| d.child(div().flex_none().text_size(px(10.5)).text_color(t.accent).child("this app")))
                    .child(action),
            )
            .child(div().text_size(px(11.)).text_color(t.muted).child(line));
        if self.devices.confirm.as_deref() == Some(id.as_str()) {
            let yes = id.clone();
            row = row.child(
                div()
                    .mt_1()
                    .p_2()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(alpha(t.err, 0.5))
                    .bg(t.panel)
                    .child(div().text_size(px(11.)).text_color(t.text).child(if own {
                        "Disconnect this app? Swarm stops accepting its session at once and this window is signed out; connect it again on the machine to come back."
                    } else {
                        "Disconnect this app? Swarm stops accepting its session at once; it has to be connected again on the machine."
                    }))
                    .child(
                        div()
                            .flex()
                            .gap_1()
                            .child(self.button(SharedString::from(format!("app-disconnect-yes-{i}")), "disconnect", true).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.disconnect_app(yes.clone(), cx))))
                            .child(self.button(SharedString::from(format!("app-disconnect-no-{i}")), "cancel", false).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                                this.devices.confirm = None;
                                cx.notify();
                            }))),
                    ),
            );
        }
        row
    }
}
