//! Settings: home machine, connections, appearance and keys.

use gpui::{ClickEvent, Context, Div, SharedString, div, prelude::*, px};

use super::{Console, alpha, now_ms};
use crate::data::Health;
use crate::fleet_config::{Level, THIS_PC};
use crate::icons::{Icon, IconStyle};

impl Console {
    pub(super) fn settings_group(&self, title: &'static str, note: &'static str, rows: Vec<Div>) -> Div {
        let t = &self.theme;
        div()
            .flex()
            .flex_col()
            .gap_1()
            .child(div().text_color(t.text).child(title))
            .child(div().pb_1().text_size(px(11.)).text_color(t.faint).child(note))
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

        let hover = t.hover;
        // Your PC first, then each fleet machine: full, ask or off.
        let mut machines: Vec<(String, String, String)> = vec![(THIS_PC.to_string(), format!("{} · your computer", crate::swarm::target::this_computer()), "its files and commands, as you, through this app".to_string())];
        machines.extend(self.machines.iter().skip(1).map(|m| (m.name.to_string(), m.name.to_string(), format!("tasks home sends to its Swarm at {}", m.gateway))));
        let fleet_rows: Vec<Div> = machines
            .into_iter()
            .map(|(key, label, detail)| {
                let current = self.fleet_config.level(&key);
                let pills = div().flex().gap_1().children([Level::Full, Level::Ask, Level::Off].into_iter().map(|level| {
                    let on = current == level;
                    let key = key.clone();
                    div()
                        .id(SharedString::from(format!("settings-level-{key}-{}", level.label())))
                        .w(px(48.))
                        .py(px(2.))
                        .flex()
                        .justify_center()
                        .rounded(px(t.radius))
                        .border_1()
                        .border_color(if on { alpha(t.accent, 0.7) } else { t.border })
                        .when(on, |d| d.bg(alpha(t.accent, 0.15)))
                        .cursor_pointer()
                        .hover(move |s| s.bg(hover))
                        .text_size(px(11.))
                        .text_color(if on { t.text } else { t.muted })
                        .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                            this.fleet_config.access.insert(key.clone(), level);
                            this.save_fleet_config(cx);
                        }))
                        .child(level.label())
                }));
                self.setting_row(label, detail, pills)
            })
            .collect();
        let mut folder_rows: Vec<Div> = self
            .fleet_config
            .folders
            .iter()
            .enumerate()
            .map(|(i, folder)| {
                self.setting_row(folder.clone(), "the Orchestrator may work in here", self.button(SharedString::from(format!("folder-remove-{i}")), "remove", false).on_click(cx.listener(
                    move |this, _: &ClickEvent, _, cx| {
                        if i < this.fleet_config.folders.len() {
                            this.fleet_config.folders.remove(i);
                        }
                        this.save_fleet_config(cx);
                    },
                )))
            })
            .collect();
        folder_rows.push(self.setting_row(
            if self.fleet_config.folders.is_empty() { "No folders yet" } else { "Add folders" },
            "pick folders the Orchestrator may work in on your computer",
            self.button("folder-add", "+ add folder", true).on_click(cx.listener(|this, _: &ClickEvent, _, cx| this.add_folders(cx))),
        ));

        let status = crate::browser::status();
        let chrome_detail = match (&status.error, status.connected) {
            (Some(e), _) => format!("socket error: {e}"),
            (None, true) => format!("connected · extension {} · {}", status.version, if status.stopped { "stopped by you in the extension" } else { "ready" }),
            (None, false) => "not connected: connect Chrome, then load the extension and open it once".to_string(),
        };
        let extension_dir = crate::browser::extension_dir().display().to_string();
        let mut browser_rows: Vec<Div> = vec![
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
        let now = now_ms();
        for (session_id, lease) in crate::browser::leases().into_iter().filter(|(_, l)| l.live(now)) {
            let title = self.sessions.iter().find(|s| s.live.as_ref().is_some_and(|l| l.session_id.as_ref() == session_id)).map(|s| s.title.to_string()).unwrap_or_else(|| session_id.clone());
            let detail = super::shield::lease_detail(&lease, lease.until.saturating_sub(now), crate::browser::tab_of(&session_id));
            let id = session_id.clone();
            browser_rows.push(self.setting_row(title, detail, self.button(SharedString::from(format!("browser-end-{session_id}")), "end", false).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.end_browser(id.clone(), cx)))));
        }

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
                "The Orchestrator may use your own Chrome through this app, only inside a lease you grant per session (sites, level, minutes) when it asks; Swarm keeps the lease and refuses calls outside it. The extension acts only in tabs it opens on those sites, never reads password fields, hides what looks like a key, and holds anything that looks like paying, publishing, keys or account settings for you unless the lease is full. Its popup has a stop.",
                browser_rows,
            ))
            .child(self.settings_group("Fleet", "Your other Swarm machines and what the Orchestrator may do on each. Click a level to change it.", fleet_rows))
            .child(self.settings_group(
                "Folders on your computer",
                "Where the Orchestrator may work on your computer through this app. Files outside them, and commands that start outside them, always wait for your run or deny.",
                folder_rows,
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

    /// Adds folders you pick (the system's folder picker) to the allowed list.
    fn add_folders(&mut self, cx: &mut Context<Self>) {
        let picked = cx.prompt_for_paths(gpui::PathPromptOptions { files: false, directories: true, multiple: true, prompt: Some("Allow".into()) });
        cx.spawn(async move |this, cx| {
            let Ok(Ok(Some(paths))) = picked.await else { return };
            let _ = this.update(cx, |c, cx| {
                for path in paths {
                    let path = path.display().to_string();
                    if !c.fleet_config.folders.contains(&path) {
                        c.fleet_config.folders.push(path);
                    }
                }
                c.save_fleet_config(cx);
            });
        })
        .detach();
    }

    /// Saves fleet.json and tells the open conversations.
    pub(super) fn save_fleet_config(&mut self, cx: &mut Context<Self>) {
        if let Err(e) = self.fleet_config.save() {
            self.notice = Some((format!("Could not save: {e}").into(), true));
        }
        self.fleet_told.clear();
        self.sync_fleet(cx);
        cx.notify();
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
