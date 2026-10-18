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
  output); minimal 7-tool read→write→delete surface (`sandbox_list`,
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

---

## Open backlog (future work)

### Driver registry + opaque `options:` — IMPLEMENTED & action plan (2026-10-18)

The driver registry and the opaque `options:` profile key (SPEC §6.11/§7.11)
are **implemented, tested and documented** on branch
`docs/driver-options-design` (plus the earlier namespaced-id work). Summary and
next steps:

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
  - **Push `docs/driver-options-design` and open PR;** merge after the darwin e2e
    passes on macOS CI (`make e2e`; Docker-in-Docker e2e remains N/A).
  - **Live-test in a real seatbelt sandbox** (the `sandbox` MCP is available
    again): verify `/usr/bin/sandbox-exec` on the macOS host and run
    `make lint test build` inside a seatbelt sandbox.
  - **External drivers:** Phase 1 = in-process Go plugins
    (`driver.Builder` / `Register` already the ABI); Phase 2 (later) =
    out-of-process IPC (STDIO child + protobuf-like) + `SANDBOX_DRIVERS_DIR`
    loader/manifest — all per SPEC §6.11/§7.14.
  - **Orphan-GC + idle reaper** (per SPEC §6.6): wire `Manager.Reap` / `Touch` +
    an orphan sweep via the driver `List` reconciliation seam.
  - **govulncheck:** re-review accepted IDs `GO-2026-4883` / `GO-2026-4887`
    (docker plugin-authz) when a fix lands upstream.
  - **Docs polish (low):** wire live badges (coverage/gosec/govulncheck/gremlins)
    to real sources; pin CI tool versions in SPEC.

### Profile schema → envelope `apiVersion` + `spec` (Phase A done / in progress; Phase B future)

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
- [ ] **Docs (Phase A):** README / SPEC / AGENTS describe the envelope, the
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
- [ ] **Docs:** once wired, remove the "future work" callouts in `SPEC.md` §6.6,
      `README.md` "Sandbox lifecycle", `AGENTS.md` — `D01 D02`

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

- [ ] **Map `spec.Isolation.Seccomp` into the docker backend (R-3):**
      `buildHostConfig` (`infrastructure/backend/docker/docker.go`) does not map
      the seccomp field into any Docker `SecurityOpt` — only the daemon's default
      seccomp profile applies, so the field is decorative. Wire
      `iso.Seccomp` → `SecurityOpt: seccomp=<profile>` (or make the default
      profile explicit) and fix `SPEC.md` §7 so the field is not misleading —
      `@security` first, `S08 S05`, `C01 C08GO T01`
- [ ] **seatbelt: SIGTERM grace before SIGKILL (R-6):** `Backend.Destroy` and the
      exec-cancel path (`infrastructure/backend/darwin/darwin.go`) SIGKILL the
      whole process group with no grace period. Add a short, configurable SIGTERM
      phase before SIGKILL on destroy / timeout / cancel so children can flush/
      clean up; update `SPEC.md` §7.12 and `README.md` — `@developer`/`@qa`,
      `X05 C01 C04GO T01`
- [ ] **Always-mask `command`/`env` in the `sandbox_exec` access-log (R-4,
      stopgap):** owner decision 2026-10-18 — for now mask **ALL** env values and
      the whole `command` string in the access-log (no name/value heuristics;
      revisit value-based redaction against known session secrets later) —
      `@developer`/`@qa`, `S02 L05 L08 T01 C01`
- [ ] **Correct SPEC/README darwin "per-session 0700 secret dir" claims (R-2):**
      `SPEC.md` §7.12/§7.13 (str. 1676/1801/1951) and `README.md` §seatbelt state
      secrets are delivered as a `0600` file in a per-session `0700` dir on
      darwin/seatbelt, but the backend delivers **no** file secrets (redaction
      only). Fix the docs to state seatbelt does NOT deliver file secrets —
      `@techwriter`, `D01 D02`

- [ ] **govulncheck `ACCEPTED_IDS` — re-review trigger (R-5, verified 2026-10-18):**
      osv.dev still lists **no** fixed version on the used
      `github.com/docker/docker` path (latest `v28.5.2+incompatible`, 2025-11-05 —
      same as pinned); the only fix is on `github.com/moby/moby/v2`, still
      **pre-release** (latest `v2.0.0-beta.28`, 2026-10-14; no stable `v2.0.0`).
      So the exception remains valid. Add a check that flags when
      `moby/moby/v2 @latest` reaches a **stable `v2.0.0`**, then re-review and
      (optionally) migrate the docker dependency to `moby/moby/v2` and drop the
      accepted IDs — `@security`/`@devops`
- [ ] **Close symlink-escape beyond `ALLOW_DIRS` in `verifyHostPath` (R-1,
      MEDIUM):** `allowedSource` (`domain/sandbox/spec/enforce.go`) gates mount
      sources by **lexical** path only, and `verifyHostPath`
      (`infrastructure/backend/docker/docker.go`) resolves symlinks but does **not**
      re-check the resolved path against `ALLOW_DIRS` (constructor no longer takes
      allowDirs). A symlink inside an allowed dir pointing outside lets a docker
      bind-mount follow it out of scope (darwin analog: file-read rule projected
      without symlink resolution). Fix: when `allowSymlinks==false`, compare the
      **resolved** path against the allowed roots in `verifyHostPath` (thread
      allowDirs back into the backend, or resolve+re-gate at the Service level);
      on seatbelt, don't project a subpath rule for an outward symlink — `@security`
      first, `S04`, `C01 C08GO T01`

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
