//! Swarm Console: a native window over a Swarm machine (SWARM_CONSOLE_DEMO shows the design set).

mod browser;
mod browser_host;
mod console;
mod data;
mod fleet_config;
mod icons;
mod onboarding;
mod machine;
mod password_prompt;
mod swarm;
mod text_input;
mod layout;
mod theme;

use console::{
    ClosePane, Console, GoAgents, GoFleet, GoLive, GoMedia,
    GoSessions, GoSettings, GoUsage, GoSearch, GoAutomations, SearchRun, NextPane, SplitDown, SplitRight, ToggleLeft, CycleIcons,
};
use gpui::{
    App, AppContext, Application, Bounds, Focusable, KeyBinding, TitlebarOptions, WindowBounds,
    WindowOptions, px, size,
};

fn main() {
    // Chrome starts this binary as the native messaging host for the Swarm
    // extension (see ~/.config/swarm-console/browser-host): no window then.
    if std::env::args().any(|a| a == "--browser-host") {
        std::process::exit(browser_host::run());
    }
    Application::new().with_assets(icons::Assets).run(|cx: &mut App| {
        cx.bind_keys([
            KeyBinding::new("enter", password_prompt::Submit, Some("PasswordPrompt")),
            KeyBinding::new("escape", password_prompt::Cancel, Some("PasswordPrompt")),
            KeyBinding::new("enter", onboarding::Continue, Some("Onboarding")),
            KeyBinding::new("escape", onboarding::Back, Some("Onboarding")),
            KeyBinding::new("enter", console::Send, Some("Composer")),
            KeyBinding::new("tab", console::Complete, Some("Composer")),
            KeyBinding::new("enter", console::RenameSave, Some("Rename")),
            KeyBinding::new("escape", console::RenameCancel, Some("Rename")),
            KeyBinding::new("ctrl-c", console::CopySelection, Some("Console")),
            KeyBinding::new("backspace", text_input::Backspace, Some("TextInput")),
            KeyBinding::new("delete", text_input::Delete, Some("TextInput")),
            KeyBinding::new("left", text_input::Left, Some("TextInput")),
            KeyBinding::new("right", text_input::Right, Some("TextInput")),
            KeyBinding::new("shift-left", text_input::SelectLeft, Some("TextInput")),
            KeyBinding::new("shift-right", text_input::SelectRight, Some("TextInput")),
            KeyBinding::new("ctrl-a", text_input::SelectAll, Some("TextInput")),
            KeyBinding::new("home", text_input::Home, Some("TextInput")),
            KeyBinding::new("end", text_input::End, Some("TextInput")),
            KeyBinding::new("shift-enter", text_input::Newline, Some("TextInput")),
            KeyBinding::new("up", text_input::Up, Some("TextInput")),
            KeyBinding::new("down", text_input::Down, Some("TextInput")),
            KeyBinding::new("ctrl-v", text_input::Paste, Some("TextInput")),
            KeyBinding::new("ctrl-c", text_input::Copy, Some("TextInput")),
            KeyBinding::new("ctrl-x", text_input::Cut, Some("TextInput")),
            KeyBinding::new("ctrl-d", SplitRight, None),
            KeyBinding::new("ctrl-shift-d", SplitDown, None),
            KeyBinding::new("ctrl-w", ClosePane, None),
            KeyBinding::new("ctrl-]", NextPane, None),
            KeyBinding::new("ctrl-b", ToggleLeft, None),
            KeyBinding::new("ctrl-4", CycleIcons, None),
            KeyBinding::new("alt-1", GoSessions, None),
            KeyBinding::new("alt-2", GoMedia, None),
            KeyBinding::new("alt-3", GoUsage, None),
            KeyBinding::new("alt-4", GoAgents, None),
            KeyBinding::new("alt-5", GoSettings, None),
            KeyBinding::new("alt-6", GoSearch, None),
            KeyBinding::new("alt-7", GoAutomations, None),
            KeyBinding::new("alt-8", GoFleet, None),
            KeyBinding::new("alt-9", GoLive, None),
            KeyBinding::new("ctrl-k", GoSearch, None),
            KeyBinding::new("enter", SearchRun, Some("Search")),
        ]);

        let bounds = Bounds::centered(None, size(px(1480.), px(900.)), cx);
        let window = cx
            .open_window(
                WindowOptions {
                    window_bounds: Some(WindowBounds::Windowed(bounds)),
                    titlebar: Some(TitlebarOptions {
                        title: Some("Swarm Console".into()),
                        ..Default::default()
                    }),
                    app_id: Some("swarm-console".into()),
                    ..Default::default()
                },
                |_, cx| cx.new(Console::new),
            )
            .expect("open window");

        window
            .update(cx, |view, window, cx| {
                window.focus(&view.focus_handle(cx));
                cx.activate(true);
            })
            .ok();
    });
}
