//! The console window: sidebars, split session panes and the status bar.

use gpui::{
    Animation, AnimationExt, AnyElement, ClickEvent, Context, Div, Entity, FocusHandle, Focusable, Hsla, SharedString, Stateful, Subscription, Window, actions, div, prelude::*,
    pulsating_between, px, svg,
};

use crate::data::{self, Entry, Health, Machine, Media, Project, Role, RunState, Session, TaskStatus, ToolState, Usage};
use crate::icons::{Icon, IconStyle};
use crate::layout::{Axis, Layout, PaneId};
use crate::onboarding::{Onboarding, OnboardingEvent};
use crate::password_prompt::PasswordPrompt;
use crate::swarm::target::Target;
use crate::theme::{Concept, Theme};
use std::collections::HashSet;
use std::time::Duration;

mod agents;
mod at_work;
mod attach;
mod automations;
mod background;
mod commands;
mod companion;
mod context;
mod earlier;
mod fleet;
mod fleet_keys;
mod fleet_live;
mod folders;
mod live;
mod machine_calls;
mod markdown;
mod media;
mod models;
mod nav;
mod panes;
mod permissions;
mod plan_review;
mod popout;
mod projects;
mod providers;
mod search;
mod select;
mod settings;
mod shield;
mod sidebar;
mod usage;

use crate::swarm::client::Client;
use crate::swarm::live::Live;
use crate::text_input::TextInput;
use live::Act;
use std::collections::HashMap;

/// Where the console's data comes from.
#[derive(Clone, PartialEq)]
enum Link {
    /// The design set (SWARM_CONSOLE_DEMO).
    Demo,
    Connecting,
    Live,
    /// Swarm stopped answering; the text says why.
    Lost(SharedString),
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Page {
    /// The person's one persistent Companion and its work, beside the
    /// project Chats (Sessions).
    Companion,
    Sessions,
    Media,
    Usage,
    /// The whole fleet: every machine, what needs you, spend.
    Fleet,
    /// Console mode: everything happening across the fleet, live.
    Live,
    Agents,
    /// The account's rules: this device, other devices, home, linked machines.
    Permissions,
    Automations,
    Settings,
    Search,
    /// Reached from the project menu, not the nav.
    Projects,
}

impl Page {
    const ALL: [Page; 11] =
        [Page::Companion, Page::Sessions, Page::Media, Page::Usage, Page::Fleet, Page::Live, Page::Agents, Page::Permissions, Page::Automations, Page::Settings, Page::Search];

    fn label(self) -> &'static str {
        match self {
            Page::Companion => "Companion",
            Page::Sessions => "Sessions",
            Page::Media => "Media",
            Page::Usage => "Usage",
            Page::Fleet => "Fleet",
            Page::Live => "Live",
            Page::Agents => "Agents",
            Page::Permissions => "Permissions",
            Page::Automations => "Automations",
            Page::Settings => "Settings",
            Page::Search => "Search",
            Page::Projects => "Projects",
        }
    }

    fn icon(self) -> Icon {
        match self {
            Page::Companion => Icon::Companion,
            Page::Sessions => Icon::Sessions,
            Page::Media => Icon::Media,
            Page::Usage => Icon::Usage,
            Page::Fleet => Icon::Machine,
            Page::Live => Icon::Live,
            Page::Agents => Icon::Agents,
            Page::Permissions => Icon::Shield,
            Page::Automations => Icon::Tasks,
            Page::Settings => Icon::Settings,
            Page::Search => Icon::Search,
            Page::Projects => Icon::Project,
        }
    }

    /// What the left sidebar (and its collapsed rail) holds under the
    /// navigation: the Companion's work in place of the project's sessions on
    /// the Companion page, the sessions everywhere else. One sidebar, never
    /// both lists, and no work column on the right.
    pub(super) fn left_body(self) -> LeftBody {
        match self {
            Page::Companion => LeftBody::CompanionWork,
            _ => LeftBody::Sessions,
        }
    }
}

/// The left sidebar's list under the navigation (Page::left_body).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(super) enum LeftBody {
    /// The current project's sessions, with new session and pick/pin/archive.
    Sessions,
    /// The Companion's work (needs you, running, results) and its details.
    CompanionWork,
}

/// Below this window width the left sidebar is always the rail.
pub(super) const NARROW_WIDTH: f32 = 760.;

/// How wide the Companion's work detail is when it opens over the
/// conversation from the rail (never wider than the room beside the rail).
pub(super) const RAIL_DETAIL_WIDTH: f32 = 300.;

/// What the left of the main area shows (Console::left_layout).
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(super) enum LeftLayout {
    /// The full sidebar: navigation and the page's list (LeftBody).
    Sidebar,
    /// The icon rail alone.
    Rail,
    /// The rail, with the Companion's open work item (its detail and
    /// approve/reject/stop) drawn over the conversation beside it, so a
    /// collapsed or narrow window can still act on it.
    RailWithCompanionDetail,
}

impl Console {
    /// The left layout for a page, the person's collapse choice, the window
    /// width and whether a Companion work item is open. Below NARROW_WIDTH
    /// the sidebar is always the rail; on the Companion page an open item
    /// then shows over the conversation instead of being unreachable.
    pub(super) fn left_layout(page: Page, collapsed: bool, width: f32, detail_open: bool) -> LeftLayout {
        if !collapsed && width >= NARROW_WIDTH {
            LeftLayout::Sidebar
        } else if page.left_body() == LeftBody::CompanionWork && detail_open {
            LeftLayout::RailWithCompanionDetail
        } else {
            LeftLayout::Rail
        }
    }
}

/// What the Agents inspector shows.
#[derive(Clone, Copy, PartialEq, Eq)]
enum AgentsFocus {
    Fleet,
    Machine(usize),
    Role(usize),
    Cell(usize, usize),
}

actions!(
    console,
    [
        SplitRight,
        SplitDown,
        ClosePane,
        NextPane,
        ToggleLeft,
        CycleIcons,
        GoSessions,
        GoMedia,
        GoUsage,
        GoAgents,
        GoFleet,
        GoLive,
        GoSettings,
        Send,
        Complete,
        CopySelection,
        RenameSave,
        RenameCancel,
        GoSearch,
        SearchRun,
        GoAutomations,
        GoCompanion,
    ]
);

struct Pane {
    id: PaneId,
    session: usize,
    /// The bar above the composer is open, and on which tab.
    tray: bool,
    tab: background::BarTab,
    /// The conversation's scroll position. While it sits at the bottom it
    /// follows new text; scrolled up, it stays where you put it.
    scroll: gpui::ScrollHandle,
    /// What it drew last, for loading earlier messages (earlier.rs).
    earlier: earlier::PaneEarlier,
}

pub struct Console {
    focus: FocusHandle,
    concept: Concept,
    theme: Theme,
    machines: Vec<Machine>,
    sessions: Vec<Session>,
    projects: Vec<Project>,
    /// The project the left sidebar lists.
    project: usize,
    project_menu: bool,
    /// Every open pane: the main window's (those in `layout`) and those
    /// popped out into their own window (see popout.rs).
    panes: Vec<Pane>,
    /// Earlier pages asked of Swarm and not answered yet, by session id,
    /// with the session's paging as it was then (earlier.rs). Asked while
    /// drawing, hence the cell.
    older_asked: std::cell::RefCell<HashMap<data::Text, data::Earlier>>,
    /// The main window's split panes and focus; empty when none is open.
    layout: Layout,
    /// Collapsed: the left sidebar shrinks to an icon rail.
    left_collapsed: bool,
    /// The status bar's fleet list is open.
    fleet_open: bool,
    /// The Search page: its field, scope, what Swarm found, and a found
    /// conversation waiting to open once the list has it.
    search_input: Option<Entity<TextInput>>,
    search_mode: search::SearchMode,
    search_results: Vec<data::Found>,
    search_busy: bool,
    search_error: Option<String>,
    search_seq: u64,
    pending_open: Option<String>,
    /// Live sessions Swarm no longer lists (archived elsewhere): kept for
    /// their open panes, out of the sidebar.
    gone: std::collections::HashSet<String>,
    /// The Projects page's open tab (a project index; none means the open
    /// project).
    projects_tab: Option<String>,
    delete_project_confirm: Option<String>,
    deleted_project: Option<String>,
    /// A project being renamed or whose project.md is being edited (its
    /// ID), the project.md field, and a project to select once listed.
    project_rename: Option<String>,
    context_edit: Option<String>,
    folders: folders::FolderState,
    context_input: Option<Entity<TextInput>>,
    pending_project: Option<String>,
    /// Sessions picked in the sidebar for one action, by session id.
    picked: std::collections::HashSet<String>,
    /// The session being renamed (its index) and the field for its name.
    renaming: Option<usize>,
    rename_input: Option<Entity<TextInput>>,
    /// None follows the concept's icon style.
    icon_override: Option<IconStyle>,
    /// Tasks whose background sessions are listed, as (session, task).
    expanded: HashSet<(usize, usize)>,
    /// Sessions whose tray lists every open task, and whose finished tasks are unfolded.
    tray_all: HashSet<usize>,
    tray_done: HashSet<usize>,
    /// Finished tasks' changes as read from Swarm, by task id.
    page: Page,
    roles: Vec<Role>,
    media: Vec<Media>,
    media_filter: Option<data::MediaKind>,
    usage: Usage,
    agents_focus: AgentsFocus,
    /// The day hovered in the Usage chart.
    usage_hover: Option<usize>,
    /// The Permissions page: Swarm's last answer (GET /v3/permissions), a
    /// read under way, the last refusal or failure, and the rule and value a
    /// click is saving until Swarm answers.
    permissions: Option<serde_json::Value>,
    permissions_busy: bool,
    permissions_error: Option<(bool, String)>,
    permission_saving: Option<(crate::swarm::permissions::Rule, String)>,
    /// The model picker, when open, and its search box.
    picker: Option<models::Picker>,
    picker_query: Option<Entity<TextInput>>,
    /// The Settings page's providers and keys, Swarm's record.
    providers: Vec<data::Provider>,
    providers_loaded: bool,
    /// What the providers page waits on, and the last refusal.
    provider_busy: Option<String>,
    provider_error: Option<String>,
    /// What the last verify said (Swarm keeps no record of it).
    provider_note: Option<String>,
    /// The provider whose add-key form is open, and its two fields.
    key_for: Option<String>,
    /// The person chose to name the key (the name field shows).
    key_named: bool,
    key_label: Option<Entity<TextInput>>,
    key_value: Option<Entity<TextInput>>,
    /// A ChatGPT sign-in in progress: login id, page, code.
    chatgpt: Option<(String, String, String)>,
    /// The name of a favorite being saved.
    picker_name: Option<Entity<TextInput>>,
    /// Parsed markdown per pane entry (see markdown()).
    md_cache: std::cell::RefCell<HashMap<String, (u64, std::rc::Rc<Vec<markdown::Block>>)>>,
    /// Models to pick from, as Swarm lists them.
    catalog: crate::swarm::models::Catalog,
    /// The machines home controls, read from home (fleet::Fleet); after home in machines.
    fleet: Option<crate::swarm::fleet::Fleet>,
    /// What needs the person on home (first) and each linked machine, from
    /// the fleet reader; empty until its first tick.
    needs: Vec<crate::swarm::needs::Reading>,
    /// An item to open once this window is connected (a machine window
    /// opened from the Fleet page).
    focus_on_connect: Option<crate::swarm::needs::Target>,
    /// A linked machine this window shows, through home's link to it
    /// (/v3/fleet/machines/NAME/api); None: home itself.
    via: Option<String>,
    /// Home's client, for its fleet routes (link, unlink, the list).
    home: Option<Client>,
    /// The link code being pasted on the Agents page.
    link_code: Option<Entity<TextInput>>,
    /// Text selected in a conversation, and whether the mouse is dragging it.
    selection: Option<select::Selection>,
    selecting: bool,
    /// The media shown large over the window (index into media).
    preview: Option<usize>,
    /// A waiting task's plan review over the window (plan_review.rs).
    plan_review: Option<plan_review::PlanReviewPanel>,
    /// What to change in a plan sent back.
    plan_feedback: Option<Entity<TextInput>>,
    /// The decision in flight accepts (else it sends the plan back).
    plan_accepting: bool,
    /// Files waiting in each pane's message box (pane id).
    attachments: HashMap<usize, Vec<data::Attachment>>,
    /// Calls to your machine already answered (by id), so none runs twice.
    answered: HashSet<data::Text>,
    /// Folders the Orchestrator asked to ready that have files: their review
    /// and which files are ticked, by request id.
    reviews: HashMap<data::Text, (crate::swarm::repository::Review, Vec<bool>)>,
    /// Browser actions the extension found critical, waiting for you: the
    /// request id and why.
    holds: HashMap<data::Text, String>,
    /// The Automations page: what the home machine has, whether a read is
    /// under way and when the last one finished.
    automations: Option<Result<crate::swarm::automations::Board, String>>,
    automations_busy: bool,
    automations_at: u64,
    /// A read was asked for while one ran: read again after it.
    automations_again: bool,
    /// Each linked machine's proposals waiting for the person, by machine
    /// name, read through the home (GET /v3/proposals?machine=).
    linked_proposals: HashMap<String, Result<Vec<crate::swarm::proposals::Proposal>, String>>,
    /// Linked machines whose proposals are being read (each on its own task,
    /// apart from the board), and those asked for again while that ran.
    linked_proposals_busy: HashSet<String>,
    linked_proposals_again: HashSet<String>,
    /// Failed runs already known (None before the first read), so only a new
    /// failure is announced.
    runs_failed_seen: Option<HashSet<String>>,
    /// The run whose own view is open, and its actions.
    run_open: Option<automations::RunOpen>,
    run_entries: Option<Result<Vec<data::Entry>, String>>,
    /// First run, shown instead of the console until it finishes.
    onboarding: Option<Entity<Onboarding>>,
    /// Focus the console on the next render (after onboarding closes).
    pending_focus: bool,
    _onboarding_done: Option<Subscription>,
    link: Link,
    live: Option<Live>,
    _live: Option<gpui::Task<()>>,
    /// Each pane's message box, by pane id.
    composers: HashMap<usize, Entity<TextInput>>,
    /// Each pane's composer bottom row width at its last layout (px), so the
    /// row drops what does not fit (`data::composer_fit`).
    composer_widths: std::rc::Rc<std::cell::RefCell<HashMap<usize, f32>>>,
    /// New sessions waiting for Swarm's receipt: draft id → session index.
    drafts: HashMap<u64, usize>,
    next_draft: u64,
    /// Newest-first position of each live session (by task id).
    rank: HashMap<SharedString, usize>,
    /// The last command's outcome, for the status bar (text, is an error).
    notice: Option<(SharedString, bool)>,
    /// The open project's theme in Swarm, as Settings names it.
    theme_name: SharedString,
    /// Themes Swarm returned, by id (a project's theme_id, "" for the account's).
    project_themes: HashMap<String, serde_json::Value>,
    /// When the running timers last redrew.
    ticked: std::time::Instant,
    /// The session the last command acted on, and the full error it ended with.
    acting: Option<usize>,
    failure: Option<(usize, SharedString)>,
    /// Answers picked so far, by pending request id and question id.
    answers: HashMap<SharedString, HashMap<SharedString, String>>,
    /// The question whose "Custom response" waits for your typed answer.
    custom_for: Option<(SharedString, SharedString)>,
    /// The Live page: the fleet stream, its lines and filters.
    live_page: fleet_live::LivePage,
    /// The fleet's agents at work (GET /v3/fleet/agents on the home): the
    /// composer's count and the Live page's panel.
    at_work: at_work::AtWork,
    /// The Fleet page's provider keys (the home's fleet key manager).
    keys_page: fleet_keys::KeysPage,
    /// Model settings being applied to linked machines from the Agents page.
    model_apply: agents::ModelApply,
    /// The password prompt root steps started here use (adding folders).
    password: Entity<PasswordPrompt>,
    /// Each conversation's context as Swarm last reported it, and compactions.
    contexts: context::Contexts,
    /// The Companion page: Swarm's answer about the Companion, its work and
    /// what the person has open there (companion.rs).
    companion: companion::CompanionPage,
    /// Put the keyboard in the Companion's message box on the next render.
    pending_focus_companion: bool,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Open {
    Here,
    Right,
    Down,
}

/// Logs how long a scope took (SWARM_CONSOLE_TRACE), when it ends.
struct Timed(&'static str, std::time::Instant);

impl Timed {
    fn new(what: &'static str) -> Self {
        Timed(what, std::time::Instant::now())
    }
}

impl Drop for Timed {
    fn drop(&mut self) {
        if std::env::var_os("SWARM_CONSOLE_TRACE").is_some() {
            let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis()).unwrap_or(0);
            eprintln!("timed {now} {} {:?}", self.0, self.1.elapsed());
        }
    }
}

fn alpha(color: Hsla, a: f32) -> Hsla {
    Hsla { a, ..color }
}

/// A hover tooltip of one line of text (a shortened badge's full text).
struct TextTooltip {
    text: SharedString,
    bg: Hsla,
    fg: Hsla,
    border: Hsla,
}

impl Render for TextTooltip {
    fn render(&mut self, _: &mut Window, _: &mut Context<Self>) -> impl IntoElement {
        div().px_2().py_1().rounded(px(4.)).border_1().border_color(self.border).bg(self.bg).text_color(self.fg).text_size(px(11.)).whitespace_nowrap().child(self.text.clone())
    }
}

impl Console {
    pub fn new(cx: &mut Context<Self>) -> Self {
        Self::build(None, cx)
    }

    /// A window on a machine home has linked, through home's link to it,
    /// that opens `target` (an item from the Fleet page) once connected.
    pub fn for_machine_at(name: String, target: Option<crate::swarm::needs::Target>, cx: &mut Context<Self>) -> Self {
        let mut console = Self::build(Some(name), cx);
        console.focus_on_connect = target;
        console
    }

    fn build(via: Option<String>, cx: &mut Context<Self>) -> Self {
        let concept = std::env::var("SWARM_CONSOLE_CONCEPT").ok().and_then(|v| Concept::ALL.into_iter().find(|c| c.label() == v)).unwrap_or(Concept::Graphite);
        let demo = std::env::var("SWARM_CONSOLE_DEMO").is_ok();
        let target = Target::from_env().unwrap_or(Target::Local);
        let mut console = Console {
            focus: cx.focus_handle(),
            concept,
            theme: Theme::for_concept(concept),
            machines: if demo { data::machines() } else { vec![live::placeholder_machine(&target)] },
            sessions: if demo { data::sessions() } else { vec![live::draft(0)] },
            projects: if demo { data::projects() } else { vec![live::placeholder_project()] },
            project: 0,
            project_menu: false,
            panes: vec![Pane { id: PaneId(0), session: 0, tray: false, tab: background::BarTab::Tasks, scroll: gpui::ScrollHandle::new(), earlier: Default::default() }],
            older_asked: Default::default(),
            layout: Layout::single(),
            left_collapsed: false,
            fleet_open: false,
            search_input: None,
            search_mode: search::SearchMode::Active,
            search_results: Vec::new(),
            search_busy: false,
            search_error: None,
            search_seq: 0,
            pending_open: None,
            gone: std::collections::HashSet::new(),
            projects_tab: None,
            delete_project_confirm: None,
            deleted_project: None,
            project_rename: None,
            context_edit: None,
            folders: Default::default(),
            context_input: None,
            pending_project: None,
            picked: std::collections::HashSet::new(),
            renaming: None,
            rename_input: None,
            icon_override: std::env::var("SWARM_CONSOLE_ICONS").ok().and_then(|v| IconStyle::ALL.into_iter().find(|s| s.label() == v)),
            expanded: HashSet::new(),
            tray_all: HashSet::new(),
            tray_done: HashSet::new(),
            page: Page::Sessions,
            roles: if demo { data::roles() } else { vec![] },
            media: if demo { data::media() } else { vec![] },
            media_filter: None,
            usage: if demo { data::usage() } else { data::Usage::default() },
            agents_focus: AgentsFocus::Fleet,
            usage_hover: None,
            permissions: None,
            permissions_busy: false,
            permissions_error: None,
            permission_saving: None,
            picker: None,
            picker_query: None,
            providers: Vec::new(),
            providers_loaded: false,
            provider_busy: None,
            provider_error: None,
            provider_note: None,
            key_for: None,
            key_named: false,
            key_label: None,
            key_value: None,
            chatgpt: None,
            picker_name: None,
            md_cache: Default::default(),
            catalog: Default::default(),
            answered: HashSet::new(),
            reviews: HashMap::new(),
            holds: HashMap::new(),
            automations: None,
            automations_busy: false,
            automations_at: 0,
            automations_again: false,
            linked_proposals: HashMap::new(),
            linked_proposals_busy: HashSet::new(),
            linked_proposals_again: HashSet::new(),
            runs_failed_seen: None,
            run_open: None,
            run_entries: None,
            attachments: HashMap::new(),
            preview: None,
            plan_review: None,
            plan_feedback: None,
            plan_accepting: false,
            selection: None,
            fleet: None,
            needs: Vec::new(),
            focus_on_connect: None,
            via: via.clone(),
            home: None,
            link_code: None,
            selecting: false,
            onboarding: None,
            pending_focus: false,
            _onboarding_done: None,
            link: if demo { Link::Demo } else { Link::Connecting },
            live: None,
            _live: None,
            composers: HashMap::new(),
            composer_widths: Default::default(),
            drafts: HashMap::new(),
            next_draft: 0,
            rank: HashMap::new(),
            notice: None,
            theme_name: "default (until the project's theme loads)".into(),
            project_themes: HashMap::new(),
            ticked: std::time::Instant::now(),
            acting: None,
            failure: None,
            answers: HashMap::new(),
            custom_for: None,
            live_page: Default::default(),
            at_work: Default::default(),
            keys_page: Default::default(),
            model_apply: Default::default(),
            password: cx.new(|cx| PasswordPrompt::new(Theme::for_concept(concept), cx)),
            contexts: Default::default(),
            companion: Default::default(),
            pending_focus_companion: false,
        };
        if demo {
            // Three panes: one beside a stack of two, to show both split directions.
            console.open(1, Open::Right);
            console.open(2, Open::Down);
            console.layout.focus(PaneId(0));
        }
        // Screenshot and demo overrides.
        if let Ok(page) = std::env::var("SWARM_CONSOLE_PAGE") {
            if let Some(p) = Page::ALL.into_iter().find(|p| p.label().eq_ignore_ascii_case(&page)) {
                console.page = p;
            }
        }
        console.left_collapsed = std::env::var("SWARM_CONSOLE_RAIL").is_ok();
        // First run: without a working key for the machine, onboarding comes first.
        let forced = std::env::var("SWARM_CONSOLE_ONBOARDING").is_ok() || std::env::var("SWARM_CONSOLE_ONBOARDING_DEMO").is_ok();
        let skipped = std::env::var("SWARM_CONSOLE_SKIP_ONBOARDING").is_ok();
        if via.is_some() {
            // Home's session reaches the machine; there is nothing to onboard.
            console.machines = vec![live::placeholder_machine(&target)];
            console.machines[0].name = via.clone().unwrap_or_default().into();
            console.machines[0].kind = "linked".into();
            console.connect_live(cx);
        } else if forced || (!skipped && target.stored_token().is_none()) {
            let (theme, icons) = (console.theme.clone(), console.icon_style());
            let onboarding = cx.new(|cx| Onboarding::new(theme, icons, target, cx));
            console._onboarding_done = Some(cx.subscribe(&onboarding, |this, _, _: &OnboardingEvent, cx| {
                this.onboarding = None;
                this._onboarding_done = None;
                this.pending_focus = true;
                if this.link != Link::Demo {
                    this.connect_live(cx);
                }
                cx.notify();
            }));
            console.onboarding = Some(onboarding);
        } else if !demo {
            console.connect_live(cx);
        }
        if std::env::var("SWARM_CONSOLE_FOCUS_CELL").is_ok() {
            console.agents_focus = AgentsFocus::Cell(2, 1);
        }
        if std::env::var("SWARM_CONSOLE_TRAY").is_ok() {
            if let Some(p) = console.panes.first_mut() {
                p.tray = true;
            }
            console.expanded.insert((0, 0));
        }
        console
    }

    /// The app takes the open project's theme from Swarm (set with
    /// manage-theme); until Swarm answers, the base colors.
    fn follow_theme(&mut self, cx: &mut Context<Self>) {
        let Some(project) = self.projects.get(self.project) else { return };
        let Some(theme) = self.project_themes.get(project.theme_id.as_ref()) else { return };
        let Some(palette) = theme.get("palette") else { return };
        let next = Theme::from_palette(palette, &Theme::for_concept(self.concept));
        let name = theme.get("name").and_then(|n| n.as_str()).unwrap_or("").to_string();
        if self.theme_name.as_ref() == name && self.theme.bg == next.bg && self.theme.accent == next.accent && self.theme.text == next.text {
            return;
        }
        self.theme = next;
        self.theme_name = name.into();
        let colors = crate::onboarding::input_colors(&self.theme);
        for input in self.composers.values() {
            input.update(cx, |i, _| i.set_colors(colors));
        }
        if let Some(onboarding) = self.onboarding.clone() {
            let (theme, icons) = (self.theme.clone(), self.icon_style());
            onboarding.update(cx, |o, cx| o.set_theme(theme, icons, cx));
        }
        let theme = self.theme.clone();
        self.password.update(cx, |p, cx| p.set_theme(theme, cx));
        cx.notify();
    }

    fn active_session(&self) -> usize {
        self.layout.active().and_then(|a| self.panes.iter().find(|p| p.id == a)).map(|p| p.session).unwrap_or(0)
    }

    fn is_open(&self, session: usize) -> bool {
        self.panes.iter().any(|p| p.session == session)
    }

    /// Shows `session` in the main window: in the focused pane (Here) or a
    /// new pane beside or below it. With no pane open, any of them opens
    /// the first pane.
    fn open(&mut self, session: usize, how: Open) {
        if how == Open::Here {
            if let Some(active) = self.layout.active() {
                if let Some(pane) = self.panes.iter_mut().find(|p| p.id == active) {
                    pane.session = session;
                }
                return;
            }
        }
        let axis = if how == Open::Down { Axis::Column } else { Axis::Row };
        let id = self.layout.alloc();
        if self.layout.place(id, None, axis, false) {
            self.panes.push(Pane { id, session, tray: false, tab: background::BarTab::Tasks, scroll: gpui::ScrollHandle::new(), earlier: Default::default() });
        }
    }

    /// Closes a pane of the main window; the last one leaves it empty.
    fn close(&mut self, id: PaneId) {
        if self.layout.close(id) {
            self.panes.retain(|p| p.id != id);
            // The keyboard goes to the pane focused now, or the console.
            self.pending_focus = true;
        }
    }

    fn set_task(&mut self, session: usize, task: usize, status: TaskStatus) {
        if let Some(t) = self.sessions.get_mut(session).and_then(|s| s.tasks.get_mut(task)) {
            t.status = status;
        }
    }

    // Actions

    fn split_right(&mut self, _: &SplitRight, _: &mut Window, cx: &mut Context<Self>) {
        self.new_pane(self.layout.active().map(|a| a.0), Open::Right, cx);
    }
    fn split_down(&mut self, _: &SplitDown, _: &mut Window, cx: &mut Context<Self>) {
        self.new_pane(self.layout.active().map(|a| a.0), Open::Down, cx);
    }
    fn close_pane(&mut self, _: &ClosePane, _: &mut Window, cx: &mut Context<Self>) {
        if let Some(active) = self.layout.active() {
            self.close(active);
            cx.notify();
        }
    }
    fn next_pane(&mut self, _: &NextPane, _: &mut Window, cx: &mut Context<Self>) {
        if self.layout.cycle().is_some() {
            cx.notify();
        }
    }
    fn toggle_left(&mut self, _: &ToggleLeft, _: &mut Window, cx: &mut Context<Self>) {
        self.left_collapsed = !self.left_collapsed;
        cx.notify();
    }
    fn go(&mut self, page: Page, cx: &mut Context<Self>) {
        if page == Page::Companion {
            self.open_companion(cx);
            return;
        }
        self.page = page;
        if page == Page::Settings {
            self.load_providers(cx);
        }
        if page == Page::Search {
            self.run_search(cx);
        }
        if page == Page::Automations {
            self.load_automations(cx);
        }
        if page == Page::Permissions || page == Page::Agents {
            self.load_permissions();
        }
        if page == Page::Fleet {
            self.load_fleet_keys(cx);
        }
        cx.notify();
    }
    fn go_sessions(&mut self, _: &GoSessions, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Sessions, cx);
    }
    fn go_companion(&mut self, _: &GoCompanion, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Companion, cx);
    }
    fn go_media(&mut self, _: &GoMedia, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Media, cx);
    }
    fn go_usage(&mut self, _: &GoUsage, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Usage, cx);
    }
    fn go_agents(&mut self, _: &GoAgents, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Agents, cx);
    }
    fn go_automations(&mut self, _: &GoAutomations, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Automations, cx);
    }
    fn go_fleet(&mut self, _: &GoFleet, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Fleet, cx);
    }
    fn go_live(&mut self, _: &GoLive, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Live, cx);
    }
    fn go_settings(&mut self, _: &GoSettings, _: &mut Window, cx: &mut Context<Self>) {
        self.go(Page::Settings, cx);
    }
    fn cycle_icons(&mut self, _: &CycleIcons, _: &mut Window, cx: &mut Context<Self>) {
        self.icon_override = Some(self.icon_style().next());
        cx.notify();
    }

    // Small building blocks

    fn state_glyph(&self, state: RunState) -> (&'static str, Hsla) {
        let t = &self.theme;
        match state {
            RunState::Running => ("●", t.ok),
            RunState::NeedsYou => ("◆", t.warn),
            RunState::Waiting => ("◷", t.accent),
            RunState::Idle => ("○", t.faint),
            RunState::Failed => ("✕", t.err),
        }
    }

    fn task_glyph(&self, status: TaskStatus) -> (&'static str, Hsla) {
        let t = &self.theme;
        match status {
            TaskStatus::PendingApproval => ("◆", t.warn),
            TaskStatus::NeedsInput => ("?", t.warn),
            TaskStatus::NeedsReview => ("◇", t.info),
            TaskStatus::Queued => ("○", t.faint),
            TaskStatus::Running => ("●", t.ok),
            TaskStatus::Completed => ("✓", t.muted),
            TaskStatus::Failed => ("✕", t.err),
            TaskStatus::Rejected => ("–", t.faint),
        }
    }

    fn health_color(&self, health: Health) -> Hsla {
        match health {
            Health::Ok => self.theme.ok,
            Health::Attention => self.theme.warn,
            Health::Degraded => self.theme.err,
            Health::Unknown | Health::Offline => self.theme.faint,
        }
    }

    fn section(&self, label: &'static str, count: Option<usize>) -> Div {
        let t = &self.theme;
        div().flex().items_center().justify_between().px_3().pt_4().pb_1().text_color(t.faint).text_size(px(10.5)).child(label).children(count.map(|n| div().child(n.to_string())))
    }

    fn button(&self, id: impl Into<SharedString>, label: impl Into<SharedString>, strong: bool) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        let base = div().id(id.into()).px_2().py(px(1.)).rounded(px(t.radius)).border_1().cursor_pointer().text_size(px(11.)).child(label.into());
        if strong {
            let accent = t.accent;
            base.bg(t.accent).border_color(t.accent).text_color(t.accent_text).hover(move |s| s.bg(alpha(accent, 0.85)))
        } else {
            base.border_color(t.border_strong).text_color(t.muted).hover(move |s| s.bg(hover))
        }
    }

    pub(super) fn icon_style(&self) -> IconStyle {
        // Line icons unless the person picks another style in Settings.
        self.icon_override.unwrap_or(IconStyle::Line)
    }

    /// An icon in a given style, tinted with `color`.
    pub(super) fn icon_in(&self, style: IconStyle, icon: Icon, size: f32, color: Hsla) -> AnyElement {
        match style {
            IconStyle::Glyph => {
                div().flex_none().w(px(size)).flex().justify_center().text_size(px(size)).line_height(px(size)).text_color(color).child(icon.glyph()).into_any_element()
            }
            _ => svg().path(icon.path(style)).flex_none().size(px(size)).text_color(color).into_any_element(),
        }
    }

    /// An icon in the current style.
    pub(super) fn icon(&self, icon: Icon, size: f32, color: Hsla) -> AnyElement {
        self.icon_in(self.icon_style(), icon, size, color)
    }

    /// An icon button with its name on hover.
    fn icon_button_tip(&self, id: impl Into<SharedString>, icon: Icon, tip: &'static str) -> Stateful<Div> {
        let (bg, fg, border) = (self.theme.surface, self.theme.text, self.theme.border);
        self.icon_button(id, icon).tooltip(move |_, cx| cx.new(|_| TextTooltip { text: tip.into(), bg, fg, border }).into())
    }

    fn icon_button(&self, id: impl Into<SharedString>, icon: Icon) -> Stateful<Div> {
        let t = &self.theme;
        let hover = t.hover;
        div()
            .id(id.into())
            .w(px(22.))
            .h(px(20.))
            .flex()
            .items_center()
            .justify_center()
            .rounded(px(t.radius))
            .cursor_pointer()
            .hover(move |s| s.bg(hover))
            .child(self.icon(icon, 14., t.faint))
    }

    // Title bar and status bar

    fn render_statusbar(&self, hints: bool, cx: &mut Context<Self>) -> Div {
        let t = &self.theme;
        let needs: usize = self
            .sessions
            .iter()
            .map(|s| {
                s.tasks.iter().filter(|x| x.status.needs_you()).count()
                    + usize::from(s.status.is_some_and(|x| x.needs_you()))
                    + s.calls.iter().filter(|c| c.tool.starts_with("request_")).count()
            })
            .sum::<usize>()
            + self.waiting_workers().len()
            + self.proposed_workflows();
        let (cost, unknown) = crate::data::spend_today(&self.machines);
        let (link, link_bad) = self.link_text();
        let sep = || div().text_color(t.border_strong).child("│");
        div()
            .h(px(24.))
            .flex_none()
            .flex()
            .items_center()
            .justify_between()
            .px_3()
            .border_t_1()
            .border_color(t.border)
            .bg(t.panel)
            .text_size(px(11.))
            .text_color(t.faint)
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().text_color(if link_bad { t.warn } else { t.ok }).child("●"))
                    .child(div().text_color(t.muted).child(link))
                    .child(sep())
                    .child(self.fleet_status(cx))
                    .child(sep())
                    .child(div().text_color(t.warn).child(format!("{needs} need you")))
                    .child(sep())
                    .child(format!("${cost:.2}{} today", if unknown { " + ?" } else { "" })),
            )
            .when_some(self.notice.clone(), |d, (text, bad)| {
                d.child(
                    div().flex_1().min_w_0().pl_4().overflow_hidden().whitespace_nowrap().text_ellipsis().text_right().text_color(if bad { t.err } else { t.muted }).child(text),
                )
            })
            .when(hints && self.notice.is_none(), |d| d.child(div().flex().gap_3().child("ctrl-d split right").child("ctrl-shift-d split down").child("ctrl-w close")))
    }

    // Left sidebar: project switcher, then that project's sessions.

    fn render_left(&self, cx: &mut Context<Self>) -> Div {
        if self.left_collapsed {
            return self.render_rail(cx);
        }
        let t = &self.theme;
        let left = div()
            .w(px(272.))
            .flex_none()
            .flex()
            .flex_col()
            .h_full()
            .bg(t.panel)
            .border_r_1()
            .border_color(t.border)
            .child(div().flex().items_center().justify_end().px_2().pt_2().child(self.icon_button("collapse-left", Icon::Collapse).on_click(cx.listener(
                |this, _: &ClickEvent, _, cx| {
                    this.left_collapsed = true;
                    cx.notify();
                },
            ))))
            .child(self.render_nav(cx));
        if self.page.left_body() == LeftBody::CompanionWork {
            // The Companion's work replaces the sessions list; its own scroll.
            return left
                .child(div().id("left-companion-work").mt_2().flex_1().min_h_0().flex().flex_col().border_t_1().border_color(t.border).child(self.render_companion_work(cx)));
        }
        let active_session = self.active_session();
        let sessions = self.session_rows(active_session, cx);
        left.child(
            div().px_2().pt_2().child(
                self.button("new-session", "", false)
                    .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.new_session(window, cx)))
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(self.icon(Icon::Plus, 12., t.muted))
                    .child("new session")
                    .w_full()
                    .py(px(4.))
                    .flex()
                    .justify_center(),
            ),
        )
        .child(
            div()
                .id("left-scroll")
                .flex_1()
                .min_h_0()
                .overflow_y_scroll()
                .child(self.section("SESSIONS", Some(sessions.len())))
                .children(self.render_picked_bar(cx))
                .children(sessions),
        )
    }

    /// The Companion's open work item over the conversation, beside the rail
    /// (LeftLayout::RailWithCompanionDetail): its detail and actions, with
    /// "‹ all work" closing it. Overlaid so the conversation keeps its width.
    fn render_rail_companion_detail(&self, width: f32, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let rail = 48.;
        let w = RAIL_DETAIL_WIDTH.min((width - rail).max(0.));
        div()
            .id("rail-companion-detail")
            .absolute()
            .top_0()
            .bottom_0()
            .left(px(rail))
            .w(px(w))
            .flex()
            .flex_col()
            .bg(t.panel)
            .border_r_1()
            .border_color(t.border)
            .occlude()
            .child(self.render_companion_work(cx))
            .into_any_element()
    }

    // Session panes

    /// The main window with every pane closed: a line saying so and the
    /// sidebar's new-session button. A session clicked in the sidebar, or
    /// dropped here, opens the first pane again.
    fn render_no_pane(&self, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let tint = alpha(t.accent, 0.12);
        div()
            .id("no-pane")
            .size_full()
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .gap_3()
            .rounded(px(t.radius + 2.))
            .drag_over::<panes::Dragged>(move |s, _, _, _| s.bg(tint))
            .on_drop(cx.listener(|this, d: &panes::Dragged, _, cx| {
                this.open(d.session, Open::Here);
                this.page = Page::Sessions;
                cx.notify();
            }))
            .child(div().text_color(t.faint).child("No session open"))
            .child(
                self.button("no-pane-new-session", "", false)
                    .on_click(cx.listener(|this, _: &ClickEvent, window, cx| this.new_session(window, cx)))
                    .flex()
                    .items_center()
                    .gap_2()
                    .px_3()
                    .py(px(4.))
                    .child(self.icon(Icon::Plus, 12., t.muted))
                    .child("new session"),
            )
            .into_any_element()
    }

    fn render_pane(&self, id: PaneId, cx: &mut Context<Self>) -> AnyElement {
        let _timed = Timed::new("render_pane");
        let t = &self.theme;
        let Some(pane) = self.panes.iter().find(|p| p.id == id) else {
            return div().into_any_element();
        };
        let si = pane.session;
        let s = &self.sessions[si];
        // A page of earlier messages arrived above since the last paint: the
        // view holds on what it showed (earlier::marker), not on the bottom.
        let hold = self.hold(pane, s);
        // Follow the tail: at the bottom at the last paint (or never scrolled),
        // the conversation stays at the bottom as text arrives. Scrolled up, it
        // stays put until you scroll back down.
        if hold.is_none() {
            let (offset, max) = (pane.scroll.offset(), pane.scroll.max_offset());
            if max.height <= px(0.) || offset.y <= -(max.height - px(12.)) {
                pane.scroll.scroll_to_bottom();
            }
            // Near the top: the page before (at most one read at a time).
            self.want_older(id);
        }
        // The Companion's pane (companion.rs) is outside the layout too, but
        // it is no pop-out: it has no close, split, pop-out or drag.
        let companion = id == companion::PANE;
        // A pane in its own window (popout.rs): the main window's layout
        // does not hold it, so it has no splits and no focus there.
        let popped = !companion && !self.layout.contains(id);
        let active = companion || popped || self.layout.active() == Some(id);
        let (glyph, color) = self.state_glyph(s.state);
        let machine = self.machines[s.machine].name.clone();
        let pid = id.0;

        let ghost = self.ghost(s.title.clone());
        let header = div()
            .id(SharedString::from(format!("pane-head-{pid}")))
            .when(!popped && !companion, |d| d.on_drag(panes::Dragged { session: si, pane: Some(pid) }, move |_, _, _, cx| cx.new(|_| ghost.clone())).cursor_grab())
            .flex_none()
            .h(px(32.))
            .flex()
            .items_center()
            .gap_2()
            .px_3()
            .border_b_1()
            .border_color(t.border)
            .bg(t.panel)
            .child(div().text_color(color).child(glyph))
            .child(div().flex_1().min_w(px(72.)).truncate().text_color(if active { t.text } else { t.muted }).child(s.title.clone()))
            .child(div().flex_shrink().min_w_0().truncate().text_size(px(11.)).text_color(t.faint).child({
                let mut parts: Vec<String> = Vec::new();
                if s.project != self.project && !companion {
                    parts.push(self.projects[s.project].name.to_string());
                }
                parts.push(machine.to_string());
                if let Some(wait) = s.waiting_on.as_ref().filter(|_| s.state == RunState::Waiting) {
                    parts.push(wait.line());
                }
                if let Some(run) = s.run {
                    let (this_run, all) = run.times(now_ms());
                    if run.active {
                        parts.push(format!("this run {} · total {}", data::clock(this_run), data::clock(all)));
                    } else if all > 0 {
                        parts.push(format!("total {}", data::clock(all)));
                    }
                } else if !s.elapsed.is_empty() {
                    parts.push(s.elapsed.to_string());
                }
                if !s.tokens.is_empty() {
                    parts.push(format!("{} tok", s.tokens));
                }
                if let Some(status) = s.status {
                    parts.push(status.label().to_string());
                }
                parts.join(" · ")
            }))
            .children(self.context_meter(si, pid, cx))
            .children(self.session_actions(si, pid, cx))
            .when(!popped && !companion, |d| {
                d.child(self.icon_button_tip(SharedString::from(format!("pane-right-{pid}")), Icon::SplitRight, "Split right").on_click(cx.listener(
                    move |this, _: &ClickEvent, _, cx| {
                        this.new_pane(Some(pid), Open::Right, cx);
                    },
                )))
                .child(self.icon_button_tip(SharedString::from(format!("pane-down-{pid}")), Icon::SplitDown, "Split down").on_click(cx.listener(
                    move |this, _: &ClickEvent, _, cx| {
                        this.new_pane(Some(pid), Open::Down, cx);
                    },
                )))
                .child(self.icon_button_tip(SharedString::from(format!("pane-window-{pid}")), Icon::PopOut, "Open in new window").on_click(cx.listener(
                    move |this, _: &ClickEvent, _, cx| {
                        this.pop_out(PaneId(pid), cx);
                    },
                )))
            })
            .when(!companion, |d| {
                d.child(self.icon_button_tip(SharedString::from(format!("pane-close-{pid}")), Icon::Close, if popped { "Close window" } else { "Close" }).on_click(cx.listener(
                    move |this, _: &ClickEvent, window, cx| {
                        if popped {
                            // Its window goes; the pane goes with it (popout.rs).
                            window.remove_window();
                        } else {
                            this.close(PaneId(pid));
                            cx.notify();
                        }
                    },
                )))
            });

        // Consecutive tool calls read as one tight stack, not separate blocks.
        let mut entries: Vec<AnyElement> = Vec::new();
        // Earlier messages loading, or why they did not come: every live
        // conversation, the Companion's as well (earlier.rs).
        entries.extend(self.older_row(pid, s, cx));
        let first = entries.len();
        let mut tools: Vec<AnyElement> = Vec::new();
        let stack = |tools: Vec<AnyElement>| div().flex().flex_col().gap(px(3.)).children(tools).into_any_element();
        for (ei, entry) in s.entries.iter().enumerate() {
            // Where what was shown begins: the page above ends its own stack.
            if let Some(h) = hold.as_ref().filter(|h| h.at == ei) {
                if !tools.is_empty() {
                    entries.push(stack(std::mem::take(&mut tools)));
                }
                pane.scroll.scroll_to_top_of_item(entries.len());
                entries.push(earlier::marker(h.shift));
            }
            let drawn = self.render_entry(pid, ei, entry, cx);
            if matches!(entry, Entry::Tool { .. }) {
                tools.push(drawn);
                continue;
            }
            if !tools.is_empty() {
                entries.push(stack(std::mem::take(&mut tools)));
            }
            entries.push(drawn);
        }
        if !tools.is_empty() {
            entries.push(stack(tools));
        }
        if self.drawing(pane, s, first, hold.is_some()) {
            self.check_after_paint(id, cx);
        }
        // Sent, not in Swarm's record yet.
        let base = s.entries.len();
        entries.extend(s.outbox.iter().enumerate().map(|(i, (text, _))| self.render_entry(pid, base + i, &Entry::User(text.clone()), cx)));
        if s.entries.is_empty() && s.outbox.is_empty() {
            let empty = match (&s.live, &self.link) {
                (Some(_), _) if companion && s.state != RunState::Running => companion::empty_line().to_string(),
                (Some(_), _) => "Reading the conversation…".to_string(),
                (None, Link::Live) => {
                    let p = &self.projects[s.project];
                    let folders = p.workspaces.iter().map(|w| w.rsplit('/').next().unwrap_or("").to_string()).collect::<Vec<_>>().join(", ");
                    format!(
                        "A new conversation with the Orchestrator of {} ({folders}). Say what you want done: it proposes tasks as cards here, they run in the background, and it reports back.",
                        p.name
                    )
                }
                (None, Link::Lost(e)) => format!("Swarm is not answering: {e}"),
                (None, _) => "Connecting to Swarm…".to_string(),
            };
            entries.push(div().px_3().text_color(t.faint).child(empty).into_any_element());
        }
        if let Some(p) = &s.pending {
            entries.push(self.render_pending(si, pid, p, cx));
        }
        entries.extend(self.render_calls(si, pid, cx));
        entries.extend(self.render_worker_reviews(si, cx));
        if let Some((_, text)) = s.activity.first().filter(|_| self.link != Link::Demo) {
            entries.push(div().px_3().text_size(px(11.5)).text_color(if s.state == RunState::NeedsYou { t.warn } else { t.faint }).child(text.clone()).into_any_element());
        }

        let input = self.composers.get(&pid).cloned();
        let typed = input.as_ref().map(|i| i.read(cx).text()).unwrap_or_default();
        let drop_tint = alpha(t.accent, 0.12);
        // While Swarm says the run is active, the send hint becomes its stop button.
        let running = s.live.is_some() && s.state == RunState::Running;
        // A run left by the retired pending wait (Swarm converts these at
        // startup): send stays, and stop cancels it by its own id from the
        // run state, since Swarm's session detail lists no active run for it.
        let waiting = s.live.is_some() && s.state == RunState::Waiting;
        let err = t.err;
        let tip = (t.surface, t.text, t.border);
        let with_tip = move |d: Stateful<Div>, text: &'static str| {
            let (bg, fg, border) = tip;
            d.tooltip(move |_, cx| cx.new(|_| TextTooltip { text: text.into(), bg, fg, border }).into())
        };
        let stop_button = |label: &'static str, name: &'static str, compact: bool| {
            div()
                .id(SharedString::from(format!("composer-stop-{pid}")))
                .flex_none()
                .h(px(18.))
                .flex()
                .items_center()
                .gap_1()
                .px_2()
                .rounded(px(t.radius))
                .border_1()
                .border_color(alpha(t.err, 0.6))
                .text_color(t.err)
                .cursor_pointer()
                .hover(move |s| s.bg(alpha(err, 0.15)))
                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.act(si, Act::Stop, cx)))
                .when(compact, |d| with_tip(d, name))
                .child(if compact { "■" } else { label })
        };
        let send = |compact: bool| {
            div()
                .id(SharedString::from(format!("composer-send-{pid}")))
                .flex_none()
                .h(px(18.))
                .flex()
                .items_center()
                .px_2()
                .border_1()
                .border_color(gpui::transparent_black())
                .when(compact, |d| with_tip(d, "send"))
                .child(if compact { "⏎" } else { "⏎ send" })
        };
        // Watching tasks at a glance, beside send/stop, working or idle. With
        // no watch there is none: the stop button says it is working.
        let badge_full = s.live.as_ref().and(data::composer_badge(s.state, s.waiting_on.as_ref()));
        let badge_short = s.live.as_ref().and(data::composer_badge_short(s.state, s.waiting_on.as_ref()));
        let hint = data::composer_hint(self.link == Link::Demo, s.live.is_some(), s.pending.as_ref().is_some_and(|p| p.question), s.state);
        // The fleet's agents at work (at_work.rs); none until Swarm answered.
        let at_work = self.at_work_label();
        // On a narrow pane "/ commands" goes first, then the agents count
        // shortens, then the hint goes, then the badge shortens; send/stop
        // always shows. Widths are estimates of the
        // row's 10.5px monospaced text plus each item's padding.
        let fit = {
            const FONT: f32 = 10.5;
            const GAP: f32 = 12.;
            // Text plus a button's padding and border.
            const PAD: f32 = 18.;
            let w = |text: &str| data::text_width(text, FONT);
            let model = if !s.model.is_empty() {
                s.model.to_string()
            } else if s.live.is_none() {
                "default model".to_string()
            } else {
                String::new()
            };
            let actions = |stop: &str, send: &str| {
                if running {
                    w(stop) + PAD
                } else if waiting {
                    w(stop) + PAD + 4. + w(send) + PAD
                } else {
                    w(send) + PAD
                }
            };
            let row = data::ComposerRow {
                model: if model.is_empty() { 0. } else { w("◆") + 4. + w(&model) + 8. },
                permissions: 11. + 4. + w("permissions") + 8.,
                hint: if hint.is_empty() { 0. } else { w(hint) },
                commands: w("/ commands"),
                badge: badge_full.as_deref().map_or(0., |b| data::text_width(b, 11.) + PAD),
                badge_short: badge_short.as_deref().map_or(0., |b| data::text_width(b, 11.) + PAD),
                agents: at_work.as_ref().map_or(0., |i| w(&i.full) + 10.),
                agents_short: at_work.as_ref().map_or(0., |i| w(&i.short) + 10.),
                actions: actions(if waiting { "■ stop wait" } else { "■ stop" }, "⏎ send"),
                actions_compact: actions("■", "⏎"),
                gap: GAP,
            };
            // Unknown until the row's first layout: show everything; send/stop
            // stays outside the clipped part, and the next frame fits the rest.
            self.composer_widths.borrow().get(&pid).map_or(data::ComposerFit::ALL, |width| data::composer_fit(*width, row))
        };
        // Send and stop are the same box, so swapping them moves nothing.
        let send_or_stop = if running {
            stop_button("■ stop", "stop", fit.compact).into_any_element()
        } else if waiting {
            div().flex().items_center().gap_1().child(stop_button("■ stop wait", "stop wait", fit.compact)).child(send(fit.compact)).into_any_element()
        } else {
            send(fit.compact).into_any_element()
        };
        // Send stays enabled beside the badge: a message never ends a watch.
        let badge = badge_full.map(|full| {
            let color = t.accent;
            let label = if fit.badge_short { badge_short.clone().unwrap_or_default() } else { full.clone() };
            div()
                .id(SharedString::from(format!("composer-badge-{pid}")))
                .flex_none()
                .h(px(18.))
                .flex()
                .items_center()
                .px_2()
                .rounded(px(t.radius))
                .bg(alpha(color, 0.14))
                .border_1()
                .border_color(alpha(color, 0.5))
                .text_size(px(11.))
                .text_color(color)
                .when(fit.badge_short, |d| {
                    let (bg, fg, border) = tip;
                    d.tooltip(move |_, cx| cx.new(|_| TextTooltip { text: full.clone().into(), bg, fg, border }).into())
                })
                .child(label)
        });
        let widths = self.composer_widths.clone();
        // Records the row's width after layout; a change redraws so the fit
        // follows. GPUI ignores `refresh` while it draws (this runs in
        // prepaint), so the redraw is asked for once the frame is done.
        let measure = gpui::canvas(
            move |bounds, window, _| {
                let width = f32::from(bounds.size.width);
                let old = widths.borrow_mut().insert(pid, width);
                if old.is_none_or(|old| (old - width).abs() >= 0.5) {
                    window.on_next_frame(|window, _| window.refresh());
                }
            },
            |_, _, _, _| {},
        )
        .absolute()
        .size_full();
        let composer = div()
            .flex_none()
            .p_2()
            .border_t_1()
            .border_color(t.border)
            .on_drop(cx.listener(move |this, paths: &gpui::ExternalPaths, _, cx| this.attach_files(pid, paths.paths(), cx)))
            .drag_over::<gpui::ExternalPaths>(move |s, _, _, _| s.bg(drop_tint))
            .children(self.render_model_picker_for_pane(si, pid, cx))
            .children(self.render_commands(pid, &typed, cx))
            .child(
                div()
                    .key_context("Composer")
                    .on_action(cx.listener(move |this, _: &Send, _, cx| this.send(pid, cx)))
                    .on_action(cx.listener(move |this, _: &Complete, _, cx| this.complete(pid, cx)))
                    .flex()
                    .flex_col()
                    .gap_1()
                    .px_2()
                    .py(px(6.))
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(if active { alpha(t.accent, 0.55) } else { t.border })
                    .bg(t.surface)
                    .children(self.render_attachments(pid, cx))
                    .child(div().flex().items_start().gap_2().child(div().h(px(20.)).flex().items_center().text_color(t.accent).child("›")).child(match input {
                        // Grows with the text, up to twelve rows, then scrolls.
                        Some(input) => {
                            div().id(SharedString::from(format!("composer-text-{pid}"))).flex_1().min_w_0().max_h(px(240.)).overflow_y_scroll().text_color(t.text).child(input)
                        }
                        None => {
                            div().id(SharedString::from(format!("composer-text-{pid}"))).flex_1().min_w_0().truncate().text_color(t.faint).child("Tell Swarm what you want done…")
                        }
                    }))
                    .child(
                        // Never wider than the pane: what does not fit is
                        // dropped (`fit`) and the model label truncates last.
                        // Send/stop sits outside the part that shrinks and
                        // clips, so it is never pushed out or cut off.
                        div()
                            .relative()
                            .flex()
                            .items_center()
                            .gap_3()
                            .min_w_0()
                            .overflow_hidden()
                            .text_size(px(10.5))
                            .text_color(t.faint)
                            .child(measure)
                            .child(
                                div()
                                    .flex_1()
                                    .min_w_0()
                                    .overflow_hidden()
                                    .flex()
                                    .items_center()
                                    .gap_3()
                                    .when(fit.permissions, |d| d.child(self.permissions_chip(pid, cx)))
                                    // The Companion runs on the account's model; it has no picker here.
                                    .when(!companion, |d| d.child(self.model_button(si, pid, cx)))
                                    // Holds the rest to the right when the hint is dropped.
                                    .child(div().flex_1().min_w_0().truncate().when(fit.hint, |d| d.child(hint)))
                                    .when(fit.commands, |d| d.child(div().flex_none().whitespace_nowrap().child("/ commands")))
                                    .children(self.at_work_indicator(pid, fit.agents_short, cx))
                                    .children(badge),
                            )
                            .child(div().flex_none().child(send_or_stop)),
                    ),
            );

        let background = self.render_background(si, id, pane.tray, pane.tab, cx);
        let border = if active { alpha(t.accent, 0.6) } else { t.border };
        div()
            .id(SharedString::from(format!("pane-{pid}")))
            .relative()
            .flex()
            .flex_col()
            .size_full()
            .min_w_0()
            .min_h_0()
            .rounded(px(t.radius + 2.))
            .border_1()
            .border_color(border)
            .bg(t.surface)
            .overflow_hidden()
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                if this.layout.active() != Some(PaneId(pid)) && this.layout.focus(PaneId(pid)) {
                    cx.notify();
                }
            }))
            .child(header)
            .child(
                div()
                    .id(SharedString::from(format!("pane-scroll-{pid}")))
                    .flex_1()
                    .min_h_0()
                    .overflow_y_scroll()
                    .track_scroll(&pane.scroll)
                    // Scrolled near the top: the page before.
                    .on_scroll_wheel(cx.listener(move |this, _: &gpui::ScrollWheelEvent, _, cx| {
                        if this.want_older(PaneId(pid)) {
                            cx.notify();
                        }
                    }))
                    .px_4()
                    .py_3()
                    .flex()
                    .flex_col()
                    .gap(earlier::GAP)
                    .children(entries),
            )
            .children(background)
            .children(self.render_context(si, pid, cx))
            .children(self.failure.as_ref().filter(|(fs, _)| *fs == si).map(|(_, text)| {
                div()
                    .flex_none()
                    .mx_2()
                    .mt_2()
                    .px_3()
                    .py_2()
                    .flex()
                    .gap_2()
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(alpha(t.err, 0.5))
                    .bg(alpha(t.err, 0.08))
                    .child(div().flex_1().min_w_0().text_size(px(11.5)).line_height(px(17.)).text_color(t.text).child(text.clone()))
                    .child(self.icon_button(SharedString::from(format!("failure-close-{pid}")), Icon::Close).on_click(cx.listener(|this, _: &ClickEvent, _, cx| {
                        this.failure = None;
                        this.notice = None;
                        cx.notify();
                    })))
            }))
            .child(composer)
            .when(!popped && !companion && cx.has_active_drag(), |d| d.child(self.drop_zones(pid, cx)))
            .into_any_element()
    }

    fn render_entry(&self, pid: usize, ei: usize, entry: &Entry, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        match entry {
            Entry::User(text) => div()
                .group(SharedString::from(format!("entry-{pid}-{ei}")))
                .flex()
                .gap_2()
                .px_3()
                .py_2()
                .rounded(px(t.radius))
                .bg(t.raised)
                .child(div().text_color(t.accent).child("›"))
                .child(div().flex_1().min_w_0().line_height(px(19.)).child(self.selectable(pid, ei, 0, text.clone(), vec![self.plain_run(text.len(), t.text)], vec![], cx)))
                .child(self.copy_button(format!("copy-{pid}-{ei}"), text.clone(), cx))
                .into_any_element(),
            // Swarm queued it for the running agent's next step; the label
            // goes once Swarm records that it reached the model.
            Entry::Queued { text, .. } => div()
                .group(SharedString::from(format!("entry-{pid}-{ei}")))
                .flex()
                .gap_2()
                .px_3()
                .py_2()
                .rounded(px(t.radius))
                .bg(t.raised)
                .child(div().text_color(t.faint).child("›"))
                .child(div().flex_1().min_w_0().line_height(px(19.)).child(self.selectable(pid, ei, 0, text.clone(), vec![self.plain_run(text.len(), t.muted)], vec![], cx)))
                .child(div().flex_none().text_size(px(11.5)).text_color(t.faint).child("queued"))
                .child(self.copy_button(format!("copy-{pid}-{ei}"), text.clone(), cx))
                .into_any_element(),
            Entry::Assistant(text) => div()
                .group(SharedString::from(format!("entry-{pid}-{ei}")))
                .flex()
                .gap_2()
                .px_3()
                .child(div().text_color(t.faint).line_height(px(19.)).child("◆"))
                .child(self.markdown(text, pid, ei, cx).flex_1().text_color(t.text))
                .child(self.copy_button(format!("copy-{pid}-{ei}"), text.clone(), cx))
                .into_any_element(),
            // A status line Swarm recorded for the person (a task wait
            // starting or ending): compact, muted, not a turn.
            Entry::Notice(text) => div()
                .group(SharedString::from(format!("entry-{pid}-{ei}")))
                .flex()
                .gap_2()
                .px_3()
                .text_size(px(11.5))
                .child(div().flex_none().text_color(t.accent).child("⏳"))
                .child(div().flex_1().min_w_0().line_height(px(17.)).child(self.selectable(pid, ei, 0, text.clone(), vec![self.plain_run(text.len(), t.muted)], vec![], cx)))
                .into_any_element(),
            Entry::Tool { name, state, call_id, .. } => {
                let failed = *state == ToolState::Failed || name.as_ref() == "error";
                let color = if failed { t.err } else { t.info };
                let rows = entry.tool_stack(data::now_ms()).unwrap_or_else(|| crate::swarm::tool_summary::stacked(name, "", "", ""));
                let copy: SharedString = rows.copy.clone().into();
                let tail_runs: Vec<_> = [(rows.summary, if failed { t.err } else { t.faint }), (rows.time, t.faint)]
                    .into_iter()
                    .filter(|(len, _)| *len > 0)
                    .map(|(len, c)| self.plain_run(len, c))
                    .collect();
                // Row one: the name, and on the right its summary and time,
                // which a long detail never pushes off.
                let head = div()
                    .flex()
                    .gap_2()
                    .child(div().flex_1().min_w_0().truncate().child(self.selectable(
                        pid,
                        ei,
                        0,
                        rows.name.clone().into(),
                        vec![self.plain_run(rows.name.len(), color)],
                        vec![],
                        cx,
                    )))
                    .when(!rows.tail.is_empty(), |d| d.child(div().flex_none().child(self.selectable(pid, ei, 1, rows.tail.clone().into(), tail_runs, vec![], cx))));
                // Row two, under the name: the detail, wrapped to two lines.
                let detail = (!rows.detail.is_empty()).then(|| {
                    div().min_w_0().text_color(t.muted).line_clamp(2).text_ellipsis().child(self.selectable(
                        pid,
                        ei,
                        2,
                        rows.detail.clone().into(),
                        vec![self.plain_run(rows.detail.len(), t.muted)],
                        vec![],
                        cx,
                    ))
                });
                // A running call pulses; a finished one hangs off the text above.
                let mark = if *state == ToolState::Running {
                    div()
                        .text_color(t.accent)
                        .child("●")
                        .with_animation(
                            SharedString::from(format!("tool-run-{pid}-{ei}-{call_id}")),
                            Animation::new(Duration::from_millis(1400)).repeat().with_easing(pulsating_between(0.25, 1.)),
                            |dot, delta| dot.opacity(delta),
                        )
                        .into_any_element()
                } else {
                    div().text_color(t.faint).child("└").into_any_element()
                };
                div()
                    .group(SharedString::from(format!("entry-{pid}-{ei}")))
                    .flex()
                    .gap_2()
                    .px_3()
                    .text_size(px(11.5))
                    .child(mark)
                    .child(div().flex_1().min_w_0().flex().flex_col().gap(px(1.)).child(head).children(detail))
                    .child(self.copy_button(format!("copy-{pid}-{ei}"), copy, cx))
                    .into_any_element()
            }
            Entry::Media { task_id, kind, title } => self.render_media_entry(pid, ei, task_id, *kind, title, cx),
        }
    }

    /// What the session waits on you for: the Orchestrator's question with its
    /// options, or a tool call to allow or deny (buttons in the header).
    fn render_pending(&self, si: usize, pid: usize, p: &data::Pending, cx: &mut Context<Self>) -> AnyElement {
        let t = &self.theme;
        let frame = div().mx_1().px_3().py_2().flex().flex_col().gap_2().rounded(px(t.radius)).border_1().border_color(alpha(t.warn, 0.5)).bg(alpha(t.warn, 0.06));
        if let Some(ask) = &p.proposal {
            // A proposal the person decides here: what, where, the change in
            // words, and Accept / Discard (the request's allow / deny).
            let place = if ask.machine.is_empty() { "this machine".to_string() } else { format!("machine {}", ask.machine) };
            return frame
                .child(div().text_size(px(11.)).text_color(t.warn).child(format!("{} PROPOSAL WAITS FOR YOU · {}", ask.kind.to_uppercase(), place.to_uppercase())))
                .child(div().text_color(t.text).child(p.title.clone()))
                .when(!p.text.is_empty(), |d| d.child(div().text_size(px(11.5)).text_color(t.muted).line_height(px(18.)).child(p.text.clone())))
                .child(
                    div()
                        .flex()
                        .items_center()
                        .gap_2()
                        .child(
                            self.button(SharedString::from(format!("proposal-accept-{pid}")), "accept", true)
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.act(si, Act::Approve, cx))),
                        )
                        .child(
                            self.button(SharedString::from(format!("proposal-discard-{pid}")), "discard", false)
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.act(si, Act::Reject, cx))),
                        )
                        .child(div().flex_1().text_size(px(11.)).text_color(t.faint).child("Only you decide; the AI cannot accept its own proposal.")),
                )
                .into_any_element();
        }
        if !p.question {
            return frame
                .child(div().text_size(px(11.)).text_color(t.warn).child("WAITING FOR YOUR APPROVAL"))
                .child(div().text_color(t.text).line_height(px(19.)).child(p.text.clone()))
                .child(div().text_size(px(11.)).text_color(t.faint).child("Allow or deny it in the header above."))
                .into_any_element();
        }
        let given = self.answers.get(&p.id).cloned().unwrap_or_default();
        let hover = t.hover;
        let questions = p.questions.iter().enumerate().map(|(qi, q)| {
            let picked = given.get(&q.id).cloned();
            let options = q.options.iter().enumerate().map(|(oi, o)| {
                let custom_on = self.custom_for.as_ref().is_some_and(|(perm, qid)| *perm == p.id && *qid == q.id);
                let on = if o.custom { custom_on } else { picked.as_deref() == Some(o.value.as_ref()) };
                let (perm, qid, value, custom) = (p.id.clone(), q.id.clone(), o.value.to_string(), o.custom);
                div()
                    .id(SharedString::from(format!("ask-{pid}-{qi}-{oi}")))
                    .px_2()
                    .py(px(5.))
                    .flex()
                    .flex_col()
                    .gap(px(1.))
                    .rounded(px(t.radius))
                    .border_1()
                    .border_color(if on { t.accent } else { t.border })
                    .bg(if on { alpha(t.accent, 0.12) } else { t.surface })
                    .cursor_pointer()
                    .hover(move |s| s.bg(hover))
                    .on_click(cx.listener(move |this, _: &ClickEvent, window, cx| {
                        if custom {
                            this.custom_for = Some((perm.clone(), qid.clone()));
                            if let Some(input) = this.composers.get(&pid) {
                                window.focus(&input.focus_handle(cx));
                            }
                            cx.notify();
                        } else {
                            this.choose(si, qid.clone(), value.clone(), cx);
                        }
                    }))
                    .child(div().text_color(t.text).child(o.label.clone()))
                    .when(!o.description.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.faint).line_height(px(16.)).child(o.description.clone())))
            });
            div()
                .flex()
                .flex_col()
                .gap_1()
                .when(!q.header.is_empty(), |d| d.child(div().text_size(px(11.)).text_color(t.muted).child(q.header.clone())))
                .child(div().text_color(t.text).line_height(px(19.)).child(q.text.clone()))
                .child(div().flex().flex_col().gap_1().children(options))
                .when_some(picked.filter(|_| p.structured), |d, a| d.child(div().text_size(px(11.)).text_color(t.accent).child(format!("answer: {a}"))))
        });
        let custom_waiting = self.custom_for.as_ref().is_some_and(|(perm, _)| *perm == p.id);
        frame
            .child(div().text_size(px(11.)).text_color(t.warn).child(if p.title.is_empty() { "THE ORCHESTRATOR ASKS".to_string() } else { p.title.to_uppercase() }))
            .children(questions)
            .child(
                div()
                    .flex()
                    .items_center()
                    .gap_2()
                    .child(div().flex_1().text_size(px(11.)).text_color(t.faint).child(if custom_waiting {
                        "Type your answer in the message box and press enter."
                    } else {
                        "Pick an option, or Custom response to type your own."
                    }))
                    .when(p.structured, |d| {
                        d.child(
                            self.button(SharedString::from(format!("ask-send-{pid}")), "send answers", true)
                                .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.send_answers(si, cx))),
                        )
                    }),
            )
            .into_any_element()
    }

    /// Header buttons for a live session: what its state lets you do.
    fn session_actions(&self, si: usize, pid: usize, cx: &mut Context<Self>) -> Option<Div> {
        let s = &self.sessions[si];
        s.live.as_ref()?;
        let row = div().flex().gap_1();
        let button = |label: &'static str, strong: bool, act: Act| {
            self.button(SharedString::from(format!("pane-{label}-{pid}")), label, strong).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| this.act(si, act, cx)))
        };
        Some(match (&s.pending, s.state) {
            (Some(p), _) if p.proposal.is_some() => row.child(button("accept", true, Act::Approve)).child(button("discard", false, Act::Reject)),
            (Some(p), _) if !p.question => row.child(button("allow", true, Act::Approve)).child(button("deny", false, Act::Reject)),
            _ => return None,
        })
    }

    /// A copy button that shows while its entry (group "entry-PANE-INDEX") is
    /// hovered; it copies to both clipboards (ctrl-v and middle click).
    fn copy_button(&self, id: String, text: SharedString, cx: &mut Context<Self>) -> impl IntoElement {
        let group = SharedString::from(id.replacen("copy-", "entry-", 1));
        let hover = self.theme.hover;
        div()
            .id(SharedString::from(id))
            .flex_none()
            .size(px(20.))
            .flex()
            .items_center()
            .justify_center()
            .rounded(px(self.theme.radius))
            .cursor_pointer()
            .opacity(0.)
            .group_hover(group, |s| s.opacity(1.))
            .hover(move |s| s.bg(hover))
            .child(self.icon(Icon::Copy, 12., self.theme.faint))
            .on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                cx.write_to_clipboard(gpui::ClipboardItem::new_string(text.to_string()));
                cx.write_to_primary(gpui::ClipboardItem::new_string(text.to_string()));
                this.notice = Some(("Copied.".into(), false));
                cx.notify();
            }))
    }

    /// The decision a task waits on before it starts (Start tasks: approve),
    /// or stopping it while it runs. Live sessions ask Swarm; demo data changes in place.
    /// A plan task, or any task on a linked machine, opens its review: the
    /// whole plan and its exact version, decided there.
    fn task_actions(&self, si: usize, ti: usize, status: TaskStatus, key: &str, cx: &mut Context<Self>) -> Div {
        let live = self.sessions[si].live.is_some();
        if let (TaskStatus::PendingApproval, true) = (status, live) {
            if let Some(review) = self.sessions[si].tasks.get(ti).and_then(|t| t.review.clone()).filter(|r| r.opens_review()) {
                let title = self.sessions[si].tasks[ti].title.clone();
                let label = if review.plan { "review plan" } else { "review" };
                return div().flex().flex_none().gap_1().child(self.button(SharedString::from(format!("{key}-review")), label, true).on_click(cx.listener(
                    move |this, _: &ClickEvent, _, cx| {
                        this.open_plan_review(review.clone(), title.clone(), cx);
                    },
                )));
            }
        }
        let button = |label: &'static str, strong: bool, act: Act, demo: TaskStatus| {
            self.button(SharedString::from(format!("{key}-{label}")), label, strong).on_click(cx.listener(move |this, _: &ClickEvent, _, cx| {
                if live {
                    this.card_act(si, ti, act, cx);
                } else {
                    this.set_task(si, ti, demo);
                    cx.notify();
                }
            }))
        };
        let row = div().flex().flex_none().gap_1();
        match status {
            TaskStatus::PendingApproval => row.child(button("approve", true, Act::Approve, TaskStatus::Running)).child(button("reject", false, Act::Reject, TaskStatus::Rejected)),
            TaskStatus::Running if live && !self.sessions[si].tasks[ti].session.is_empty() => row.child(button("stop", false, Act::Stop, TaskStatus::Failed)),
            _ => row,
        }
    }
}

impl Focusable for Console {
    fn focus_handle(&self, cx: &gpui::App) -> FocusHandle {
        // While onboarding shows, it takes the keyboard.
        match &self.onboarding {
            Some(onboarding) => onboarding.focus_handle(cx),
            None => self.focus.clone(),
        }
    }
}

impl Render for Console {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let _timed = Timed::new("render");
        self.sync_companion_pane();
        self.ensure_composers(cx);
        self.sync_live_page(cx);
        self.sync_fleet_keys(cx);
        self.sync_agents(cx);
        // The conversations open in panes are followed in full, from the moment
        // they open (watch sends nothing when the set is unchanged).
        if let Some(live) = &self.live {
            live.watch(self.panes.iter().filter_map(|p| self.sessions.get(p.session)).filter_map(|s| s.live.as_ref()).map(|l| l.session_id.to_string()));
        }
        if self.pending_focus_companion && self.page == Page::Companion {
            if let Some(input) = self.composers.get(&companion::PANE.0) {
                self.pending_focus_companion = false;
                window.focus(&input.focus_handle(cx));
            }
        }
        if self.pending_focus {
            self.pending_focus = false;
            match self.layout.active().and_then(|a| self.composers.get(&a.0)) {
                Some(input) if self.link != Link::Demo => window.focus(&input.focus_handle(cx)),
                _ => window.focus(&self.focus),
            }
        }
        if let Some(onboarding) = self.onboarding.clone() {
            return div().size_full().key_context("Console").child(onboarding);
        }
        let t = self.theme.clone();
        // Narrow windows (tiling managers, half screens) drop sidebars before panes.
        let width = window.viewport_size().width;
        // Below NARROW_WIDTH the sidebar is always the rail; an open Companion
        // work item then shows over the conversation (left_layout).
        let left_layout = Self::left_layout(self.page, self.left_collapsed, f32::from(width), self.companion.detail.is_some());
        let right = width >= px(1100.);
        let hints = width >= px(1240.);
        let layout = self.layout.clone();
        div()
            .key_context("Console")
            .track_focus(&self.focus)
            .relative()
            .on_action(cx.listener(|this, _: &CopySelection, _, cx| this.copy_selection(cx)))
            .on_action(cx.listener(Self::split_right))
            .on_action(cx.listener(Self::split_down))
            .on_action(cx.listener(Self::close_pane))
            .on_action(cx.listener(Self::next_pane))
            .on_action(cx.listener(Self::toggle_left))
            .on_action(cx.listener(Self::cycle_icons))
            .on_action(cx.listener(Self::go_sessions))
            .on_action(cx.listener(Self::go_companion))
            .on_action(cx.listener(Self::go_media))
            .on_action(cx.listener(Self::go_usage))
            .on_action(cx.listener(Self::go_agents))
            .on_action(cx.listener(Self::go_fleet))
            .on_action(cx.listener(Self::go_live))
            .on_action(cx.listener(Self::go_settings))
            .on_action(cx.listener(Self::go_search))
            .on_action(cx.listener(Self::go_automations))
            .size_full()
            .flex()
            .flex_col()
            .bg(t.bg)
            .text_color(t.text)
            .font_family(t.mono)
            .text_size(px(12.5))
            .child(self.render_titlebar(cx))
            .child(
                div()
                    .flex_1()
                    .min_h_0()
                    .flex()
                    .relative()
                    .child(match left_layout {
                        LeftLayout::Sidebar => self.render_left(cx),
                        LeftLayout::Rail | LeftLayout::RailWithCompanionDetail => self.render_rail(cx),
                    })
                    .child(match self.page {
                        Page::Companion => div().flex_1().min_w_0().flex().child(self.render_companion(cx)),
                        Page::Sessions => div().flex_1().min_w_0().flex().p(px(6.)).child(match layout.root() {
                            Some(root) => self.render_node(root, vec![], cx),
                            None => self.render_no_pane(cx),
                        }),
                        Page::Media => div().flex_1().min_w_0().child(self.render_media(cx)),
                        Page::Usage => div().flex_1().min_w_0().child(self.render_usage(cx)),
                        Page::Fleet => div().flex_1().min_w_0().child(self.render_fleet(cx)),
                        Page::Live => div().flex_1().min_w_0().child(self.render_live(cx)),
                        Page::Agents => div().flex_1().min_w_0().child(self.render_agents(cx)),
                        Page::Permissions => div().flex_1().min_w_0().child(self.render_permissions(cx)),
                        Page::Automations => div().flex_1().min_w_0().child(self.render_automations(cx)),
                        Page::Settings => div().flex_1().min_w_0().child(self.render_settings(cx)),
                        Page::Search => div().flex_1().min_w_0().child(self.render_search(cx)),
                        Page::Projects => div().flex_1().min_w_0().child(self.render_projects(cx)),
                    })
                    .when(right && matches!(self.page, Page::Agents | Page::Fleet), |d| d.child(self.render_agents_inspector(cx)))
                    .when(left_layout == LeftLayout::RailWithCompanionDetail, |d| d.child(self.render_rail_companion_detail(f32::from(width), cx))),
            )
            .child(self.render_statusbar(hints, cx))
            .children(self.render_fleet_popover(cx))
            .children(self.render_project_menu(cx))
            .children(self.render_preview(cx))
            .children(self.render_plan_review(cx))
            .child(self.password.clone())
    }
}

/// Now in Unix ms, to read Swarm's timestamps against.
pub(super) fn now_ms() -> u64 {
    std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_millis() as u64).unwrap_or(0)
}

/// How long ago, shortly: "now", "4m", "3h", "2d".
pub(super) fn ago(ms: u64) -> String {
    let m = ms / 60_000;
    match m {
        0 => "now".into(),
        m if m < 60 => format!("{m}m"),
        m if m < 60 * 24 => format!("{}h", m / 60),
        m => format!("{}d", m / (60 * 24)),
    }
}
