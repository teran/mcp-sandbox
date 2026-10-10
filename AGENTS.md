# AGENTS.md

Guidance for AI agents working in this repository. `AGENTS.md`, like
`SPEC.md`, is **strictly English** (D02). The README is English by default
(D01).

## This repository

- An **MCP server** that provides a safe, isolated Docker **sandbox** so a
  model-host client (opencode) can run arbitrary commands **without using the
  host shell** — per-session ephemeral containers that are destroyed when the
  session ends.
- Implementation language: **Go 1.27.2** (see `go.mod` / `GO_VERSION` in CI)
  (**C02GO/C01GO**).
- See **[`SPEC.md`](SPEC.md)** for the application-level architecture and the
  security / data / logging contracts that **every change must respect**.
- The server is **self-describing** over the MCP protocol (server-level
  `Instructions` + `sandbox://guidance/*` resources + enriched tool
  descriptions, `SPEC.md` §5.2) — there is **no standalone skill file** and no
  AGENTS.md pointer; a model-host discovers usage directly over the protocol.

## Working agents

| Agent | Role in this repo |
|-------|-------------------|
| **@architect** | Architecture, dependency and system-design decisions (DDD/Clean, Backend roles). |
| **@qa** | Writes tests first (isolated context) — unit, mutation, e2e; owns the coverage gate. |
| **@developer** | Writes the implementation (isolated context) to make the tests pass. |
| **@security** | Reviews the security model (Isolation, ALLOW_DIRS, redaction, Docker socket). |
| **@devops** | CI/CD, goreleaser release, Makefile interface. |
| **@techwriter** | Keeps `SPEC.md`, `README.md`, `AGENTS.md` current and English. |

**TDD workflow (T01):** tests first (@qa), implementation second (@developer).
Never weaken tests to make the implementation pass.

## Build system (R07) & quality gates (CI enforces these)

CI binds to the **build-system interface** (R07): it calls `make <target>`, not
raw language-specific commands. Interface targets:

- `make build` — binaries for all architectures via goreleaser (B01/B02).
- `make test` — `go test -race` + **coverage ≥ 95%** (C01/C04GO).
- `make lint` — golangci-lint, gosec, govulncheck, go-arch-lint.
- `container-image` is **NOT declared** — this is a **Local stdio-only**
  server; no image (B03/R01). A CI job must never invoke an undeclared optional
  target (N31).

> **e2e (C09GO / N07GO / T02 / C04) is N/A.** There is **no e2e suite** and no
> `make e2e` target. A meaningful e2e would require driving the real Docker
> daemon from inside a sandbox, which conflicts with the immutable `Isolation`
> hardening (Docker-in-Docker needs a nested/rootless daemon or socket access,
> outside the MVP scope). The full tool-handler → Docker-backend path is covered
> by unit tests + the coverage gate. `go-docker-testsuite` is **not** a
> dependency. No e2e CI job; N30 not violated (no e2e tests exist).

Additional hard-gate jobs (not part of the make interface): **gremlins**
mutation testing at **80/80** (C08GO) and **gitleaks** secret scan over git
history (C03/N28).

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
  > **command is logged as-is (S02 disclaimer):** the `sandbox_exec` access-log
  > records `command` as passed and is **not** secret-value-redacted. `command`
  > is **not** a channel for secrets — never place a secret in a command line;
  > use tmpfs `0600` secret files. Logging is opt-in (only when `LOG_LEVEL` is
  > set, L02) and the file is `chmod 600` (L01).
- **Filesystem scope (S04/N03):** host access only via `ALLOW_DIRS`, empty by
  default. `ALLOW_SYMLINKS=false` by default — canonicalize with
  `EvalSymlinks`, open-then-verify; reject anything escaping the
  allowed roots. Hardlinks are an OS-level concern, not the path check's.
  Prefer `copy_in`/`copy_out`; write only into tmpfs.
- **No shell/SQL/URL concatenation (S11/N21):** **no shell-string
  concatenation/interpolation** — `sandbox_exec` takes a scalar `command`
  through a **fixed** `[<shell>,"-c",command]` wrapper (shell from the profile,
  default `bash`); no user string is interpolated into a shell string.
  `sandbox_install_package` uses a **fixed** apt/dnf invocation — never
  interpolate into a shell string.
- **HITL (S12):** destructive tools (`sandbox_destroy`, `sandbox_destroy_all`)
  require **human confirmation**, realized **client-side** by the model-host
  (stdio server has no HTTP UI).
- **Docker socket (documented limitation):** the socket is effectively root on
  the host. Prefer **rootless Docker** or confine socket access to the server
  process. Never expose the socket to output/tools.
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

- **No general outbound HTTP** service calls exist. The only backend is the
  local Docker daemon via the **official `github.com/docker/docker` SDK**
  (unix socket). Do **not** hand-roll a low-level `http.Client` for Docker
  (N05GO) — resty is not suited to the unix-socket transport. If a future
  backend requires genuine remote HTTP, use `resty.dev/v3` (L02GO).
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
- **Session lifecycle / GC (designed, NOT YET WIRED):** the idle reaper
  (`Manager.Reap`, `Session.Touch`/`Expired`, `SANDBOX_IDLE_TIMEOUT`) and the
  orphan-GC are **designed but non-functional today** — no goroutine calls
  `Reap`, no tool calls `Touch`, and there is no orphan sweep. Sandboxes are
  removed **only** by explicit `sandbox_destroy`/`sandbox_destroy_all`. Any
  change touching session lifecycle/GC should treat these as open work (see
  `SPEC.md` §6.6 and `TODO.md`).
- **Idempotency (X02):** `sandbox_create` = search-before-create;
  `sandbox_destroy`/`destroy_all` = naturally idempotent; `write_file`/
  `copy_*` = natural-key upsert. Declare `idempotentHint:true` only where a real
  mechanism exists (N25).
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
  `args`, `source` (`STDIO`), `duration`, `outcome`.
- **`request_id` (L09/L04GO):** per request, via context.
- **Logger:** `logrus` (N04GO). **No secrets in logs (L05).**

### Architecture (A01)

Layered DDD/Clean with **top-level named packages** — **no `internal/`**
(recorded decision in `SPEC.md` §6.1). Respect the layer edges enforced by
**go-arch-lint**: `cmd/` → `application/` + `infrastructure/`; `application/` →
`domain/`; `infrastructure/` → `domain/`. `domain/` must **not** import
infrastructure.

- Backend roles (`Backend`, `Lifecycle`, `Executable`, `Filesystem`,
  `Introspectable`) are dispatched by **type-assertion**; unsupported
  capabilities return **`ErrCapabilityNotSupported`** — **no stubs**.
- Tools are declared as **`ToolDef`s** and must go through the shared
  handler-wrapper (validate → access-log → dispatch → redact → structured
  output). **Do not bypass the wrapper.**
- **Streaming (`domain/progress`, SPEC §5.1):** the progress `Sink` is threaded
  via `context` (`WithSink`/`SinkFromContext`), so it is **generic across every
  tool** — the shared handler stashes it into the context before dispatch.
  `LineBatcher` is **line-buffered with no goroutine/ticker** (zero lifecycle
  risk). Nil-sink leaves behavior **byte-for-byte unchanged**.

## Definition of done

A change is done **only when CI is green**:

- `make lint` — golangci-lint, gofmt/gofumpt, gosec, govulncheck, go-arch-lint;
  findings fixed, not suppressed.
- `make test` — `go test -race` with **coverage ≥ 95%**.
- **gremlins** mutation gate (80/80) — no survivors.
- **gitleaks** secret scan over git history — clean.
- `make build` — goreleaser produces the release binaries (when a build is
  part of the change).
- Docs (`SPEC.md`, `README.md`, `AGENTS.md`) remain **English** and in sync
  with any changed behavior.
