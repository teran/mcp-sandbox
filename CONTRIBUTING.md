# Contributing

Thanks for your interest in contributing! Please read this guide and the
repository's `AGENTS.md` and `SPEC.md` before opening a pull/merge request.

> `SPEC.md` and `AGENTS.md` are strictly English; the README is English by
> default unless you explicitly request another language.

## How to contribute

- **Report a bug** — open an issue. Include the exact command/arguments, the
  expected and actual behaviour, and (if relevant) logs **without secrets**.
  This project is a security-sensitive Docker sandbox server; never place a
  secret in a command line. `sandbox_exec` records `command`/`summary` verbatim
  and they are intentionally **not** masked, so keep secrets in tmpfs `0600`
  secret files instead of argv. See the redaction rules (`secret:true`
  annotation-based redaction) in `AGENTS.md` (S02).
- **Request a feature** — open an issue describing the use case, who calls the
  server, and the external system it would talk to (this drives the transport
  and auth decisions, see `SPEC.md`).
- **Open a pull/merge request** — describe the change and link the related
  issue. Every change must keep CI green (see below).

## Development workflow

Work is done **test-first** (TDD):

1. **`@qa`** writes the tests first, using an **isolated context**.
2. **`@developer`** writes the implementation, using an **isolated context**.

Do not weaken tests to make an implementation pass.

## Quality gates (CI enforces these)

CI binds to the **build-system interface** (R07) — it calls `make <target>`,
not raw language-specific commands. This is a **Local stdio-only** server:
there is **no `container-image` target** (no image is produced) and no HTTP
listener.

```bash
make lint     # golangci-lint + go vet + gosec + govulncheck + go-arch-lint
make test     # go test -race + coverage >= 95% gate
make build    # binaries for all architectures via goreleaser -> dist/
make e2e      # darwin e2e smoke tests (//go:build e2e && darwin) on a macOS CI job
make mutate   # gremlins mutation testing hard gate (80/80)
```

- **Coverage ≥ 95%** — `make test` **fails** below the threshold. Never ship a
  change that lowers coverage; add tests with the code.
- **Mutation testing (gremlins)** — a hard gate at an **80/80 floor**
  (efficacy and mutation coverage). Surviving mutants are a defect; improve the
  tests, do not lower the threshold or exclude packages to dodge it.
- **gosec / govulncheck / go-arch-lint / gitleaks** — findings are **fixed, not
  suppressed**. No blanket `#nosec` / default excludes. **gitleaks** is a
  dedicated hard-gate job scanning git history for leaked secrets.
- **e2e tests** — a **darwin e2e smoke suite** (`go test -tags e2e ./...`,
  files tagged `//go:build e2e && darwin`) runs on a dedicated macOS CI `e2e`
  job. There is **no separate `e2e-darwin` target** by design. Docker-backed
  e2e (Docker-in-Docker) is **out of scope**; the tool-handler → Docker-backend
  path is covered by unit tests plus the coverage gate.
- **CI never mutates repository state** — it is read-only over the working
  tree; no auto-commits, no pushing tags/commits back.

## Definition of done

A change is done **only when CI is green**:

- `make lint` — golangci-lint, go vet, gosec, govulncheck, go-arch-lint;
  findings fixed, not suppressed.
- `make test` — `go test -race` with **coverage ≥ 95%**.
- `make e2e` — the darwin e2e smoke tests pass on a macOS `e2e` job
  (Docker-backed e2e remains out of scope).
- **gremlins** mutation gate (80/80) — no survivors.
- **gitleaks** secret scan over git history — clean.
- `make build` — goreleaser produces the release binaries (when a build is
  part of the change).
- Docs (`SPEC.md`, `README.md`, `AGENTS.md`) remain **English** and in sync
  with any changed behavior.

## Code of Conduct

This project adheres to the [Code of Conduct](CODE_OF_CONDUCT.md). By
participating you are expected to uphold this code.
