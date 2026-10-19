# mcp-sandbox — TODO / Backlog

> Source: [`SPEC.md`](SPEC.md) (greenfield, Docker-first, security-first).
> Work is **TDD** (T01): `@qa` writes tests first, `@developer` writes the
> implementation. Each task carries its mcp-server-spec criteria.

## Completed (implemented, tested, documented)

All planned phases through **Phase 7** are **implemented and wired**, and are
documented in `SPEC.md` / `README.md` / `AGENTS.md`. Summary:

- **Phase 0 — Scaffold & foundation:** repo scaffold, Makefile interface,
  CI (lint / test / build + gremlins 80/80 and gitleaks hard gates),
  golangci-lint / go-arch-lint / goreleaser config, env-driven config, logrus
  logging (stdio → file, `LOG_LEVEL`-gated).
- **Phase 1 — Domain & ISP roles:** typed `SandboxSpec` + immutable
  `Isolation`; `Backend` / `Lifecycle` / `Executable` / `Introspectable` role
  interfaces + `ErrCapabilityNotSupported` (no stubs).
- **Phase 2 — Docker backend:** constructor-register factory + lazy
  `Available()`; docker SDK (unix socket) implementation; immutable isolation;
  mounts / `ALLOW_DIRS` (cwd 1:1 read-only default); tmpfs secrets + redaction;
  session manager; privilege model (non-root + scoped `exec -u 0`).
- **Phase 3 — Tool framework + MCP surface:** `ToolDef` / Registry / shared
  handler-wrapper (validate → access-log → dispatch → redact → structured
  output); minimal 8-tool read→write→delete surface (`sandbox_list`,
  `sandbox_info`, `sandbox_stats`, `profile_list`, `sandbox_create`,
  `sandbox_exec`, `sandbox_destroy`, `sandbox_destroy_all`).
- **Phase 4 — e2e (darwin):** a **darwin e2e smoke suite** exists as
  `//go:build e2e && darwin` files, run under the generic `make e2e`
  (`go test -tags e2e ./...`) on a dedicated macOS CI `e2e` job. Docker-in-Docker
  e2e remains **N/A** (would conflict with the immutable hardening); the Docker
  handler→backend path is covered by unit tests + the coverage gate.
- **Phase 5 — Security verification:** security review, redaction-completeness,
  digest-pinning, privileges / rootless-docker decision.
- **Phase 6 — Streaming:** `domain/progress` + `notifications/progress` from
  `sandbox_exec` (line-batched, redacted, ANSI-sanitized); `req.Timeout` honored
  as a `context.WithTimeout` cancellation.
- **Phase 7 — Operator profiles:** read-only YAML profiles under
  `SANDBOX_PROFILES_DIR` (read on demand, no restart); `profile_list`;
  `sandbox_create` takes `profile` / `session_id` / `cwd` / `secrets`;
  removed `TRUSTED_REGISTRIES` / `SANDBOX_CWD_MODE` / `SANDBOX_CGROUP_*`.
- **Self-describing server:** MCP `instructions` + `sandbox://guidance/*`
  resources + enriched tool descriptions (no standalone skill file, no AGENTS.md
  pointer).
- **`sandbox_exec` input model:** scalar `command` via a fixed
  `[<shell>,"-c",command]` wrapper; `shell` as a profile field; `env` map;
  `summary`.
- **GOCACHE/GOTMPDIR recipe:** default profile `env` decided and folded into the
  `sandbox://guidance/filesystem-mounts` resource.
- **Driver registry + opaque `options:` (merged as PR #15):** `driver:` required
  (`auto`/empty removed) + opaque `options:` (`yaml.Node`); `driver.Options` /
  `Registry` / `Builder` / `BuildFunc`; built-in `docker` / `seatbelt` builders
  (registering via `init()` into `driver.Default`); `Service.BuildSpec` +
  `EnforceHardening` first in `Create`; `ALLOW_DIRS` containment lifted to the
  Service (docker backend keeps only the TOCTOU path check; `New(api, opts ...)`
  takes no allow-dirs arg); unknown driver → `ErrUnknownDriver` at use; reserved
  built-ins not shadowable; one-shot migration (no compat shim); examples
  converted; README / SPEC / AGENTS updated. Namespaced `slug:internalID`
  session id landed as PR #14.
- **Profile schema → envelope `apiVersion` + `spec` (Phase A):** envelope
  `apiVersion` + `spec` profile form; `spec.cwd`/`spec.shell` extracted by the
  store; `driver.Reconcile` fail-closed `apiVersion` check
  (`ErrAPIVersionMismatch`); built-in drivers declare
  `docker`/`seatbelt.profiles.sandbox.teran.dev/v1`; legacy `driver:` +
  `options:` + top-level `cwd`/`shell` still works but deprecated (warning at the
  tool boundary) — documented in README / SPEC / AGENTS. Phase B (remove the
  legacy form) remains future / owner decision.
- **Docs consistency pass (2026-10-19):** R-4 (explicitly document `command` is
  NOT masked, reversing the stopgap), R-2 (seatbelt delivers no file secrets),
  fake static badges removed + live CI gates note + CI tool versions pinned in
  SPEC §11, and GC "future work" callouts trimmed (accurate "explicit-only
  cleanup, no automatic GC" retained) — across SPEC / README / AGENTS / TODO.
  GC (reaper + orphan-GC) itself remains **not implemented**.

---

## Open backlog (future work)

### Driver registry + opaque `options:` — IMPLEMENTED & MERGED (PR #15, 2026-10-18)

The driver registry and the opaque `options:` profile key (SPEC §6.11/§7.11)
are **implemented, tested, documented and merged into `master`** (plus the
earlier namespaced-id work). Summary and next steps:

- **Done (implemented, tested, documented):**
  - **Namespaced sandbox id `slug:internalID`** (merged as #14): `Slug()` on
    `role.Backend` (docker → `docker`, seatbelt → `seatbelt`);
    `session.NewManager(slug, ...)` composes `slug:internalID`;
    `Service.resolveSessionID` routes by slug (no `sessionDriver` map); backends
    strip the slug before their underlying call; `ShortID` keeps the slug;
    destroy by unknown slug → `ErrSessionNotFound`; `sandbox_create` returns the
    namespaced `ShortID`.
  - **Driver registry + opaque `options:`** (branch `docs/driver-options-design`):
    profile schema `driver:` (**required**, `auto`/empty removed) + `options:`
    (opaque `yaml.Node`); `driver.Options` / `Registry` / `Builder` / `BuildFunc`;
    built-in `docker` / `seatbelt` builders (registering via `init()` into
    `driver.Default`); `Service.BuildSpec` + `EnforceHardening` **first** in
    `Create`; `ALLOW_DIRS` containment lifted to the Service (docker backend keeps
    only the TOCTOU `EvalSymlinks`/open-then-verify path check;
    `New(api, opts ...Option)` **no longer takes an allow-dirs arg**); unknown
    driver → `ErrUnknownDriver` at use; reserved built-ins not shadowable;
    **one-shot migration** (no compat shim for the old `docker:`/`seatbelt:`
    blocks); `examples/profiles/*.yml` converted; `README` / `SPEC` / `AGENTS`
    updated.
  - **Gates green on the merged branch:** `make lint` (0 issues), `make test`
    (95.8% ≥ 95), `make build` (goreleaser) — verified.

- **Open / next (action plan):**
  - [x] **Push `docs/driver-options-design` and open PR** — **DONE: merged as
        PR #15 (2026-10-18)** after the darwin e2e passed on macOS CI
        (`make e2e`; Docker-in-Docker e2e remains N/A).
  - [x] **Live-test in a real seatbelt sandbox — DONE (2026-10-20):** verified
    `/usr/bin/sandbox-exec` on the macOS host (`-rwxr-xr-x`, root:wheel) and ran
    `make lint`, `make test`, `make build` inside a live seatbelt sandbox
    (sandbox MCP profile `seatbelt`, session `seatbelt:darwin-2`). All three
    gates **PASS** (exit 0): lint clean (golangci 0 issues, vet, gosec,
    go-arch-lint OK; govulncheck only accepted no-fix `GO-2026-4883/4887`),
    `make test` green with coverage ≥ 95%, `make build` produced 4 goreleaser
    targets. Note: a nested `/usr/bin/sandbox-exec` inside a seatbelt sandbox
    returns `Operation not permitted` — expected macOS sandbox-nesting
    limitation, not a backend defect. Sandbox destroyed after the test.
  - **External drivers:** Phase 1 = in-process Go plugins
    (`driver.Builder` / `Register` already the ABI); Phase 2 (later) =
    out-of-process IPC (STDIO child + protobuf-like) + `SANDBOX_DRIVERS_DIR`
    loader/manifest — all per SPEC §6.11/§7.14.
  - **Orphan-GC + idle reaper** (per SPEC §6.6): wire `Manager.Reap` / `Touch` +
    an orphan sweep via the driver `List` reconciliation seam.
  - **govulncheck:** re-review accepted IDs `GO-2026-4883` / `GO-2026-4887`
    (docker plugin-authz) when a fix lands upstream.
  - [x] **Docs polish (2026-10-19):** fake always-green static badges
        (coverage/gosec/govulncheck/gremlins) **removed** from `README.md` —
        there is no per-job live source on shields.io (no `job` param on the
        workflow-status endpoint), so they are replaced by a single live **CI**
        badge + an explicit "CI gates" note. CI tool versions pinned in
        `SPEC.md` §11 ("Pinned CI tooling versions") — `@devops`, `D01 D02`.

### Profile schema → envelope `apiVersion` + `spec` (Phase A done; Phase B future)

The preferred profile schema migrates from the legacy `driver:` + `options:` +
top-level `cwd`/`shell` form to the **envelope** `apiVersion` + `spec`
(`SPEC.md` §7.11). `apiVersion` = `«<driver>.profiles.sandbox.teran.dev/vN»`
(driver = first DNS label; `vN` = profile-schema version, NOT the driver
implementation version); `spec` is driver-owned (replaces `options:`); `cwd`/
`shell` live **inside** `spec` as common server-understood fields. At USE time
`driver.Reconcile` fail-closes on an `apiVersion` mismatch
(`ErrAPIVersionMismatch`); legacy profiles without `apiVersion` pass.

- [x] **Phase A (additive) — envelope schema accepted & implemented:** the
      envelope `apiVersion` + `spec` profile form; `spec.cwd`/`spec.shell`
      extracted by the store into `Profile.Cwd`/`Profile.Shell`; `driver.Reconcile`
      fail-closed `apiVersion` check; built-in drivers declare
      `docker`/`seatbelt.profiles.sandbox.teran.dev/v1`; legacy `driver:` +
      `options:` + top-level `cwd`/`shell` **still works but deprecated** (warning
      at the tool boundary) — `@architect`/`@developer`, `D01 D02`
- [x] **Docs (Phase A):** README / SPEC / AGENTS describe the envelope, the
      migration table (legacy → envelope) and the deprecation of the legacy form —
      English, `D01 D02`
- [ ] **Phase B (future — owner decision):** make the `.spec` envelope the
      **only** accepted profile form and **remove** legacy `driver:` / `options:` /
      top-level `cwd` / `shell` — a breaking change, scheduled by the owner, not
      yet done (`SPEC.md` §7.11/§7.14) — `@architect`/`@developer`, `D01 D02`

### Sandbox lifecycle & GC (backlog — designed, not yet wired)

Sandboxes are currently removed **only** by explicit
`sandbox_destroy`/`sandbox_destroy_all`. There is **no automatic GC** today. See
`SPEC.md` §6.6.

- [ ] **Wire the idle reaper (in-process):** add a per-process background loop
      that periodically calls `Manager.Reap(ctx, cfg.SandboxIdleTimeout, now)`;
      ensure every per-session tool (`exec`/`info`/`stats`) calls
      `Session.Touch(at)` (not the global `list`); maintain the `busy` counter so
      a session with a running `exec` is never reaped — `X01 X02 X05`
- [ ] **Implement orphan-GC (process death):** graceful shutdown calls
      `DestroyAll()` of the process's own sessions; plus a startup/periodic sweep
      that lists `mcp-sandbox`-labeled containers and destroys any whose session
      id is not among the live sessions of this process — `X01`
- [ ] **Tests for GC:** unit (reaper skip-busy, `Touch`/`Expired`, orphan sweep)
      + e2e (idle reaping, orphan reclamation) — `C01 C04GO T01 T02 C09GO`
- [ ] **Docs (pending GC wiring):** once GC is actually wired, remove the "future
      work" callouts in `SPEC.md` §6.6, `README.md` "Sandbox lifecycle",
      `AGENTS.md` — `D01 D02`
      **Note (2026-10-19):** the "future work" framing for the reaper/orphan-GC
      callouts in `SPEC.md` §6.6, `README.md`, `AGENTS.md` was **trimmed** per
      owner decision (docs no longer advertise reaper/orphan-GC as a feature),
      while keeping the accurate "explicit-only cleanup, no automatic GC"
      statement. GC is **still NOT implemented** — this TODO (wire reaper +
      orphan-GC) remains open.

### Default GOCACHE/GOTMPDIR — apply to the `golang` profile (operator action)

Design and docs are done; the remaining step is an operator-side edit.

- [ ] **Apply to the `golang` profile:** operator adds the `env:` block
      (`GOCACHE=/tmp/.gocache`, `GOTMPDIR=/tmp`, `GOMODCACHE=/go/pkg/mod`) to
      `~/.config/sandbox/profiles/golang.yml` (external file, outside
      `ALLOW_DIRS`; cannot be edited from the sandbox).

### Go tooling in the image (backlog — design open)

The `golang` image ships no extra tooling (`golangci-lint`, `go-arch-lint`,
`gosec`, `govulncheck`, `gitleaks`, `gremlins`); `go install` is hampered by the
link-time mmap quirk and by there being no clean writable+exec location besides
the repo mount.

- [ ] **Document the mount matrix** (ro overlay / noexec tmpfs / virtiofs rw) as
      a dedicated `SPEC.md`/`README.md` section — English, `D01 D02`
- [ ] **Decide tooling delivery:** vendor the linters into a writable+exec
      location (repo-scoped `GOBIN`) vs. a custom tooling image vs. host-run
      linters; pick one that survives the mmap quirk — `@devops`/`@architect`
- [ ] **Wire it** so `make lint` (golangci, gosec, govulncheck, go-arch-lint,
      gremlins, gitleaks) runs cleanly from the sandbox without polluting the
      working tree — `@devops`/`@developer`, `C01 C03 C08GO`

### `go test -race` link flakiness on arm64 (`crosscall1`) (backlog — investigate)

Occasionally `go test -race` fails to link some test binaries on linux/arm64
with `undefined reference to crosscall1` — believed to be a build-cache /
parallel-link race, not a code bug. Workaround today: retry (warm cache) or
`go clean -cache`.

- [ ] **Reproduce & diagnose:** capture the failing link on arm64; confirm root
      cause — `@developer`, `C04GO T01`
- [ ] **Decide & fix:** serialize/limit parallel `-race` linking, pre-warm the
      cache, or pin a flag — `@developer`/`@qa`, `C01`
- [ ] **Docs:** record the outcome — English, `D01 D02`

### Docker from inside the sandbox (backlog — design open)

Running container-based e2e from inside a hardened sandbox needs a live Docker
daemon. Today this is out of scope (Phase 4, Docker-in-Docker e2e N/A; the
darwin e2e does not need a daemon).

- [ ] **Security review** of the socket-passthrough lane (host-root equivalence,
      `ALLOW_DIRS` gating of the socket path, default sandboxes stay isolated) —
      `@security` first, `S01 S11`
- [ ] **Author the opt-in profile:** golang image + host socket mount + host
      network; verify `go-docker-testsuite` drives the live daemon —
      `@devops`/`@developer`
- [ ] **Docs:** update `AGENTS.md`/`TODO.md` Phase-4 N/A note if the lane is
      adopted — English, `D01 D02`

### Darwin backend — `sandbox-exec` (Seatbelt) driver (implemented — portable core + darwin `log stream` tailer + darwin e2e)

Designed in `SPEC.md` §7.12 and `README.md` "macOS driver". macOS-only,
policy-based containment (deny-by-default SBPL), pure Go via `/usr/bin/
sandbox-exec` (no cgo, no root). Docker remains the default backend. The
portable core, the darwin `log stream` tailer wrapper and the darwin e2e smoke
suite are **implemented and exercised**: unit tests (mock runner/tailer,
coverage ≥ 95%) run in CI on any GOOS, and the live real-macOS verification
runs via `make e2e` on a dedicated macOS runner.

- [x] **`spec.Sandbox` type + profile `seatbelt:` block:** `domain/sandbox/
      spec.Sandbox{SBPL, MachLookups, Sysctls}` and `Profile.Sandbox`/`Seatbelt`
      (`application/profile`, yaml `seatbelt`); `BuildSpec` copies it into
      `SandboxSpec`; validation rejects NUL in SBPL — `@architect`/`@developer`,
      `D01 D02`
- [x] **SBPL generator + literal validator:** deterministic `SandboxSpec → SBPL`
      renderer (`domain/sandbox/sbpl`; imports → `deny default` → network → mach
      → sysctl → file → operator `seatbelt.sbpl`) plus fail-closed validation
      rejecting SBPL metacharacters (`" \ ( ) ; \n \r \0`) in any path —
      `@security` first, `S04 S11`
- [x] **darwin backend package:** `infrastructure/backend/darwin` registering via
      `init()` (Priority 10, `Driver` = `seatbelt`), `Available()` gated on
      `GOOS==darwin` + `/usr/bin/sandbox-exec` + `euid != 0`; portable core
      (`darwin.go`) + build-tag runner (`runner_darwin.go` / `runner_other.go`);
      `Lifecycle` (spawn/kill child) and `Executable` (fixed `[<shell>,"-c",command]`
      wrapper); `Introspectable` returns `ErrCapabilityNotSupported` — `@developer`,
      `C01`
- [x] **setrlimit wrapper:** `RLIMIT_CPU/NOFILE/AS/NPROC/FSIZE/STACK/CORE`
      applied before `exec` (child inherits); `Memory`/`NanoCPUs`/`PidsLimit`
      honestly reported as unsupported on darwin — `@developer`, `C01`
- [x] **Policy-denial detection — portable core:** fail-closed classifier +
      `ConsumeDenials` + `logTailer` boundary (`deny.go`), classified
      `sandbox_deny {session_id, pid, ...}` records (L08-style, no secrets, S02) —
      `@sre`/`@developer`, `L08 L09`
- [x] **Unit tests (no real sandbox-exec):** mock `os/exec` runner + in-memory
      tailer (like the docker fake client) so `make test` + coverage ≥ 95% hold in
      CI; SBPL injection-focussed tests — `@qa`, `C01 C08GO`
- [x] **`createSleepCmd` fix:** `"sleep infinity"` (GNU-ism) →
      `"sleep 2147483647"` (BSD-portable) in `darwin.go` — SPEC §7.12 —
      `@developer`, `C01`
- [x] **In-band policy-denial wired into `Backend.Exec`:** the `deny.go`
      classifier (`IsPolicyDenial`, previously dead code — never called from
      `darwin.go`) now sets the new `exec.Result.PolicyDenied` bool on the
      returned result — Docker backend unaffected — `@developer`, `L08`
- [x] **setrlimit applied-to-parent-and-never-restored fix:** portable
      `rlimit_apply.go` (`rlimitOps` + `rlimitSaveAndApply` + `rlimitRestore`,
      with rollback) wired into `realRunner.Start` with a deferred restore —
      parent limits always restored, child inherits the lowered limits at fork —
      `@developer`, `C01`
- [x] **darwin `log stream` tailer wrapper:** the macOS-specific unified-log tail
      (`subsystem == "com.apple.sandbox"`, spawned via `log stream`) behind the
      `darwin` build tag (`tailer_darwin.go`, `//go:build darwin`) is now
      **implemented** behind the portable `ConsumeDenials`/`logTailer` boundary
      (which is unit-tested with in-memory tailers); the live
      deny-classification on a real host is exercised by the darwin e2e —
      `@sre`/`@developer`, `L08 L09`
- [x] **darwin e2e on macOS:** a real-macOS smoke suite (`//go:build e2e &&
      darwin`) spawns a live `/usr/bin/sandbox-exec` and runs a live unified-log
      `log stream` tail; it runs under the generic **`make e2e`**
      (`go test -tags e2e ./...`) on a dedicated macOS CI `e2e` job — there is
      **no `e2e-darwin` target** by design — `@devops`, `N31 N36`
- [x] **Docs:** §7.12 / README "macOS driver" updated to reflect the implemented
      darwin `log stream` tailer and the darwin e2e — English, `D01 D02`

### Docker backend — container resolution after the session-id change (BUG — FIXED & CI-verified)

Introduced by the "session id = backend id" refactor (SPEC §6.6/§7.8): the session
manager now calls the backend first and uses its returned id as the session id
(docker = **container id**) and no longer passes a generated session id via
`WithSessionID`, so docker containers are created **without** the
`dev.teran.sandbox.session` label.

- [x] **Bug:** `Destroy`/`Exec`/`Info`/`Stats` (`infrastructure/backend/docker/
      docker.go:313,552,683,697`) still resolve the container via
      `containerIDBySession`, which filters by the `dev.teran.sandbox.session`
      label. Since the manager no longer sets that label, the filter matches
      nothing and every docker operation returns `ErrNotFound` in the
      integrated manager → backend flow. The docker unit tests pass only because
      they drive the backend directly with a session id in context (exercising
      the label path), not the integrated flow.
- [x] **Fix (TDD):** because the session id IS the container id, use the passed
      id directly as the container id in `Destroy`/`Exec`/`Info`/`Stats` (the
      Docker API errors naturally for an unknown id); drop the now-obsolete
      `containerIDBySession` label lookup; update the docker tests that assert
      the label path. The `dev.teran.sandbox.owner` label remains for
      orphan-identification. Criteria: `X01 X02 S04`.
      **Status:** code + tests **done** and **CI-verified** — `make test`
      (`go test -race`, coverage 95.9% ≥ 95), `make e2e`, lint and the mutation
      gate all pass (2026-10-17 conformance audit). The integrated
      manager → backend flow is covered by `manager_integration_test.go`.

### Docker / tool layer — short-id routing bug (BUG — FIXED & CI-verified)

`sandbox_create` returns `domainsession.ShortID(s.ID())` (12 chars) and
`sandbox_list`/`sandbox_info` display `ShortID` too
(`application/tools/sandbox/{write,read}.go`), but `Service.Exec/Info/Stats/
Destroy` route via `runtimeFor(id)` (`infrastructure/mcp/service.go:172`) which
does an **exact** `sessionDriver[id]` lookup keyed by the **full** session id
(registered as `sess.ID()` in `Service.Create`, service.go:220). So a client
that echoes the returned/displayed short id always gets `ErrSessionNotFound`
(or a silent no-op destroy) — **no docker tool op can ever route through the
tools**. This is independent of the (fixed) docker label bug above.

- [x] **Fix (TDD):** make tool-id routing resolve short ids — either have
      `runtimeFor` do prefix resolution against live session ids (mirroring
      `Manager.resolveID`, SPEC §6.6), or return/pass the full id. Keep
      `Destroy` idempotent and preserve prefix-ambiguity handling. Criteria:
      `X01 X02`. `@architect`/`@developer`/`@qa`. This also unblocks running
      `make test` from inside a docker sandbox.
      **Status: code + tests done and CI-verified** (`@qa` added
      `TestService_ShortID_*`, `TestService_PrefixAmbiguous`, etc.; `@developer`
      added prefix-resolving `Service.resolveID` in
      `infrastructure/mcp/service.go` returning the owning runtime + full id,
      routed through `Get`/`Destroy`/`Exec`/`Info`/`Stats`, `runtimeFor`
      removed, full id passed to backends). `make test` (coverage 95.9%),
      `make e2e`, lint and mutation all pass (2026-10-17 conformance audit);
      tool-id routing is no longer blocked.

### Security hardening — conformance & security audit (2026-10-18)

Findings from the 2026-10-18 conformance (`qa`) + security (`security`) review.
Approved for the backlog; implemented **TDD** (`@qa` tests first, `@developer`
impl) with `@security` review first.

- [x] **Map `spec.Isolation.Seccomp` into the docker backend (R-3) — RESOLVED via
      the alternative option (fail-closed marker + explicit default):** the
      field is **no longer decorative** and is **not** wired into any Docker
      `SecurityOpt` **by design**. `spec.Isolation.Seccomp` is now a fail-closed
      **closed-enum marker** — `domain/sandbox/spec/spec.go` admits only
      `SeccompDefault` (`IsValidSeccompProfile`), enforced by `spec.Validate()`
      and `EnforceHardening` (`domain/sandbox/spec/enforce.go`). The docker
      backend deliberately emits **no** seccomp `SecurityOpt`
      (`infrastructure/backend/docker/docker.go:354-361`, comment "INVARIANT
      (R-3)"): `seccomp=default` is rejected by Docker and
      `seccomp=unconfined`/`<path>` would **weaken** security — this takes the
      original TODO's offered alternative **"make the default profile
      explicit"**. Guarded by `TestBuildHostConfig_NoSeccompEmission`
      (`infrastructure/backend/docker/docker_helpers_test.go`); `SPEC.md` §7
      updated (~534, 978, 1904) so the field is not misleading.
      **Status: code + tests done and CI-verified** (`@security`/`@developer`/
      `@qa`). The **only remaining** seccomp backlog item is the **custom
      seccomp profile** ("mandatory roadmap", `SPEC.md` §7.7) — not yet
      implemented.
- [x] **seatbelt: SIGTERM grace before SIGKILL (R-6):** `Backend.Destroy` and the
      exec-cancel path (`infrastructure/backend/darwin/darwin.go`) SIGKILL the
      whole process group with no grace period. Add a short, configurable SIGTERM
      phase before SIGKILL on destroy / timeout / cancel so children can flush/
      clean up; update `SPEC.md` §7.12 and `README.md` — `@developer`/`@qa`,
      `X05 C01 C04GO T01`
      **Status: implemented & CI-verified (2026-10-19).** Both `Backend.Destroy`
      and the exec cancel/timeout path now do group-wide `SIGTERM` → grace →
      group-wide `SIGKILL`, reaping exactly once. Grace is configurable via
      `WithKillGrace(d)` (default `defaultKillGrace` = 1s, hard-capped at
      `maxKillGrace` = 5s, clamped fail-closed: ≤0 → default, >max → max); it
      does **not** weaken containment (still sandboxed under SIGTERM; SIGKILL is
      untrappable) and is bounded (≤ 5s) so a hostile SIGTERM-ignoring child
      cannot cause unbounded teardown delay. Also fixes the pre-existing **X05
      double-`Wait`** panic via a per-session `reapMu` + a `sync.Once` in
      `osProcess` (concurrent Destroy / Destroy-vs-Exec-cancel safe). `SPEC.md`
      §7.12/§7.13 and `README.md` updated.
- [x] **R-4 RESOLVED — explicitly document that `command` is NOT masked**
      (owner decision 2026-10-19, reversing the earlier stopgap): instead of
      masking `command`/`summary`, the docs now state explicitly that the
      `sandbox_exec` access-log records `command` **as-is and intentionally
      NOT masked** (S02 disclaimer; no masking applied; `command` is not a
      channel for secrets — secrets go into tmpfs `0600` files). Applied to
      `SPEC.md` §7, `README.md` Security, `AGENTS.md`. — `@techwriter`, `D01 D02`
- [x] **Correct SPEC/README darwin "per-session 0700 secret dir" claims (R-2):**
      `SPEC.md` §7.12/§7.13 (str. 1691/1819/1972) and `README.md` seatbelt state
      now state clearly that seatbelt delivers **no file secrets** (redaction
      only; secrets never leave the server process as files). — `@techwriter`,
      `D01 D02`

- [ ] **govulncheck `ACCEPTED_IDS` — re-review trigger (R-5, verified 2026-10-18):**
      osv.dev still lists **no** fixed version on the used
      `github.com/docker/docker` path (latest `v28.5.2+incompatible`, 2025-11-05 —
      same as pinned); the only fix is on `github.com/moby/moby/v2`, still
      **pre-release** (latest `v2.0.0-beta.28`, 2026-10-14; no stable `v2.0.0`).
      So the exception remains valid. Add a check that flags when
      `moby/moby/v2 @latest` reaches a **stable `v2.0.0`**, then re-review and
      (optionally) migrate the docker dependency to `moby/moby/v2` and drop the
      accepted IDs — `@security`/`@devops`
- [x] **RESOLVED — by design (2026-10-20): symlink inside an allowed dir pointing
      outside is NOT a sandbox escape (R-1, wontfix).** A symlink physically
      located inside an `ALLOW_DIRS` tree that points to a path outside it is an
      **operator-authored** host-side directory entry, not a containment breach:
      the sandbox **cannot** create host-side symlinks outside its mounted roots
      (the host filesystem is not writable beyond the mounts), bind-mounts are
      fixed at `ContainerCreate` time (a running sandbox cannot add/redirect
      mounts), and a symlink created inside a mounted tree resolves **within the
      container's namespace** (in-container `/etc` is the container rootfs, not
      the host). So reaching e.g. `/Users/teran/secret` via
      `/Users/teran/work/link -> /Users/teran/secret` requires the operator to
      have ALREADY placed that pointer in the allowed tree AND referenced it in a
      profile — operator trust/misconfiguration, not an escape. `verifyHostPath`
      (`infrastructure/backend/docker/docker.go`) therefore does **identity-only**
      `EvalSymlinks` + open-then-verify (TOCTOU), with **no** resolved-path
      re-gate against `ALLOW_DIRS` — and by this decision it intentionally stays
      that way. **No code change; docs updated** (`SPEC.md` §7.2/§7.13,
      `README.md`, `AGENTS.md`). — `@security`, `S04`

### Deferred / open questions (discuss later, non-blocking)

- HITL client contract: exact confirmation semantics for destructive tools (opencode).
- gVisor/runsc + rootless as target state (T1 kernel-escape containment).
- Apple Container and other backends — extension after Docker (darwin driver
  designed in §7.12; gVisor/runsc as kernel-escape target).
- `CapStream` / `Mountable` capabilities (streaming, dynamic mounts) — future.
- Warm-instance pool — outside MVP (architecture ready via `SandboxSpec` hash).
- **Docker-in-Docker corner case** — running containers from inside the sandbox
  (e.g. `go-docker-testsuite`): needs a nested/rootless daemon or socket access;
  conflicts with the immutable Isolation — solve deliberately when needed.
- **cgroup / namespace management** — granular cgroup/namespace knobs on
  `sandbox_create` — deferred; the immutable `Isolation` block provides the
  security baseline.
