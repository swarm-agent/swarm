# Artifact V2 storyboard and Video Studio conversion

## Invariant and threat

One exact Artifact V2 composition head is the sole source for storyboard metadata, rendered stills, animation candidates, fallback media, and a pending Video Studio proposal. The primary AI workflow cannot supply export arrays, candidate arrays, fallback references, or reconstructed storyboard manifests. Stale, foreign, incomplete, incompatible, or unresolved source evidence must fail before proposal mutation.

## Implemented boundary

- `artifactv2.StoryboardSection` is a strict independently revisioned part payload keyed to its durable part identity. It carries capture state, duration, creative direction, filming requirements, production state, and optional spatial composition.
- `artifactv2.StoryboardCompiler` owns the capture shell and renders ordered part metadata directly from the selected composition.
- `artifactv2.StoryboardValidator` validates every exact state together through the trusted capture primitive.
- `CreateStoryboardStills` creates V2 derivatives with exact source part/revision/capture-state lineage. Conversion refuses sections without matching exact stills.
- `ArtifactV2VideoReference` is structurally distinct from V1 collection/variant references and is validated/read only through Artifact V2 authority.
- `VideoConversionService.ConvertToPendingProposal` accepts one exact published head and project base, derives the complete plan server-side, and exposes only `CreateEditProposal` on its Video Studio sink.
- Animation conversion uses the exact V2 HTML head, validates duration/profile/policy, creates a fallback through the V2 derivative authority only when absent, and defers MP4 creation/promotion until selection and durable render need it.
- The provider-facing `manage_video` action registry exposes `convert_artifact_v2`; `import_storyboard` and `propose_html_iteration` are removed from that enum. Their old internal implementation remains deletion evidence pending the final cutover checkpoint, not a fallback route.

## Focused validation

- `cd swarmd && go test ./internal/artifactv2`
- `cd swarmd && go test ./internal/videoproject`
- compile-only package checks for `internal/tool`, `internal/runtime`, and `internal/videorender`

The wider `internal/store/pebble` package contains unrelated pre-existing failures and is not used as checkpoint evidence.
