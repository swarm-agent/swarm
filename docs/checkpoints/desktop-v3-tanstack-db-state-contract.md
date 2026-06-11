# Desktop V3 TanStack DB state contract

Status: Superseded. Do not implement this rail. Use `docs/checkpoints/desktop-v3-external-store-state-contract.md` instead.

Previous status: CP-db-1 architecture rail for the Desktop V3 state replacement.

Desktop V3 must have one frontend authority for backend-derived data: TanStack DB. The backend remains the source of truth, and `POST /v3/sessions:workset` remains the bootstrap/sync input. React Query is allowed only as a transport/mutation layer that moves wire data into TanStack DB; it must not be read as an authoritative cache for Desktop routing, readiness, sidebar selection, chat rendering, permissions, plans, usage, or session metadata.

## Non-negotiable invariants

1. **Backend source of truth**: Desktop obtains backend-derived session/workspace/chat state from explicit backend APIs. The initial normalized session graph comes from `/v3/sessions:workset`.
2. **Single frontend authority**: Once wire data reaches the browser, TanStack DB is the only authoritative store for backend-derived Desktop V3 data.
3. **No split-brain stores**: Zustand, LocalStorage, React component state, and React Query caches must not hold backend-derived Desktop data as a second authority.
4. **React Query transport only**: React Query may perform network calls and mutations, but successful results must be normalized into TanStack DB before UI/readiness code reads them.
5. **Local route/sidebar switching**: Switching to a session already present in TanStack DB is local lookup plus navigation/render only. It must not trigger per-session hydrate, query prefetch, or snapshot network calls.
6. **DB-derived readiness**: Route readiness and render eligibility are derived from TanStack DB records and explicit omission/manifest state, not from query status or Zustand hydration flags.
7. **No hidden fallback paths**: If a selected session/resource is not present or is explicitly omitted, the UI must fail clearly or use an explicit workset/chunk follow-up path. It must not silently fall back to the old per-session snapshot authority.

## Allowed state responsibilities

- TanStack DB collections own backend-derived Desktop records: sessions, messages, plans, permissions, usage, preferences, run intents, projections, omissions, manifests/chunks, notifications, and any route readiness metadata derived from those records.
- React component state may own ephemeral view-only UI state such as modal open/closed state, input focus, resize state, temporary hover state, and non-authoritative draft affordances.
- LocalStorage may persist only explicit user/UI preferences that are not backend-derived records, for example collapsed sidebar layout. It must not persist canonical session/workset/readiness data.
- React Query may own request lifecycle state for network transport. UI selectors must read the normalized DB graph, not query result objects.

## Replacement sequence

1. Add failing rails that reject the current split-brain architecture.
2. Add `web/src/features/desktop/state/desktop-db.ts` as the canonical TanStack DB module.
3. Normalize `/v3/sessions:workset` responses into DB collections.
4. Replace route readiness/hydration with DB-derived selectors.
5. Migrate sidebar/chat/notification/render reads to DB selectors.
6. Route durable mutations and realtime events into DB writes.
7. Delete old authority paths: `use-desktop-store.ts`, `desktop-v3-cache.ts`, durable reducer authority, and the `zustand` dependency.
8. Keep static anti-regression tests proving there is one Desktop V3 data authority.

## Static rail intent

`web/src/features/desktop/state/desktop-db-architecture.spec.ts` intentionally fails against the old architecture. It is the migration gate: later checkpoints must turn each failure green by replacing old authoritative imports and dependencies with TanStack DB-backed reads/writes.
