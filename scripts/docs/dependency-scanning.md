# Dependency vulnerability scanning

Protected pushes to `dev` and `main` run `scripts/check-prepush.sh`, which calls
`scripts/check-precommit.sh` and then `scripts/check-vulns.sh`. The same
vulnerability script runs in `.github/workflows/dependency-vulnerability-scan.yml`
for protected-branch pushes, pull requests, manual dispatches, and a weekly
scheduled rescan so newly published advisories are detected without a code
change.

## Coverage

The gate is deliberately layered because no single database or analysis mode
covers Swarm's stack:

- `govulncheck -mode=source -scan=symbol` runs independently over the canonical
  production roots (`cmd`, `internal`, `pkg`, and `theme` as applicable) in the
  root and `swarmd` Go modules. These explicit `...` patterns retain the Go
  team's recommended source analysis while excluding intentionally relocated
  test fixtures and ignored `tmp` programs that do not compile as product code.
- `pnpm audit --audit-level=low` audits all production and development packages
  represented by `web/pnpm-lock.yaml`. Each registry request has a 30-second
  timeout and the gate retries an unavailable registry up to three times with
  short bounded backoff. A complete vulnerability report is never retried or
  reclassified: findings fail immediately, and registry/network errors remain
  fatal after the bounded attempts. The gate never uses pnpm's
  `--ignore-registry-errors` option.
- `trivy fs` performs separate inventory-level checks on the two tracked
  `go.mod` files and `web/pnpm-lock.yaml`, including pnpm development
  dependencies. It scans vulnerabilities only, refreshes its advisory database
  normally, and exits 1 on any unsuppressed severity. Passing the exact tracked
  manifests prevents ignored scratch modules, caches, tools, and `node_modules`
  from becoming machine-dependent vulnerability scope.

The Trivy layer is intentionally broader than govulncheck. It may report a
vulnerable Go module even when Swarm does not call the affected package or
symbol. Such findings require remediation or a narrowly scoped, expiring entry
in `.trivyignore.yaml`; scanner output must not be discarded as cache noise.

## Reproducible tools and updates

`scripts/security-tool-versions.sh` pins govulncheck and Trivy. The gate verifies
the govulncheck module version, downloads the exact pinned Trivy release when
needed, and verifies its official SHA-256 checksum before execution. The web
package manager remains pinned by `web/package.json` (`packageManager`).

Dependabot checks the two Go modules, the web pnpm lockfile, and GitHub Actions
weekly. Scanner updates should be reviewed like dependency updates:

1. Review the upstream release notes.
2. Update the version pin (and all official Trivy archive checksums together).
3. Run `bash -n scripts/check-vulns.sh scripts/security-tool-versions.sh`.
4. Run `./scripts/check-vulns.sh` and resolve every finding or scanner error.

Do not replace pins with an unchecked system binary or a permanently cached
`@latest` install. Do not add `--skip-db-update`, `--ignore-registry-errors`, or
an unscoped/permanent vulnerability suppression.

## References

- Go vulnerability management: <https://go.dev/doc/security/vuln/>
- govulncheck command: <https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck>
- pnpm audit: <https://pnpm.io/cli/audit>
- Trivy filesystem scanning: <https://trivy.dev/docs/latest/guide/target/filesystem/>
- Trivy Go coverage: <https://trivy.dev/docs/latest/coverage/language/golang/>
- Trivy Node.js/pnpm coverage: <https://trivy.dev/docs/latest/coverage/language/nodejs/>
- Trivy filtering: <https://trivy.dev/docs/latest/configuration/filtering/>
