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
- [ ] 2.3 Isolation: read-only root, cap-drop ALL, no-new-privileges, seccomp (default), non-root user, pids/mem/cpu/ulimits, network none (host forbidden), tmpfs workdir, AutoRemove — `§7.1` (server-enforced, immutable)
- [ ] 2.4 Mounts/`ALLOW_DIRS`: EvalSymlinks + open-then-verify + `O_NOFOLLOW`, read-only default, minimize bind-mounts (`copy_in`/`copy_out`) — `S04 N03`
- [ ] 2.5 Secrets → tmpfs `0600`, redactor at output boundary (incl. Docker errors) — `S02 N02 L05`
- [ ] 2.6 Session: reaper (idle), `DestroyAll`, label ownership, idempotent destroy — `X01 X02 X05`
- [ ] 2.7 Docker unit tests (fake client) — `C01 C04GO`

## Phase 3 — Declarative tool framework + MCP surface

- [ ] 3.1 `application/tools`: `ToolDef`, Registry, shared handler-wrapper (validate → access-log → dispatch → redact → structured output) — `M04 S08 S09 L08`
- [ ] 3.2 MCP server wiring (go-sdk, slog→logrus) — `M01 L07/L03GO`
- [ ] 3.3 READ tools: `sandbox_list`, `sandbox_info`, `sandbox_stats`, `sandbox_read_file` — `M05 M07 S03`
- [ ] 3.4 WRITE tools: `sandbox_create`, `sandbox_exec` (argument-array, no shell), `sandbox_write_file`, `sandbox_copy_in`, `sandbox_copy_out`, `sandbox_install_package` (fixed apt/dnf per SudoPolicy) — `S11 N21 S02`
- [ ] 3.5 DELETE tools: `sandbox_destroy`, `sandbox_destroy_all` (destructiveHint, client-side HITL) — `S12 S03`
- [ ] 3.6 Tool tests (annotations, schemas, redaction, access-log) — `M04 S08 S09`

## Phase 4 — e2e contract suite

- [ ] 4.1 `e2e/contract` independent suite (lifecycle/exec/fs/copy/read-only-root/mount-immutability/uid-gid/concurrency/introspect/cleanup) — `T02 C09GO`
- [ ] 4.2 `e2e/docker` + `make e2e` + e2e CI job — `C04 C09GO N07GO`

## Phase 5 — Security verification

- [ ] 5.1 Security review of implementation (Isolation, mounts, redaction, socket) — `S01–S13`
- [ ] 5.2 Redaction-completeness + focused tests; mutations catch gaps — `S02 C02`
- [ ] 5.3 Pin images by digest + trusted registries — `C05 T7`
- [ ] 5.4 Server privileges / rootless docker decision — `§7.6`
- [ ] 5.5 Final conformance check (@qa per checklist) — all

## Deferred / open questions (discuss later, non-blocking)

- HITL client contract: exact confirmation semantics for destructive tools (opencode).
- gVisor/runsc + rootless as target state (T1 kernel-escape containment).
- Apple Container and other backends — extension after Docker.
- `CapStream` / `Mountable` capabilities (streaming, dynamic mounts) — future.
- Warm-instance pool — outside MVP (architecture ready via `SandboxSpec` hash).
