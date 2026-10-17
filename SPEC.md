# SPEC — mcp-sandbox

> Status: **draft v1** — first pass for user review.
>
> This document is **strictly English** (D02). It is the application-level
> architecture and conformance reference for the **mcp-sandbox** MCP server.
> Every section carries the criteria codes it satisfies so a reviewer can trace
> a `MUST` back to a section. Codes resolve against the `reference/criteria/`
> files of the external **`mcp-server-spec` skill** (not a repo directory); the
> pinned versions (Go, go-sdk, logrus) come from that skill's
> `reference/versions.md`. None of these `reference/` paths exist in this repo —
> they belong to the external skill.

---

## 1. Overview

**mcp-sandbox** is a **Local (stdio-only)** MCP server that gives a model-host
client (e.g. **opencode**) a safe, isolated environment to run arbitrary
commands — a **replacement for the host shell**.

The model-host does **not** use its own host-shell. Instead, for every user
session it drives:

- a **per-session, isolated Docker container** (a _sandbox_),
- with the **required directories mounted in** (e.g. the working directory
  `cwd`),
- where **arbitrary commands are executed inside** the container, and
- which is **ephemeral** and per-session — the container is destroyed
  **explicitly** by `sandbox_destroy` / `sandbox_destroy_all` (there is **no
  automatic GC**; see [§6.6](#66-session-lifecycle)).

The **top priority is security**: the sandbox is a drop-in replacement for the
host shell, so it must be _more_ restrictive than the host, never less. All
hardening (see [Security](#7-security)) is **enforced by the server** and
encoded in an **immutable `Isolation` block** that a caller cannot weaken.

### Scope

| Scope                                  | Status                                                                                                                                                                                                                                                                                                                                                                                                    |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Docker backend**                     | ✅ **MVP — implemented**                                                                                                                                                                                                                                                                                                                                                                                  |
| Apple Container (and other backends)   | ⏳ **Future extension** — the `Backend` abstraction is designed for it (see [Architecture](#6-architecture)) but is **not** implemented in the MVP. An **experimental macOS `sandbox-exec` (Seatbelt) driver** is **implemented (portable core wired + darwin `log stream` tailer; darwin e2e run via `make e2e` on macOS)** — see [§7.12](#712-apple--sandbox-exec-driver-darwin-backend--experimental). |
| `CapStream` / `Mountable` capabilities | ⏳ Future — **not** implemented in the MVP                                                                                                                                                                                                                                                                                                                                                                |

The rationale for Docker-first: _"Docker is everywhere"_ — it is the most
widely available container runtime and the fastest path to a safe MVP.

**Language:** Go (**C02GO**), pinned to the latest stable **Go 1.27.2**
(**C01GO**; pinned version from the external `mcp-server-spec` skill).
Official MCP SDK: **go-sdk v1.8.0** (**M01**).

---

## 2. Repository & CI

- **Repository host & path (R05):** `github.com/teran/mcp-sandbox` — a **public
  GitHub.com** repository, decided before development starts.
- **Module path (S06):** because the repository is **public on GitHub.com**, the
  standard **public module path** is used: `github.com/teran/mcp-sandbox`. (S06
  requires local-only names only for **internal Forgejo** repos; a public
  GitHub.com server uses standard public paths.)
- **Default branch (R02):** `master` (never `main`).
- **CI provider (R06):** **GitHub Actions** — the native CI of the repository
  host. A single provider is used (no multi-provider setup).
- **Language profile:** Go profile (see [CI/CD](#11-cicd)).

---

## 3. Transports & Auth decisions

### 3.1 Deployment type (M06)

**Deployment type:** **Local (stdio-only)**.

The server is launched by the model-host as a local child process. It
communicates over **STDIO** — short-lived, no listener, no network surface. The
consequences of _Local / stdio-only_:

- **Launch mode (M06):** the server always runs the **stdio** transport — there
  is **no mode flag or parameter** to select a launch mode. For a Local server
  the transport is fixed to `stdio`.
- **Auth:** via **environment variables** (`ALLOW_DIRS`, `LOG_*`), never HTTP
  headers.
- **Logging (L01/L02):** stdio → **file** (`LOG_FILENAME`), never stdout;
  enabled **only when `LOG_LEVEL` is set**.
- **No container image (R01/B03):** a Local stdio-only server produces no image
  — `container-image` is not an applicable Makefile target (N31/N17).
- **No observability endpoint (O01/N32):** observability applies only to
  Remote/Hybrid. A Local stdio server has no `:8080`/`:8081` ports — see
  [§10 Metrics & Observability](#10-metrics--observability).

### 3.2 Transport decision (M02)

**STDIO** is the transport. Reasoning: the server is a **local companion** to a
model-host process — it is spawned by the client, lives only for the session,
has no remote callers, and must not open a listener. STDIO is the native,
secure-fit transport for this topology (no port exposure, no proxy in front).

### 3.3 OAuth2 decision (M03)

**No OAuth2.** The server has **no upstream HTTP API that it authorizes
against** — its only "backend" is the local Docker daemon accessed via a unix
socket (the daemon itself has no OAuth2-for-API). There is nothing for OAuth2
to protect. This is the fleet default (**no OAuth2**). No tokens, no
authorization-code flows.

### 3.4 TLS statement (S01 / N01)

**TLS is never implemented inside the server.** The server is Local / stdio-only
and has **no HTTP listener at all** — there is no TLS-terminating surface
inside the server. If the server is ever used behind a network-facing gateway
in the future, TLS termination is the reverse proxy's job, never the server's.

---

## 4. Tool surface

All tools are **specific, explicitly named and strongly typed** (**M07**) — no
generic CRUD/passthrough mega-tools. Each tool is a **complete use case**
(**M05**): a single call accomplishes a task end-to-end without the client
chaining low-level primitives. Each tool carries **Annotations + Instructions**
(**M04**) and an **output/redaction contract** (**S02**). Tools are grouped in
**read → write → delete** priority order (**S03**).

Every tool declares:

- `inputSchema` — strict, `additionalProperties: false`, validated **before**
  execution (**S08**);
- `outputSchema` — structured output (**S09**);
- a **RedactionContract** (**S02**) — which fields are `secret:true` and hidden
  from output and logs;
- **Annotations** and per-tool **Instructions**.

All tools: `openWorldHint: false` (**S07/S10** — closed domain; no tool is an
open-world tool, so none is "isolated from ALLOW_DIRS" — the entire server is
the sandbox domain).

### 4.1 READ tools (`readOnlyHint: true`)

| Tool            | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description                                                                                                                                                                                                                                                      |
| --------------- | -------------- | ----------------- | ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sandbox_list`  | `true`         | `false`           | `true`           | List sandbox sessions (id, backend, image, created, state).                                                                                                                                                                                                      |
| `sandbox_info`  | `true`         | `false`           | `true`           | Detailed spec + state of one sandbox (image, mounts, resources, isolation flags, secrets redacted).                                                                                                                                                              |
| `sandbox_stats` | `true`         | `false`           | `true`           | Live resource usage of one sandbox (CPU/mem/net/IO via Docker stats).                                                                                                                                                                                            |
| `profile_list`  | `true`         | `false`           | `true`           | List operator-authored sandbox profiles (name, description). **Phase 7.** Read-only over the `SANDBOX_PROFILES_DIR` store (§7.11). Output carries **only** `name`/`description` — no env values, mount sources, image, user or sudo policy is emitted (**S02**). |

**Instructions (common to READ):** read-only; never mutate the sandbox; paths
must resolve inside the sandbox scope; sensitive content is redacted per the
redaction contract before returning; never returns raw untrusted free-form
text without an `outputSchema`. `profile_list` reads from the **read-only
profile store** — it never writes, edits or deletes a profile (there are no
profile create/update/delete tools, §7.11).

### 4.2 WRITE / UPDATE tools (`readOnlyHint: false`, `destructiveHint: false`)

| Tool             | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| ---------------- | -------------- | ----------------- | ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `sandbox_create` | `false`        | `false`           | `false`          | Create a sandbox from an **operator-authored profile** (**Phase 7**, §7.11). Takes **required** `profile` (the name of a profile in the read-only store), `session_id` (the OpenCode client session id, recorded as a container label for traceability, §7.8) and `cwd` (the model-host's session working directory, mounted per the profile's `cwd` config — 1:1 at the same absolute path by default). Optional `secrets` are delivered as tmpfs `0600` files. All per-request knobs that used to exist — `image`, `env`, `mounts`, `nano_cpus`, `memory`, `pids_limit`, `uid`, `gid`, `sudo` — are **removed**; they now come **from the profile** (§7.11). Idempotency hint `false`: creation is a distinct side-effect; duplicate creation is surfaced as an error (search-before-create still applies, X02).                                                                                                                                                                                                                                                                                                                                    |
| `sandbox_exec`   | `false`        | `false`           | `false`          | Execute a command **inside** the sandbox. **Classified as write** (the container filesystem may be modified by the command). Takes **required** `id` and `command` (a scalar string, the full command line); optional `env` (a `map[string]string` of per-exec `KEY=VALUE` environment entries, literal — no shell evaluation), `summary` (a short human-readable description of the command, e.g. "run unit tests"; purely presentational — it is **not** passed to `exec.Request` and has no effect on execution; MCP clients use it to display the command in the tool-call header), plus `working_dir`, `timeout` and `user`. The `command` is executed via the profile-configured shell as a **fixed** wrapper `[ <shell>, "-c", command ]` through the Docker exec API (§7.5). Runs under the sandbox's **unprivileged user** by default; a scoped `user`/`sudo` option runs as uid 0 per `SudoPolicy` (§7.10). ANSI sanitized on output (**S09**); paths/command redacted (**S02**). Optional `timeout` (seconds) is applied via `context.WithTimeout` and becomes a cancellation that reaches the Docker calls and the progress sink (§6.10). |

> **Files come from the image, the host, or the runtime — never via MCP file-write
> tools.** The session `cwd` is mounted 1:1, so the model-host writes files on the
> **host** and the container reads/executes them at the same path; runtime
> artifacts live in the writable tmpfs `/tmp` (executable where the daemon
> honours `exec` on tmpfs; on daemons that force `noexec` — e.g. Docker Desktop —
> compilation goes into the writable bind-mounted `cwd`, per the profile's
> `cwd.mode`). Therefore the MVP exposes **no**
> `write_file`/`copy_in`/`copy_out`/`read_file`/`install_package` tools — **mount +
> exec is the core mechanism** (see §7.2). Per-request cgroup/namespace knobs are
> gone — resource limits are owned by the operator profile (§7.11) — and
> Docker-in-Docker is deferred (see TODO "Deferred").

**Instructions — `sandbox_create`:** reference an **operator-authored profile**
by `profile` name (from `SANDBOX_PROFILES_DIR`) — the image, env, mounts,
network, cgroup limits, user and sudo all come from the profile (§7.11); pass
the OpenCode `session_id` (recorded as a container label) and the `cwd` to
mount 1:1 (read-only by default, per the profile's `cwd.mode`). Optional
`secrets` are delivered as tmpfs `0600` files — never in `argv`. This is a
distinct side-effect (not idempotent): a duplicate `session_id` is surfaced as
an error. The immutable `Isolation` hardening block is applied automatically and
cannot be weakened by the caller.

**Instructions — `sandbox_exec`:** pass a single scalar `command` string (the
full command line); it is executed via a **fixed** shell wrapper
`[ <shell>, "-c", command ]` where `<shell>` is the profile-configured shell
(§7.11) — **no shell-string concatenation/interpolation** of separate parts
(§7.5). Optional `env` sets per-exec environment entries (`KEY=VALUE`), with
**literal values** — no shell evaluation; newlines/NUL and empty keys are
rejected (`=` in the value is allowed). Optional `summary` holds a short
human-readable description of the command (e.g. "run unit tests"); it is purely
presentational — not passed to `exec.Request` and without effect on execution —
so MCP clients can render the command in the tool-call header (scalar
primitives are displayed; the visible `command` is the primary display scalar).
Output is ANSI-sanitized; the command runs under the sandbox user with
`no-new-privileges`; only allowed capabilities are available. A scoped
`user: root` / sudo-classed exec runs as uid 0 per the sandbox's `SudoPolicy`
(§7.10) — never `--privileged`. Note: the `command`, per-exec `env` and
`summary` are recorded as-is in the access-log and are **not** a channel for
secrets (see §7.3 S02 disclaimer).
Optional `timeout` (seconds) is honored as a `context.WithTimeout` cancellation
that also reaches the Docker API calls and any progress stream (§6.10);
`timeout == 0` means no deadline.

> **⚠ Phase 7 — breaking change to `sandbox_create`.** The request shape changed
> incompatibly. Callers that passed `image`, `env`, `mounts`, `nano_cpus`,
> `memory`, `pids_limit`, `uid`, `gid` or `sudo` on `sandbox_create` must
> migrate to an **operator-authored profile** (§7.11): encode those values in a
> `<name>.yml` under `SANDBOX_PROFILES_DIR`, then pass only `profile`,
> `session_id`, `cwd` and (optionally) `secrets`. The old parameters are
> **rejected** (they are removed from the input schema), so an un-migrated
> caller fails fast with `InvalidParams`. `profile` and `cwd` are now both
> **required**. See README "Migration" and TODO Phase 7.

### 4.3 DELETE tools (`destructiveHint: true`, HITL)

| Tool                  | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description                                                                           |
| --------------------- | -------------- | ----------------- | ---------------- | ------------------------------------------------------------------------------------- |
| `sandbox_destroy`     | `false`        | `true`            | `true`           | Destroy one sandbox (idempotent — destroying a non-existent session is a no-op, X02). |
| `sandbox_destroy_all` | `false`        | `true`            | `true`           | Destroy all sandbox sessions (idempotent).                                            |

**Instructions — DELETE tools:** both are **destructive** and require **human
confirmation before invocation** (HITL, §4.4, realized client-side for this
Local stdio server). They are **idempotent** — destroying a missing session is a
no-op. Never call them automatically/cascading; use `sandbox_destroy` for one
session and `sandbox_destroy_all` only when every sandbox must be torn down
(e.g. server shutdown).

### 4.4 Human-in-the-loop for destructive tools (S12)

`destructiveHint: true` tools (`sandbox_destroy`, `sandbox_destroy_all`) are
**human-confirmable/deniable**. There is **no automatic cascading** of
destructive calls.

Because the server is **Local / stdio-only** and has **no HTTP UI** (S12),
HITL is implemented **client-side**: the model-host (opencode)
presents the destructive action to the human operator and obtains
**confirmation before the tool is invoked**. The server itself does not hold
confirmation state; it relies on the client contract that destructive calls are
only issued after human consent. This is recorded here so the implementation
and review are aware of where the HITL gate lives.

---

## 5. Protocol surface

The server implements the **`tools`** capability of the MCP protocol via the
official **go-sdk v1.8.0** (**M01**).

| MCP capability | Implemented         | Notes                                                                                                                                                                                                                           |
| -------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **tools**      | ✅                  | The entire sandbox surface (§4).                                                                                                                                                                                                |
| prompts        | ❌                  | Out of scope.                                                                                                                                                                                                                   |
| resources      | ✅                  | **Used for `sandbox://guidance/*` self-describing playbooks only** (§5.2). File I/O **never** goes through MCP resources — files come from the image, the host, or the runtime via tools (§4.2). Unknown URIs return not-found. |
| sampling       | ❌                  | Out of scope.                                                                                                                                                                                                                   |
| roots          | ❌                  | Out of scope — the sandbox scope is configured by `ALLOW_DIRS`, not MCP roots.                                                                                                                                                  |
| logging        | ❌ (as MCP logging) | The SDK logger is wired to the server's logrus logger (L07); MCP-client-visible logging is not used.                                                                                                                            |

Per-tool **Instructions** are preferred over server-level instructions and are
exposed per tool (M04).

### 5.2 Self-describing server

The server is **self-describing** over the MCP protocol so a model-host client
can discover how to use it without external files (**M04/M08**):

- **Server-level `Instructions`** — a short paragraph exposing the redirect
  invariant (**"The host shell is restricted — route commands through this
  sandbox server."**), tool grouping (read → write → delete), a secrets warning
  (never place secrets in a command or env — use tmpfs `0600` secret files on
  `sandbox_create`), and an index of the guidance resources below. This text is
  produced by the pure application-layer `application/tools/guidance` package
  (`guidance.Instructions()`), which must **not** import the go-sdk.
- **`resources` capability (`sandbox://guidance/*`)** — six read-only,
  **markdown** playbooks registered as MCP resources and served via the
  `resources/read` handler; unknown URIs return **not-found**:

  | URI                                            | Title                                                            |
  | ---------------------------------------------- | ---------------------------------------------------------------- |
  | `sandbox://guidance/overview`                  | mcp-sandbox overview (tool grouping, annotations, HITL)          |
  | `sandbox://guidance/shell-restricted-redirect` | Host shell restricted — route commands through the sandbox       |
  | `sandbox://guidance/sandbox-create`            | Creating a sandbox                                               |
  | `sandbox://guidance/sandbox-exec`              | Executing commands in a sandbox                                  |
  | `sandbox://guidance/filesystem-mounts`         | Filesystem layout and mounts (incl. the GOCACHE/GOTMPDIR recipe) |
  | `sandbox://guidance/security`                  | Security model                                                   |

  Resource content is **secrets-safe (S02)** and **ANSI-safe (S09)**. This is
  the **only** use of the `resources` capability — file I/O is **never** routed
  through MCP resources; it goes through tools (`mount + exec`, §4.2).

- **Enriched tool descriptions** — `sandbox_create` / `sandbox_exec` carry a
  redirect note (see `guidance.RedirectNote`) plus a pointer to the relevant
  guidance resource, so the model-host is directed away from the host shell
  right where the tools are invoked.

There is **no standalone skill file** and no AGENTS.md pointer required — the
self-description travels with the server over the protocol.

### 5.1 Streaming output via `notifications/progress`

Long-running tools — starting with `sandbox_exec` — can stream live output and
call parameters to the model-host via the MCP **`notifications/progress`**
notification while the command runs. This is **additive**: the client opts in by
passing a **progress token**; without one, tool behavior (and the final
`Result`) is **byte-for-byte unchanged** and no progress notifications are
emitted.

**Streaming contract** (`sandbox_exec` and any long-running tool):

1. **`start`** — emitted once when execution begins, carrying the **redacted**
   `command` so the model-host sees the actual call parameters that are running.
2. **chunks** — stdout/stderr are demuxed (via the Docker `stdcopy` stream) and
   fed through the line-batched `domain/progress.LineBatcher`; each batch
   becomes a progress notification whose `Message` is **ANSI-sanitized** and
   **secrets-redacted** (S02/S09).
3. **`done`** — terminal event carrying the command's **exit code**.
4. **`cancelled`** — terminal event emitted on **context cancellation** (e.g. a
   client `cancel`, or a `req.Timeout` honored as a cancellation) **before** the
   existing error is returned to the model.

**Security note:** streaming output is subject to the same contract as all other
output — it passes through the **redaction layer (S02)** and **ANSI sanitization
(S09)** before being emitted, so no secret and no control escape sequence leaks
to the model-host through the progress channel (see §7.3, §7.4).

**Architecture:** streaming is a pure-domain concern in the new
`domain/progress` package — `Event{Message, Progress, Total}`, a `Sink` callback
and the `LineBatcher`. The sink is threaded **generically** via `context`
(`WithSink`/`SinkFromContext`) so **every** tool inherits it; it is **nil-safe**
(no sink / no progress token ⇒ no-op, behavior unchanged). `LineBatcher` is
**line-buffered with no goroutine or ticker** (zero lifecycle risk): it batches
on complete lines and a partial-line size threshold, flushes on EOF, applies a
per-message cap, and tracks a **monotonic** `Progress` byte counter. See
§6.1 for the package layout and §7.3/§7.4 for the redaction/ANSI guarantees.

---

## 6. Architecture

### 6.1 Layout — DDD / Clean, top-level named packages (A01)

The layout is **always layered** (A01/C07GO) using **top-level named packages by
default** — **no `internal/`** visibility wrapper. Rationale for _not_ using
`internal/` (recorded per A01): this is an application (composition root), not
a library imported by external code, and the layer edges are already enforced
by **go-arch-lint** (C07GO). There is no concrete reason to restrict package
visibility, so the top-level default is kept.

```
cmd/mcp-sandbox/           composition root: config load, logger, banner,
                           layer composition, stdio transport start
domain/                    pure domain model, no infra dependencies
  sandbox/
    spec/                  typed SandboxSpec (Image, WorkingDir, Env, Mounts,
                           Resources, Network, UIDGID, Sudo, Secrets,
                           ReadOnlyRoot, Isolation), validation
    profile/               operator-authored profile model (Phase 7): Profile,
                           Mount, CwdConfig, Network, Cgroups, User, Sudo,
                           Validate, BuildSpec (profile → SandboxSpec,
                           §6.5/§7.11)
    exec/                  exec request/result types, result classification
    role/                  ISP role interfaces: Backend, Lifecycle, Executable,
                           Introspectable (+ ErrCapabilityNotSupported)
    capability/            capability set types
  session/                 Session aggregate: id, spec, state, activity markers
                           (Touch/Expired), mutex
  progress/                pure-domain streaming: Event{Message, Progress, Total},
                           Sink, LineBatcher (line-buffered, no goroutine),
                           WithSink/SinkFromContext (context-threaded)
application/               use-case orchestration
  config/                  env-driven Config (ALLOW_DIRS, SANDBOX_PROFILES_DIR,
                           LOG_*), validation
  profile/                 profile store (Phase 7): reads on demand — scans
                           SANDBOX_PROFILES_DIR, parses + validates each
                           <name>.yml, Get/List (read-only — no
                           create/update/delete)
  sandbox/                 sandbox service: create/use/destroy, Backend select
  session/                 session manager: registry, lifecycle (explicit
                           destroy; idle reaper is future work)
  tools/                   declarative tool framework
    def/                   ToolDef (Name/Title/Description/Annotations/
                           Instructions/InputSchema/OutputSchema/
                           RedactionContract/Handler)
    registry/              tool registry
    handler/               shared handler-wrapper: validate → access-log →
                           dispatch → redact → structured output
    profile/               read-only profile_list tool (ToolDef, Phase 7)
infrastructure/            adapters
  backend/
    registry/              backend factory registry (constructor-register)
    select/                capability-based backend selection
    docker/                Docker backend (official docker SDK, unix socket)
  mcp/                     MCP server wiring (go-sdk), SDK slog→logrus logger
  logging/                 logrus setup, channel routing, redaction
```

### 6.2 Role interfaces (ISP) — capability dispatch

The backend contract is split into **small, role-specific interfaces** (ISP).
The base role is **`Backend`**:

```go
type Backend interface {
    Name() string              // "docker"
    Priority() int             // ordering for backend selection
    Capabilities() CapabilitySet
}
```

Role interfaces, each implemented _by_ a backend and discovered via
**type-assertion**:

```go
type Lifecycle      interface { Create(ctx, spec) (id, error); Destroy(ctx, id) error }
type Executable     interface { Exec(ctx, id, req) (*ExecResult, error) }
type Introspectable interface { Info(ctx, id); Stats(ctx, id) }
```

- Capability dispatch is done through **type-assertion** against these roles.
- **No stub/no-op methods**: a backend that does not implement a role is
  detected and the tool returns **`ErrCapabilityNotSupported`** — never a
  silent no-op.
- **`CapStream` and `Mountable`** are designed but **not** implemented in the
  MVP (see [§1 Scope](#scope)) — they are future capabilities for streaming and
  additional mount backends.

### 6.3 Backend factory registry (constructor-register) + lazy availability

Backends are registered through a **constructor-register** (a **Factory**):

```go
type Factory struct {
    Name         string
    Priority     int
    Capabilities CapabilitySet
    Available    func() bool          // lazily evaluated, never at startup
    New          func(cfg) (Backend, error)
}
```

- **Docker registers itself** via package `init()`.
- The **darwin** backend (macOS Seatbelt via `/usr/bin/sandbox-exec`, §7.12)
  registers the same way with `Priority` 10 (docker preferred) and an
  `Available()` gated on `runtime.GOOS == "darwin"`, the presence of
  `/usr/bin/sandbox-exec`, and `euid != 0`. It is now **selectable**: the
  capability set required to create/drive a sandbox is `Lifecycle + Executable`
  (not `Introspectable`), which the darwin backend satisfies even though it
  lacks `Introspectable` (no cgroups on macOS; `Info`/`Stats` →
  `ErrCapabilityNotSupported`). Previously `needAll` also required
  `Introspectable`, which made darwin fail `covers()` in `registry.Select` and
  thus unselectable everywhere.
- `Available()` is evaluated **lazily inside `sandbox_create`** — never at
  startup (**A02/N35**: no outbound calls at startup). Backend selection happens
  on the first tool call that needs a backend.
- **Multi-backend `Service` (one runtime per driver).** The MCP `Service`
  (`infrastructure/mcp`) does **not** cache a single backend. It keeps **one
  runtime (backend + session manager) per concrete driver**, created lazily on
  first use (A02/N35), and routes `Exec`/`Info`/`Stats`/`Destroy`/`Get` to the
  owning backend **by the session id's slug** (`resolveSessionID` → the
  manager scoped to that driver; there is **no `sessionDriver` map** and no
  global cross-driver prefix scan), while `List`/`DestroyAll` aggregate across
  all runtimes. A single `mcp-sandbox`
  process therefore holds **docker and seatbelt sandboxes simultaneously** —
  there is no longer a one-backend-per-process coupling. `Create` resolves an
  auto/empty driver to a concrete one before routing to the owning runtime.

### 6.4 Declarative tool framework

Every tool is a **`ToolDef`** declaration:

```go
type ToolDef struct {
    Name              string
    Title             string
    Description       string
    Annotations       Annotations       // readOnly/destructive/idempotent/openWorld hints
    Instructions      string            // per-tool (M04)
    InputSchema       *jsonschema.Schema
    OutputSchema      *jsonschema.Schema
    RedactionContract []RedactionField  // secret:true fields (S02)
    Handler           ToolHandler       // application-layer handler
}
```

Tools are declared in **`application/tools/def/`**, registered in a
**`Registry`**, and each is wrapped by a **shared handler-wrapper** in
`application/tools/handler/` that guarantees a uniform pipeline on every call:

```
validate args vs InputSchema (S08)          → reject InvalidParams on failure
emit access-log line at info (L08)          → tool / redacted args / source / duration / outcome
dispatch to ToolDef.Handler
redact output via RedactionContract (S02/S09)
return structured output conforming to OutputSchema
```

This wrapper is the single place where validation, logging, redaction and
structured output are guaranteed — no tool can bypass it.

### 6.5 Typed `SandboxSpec` (not free-form)

A sandbox is created from a **typed `SandboxSpec`**, never a free-form blob:

```go
type SandboxSpec struct {
    Image          string
    WorkingDir     string
    Env            map[string]string
    Mounts         []Mount          // immutable after create
    Resources      Resources        // NanoCPUs, Memory, MemorySwap, PidsLimit, Ulimits
    Network        Network          // default: none
    UIDGID         UIDGID           // non-root user inside
    Sudo           SudoPolicy       // scoped root ("sudo", exec -u 0) for sudo-classed exec ops (§7.10)
    Secrets        []SecretRef      // mounted into tmpfs as 0600 files
    ReadOnlyRoot   bool             // true
    Isolation      Isolation        // IMMUTABLE — server-enforced, caller cannot weaken (§7.1)
}
```

**SandboxSpec provenance (Phase 7).** In the profile model, `SandboxSpec` is
**not** assembled from per-request client parameters. It is produced by
`domain/sandbox/profile.BuildSpec(isolation, Params{Cwd, Secrets,
ClientSessionID})` — the single seam where operator intent is merged with the
server's immutable `Isolation` policy (§6.5 → §7.11):

- **Profile-owned fields override the server policy baseline** (`pol`):
  `NanoCPUs`, `Memory`/`MemorySwap`, `PidsLimit`, `Ulimits`, `NetworkMode` come
  from the profile's `cgroups`/`network` (§7.11). The profile also supplies
  `Image`, `Env`, `Mounts` (appended after the auto cwd mount), `UIDGID`
  (non-root enforced), `Sudo`, and the `cwd` target/mode.
- **Hardening fields ALWAYS come from `pol` and are NEVER overridden by a
  profile**: `ReadOnlyRootfs`, `CapDrop`, `NoNewPrivileges`, `Seccomp`, the
  enforced non-root `User`, `TmpfsWorkdir`, `AutoRemove`. A hostile profile
  cannot weaken the sandbox (§7.11).
- The **session `cwd` mount is always present** (mounted 1:1 at the same
  absolute path, read-only by default) even when the profile declares no
  `cwd` block; the profile may remap the container `target` and set
  `mode: ro|rw`.

### 6.6 Session lifecycle

A **session** (`domain/session`) owns one sandbox and its state:

```
create → use (exec/read/write/copy/install) → destroy
```

- **Mounts are immutable** — changing a mount requires `destroy` + `create`
  (recreate). This is enforced by the session manager (X01 state model).
- **Session id = namespaced backend id (`slug:internalID`, not a server-generated
  opaque id).** The session manager calls the backend **first** and uses the
  returned id as the **internal** part of the session id, namespacing it with the
  backend's slug (`role.Backend.Slug()`): for docker that is the **container id**
  (a sandbox is directly traceable to its container via `docker ps`), for darwin
  it is the backend-generated **`darwin-N`** id. A backend that returns an
  **empty** id falls back to a generated opaque 64-hex (`crypto/rand`) id. The
  post-create duplicate check enforces uniqueness on the full namespaced id
  (X02; see §7.8).
- **Cleanup is explicit only — idle reaper & orphan-GC are future work.**
  Sandboxes are removed **only** by explicit `sandbox_destroy` /
  `sandbox_destroy_all`; there is **no automatic GC** today. An in-process idle
  reaper (`Manager.Reap` / `Session.Touch`) and an orphan-reclamation sweep are
  **designed but not wired** — no production code path calls them (see `TODO.md`).
  `SANDBOX_IDLE_TIMEOUT` is parsed but has no effect until the reaper is wired.
- **Shutdown-teardown (intended, not automatically wired):** the design intends
  graceful shutdown to call `DestroyAll()` of the process's own sessions.
  **Today `DestroyAll` runs only via the `sandbox_destroy_all` tool** — there is
  no automatic teardown on server exit, so a process that dies without explicit
  destroy leaves orphaned containers (to be reclaimed by the future orphan-GC).
- **darwin/`sandbox-exec` driver (§7.12):** `sandbox-exec` applies a
  sandbox scheme and **execs** the command into the **same process** — there is
  **no supervisor wrapper and no `AutoRemove` equivalent**. The session id is
  the backend-generated **`darwin-N`** id (never the pid); the backend keeps an
  in-memory record mapping that id to its Create child, whose **process-group
  pid** is the handle `Destroy` kills (`kill(-pgid, SIGKILL)`),
  **idempotently** (`ESRCH` → no-op). A server exit
  without explicit destroy leaves the sandboxed process tree as an **orphan**.
  `req.Timeout` (§6.10) becomes a process-group `SIGKILL` cancellation.
- **State model (X01):** **stateful, per-session, in-memory.** State lives in
  the session manager (a process-local map); it is _not_ persisted to disk or a
  DB. Reasoning: sandboxes are ephemeral by design and tied to the lifetime of
  the model-host process that spawned the server. No persistence layer, no
  encryption-at-rest, no schema migration (X04 not applicable — declared
  stateful-but-in-memory).
  - **Per-process / multi-copy model:** each `mcp-sandbox` process tracks **only
    its own** sessions in its own in-memory map. Concurrent copies of the server
    do **not** coordinate. In-memory state dies with the process; the idle-reaper
    and orphan-GC (both future work) are the intended mechanisms for reclaiming
    sessions after a process exits.
- **Idempotency (X02):** mutating tools have a data-level idempotency
  mechanism — `sandbox_create` does **search-before-create** (a duplicate
  creation — a session with the same id — is surfaced as an **error** rather
  than creating a duplicate),
  and `sandbox_destroy`/`destroy_all` are naturally idempotent (destroying a
  non-existent session is a no-op).
- **Concurrency (X05):** each session owns a **per-session mutex**; reads and
  writes are serialized per session so no partial/corrupt state results.
  Concurrency is exercised with `go test -race` (C04GO).
- **No silent retry (X03):** failures on Docker API calls are returned to the
  model and **logged** (no secrets); there is no silent retry/backoff (L02GO).
  For `sandbox_exec`, `req.Timeout` is applied as a `context.WithTimeout`
  cancellation that reaches the Docker calls and the progress sink (§6.10).

### 6.7 Error handling

Error taxonomy and mapping to JSON-RPC codes:

| Error                                 | Meaning                                | JSON-RPC                        |
| ------------------------------------- | -------------------------------------- | ------------------------------- |
| `InvalidParams`                       | `inputSchema` validation failure (S08) | `InvalidParams`                 |
| `ErrCapabilityNotSupported`           | backend lacks a role capability        | tool-level structured error     |
| not-found (unknown sandbox id)        | session does not exist                 | structured "not found"          |
| transient (Docker daemon unavailable) | upstream/backend failure               | returned to model, logged (X03) |

Every error is logged through the unified logging pipeline (**L08/L09**), with
secrets redacted.

### 6.8 Configuration (env-driven)

Configuration is read from environment variables (via envconfig), validated at
startup (local wiring only):

| Env                    | Default                       | Meaning                                                                                                                                                                                                                                                                                                                                                      |
| ---------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `ALLOW_DIRS`           | `[]` (none — deny by default) | Permitted host directories that may be mounted into sandboxes — gates the client-supplied `cwd` **and** every mount `source`, including those declared in profiles (**S04**). Empty ⇒ no host dirs mountable. **Still required in Phase 7** (§7.11).                                                                                                         |
| `ALLOW_SYMLINKS`       | `false`                       | Symlink policy for host path resolution (**S04**).                                                                                                                                                                                                                                                                                                           |
| `DOCKER_HOST`          | `unix:///var/run/docker.sock` | Docker daemon socket (unix).                                                                                                                                                                                                                                                                                                                                 |
| `LOG_LEVEL`            | _(unset ⇒ logging disabled)_  | Enable + level; stdio logging is on **only when set** (**L02**).                                                                                                                                                                                                                                                                                             |
| `LOG_FILENAME`         | —                             | stdio log file path, `chmod 600` (**L01/L03**).                                                                                                                                                                                                                                                                                                              |
| `LOG_FORMAT`           | `text`                        | `text` or `json` (**L04**).                                                                                                                                                                                                                                                                                                                                  |
| `SANDBOX_IDLE_TIMEOUT` | `15m`                         | Idle reaper window (**not yet wired** — the reaper is designed but not running; see §6.6).                                                                                                                                                                                                                                                                   |
| `SANDBOX_PROFILES_DIR` | `~/.config/sandbox/profiles`  | Directory of **operator-authored, READ-ONLY** YAML sandbox profiles — one `<name>.yml` per profile (**Phase 7**, §7.11). Read **on demand** on each `Get`/`List` (§7.11) — edits take effect without a restart. The source of `image`, `env`, `mounts`, `cwd`, `network`, `cgroups`, `user`, `sudo`. There is **no** profile create/update/delete mechanism. |

> **Removed configuration (Phase 7).** The following env vars were **removed**;
> their responsibility moved into the operator profile (§7.11) or the profile
> image reference:
>
> - `SANDBOX_CWD_MODE` — the `cwd` mount mode (`ro`/`rw`) now comes from the
>   profile's `cwd.mode` (default `ro`).
> - `SANDBOX_CGROUP_CPU_LIMIT_CORES` / `SANDBOX_CGROUP_MEMORY_LIMIT_GB` —
>   resource limits (`cgroups.nano_cpus`, `cgroups.memory`, `cgroups.memory_swap`,
>   `cgroups.pids_limit`, `cgroups.ulimits`) now come from the profile.
> - `TRUSTED_REGISTRIES` — the registry allowlist is **gone**; the image comes
>   from the operator-authored profile and **digest-pinning is the recommended
>   operator convention** (§7.9/§7.11).

### 6.10 Command timeout (`req.Timeout`) — applied as a cancellation

`sandbox_exec` accepts an optional `timeout` parameter (integer seconds, see
`domain/sandbox/exec.Request.Timeout` and the tool input schema in §4.2). When
`timeout > 0`, the Docker backend's `Exec` (the `Executable` role, §6.2) wraps
the execution context with `context.WithTimeout(ctx,
time.Duration(timeout)*time.Second)` and hands that **cancellable** context to
**every** Docker API call in the exec path (create / attach / inspect / the
streaming read loop). The command timeout therefore becomes a **cancellation**
that propagates down to the Docker daemon and to the progress sink:

- **`timeout == 0` (default):** no deadline is applied — the original context is
  used as-is and the command runs until it completes or the caller's own context
  is cancelled.
- **`timeout > 0`:** after `timeout` seconds the context deadline fires, the
  running Docker exec is cancelled, and — when a progress sink is present — a
  terminal **`cancelled`** event is emitted (see §5.1) before the cancellation
  error is returned to the model. The error is **returned and logged (no
  secrets)** with **no silent retry/backoff** (X03).

The timeout is applied at the **backend** layer, so it works uniformly for the
Docker API calls and the streaming progress channel, independent of whether the
client supplied a progress token. Criteria: **X03**.

### 6.11 Driver registry & external drivers — design direction (NOT yet implemented)

> **Status: PROPOSED / DIRECTIONAL — not yet implemented.** This subsection
> records the agreed design direction for generalizing the backend model into a
> **driver registry + opaque `options:`** so new drivers can be added **without a
> commit to mcp-sandbox** and without linking proprietary code into the binary.
> **None of the new identifiers below exist in the code yet**; they are the
> target shape the implementation should move toward. It cross-references the
> existing role interfaces (§6.2), the factory registry (§6.3) and the profile
> schema (§7.11) rather than restating them. This is the design that motivates
> the §7.11 schema direction.
>
> **Delivery roadmap (settled, user decision):** **Phase 1 = in-process Go
> plugins only** (same language, no IPC, no loader); **Phase 2 (later) =
> out-of-process IPC** (a STDIO child + protobuf-like encoding) that opens other
> languages. The **driver contract is defined so it is realizable both
> in-process (Go) and, later, over IPC without changing the contract**; the
> wire/framing details are deferred to Phase 2. The **loader/manifest/entrypoint
> and IPC content below is the Phase-2 realization**; Phase 1 is in-process Go
> plugins that implement the same `DriverBuilder`/role contract directly.

#### Driver = a single-sandbox primitive

A **driver** is the primitive that implements the interface to work with **ONE
sandbox**. Its responsibilities map 1:1 onto the existing ISP role interfaces
(§6.2, `domain/sandbox/role`):

| Existing role interface | Driver method | Driver responsibility                                           |
| ----------------------- | ------------- | ---------------------------------------------------------------- |
| `Backend`               | `Name`/`Priority`/`Capabilities`/`Driver` | identity + capability advertisement          |
| `Lifecycle`             | `Create` / `Destroy` | create a sandbox from a spec; destroy it                    |
| `Executable`            | `Exec`        | run a command inside an existing sandbox                        |
| `Introspectable`        | `Info` / `Stats` | sandbox detail / live resource usage                         |
| (via `Factory`)         | `Available()` | is this driver usable right now (lazy, A02/N35)                 |

- **`List` is NOT a single-sandbox primitive (QA P1).** It is an **optional
  reconciliation seam** — for the future orphan-GC (§6.6) — and is **not part of
  the core "one sandbox" role set**. The server's session manager already owns
  `List`/`DestroyAll` aggregation (§6.3); a driver does not need `List` to be
  usable.
- **`Delete` is renamed `Destroy` (QA P1).** The `Lifecycle` role already uses
  `Destroy` (§6.2, `domain/sandbox/role.Lifecycle`); the driver contract uses
  the same verb — there is **no `Delete`**.
- **`Driver()` vs `Slug()` (QA P2).** `role.Backend` already exposes `Driver()`
  (`spec.Driver`, §6.2) — the runtime capability selector. A future **`Slug()`**
  — a **stable, allowlist-validated** identifier used to build a namespaced
  session id (`slug:internalID`, §7.8) — is a **distinct concept and must not be
  conflated** with `Driver()`. `Slug()` is a stable, collision-free namespace
  token validated exactly like `profile.ValidName` (fail-closed on anything
  outside the allowlist). See §7.8 for the namespaced-id step.

The driver is **not** responsible for orchestrating multiple sandboxes. All
**orchestration** is the **MCP server's** job — the session manager, per-driver
routing, session lifecycle, HITL, streaming/redaction and `sandbox_list`
aggregation (§6.3, §6.6). The server calls the driver **per sandbox**, exactly
as the `Service` (`infrastructure/mcp/service.go`) routes today.

#### DriverBuilder / DriverOptions abstraction

The proposed construction abstraction separates the **fixed contract** (how a
driver is built) from the **driver-owned config** (what it means). A
**`DriverBuilder`** constructs a concrete backend for a named driver from an
opaque **`DriverOptions`** blob:

```go
// DriverBuilder constructs a backend for a named driver from an opaque,
// driver-owned options blob. It is the registry entry point (analogous to
// backend.Factory, §6.3). It returns a role.Backend — it does NOT build a
// SandboxSpec; that is profile.BuildSpec's job (domain, §6.5/Developer G).
type DriverBuilder func(opts DriverOptions, cfg backend.Config) (role.Backend, error)

// DriverOptions is the opaque per-driver config. It is interpreted ONLY by the
// driver matched by `driver`; the core never inspects its contents.
type DriverOptions map[string]any
```

- The **`driver` name is the registry key** — the map lookup / `switch` key for
  selecting the builder, and the key that owns the profile `options:` block.
- The core registers builders **by name**; an unknown name **fails closed**
  (`ErrNoAvailableBackend`, §6.3) — there is **no silent fallback** to another
  driver.
- **SandboxSpec provenance is unchanged (Developer G).** The typed `SandboxSpec`
  is built by `domain/sandbox/profile.BuildSpec` (§6.5) — **NOT by the driver**.
  `DriverBuilder` returns a `role.Backend` (the runtime adapter); the spec that
  backend consumes is always produced by the domain and carries the immutable
  `Isolation` block.

#### Driver registry keyed by `driver`

The existing constructor-register (`infrastructure/backend/registry.go`) already
registers backends by name with a `Driver` and lazy `Available()`. The direction
extends it so the registry key is the **`driver` name** from the profile, and a
profile's `options:` blob is handed to that driver's builder verbatim (validated
by the driver, not the core).

#### Reserved built-in names & fail-closed loading (Security B, HIGH)

- **`docker` and `seatbelt` are reserved.** An external driver **MUST NOT** be
  able to shadow or override them. Registration **fails closed on a duplicate
  name** — an external driver registering `driver: docker` or `driver: seatbelt`
  is **rejected**, never silently overriding the built-in.
- **Fail-closed loading (F3):** a driver that registers but produces a
  `SandboxSpec` that **fails post-build `Validate()`** (§6.5) is **REJECTED**,
  not coerced or patched by the core. The core never "fixes up" a driver's spec
  to make it valid — an invalid spec is an error.

#### Hardening-on-result = DATA-PLANE only (Security A/F1, critical)

The core owns a **uniform security floor**. For **first-party / auditable**
drivers this is **defense-in-depth + DRY**: after the driver path produces a
`SandboxSpec`, the core **re-asserts** the immutable `Isolation` hardening
fields (§7.1) so every driver holds the same invariant without each
re-implementing it.

**For EXTERNAL, non-auditable drivers the guarantee is narrower — it is
DATA-PLANE only.** The core can re-assert the **declared** `Isolation` fields of
a returned spec, but the **MECHANISM-plane** — whether the backend actually
enforces read-only-rootfs / cap-drop / no-new-privileges / cgroup quotas — is in
a **"not audited" trust zone**. The driver's manifest is **self-attestation**:
the core **cannot distinguish a truthful manifest from a lying one**. A hostile
external driver can always *claim* it enforces hardening it does not. Therefore:

> **One-liner (data-plane):** "Trust the driver to apply isolation, but
> guarantee a uniform security floor at the *input* that does not depend on
> whose driver it is or how careful it is." This holds **only** for the
> **data-plane** (re-asserting declared fields); it does **NOT** extend to the
> **mechanism-plane**, which for external drivers is self-attested and
> unaudited.

**Capability vs. weakness — closed enum, pessimistic default (Security F2).** A
driver's manifest must fill an explicit **CLOSED enum of isolation guarantees**
— `readonly-rootfs`, `cap-drop`, `no-new-privileges`, `non-root`,
`cgroup-quotas`, `network-isolation`, `tmpfs`, `autoremove` — one entry per
guarantee, declared honestly:

- **Pessimistic default:** a driver is considered **weak until it declares
  otherwise**; a **missing field = "not guaranteed"**.
- **A declared weakness must NOT silently pass.** It produces an honest
  **REDUCED-POSTURE marker** that (a) propagates to tool output/warnings (e.g.
  the `sandbox_create`/`sandbox_info` result), (b) is **never silently ignored**,
  and (c) is **never presented as full isolation** — directly analogous to the
  darwin backend's "MUST NOT be presented as equivalent" rule (§7.12/§7.13).
- **Fail-closed-reject** applies to **data-plane fields the core CAN enforce**
  but the driver returned wrong (e.g. a spec whose
  `Isolation.ReadOnlyRootfs=false` fails `Validate()` → rejected, F3). For
  mechanism-plane fields the core cannot verify, the honest REDUCED-POSTURE
  marker is the correct outcome — not silent acceptance, and not a false claim
  of enforcement.

Rationale for the data-plane / mechanism-plane split:

1. a single choke-point means **first-party** drivers hold the same invariant
   without each re-implementing it (DRY / defense-in-depth);
2. the least-trusted party is the **input** (operator profile `options`, which
   may be influenced by the model-host the sandbox contains) — input must never
   weaken driver-applied isolation, the same as today's `BuildSpec` /
   `applyDocker` re-applying `pol` last (§6.5);
3. for **non-auditable external/proprietary drivers** the data-plane floor plus
   the honest REDUCED-POSTURE marker is what lets the server admit them
   **without falsely claiming** a bar they may not actually meet.

#### EnforceHardening — the single core-owned choke-point (Developer G)

The re-assertion is a **pure domain function** in `domain/sandbox/spec`:

```go
// EnforceHardening re-asserts the immutable Isolation hardening on a spec (built
// by profile.BuildSpec, §6.5) and gates every mount source by allowDirs (S04).
// It is the single core-owned choke-point for the data-plane security floor.
// Pure — no I/O, no driver — so it is fully unit-testable without a driver.
func EnforceHardening(pol Isolation, s *SandboxSpec, allowDirs []string) error
```

- It is called from **`Service.Create` before the backend `Create`**
  (`infrastructure/mcp/service.go`), on the spec produced by
  `profile.BuildSpec` (§6.5).
- It re-asserts read-only rootfs, cap-drop ALL, no-new-privileges, non-root,
  bounded cgroups, tmpfs, `AutoRemove`; validates network mode / control chars;
  and **gates every mount `source` by `ALLOW_DIRS` (S04)**.
- **The `ALLOW_DIRS` mount gate is LIFTED to the Service level** (the
  choke-point), **not duplicated** in the backends. Backends consume an
  already-hardened, already-gated spec. (Today the gate lives inside the
  docker/darwin adapters; the direction consolidates it here as the single
  core-owned choke-point.)

#### Loader & tamper control (Security E/F6, MED) — Phase 2 IPC realization

- **Loader is traversal-safe (F6):** the driver `entrypoint` is restricted to
  the drivers directory (e.g. `SANDBOX_DRIVERS_DIR`) or an operator-approved
  area, opened via **`os.OpenRoot`**; the driver `name` is validated like
  `profile.ValidName` (fail-closed on anything outside the allowlist / any path
  traversal). An unloadable/hostile driver **fails closed** — no silent skip, no
  fallback.
- **Tamper control:** the manifest carries a **digest and/or signature** of the
  driver binary, verified before load — analogous to image **digest-pinning
  (§7.9)**. A driver whose binary does not match its declared digest/signature
  is rejected.
- **Lazy (A02/N35):** `Available()` / driver **spawn** stays lazy — a driver is
  **never spawned at startup**. Only the first tool call that needs the driver
  triggers its load/spawn.

#### IPC/stream boundary (Security C, SRE) — Phase 2 IPC realization

- **Frame size limits** (DoS/memory protection): a per-frame maximum is
  enforced; oversized frames are rejected.
- **Framing integrity:** negative / overflowing / huge lengths, premature EOF and
  partial frames are **rejected fail-closed** — never partially interpreted.
- **Every byte originating from a driver** passes **S09 `sanitizeANSI` + S02
  redaction** before it reaches the client, log, or stream. The server
  **re-redacts streamed chunks** — it does **not trust a non-auditable driver to
  redact its own output** (§5.1 contract preserved).
- **`request_id` in every frame** (L09): each driver frame carries the server's
  per-request id for correlation.
- **Cancel/abort frame:** so `req.Timeout` becomes a **wire deadline** — the
  server sends a cancel frame the driver must honor (analogous to §6.10 / §5.1
  `cancelled`).
- **Secrets never echo:** secrets sent to a driver (tmpfs `0600` files, §7.3)
  are **never echoed back over IPC and never logged** (S02/N02/L05).

#### Testability (QA P1/H)

The design deliberately leaves **testable seams**: the registry (register-by-name
+ reserved-name + duplicate rejection), the loader (traversal-safe resolution +
digest verification), the IPC codec (framing/size/EOF fail-closed), supervision
(lazy spawn, cancel frame) and the **enforcement floor** (`EnforceHardening`).
The security floor is a **pure function testable without any concrete driver** —
feed it a fake/malicious spec and assert the hardened result — reusing the
existing **fake-driver pattern** (`infrastructure/backend/docker/fake_client_test.go`,
the darwin mock runner/tailer, and `application/tools/sandbox/fake_test.go`).
See §7.14 for what remains open.

---

## 7. Security

Security is the **primary design goal** — the sandbox replaces the host shell,
so it must be at least as restrictive as the host, and defensively so. The
Docker daemon socket grants near-root host access (see [§7.6](#76-server-process--docker-socket-access))
— this is documented as a **known limitation and controlled risk**.

### 7.1 The immutable `Isolation` block

`SandboxSpec.Isolation` is **immutable** — the caller **cannot weaken** any of
its values. It is populated by the server policy and applied on every container
create, regardless of the input spec:

| Isolation policy        | Value / enforcement                                                                                                                                                                                                                                                                                                                                                                                                 |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ReadOnlyRootfs`        | `true` — rootfs is read-only.                                                                                                                                                                                                                                                                                                                                                                                       |
| `CapDrop`               | `ALL`, then re-add only the minimal required capabilities.                                                                                                                                                                                                                                                                                                                                                          |
| `NoNewPrivileges`       | `true`.                                                                                                                                                                                                                                                                                                                                                                                                             |
| `Seccomp`               | **Default profile for the MVP**; a **custom seccomp profile is mandatory roadmap** (see §7.7).                                                                                                                                                                                                                                                                                                                      |
| User                    | **Non-root** inside the container (`UIDGID` / user namespaces).                                                                                                                                                                                                                                                                                                                                                     |
| `PidsLimit`             | Set (bounded process count).                                                                                                                                                                                                                                                                                                                                                                                        |
| `Memory` / `MemorySwap` | Set and bounded.                                                                                                                                                                                                                                                                                                                                                                                                    |
| `NanoCPUs`              | Bounded.                                                                                                                                                                                                                                                                                                                                                                                                            |
| `Ulimits`               | Set.                                                                                                                                                                                                                                                                                                                                                                                                                |
| `NetworkMode`           | **From the operator profile** (§7.11): `bridge` (safe default) or `none`; **`host` is valid only when the operator authors it into a profile** — it is never allowed implicitly. The immutable block still bounds which modes are legal (`bridge`/`none`/`host`).                                                                                                                                                   |
| `tmpfs` mounts          | **`/run`** tmpfs `rw,noexec,nosuid,nodev` (owned by the sandbox user; holds the `/run/sandbox` secret dir) and **`/tmp`** tmpfs `rw,nosuid,nodev` (writable, no `noexec` requested). **Platform note:** some daemons (Docker Desktop on macOS) force `noexec` on tmpfs, so `/tmp` may not be executable; runtime compilation (Go builds) then needs the writable bind-mounted `cwd` (per the profile's `cwd.mode`). |
| `AutoRemove`            | `true` — container auto-removed on stop.                                                                                                                                                                                                                                                                                                                                                                            |

Because the block is immutable and applied by the server, a caller cannot pass
a spec that disables these protections.

**darwin/`sandbox-exec` driver — weaker resource isolation (documented, §7.12).**
macOS has **no cgroup** for general use, so the immutable block maps only
partially onto the darwin backend: `PidsLimit`→`RLIMIT_NPROC`, `Ulimits`→
`setrlimit` (`RLIMIT_CPU/NOFILE/FSIZE/STACK/CORE`), read-only rootfs→
`file-read-only` scheme rights; **`Memory`/`MemorySwap`/`NanoCPUs` are not
hard-enforceable** (`RLIMIT_AS` is an approximation only) and are declared
**unsupported** on darwin, never silently ignored. Resource limits on darwin
are therefore **weaker** than Docker and **must not be relied on as hard
quotas**.

**Resource limits come from the operator profile (Phase 7).** The
`Memory`/`MemorySwap`/`NanoCPUs`/`PidsLimit`/`Ulimits` values (rows above) are
no longer seeded from environment variables — they are owned by the operator
profile's `cgroups` block (§7.11). A profile that omits a limit falls back to
the built-in server defaults (currently 1 CPU, 4 GiB, swap off, `PidsLimit`
512). Where a profile sets a value, the profile **overrides** the policy
baseline; a caller has **no** per-request override — `sandbox_create` no longer
accepts `nano_cpus`/`memory`/`pids_limit`. The immutable hardening of the block
itself can **never** be weakened (by profile or caller).

### 7.2 Filesystem scope & mounts (S04)

- **`ALLOW_DIRS []string`** — the **only** host directories permitted to be
  mounted into a sandbox. In Phase 7 it gates **both** the client-supplied
  session `cwd` **and every mount `source` declared in a profile** (§7.11) —
  a profile cannot reach a host path outside `ALLOW_DIRS`. Anything outside is
  forbidden (**N03**). Empty by default (deny). **`ALLOW_DIRS` is retained and
  still required.**
- **`ALLOW_SYMLINKS bool`** — default `false`.
  - `false` (safe default): every host path is canonicalized with
    `filepath.EvalSymlinks`, then **open-then-verify** (open the fd, `fstat`,
    compare the resolved real path against the allowed roots); reject if it
    escapes `ALLOW_DIRS`.
  - `true`: following symlinks is accepted (operator explicitly opts in).
  - **Hardlinks** cannot be prevented by a path check (a hardlink is another
    name for the same inode); this is mitigated at the OS level (Linux
    `fs.protected_hardlinks`, separate bind-mount for the allowed dir, immutable
    dirs) and is documented as an OS-level concern, not something the path check
    enforces.
- **Mount policy:** only **explicit `ALLOW_DIRS`** may be mounted — whether the
  source comes from the client `cwd` or a profile `mounts.source`; **read-only
  by default**; **minimize the number of paths**; **`/` and secret directories
  are forbidden**.
- **Minimize bind-mounts:** the session `cwd` and profile-declared mounts are
  the only bind-mounts; file transfer is **host-side** (the model-host writes
  into the mounted cwd), and runtime writes happen **only into tmpfs** (not into
  bind-mounted host paths). This reduces the host surface a compromised sandbox
  could reach.
- **Session working-directory mount (1:1):** `sandbox_create` accepts the
  model-host's **current working directory** and mounts it at the **same
  absolute path** inside the container — host `/Users/myuser/Downloads/blah` →
  container `/Users/myuser/Downloads/blah` — with **no path rewriting** by
  default. The mounted cwd must be **within `ALLOW_DIRS`** (or explicitly
  authorized). It is **read-only by default** (the model-host writes into the
  mounted cwd on the host; the container reads/executes). The profile's
  `cwd` block can remap the container **target** and set **`mode: rw`**
  (§7.11) for cases where the container must write artifacts into the cwd
  during command execution. This is the one sanctioned bind-mount for the
  model-host use case so the model operates on the session's files with
  **identical paths**; cross-referenced with the `UIDGID` mapping (§7.10) so the
  container's unprivileged user can access host files owned by the operator.

### 7.3 Secrets & redaction (S02 / N02)

- A single **redactor** is applied **at the boundary of all output**: `exec`
  stdout/stderr, Docker API error messages, audit output, `info`/`inspect`.
- **Secrets are modeled as structs with `secret:true` annotations**; the
  redaction helper hides annotated fields from tool output and logs — **no
  heuristics/regex "guessing"**; the struct is authoritative.
- **Secrets in the sandbox** are delivered as **tmpfs files with mode `0600`** under
  `/run/sandbox` (a subdir of the sandbox-user-owned `/run` tmpfs, §7.1) —
  **never** in argv, never baked into the image, never in environment exposed to
  `exec`.
- **No secret leakage** in any output or log (**N02/L05**).
- **Streaming output is redacted too** (§5.1): the same redaction layer applies
  to the `notifications/progress` chunks emitted while a command runs, so no
  secret leaks through the live-streaming channel.

> **⚠ `command`, `env` and `summary` are logged as-is (S02 disclaimer).** The
> access-log line (L08) records the `sandbox_exec` `command`, the per-exec
> `env` (a `map[string]string` of literal `KEY=VALUE` entries) and `summary`
> **as passed** — the access-log path is **not** run through secret-value
> redaction. This is a **deliberate design choice**: the secret model is
> struct-annotation-based, and `command`, `env` and `summary` are free-form for
> which **no** secret values are known in advance, so they **cannot** be
> reliably masked if one ever appeared there. **Contract / expectation:**
> `command`, `env` and `summary` are **not** a channel for secrets — secrets are
> delivered as tmpfs `0600` files (§7.3) and must never be placed in a command
> line or an env entry. The same applies to the `start` event of the
> `notifications/progress` stream (§5.1), which echoes the redacted-later
> `command`/`summary` to the initiating client. Operators who may pass sensitive
> values in a command line, an env entry or a summary should note that:
>
> - the access log is written to `LOG_FILENAME` (`chmod 600`, operator-owned); and
> - logging is **opt-in** — it is enabled **only when `LOG_LEVEL` is set**
>   (L02); with `LOG_LEVEL` unset no log file is written and no `command`/`env`/
>   `summary` is persisted at all.

### 7.4 Strict input & output validation (S08/S09)

- Every tool validates arguments against `inputSchema`
  (`additionalProperties: false`, strict types, limits) **before** execution;
  rejects with `InvalidParams` (**S08/N22**).
- Output conforms to `outputSchema`; **ANSI/control escape sequences are
  filtered** in text output (**S09/N23**) — `sandbox_exec` output passes through
  ANSI sanitization. The same sanitization is applied to **streamed** output
  before it is emitted via `notifications/progress` (§5.1).

### 7.5 No shell / SQL / URL concatenation (S11 / N21)

- **No shell-string concatenation/interpolation.** `sandbox_exec` takes a
  scalar `command` string and executes it via a **fixed** wrapper
  `[ <shell>, "-c", command ]` through the Docker SDK exec API — the whole
  `command` is passed as **one argv element** to the fixed wrapper, so there is
  **no concatenation hazard** between separate parts and **no user string is
  interpolated into a shell string**. The `<shell>` is the profile-configured
  shell (default `bash`, §7.11), and the wrapper runs inside the **already-
  isolated** container, so host isolation is unchanged. The only other `sh -c`
  is the server-authored **fixed** secret-writer script (never user-influenced).
- No SQL or outbound URL construction exists in the MVP.

### 7.6 Server process & Docker socket access

- The **server process runs as an unprivileged user** (never root).
- **Access to the Docker socket is a deliberate decision** and a **documented
  limitation**: the Docker socket (`/var/run/docker.sock`) is effectively **root
  on the host**. The MVP mitigations:
  - prefer a **rootless Docker** daemon where available;
  - otherwise run the server in an **isolated / minimal environment** with the
    socket access confined to the server process only;
  - never expose the socket to the model-host or any tool output.
- This limitation is recorded explicitly so the operator can choose the socket
  access model appropriate to their host.

### 7.7 Roadmap / security hardening

| Item                                         | Status                                                                                                                                                                                                |
| -------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Custom seccomp profile                       | **Mandatory roadmap** (replaces the default MVP profile).                                                                                                                                             |
| AppArmor profiles                            | Roadmap.                                                                                                                                                                                              |
| **gVisor / runsc**                           | **Target state** for kernel-escape containment (**T1 kernel-escape**).                                                                                                                                |
| Rootless Docker                              | Roadmap (reduces the §7.6 socket risk).                                                                                                                                                               |
| **Darwin backend (`sandbox-exec`/Seatbelt)** | **Experimental (implemented — portable core wired + darwin `log stream` tailer; darwin e2e run via `make e2e` on macOS)** — **policy-based containment, NOT a kernel-escape equivalent** (see §7.12). |
| Backend abstraction                          | Ready — a future `gvisor`/`apple` backend implements the same `Backend` roles.                                                                                                                        |

### 7.8 Session identifiers & ownership

- **Namespaced session id (`slug:internalID`).** Every session id is a
  `slug:internalID` pair (e.g. `docker:6b4464b592f9`, `seatbelt:darwin-3`). The
  **slug** is the backend's namespacing prefix (`role.Backend.Slug()`; docker →
  `docker`, seatbelt → `seatbelt`) and selects the owning backend for routing;
  the **internal** part is the backend's own sandbox handle. The parser
  (`domain/session`) is **STRICT** — a single `:` (internal must not contain
  `:`), both parts non-empty, and the slug must match `^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`.
  An empty, malformed (non-namespaced) or unknown-slug id resolves to
  `ErrSessionNotFound` on every op; destroying by an unknown slug is an **error,
  not a no-op** (user decision 1), while a valid slug whose internal id matches
  no live session stays an idempotent per-driver no-op (X02).
- **Session id = backend id, namespaced by the manager.** The session manager
  calls the backend **first** and uses the backend's returned id as the
  **internal** part: for the **docker** backend that is the **container id**, so
  a sandbox is directly traceable to its container via `docker ps`; for the
  **darwin** backend it is the backend-generated **`darwin-N`** id (never the
  pid). A backend that returns an **empty** id falls back to a generated opaque
  64-hex `crypto/rand` id. The manager composes the full id as
  `slug:<internalID>` and keys the session by it; a manager is scoped to ONE
  backend/slug, so resolution is per-driver (`Manager.Resolve`).
- **Backends strip the slug.** Because the session id is `slug:internalID`, the
  docker and darwin backends **strip the slug** (`ParseNamespacedID` → use
  `.Internal`) before calling their underlying API — the Docker API receives the
  raw container id, and the darwin session map is keyed by the raw `darwin-N`.
  `Create` still returns the raw internal id to the manager.
- **Service routing (`resolveSessionID`).** The MCP `Service` routes
  `Get`/`Exec`/`Info`/`Stats`/`Destroy` by **slug**: it parses the id, looks up
  the owning runtime (`runtimes[spec.Driver(slug)]`) and calls that manager's
  `Resolve` to expand a full or unique `slug:<prefix>` id to the full namespaced
  id before the backend call. There is **no `sessionDriver` map** and no global
  cross-driver prefix scan — routing is entirely slug-scoped per driver.
- **Short id keeps the slug.** `ShortID` on a namespaced id **preserves the slug**
  and shortens only the internal id to 12 chars (docker-style): `docker:<64hex>`
  → `docker:<12hex>`, `seatbelt:darwin-3` unchanged. A legacy bare id keeps the
  old bare 12-char truncation.
- **Owner label is retained; the session label is redundant.** The docker
  backend always applies the owner label (`dev.teran.sandbox.owner =
mcp-sandbox`) so the server can identify, reap and destroy exactly its own
  containers. The per-session label `dev.teran.sandbox.session` is **now
  redundant** because the session id _is_ the container id; the docker backend
  sets it only when a caller passes a session id via context, which the session
  manager no longer does.
- **OpenCode session id (`session_id`):** `sandbox_create` takes a **required**
  `session_id` parameter carrying the OpenCode client session id that created
  the sandbox. The docker backend records it as a dedicated container label
  (`dev.teran.sandbox.opencode_session`, set only when non-empty) so each
  sandbox container is traceable to the OpenCode session that created it. This
  is **distinct** from the sandbox session id above (the backend/container id).
  The required-ness is enforced at the tool input boundary; the domain-level
  `SandboxSpec.ClientSessionID` is optional so internal callers and e2e specs
  that build a spec directly can omit it.
- Each session has a **per-session mutex**; `destroy` is **idempotent** (X02).

> **Namespaced session id (`slug:internalID`) — planned as a SEPARATE FIRST
> implementation step (user decision, J4).** The driver-registry + `options:`
> work (§6.11/§7.11/§7.14) is being finalized in these docs as design direction,
> but the **namespaced-id change is a distinct, earlier step** that will be
> implemented first — it is a breaking change to the **sandbox id schema** of
> this section, independent of the registry. Requirements recorded for that step:
> a **stable, allowlist-validated `slug`** (the `Slug()` token, §6.11 I) joined
> to an **opaque internal id** by a single `:` (`slug:internalID`);
> **fail-closed parsing on the first `:`**; rework of `ShortID` (§6.6),
> `resolveID` (both `application/session/manager.go` and
> `infrastructure/mcp/service.go`) and their tests. This step is **not part of
> the docs being finalized here** — it is tracked as its own breaking change to
> this section.

### 7.9 Supply-chain (T7 / C05 / S05)

> **Phase 7 change:** the **trusted-registry allowlist (`TRUSTED_REGISTRIES`) and
> the built-in registry filter are removed.** The image now comes from the
> **operator-authored profile** (§7.11), so the operator — not an in-code
> allowlist — is the trusted party that decides which registry an image may come
> from. Image validation is **parse-only** (any valid reference from any registry
> is accepted); the supply-chain guarantee moves to the **operator convention of
> digest-pinning** plus the dependency/signing/scanner gates below.

- **Image validation is parse-only (Phase 7).** On `sandbox_create` and in
  profile validation, an image reference is accepted if it **parses** as a valid
  reference (`name[:tag][@digest]`). Empty or unparsable references are
  rejected. There is **no registry allowlist** — a profile may reference an image
  from **any** registry (`ghcr.io`, a private `registry.example.com:5000`,
  `localhost:5000`, …). The trust boundary is the operator who authors the
  profile (§7.11).
- **Auto-pull on create**: on `sandbox_create`, if the (validated) image is not
  present in the local cache the backend pulls it before creating the container.
  The pull stream is drained and discarded — image content is never logged (L05)
  and carries no secrets.
- **Digest-pinning is the recommended operator convention (Phase 7).** Because
  there is no registry allowlist, the **strongest** and **recommended** posture
  is to reference images **by digest** (`image@sha256:…`) in the profile rather
  than a floating tag (C05/T7). A digest pins _exactly which_ image is used and
  cannot be silently retagged. Tag references are permitted but are a weaker,
  operator-accepted choice.
- **Dependencies are pinned to the latest stable versions (C05)** and
  **verified**; installed packages at build/install time are **signed** where
  available.
- **Security-scanner findings are fixed, not suppressed (S05/N02GO).**

**Accepted dependency risk (documented):** the pinned `github.com/docker/docker`
SDK carries two advisories with **no known fixed version** on the used module
path — **GO-2026-4887** (CVE-2026-34040, AuthZ plugin bypass) and **GO-2026-4883**
(CVE-2026-33997, off-by-one in legacy plugin privilege validation). Both are in
Docker's **plugin-authorization** subsystem, which this client-only server does
not exercise (we do not manage Docker plugins). The fix exists only in the
pre-release `github.com/moby/moby/v2` (v2.0.0-beta.8). These two IDs are tracked
as accepted risk and are the only exceptions in `scripts/govulncheck.sh`;
**any new** vulnerability still fails the gate (S05 — no blanket suppression).
Revisit on the next docker SDK / moby upgrade.

### 7.10 Privilege model: default non-root + scoped root (`exec -u`)

Commands inside the sandbox run **unprivileged by default**; a scoped set of
"sudo" operations may run as **uid 0 inside the container** — achieved with
`docker exec -u`, **never** with `--privileged`.

- **Default user:** `sandbox_exec` runs with the sandbox's **unprivileged
  user** (`SandboxSpec.UIDGID`, or the image's `USER`). For our own base images
  that user is a known fixed non-root uid (e.g. `1000`) which owns the writable,
  executable tmpfs `/tmp` (and the `/run` tmpfs for secrets), so exec and file
  operations work without guessing.
- **External images:** fall back to a well-known unprivileged uid (`nobody`,
  `65534:65534`); such images may not permit tmpfs writes, so **our own
  images are the supported path**.
- **Sudo (scoped root):** an operation classified as "sudo" (`user: root` in
  `sandbox_exec`) runs with `docker exec -u 0`. Inside the container
  uid 0 is **still constrained** by the immutable `Isolation` block —
  `cap-drop ALL` + `no-new-privileges` + read-only rootfs — so it is
  container-internal uid 0, **not** host privilege. `--privileged` is **never**
  used (N21).
- **Sudo policy gate:** `SudoPolicy` controls which sudo-classed operations are
  permitted (`AllowedCommands`, `PackageInstallAllowed`).
- **Read-only rootfs vs package install:** because the rootfs is read-only,
  runtime `apt-get`/`dnf install` **cannot** write to the image layer. The
  supported model is **pre-provisioned images** (packages baked at build time);
  runtime package-manager writes into a bounded writable area are **deferred**
  (roadmap). `SudoPolicy.PackageInstallAllowed` therefore refers to
  _provisioned_ images for the MVP.

### 7.11 Operator-authored sandbox profiles (Phase 7)

A **sandbox profile** is a declarative, **operator-authored, READ-ONLY YAML
file** that fully determines the runtime shape of a sandbox: image, env, mounts,
network mode, cgroups/limits, user (non-root) and sudo. **Profiles replace the
per-request `sandbox_create` knobs** (image, env, mounts, nano_cpus, memory,
pids_limit, uid, gid, sudo — all removed) and the operator-configuration env
vars that are now gone (§6.8). There is **no** profile create/update/delete
mechanism and **no** profile tool that writes — profiles exist only as files.

#### Trust model

- **Operators author, models reference (read-only).** Profiles live under
  `SANDBOX_PROFILES_DIR` (default `~/.config/sandbox/profiles`), one
  `<name>.yml` per profile. The model-host never edits them — `sandbox_create`
  takes a `profile` **name** and the server resolves it against the read-only
  store; `profile_list` only enumerates `name`/`description`.
- The profile is the **trusted party for image, env, mounts, network, limits,
  user and sudo**. Because a profile is operator-authored, the image may come
  from **any** registry (no allowlist, §7.9) and network may be `host` when the
  operator authors it — these are deliberate operator decisions, not
  model-supplied.
- **`ALLOW_DIRS` is retained as the host-path gate.** It gates the
  client-supplied `cwd` **and every mount `source` in a profile** — a profile
  cannot mount a host path outside `ALLOW_DIRS`, and `ALLOW_SYMLINKS` still
  governs symlink resolution (§7.2). The immutable `Isolation` hardening block
  (read-only rootfs, cap-drop ALL, no-new-privileges, seccomp, non-root, tmpfs
  `/run`+`/tmp`, `AutoRemove`) still applies on top of every profile (§7.1) and
  a profile can **never** weaken it.
- **No secrets in profile output.** `profile_list` emits only `name` and
  `description`; env values, mount sources, image, user and sudo policy are
  **never** returned (S02).

#### Schema

> **Schema status (design direction, not yet implemented):** the schema below
> (with the typed `docker:` / `seatbelt:` blocks) is the **current / migrating**
> form. The agreed **target** is a `driver: <name>` + opaque **`options:`** form
> (see the end of this section) so new drivers can be added **without a commit to
> mcp-sandbox** (§6.11). The typed blocks below migrate into `options:`.

```yaml
name: default # required; store key; [a-zA-Z0-9._-], no path separators
description: the default profile # required
driver: docker # docker | seatbelt | auto (auto = resolve the single present block)
docker: # Docker-backend block; mutually exclusive with seatbelt (§7.12)
  image: alpine@sha256:… # required; parse-only validation (any registry; digest recommended, §7.9)
  env: # optional
    FOO: bar
  mounts: # optional; every source must be within ALLOW_DIRS (§7.2)
    - source: /data
      target: /data
      read_only: true
  network:
    mode: bridge # bridge (default) | none | host (host only if operator authors it)
  cgroups: # limits; a missing value falls back to the server default (§7.1)
    cpu_cores: 1 # human-readable: whole/virtual cores (1.0 = one core); raw nano_cpus takes precedence if both set
    memory_gb: 1 # human-readable: whole GiB; raw memory/memory_swap take precedence if set; swap disabled ⇒ memory_swap = memory
    pids_limit: 256
    ulimits:
      - name: nofile
        soft: 1024
        hard: 1024
  user: # non-root enforced; nil ⇒ server default non-root user
    uid: 1000
    gid: 1000
  sudo: never # never (default) | require
cwd: # optional; controls the auto session-cwd mount (§7.2)
  target: "" # empty ⇒ 1:1 at the same absolute path (default)
  mode: ro # ro (default) | rw
shell: bash # optional; default bash; whitespace-separated tokens (§7.5)
```

Schema key reference (`domain/sandbox/profile.Profile`):

| Key                 | Type                                                   | Meaning / validation                                                                                                                                                                                                                                                                              |
| ------------------- | ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`              | string                                                 | Required. Store key. Regex `[a-zA-Z0-9._-]+`; must not be a path separator / `..` (path-traversal rejected, §7.11 loader).                                                                                                                                                                       |
| `description`       | string                                                 | Required. Human-readable summary shown by `profile_list`.                                                                                                                                                                                                                                         |
| `driver`            | string                                                 | Optional. `auto`(default; resolves to the single present block)/`docker`/`seatbelt`. An explicit driver must match its block and forbid the other (§7.12).                                                                                                                                       |
| `docker`            | block                                                  | Docker-backend block (below); mutually exclusive with `seatbelt`. `driver: docker` requires it.                                                                                                                                                                                                   |
| `seatbelt`          | `{env,sbpl,mach_lookups,sysctls}`                      | Optional. **darwin/Seatbelt only (§7.12)** — session env (redacted per S02 like Docker profile `env`), operator SBPL template (merged after server-hardening ops), extra `mach-lookup` services and `sysctl` keys. Ignored by the Docker backend. Validation rejects only NUL in `sbpl`.          |
| `cwd`               | `{target,mode}`                                        | Optional. `target` empty ⇒ mounted 1:1 at the client path; `mode` `ro`(default)/`rw`.                                                                                                                                                                                                             |
| `shell`             | string                                                 | Optional. The shell used for the **fixed** `sandbox_exec` wrapper `[ <shell>, "-c", command ]` (§7.5). Whitespace-separated tokens (split on whitespace); NUL/newline characters rejected; empty/missing ⇒ default `bash`. Operator picks a shell available in the image.                        |
| `docker.image`      | string                                                 | Required (docker block). Parse-only reference validation (§7.9); digest-pinning recommended.                                                                                                                                                                                                      |
| `docker.env`        | map[string]string                                      | Optional. Container environment.                                                                                                                                                                                                                                                                  |
| `docker.mounts[]`   | `{source,target,read_only}`                            | Optional. `source` must be absolute **and within `ALLOW_DIRS`** (§7.2).                                                                                                                                                                                                                           |
| `docker.network`    | `{mode}`                                               | Optional. `bridge`(default)/`none`/`host` — `host` only when operator-authored.                                                                                                                                                                                                                   |
| `docker.cgroups`    | `{nano_cpus,memory,memory_swap,pids_limit,ulimits}`    | Optional. Bounded resource limits; profile overrides the server baseline; missing ⇒ server default (§7.1). `memory_swap` ≥ `memory`; values must be bounded (> 0).                                                                                                                                |
| `docker.user`       | `{uid,gid}`                                            | Optional. Must be **non-root** (`uid != 0`); nil ⇒ server default non-root user.                                                                                                                                                                                                                  |
| `docker.sudo`       | string                                                 | Optional. `never`(default)/`require`.                                                                                                                                                                                                                                                             |

#### Loader & store (read-only)

- **`application/profile`** exposes `New(dir)`, `Get(name) (*Profile, error)`
  and `List() ([]*Profile, error)`. It is **read-only by construction** — there
  are no create/update/delete operations.
- **Read-on-demand (no in-memory store, no startup load):** there is **no**
  in-memory store and **nothing is loaded at startup**. Every `Get(name)` /
  `List()` call **reads and parses `SANDBOX_PROFILES_DIR` fresh**, on demand.
  `List()` returns profiles sorted by name. Consequently, editing a `<name>.yml`
  takes effect immediately — **no server restart needed**.
- **Fail-closed and traversal-safe:** a file that is unparsable YAML, fails
  `Validate`, declares a duplicate `name`, or whose declared `name` attempts
  path traversal (`..`, separators) surfaces a **wrapped error** from
  `Get`/`List` — the store does not silently skip or serve a hostile profile. A
  missing directory yields an empty set (`Get` → `ErrProfileNotFound`, `List` →
  empty + nil).
- **Error semantics:** `Get` on a missing/empty directory or an unknown name
  returns `ErrProfileNotFound`; a malformed or traversal-attempting lookup
  `name` returns `ErrInvalidName`. The lookup `name` is **never used as a
  filesystem path**: the store scans the whole directory on each call and
  matches the **declared** `name` fields (which also enables duplicate
  detection). Traversal safety is enforced via `os.OpenRoot`.
- The profile's declared `name` (not the filename) is the authoritative store
  key; a hostile name is rejected even when the filename itself is plain.
- **Transient torn-write window:** because each call reads the directory fresh,
  an in-place/torn file write can surface a transient fail-closed error on that
  single call only (it self-heals on the next call). **Prefer atomic writes** to
  `<name>.yml`.

#### `sandbox_create` in the profile model

`sandbox_create` takes `profile` (required), `session_id` (required), `cwd`
(required) and optional `secrets`. The server resolves `profile` to a
`domain/sandbox/profile.Profile`, then `BuildSpec(isolation, Params{Cwd,
Secrets, ClientSessionID})` produces the typed `SandboxSpec` (§6.5) with:

- `image`, `env`, `mounts` (appended after the auto cwd mount), `network`,
  `cgroups`, `user`, `sudo` from the profile;
- the **session `cwd` always mounted 1:1** at the same absolute path (read-only
  by default) unless the profile's `cwd` remaps `target`/`mode` (§7.2);
- the **immutable `Isolation` hardening block always from server policy**,
  never from the profile (§6.5/§7.1);
- `secrets` delivered as tmpfs `0600` files under `/run/sandbox` (§7.3).

If the requested `profile` is not in the store, `sandbox_create` returns a
"not found" error.

#### Target schema — `driver` + opaque `options:` (design direction, not yet implemented)

The agreed direction is a **breaking schema migration** from the hybrid
driver-aware form above to a **driver registry + opaque `options:`** form
(§6.11). The `driver` name stays the registry key; the typed `docker:` /
`seatbelt:` blocks migrate into an opaque **`options:`** blob whose keys are
**driver-owned** (interpreted only by the driver matched by `driver`). Top-level
`name` / `description` / `cwd` / `shell` remain.

> **This is a BREAKING schema change.** The exact key names inside `options:`
> are owned by the driver, not by mcp-sandbox — so a Docker block keyed
> `options.image` is a driver convention, not a server contract.

**Before (current / migrating):**

```yaml
name: default
description: the default profile
driver: docker
docker:
  image: alpine@sha256:…
  env: { FOO: bar }
  mounts:
    - source: /data
      target: /data
      read_only: true
  network: { mode: bridge }
  cgroups: { cpu_cores: 1, memory_gb: 1, pids_limit: 256 }
  user: { uid: 1000, gid: 1000 }
  sudo: never
cwd: { target: "", mode: ro }
shell: bash
```

**After (target form):**

```yaml
name: default
description: the default profile
driver: docker # REQUIRED; the registry key; selects the builder (auto/empty removed)
options:        # opaque; interpreted ONLY by the docker driver
  image: alpine@sha256:…
  env: { FOO: bar }
  mounts:
    - source: /data
      target: /data
      read_only: true
  network: { mode: bridge }
  cgroups: { cpu_cores: 1, memory_gb: 1, pids_limit: 256 }
  user: { uid: 1000, gid: 1000 }
  sudo: never
cwd: { target: "", mode: ro }
shell: bash
```

**Migration note (settled — ONE-SHOT breaking, no compat shim):** existing
profiles must move their `docker:` / `seatbelt:` keys into `options:` and set an
explicit **`driver:`** (required). In the target schema `driver` is **REQUIRED**
and **`auto` / empty is REMOVED** — there is **no typed block to auto-resolve**
and no implicit selection. The migration is a **one-shot breaking change**: there
is **no translation shim** that still reads the old `docker:`/`seatbelt:` blocks.
A README **migration guide / helper** documents and automates converting a
profile to the target form. `Service.resolveDriver` (which today resolves
auto/empty to a concrete driver, `infrastructure/mcp/service.go`) may **remain
only for internal/built-in compatibility**, not for stored profiles — a stored
profile must always carry an explicit `driver:`.

The core's hardening enforcement is **unchanged** and is the **data-plane floor
(§6.11)**: the immutable `Isolation` block is re-asserted on the result via
`EnforceHardening` (§6.11, G), and every mount `source` inside `options` is still
gated by `ALLOW_DIRS` (§6.11).

### 7.12 Apple / `sandbox-exec` driver (darwin backend) — experimental

> **Status: experimental alternative driver — implemented (portable core +
> darwin `log stream` tailer), real-macOS verification via the darwin e2e.** This
> section documents a **second** backend that,
> unlike the Docker MVP (§1), is **macOS-only** and runs on **Apple's Seatbelt**
> via the external `/usr/bin/sandbox-exec` binary — **pure Go, no cgo, no
> root**. It realizes the same `Backend` roles on Apple platforms and is a
> concrete instance of the "Apple Container (and other backends)"
> future-extension slot (§1) and the backend-abstraction roadmap (§7.7).
> **Docker remains the default/preferred backend** (lower `Priority`); the
> darwin backend is selected only on macOS when Docker is unavailable.
> **It is now selectable**: the create/exec/destroy capability set
> (`Lifecycle + Executable`) no longer includes `Introspectable`, which the
> darwin backend legitimately lacks (no cgroups on macOS), so it passes
> capability-based selection on macOS (§6.3).
>
> **Implementation status:** the backend is **implemented** in
> `infrastructure/backend/darwin` (`darwin.go` — portable core; `runner_darwin.go`
> — the real `/usr/bin/sandbox-exec` runner behind the `darwin` build tag;
> `runner_other.go` — non-darwin stand-in; `deny.go` — portable policy-denial
> classifier; `tailer_darwin.go` — the **macOS-specific unified-log `log stream`
> tailer wrapper** behind the `darwin` build tag, `//go:build darwin`), wired
> end-to-end via the `seatbelt` profile block
> (`application/profile`), `spec.Sandbox` / `spec.DriverSeatbelt`
> (`domain/sandbox/spec`), the SBPL generator+validator
> (`domain/sandbox/sbpl`), and factory registration through `init()`
> (`Driver: seatbelt`, `Priority` 10). The full backend logic — including the
> `log stream` tailer wrapper behind its injectable boundary — is exercised by
> unit tests in CI (mock runner/tailer, coverage ≥ 95%) on **any** GOOS.
> Recent fixes (all TDD-verified, unit-tested): the child command is the
> **BSD-portable `sleep 2147483647`** (not the GNU-ism `sleep infinity`); the
> **in-band policy-denial classifier is now wired into `Backend.Exec`** —
> `IsPolicyDenial(res)` sets the new `exec.Result.PolicyDenied` bool on the
> returned result (the `deny.go` classifier was previously dead code; the Docker
> backend is unaffected); and the **setrlimit save/apply/restore is now correct**
> — a portable `rlimit_apply.go` (`rlimitOps` + `rlimitSaveAndApply` +
> `rlimitRestore`, with rollback) is wired into `realRunner.Start` with a
> deferred restore, so the parent's limits are always restored and the child
> inherits the lowered limits at fork.
> **Real-macOS verification (darwin e2e):** spawning a live
> `/usr/bin/sandbox-exec` child and asserting its exit-code / `IsPolicyDenial`
> signals require a real macOS host and cannot be exercised inside a
> Docker/CI sandbox, so this is exercised by the **darwin e2e smoke tests**
> (`//go:build e2e && darwin`), which run under the generic **`make e2e`**
> (`go test -tags e2e ./...`) on a **dedicated macOS CI `e2e` job** (§11). There
> is **no separate `e2e-darwin` target** by design; on Linux the darwin e2e is
> excluded by the `darwin` build tag. **The unified-log `log stream` tailer is
> NOT exercised by the e2e** — the e2e drives `sandbox-exec` directly and only
> asserts exit code + `IsPolicyDenial`; the tailer is covered by `darwin`-tagged
> unit tests (`tailer_darwin_test.go`), not a live log stream.

#### Mechanism

- A sandbox is a **self-sandboxed child process**: the backend spawns a
  long-lived child under `/usr/bin/sandbox-exec -p <SBPL> <shell> -c ...` (the
  same **fixed** `[<shell>,"-c",command]` wrapper as §7.5, **S11**; the
  rendered profile is passed as an argv argument, never shell-interpolated).
  `sandbox-exec` applies the Seatbelt (SBPL) profile to **itself** and then
  `execvp`s the child, which inherits the sandbox — so **no root and no
  privilege elevation** is needed and the "started but not yet sandboxed"
  window is closed. "Create" spawns the child (the `sleep 2147483647`
  analogue — the BSD-portable form, not the GNU `sleep infinity`); "Destroy" is
  `SIGKILL` + `Wait` (idempotent, **X02**).
- **Pure Go, no cgo:** `sandbox-exec` is an external binary invoked via
  `os/exec`; there is no cgo and the server process is **never root**
  (`Available()` returns false when `euid == 0`). The package is split into a
  **portable core** (`darwin.go`, compiled on every GOOS and exercised in CI
  against a mock runner) and a **platform runner** selected by build tag
  (`runner_darwin.go` / `runner_other.go`) — so the backend logic is verifiable
  on any platform while the real `os/exec` wrapper lives behind the `darwin`
  tag.

#### Roles (capability set)

| Role                              | darwin backend | Notes                                                                                                             |
| --------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------- |
| `Backend`                         | ✅             | Name `darwin`, `Driver` = `seatbelt`, `Priority` 10 (docker=0 preferred).                                         |
| `Lifecycle` (`Create`/`Destroy`)  | ✅             | Spawn/kill of the sandboxed child; idempotent (**X02**).                                                          |
| `Executable` (`Exec`)             | ✅             | `sandbox-exec -p <SBPL>` wrapper around the fixed `[<shell>,"-c",command]` form.                                  |
| `Introspectable` (`Info`/`Stats`) | ❌             | **Not implemented.** No cgroups on macOS; `Info`/`Stats` return `ErrCapabilityNotSupported` (§6.2) — **no stub**. |

Registration follows §6.3: the factory registers via `init()` into the shared
registry, is blank-imported in `cmd/mcp-sandbox/main.go`, and its `Available()`
is evaluated **lazily** in `Select` (**A02/N35**): `runtime.GOOS == "darwin"`,
`/usr/bin/sandbox-exec` exists, and `euid != 0`.

#### Profile extension (`seatbelt` block, §7.11)

The operator profile gains an optional **`seatbelt`** block that carries the
Seatbelt (SBPL) template and darwin knobs. It is projected into
`SandboxSpec.Sandbox` by `Profile.BuildSpec` and consumed by the darwin
backend (`domain/sandbox/spec.Sandbox`):

```yaml
seatbelt: # optional; darwin/Seatbelt
  env: # optional; session environment (redacted per S02, §7.3)
    GIT_AUTHOR_NAME: "Igor Shishkin"
    GIT_AUTHOR_EMAIL: "me@teran.dev"
  sbpl: | # operator SBPL merged AFTER server-hardening ops
    (import "system.sb")
    (allow process-fork)
  mach_lookups: # extra (allow mach-lookup ...)
    - com.apple.system.logger
  sysctls: # extra (allow sysctl-read ...)
    - kern.osversion
```

`domain/sandbox/profile.Profile.Seatbelt` is a `*SeatbeltProfile` (SBPL,
Env, MachLookups, Sysctls) that `BuildSpec` projects into
`SandboxSpec{Env, Sandbox: spec.Sandbox{SBPL, MachLookups, Sysctls}}` —
the `env` values populate the session environment (delivered via `cmd.Env`,
redacted per S02 like Docker profile `env`, §7.3) and the SBPL/mach/sysctl
fields fill the `spec.Sandbox` block consumed by the darwin backend. **The
operator `seatbelt.sbpl` is appended verbatim
(last-match-wins) and does NOT pass through the `sbpl` metacharacter validators**
(`ValidatePath`/`ValidateMachName`/`ValidateSysctlName`) — only a NUL byte is
rejected (`profile.ValidateSeatbelt`, `spec.Validate`). This is a deliberate
trust decision: the **operator is trusted** to author SBPL, and a model-host
caller can never supply SBPL text — it only supplies a typed `SandboxSpec`
(`sbpl.Generate`). Because the tail is appended last, an over-broad operator
rule (e.g. `(allow default)`) re-opens surfaces the server baseline denied —
see the security audit below. Validation rejects only NUL in `SBPL`; the profile is **read-only**
and operator-authored per the §7.11 trust model. A profile that requests a
darwin-unsupported capability (see below) remains **valid** but the limit is
**honestly reported as not enforced** on this backend — never silently ignored.

#### Isolation → Seatbelt projection

The darwin backend renders a full SBPL from the typed `SandboxSpec` (order:
imports → `(deny default)` → network → mach → sysctl → file → operator
`seatbelt.sbpl`). The generator is a **deterministic pure function
`SandboxSpec → SBPL`**; a caller never supplies SBPL text, it supplies a
`SandboxSpec` exactly as for Docker:

| `Isolation` / spec                    | Seatbelt operation                                                                                                                               |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| always                                | `(deny default)`                                                                                                                                 |
| `Mounts` + `cwd` (ro)                 | `(allow file-read* (subpath "<path>"))`                                                                                                          |
| `Mounts` + `cwd.mode=rw`              | `(allow file-read* …)` + `(allow file-write* …)`                                                                                                 |
| `NetworkMode == none`                 | `(deny network*)` (default)                                                                                                                      |
| `NetworkMode == bridge`/`host`        | `(allow network-outbound)`                                                                                                                       |
| `Resources.Ulimits`                   | `setrlimit(2)` via Go wrapper before `execvp` (`RLIMIT_NOFILE/AS/CPU/NPROC/FSIZE/STACK/CORE`)                                                    |
| `Resources.NanoCPUs/Memory/PidsLimit` | **not mappable** (no cgroups on macOS) — declared unsupported, never silently ignored                                                            |
| `UIDGID`                              | **not applied** (self-sandbox runs as the current host user)                                                                                     |
| `Isolation.User` (non-root)           | **enforced only as `euid != 0`** (server refuses root); the process still runs as the current host user (`UIDGID` not applied)                   |
| `Isolation.NoNewPrivileges`           | **not enforced on darwin** — validated by `spec.Validate`, but there is no mechanism (`SysProcAttr` is `Setpgid` only, `runner_darwin.go:45-49`) |
| `Isolation.ReadOnlyRootfs`            | **not enforced on darwin** — no container rootfs; the process runs on the host filesystem under SBPL file rules                                  |
| `Isolation.CapDrop`                   | **not enforced on darwin** — no capability model; the process is a plain host process                                                            |
| `Isolation.TmpfsWorkdir`              | **not enforced on darwin** — no tmpfs; the secret dir is a per-session `0700` directory on the host                                              |
| `Isolation.AutoRemove`                | **not applicable on darwin** — no container; cleanup is explicit kill (§7.12)                                                                    |
| mach                                  | `(deny mach-lookup)` + allow-list                                                                                                                |
| sysctl                                | `(deny sysctl*)` + allow-list                                                                                                                    |

> **Deny-all default — every allowance is operator-authored.** The server-rendered
> baseline for the darwin backend is a **single default `(deny default)`** — there
> is **no** implicit `allow` in the generated profile. **Every** allowance,
> including the **`(allow process-exec*)`** needed to run a command, **must be
> explicitly authored by the operator** in the profile's `seatbelt.sbpl`. Without
> an exec grant (and the file-read/mach/sysctl grants for the shell and its
> runtime, e.g. `/bin`, `/usr/bin`, `/usr/lib`, dyld), `sandbox_exec` cannot run
> anything — the shell itself is denied by `(deny default)` and `execvp` fails.
> This is **by design**: `deny-all` is the only default; the operator owns all
> grants. **Either exec form is acceptable** — an explicit `(allow process-exec*)`
> (narrower but hand-maintained), or `(allow default)` for non-file operations
> with a narrowed file scope (the pragmatic model used by the bundled, verified
> `examples/profiles/seatbelt.yml`, below). **This gap is open**: the server
> generator does not yet emit `process-exec*`; supplying it is an operator
> requirement, not something the server does. The darwin e2e
> (`§11`) drives `sandbox-exec` with an `(allow default)` SBPL directly,
> bypassing the server generator, so it does **not** validate exec under the
> server-rendered deny-default baseline.

> **Practical file-deny-all (validated on macOS 26, SBPL version 3).** A pure
> deny-all across **every** operation is not practical for running arbitrary
> commands: `/bin/bash` and dyld require **dozens** of system operations
> (`process-exec`, `mach-*`, `iokit-*`, `ipc-posix-*`, `system-socket`,
> `syscall-*` …) that cannot be hand-listed without breaking startup. This is
> exactly why Bazel's `DarwinSandboxedSpawnRunner` builds its profile on
> `(allow default)` + targeted `deny` rather than a hand-listed deny-all. The
> pragmatic, **verified** model (passes `make lint`/`make test`/`make build`
> under the sandbox) is therefore:
>
> - `(allow default)` **only** for non-file system operations (process/mach/
>   iokit/ipc/syscall) — unavoidable for the shell to start;
> - **FILE access is deny-all**: `(deny file-read*)` + `(deny file-write*)`,
>   with explicit `(allow file-read* …)` for the system toolchain dirs
>   (`/usr/lib`, `/usr/share`, `/System/Library`, `/bin`, `/usr/bin`, `/dev`,
>   `/private/etc/ssl`, homebrew/go paths) and the **single repo root (cwd)**;
>   write allowed only in cwd + `/dev` + temp dirs. Every other host path
>   (`.ssh`, `.aws`, `Movies`, `Documents`, `/etc/hosts`, `~/.config/…`) is
>   denied **without** listing each one.
> - `(allow file-read-metadata)` is granted **globally** — it exposes only
>   `stat`/`readlink`, never file contents, so it is safe to grant broadly and
>   lets tools (`go`, `golangci-lint`, `make`, `git`) resolve path components
>   (`EvalSymlinks`, `realpath`, cache dirs) without whitelisting every
>   intermediate directory.
> - `(allow network-outbound)` for outbound connectivity.
> - `shell` MUST be an **absolute** path (`/bin/bash`), because
>   `sandbox-exec` does `execvp(shell)` against the **server** process `PATH`
>   (not the per-exec env) — a bare `bash` yields
>   `execvp() of 'bash' failed: No such file or directory` even under
>   `(allow default)` when the server `PATH` omits `/bin`.
>
> **`(debug deny)` is unavailable on macOS 26.** The `debug` macro is absent
> from `system.sb` (SBPL version 3), so emitting `(debug deny)` — either first
> or after `(import "system.sb")` — fails to compile with
> `unbound variable: debug`. Diagnostics must instead rely on the unified log
> (`log stream --predicate 'subsystem == "com.apple.sandbox"'`) and a
> `file-read-metadata`-granted environment. The `seatbelt` example profile
> documents this working file-deny-all template (with `/Users/<username>`
> placeholder).

**SBPL injection is a real new vector** (an S11-analog absent from the Docker
backend). No raw user/profile string is ever interpolated into SBPL; only
typed, validated absolute paths are used, and each path is rejected if it
contains any SBPL metacharacter from `" \ ( ) ;` or `\n \r \0` — otherwise a
crafted value such as `") (allow file-write*) ;"` would terminate the string
and inject an `(allow file-write*)` token. The generator is covered by
injection-focussed unit tests (fail-closed: a hostile value aborts the whole
projection with an error matching `errors.Is(err, sbpl.ErrSBPLMetachar)`, never
a partial profile).
`ALLOW_DIRS` remains the **single** host-path gate (**S04**): the generator
emits `file-read*`/`file-write* (subpath …)` rules **only** for sources inside
`ALLOW_DIRS` (and the sanctioned per-session `0700` scratch dir); the session
`cwd` must be within `ALLOW_DIRS`; everything else is deny-by-default.
`ALLOW_SYMLINKS`/`EvalSymlinks`/open-then-verify run **before** SBPL
generation. `mach-lookup` is narrowed to the minimum bootstrap services a
shell needs (confused-deputy containment, below).

#### Reduced threat model vs Docker (explicit)

The darwin driver is **strictly weaker** than the Docker MVP and is documented
as such; it is a **policy-based containment** convenience for macOS hosts,
**not** the security target and **MUST NOT** be presented as equivalent to
Docker/gVisor isolation:

- **Shared host kernel.** No namespaces, no separate kernel, no user
  namespace. An SBPL bypass or a kernel bug = **full host compromise** at
  operator privilege — not a root boundary and **not** a gVisor/runsc-class
  kernel-escape containment (see §7.7 target state).
- **No cgroup resource budgets.** `NanoCPUs`, `Memory`, `MemorySwap`,
  `PidsLimit` do **not** translate 1:1 (macOS has no cgroups) and are declared
  **unsupported**, never silently ignored. Only setrlimit-mappable `ulimits`
  are enforced, and those are **soft per-process limits**, not hard cgroup
  quotas — a runaway process can degrade the whole host (DoS).
- **No uid change.** `sandbox-exec` runs as the **current host user**
  (`UIDGID` is not applied); a darwin sandbox shares the host user's identity
  and is not the non-root drop of §7.10. The driver refuses to run at `uid 0`
  (self-sandbox, §7.6-analog).
- **No network isolation.** macOS has no network namespaces, so `bridge` is
  not a real bridge and host/bridge are indistinguishable; only
  `network-outbound` deny/allow is enforceable.
- **Shared IPC / confused deputy.** The Mach/IPC namespace is shared; any
  allowed `mach-lookup` to a privileged Mach service is a confused-deputy path
  (the service cannot distinguish a sandboxed caller). Hence `mach-lookup`
  MUST be minimal.
- **No supply-chain image/digest boundary (§7.9).** There is no image; SBPL
  executes on the host OS against host libraries. The operator-authored
  profile is trusted only insofar as its SBPL is not over-broad.
- **Unsupported / deprecated Apple API.** Both `/usr/bin/sandbox-exec(1)` and
  the C API `sandbox_init(3)` are formally **deprecated** in the macOS SDK
  (`API_DEPRECATED` in `sandbox.h`), and there is **no non-deprecated Apple
  runtime API** to sandbox an arbitrary subprocess — App Sandbox is
  entitlement/code-signing based and does not apply to runtime-spawned children.
  These are therefore **not guaranteed** by Apple as a security boundary for
  hostile/multi-tenant code; behavior may change across OS releases.
  **Decision:** the darwin backend **stays on `/usr/bin/sandbox-exec`** and keeps
  the **"pure Go, no cgo"** constraint (C01GO/C02GO). Moving to cgo +
  `sandbox_init` was evaluated and **rejected**: it would break the no-cgo
  recorded decision and merely duplicate `sandbox-exec`'s
  self-sandbox-then-exec logic.

**Invariants preserved:** `ALLOW_DIRS` as the single host-path gate (**S04**);
no secret leakage (**S02/N02**) — secrets delivered as a `0600` file in a
per-session `0700` directory (macOS has no tmpfs; the analog of `/run/sandbox`),
**never** in argv and **never** in SBPL (the literal validator is what prevents
a secret from entering SBPL); non-root self-sandbox (§7.6-analog); **S11**
no-shell-concat preserved.

#### Lifecycle, observability & reliability

- **No container/daemon/image.** A session is an **in-memory record** (SBPL +
  env + cwd) in the session manager (**X01**); each `sandbox_exec` spawns a
  fresh `sandbox-exec` child. Cleanup is **explicit only** (§6.6): `Destroy`
  kills the whole process group (`kill(-pgid, SIGKILL)`), **idempotently**
  (`ESRCH` → no-op). There is **no `AutoRemove` equivalent**; a server exit
  without explicit destroy leaves the sandboxed tree as an **orphan** (nothing
  is left but live processes and any host path the profile granted `rw`).
  `req.Timeout` (§6.10) becomes a process-group `SIGKILL` cancellation
  (SIGTERM is unreliable against sandboxed children).
- **No live introspection** (`Introspectable` → `ErrCapabilityNotSupported`);
  resource observability uses `getrusage`/`task_info`/`ps` (no cgroup metrics).
- **Policy-denial detection.** `sandbox-exec` does not log its own denials;
  macOS records them in the **unified log**
  (`subsystem == "com.apple.sandbox"`). The darwin driver tails this log
  filtered by session and classifies a `deny` as a **policy failure**, not a
  normal exit code (a denied syscall also surfaces in-band as `EPERM`). Logged
  with `session_id` and pid (L08-style, no secrets). The **in-band classifier is
  now wired into `Backend.Exec`**: `IsPolicyDenial(res)` sets the new
  `exec.Result.PolicyDenied` bool on the returned result, so a policy failure is
  distinguishable from a plain non-zero exit even when the exit codes coincide
  (the classifier was previously dead code — never called from `darwin.go`; the
  Docker backend is unaffected). **Implementation status:**
  the portable, fail-closed classifier (`ClassifyDeny` / `ConsumeDenials` /
  `IsPolicyDenial`, `deny.go`) and the `logTailer` boundary interface are
  **implemented and unit-tested** (in-memory tailer, no secrets, S02), and the
  **macOS-specific `log stream` tailer wrapper** (`tailer_darwin.go`, behind the
  `darwin` build tag) is now **implemented** behind that injectable boundary.
  The live unified-log tail is **NOT exercised by the darwin e2e**: the e2e
  (`sandbox_exec_e2e_darwin_test.go`) drives `sandbox-exec` directly and asserts
  only exit code and `IsPolicyDenial`; the tailer is covered by `darwin`-tagged
  unit tests (`tailer_darwin_test.go`), not a live `log stream` on a real host.
- **Portability (declared break).** The `sandbox-exec` backend is
  **macOS-only**; a Linux host has no `sandbox-exec` and must fall back to
  Docker (or a future `bwrap` backend). Backends are **non-interchangeable
  isolation mechanisms**, not a portable abstraction layer — backend selection
  is explicit per platform, and the absence of an available backend surfaces
  `ErrNoAvailableBackend` rather than silently degrading (§6.3).

### 7.13 Domain security audit — Docker MVP & darwin / `sandbox-exec` backend (S14)

This subsection is the **consolidated, authoritative** threat model. It covers
the **Docker MVP** (default), the **darwin / `sandbox-exec`** backend, and (as a
**design-direction** trust zone, §6.11) **external / non-auditable drivers**. It
supersedes the scattered per-item notes in §7.1–§7.12 as the single
source of truth for security review, and cross-references §7.12 (mechanism /
projection / lifecycle) and §8 (data & state).

#### Docker MVP — consolidated audit

The Docker backend is the **default** isolation mechanism (kernel/container
isolation via namespaces + cgroups + seccomp + read-only rootfs). Its threat
model is distributed across §7.1–§7.11; this block consolidates it.

##### Trust zones (Docker)

| Zone                                                         | Trust                          | Notes                                                                                                                                                                                    |
| ------------------------------------------------------------ | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Model-host caller**                                        | **Untrusted**                  | Supplies only a typed `SandboxSpec`; can never weaken the immutable `Isolation` block or widen `ALLOW_DIRS` (§6.5/§7.1).                                                                 |
| **Operator profile author**                                  | **Trusted**                    | Authors the read-only profile (image/env/mounts/network/limits/user/sudo); can choose `host` network or any registry image, but can **never** weaken the immutable `Isolation` block (§7.1/§7.11). |
| **Sandbox container**                                        | **Untrusted**                  | A possibly-hostile command runs inside an isolated, hardened container (namespaces, cgroups, seccomp, read-only rootfs, non-root).                                                       |
| **Host kernel**                                              | **Isolated (kernel boundary)** | Container isolation is a **kernel-escape** boundary; a container escape would be a host compromise at operator privilege (§7.7 target is gVisor/runsc).                                  |
| **Docker daemon / socket**                                   | **Trusted but privileged**     | The Docker socket is effectively root on the host (§7.6); access is confined to the server process and never exposed to the model-host.                                                  |

##### Threat vectors (Docker, D1–D6)

| ID     | Threat                                                                   | Docker exposure                                                                                                                                                                                                                                        |
| ------ | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **D1** | **Container escape (kernel-escape)**                                     | **MED-HIGH.** No gVisor/runsc yet (roadmap §7.7); a kernel/container-runtime vulnerability could escape to the host at operator privilege. Mitigated by hardened defaults (seccomp, cap-drop ALL, no-new-privileges, non-root, read-only rootfs).       |
| **D2** | **Host-path access via mounts**                                          | **LOW-MED.** Host access only via `ALLOW_DIRS` (S04), read-only by default; `EvalSymlinks` + open-then-verify + `O_NOFOLLOW` reject escapes; hardlinks mitigated at OS level (§7.2). A `cwd.mode=rw` mount widens the host-write surface.             |
| **D3** | **Privilege escalation inside the container**                            | **MED.** Commands run unprivileged by default; scoped `exec -u 0` gives container-internal uid 0 still bounded by cap-drop ALL + no-new-privileges + read-only rootfs — never `--privileged`, never host root (§7.10).                                  |
| **D4** | **Resource exhaustion (DoS)**                                            | **LOW-MED.** cgroup quotas (`Memory`/`MemorySwap`/`NanoCPUs`/`PidsLimit`/`Ulimits`) bound the container (§7.1). A runaway container cannot degrade the whole host beyond its quota.                                                                       |
| **D5** | **Supply chain / image**                                                 | **MED (operator-managed).** Image comes from the operator profile; **digest-pinning is the recommended convention** (§7.9). No registry allowlist — the operator is the trust boundary for the image source.                                             |
| **D6** | **Network reach-in / exfiltration**                                      | **MED.** Network comes from the operator profile: `bridge` (safe default) or `none`; `host` only when operator-authored (§7.1). Outbound connectivity is operator-controlled.                                                                            |

##### Data-sensitivity (Docker, C-I-A)

| Property            | Assessment                                                                                                                                                                                                                                                 |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Confidentiality** | Stronger: container isolation + read-only rootfs + non-root; host filesystem visible only within `ALLOW_DIRS` (read-only default). Secrets delivered as tmpfs `0600` files, never argv/image/env (§7.3).                                                     |
| **Integrity**       | Container-internal writes only into writable tmpfs `/tmp` (+ `cwd` when `mode=rw`); host-visible state is protected by read-only mounts and the immutable `Isolation` block.                                                                                |
| **Availability**    | cgroup resource quotas bound the container; explicit-only cleanup means orphaned containers are possible on server exit without destroy (§6.6, future orphan-GC).                                                                                         |

##### Auditor checklist (Docker, filled)

| Check                                                      | Result                                                                                                               |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Immutable `Isolation` hardening (read-only rootfs, cap-drop ALL, no-new-privileges, seccomp, non-root)? | ✅ Enforced by the server, cannot be weakened by caller or profile (§7.1). |
| Host-path gate (`ALLOW_DIRS`, S04) + symlink safety?       | ✅ `EvalSymlinks` + open-then-verify + `O_NOFOLLOW`; `ALLOW_SYMLINKS` default `false` (§7.2).                        |
| No shell/SQL/URL concatenation (S11)?                      | ✅ Fixed `[<shell>,"-c",command]` wrapper; no user string interpolated into a shell string (§7.5).                  |
| Secrets out of argv/env/image (S02)?                       | ✅ Redaction + tmpfs `0600` secret files (§7.3); `command`/`env`/`summary` disclaimer recorded.                     |
| Resource budgets?                                          | ✅ cgroup quotas (`Memory`/`NanoCPUs`/`PidsLimit`/`Ulimits`), profile-overridable but never below hardening (§7.1).  |
| Privilege model (scoped root only)?                        | ✅ `exec -u 0` bounded by immutable block; never `--privileged` (§7.10).                                             |
| Docker socket access confined?                             | ✅ Documented known limitation (§7.6); never exposed to output/tools.                                                |
| Cleanup explicit + idempotent?                             | ✅ Explicit `destroy`; `AutoRemove=true`; idempotent (X02); no automatic GC (§6.6).                                  |

**Verdict (Docker S14):** the Docker MVP is the **security target** — a
kernel-escape boundary with hardened defaults. Residual dominant risks are **D1**
(kernel escape — mitigate toward gVisor/runsc, §7.7) and **D5** (supply chain —
mitigate with digest-pinning, §7.9). The Docker path is the supported, stronger
isolation mechanism; the darwin backend below is strictly weaker and must not be
presented as equivalent.

#### Trust zones (darwin)

| Zone                                                         | Trust                          | Notes                                                                                                                                                                                    |
| ------------------------------------------------------------ | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Model-host caller**                                        | **Untrusted**                  | Supplies only a typed `SandboxSpec`; can never supply SBPL text, weaken the immutable `Isolation` block, or widen `ALLOW_DIRS` (§6.5/§7.1).                                              |
| **Operator profile author**                                  | **Trusted**                    | Authors the read-only `seatbelt.sbpl` tail, appended **verbatim** (last-match-wins); can re-open any surface the server baseline denies — **including `(allow default)`** (§7.11/§7.12). |
| **`sandbox-exec` child**                                     | **Untrusted**                  | A possibly-hostile command runs as a plain host process under an SBPL policy; the whole host is its trust boundary.                                                                      |
| **Host kernel / Mach / IPC**                                 | **Shared, not isolated**       | No namespaces; the child shares the kernel and Mach namespace with every other host process.                                                                                             |
| **Apple runtime (`sandbox-exec`/`sandboxd`/`sandbox_init`)** | **Deprecated, not guaranteed** | Formally `API_DEPRECATED`; not guaranteed by Apple as a security boundary for hostile code.                                                                                              |

#### Threat vectors (darwin, T1–T8)

| ID     | Threat                                                                   | darwin exposure                                                                                                                                                                                                                                           |
| ------ | ------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **T1** | **Kernel-escape / SBPL bypass**                                          | **HIGH.** No kernel isolation — an SBPL bug or kernel vuln is **full host compromise** at the operator's privilege, not a root boundary. This is a policy-based, not kernel-escape, containment (§7.7 target is gVisor/runsc).                            |
| **T2** | **Resource exhaustion (DoS)**                                            | **HIGH.** Only soft per-process `setrlimit` (no cgroups); a runaway process can degrade the whole host. `NanoCPUs`/`Memory`/`PidsLimit` unsupported.                                                                                                      |
| **T3** | **`no-new-privileges` / hardening bypass**                               | **HIGH (documented gap).** `spec.Validate` requires `NoNewPrivileges`/`ReadOnlyRootfs`/`CapDrop=ALL`/`TmpfsWorkdir`/non-root, but the darwin backend has **no mechanism** to enforce them (`SysProcAttr` is `Setpgid` only). Only `euid != 0` is refused. |
| **T4** | **Privilege / identity**                                                 | **MED.** No uid change (`UIDGID` not applied) — the child shares the host user's identity and its real access (only SBPL file rules constrain it).                                                                                                        |
| **T5** | **Confused deputy (Mach/IPC)**                                           | **MED.** Shared Mach/IPC namespace; an allowed `mach-lookup` to a privileged service cannot distinguish a sandboxed caller. `mach-lookup` must be minimal.                                                                                                |
| **T6** | **Network exfiltration / reach-in**                                      | **MED-HIGH when `(allow network-outbound)` is granted.** No destination filtering on macOS; combined with `(allow default)` a compromised process can exfiltrate or reach internal services.                                                              |
| **T7** | **Supply-chain / image**                                                 | **N/A (no image).** No image/digest boundary (§7.9 not applicable); SBPL executes on host libraries. The risk shifts entirely to an over-broad operator profile.                                                                                          |
| **T8** | **Operator-profile over-breadth (`(allow default)` boundary reduction)** | **HIGH (explicit).** Because the operator tail is appended last, `(allow default)` re-opens mach-lookup/sysctl/iokit/ipc/network/syscall; the **file** scope is the only real isolation in that model. `network-outbound` cannot be destination-limited.  |

#### Data-sensitivity (darwin, C-I-A)

| Property            | Assessment                                                                                                                                                                                                                                                 |
| ------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Confidentiality** | Weakened: no kernel isolation, no uid change, shared host user identity; the SBPL **file** scope is the only confidentiality control. Secrets are never placed in argv/SBPL (S02) but the process shares the host user's view of anything not file-denied. |
| **Integrity**       | Weakened: a compromised child can write anywhere the SBPL allows (a `cwd.mode=rw` or host `rw` mount) and, without kernel isolation, tamper with host-visible state at operator privilege if SBPL is bypassed.                                             |
| **Availability**    | Weakened: no resource quotas; explicit-only cleanup means orphaned children are possible on server exit without destroy (§8).                                                                                                                              |

#### Auditor checklist (darwin, filled)

| Check                                                      | Result                                                                                                               |
| ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Deny-by-default SBPL baseline?                             | ✅ `(deny default)` always emitted first (§7.12).                                                                    |
| All allowances operator-authored?                          | ✅ No implicit `allow`; operator tail appended last.                                                                 |
| Operator SBPL validated?                                   | ⚠️ **Verbatim append — only NUL rejected.** Operator is trusted (§7.11); metachar validators do not run on the tail. |
| SBPL injection on typed paths?                             | ✅ `ValidatePath`/`ValidateMachName`/`ValidateSysctlName` fail-closed, `errors.Is(..., ErrSBPLMetachar)` (§7.12).    |
| Single host-path gate (`ALLOW_DIRS`, S04)?                 | ✅ Gates every mount source + cwd; paths outside are silently skipped.                                               |
| Secrets out of argv/SBPL (S02)?                            | ✅ Redaction + per-session `0700` secret dir (no tmpfs).                                                             |
| Non-root / no root (S07-analog)?                           | ✅ `euid != 0` refused; but **no uid change** (host-user identity).                                                  |
| `no-new-privileges` / cap-drop / read-only-rootfs / tmpfs? | ❌ **Not enforced on darwin** (no mechanism; see T3).                                                                |
| Resource budgets?                                          | ⚠️ Soft `setrlimit` only; no cgroup quotas (T2).                                                                     |
| Network isolation?                                         | ⚠️ `network-outbound` deny/allow only; no destination filtering, no namespaces (T6).                                 |
| Deprecated runtime acknowledged?                           | ✅ `sandbox-exec`/`sandbox_init` deprecated, not guaranteed (see trust zones).                                       |
| Cleanup explicit + idempotent?                             | ✅ Kill whole process group `SIGKILL`; idempotent (X02); explicit-only (orphan risk, §8).                            |

**Verdict (S14):** the darwin backend is a **policy-based containment
convenience**, strictly weaker than the Docker MVP. It is **not** a
kernel-escape or multi-tenant boundary and **MUST NOT** be presented as
equivalent to Docker/gVisor isolation. The dominant residual risks are **T1**
(kernel escape), **T3** (unenforced hardening fields) and **T8** (operator
`(allow default)` boundary reduction). Any hardening work on this backend
should prioritize closing T3 (enforce what the immutable `Isolation` block
declares) and constraining the operator tail (T8).

#### External / non-auditable drivers — trust zone & threat model (S14, Security D/F5, HIGH)

This is a **design-direction trust zone** for the driver-registry design (§6.11)
— it does **not** describe shipped behavior. The moment an **external, non-
auditable driver** is admitted (Phase 1 Go plugins, Phase 2 IPC, §6.11), it adds
a new trust zone and threat surface. **This is the key distinction:** the core
can re-assert the *data-plane* (declared `Isolation` fields) but the
*mechanism-plane* is **self-attestation** and **unaudited** — a driver can lie
(§6.11 F1/F2).

##### Trust zones (external drivers)

| Zone | Trust | Notes |
| ---- | ----- | ----- |
| **External driver process / plugin** | **Trusted for mechanism, self-attested** | The driver *claims* to enforce the declared isolation; the core **cannot audit** that claim. Data-plane fields are re-asserted (§6.11 F1), mechanism-plane is self-attestation (unaudited). |
| **Driver manifest** | **Self-attestation, unverifiable** | The core cannot distinguish a truthful manifest from a lying one (§6.11 F1). |
| **Driver binary** | **Digest/signature-verified** | Tamper-controlled via manifest digest/signature before load (§6.11 E/F6, analog of §7.9). |
| **Driver IPC/stream** | **Untrusted bytes** | Every driver-originated byte is ANSI-sanitized (S09) and redacted (S02) server-side before reaching client/log/stream (§6.11 C). |
| **Sandbox container/process** | **Untrusted** | Same as the Docker/darwin sandbox — a possibly-hostile command. |

##### Threat vectors (external drivers, X1–X4)

| ID | Threat | Exposure |
| --- | ------ | -------- |
| **X1** | **Lying self-attestation** | **HIGH.** A hostile driver declares it enforces read-only-rootfs / cap-drop / no-new-privileges / quotas but does not. The core's data-plane floor re-asserts declared fields but cannot verify the mechanism — a lie yields a REDUCED-POSTURE marker at best, and an effective hardening bypass at worst. Mitigation: treat external drivers as **not-audited**; surface the honest reduced-posture marker; prefer first-party/auditable drivers for untrusted workloads. |
| **X2** | **Sandbox → driver → server escalation** | **HIGH (Phase 2 IPC).** A compromised sandbox could compromise its driver process and, over IPC, reach the **server with server privileges** (the driver child may run with higher privileges than the sandbox). The IPC boundary must treat the driver as **untrusted bytes**, apply frame limits / fail-closed framing / re-redaction (§6.11 C), and run the driver child with the **least privilege** consistent with its job. |
| **X3** | **DoS via IPC** | **MED-HIGH.** Unbounded frames, partial/hostile streams, or a driver that floods output can exhaust server memory or CPU. Mitigation: frame size limits, fail-closed framing (negative/overflow/huge lengths, premature EOF), and server-side re-redaction/ANSI sanitization (§6.11 C). |
| **X4** | **Driver-name shadowing** | **MED.** An external driver registering a **reserved** built-in name (`docker`/`seatbelt`) could shadow/override a trusted backend. Mitigation: reserved names, **fail-closed on duplicate registration** (§6.11 B). |

##### Data-sensitivity (external drivers, C-I-A)

| Property | Assessment |
| -------- | ----------- |
| **Confidentiality** | Depends on the driver. Secrets sent to a driver (tmpfs `0600`, §7.3) must **never echo back over IPC or be logged** (§6.11 C). Driver output is re-redacted server-side (S02). |
| **Integrity** | The data-plane floor re-asserts declared `Isolation` fields; the mechanism-plane is self-attested and unaudited — a dishonest driver can undermine integrity (§6.11 F1). |
| **Availability** | DoS via IPC (X3) is bounded by frame limits / fail-closed framing; driver crash/liveness is a supervision concern (lazy spawn, §6.11 E). |

##### Auditor checklist (external drivers, to be filled at implementation)

| Check | Result |
| ----- | ------ |
| Reserved built-in names cannot be shadowed; duplicate registration fails closed (X4)? | ⏳ **Design direction** — §6.11 B. |
| Driver spec failing post-build `Validate()` is rejected, not patched? | ⏳ **Design direction** — §6.11 B/F3. |
| Data-plane hardening re-asserted via `EnforceHardening` before backend `Create`? | ⏳ **Design direction** — §6.11 G. |
| Every mount `source` gated by `ALLOW_DIRS` at the Service choke-point? | ⏳ **Design direction** — §6.11 G/S04. |
| Capability/weakness closed enum, pessimistic default, REDUCED-POSTURE marker never silently ignored / never presented as full isolation? | ⏳ **Design direction** — §6.11 F2. |
| Loader traversal-safe (`os.OpenRoot`), name validated like `profile.ValidName`; driver binary digest/signature verified before load? | ⏳ **Design direction** — §6.11 E/F6. |
| IPC: frame size limits, fail-closed framing, every driver byte ANSI-sanitized + redacted, `request_id` per frame, cancel/abort frame, secrets never echo? | ⏳ **Design direction** — §6.11 C (Phase 2). |
| Driver spawn / `Available()` stays lazy (never at startup)? | ⏳ **Design direction** — §6.11 E/A02. |
| Sandbox→driver→server escalation (X2) mitigated via least-privilege driver child + untrusted-bytes IPC? | ⏳ **Design direction** — §6.11 C. |

**Verdict (S14, external drivers):** an external, non-auditable driver is a
**trusted-for-mechanism / self-attested** component. The core guarantees only a
**data-plane floor** (re-asserted declared fields + honest REDUCED-POSTURE
marker); the **mechanism-plane is unaudited**. The dominant residual risks are
**X1** (lying self-attestation) and **X2** (sandbox→driver→server escalation via
IPC). External drivers should be treated as **not-audited** and used only where
that reduced trust is acceptable; the honest reduced-posture marker must never be
silently ignored or presented as full isolation (§6.11 F2, analog of §7.12).

### 7.14 Driver-registry design — settled decisions & remaining open questions (design direction — NOT yet implemented)

The design-direction in §6.11/§7.11/§7.13 was reviewed by qa, devops, sre,
developer and security (all approve-with-changes). The **settled decisions** below
are recorded for traceability; the genuinely **open questions** remain for the
implementation phase.

#### Settled decisions (J1–J5 + reviewer F1–F6)

1. **Delivery roadmap (J1):** **Phase 1 = in-process Go plugins**; **Phase 2
   (later) = out-of-process IPC** (STDIO child + protobuf-like encoding) opening
   other languages. The contract is defined to be realizable both ways; wire/
   framing details are deferred to Phase 2 (§6.11).
2. **Migration (J2):** the `docker:`/`seatbelt:` → `options:` migration is a
   **ONE-SHOT breaking change** — **no compat shim**. A README migration guide /
   helper documents and automates conversion (§7.11).
3. **Registry-key semantics (J3):** in the target schema `driver` is **REQUIRED**
   and **`auto` / empty is REMOVED** — no typed block to auto-resolve.
   `Service.resolveDriver` remains **only for internal/built-in compatibility**,
   not for stored profiles (§7.11).
4. **Capability vs. weakness (Security F2):** **closed enum** of isolation
   guarantees; **pessimistic default** (missing = "not guaranteed"); a declared
   weakness produces an honest **REDUCED-POSTURE marker** (propagates to tool
   output, never silently ignored, never presented as full isolation);
   **fail-closed-reject** for data-plane fields the core can enforce (§6.11).
5. **Fail-closed loading (Security B/F3):** a driver spec failing post-build
   `Validate()` is **rejected**, not patched; **reserved built-in names**
   (`docker`/`seatbelt`) cannot be shadowed; duplicate registration fails closed
   (§6.11).
6. **Enforcement = data-plane only (Security A/F1):** for external non-auditable
   drivers the core re-asserts only declared `Isolation` fields; the
   mechanism-plane is **self-attestation / unaudited**. `EnforceHardening` is the
   single core-owned choke-point, and the `ALLOW_DIRS` mount gate is lifted to
   the Service level (§6.11 G).
7. **Loader / tamper (Security E/F6):** traversal-safe loader (`os.OpenRoot`,
   name validated like `profile.ValidName`), driver binary digest/signature in
   the manifest (analog of §7.9), lazy spawn/Available (A02/N35) (§6.11).
8. **IPC/stream boundary (Security C/F4):** frame size limits, fail-closed
   framing, every driver byte ANSI-sanitized + redacted server-side, `request_id`
   per frame, cancel/abort frame for `req.Timeout` wire deadline, secrets never
   echo (§6.11 C).
9. **New trust zone (Security F5):** "external drivers" trust zone added to the
   §7.13 S14 audit (lying self-attestation, sandbox→driver→server escalation,
   DoS via IPC, name shadowing; X1–X4) with an auditor checklist (§7.13).
10. **Role alignment (QA P1):** `List` is an **optional reconciliation seam**
    (not a single-sandbox primitive); `Delete` → **`Destroy`** (§6.11 F).
11. **`Driver()` vs `Slug()` (QA P2):** distinct concepts; a future `Slug()` is a
    stable, allowlist-validated namespace token, not conflated with `Driver()`
    (§6.11 I; §7.8).
12. **Testability (QA P1/H):** seams documented (registry / loader / IPC codec /
    supervision / enforcement floor); the security floor is a pure function
    testable without a concrete driver (fake-driver pattern) (§6.11 H).

#### Remaining open questions (implementation phase)

1. **Wire/encoding (Phase 2):** exact encoding of the IPC stream. **Settled:** it
   is a **STDIO child + protobuf-like** encoding, and the **process boundary is a
   STDIO child** (not necessarily a long-lived daemon). **Open:** the concrete
   **framing** (length-delimited vs. other) and the exact field/error schema
   mapping `Create`/`Destroy`/`Exec`/`Info`/`Stats`/`Available`.
2. **Driver supervision (Phase 2):** how a crashed/stuck driver child is
   detected, restarted or failed — while keeping `Available()`/spawn lazy
   (A02/N35) and preserving fail-closed framing (§6.11 C/E).
3. **Go-plugin ABI (Phase 1):** the concrete in-process Go-plugin interface /
   ABI for Phase 1 (how a plugin registers its `DriverBuilder`, version/platform
   coupling constraints for in-process loading).

These three remain open because they are implementation-stage details deferred
by the settled roadmap (§6.11); none changes the driver contract itself.

---

## 8. Data & state

- **State model (X01):** declared **stateful, per-session, in-memory**. State is
  the set of live sandbox sessions in the session manager's process-local map.
  It is **not** persisted (ephemeral by design; tied to the model-host process
  lifetime). No file/DB, no encryption-at-rest, no schema migration (X04 is not
  applicable).
- **Write idempotency (X02):** `sandbox_create` = search-before-create;
  `sandbox_destroy`/`destroy_all` = naturally idempotent (no-op on missing).
  `idempotentHint:
true` is declared only where such a mechanism exists.
- **Upstream connection & limits (X03):** every Docker API call carries an
  explicit timeout/context — for `sandbox_exec` the optional `req.Timeout`
  (seconds) is applied as a `context.WithTimeout` deadline so the command
  timeout becomes a **cancellation** reaching both the Docker API calls and the
  progress sink (§6.10; `timeout == 0` ⇒ no deadline). On failure the error is
  **returned to the model and logged** (no secrets) — **no silent retry/backoff**.
- **Concurrency (X05):** per-session mutex serializes reads/writes; no
  partial/corrupt state; verified with `go test -race`.

---

## 9. Logging

**Logging library:** `logrus` v1.10.2 (**L01GO**).

- **Channel by transport (L01):** Local / stdio → logs go to a **file**
  (`LOG_FILENAME`, `chmod 600`), **never stdout**. (HTTP mode would write to
  stdout — not applicable here.)
- **Mode-dependent enablement (L02):** logging is **enabled only when
  `LOG_LEVEL` is set**; unset ⇒ disabled.
- **`LOG_FILENAME` (L03)** overrides the path; **`LOG_FORMAT` (L04)** = `text`
  (default, full absolute timestamp) or `json`.
- **No secrets in logs (L05):** redaction driven by `secret:true` annotations
  (S02).
- **Startup banner (B05/L06):** when logging is enabled, the **very first log
  line** is the banner:
  `Starting {appName}/{appVersion} (commit: {appCommit}; built at {appTimestamp}) ...`
  (metadata embedded via ldflags, B02). When logging is disabled (unset
  `LOG_LEVEL`), no banner is emitted.
- **SDK logger wiring (L07/L03GO):** the go-sdk's `slog` logger is wired into
  logrus via `ServerOptions.Logger` (an `slog` handler forwarding to logrus), so
  SDK events are visible in the server logs.
- **Access log (L08/L03GO):** every `tools/call` emits a **per-request line at
  `info`** (visible whenever logging is enabled — **no `debug` gate**) with
  structured fields `tool`, non-sensitive redacted `args`, `source` (`STDIO`),
  `duration`, `outcome` (`ok`/`error`); **no secrets**.
- **`request_id` correlation (L09/L04GO):** generated per request, carried via
  context (`WithRequestID`/`RequestIDFromContext`), threaded on every log
  record.
- **darwin driver log events (§7.12)** — same pipeline (L08/L09,
  `request_id`-correlated, `command`/paths redacted): `session_create {
session_id, pid, profile }`, `session_destroy { session_id, pid, rc,
term_signal }`, `sandbox_deny { session_id, pid, message }` (from §7.12
  policy-denial detection), and `exec_result { session_id, exit_code }`.

---

## 10. Metrics & Observability

**N/A for a Local (stdio-only) server.**

Metrics/observability (O01–O04, N32) apply **only** to Remote/Hybrid servers
that expose an HTTP transport. This server is **Local / stdio-only**: there is
no `:8080` listen address, no internal `:8081` observability address, no
Prometheus/pprof/probes, and no `LISTEN_ADDR`/`INTERNAL_ADDR` configuration. It
does not violate N32 because N32's precondition (a Remote/HTTP or Hybrid server)
does not hold.

---

## 11. CI/CD

**CI provider (R06):** GitHub Actions (native CI of the repository host).

**CI binds to the standard build-system interface (R07)** — it calls
`make <target>`, never raw `go test`/`go build` commands (N31). The Makefile
exposes:

| Target            | Purpose                                                                                                                                                                                                                  |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `build`           | Produce binary artifacts for all intended architectures (via goreleaser, **B01/B02**).                                                                                                                                   |
| `release`         | Publish real goreleaser binaries + GitHub Release on `v*` tags (**B01**) — used by the release CI job only.                                                                                                              |
| `test`            | Unit tests (`go test -race`, **C04GO**) + **coverage ≥ 95% gate** (**C01**).                                                                                                                                             |
| `e2e`             | **darwin e2e smoke tests** (`go test -tags e2e ./...`, **C04/T02/C09GO**) — runs on a dedicated macOS CI `e2e` job; excluded by the `darwin` build tag on Linux. There is **no separate `e2e-darwin` target** by design. |
| `lint`            | golangci-lint (`golangci-lint run ./...`, **C03GO**), gosec (**C05GO**), govulncheck (**C06GO**), go-arch-lint (**C07GO**).                                                                                              |
| `container-image` | **NOT declared** — Local stdio-only server produces no image (**B03/R01/N31/N17**).                                                                                                                                      |

The pipeline shape (linters → build → tests → release artifacts) is
provider-agnostic; here it is realized in GitHub Actions:

- **linters** stage: `make lint` (golangci-lint, gofmt/gofumpt, gosec,
  govulncheck, go-arch-lint).
- **tests** stage: `make test` (race + coverage ≥ 95%), plus the **hard-gate**
  jobs: **gremlins** mutation testing `--threshold-efficacy=80
--threshold-mcover=80` (C02/C08GO) and **gitleaks** secret scan over git
  history (C03/N28). These are dedicated hard-gate jobs, not part of the make
  interface.
- **e2e** stage: a **dedicated macOS CI `e2e` job** runs `make e2e` (`go test
-tags e2e ./...`) — the **darwin e2e smoke suite** (`//go:build e2e && darwin`)
  spawns a live `/usr/bin/sandbox-exec` and tails the unified log on a real
  macOS runner; on Linux the `darwin` build tag excludes it.
- **build** stage: **dedicated `build` CI job** running `make build` (goreleaser
  snapshot → single binary per platform, ldflags-embedded build metadata **B02**).
- **release artifacts:** for a Local stdio-only server there is **no container
  image** to publish (B03/R01); a **dedicated `release` CI job** runs `make
release` on every `v*` tag to publish binary release artifacts via goreleaser
  (**B01/B02**).

> **e2e (C09GO / N07GO / T02 / C04) — scoped: darwin e2e exists; Docker-backed
> e2e remains N/A.** There **is** now a darwin **e2e smoke suite**: files guarded
> by `//go:build e2e && darwin` run under the generic **`make e2e`**
> (`go test -tags e2e ./...`) on a **dedicated macOS CI `e2e` job**. On Linux
> the darwin e2e is excluded by the `darwin` build tag; there is **no separate
> `e2e-darwin` target** by design (darwin e2e rides the generic `make e2e` on
> macOS runners). **Docker-backed e2e remains out of scope / N/A**: a meaningful
> Docker e2e would have to drive the real Docker daemon from inside a sandbox,
> which conflicts with the immutable `Isolation` hardening (Docker-in-Docker
> needs a nested/rootless daemon or socket access and is not part of the MVP).
> The full tool-handler → Docker-backend path is covered by unit tests (coverage
> gate C01 ≥ 95%) and the `go-docker-testsuite` harness is **not** a dependency.
> N30 is not violated: e2e tests exist only under the `e2e && darwin` tag and
> are run only on macOS runners where they are applicable.

**Coverage gate (C01):** ≥ 95%, fails the build below.
**Mutation gate (C02/C08GO):** gremlins hard gate at 80/80.
**Secret scan (C03):** gitleaks over full git history; findings fixed, not
suppressed.

**CI never mutates repository state (N36):** CI is **read-only** over the
working tree — no auto-commits, no pushing commits/tags back, no writing to
tracked files. Write-side effects are **external publications only** (release
binaries on GitHub Release). For this Local server there are no registry image
publications.

**Go / dependency currency (C05/N33):** pinned to latest stable — **Go 1.27.2**,
**go-sdk v1.8.0**, **logrus v1.10.2** (pinned versions come from the external
`mcp-server-spec` skill's `reference/versions.md` — not a repo file).
**resty.dev/v3** is **not** a current dependency: the server performs no
outbound HTTP (L02GO applies only when genuine remote HTTP is introduced), and
resty would be added only for a future backend that requires it.

**TDD workflow (T01):** all fixes/features are done TDD-style — **@qa** writes
tests first (isolated context), **@developer** writes the implementation
(isolated context).

### 11.1 Outbound-HTTP / Docker client choice (L02GO)

The fleet rule **L02GO** requires outbound HTTP via `resty.dev/v3`, never a
low-level `http.Client{}`/`http.Get`. **This server has no general outbound HTTP
service calls.** Its only "backend" is the **local Docker daemon over a unix
socket**, accessed through the **official `github.com/docker/docker` SDK**.

- **Rationale (recorded):** `resty` is not a fit for a **unix-socket** Docker
  transport (it targets HTTP(S) endpoints and retry semantics we explicitly
  must not build). The official **Docker SDK wraps its own HTTP transport over
  the unix socket** and provides the typed exec/container API we need. We use
  the official SDK rather than a raw `http.Client`, so **N05GO is not violated**
  (we are not hand-rolling low-level `http.Client` against a remote service).
- If a future backend ever required genuine outbound HTTP to a remote service,
  that path would use `resty.dev/v3` per L02GO. No such path exists in the MVP.

---

## 12. Docs

- **SPEC.md and AGENTS.md** are **strictly English** (D02/N04).
- **README.md** is **English** by default (D01/N05), begins with the
  **AI-Generated Content disclaimer** as its first line (D03/N11) and includes
  the **required badge set** under the title (D04/N12).

---

## 13. Conformance summary (MUST / MUST-NOT)

**MUST satisfied:** M01–M07, C01, C02, C04, C05, T01, T02, C01GO–C09GO,
S01–S12, **S14**, R02, R05, R06, R07, B01, B02, B05, L01–L09,
L01GO–L04GO, A01, A02, D01–D04, X01, X02, X03, X05, C03.

**Explicitly not applicable (documented):**

- **O01–O04/N32** — Local stdio-only server: no HTTP, no observability
  endpoint.
- **R01/R03/R04/B03/B04/N14/N17/N18** — no container image for a Local server.
- **M03 OAuth2** — no upstream HTTP API to authorize against (justified).
- **X04** — stateful-but-in-memory; no persistence/migration.
- **C04/T02/C09GO** — satisfied by the **darwin e2e smoke suite** (`//go:build
e2e && darwin`, `make e2e` on a dedicated macOS CI `e2e` job, §11); listed as
  MUST-satisfied above (was previously grouped under "e2e N/A").
- **N07GO/N30 (Docker-backed e2e) — N/A / not violated.** The
  **Docker-in-Docker e2e** remains out of scope (driving the real Docker daemon
  from inside a sandbox conflicts with the immutable `Isolation` hardening); the
  full tool-handler → Docker-backend path is covered by unit tests + the C01
  coverage gate (§11). N30 is not violated: e2e tests exist only under the
  `e2e && darwin` tag and are run only on macOS runners where they are
  applicable.
- **S06/S07/S10/S13** — not applicable: public GitHub.com repo (S06); every
  tool is a closed-world `openWorldHint:false` tool (S07/S10); Go is not a
  high-risk TS/JS ecosystem (S13).

**MUST NOT respected:** N01–N13, N15–N36 as applicable (notably N31 — no
undeclared optional Makefile targets; N36 — CI read-only; N05GO — official
Docker SDK, not low-level `http.Client`).

> **§7.12/§7.13 note:** the darwin/`sandbox-exec` driver section does **not**
> change any existing MUST/NOT; it records an **additional** contract for the
> macOS-only backend with a strictly reduced threat model (policy-based
> containment, not kernel isolation). **§7.13** now carries the consolidated
> **S14 domain security audit for both backends** — a filled Docker-audit block
> (trust zones, D1–D6 vectors, C-I-A, auditor checklist) and the darwin audit
> (T1–T8) — satisfying **S14/N38** (recorded trust boundaries + concrete threat
> model + data-sensitivity classification + filled auditor checklist for the
> actual domain).
