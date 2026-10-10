# mcp-sandbox — TODO / Backlog

> Source: [`SPEC.md`](SPEC.md) (greenfield, Docker-first, security-first).
> Organized by phase; work is **TDD** (T01): `@qa` writes tests first, `@developer`
> writes the implementation. Each task carries its mcp-server-spec criteria.

## Phase 0 — Scaffold & foundation

- [x] 0.1 Repo scaffold: `go.mod` (Go 1.27.1, go-sdk v1.8.0, logrus, docker SDK), `cmd/mcp-sandbox/main.go` (composition root, `-mode stdio`) — `C01GO C02GO M01 M06`
- [x] 0.2 Makefile interface: `lint` / `test` / `build` (no `container-image`, no `e2e`) + `.PHONY` — `R07 N31`
- [x] 0.3 CI (GitHub Actions): lint / test / build + hard-gate jobs (gremlins 80/80, gitleaks) — `C01 C02 C03 C08GO N36`
- [x] 0.4 `.golangci.yml`, `.go-arch-lint.yml` (layers), `.goreleaser.yml` (ldflags) — `C03GO C07GO B01 B02`
- [x] 0.5 Config (envconfig) + `ALLOW_DIRS`/`ALLOW_SYMLINKS` validation — `S04`
- [x] 0.6 Logging: logrus, stdio→file channel, enablement by `LOG_LEVEL`, banner, SDK slog wiring, access-log at info, request_id — `L01–L09 L01GO–L04GO B05`

## Phase 1 — Domain model & ISP role interfaces

- [x] 1.1 `domain/sandbox`: typed `SandboxSpec` (+ immutable `Isolation`) — `A01 S08`
- [x] 1.2 Role interfaces: `Backend`, `Lifecycle`, `Executable`, `Filesystem`, `Introspectable` + capability set + `ErrCapabilityNotSupported` — `A01 M07`
- [x] 1.3 `domain/session`: Session aggregate + Manager interface, `crypto/rand` id, per-session mutex — `X01 X05`
- [x] 1.4 Domain unit tests (mocks, race) — `C01 C04GO T01`

## Phase 2 — Docker backend

- [x] 2.1 Backend registry + capability select; `init()`-registration of docker; lazy `Available()` — `A02/N35 M06`
- [x] 2.2 Docker implementation of roles via official docker SDK (unix socket) — `L02GO N05GO`
- [x] 2.3 Isolation: read-only root, cap-drop ALL, no-new-privileges, seccomp (default), non-root user, pids/mem/cpu/ulimits, network bridge (host forbidden), tmpfs mounts (`/run` noexec + `/tmp` writable/executable), AutoRemove — `§7.1` (server-enforced, immutable)
- [x] 2.4 Mounts/`ALLOW_DIRS`: EvalSymlinks + open-then-verify, read-only default, minimize bind-mounts (`copy_in`/`copy_out`); **session `cwd` mounted 1:1 at the same absolute path** (read-only default) — `S04 N03 §7.2`
- [x] 2.5 Secrets → tmpfs `0600`, redactor at output boundary (incl. Docker errors) — `S02 N02 L05`
- [x] 2.6 Session: reaper (idle), `DestroyAll`, label ownership, idempotent destroy — `X01 X02 X05`
- [x] 2.7 Docker unit tests (fake client) — `C01 C04GO`
- [x] 2.8 Privilege model: default non-root + scoped root (`exec -u 0`); base image with known non-root user + writable/executable `/tmp` (and `/run` for secrets); `SudoPolicy` gating — `§7.10 S11`

## Phase 3 — Declarative tool framework + MCP surface

- [x] 3.1 `application/tools`: `ToolDef`, Registry, shared handler-wrapper (validate → access-log → dispatch → redact → structured output) — `M04 S08 S09 L08`
- [x] 3.2 MCP server wiring (go-sdk, slog→logrus) — `M01 L07/L03GO`
- [x] 3.3 READ tools: `sandbox_list`, `sandbox_info`, `sandbox_stats` (originally also `read_file` — trimmed) — `M05 M07 S03`
- [x] 3.4 WRITE tools: `sandbox_create`, `sandbox_exec` (argument-array, no shell) (originally also `write_file`/`copy_in`/`copy_out`/`install_package` — trimmed) — `S11 N21 S02`
- [x] 3.5 DELETE tools: `sandbox_destroy`, `sandbox_destroy_all` (destructiveHint, client-side HITL) — `S12 S03`
- [x] 3.6 Tool tests (annotations, schemas, redaction, access-log) — `M04 S08 S09`
- [x] 3.7 **Trim** MCP surface to the minimal 7-tool set — remove `sandbox_read_file`, `sandbox_write_file`, `sandbox_copy_in`, `sandbox_copy_out`, `sandbox_install_package` (files come from image/host/runtime; **mount + exec is the core**; §4.2) — `M07 S03`
- [x] 3.8 cwd mount **read-only by default** + opt-in `rw` via `SANDBOX_CWD_MODE` (align with host-side writes) — `§7.2`

## Phase 4 — e2e contract suite

**DECIDED — e2e is N/A (removed).** No e2e suite, no `make e2e`, no e2e CI job.
A real e2e would require Docker-in-Docker from inside a hardened sandbox
(nested/rootless daemon or socket access), which conflicts with the immutable
`Isolation` block and is out of the MVP scope. The full tool-handler → Docker
backend path is covered by unit tests + the C01 coverage gate. `C09GO N07GO
T02 C04 N30` declared not applicable. The `e2e/` directory is removed.

## Phase 5 — Security verification

- [x] 5.1 Security review of implementation (Isolation, mounts, redaction, socket) — `S01–S13`
- [x] 5.2 Redaction-completeness + focused tests; mutations catch gaps — `S02 C02`
- [x] 5.3 Pin images by digest + trusted registries — `C05 T7`
- [x] 5.6 **Configurable trusted-registry allowlist** (`TRUSTED_REGISTRIES` env, REPLACE semantics, exact-match validation, lowercased/deduped, fail-closed) — replaces the hardcoded set in 5.3 — `C05 T7 §7.9`
- [x] 5.7 **Operator-configurable cgroup resource limits** (`SANDBOX_CGROUP_CPU_LIMIT_CORES` cores → `NanoCPUs`; `SANDBOX_CGROUP_MEMORY_LIMIT_GB` GiB → bytes with `MemorySwap` tracked so swap stays off; unset/`0` ⇒ immutable defaults apply) — `§7.1 §6.8`
- [x] 5.4 Server privileges / rootless docker decision — `§7.6`
- [x] 5.5 Final conformance check (@qa per checklist) — all

## Phase 6 — Streaming output via MCP progress notifications

Goal: emit `notifications/progress` from `sandbox_exec` (and any long-running tool) so the model-host sees live command output and the call parameters while the command runs. Additive — without a progress token behavior is byte-for-byte unchanged. See the architect design (session note); summary:

- [x] 6.1 `domain/progress` (new pure-domain package): `Event{Message, Progress, Total}`, `Sink func(ctx, Event)`, `WithSink`/`SinkFromContext` — threaded via `context` (repo idiom like `WithSessionID`) — `A01`
- [x] 6.2 `LineBatcher` (in `domain/progress`): line-buffered chunking, flush-on-complete-line + partial-line size threshold + `Flush()` on EOF, per-message cap, monotonic `Progress` byte counter; **no goroutine/ticker** (zero lifecycle risk) — `A01 T01`
- [x] 6.3 `application/tools/handler`: add `Dependencies.ProgressSink func(*mcp.CallToolRequest) progress.Sink`; stash sink into ctx in `Call` before dispatch (generic, every tool inherits) — `M04`
- [x] 6.4 `infrastructure/mcp/server.go`: wire default `ProgressSink` = closure over `req.Session.NotifyProgress` + `req.Params.GetProgressToken()` (nil-safe) — `M01`
- [x] 6.5 `application/tools/sandbox/write.go`: `sandboxExecDef` wraps ctx sink with `redactSecrets` + `sanitizeANSI` (S02, secrets known at tool layer), pass to `Exec` — `S02 S09`
- [x] 6.6 `infrastructure/backend/docker/docker.go`: `Exec` reads sink from ctx; emit `start` (redacted argv) → chunks (stdout/stderr from `stdcopy` demux fed to batchers) → `done` (exit code); terminal `cancelled` event on ctx cancel before returning the existing error — `L02GO X03`
- [x] 6.7 Unit tests: `domain/progress` (batcher, roundtrip), handler (sink injection), sandbox (redaction wrap), docker fake (streaming, cancel, nil-sink unchanged) — `C01 C04GO T01`
- [x] 6.8 e2e (`e2e/contract` + `e2e/docker`): progress scenario asserts ≥1 chunk arrives before completion; cancel/timeout scenario asserts terminal notification; final `Result` still byte-for-byte — `T02 C09GO`
- [x] 6.9 Docs: SPEC/README/AGENTS — document progress notifications and the `sandbox_exec` streaming behavior (English) — `D01 D02`
- [x] 6.10 Honor `req.Timeout` via `context.WithTimeout` in `Exec` so timeouts become cancellations (terminal-notification path already covers it) — `X03`

## Phase 7 — Operator-authored sandbox profiles

Goal: move the runtime shape of a sandbox (image, env, mounts, network, cgroup
limits, user, sudo) out of the per-request `sandbox_create` knobs and operator
env vars into **read-only, operator-authored YAML profiles** under
`SANDBOX_PROFILES_DIR`. The model references a profile **by name**; there are
**no** profile create/update/delete tools. `ALLOW_DIRS` is retained as the
host-path gate for `cwd` + every profile mount source; the immutable
`Isolation` block is never weakened. See `SPEC.md` §6.5/§7.9/§7.11.

Status: **implemented and wired** (TDD — `@qa` wrote the tests first, `@developer`
landed the implementation). Verified against the code:

- `domain/sandbox/profile` — `Profile`, `Validate`, `BuildSpec` (`profile_test.go`).
- `application/profile` — read-only `Load`/`Get`/`List` store (`store_test.go`),
  wired into `application/config` as `SANDBOX_PROFILES_DIR` and loaded in
  `cmd/mcp-sandbox/main.go` (fallback to an empty store on load error).
- `application/tools/profile` — `profile_list` tool (`profile_list_test.go`),
  registered in `infrastructure/mcp/server.go`.
- `application/tools/sandbox/write.go` — `sandbox_create` rework: input is
  `{profile, cwd, session_id, secrets}`; resolves profile → `BuildSpec`;
  unknown profile ⇒ error. The removed envs (`TRUSTED_REGISTRIES`,
  `SANDBOX_CWD_MODE`, `SANDBOX_CGROUP_*`, `ALLOW_NETWORK_MODES`) are **gone**
  from `application/config` and pinned as inert by `config_test.go`.

- [x] 7.1 `domain/sandbox/profile`: typed `Profile` (Name, Description, Image,
      Env, Mounts, CwdConfig, Network, Cgroups, User, Sudo), `Validate`
      (non-root enforced, bounded cgroups, name regex + path-traversal
      rejection, **image parse-only** — any registry, no allowlist), and
      `BuildSpec(isolation, Params{Cwd, Secrets, ClientSessionID})`
      (profile → `SandboxSpec`; cwd always mounted 1:1 read-only default;
      profile-owned fields override policy; hardening never overridden) —
      `A01 S04 §6.5 §7.11`
- [x] 7.2 `domain/sandbox/profile` implementation: `Profile`, `Mount`,
      `CwdConfig`, `Network`, `Cgroups`, `User`, `Sudo`, `Validate`,
      `BuildSpec` — `A01 S04`
- [x] 7.3 `application/profile` store: scan `SANDBOX_PROFILES_DIR`, parse +
      validate each `<name>.yml`, `Get`/`List` (sorted); **read-only** (no
      create/update/delete); fail-closed on unparsable/invalid/duplicate/
      traversal `name`; missing dir ⇒ empty store — `S04`
- [x] 7.4 `application/profile` store implementation: `Load`, `Get`, `List`,
      `SANDBOX_PROFILES_DIR` wiring in `application/config` — `S04`
- [x] 7.5 `application/tools/profile`: read-only `profile_list` tool
      (annotations: read-only/idempotent/non-destructive/open-world false;
      output `{profiles:[{name,description}]}` with **no leakage** of
      env/mounts/image/user/sudo — S02) — `M05 M07 S02 S03`
- [x] 7.6 `application/tools/profile` implementation + register `profile_list`
      in the shared registry — `M05 M07 S03`
- [x] 7.7 **`sandbox_create` rework**: input becomes `profile` (req),
      `session_id` (req), `cwd` (req), `secrets` (opt); **remove** `image`,
      `env`, `mounts`, `nano_cpus`, `memory`, `pids_limit`, `uid`, `gid`,
      `sudo`; resolve profile → `BuildSpec`; error "not found" for unknown
      profile — `M05 S08 X02`
- [x] 7.8 Backend/config/spec changes: drop `validateImageRef` registry
      allowlist (parse-only); remove `TRUSTED_REGISTRIES`, `SANDBOX_CWD_MODE`,
      `SANDBOX_CGROUP_CPU_LIMIT_CORES`, `SANDBOX_CGROUP_MEMORY_LIMIT_GB`,
      `ALLOW_NETWORK_MODES`; network from profile (host valid when
      operator-authored); limits from profile `cgroups`; defaults from server —
      `§7.1 §7.9 §7.11`
- [x] 7.9 Allowlist removals cleanup: delete `DefaultTrustedRegistries`,
      `DefaultNetworkModes` gating of host (host allowed via profile), and the
      `TRUSTED_REGISTRIES`/`SANDBOX_CWD_MODE`/`SANDBOX_CGROUP_*` config paths —
      `S04 §7.9`
- [x] 7.10 Tests: unit (domain build/validate, store, tool, sandbox_create
      build-spec + unknown-profile) — `C01 C04GO T01`. (e2e sub-part is **N/A**
      — the repo has no e2e suite, consistent with Phase 4.)
- [x] 7.11 Docs: SPEC/README/AGENTS — profiles schema + trust model,
      `SANDBOX_PROFILES_DIR`, `profile_list`, removed envs, breaking
      `sandbox_create` migration note, digest-pinning convention — `D01 D02`

## Sandbox lifecycle & GC (backlog — designed, not yet wired)

These document **intended** behavior that is currently **non-functional**: sandboxes
are removed only by explicit `sandbox_destroy`/`sandbox_destroy_all`. See
`SPEC.md` §6.6.

- [ ] **Wire the idle reaper (in-process):** add a per-process background loop that
      periodically calls `Manager.Reap(ctx, cfg.SandboxIdleTimeout, now)`; ensure
      every per-session tool (`exec`/`info`/`stats`) calls `Session.Touch(at)` (not
      the global `list`); maintain the `busy` counter (increment on exec start,
      decrement + `Touch` on exec end) so a session with a running `exec` is never
      reaped even if it outlives the idle window — `X01 X02 X05`
- [ ] **Implement orphan-GC (process death):** graceful shutdown calls
      `DestroyAll()` of the process's own sessions; plus a startup/periodic sweep
      that lists `mcp-sandbox`-labeled containers (ownership label + session id)
      and destroys any whose session id is not among the live sessions of this
      process — `X01`
- [ ] **Tests for GC:** unit (reaper skip-busy, `Touch`/`Expired`, orphan sweep)
      + e2e (idle reaping, orphan reclamation) — `C01 C04GO T01 T02 C09GO`
- [ ] **Docs:** flip the "NOT WIRED / non-functional" callouts in `SPEC.md`
      `§6.6`, `README.md` "Sandbox lifecycle & GC", `AGENTS.md` once wired — `D01 D02`

## Deferred / open questions (discuss later, non-blocking)

- HITL client contract: exact confirmation semantics for destructive tools (opencode).
- gVisor/runsc + rootless as target state (T1 kernel-escape containment).
- Apple Container and other backends — extension after Docker.
- `CapStream` / `Mountable` capabilities (streaming, dynamic mounts) — future.
- Warm-instance pool — outside MVP (architecture ready via `SandboxSpec` hash).
- **Docker-in-Docker corner case** — running containers from inside the sandbox (e.g. `go-docker-testsuite`): needs a nested/rootless daemon or socket access; conflicts with the immutable Isolation — solve deliberately when needed.
- **cgroup / namespace management** — granular cgroup/namespace knobs on `sandbox_create` — deferred; the immutable `Isolation` block provides the security baseline.
