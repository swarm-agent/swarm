# Headless core executable

Build from the `swarmd` module using its required Go toolchain:

```sh
go build -o "$TMPDIR/swarm-headless" ./cmd/swarm-headless
```

This entrypoint calls `config.ParseHeadless` and `runtime.NewHeadless`. It uses
Swarm's existing backend services and canonical storage. Desktop routes, assets,
cookie authentication and listener composition are installed only by the existing
`swarmd` entrypoint. Headless rejects `--desktop-port`, ignores an existing
configuration's Desktop port at runtime, and leaves that stored configuration
unchanged. API credentials remain in headers; the private local transport keeps
its existing identity/setup authority.

The command supports daemon flags, `--help`, and `config removed-keys`.
Other positional commands are rejected before state initialization. The existing
`swarmd` runner and diagnostic subcommands remain on that entrypoint.

To verify the build boundary, also build an unstripped existing daemon:

```sh
go build -o "$TMPDIR/swarmd-desktop" ./cmd/swarmd
cd ..
bash scripts/check-headless-composition.sh \
  "$TMPDIR/swarm-headless" "$TMPDIR/swarmd-desktop"
```

The check reads the compiler dependency graph and both actual symbol tables.
It requires canonical backend symbols, rejects Desktop composition and launcher/
TUI dependencies in Headless, and verifies Desktop symbols in the existing daemon.
It never starts a service. Focused HTTP/configuration tests live in
`internal/api/core_composition_test.go`, `internal/config/headless_test.go` and
`internal/runtime/headless_test.go` within the daemon module.

The independent Stage 2B host candidate is built by
`scripts/build-headless-core.sh` at the repository root. Its package, service,
CLI initialization and isolated verification are documented in
`containers/headless/core/README.md`. Full clean-host deployment qualification
and independent update/recovery remain explicit gates. The legacy Desktop
`/v1/update/*` routes are excluded from Headless;
independent Headless updating still needs that later implementation. Existing
backend media/rendering dependencies remain required. Do not replace an installed
service with this build until those packaging and recovery gates are completed.
