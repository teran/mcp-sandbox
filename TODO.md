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
- **Phase 4 — e2e:** **N/A** — no e2e suite (a real e2e needs Docker-in-Docker,
  conflicting with the immutable hardening); the handler→backend path is covered
  by unit tests + the coverage gate.
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
daemon. Today this is out of scope (Phase 4, e2e N/A).

- [ ] **Security review** of the socket-passthrough lane (host-root equivalence,
      `ALLOW_DIRS` gating of the socket path, default sandboxes stay isolated) —
      `@security` first, `S01 S11`
- [ ] **Author the opt-in profile:** golang image + host socket mount + host
      network; verify `go-docker-testsuite` drives the live daemon —
      `@devops`/`@developer`
- [ ] **Docs:** update `AGENTS.md`/`TODO.md` Phase-4 N/A note if the lane is
      adopted — English, `D01 D02`

### Deferred / open questions (discuss later, non-blocking)

- HITL client contract: exact confirmation semantics for destructive tools (opencode).
- gVisor/runsc + rootless as target state (T1 kernel-escape containment).
- Apple Container and other backends — extension after Docker.
- `CapStream` / `Mountable` capabilities (streaming, dynamic mounts) — future.
- Warm-instance pool — outside MVP (architecture ready via `SandboxSpec` hash).
- **Docker-in-Docker corner case** — running containers from inside the sandbox
  (e.g. `go-docker-testsuite`): needs a nested/rootless daemon or socket access;
  conflicts with the immutable Isolation — solve deliberately when needed.
- **cgroup / namespace management** — granular cgroup/namespace knobs on
  `sandbox_create` — deferred; the immutable `Isolation` block provides the
  security baseline.
