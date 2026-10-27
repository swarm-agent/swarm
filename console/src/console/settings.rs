//! Settings: home machine, connections, appearance and keys.

use gpui::{ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha};
use crate::data::Health;
use crate::icons::{Icon, IconStyle};

impl Console {
    pub(super) fn settings_group(&self, title: impl Into<SharedString>, note: impl Into<SharedString>, rows: Vec<Div>) -> Div {
        let t = &self.theme;
        div()
            .flex()
            .flex_col()
            .gap_1()
            .child(div().text_color(t.text).child(title.into()))
            .child(div().pb_1().text_size(px(11.)).text_color(t.faint).child(note.into()))
            .child(
                div()
                    .flex()
                    .flex_col()
                    .rounded(px(t.radius + 2.))
                    .border_1()
                    .border_color(t.border)
                    .bg(t.surface)
                    .children(rows.into_iter().enumerate().map(|(i, r)| {
                        r.when(i > 0, |d| d.border_t_1().border_color(alpha(t.border, 0.7)))
                    })),
            )
    }

    pub(super) fn setting_row(&self, label: impl Into<SharedString>, detail: impl Into<SharedString>, control: impl IntoElement) -> Div {
        let t = &self.theme;
        div()
            .w_full()
            .flex()
            .items_center()
            .gap_3()
            .px_3()
            .py_2()
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .child(div().truncate().text_color(t.text).child(label.into()))
                    .child(div().text_size(px(11.)).text_color(t.faint).child(detail.into())),
            )
            .child(control)
    }

    pub(super) fn render_settings(&self, cx: &mut Context<Self>) -> impl IntoElement + use<> {
        let t = &self.theme;

        let current = self.icon_style();
        let icon_choices = div().flex().gap_1()
        .children(IconStyle::ALL.into_iter().map(|style| {
            self.button(SharedString::from(format!("settings-icons-{}", style.label())), style.label(), current == style)
                .flex()
                .items_center()
                .gap_1()
                .child(self.icon_in(style, Icon::Agents, 11., if current == style { t.accent_text } else { t.muted }))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    this.icon_override = Some(style);
                    cx.notify();
                }))
        }));

        let connections: Vec<Div> = self
            .machines
            .iter()
            .map(|m| {
                let detail = match m.kind.as_ref() {
                    "local" => "this computer · client session".to_string(),
                    "tailnet" => format!("tailnet · {} key", m.access),
                    "relay" => format!("relay sign-in · {} key", m.access),
                    "paired" => format!("home reaches it at {}", m.gateway),
                    _ => "ssh tunnel · client session".to_string(),
                };
                div()
                    .flex()
                    .items_center()
                    .gap_3()
                    .px_3()
                    .py_2()
                    .child(div().text_color(self.health_color(m.health)).child("●"))
                    .child(
                        div()
                            .flex_1()
                            .child(div().text_color(t.text).child(m.name.clone()))
                            .child(div().text_size(px(11.)).text_color(t.faint).child(detail)),
                    )
                    .child(
                        div()
                            .text_size(px(11.))
                            .text_color(if m.health == Health::Offline { t.err } else { t.faint })
                            .child(if m.health == Health::Offline { "unreachable" } else { "connected" }),
                    )
            })
            .collect();

        let status = crate::browser::status();
        let chrome_detail = match (&status.error, status.connected) {
            (Some(e), _) => format!("socket error: {e}"),
            (None, true) => format!("connected · extension {} · {}", status.version, if status.stopped { "stopped by you in the extension" } else { "ready" }),
            (None, false) => "not connected: connect Chrome, then load the extension and open it once".to_string(),
        };
        let extension_dir = crate::browser::extension_dir().display().to_string();
        let browser_rows: Vec<Div> = vec![
            self.setting_row("Chrome", chrome_detail, self.button("browser-connect", "connect Chrome", !status.connected).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.connect_chrome(cx)))),
            self.setting_row(
                "Extension",
                format!("chrome://extensions → Developer mode → Load unpacked → {extension_dir} (id {})", crate::browser::EXTENSION_ID),
                self.button("browser-copy-path", "copy the path", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                    cx.write_to_clipboard(gpui::ClipboardItem::new_string(crate::browser::extension_dir().display().to_string()));
                    this.notice = Some(("Copied.".into(), false));
                    cx.notify();
                })),
            ),
        ];

        div()
            .id("settings-page")
            .size_full()
            .overflow_y_scroll()
            .p_5()
            .flex()
            .flex_col()
            .gap_5()
            .max_w(px(760.))
            .child(div().text_size(px(16.)).text_color(t.text).child("Settings"))
            .child(self.settings_group(
                "Home",
                "The machine whose Orchestrator you talk to; your conversations live there.",
                vec![self.setting_row("Home machine", format!("{} · {}", self.machines[0].name, self.machines[0].kind), div())],
            ))
            .child(self.render_providers(cx))
            .child(self.settings_group(
                "Browser",
                "How this app reaches your own Chrome. Whether agents may use it is this device's Browser rule on the Permissions page. The extension acts only in tabs it opens, never reads password fields and hides what looks like a key. Its popup has a stop.",
                browser_rows,
            ))
            .child(self.settings_group(
                "Connections",
                "How this app reaches each machine. Keys are files only you can read, in ~/.config.",
                connections,
            ))
            .child(self.settings_group(
                "Appearance",
                "Colors come from the project's theme in Swarm; ask the AI to change it.",
                vec![
                    self.setting_row("Theme", self.theme_name.clone(), div()),
                    self.setting_row("Icons", "line by default · ctrl-4 cycles", icon_choices),
                    self.setting_row("Font", "JetBrains Mono · 12.5px", div()),
                ],
            ))
    }

    /// Writes Chrome's native messaging manifest and the host script, so
    /// the extension can reach this app.
    fn connect_chrome(&mut self, cx: &mut Context<Self>) {
        self.notice = Some(match crate::browser::connect_chrome() {
            Ok(written) => (format!("Wrote {}. Now load the extension in chrome://extensions and open its popup once.", written.iter().map(|p| p.display().to_string()).collect::<Vec<_>>().join(", ")).into(), false),
            Err(e) => (e.into(), true),
        });
        cx.notify();
    }
}
