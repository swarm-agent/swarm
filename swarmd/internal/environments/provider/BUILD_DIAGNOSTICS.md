# Managed build failure diagnostics

Build, export and import failures report phase, failure kind and observed command
exit code. The build command is `systemd-run --wait`: its exit code may describe
launcher infrastructure rather than Podman. A signalled command has code -1;
cancellation and deadline expiry use 130 and 124 and retain context identity.
An export size-limit failure is reported separately from cancellation.

Recipe stdout is discarded. At most 16 KiB of stderr is inspected in memory;
only fixed troubleshooting hints are returned, never matching lines, paths,
arguments or underlying error text. Hints are explicitly untrusted: a recipe can
forge an infrastructure message. They must not authorize retries, admission or
weaker isolation. No raw build log is saved by this path.

For an unknown failure, an operator must explicitly authorize a private runtime
reproduction using the same committed inputs and isolation settings. Inspect the
configured user manager, rootless helper versions, delegated cgroups and storage
capacity first. If raw output is needed, capture it only in operator-controlled,
access-restricted scratch under the run-provided TMPDIR; treat all output as
source-secret-bearing. Do not paste it into session messages, durable operation
records or public issues. Report only reviewed structural failure facts. Do not
reuse another operation's storage, retain failed images, supply ambient auth or
proxy configuration, or disable isolation to make a reproduction succeed.

These diagnostics do not establish the cause of a particular engine exit 125.
Real Podman/systemd reproduction is still required; injected tests establish
failure reporting and cleanup contracts, not host compatibility. Podman 4.9.3
strictly limits the `--runroot` parameter to at most 50 characters due to UNIX
domain socket path limits; managed builds allocate a short, private, per-operation
runroot directly beneath the verified user runtime directory (`XDG_RUNTIME_DIR`),
which is cleaned up along with isolated storage during operation teardown.
