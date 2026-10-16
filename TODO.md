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

### Docker backend — container resolution after the session-id change (BUG — fixed in code + tests, CI pending)

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
      **Status:** code + tests **done** (`@qa` updated `docker_test.go`,
      `fake_client_test.go`, `docker_error_test.go`, `docker_logging_test.go`,
      `progress_test.go` and added `manager_integration_test.go`; `@developer`
      changed `docker.go`). **CI verification still pending** — `go test` could
      not be run in the working environment (see next section: the short-id
      routing bug blocks all sandbox tool exec). Run `make test` + `make e2e`
      to confirm coverage ≥ 95% and the lint/mutation gates.

### Docker / tool layer — short-id routing bug (NEW — open, blocks tool exec)

`sandbox_create` returns `domainsession.ShortID(s.ID())` (12 chars) and
`sandbox_list`/`sandbox_info` display `ShortID` too
(`application/tools/sandbox/{write,read}.go`), but `Service.Exec/Info/Stats/
Destroy` route via `runtimeFor(id)` (`infrastructure/mcp/service.go:172`) which
does an **exact** `sessionDriver[id]` lookup keyed by the **full** session id
(registered as `sess.ID()` in `Service.Create`, service.go:220). So a client
that echoes the returned/displayed short id always gets `ErrSessionNotFound`
(or a silent no-op destroy) — **no docker tool op can ever route through the
tools**. This is independent of the (fixed) docker label bug above.

- [ ] **Fix (TDD):** make tool-id routing resolve short ids — either have
      `runtimeFor` do prefix resolution against live session ids (mirroring
      `Manager.resolveID`, SPEC §6.6), or return/pass the full id. Keep
      `Destroy` idempotent and preserve prefix-ambiguity handling. Criteria:
      `X01 X02`. `@architect`/`@developer`/`@qa`. This also unblocks running
      `make test` from inside a docker sandbox.
      **Status: code + tests done** (`@qa` added `TestService_ShortID_*`,
      `TestService_PrefixAmbiguous`, etc.; `@developer` added prefix-resolving
      `Service.resolveID` in `infrastructure/mcp/service.go` returning the
      owning runtime + full id, routed through `Get`/`Destroy`/`Exec`/`Info`/
      `Stats`, `runtimeFor` removed, full id passed to backends). **CI
      verification pending** — requires the server to be rebuilt + restarted so
      tool exec unblocks; then `make test` + `make e2e`.

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
