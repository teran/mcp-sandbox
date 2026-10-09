# mcp-sandbox — TODO / Backlog

> Source: [`SPEC.md`](SPEC.md) (greenfield, Docker-first, security-first).
> Organized by phase; work is **TDD** (T01): `@qa` writes tests first, `@developer`
> writes the implementation. Each task carries its mcp-server-spec criteria.

## Phase 0 — Scaffold & foundation

- [ ] 0.1 Repo scaffold: `go.mod` (Go 1.27.1, go-sdk v1.8.0, logrus, docker SDK, go-docker-testsuite), `cmd/mcp-sandbox/main.go` (composition root, `-mode stdio`) — `C01GO C02GO M01 M06`
- [ ] 0.2 Makefile interface: `lint` / `test` / `build` / `e2e` (no `container-image`) + `.PHONY` — `R07 N31`
- [ ] 0.3 CI (GitHub Actions): lint / test / build / e2e + hard-gate jobs (gremlins 80/80, gitleaks) — `C01 C02 C03 C04 C08GO N36`
- [ ] 0.4 `.golangci.yml`, `.go-arch-lint.yml` (layers), `.goreleaser.yml` (ldflags) — `C03GO C07GO B01 B02`
- [ ] 0.5 Config (envconfig) + `ALLOW_DIRS`/`ALLOW_SYMLINKS` validation — `S04`
- [ ] 0.6 Logging: logrus, stdio→file channel, enablement by `LOG_LEVEL`, banner, SDK slog wiring, access-log at info, request_id — `L01–L09 L01GO–L04GO B05`

## Phase 1 — Domain model & ISP role interfaces

- [ ] 1.1 `domain/sandbox`: typed `SandboxSpec` (+ immutable `Isolation`) — `A01 S08`
- [ ] 1.2 Role interfaces: `Backend`, `Lifecycle`, `Executable`, `Filesystem`, `Introspectable` + capability set + `ErrCapabilityNotSupported` — `A01 M07`
- [ ] 1.3 `domain/session`: Session aggregate + Manager interface, `crypto/rand` id, per-session mutex — `X01 X05`
- [ ] 1.4 Domain unit tests (mocks, race) — `C01 C04GO T01`

## Phase 2 — Docker backend

- [ ] 2.1 Backend registry + capability select; `init()`-registration of docker; lazy `Available()` — `A02/N35 M06`
- [ ] 2.2 Docker implementation of roles via official docker SDK (unix socket) — `L02GO N05GO`
- [ ] 2.3 Isolation: read-only root, cap-drop ALL, no-new-privileges, seccomp (default), non-root user, pids/mem/cpu/ulimits, network bridge (host forbidden), tmpfs mounts (`/run` noexec + `/tmp` writable/executable), AutoRemove — `§7.1` (server-enforced, immutable)
- [ ] 2.4 Mounts/`ALLOW_DIRS`: EvalSymlinks + open-then-verify + `O_NOFOLLOW`, read-only default, minimize bind-mounts (`copy_in`/`copy_out`); **session `cwd` mounted 1:1 at the same absolute path** (read-only default) — `S04 N03 §7.2`
- [ ] 2.5 Secrets → tmpfs `0600`, redactor at output boundary (incl. Docker errors) — `S02 N02 L05`
- [ ] 2.6 Session: reaper (idle), `DestroyAll`, label ownership, idempotent destroy — `X01 X02 X05`
- [ ] 2.7 Docker unit tests (fake client) — `C01 C04GO`
- [ ] 2.8 Privilege model: default non-root + scoped root (`exec -u 0`); base image with known non-root user + writable/executable `/tmp` (and `/run` for secrets); `SudoPolicy` gating — `§7.10 S11`

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

- [x] 4.1 `e2e/contract` independent suite (lifecycle/exec/fs/copy/read-only-root/mount-immutability/uid-gid/concurrency/introspect/cleanup) — `T02 C09GO`
- [x] 4.2 `e2e/docker` + `make e2e` + e2e CI job — `C04 C09GO N07GO`

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
- [ ] 6.10 (follow-up) Honor `req.Timeout` via `context.WithTimeout` in `Exec` so timeouts become cancellations (terminal-notification path already covers it) — `X03`

## Deferred / open questions (discuss later, non-blocking)

- HITL client contract: exact confirmation semantics for destructive tools (opencode).
- gVisor/runsc + rootless as target state (T1 kernel-escape containment).
- Apple Container and other backends — extension after Docker.
- `CapStream` / `Mountable` capabilities (streaming, dynamic mounts) — future.
- Warm-instance pool — outside MVP (architecture ready via `SandboxSpec` hash).
- **Docker-in-Docker corner case** — running containers from inside the sandbox (e.g. `go-docker-testsuite`): needs a nested/rootless daemon or socket access; conflicts with the immutable Isolation — solve deliberately when needed.
- **cgroup / namespace management** — granular cgroup/namespace knobs on `sandbox_create` — deferred; the immutable `Isolation` block provides the security baseline.
