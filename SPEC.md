# SPEC — mcp-sandbox

> Status: **draft v1** — first pass for user review.
>
> This document is **strictly English** (D02). It is the application-level
> architecture and conformance reference for the **mcp-sandbox** MCP server.
> Every section carries the criteria codes it satisfies so a reviewer can trace
> a `MUST` back to a section. Codes resolve against
> `reference/criteria/common.md` and `reference/criteria/go.md` of the
> `mcp-server-spec` skill; pinned versions come from `reference/versions.md`.

---

## 1. Overview

**mcp-sandbox** is a **Local (stdio-only)** MCP server that gives a model-host
client (e.g. **opencode**) a safe, isolated environment to run arbitrary
commands — a **replacement for the host shell**.

The model-host does **not** use its own host-shell. Instead, for every user
session it drives:

- a **per-session, isolated Docker container** (a *sandbox*),
- with the **required directories mounted in** (e.g. the working directory
  `cwd`),
- where **arbitrary commands are executed inside** the container, and
- which is **ephemeral** and per-session — the container is destroyed
  **explicitly** by `sandbox_destroy` / `sandbox_destroy_all` (there is **no
  automatic GC**; see [§6.6](#66-session-lifecycle)).

The **top priority is security**: the sandbox is a drop-in replacement for the
host shell, so it must be *more* restrictive than the host, never less. All
hardening (see [Security](#7-security)) is **enforced by the server** and
encoded in an **immutable `Isolation` block** that a caller cannot weaken.

### Scope

| Scope | Status |
|-------|--------|
| **Docker backend** | ✅ **MVP — implemented** |
| Apple Container (and other backends) | ⏳ **Future extension** — the `Backend` abstraction is designed for it (see [Architecture](#6-architecture)) but is **not** implemented in the MVP. An **experimental macOS `sandbox-exec` (Seatbelt) driver** is **implemented (portable core wired + darwin `log stream` tailer; darwin e2e run via `make e2e` on macOS)** — see [§7.12](#712-apple--sandbox-exec-driver-darwin-backend--experimental). |
| `CapStream` / `Mountable` capabilities | ⏳ Future — **not** implemented in the MVP |

The rationale for Docker-first: *"Docker is everywhere"* — it is the most
widely available container runtime and the fastest path to a safe MVP.

**Language:** Go (**C02GO**), pinned to the latest stable **Go 1.27.2**
(**C01GO**; see `reference/versions.md`). Official MCP SDK: **go-sdk v1.8.0**
(**M01**).

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
consequences of *Local / stdio-only*:

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

| Tool | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description |
|------|----------------|-------------------|------------------|-------------|
| `sandbox_list` | `true` | `false` | `true` | List sandbox sessions (id, backend, image, created, state). |
| `sandbox_info` | `true` | `false` | `true` | Detailed spec + state of one sandbox (image, mounts, resources, isolation flags, secrets redacted). |
| `sandbox_stats` | `true` | `false` | `true` | Live resource usage of one sandbox (CPU/mem/net/IO via Docker stats). |
| `profile_list` | `true` | `false` | `true` | List operator-authored sandbox profiles (name, description). **Phase 7.** Read-only over the `SANDBOX_PROFILES_DIR` store (§7.11). Output carries **only** `name`/`description` — no env values, mount sources, image, user or sudo policy is emitted (**S02**). |

**Instructions (common to READ):** read-only; never mutate the sandbox; paths
must resolve inside the sandbox scope; sensitive content is redacted per the
redaction contract before returning; never returns raw untrusted free-form
text without an `outputSchema`. `profile_list` reads from the **read-only
profile store** — it never writes, edits or deletes a profile (there are no
profile create/update/delete tools, §7.11).

### 4.2 WRITE / UPDATE tools (`readOnlyHint: false`, `destructiveHint: false`)

| Tool | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description |
|------|----------------|-------------------|------------------|-------------|
| `sandbox_create` | `false` | `false` | `false` | Create a sandbox from an **operator-authored profile** (**Phase 7**, §7.11). Takes **required** `profile` (the name of a profile in the read-only store), `session_id` (the OpenCode client session id, recorded as a container label for traceability, §7.8) and `cwd` (the model-host's session working directory, mounted per the profile's `cwd` config — 1:1 at the same absolute path by default). Optional `secrets` are delivered as tmpfs `0600` files. All per-request knobs that used to exist — `image`, `env`, `mounts`, `nano_cpus`, `memory`, `pids_limit`, `uid`, `gid`, `sudo` — are **removed**; they now come **from the profile** (§7.11). Idempotency hint `false`: creation is a distinct side-effect; duplicate creation is surfaced as an error (search-before-create still applies, X02). |
| `sandbox_exec` | `false` | `false` | `false` | Execute a command **inside** the sandbox. **Classified as write** (the container filesystem may be modified by the command). Takes **required** `id` and `command` (a scalar string, the full command line); optional `env` (a `map[string]string` of per-exec `KEY=VALUE` environment entries, literal — no shell evaluation), `summary` (a short human-readable description of the command, e.g. "run unit tests"; purely presentational — it is **not** passed to `exec.Request` and has no effect on execution; MCP clients use it to display the command in the tool-call header), plus `working_dir`, `timeout` and `user`. The `command` is executed via the profile-configured shell as a **fixed** wrapper `[ <shell>, "-c", command ]` through the Docker exec API (§7.5). Runs under the sandbox's **unprivileged user** by default; a scoped `user`/`sudo` option runs as uid 0 per `SudoPolicy` (§7.10). ANSI sanitized on output (**S09**); paths/command redacted (**S02**). Optional `timeout` (seconds) is applied via `context.WithTimeout` and becomes a cancellation that reaches the Docker calls and the progress sink (§6.10). |

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
(§7.10) — never `--privileged`. Note: the `command` is recorded as-is in the
access-log and is **not** a channel for secrets (see §7.3 S02 disclaimer).
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

| Tool | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description |
|------|----------------|-------------------|------------------|-------------|
| `sandbox_destroy` | `false` | `true` | `true` | Destroy one sandbox (idempotent — destroying a non-existent session is a no-op, X02). |
| `sandbox_destroy_all` | `false` | `true` | `true` | Destroy all sandbox sessions (idempotent). |

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

| MCP capability | Implemented | Notes |
|----------------|-------------|-------|
| **tools** | ✅ | The entire sandbox surface (§4). |
| prompts | ❌ | Out of scope. |
| resources | ✅ | **Used for `sandbox://guidance/*` self-describing playbooks only** (§5.2). File I/O **never** goes through MCP resources — files come from the image, the host, or the runtime via tools (§4.2). Unknown URIs return not-found. |
| sampling | ❌ | Out of scope. |
| roots | ❌ | Out of scope — the sandbox scope is configured by `ALLOW_DIRS`, not MCP roots. |
| logging | ❌ (as MCP logging) | The SDK logger is wired to the server's logrus logger (L07); MCP-client-visible logging is not used. |

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

  | URI | Title |
  |-----|-------|
  | `sandbox://guidance/overview` | mcp-sandbox overview (tool grouping, annotations, HITL) |
  | `sandbox://guidance/shell-restricted-redirect` | Host shell restricted — route commands through the sandbox |
  | `sandbox://guidance/sandbox-create` | Creating a sandbox |
  | `sandbox://guidance/sandbox-exec` | Executing commands in a sandbox |
  | `sandbox://guidance/filesystem-mounts` | Filesystem layout and mounts (incl. the GOCACHE/GOTMPDIR recipe) |
  | `sandbox://guidance/security` | Security model |

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
default** — **no `internal/`** visibility wrapper. Rationale for *not* using
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

Role interfaces, each implemented *by* a backend and discovered via
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
  first use (A02/N35), and maps each live **session id → owning driver** so
  `Exec`/`Info`/`Stats`/`Destroy`/`Get` route to the correct backend while
  `List`/`DestroyAll` aggregate across all of them. A single `mcp-sandbox`
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
- **Session id = backend id (not a server-generated opaque id).** The session
  manager calls the backend **first** and uses the returned id as the session
  id: for docker that is the **container id** (a sandbox is directly traceable
  to its container via `docker ps`), for darwin it is the backend-generated
  `darwin-N` id. A backend that returns an **empty** id falls back to a
  generated opaque 64-hex (`crypto/rand`) id. The post-create duplicate check
  enforces uniqueness on that id (X02; see §7.8).
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
  **no supervisor wrapper and no `AutoRemove` equivalent**. The **leader process
  pid** is the sandbox handle (analogous to label-based ownership, §7.8);
  `Create` returns the leader pid and `Destroy` kills the **whole process group**
  (`kill(-pgid, SIGKILL)`), **idempotently** (`ESRCH` → no-op). A server exit
  without explicit destroy leaves the sandboxed process tree as an **orphan**.
  `req.Timeout` (§6.10) becomes a process-group `SIGKILL` cancellation.
- **State model (X01):** **stateful, per-session, in-memory.** State lives in
  the session manager (a process-local map); it is *not* persisted to disk or a
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

| Error | Meaning | JSON-RPC |
|-------|---------|----------|
| `InvalidParams` | `inputSchema` validation failure (S08) | `InvalidParams` |
| `ErrCapabilityNotSupported` | backend lacks a role capability | tool-level structured error |
| not-found (unknown sandbox id) | session does not exist | structured "not found" |
| transient (Docker daemon unavailable) | upstream/backend failure | returned to model, logged (X03) |

Every error is logged through the unified logging pipeline (**L08/L09**), with
secrets redacted.

### 6.8 Configuration (env-driven)

Configuration is read from environment variables (via envconfig), validated at
startup (local wiring only):

| Env | Default | Meaning |
|-----|---------|---------|
| `ALLOW_DIRS` | `[]` (none — deny by default) | Permitted host directories that may be mounted into sandboxes — gates the client-supplied `cwd` **and** every mount `source`, including those declared in profiles (**S04**). Empty ⇒ no host dirs mountable. **Still required in Phase 7** (§7.11). |
| `ALLOW_SYMLINKS` | `false` | Symlink policy for host path resolution (**S04**). |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon socket (unix). |
| `LOG_LEVEL` | *(unset ⇒ logging disabled)* | Enable + level; stdio logging is on **only when set** (**L02**). |
| `LOG_FILENAME` | — | stdio log file path, `chmod 600` (**L01/L03**). |
| `LOG_FORMAT` | `text` | `text` or `json` (**L04**). |
| `SANDBOX_IDLE_TIMEOUT` | `15m` | Idle reaper window (**not yet wired** — the reaper is designed but not running; see §6.6). |
| `SANDBOX_PROFILES_DIR` | `~/.config/sandbox/profiles` | Directory of **operator-authored, READ-ONLY** YAML sandbox profiles — one `<name>.yml` per profile (**Phase 7**, §7.11). Read **on demand** on each `Get`/`List` (§7.11) — edits take effect without a restart. The source of `image`, `env`, `mounts`, `cwd`, `network`, `cgroups`, `user`, `sudo`. There is **no** profile create/update/delete mechanism. |

> **Removed configuration (Phase 7).** The following env vars were **removed**;
> their responsibility moved into the operator profile (§7.11) or the profile
> image reference:
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

| Isolation policy | Value / enforcement |
|------------------|---------------------|
| `ReadOnlyRootfs` | `true` — rootfs is read-only. |
| `CapDrop` | `ALL`, then re-add only the minimal required capabilities. |
| `NoNewPrivileges` | `true`. |
| `Seccomp` | **Default profile for the MVP**; a **custom seccomp profile is mandatory roadmap** (see §7.7). |
| User | **Non-root** inside the container (`UIDGID` / user namespaces). |
| `PidsLimit` | Set (bounded process count). |
| `Memory` / `MemorySwap` | Set and bounded. |
| `NanoCPUs` | Bounded. |
| `Ulimits` | Set. |
| `NetworkMode` | **From the operator profile** (§7.11): `bridge` (safe default) or `none`; **`host` is valid only when the operator authors it into a profile** — it is never allowed implicitly. The immutable block still bounds which modes are legal (`bridge`/`none`/`host`). |
| `tmpfs` mounts | **`/run`** tmpfs `rw,noexec,nosuid,nodev` (owned by the sandbox user; holds the `/run/sandbox` secret dir) and **`/tmp`** tmpfs `rw,nosuid,nodev` (writable, no `noexec` requested). **Platform note:** some daemons (Docker Desktop on macOS) force `noexec` on tmpfs, so `/tmp` may not be executable; runtime compilation (Go builds) then needs the writable bind-mounted `cwd` (per the profile's `cwd.mode`). |
| `AutoRemove` | `true` — container auto-removed on stop. |

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

> **⚠ command is logged as-is (S02 disclaimer).** The access-log line (L08)
> records the `sandbox_exec` `command` **as passed** — the access-log path is
> **not** run through secret-value redaction. This is a **deliberate design
> choice**: the secret model is struct-annotation-based, and `command` is a
> free-form command line for which **no** secret values are known in advance, so
> they **cannot** be reliably masked if one ever appeared there. **Contract /
> expectation:** `command` is **not** a channel for secrets — secrets are
> delivered as tmpfs `0600` files (§7.3) and must never be placed in a command
> line. Operators who may pass sensitive values in a command line should note
> that:
> - the access log is written to `LOG_FILENAME` (`chmod 600`, operator-owned); and
> - logging is **opt-in** — it is enabled **only when `LOG_LEVEL` is set**
>   (L02); with `LOG_LEVEL` unset no log file is written and no command is
>   persisted at all.

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

| Item | Status |
|------|--------|
| Custom seccomp profile | **Mandatory roadmap** (replaces the default MVP profile). |
| AppArmor profiles | Roadmap. |
| **gVisor / runsc** | **Target state** for kernel-escape containment (**T1 kernel-escape**). |
| Rootless Docker | Roadmap (reduces the §7.6 socket risk). |
| **Darwin backend (`sandbox-exec`/Seatbelt)** | **Experimental (implemented — portable core wired + darwin `log stream` tailer; darwin e2e run via `make e2e` on macOS)** — **policy-based containment, NOT a kernel-escape equivalent** (see §7.12). |
| Backend abstraction | Ready — a future `gvisor`/`apple` backend implements the same `Backend` roles. |

### 7.8 Session identifiers & ownership

- **Session id = backend id.** The session manager calls the backend **first**
  and uses the **backend's returned id** as the session id (§6.6). For the
  **docker** backend that is the **container id**, so a sandbox is directly
  traceable to its container via `docker ps`; for the **darwin** backend it is
  the backend-generated `darwin-N` id. A backend returning an **empty** id
  falls back to a generated opaque 64-hex `crypto/rand` id (the previous
  default, now only a fallback). The id is returned by `sandbox_create` as
  `id` and is the handle used by every other tool.
- **Owner label is retained; the session label is redundant.** The docker
  backend always applies the owner label (`dev.teran.sandbox.owner =
  mcp-sandbox`) so the server can identify, reap and destroy exactly its own
  containers. The per-session label `dev.teran.sandbox.session` is **now
  redundant** because the session id *is* the container id; the docker backend
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
  than a floating tag (C05/T7). A digest pins *exactly which* image is used and
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
  *provisioned* images for the MVP.

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

```yaml
name: default            # required; store key; [a-zA-Z0-9._-], no path separators
description: the default profile   # required
image: alpine@sha256:…   # required; parse-only validation (any registry; digest recommended, §7.9)
env:                     # optional
  FOO: bar
mounts:                  # optional; every source must be within ALLOW_DIRS (§7.2)
  - source: /data
    target: /data
    read_only: true
cwd:                     # optional; controls the auto session-cwd mount (§7.2)
  target: ""             # empty ⇒ 1:1 at the same absolute path (default)
  mode: ro               # ro (default) | rw
network:
  mode: bridge           # bridge (default) | none | host (host only if operator authors it)
cgroups:                 # limits; a missing value falls back to the server default (§7.1)
  cpu_cores: 1           # human-readable: whole/virtual cores (1.0 = one core); raw nano_cpus takes precedence if both set
  memory_gb: 1           # human-readable: whole GiB; raw memory/memory_swap take precedence if set; swap disabled ⇒ memory_swap = memory
  pids_limit: 256
  ulimits:
    - name: nofile
      soft: 1024
      hard: 1024
user:                    # non-root enforced; nil ⇒ server default non-root user
  uid: 1000
  gid: 1000
sudo: never              # never (default) | require
shell: bash              # optional; default bash; whitespace-separated tokens (§7.5)
```

Schema key reference (`domain/sandbox/profile.Profile`):

| Key | Type | Meaning / validation |
|-----|------|----------------------|
| `name` | string | Required. Store key. Regex `[a-zA-Z0-9._-]+`; must not be a path separator / `..` (path-traversal rejected, §7.11 loader). |
| `description` | string | Required. Human-readable summary shown by `profile_list`. |
| `image` | string | Required. Parse-only reference validation (§7.9); digest-pinning recommended. |
| `env` | map[string]string | Optional. Container environment. |
| `mounts[]` | `{source,target,read_only}` | Optional. `source` must be absolute **and within `ALLOW_DIRS`** (§7.2). |
| `cwd` | `{target,mode}` | Optional. `target` empty ⇒ mounted 1:1 at the client path; `mode` `ro`(default)/`rw`. |
| `network` | `{mode}` | Optional. `bridge`(default)/`none`/`host` — `host` only when operator-authored. |
| `cgroups` | `{nano_cpus,memory,memory_swap,pids_limit,ulimits}` | Optional. Bounded resource limits; profile overrides the server baseline; missing ⇒ server default (§7.1). `memory_swap` ≥ `memory`; values must be bounded (> 0). |
| `user` | `{uid,gid}` | Optional. Must be **non-root** (`uid != 0`); nil ⇒ server default non-root user. |
| `sudo` | string | Optional. `never`(default)/`require`. |
| `shell` | string | Optional. The shell used for the **fixed** `sandbox_exec` wrapper `[ <shell>, "-c", command ]` (§7.5). Whitespace-separated tokens (split on whitespace); NUL/newline characters rejected; empty/missing ⇒ default `bash`. Operator picks a shell available in the image. |
| `seatbelt` | `{sbpl,mach_lookups,sysctls}` | Optional. **darwin/Seatbelt only (§7.12)** — carries the operator SBPL template (merged after server-hardening ops), extra `mach-lookup` services and `sysctl` keys. Ignored by the Docker backend. Validation rejects only NUL in `sbpl`. |

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
> `/usr/bin/sandbox-exec` child and tailing the **unified log** via
> `log stream` require a real macOS host and cannot be exercised inside a
> Docker/CI sandbox, so this is now exercised by the **darwin e2e smoke tests**
> (`//go:build e2e && darwin`), which run under the generic **`make e2e`**
> (`go test -tags e2e ./...`) on a **dedicated macOS CI `e2e` job** (§11). There
> is **no separate `e2e-darwin` target** by design; on Linux the darwin e2e is
> excluded by the `darwin` build tag.

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

| Role | darwin backend | Notes |
|------|----------------|-------|
| `Backend` | ✅ | Name `darwin`, `Driver` = `seatbelt`, `Priority` 10 (docker=0 preferred). |
| `Lifecycle` (`Create`/`Destroy`) | ✅ | Spawn/kill of the sandboxed child; idempotent (**X02**). |
| `Executable` (`Exec`) | ✅ | `sandbox-exec -p <SBPL>` wrapper around the fixed `[<shell>,"-c",command]` form. |
| `Introspectable` (`Info`/`Stats`) | ❌ | **Not implemented.** No cgroups on macOS; `Info`/`Stats` return `ErrCapabilityNotSupported` (§6.2) — **no stub**. |

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
seatbelt:                    # optional; darwin/Seatbelt
  sbpl: |                    # operator SBPL merged AFTER server-hardening ops
    (import "system.sb")
    (allow process-fork)
  mach_lookups:              # extra (allow mach-lookup ...)
    - com.apple.system.logger
  sysctls:                   # extra (allow sysctl-read ...)
    - kern.osversion
```

`domain/sandbox/profile.Profile.Seatbelt` is a `*SeatbeltProfile` (SBPL,
MachLookups, Sysctls) that `BuildSpec` projects into `spec.Sandbox{SBPL,
MachLookups, Sysctls}`. Validation rejects only NUL in `SBPL`; the profile is **read-only**
and operator-authored per the §7.11 trust model. A profile that requests a
darwin-unsupported capability (see below) remains **valid** but the limit is
**honestly reported as not enforced** on this backend — never silently ignored.

#### Isolation → Seatbelt projection

The darwin backend renders a full SBPL from the typed `SandboxSpec` (order:
imports → `(deny default)` → network → mach → sysctl → file → operator
`seatbelt.sbpl`). The generator is a **deterministic pure function
`SandboxSpec → SBPL`**; a caller never supplies SBPL text, it supplies a
`SandboxSpec` exactly as for Docker:

| `Isolation` / spec | Seatbelt operation |
|--------------------|--------------------|
| always | `(deny default)` |
| `Mounts` + `cwd` (ro) | `(allow file-read* (subpath "<path>"))` |
| `Mounts` + `cwd.mode=rw` | `(allow file-read* …)` + `(allow file-write* …)` |
| `NetworkMode == none` | `(deny network*)` (default) |
| `NetworkMode == bridge`/`host` | `(allow network-outbound)` |
| `Resources.Ulimits` | `setrlimit(2)` via Go wrapper before `execvp` (`RLIMIT_NOFILE/AS/CPU/NPROC/FSIZE/STACK/CORE`) |
| `Resources.NanoCPUs/Memory/PidsLimit` | **not mappable** (no cgroups on macOS) — declared unsupported, never silently ignored |
| `UIDGID` | **not applied** (self-sandbox runs as the current host user) |
| mach | `(deny mach-lookup)` + allow-list |
| sysctl | `(deny sysctl*)` + allow-list |

> **Deny-all default — every allowance is operator-authored.** The server-rendered
> baseline for the darwin backend is a **single default `(deny default)`** — there
> is **no** implicit `allow` in the generated profile. **Every** allowance,
> including the **`(allow process-exec*)`** needed to run a command, **must be
> explicitly authored by the operator** in the profile's `seatbelt.sbpl`. Without
> a `process-exec` (and the file-read/mach/sysctl grants for the shell and its
> runtime, e.g. `/bin`, `/usr/bin`, `/usr/lib`, dyld), `sandbox_exec` cannot run
> anything — the shell itself is denied by `(deny default)` and `execvp` fails.
> This is **by design**: `deny-all` is the only default; the operator owns all
> grants. The `seatbelt` example profile above only grants `network-outbound`,
> so it is illustrative for `sandbox_create`/`sandbox_list` but **not** sufficient
> to `sandbox_exec` — add the shell-exec allowances to run commands. **This gap
> is open**: the server generator does not yet emit `process-exec*`; supplying it
> is an operator requirement, not something the server does. The darwin e2e
> (`§11`) drives `sandbox-exec` with an `(allow default)` SBPL directly,
> bypassing the server generator, so it does **not** validate exec under the
> server-rendered deny-default baseline.

**SBPL injection is a real new vector** (an S11-analog absent from the Docker
backend). No raw user/profile string is ever interpolated into SBPL; only
typed, validated absolute paths are used, and each path is rejected if it
contains any SBPL metacharacter from `" \ ( ) ;` or `\n \r \0` — otherwise a
crafted value such as `") (allow file-write*) ;"` would terminate the string
and inject an `(allow file-write*)` token. The generator is covered by
injection-focussed unit tests (fail-closed: a hostile path ⇒ `InvalidParams`).
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
  The live unified-log tail on a real macOS host is exercised by the **darwin
  e2e** (§11): it runs under `make e2e` on a dedicated macOS runner.
- **Portability (declared break).** The `sandbox-exec` backend is
  **macOS-only**; a Linux host has no `sandbox-exec` and must fall back to
  Docker (or a future `bwrap` backend). Backends are **non-interchangeable
  isolation mechanisms**, not a portable abstraction layer — backend selection
  is explicit per platform, and the absence of an available backend surfaces
  `ErrNoAvailableBackend` rather than silently degrading (§6.3).

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

| Target | Purpose |
|--------|---------|
| `build` | Produce binary artifacts for all intended architectures (via goreleaser, **B01/B02**). |
| `release` | Publish real goreleaser binaries + GitHub Release on `v*` tags (**B01**) — used by the release CI job only. |
| `test` | Unit tests (`go test -race`, **C04GO**) + **coverage ≥ 95% gate** (**C01**). |
| `e2e` | **darwin e2e smoke tests** (`go test -tags e2e ./...`, **C04/T02/C09GO**) — runs on a dedicated macOS CI `e2e` job; excluded by the `darwin` build tag on Linux. There is **no separate `e2e-darwin` target** by design. |
| `lint` | golangci-lint (`golangci-lint run ./...`, **C03GO**), gosec (**C05GO**), govulncheck (**C06GO**), go-arch-lint (**C07GO**). |
| `container-image` | **NOT declared** — Local stdio-only server produces no image (**B03/R01/N31/N17**). |

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
**go-sdk v1.8.0**, **logrus v1.10.2** (see `reference/versions.md`).
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
S01–S12, R02, R05, R06, R07, B01, B02, B05, L01–L09,
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

> **§7.12 note:** the darwin/`sandbox-exec` driver section does **not** change
> any existing MUST/NOT; it records an **additional** contract for the
> macOS-only backend with a strictly reduced threat model (policy-based
> containment, not kernel isolation).
