# HTML video-still capture contract

Status: implementation contract for the managed HTML-to-PNG bridge.

## 1. Authority and purpose

This contract turns declared states from one exact ready managed HTML artifact into immutable `image/png` variants that can be supplied directly to the existing visual video-plan contract. It does not make HTML a video clip, does not accept browser download blobs or workspace paths, and does not grant proposal-acceptance or render-start authority.

The canonical source and output identity is always the four-field managed-artifact reference:

- `session_id`
- `collection_id`
- `variant_id`
- `event_seq`

The artifact authority authenticates ownership, exact readiness, source lineage, durable byte publication, and final ready metadata. The renderer may produce bytes, but it is not an artifact or lineage authority.

## 2. Normalized authoring format: `swarm.capture/v1`

A capture-compatible document is a ready managed `text/html` artifact or a ready managed `application/zip` HTML package whose canonical entry document is `index.html`. The entry document must contain exactly one manifest and expose the canonical runtime object.

### 2.1 Static manifest

The document contains this non-executable element:

```html
<script id="swarm-capture-manifest" type="application/json">
{
  "version": "swarm.capture/v1",
  "states": [
    { "id": "opening", "label": "Opening" },
    { "id": "proof", "label": "Proof" }
  ]
}
</script>
```

Manifest rules:

- The root is an object with only `version` and `states`.
- `version` is exactly `swarm.capture/v1`.
- `states` contains 1 to 16 entries in canonical export order.
- Each entry has only `id` and optional `label`.
- `id` is 1 to 64 ASCII characters matching `^[a-z0-9][a-z0-9._-]{0,63}$` and is unique byte-for-byte.
- `label`, when present, is trimmed UTF-8 of at most 128 bytes and contains no control characters.
- The decoded manifest is at most 16 KiB.
- Duplicate manifest elements, unknown fields, malformed JSON, duplicate IDs, or an unsupported version fail closed.

The manifest is the enumerable-state authority. Runtime-created state names, URL/query state, review tabs, and DOM heuristics are not capture states.

### 2.2 Runtime handshake

Before `DOMContentLoaded`, the document installs `globalThis.__SWARM_CAPTURE_V1__`:

```html
<script>
globalThis.__SWARM_CAPTURE_V1__ = Object.freeze({
  version: "swarm.capture/v1",
  async select(stateId) {
    // Apply only the declared state. Do not show review controls.
    document.documentElement.dataset.swarmCaptureState = stateId
  },
  async ready(stateId) {
    // Resolve only after state-specific data, images, fonts, layout, and canvas work are done.
    return { state_id: stateId }
  }
})
</script>
```

Runtime rules:

- The object has exactly the string `version` and callable `select` and `ready` members. Additional members are ignored and convey no authority.
- `select(stateId)` accepts only a manifest ID. It may be synchronous or return a promise. It must render the requested state without requiring clicks, pointer movement, keyboard input, URL changes, timers, or review UI and must set `document.documentElement.dataset.swarmCaptureState` to the exact ID before resolving.
- `ready(stateId)` may be synchronous or return a promise. It resolves only when no author-controlled asynchronous work can still change the captured pixels. Its result is exactly `{state_id: stateId}`.
- A thrown exception, rejected promise, wrong marker, wrong readiness result, navigation, reload, popup, download, or timeout is a capture failure.
- The document must honor `prefers-reduced-motion: reduce` and must stop JavaScript-driven motion in capture mode. The backend's stability audit is authoritative even after `ready` resolves.

The renderer calls the runtime directly. It never clicks gallery tabs or other controls to select a state.

### 2.3 Capture-only markup

Authoring conventions:

- Mark review chrome, variant selectors, playback controls, debug information, explanatory overlays, and any other non-output UI with `data-swarm-capture-ui`. The renderer removes these nodes before capture.
- Mark a condition that makes capture unsafe or incomplete with `data-swarm-capture-blocking`. A visible matching element fails capture and its text is not returned to the caller.
- Do not use open `<dialog>`, visible `[role="dialog"][aria-modal="true"]`, open popovers, browser prompts, permission prompts, or top-layer review overlays in a capturable state.
- The intended output must fit the 1920×1080 viewport. The document must not depend on scroll position or scrollbars.
- Package resources use normalized relative paths within the same managed package. External URLs, protocol-relative URLs, filesystem URLs, credentials, data exfiltration, and runtime package mutation are prohibited.

### 2.4 Backend normalization and audit

After `select` and `ready`, and before PNG encoding, the renderer applies a non-author-overridable capture layer that:

- fixes the viewport and `html`/`body` box to exactly 1920×1080 CSS pixels at device scale factor 1;
- sets transparent browser chrome to an opaque document background, defaulting to white when the computed canvas is transparent;
- removes `[data-swarm-capture-ui]`;
- clears focus, selection, text carets, and hover/pointer input; visible modal or top-layer state fails closed before capture;
- hides scrollbars and sets root overflow to hidden;
- disables CSS transitions, animations, smooth scrolling, cursor painting, and pointer events;
- cancels all active Web Animations API animations;
- waits for `document.fonts.ready` and for every in-document image to be complete and decodable;
- rejects a visible capture blocker, modal dialog, open dialog/popover, failed image, or root content that still requires scrolling after normalization.

The renderer then takes two full-viewport PNG samples 100 ms apart. Their decoded RGBA pixels must be identical. A mismatch is `capture_state_unstable`; no first-sample fallback is allowed.

## 3. Trusted renderer adapter and lifecycle

### 3.1 Adapter choice

The backend adapter is a Go `chromedp` client over the Chrome DevTools Protocol, pinned in the daemon module, controlling the system-managed Google Chrome binary at the fixed package path `/opt/google/chrome/chrome`. Swarm does not commit, download, bundle, copy, or update a browser. The daemon does not search `PATH`, use a user browser profile, accept a model/user override, or reuse the web workspace's development-only Playwright dependency. On Ubuntu, this fixed package path is the path covered by the distribution's Chrome AppArmor user-namespace profile; other hosts must provide an equivalently sandboxed system package at that exact path or capture remains unavailable.

Development builds may inject a renderer implementation through the existing Go constructor/test seam. There is no model-authored executable path, URL, environment override, browser channel, flag list, or provider payload.

If the fixed system-managed Chrome executable, its safe ownership/mode, its host AppArmor/user-namespace support, or Chromium's own sandbox is unavailable, export fails with `capture_renderer_unavailable` or `capture_renderer_failed`. The daemon and unrelated artifact operations may continue; the capture feature does not change AppArmor or sysctl policy, silently switch engines, download a browser, or launch with `--no-sandbox`.

### 3.2 Per-request lifecycle

One export request owns one fresh browser process and one fresh private profile. The process and profile are never shared with Desktop, the user's browser profile, another account, or another export request.

The renderer:

1. Authenticates and reads the exact source through the artifact authority.
2. Validates a single HTML document or a bounded package before launch.
3. Creates a mode `0700` job directory under the daemon's canonical private cache root and a fresh browser profile inside it. Repository paths and host-global temporary paths are not used.
4. Starts a capability-scoped loopback server that serves only the validated in-memory HTML/package entries for this job.
5. Launches the fixed system-managed Chrome package in headless mode with a fixed reviewed argument set, Chromium sandbox enabled, one browser context, at most two renderer processes, disabled background networking, disabled extensions, disabled sync, disabled downloads, and host resolution mapped to failure except for the capability-scoped loopback origin.
6. Enables CDP request interception before navigation. Only the exact job origin and normalized package entries are fulfilled; every other HTTP(S), WebSocket, service-worker, file, FTP, data-navigation, popup, and download request is aborted and recorded as `capture_network_blocked`.
7. Uses the preview-equivalent CSP (`connect-src 'none'`, no forms, objects, or top-level navigation) and an opaque/no-referrer document boundary. No Desktop bearer token or private storage path enters the page.
8. Validates the manifest before launch, captures requested states sequentially in manifest order, validates each PNG, then cancels the browser context and allocator.
9. Removes the private job directory on success or failure.

Bounds are fixed by backend constants: one active renderer per daemon by default, at most 16 requested states, 128 package entries, 32 MiB total unpacked source, 8 MiB per package entry, 16 MiB per PNG, 5 seconds for initial document readiness, 5 seconds per state, and 45 seconds total per request. Browser process descendants are terminated when the context is cancelled or a deadline expires. These values are not tool parameters.

## 4. Export action

The AI/tool action is `manage_artifact` action `export_html_stills`.

### 4.1 Request

```json
{
  "action": "export_html_stills",
  "session_id": "source session",
  "collection_id": "source collection",
  "variant_id": "source variant",
  "event_seq": 42,
  "state_ids": ["opening", "proof"]
}
```

Rules:

- All four source-reference fields are required and must identify the same owned ready event.
- The source media type must be `text/html` or `application/zip` with a valid canonical HTML entry.
- `state_ids`, when supplied, contains 1 to 16 unique declared IDs. The backend canonicalizes output order to manifest order, not caller order.
- When `state_ids` is omitted, every declared state is exported. An empty array is invalid.
- No destination IDs, filenames, output dimensions, arbitrary URL, browser option, executable path, network option, script, workspace path, or provider/model field is accepted.
- Output requirements are the immutable canonical `landscape_video` snapshot: 1920×1080, 16:9, landscape, device scale factor 1.

### 4.2 Durable output and lineage

The trusted tool-call ID and exact source reference derive one destination collection and one stable variant ID per state. The collection is parent-session owned. A state variant uses media type `image/png`, presentation kind `image`, a bounded filename derived from the state ID, and the immutable `landscape_video` output requirements.

Each variant copies all source fields into authoritative lineage:

- `SourceSessionID = request.session_id`
- `SourceCollectionID = request.collection_id`
- `SourceVariantID = request.variant_id`
- `SourceEventSeq = request.event_seq`

Run, plan, checkpoint, attempt, and parent-session lineage continue to come only from trusted run context. HTML content and tool arguments cannot author those fields.

Before publication the backend decodes every output with Go's PNG decoder and requires:

- the PNG signature and complete decode with no trailing second image;
- exactly 1920×1080 pixels;
- an image decodable by Go's PNG decoder and normalizable to 8-bit RGBA;
- nonzero size no larger than 16 MiB;
- a digest matching the exact bytes passed to the artifact authority.

The artifact authority stages bytes, finalizes immutable storage, and commits ready metadata through the canonical V3 session mutation boundary. A result is returned only after every requested variant can be reread as ready with its exact event sequence.

### 4.3 Idempotency and partial failure

The request ID is derived from parent session, trusted tool-call ID, action, exact source identity, and normalized state ID. Retrying the same tool call and payload converges on the same collection and variants. Reusing that trusted call identity with a different source, state set, requirements snapshot, bytes, or lineage returns `capture_idempotency_conflict`.

Publication is per state because each PNG is an immutable artifact. If state N fails after earlier states became ready, the action returns failure and keeps those valid ready variants; it never deletes or mislabels them. Retrying the same request reuses compatible ready variants and continues missing states. The action reports overall success only after the complete requested set is ready.

### 4.4 Success response

```json
{
  "tool": "manage_artifact",
  "action": "export_html_stills",
  "status": "ok",
  "source_reference": {
    "session_id": "source session",
    "collection_id": "source collection",
    "variant_id": "source variant",
    "event_seq": 42
  },
  "output_requirements": {
    "preset_id": "landscape_video",
    "width": 1920,
    "height": 1080,
    "aspect_ratio": "16:9",
    "orientation": "landscape"
  },
  "exports": [
    {
      "state_id": "opening",
      "artifact": { "status": "ready", "media_type": "image/png" },
      "reference": {
        "session_id": "parent session",
        "collection_id": "output collection",
        "variant_id": "output variant",
        "event_seq": 84
      }
    }
  ],
  "count": 1
}
```

`exports` follows manifest order and contains one complete exact ready reference per state. These references are directly valid as `manage_video` visual-plan `part.visual` values. Export does not create a video project, propose a plan, accept a proposal, create a final revision, or start rendering.

## 5. Stable failure codes

The action returns a stable code plus a bounded user-safe message. It never returns page text, console payloads, source bytes, browser command lines, private paths, capability URLs, credentials, or screenshots from a failed state.

- `capture_source_reference_invalid` — the four exact-reference fields are missing, mixed, unowned, stale, or not ready.
- `capture_source_type_unsupported` — the ready source is not compatible HTML/package media.
- `capture_source_limit_exceeded` — source bytes, entries, manifest, or selected-state count exceed a fixed bound.
- `capture_package_invalid` — package names, canonical entry, file types, compression bounds, or traversal checks fail.
- `capture_manifest_missing` — no canonical manifest exists.
- `capture_manifest_invalid` — duplicate/malformed/unsupported manifest or state declaration.
- `capture_state_unknown` — a requested ID is not declared.
- `capture_runtime_missing` — the canonical runtime object is absent or has the wrong version/members.
- `capture_state_select_failed` — state selection throws, rejects, navigates, or produces the wrong marker.
- `capture_state_not_ready` — readiness rejects, returns the wrong acknowledgement, or required fonts/images fail.
- `capture_state_blocked` — capture UI/top-layer/modal/blocker or scroll-dependent output remains.
- `capture_state_unstable` — the two audited samples differ.
- `capture_network_blocked` — the page attempts a prohibited request, popup, worker escape, or download.
- `capture_timeout` — document, state, or total deadline expires.
- `capture_renderer_unavailable` — the pinned reviewed renderer or required sandbox is unavailable.
- `capture_renderer_failed` — the reviewed renderer exits or CDP fails without a more specific safe code.
- `capture_png_invalid` — bytes fail PNG signature/decode/color/dimension/size validation.
- `capture_publish_failed` — canonical artifact staging/finalization/ready mutation fails.
- `capture_idempotency_conflict` — a trusted request identity is reused with incompatible input or output.

## 6. Security invariants

- Exact managed references, not names or latest-selection guesses, are the only source authority.
- No model/user renderer URL, executable, flags, profile, path, script, network policy, output dimensions, destination variant, or provider payload is accepted.
- The browser has a fresh private profile, fixed deadlines, fixed process and byte/state bounds, no user data, no credentials, and no unrestricted network.
- Package traversal, symlinks, special files, absolute paths, duplicate normalized names, and compressed-size expansion beyond bounds fail before browser launch.
- Review controls are never used to drive selection and are removed from output; visible blocking overlays fail rather than being captured.
- PNG publication remains within the existing artifact authority and V3 mutation boundary; the renderer cannot mark an artifact ready.
- The returned exact PNG references preserve source lineage and can enter only the existing pending visual-plan workflow. Existing user-only proposal acceptance and final-render start boundaries remain unchanged.

## 7. Relevant implementation paths

- `swarmd/internal/tool/runtime_manage_artifact.go`
- `swarmd/internal/artifact/authority.go`
- `swarmd/internal/artifact/requirements.go`
- `swarmd/internal/api/sessions_v3_artifacts.go`
- `swarmd/internal/runtime/daemon.go`
- `swarmd/internal/tool/runtime_manage_video.go`
- `swarmd/internal/videoproject/service.go`
- `scripts/smoke-release-archive.sh`
