# AGENTS.md

Guidance for AI agents working in this repository. `AGENTS.md`, like
`SPEC.md`, is **strictly English** (D02). The README is English by default
(D01).

## This repository

- An **MCP server** that provides a safe, isolated Docker **sandbox** so a
  model-host client (opencode) can run arbitrary commands **without using the
  host shell** — per-session, isolated containers. Cleanup is **explicit**: a
  container is removed only by `sandbox_destroy` / `sandbox_destroy_all` (no
  automatic GC; see `SPEC.md` §6.6).
- Implementation language: **Go 1.27.3** (see `go.mod` / `GO_VERSION` in CI)
  (**C02GO/C01GO**).
- See **[`SPEC.md`](SPEC.md)** for the application-level architecture and the
  security / data / logging contracts that **every change must respect**.
- The server is **self-describing** over the MCP protocol (server-level
  `Instructions` + `sandbox://guidance/*` resources + enriched tool
  descriptions, `SPEC.md` §5.2) — there is **no standalone skill file** and no
  AGENTS.md pointer; a model-host discovers usage directly over the protocol.

## Working agents

| Agent           | Role in this repo                                                                    |
| --------------- | ------------------------------------------------------------------------------------ |
| **@architect**  | Architecture, dependency and system-design decisions (DDD/Clean, Backend roles).     |
| **@qa**         | Writes tests first (isolated context) — unit, mutation, e2e; owns the coverage gate. |
| **@developer**  | Writes the implementation (isolated context) to make the tests pass.                 |
| **@security**   | Reviews the security model (Isolation, ALLOW_DIRS, redaction, Docker socket).        |
| **@devops**     | CI/CD, goreleaser release, Makefile interface.                                       |
| **@techwriter** | Keeps `SPEC.md`, `README.md`, `AGENTS.md` current and English.                       |

**TDD workflow (T01):** tests first (@qa), implementation second (@developer).
Never weaken tests to make the implementation pass.

## Build system (R07) & quality gates (CI enforces these)

CI binds to the **build-system interface** (R07): it calls `make <target>`, not
raw language-specific commands. Interface targets:

- `make build` — binaries for all architectures via goreleaser (B01/B02).
- `make test` — `go test -race` + **coverage ≥ 95%** (C01/C04GO).
- `make e2e` — **darwin e2e smoke tests** (`go test -tags e2e ./...`,
  **C04/T02/C09GO**) — runs on a dedicated macOS CI `e2e` job; excluded by the
  `darwin` build tag on Linux. There is **no separate `e2e-darwin` target** by
  design.
- `make lint` — golangci-lint, gosec, govulncheck, go-arch-lint.
- `container-image` is **NOT declared** — this is a **Local stdio-only**
  server; no image (B03/R01). A CI job must never invoke an undeclared optional
  target (N31).

> **e2e (C09GO / N07GO / T02 / C04) — scoped.** A **darwin e2e smoke suite**
> exists as `//go:build e2e && darwin` files, run under the generic **`make e2e`**
> (`go test -tags e2e ./...`) on a **dedicated macOS CI `e2e` job**. On Linux the
> darwin e2e is excluded by the `darwin` build tag; there is **no separate
> `e2e-darwin` target** by design (darwin e2e rides the generic `make e2e` on
> macOS runners). **Docker-backed e2e (Docker-in-Docker) remains out of scope /
> N/A**: it would require driving the real Docker daemon from inside a sandbox,
> which conflicts with the immutable `Isolation` hardening (a nested/rootless
> daemon or socket access, outside the MVP scope); the full tool-handler →
> Docker-backend path is covered by unit tests + the coverage gate.
> `go-docker-testsuite` is **not** a dependency. N30 is not violated: e2e tests
> exist only under the `e2e && darwin` tag and are run only on macOS runners
> where applicable.

Additional hard-gate jobs: **gremlins** mutation testing at **80/80** (C08GO)
runs via the `make mutate` interface target, and **gitleaks** secret scan over
git history (C03/N28) runs as a standalone `gitleaks/gitleaks-action` — the one
hard gate **not** part of the make interface.

- **Coverage ≥ 95%** — `make test` **fails** below the threshold. Never ship a
  change that lowers coverage; add tests with the code.
- **Mutation testing (gremlins)** — hard gate at an 80% floor. Surviving
  mutants are a defect; improve the tests, do not lower the threshold or
  exclude packages to dodge it (N03GO).
- **gosec / govulncheck / go-arch-lint** — findings are **fixed, not
  suppressed** (S05/N02GO). No blanket `#nosec` / default excludes.
- **CI never mutates repository state (N36)** — read-only over the working
  tree; no auto-commits, no pushing tags/commits back. Release binaries are
  external publications only.

## Working in this repo

### Security-first (non-negotiable)

The sandbox **replaces the host shell** — security is the primary contract.
See `SPEC.md` §7.

- **Immutable `Isolation` block:** `ReadOnlyRootfs=true`, `CapDrop:ALL` (minimal
  re-add), `no-new-privileges`, seccomp (default MVP; custom is mandatory
  roadmap), **non-root** user, bounded `PidsLimit`/`Memory`/`NanoCPUs`/
  `Ulimits`, **network from the operator profile** (bridge/none default; host
  only when operator-authored), tmpfs mounts — `/run`
  (`rw,noexec,nosuid,nodev`, sandbox-user-owned) and `/tmp` (`rw,nosuid,nodev`,
  writable) — `AutoRemove=true`. A caller must **never** be able to weaken these.

  > **Platform note (`exec` on tmpfs):** some Docker daemons (notably Docker
  > Desktop on macOS) **force `noexec` on tmpfs** regardless of the requested
  > `exec` flag, so `/tmp` may not be executable even though it is writable.
  > Runtime code compilation (e.g. Go builds) therefore needs a writable
  > **bind-mount** (the session `cwd` with the profile's `cwd.mode=rw`) — not tmpfs.

- **Operator-authored profiles (Phase 7) — trust model:** sandboxes are created
  from **read-only** YAML profiles in `SANDBOX_PROFILES_DIR`. **Operators author,
  the model references read-only** (by `profile` name on `sandbox_create`); a
  profile owns image/env/mounts/network/limits/user/sudo. Profiles are **read on
  demand** — there is **no in-memory store and no startup load**; each
  `Get`/`List` reads `SANDBOX_PROFILES_DIR` fresh, so editing a `<name>.yml`
  takes effect **without restarting the server** (§7.11). The profile is the
  trusted party for those fields, but it can **never** weaken the immutable
  `Isolation` block (§6.5/§7.11). **`ALLOW_DIRS` is retained and still required**
  — it gates the client-supplied `cwd` **and every profile mount `source`**
  (plus `ALLOW_SYMLINKS` for path resolution, S04). There are **no** profile
  create/update/delete tools; `profile_list` emits only `name`/`description`
  (no env/mount/image/user/sudo leakage, S02).
- **TLS is never implemented in-server (S01).** This is a Local/stdio server —
  no HTTP listener at all.
- **No secret leakage (S02/N02).** All API requests/responses/config are
  structs; sensitive fields annotated `secret:true`. The single redaction
  helper hides them from **all output and logs**. Secrets go into tmpfs `0600`
  files under `/run/sandbox`, **never** argv/image.
  > **`command` is intentionally NOT masked (S02 disclaimer):** the
  > `sandbox_exec` access-log records `command` as passed and is deliberately
  > **not** secret-value-redacted — **no masking is applied**. `command` is
  > **not** a channel for secrets — never place a secret in a command line;
  > use tmpfs `0600` secret files. Logging is opt-in (only when `LOG_LEVEL` is
  > set, L02) and the file is `chmod 600` (L01).
- **Filesystem scope (S04/N03):** host access only via `ALLOW_DIRS`, empty by
  default. `ALLOW_SYMLINKS=false` by default — canonicalize with
  `EvalSymlinks`, open-then-verify (identity-only, TOCTOU); **no** resolved-path
  re-gate against `ALLOW_DIRS`. A symlink physically inside an allowed dir that
  points outward is **operator-authored and trusted** (the sandbox can't create
  host-side symlinks outside its mounts, mounts are fixed at create, and
  in-container symlinks resolve in the container namespace) — not a sandbox
  escape, by design (§7.2). Hardlinks are an OS-level concern, not the path
  check's. Write only into tmpfs.
- **No shell/SQL/URL concatenation (S11/N21):** **no shell-string
  concatenation/interpolation** — `sandbox_exec` takes a scalar `command`
  through a **fixed** `[<shell>,"-c",command]` wrapper (shell from the profile,
  default `bash`); no user string is interpolated into a shell string.
  The only other `sh -c` is the server-authored fixed secret-writer script.
- **HITL (S12):** destructive tools (`sandbox_destroy`, `sandbox_destroy_all`)
  require **human confirmation**, realized **client-side** by the model-host
  (stdio server has no HTTP UI).
- **Docker socket (documented limitation):** the socket is effectively root on
  the host. Prefer **rootless Docker** or confine socket access to the server
  process. Never expose the socket to output/tools.
- **darwin backend (`sandbox-exec`, SPEC §7.12) — weaker security posture:**
  policy-based containment, not kernel/container isolation. **`no-new-privileges`
  / `read-only-rootfs` / `cap-drop` / `tmpfs-workdir` are validated by
  `spec.Validate` but NOT enforced on darwin** (no mechanism; `SysProcAttr` is
  `Setpgid` only). No uid change (runs as current host user, `euid != 0`
  refused), no cgroup quotas (soft `setrlimit` only), no network isolation. The
  operator `seatbelt.sbpl` is appended **verbatim** (last-match-wins) and does
  **not** pass the `sbpl` metacharacter validators — only NUL is rejected; the
  operator is trusted and a model-host can never supply SBPL. An operator
  `(allow default)` re-opens surfaces the server baseline denied. Consolidated
  threat model: `SPEC.md` §7.13.
- **Supply chain (T7/C05):** sandbox images come from the **operator profile**
  (SPEC §7.11) with **parse-only** validation — the trusted-registry allowlist
  (`TRUSTED_REGISTRIES`) is **removed**; **digest-pinning is the recommended
  operator convention** (SPEC §7.9), signed packages; dependencies pinned to
  latest stable.
- **Streaming output (`notifications/progress`, SPEC §5.1):** long-running tools
  (starting with `sandbox_exec`) may stream live output. Streamed chunks are
  **secrets-redacted (S02)** and **ANSI-sanitized (S09)** — the same contract as
  any other output; a redacted `start` and a terminal `done`/`cancelled` frame
  the stream. No secret or control sequence leaks through the progress channel.

### Outbound HTTP / Docker client

- **No general outbound HTTP** service calls exist. The default backend is the
  local Docker daemon via the **official `github.com/docker/docker` SDK**
  (unix socket); the experimental **darwin backend** (`SPEC.md` §7.12) spawns
  `/usr/bin/sandbox-exec` children via `os/exec` and needs no daemon. Do **not**
  hand-roll a low-level `http.Client` for Docker
  (N05GO) — resty is not suited to the unix-socket transport. If a future
  backend requires genuine remote HTTP, use `resty.dev/v3` (L02GO).
- **darwin exec limitation (SPEC §7.12):** the server-rendered SBPL is
  deny-default and emits **no `process-exec*`**, so an operator-authored
  `seatbelt.sbpl` in the darwin profile MUST grant exec — either an explicit
  `(allow process-exec*)` (plus the shell runtime's file/mach/sysctl
  allowances) or `(allow default)` for non-file operations with a narrowed file
  scope. Both are valid; the bundled, verified file-deny-all example is
  `examples/profiles/seatbelt.yml`. **Runtime wiring caveats:** the in-band
  `IsPolicyDenial` classifier is wired into `Backend.Exec` (`darwin.go`), but
  the `log stream` **tailer + `ConsumeDenials` are NOT wired into runtime**
  (unit-tested only), and the production factory (`darwin.go:471`) does **not**
  call `WithScratchDir` (so `scratchDir == ""` in prod). The darwin e2e drives
  `sandbox-exec` with an `(allow default)` SBPL directly, so it does not
  validate exec under the server-generated baseline.
- **No retry/backoff** — set an explicit timeout/context and **return the error
  to the model** (X03/L02GO). For `sandbox_exec`, the optional `req.Timeout`
  (seconds) is applied as a `context.WithTimeout` cancellation that reaches the
  Docker API calls and the progress sink (SPEC §6.10; `timeout == 0` ⇒ no
  deadline).

### No implicit startup actions (A02/N35)

Startup performs **local wiring only**: load config, build logger, emit the
startup banner (B05/L06, when logging enabled), compose layers, start the
transport. **Never** make an outbound/upstream request at startup — no Docker
connectivity pings, no eager data fetches. Backend `Available()` is evaluated
**lazily inside `sandbox_create`**, on the first tool call (A02/N35).

### Data & state

- **State model (X01):** **stateful, per-session, in-memory** — sessions live in
  the session manager's process map, tied to the model-host process lifetime.
  No persistence, no DB, no encryption-at-rest, no migration (X04 N/A).
  Each `mcp-sandbox` process tracks **only its own** sessions; concurrent copies
  do not coordinate.
- **Multi-backend, one runtime per driver (SPEC §6.3):** the MCP `Service`
  keeps a backend + session manager **per concrete driver**, created lazily
  (A02/N35), and routes each session to its owning backend — a single process
  can hold docker and seatbelt sandboxes simultaneously.
- **Session id = namespaced backend id (SPEC §7.8):** every session id is
  `slug:internalID` — the manager calls the backend first and uses its returned
  id as the **internal** part (docker = **container id**, darwin = **`darwin-N`**;
  an empty backend id falls back to an opaque 64-hex id), namespaced by the
  backend's slug (`docker:` / `seatbelt:`). Backends **strip the slug** before
  their underlying API call, and the service routes by slug (`resolveSessionID`,
  no `sessionDriver` map). The docker
  `dev.teran.sandbox.session` label is therefore **redundant** (the internal id
  _is_ the container id); the `dev.teran.sandbox.owner` label remains.
- **Session cleanup (explicit only):** sandboxes are removed **only** by
  explicit `sandbox_destroy` / `sandbox_destroy_all`. There is **no automatic
  GC** — a sandbox persists until explicitly destroyed.
- **Idempotency (X02):** `sandbox_create` = search-before-create;
  `sandbox_destroy`/`destroy_all` = naturally idempotent (no-op on missing).
  Declare `idempotentHint:true` only where a real mechanism exists (N25).
- **Concurrency (X05):** per-session mutex; no partial/corrupt state; verified
  with `go test -race`.

### Logging

- **Enablement (L02):** logging is on **only when `LOG_LEVEL` is set**; unset
  ⇒ disabled. (HTTP mode would always be on — not
  applicable here.)
- **Channel (L01):** stdio → **file** (`LOG_FILENAME`, `chmod 600`), **never
  stdout** (N04GO).
- **Banner (B05/L06):** when enabled, the **first log line** is the startup
  banner.
- **SDK logger (L07/L03GO):** the go-sdk `slog` logger is wired into logrus.
- **Access log (L08/L03GO):** every `tools/call` emits a line **at `info`**
  (visible whenever logging is on — no `debug` gate) with `tool`, redacted
  `args`, `source` (`STDIO`), `duration`, `outcome`. **`sandbox_exec` only:**
  `command`/`summary` are hoisted to **top-level** fields and the whole `args`
  map (including the per-exec `env`) is dropped for that tool; every other tool
  keeps logging the `args` map unchanged.
- **`request_id` (L09/L04GO):** per request, via context.
- **Logger:** `logrus` (N04GO). **No secrets in logs (L05).**

### Architecture (A01)

Layered DDD/Clean with **top-level named packages** — **no `internal/`**
(recorded decision in `SPEC.md` §6.1). Respect the layer edges enforced by
**go-arch-lint**: `cmd/` → `application/` + `infrastructure/`; `application/` →
`domain/`; `infrastructure/` → `domain/`. `domain/` must **not** import
infrastructure.

- Backend roles (`Backend`, `Lifecycle`, `Executable`, `Introspectable`) are
  dispatched by **type-assertion**; unsupported
  capabilities return **`ErrCapabilityNotSupported`** — **no stubs**.
- Tools are declared as **`ToolDef`s** and must go through the shared
  handler-wrapper (validate → access-log → dispatch → redact → structured
  output). **Do not bypass the wrapper.**
- **Streaming (`domain/progress`, SPEC §5.1):** the progress `Sink` is threaded
  via `context` (`WithSink`/`SinkFromContext`), so it is **generic across every
  tool** — the shared handler stashes it into the context before dispatch.
  `LineBatcher` is **line-buffered with no goroutine/ticker** (zero lifecycle
  risk). Nil-sink leaves behavior **byte-for-byte unchanged**.

### Driver registry & external drivers (in-process IMPLEMENTED; external loader/IPC FUTURE)

The driver-registry + driver-owned config work in `SPEC.md` §6.11/§7.11 is
**partially implemented**. The **profile schema is the envelope `apiVersion` +
`spec`** (Phase A, additive — `SPEC.md` §7.11): `apiVersion` =
`«<driver>.profiles.sandbox.teran.dev/vN»` (driver = first DNS label, `vN` =
profile-schema version, NOT the driver implementation version), `spec` is the
**driver-owned** blob that replaces the legacy `options:`, and `cwd`/`shell`
live **inside** `spec` as common server-understood fields. At USE time
`driver.Reconcile` fail-closes on an `apiVersion` mismatch
(`ErrAPIVersionMismatch`); legacy profiles without `apiVersion` pass. The
**legacy `driver:` + `options:` + top-level `cwd`/`shell` form still works but
is DEPRECATED** (Phase A is additive; making `.spec` the only form is future
Phase B, owner decision).
**Implemented (Phase 1, in-process):** the
**driver-owned profile key** (`spec`, legacy `options:`), **`driver.Options`**
(opaque `*yaml.Node` wrapper),
the **`driver.Registry` / `Builder` / `BuildFunc` / `Context`** model, the
built-in `docker`/`seatbelt` builders (registering via `init()` into
`driver.Default`), the **`EnforceHardening`** pure function, the docker backend's
**`New(api, opts ...Option)`** no-allow-dirs signature, and `Service.BuildSpec` +
`Service.Create` wiring (fail-closed unknown driver, ALLOW_DIRS gate at the
Service). These are **real, live code** — treat them as such, not as a design
sketch. **NOT implemented — FUTURE (Phase 2):** the **`SANDBOX_DRIVERS_DIR`**
loader, out-of-process **IPC** (STDIO child + protobuf-like), and admission of
external/non-auditable drivers. **Delivery roadmap (settled):**
**Phase 1 = in-process Go plugins** (done); **Phase 2 (later) = out-of-process
IPC**. The loader/manifest/IPC content below is the Phase-2 realization.

- **Driver = single-sandbox primitive.** A driver implements the interface to
  work with **ONE** sandbox, mapping onto the existing roles (§6.2):
  `Create`/`Destroy` (Lifecycle), `Exec` (Executable), `Info`/`Stats`
  (Introspectable), `Available` (Factory). **`List` is NOT a single-sandbox
  primitive** — it is an optional reconciliation seam;
  **`Delete` is renamed `Destroy`** (QA P1, §6.11). **Orchestration** of many
  sandboxes (session manager, routing, lifecycle, HITL, streaming/redaction,
  `sandbox_list`) stays with the MCP server. In the implemented shape the
  registry maps `spec.Driver` → `Builder` (`Build(ctx Context) (spec.SandboxSpec,
  error)`); the built-in builders decode the opaque `options` into their own
  typed structs and register via `init()`.
- **`Driver()` vs `Slug()` (QA P2, implemented):** `role.Backend` exposes both
  `Driver()` and `Slug()`; `Slug()` (docker → `docker`, seatbelt → `seatbelt`)
  is the implemented namespacing prefix for the `slug:internalID` session id
  (SPEC §7.8), a **distinct**, stable token — do not conflate the two.
- **`driver` is the registry key, and is REQUIRED in the shipped schema.**
  `auto`/empty is **removed** and rejected at the profile layer (§7.11); the
  profile driver (from the envelope `apiVersion` group, or the legacy `driver:`
  token) selects the builder; its driver-owned `spec:` blob (legacy `options:`)
  is opaque and
  driver-owned. An **unknown driver fails closed at USE time** with
  `ErrUnknownDriver` — no silent fallback (§6.3). **Reserved built-in names**
  (`docker`, `seatbelt`) cannot be shadowed; duplicate registration fails closed
  with `ErrDuplicateDriver`; a driver spec failing post-build `Validate()` is
  **rejected**, not patched (Security B/F3).
- **Hardening-on-result = DATA-PLANE only** (§6.11 F1). For the shipped in-process
  builders it is defense-in-depth/DRY: `Service.Create` re-asserts the immutable
  `Isolation` via `EnforceHardening` and gates every mount `source` by
  `ALLOW_DIRS` before the backend runs. For **future external non-auditable
  drivers** the core re-asserts only **declared** `Isolation` fields; the
  **mechanism-plane is self-attestation / unaudited**. A **closed enum** of
  isolation guarantees (readonly-rootfs, cap-drop, no-new-privileges, non-root,
  cgroup-quotas, network-isolation, tmpfs, autoremove) must be filled explicitly,
  with a **pessimistic default** (missing = "not guaranteed"); a declared
  weakness yields an honest **REDUCED-POSTURE marker** — never silently ignored,
  never presented as full isolation (analog of the darwin rule, §7.12). _One-liner
  (data-plane):_ "trust the driver to apply isolation, but guarantee a uniform
  security floor at the *input* that does not depend on whose driver it is or how
  careful it is." — it does **not** extend to the mechanism-plane.
- **`EnforceHardening` is the single core-owned choke-point** (Developer G,
  implemented): a pure domain function in `domain/sandbox/spec` (`enforce.go`),
  called from `Service.Create` **FIRST — before backend selection/creation** on
  the `Service.BuildSpec` output (§6.5/§7.11). The **`ALLOW_DIRS` mount gate is
  lifted to the Service level**, not duplicated in backends — the docker backend
  keeps only the **TOCTOU** `EvalSymlinks`/open-then-verify path check, and its
  constructor `New(api, opts ...Option)` **no longer takes an allow-dirs arg**
  (the darwin constructor keeps `allowDirs` only to feed `sbpl.Generate`, not for
  containment gating). It is a pure function, testable without any concrete
  driver (fake/malicious spec → hardened result; reuse the fake-driver pattern).
- **Loader & tamper control (Phase 2 — FUTURE):** traversal-safe (`os.OpenRoot`,
  name validated like `profile.ValidName`), fail-closed; driver binary
  **digest/signature in the manifest** (analog of §7.9); `Available()`/spawn
  stays **lazy** (A02/N35) — never at startup.
- **IPC/stream boundary (Phase 2 — FUTURE):** frame size limits, fail-closed
  framing (negative/overflow/huge lengths, premature EOF, partial frames),
  **every driver byte ANSI-sanitized (S09) + redacted (S02) server-side** (never
  trust a driver to redact itself), `request_id` per frame, a **cancel/abort
  frame** so `req.Timeout` becomes a wire deadline, and secrets (tmpfs `0600`)
  **never echo** over IPC or logs.
- **New trust zone (Phase 2 — FUTURE):** "external drivers" added to `SPEC.md`
  §7.13 (S14 audit) with threat model (lying self-attestation, sandbox→driver→server
  escalation, DoS via IPC, name shadowing; X1–X4) and an auditor checklist.
- **Migration (J2): one-shot breaking, DONE** — the `docker:`/`seatbelt:` →
  `driver:` + `options:` conversion is complete; the server **no longer reads**
  the old typed blocks (no compat shim). The README **migration guide** maps the
  old block keys to `options:` keys and gives a convert checklist (§7.11).
- **Namespaced id** (`slug:internalID`, §7.8) is **implemented** — it landed
  ahead of the driver-registry work as its own breaking change to the sandbox id
  schema (slug from `role.Backend.Slug()`, composed in `session.NewManager`,
  routing via `Service.resolveSessionID`).
- Remaining open (implementation stage, §7.14): exact Phase-2 wire framing,
  driver supervision, and the Phase-2 external Go-plugin ABI (the built-in
  Phase-1 shape is realized via `driver.Builder`/`BuildFunc` + `Register`).

## Definition of done

A change is done **only when CI is green**:

- `make lint` — golangci-lint, gofmt/gofumpt, gosec, govulncheck, go-arch-lint;
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
