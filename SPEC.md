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
- which is **ephemeral** — the container is **destroyed when the session ends**.

The **top priority is security**: the sandbox is a drop-in replacement for the
host shell, so it must be *more* restrictive than the host, never less. All
hardening (see [Security](#7-security)) is **enforced by the server** and
encoded in an **immutable `Isolation` block** that a caller cannot weaken.

### Scope

| Scope | Status |
|-------|--------|
| **Docker backend** | ✅ **MVP — implemented** |
| Apple Container (and other backends) | ⏳ **Future extension** — the `Backend` abstraction is designed for it (see [Architecture](#6-architecture)) but is **not** implemented in the MVP |
| `CapStream` / `Mountable` capabilities | ⏳ Future — **not** implemented in the MVP |

The rationale for Docker-first: *"Docker is everywhere"* — it is the most
widely available container runtime and the fastest path to a safe MVP.

**Language:** Go (**C02GO**), pinned to the latest stable **Go 1.27.1**
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

- **Launch mode (M06):** selected at startup by the **`-mode http|stdio`**
  parameter; **default is `stdio`**. For a Local server the mode is effectively
  fixed to `stdio`.
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

**Instructions (common to READ):** read-only; never mutate the sandbox; paths
must resolve inside the sandbox scope; sensitive content is redacted per the
redaction contract before returning; never returns raw untrusted free-form
text without an `outputSchema`.

### 4.2 WRITE / UPDATE tools (`readOnlyHint: false`, `destructiveHint: false`)

| Tool | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description |
|------|----------------|-------------------|------------------|-------------|
| `sandbox_create` | `false` | `false` | `false` | Create a sandbox from a typed `SandboxSpec`. Takes a **required** `session_id` — the OpenCode client session id — which is recorded as a container label (`dev.teran.sandbox.opencode_session`) for traceability (§7.8). The model-host passes its **session `cwd`**, which is mounted **1:1 at the same absolute path** (read-only by default; opt-in `rw` via `SANDBOX_CWD_MODE=rw`). **Mounts are immutable** — changing a mount requires destroying and recreating the session. Idempotency hint `false`: creation is a distinct side-effect; duplicate creation is surfaced as an error (search-before-create still applies, X02). |
| `sandbox_exec` | `false` | `false` | `false` | Execute a command **inside** the sandbox via an argument array (no shell). **Classified as write** (the container filesystem may be modified by the command). Runs under the sandbox's **unprivileged user** by default; a scoped `user`/`sudo` option runs as uid 0 per `SudoPolicy` (§7.10). ANSI sanitized on output (**S09**); paths/argv redacted (**S02**). |

> **Files come from the image, the host, or the runtime — never via MCP file-write
> tools.** The session `cwd` is mounted 1:1, so the model-host writes files on the
> **host** and the container reads/executes them at the same path; runtime
> artifacts live in the writable tmpfs `/tmp` (executable where the daemon
> honours `exec` on tmpfs; on daemons that force `noexec` — e.g. Docker Desktop —
> compilation goes into the writable bind-mounted `cwd`). Therefore the MVP exposes **no**
> `write_file`/`copy_in`/`copy_out`/`read_file`/`install_package` tools — **mount +
> exec is the core mechanism** (see §7.2). cgroup/namespace knobs and
> Docker-in-Docker are deferred (see TODO "Deferred").

**Instructions — `sandbox_exec`:** pass command and args as an **array**, never
a shell string; do **not** log the argv in full (redact paths/values); output
is ANSI-sanitized; runs under the sandbox user with `no-new-privileges`; only
allowed capabilities are available. A scoped `user: root` / sudo-classed exec
runs as uid 0 per the sandbox's `SudoPolicy` (§7.10) — never `--privileged`.

### 4.3 DELETE tools (`destructiveHint: true`, HITL)

| Tool | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Description |
|------|----------------|-------------------|------------------|-------------|
| `sandbox_destroy` | `false` | `true` | `true` | Destroy one sandbox (idempotent — destroying a non-existent session is a no-op, X02). |
| `sandbox_destroy_all` | `false` | `true` | `true` | Destroy all sandbox sessions (idempotent). |

### 4.4 Human-in-the-loop for destructive tools (S12)

`destructiveHint: true` tools (`sandbox_destroy`, `sandbox_destroy_all`) are
**human-confirmable/deniable**. There is **no automatic cascading** of
destructive calls.

Because the server is **Local / stdio-only** and has **no HTTP UI** (S12 /
`-mode stdio`), HITL is implemented **client-side**: the model-host (opencode)
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
| resources | ❌ | Out of scope. Files are read/written via tools, not MCP resources. |
| sampling | ❌ | Out of scope. |
| roots | ❌ | Out of scope — the sandbox scope is configured by `ALLOW_DIRS`, not MCP roots. |
| logging | ❌ (as MCP logging) | The SDK logger is wired to the server's logrus logger (L07); MCP-client-visible logging is not used. |

Per-tool **Instructions** are preferred over server-level instructions and are
exposed per tool (M04).

### 5.1 Streaming output via `notifications/progress`

Long-running tools — starting with `sandbox_exec` — can stream live output and
call parameters to the model-host via the MCP **`notifications/progress`**
notification while the command runs. This is **additive**: the client opts in by
passing a **progress token**; without one, tool behavior (and the final
`Result`) is **byte-for-byte unchanged** and no progress notifications are
emitted.

**Streaming contract** (`sandbox_exec` and any long-running tool):

1. **`start`** — emitted once when execution begins, carrying the **redacted**
   `argv` so the model-host sees the actual call parameters that are running.
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
cmd/mcp-sandbox/           composition root: flags (-mode), config load,
                           logger, banner, layer composition, transport start
domain/                    pure domain model, no infra dependencies
  sandbox/
    spec/                  typed SandboxSpec (Image, WorkingDir, Env, Mounts,
                           Resources, Network, UIDGID, Sudo, Secrets,
                           ReadOnlyRoot, Isolation), validation
    exec/                  exec request/result types, result classification
    role/                  ISP role interfaces: Backend, Lifecycle, Executable,
                           Introspectable (+ ErrCapabilityNotSupported)
    capability/            capability set types
  session/                 Session aggregate: id, spec, state, reaper, mutex
  progress/                pure-domain streaming: Event{Message, Progress, Total},
                           Sink, LineBatcher (line-buffered, no goroutine),
                           WithSink/SinkFromContext (context-threaded)
application/               use-case orchestration
  config/                  env-driven Config (ALLOW_DIRS, LOG_*), validation
  sandbox/                 sandbox service: create/use/destroy, Backend select
  session/                 session manager: registry, lifecycle, reaper
  tools/                   declarative tool framework
    def/                   ToolDef (Name/Title/Description/Annotations/
                           Instructions/InputSchema/OutputSchema/
                           RedactionContract/Handler)
    registry/              tool registry
    handler/               shared handler-wrapper: validate → access-log →
                           dispatch → redact → structured output
infrastructure/            adapters
  backend/
    registry/              backend factory registry (constructor-register)
    select/                capability-based backend selection
    docker/                Docker backend (official docker SDK, unix socket)
  mcp/                     MCP server wiring (go-sdk), SDK slog→logrus logger
  logging/                 logrus setup, channel routing, redaction
e2e/                       contract suite + docker harness (go-docker-testsuite)
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
- `Available()` is evaluated **lazily inside `sandbox_create`** — never at
  startup (**A02/N35**: no outbound calls at startup). Backend selection happens
  on the first tool call that needs a backend.

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

### 6.6 Session lifecycle

A **session** (`domain/session`) owns one sandbox and its state:

```
create → use (exec/read/write/copy/install) → destroy
```

- **Mounts are immutable** — changing a mount requires `destroy` + `create`
  (recreate). This is enforced by the session manager (X01 state model).
- **Reaper:** an idle-timeout reaper destroys sessions that have been idle
  beyond a configurable window.
- **Shutdown-teardown:** on server shutdown, `DestroyAll` destroys every
  remaining sandbox (ephemeral guarantee).
- **State model (X01):** **stateful, per-session, in-memory.** State lives in
  the session manager (a process-local map); it is *not* persisted to disk or a
  DB. Reasoning: sandboxes are ephemeral by design and tied to the lifetime of
  the model-host process that spawned the server. No persistence layer, no
  encryption-at-rest, no schema migration (X04 not applicable — declared
  stateful-but-in-memory).
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
| `-mode` (flag) | `stdio` | Launch mode (**M06/A01**). Local server is fixed to stdio. |
| `ALLOW_DIRS` | `[]` (none — deny by default) | Permitted host directories that may be mounted into sandboxes (e.g. the session `cwd`) (**S04**). Empty ⇒ no host dirs mountable. |
| `ALLOW_SYMLINKS` | `false` | Symlink policy for host path resolution (**S04**). |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker daemon socket (unix). |
| `LOG_LEVEL` | *(unset ⇒ logging disabled)* | Enable + level; stdio logging is on **only when set** (**L02**). |
| `LOG_FILENAME` | — | stdio log file path, `chmod 600` (**L01/L03**). |
| `LOG_FORMAT` | `text` | `text` or `json` (**L04**). |
| `SANDBOX_IDLE_TIMEOUT` | `15m` | Reaper idle window. |
| `SANDBOX_CWD_MODE` | `ro` | Session `cwd` mount mode: `ro` (default) or `rw` (opt-in, global feature flag, §7.2). |
| `SANDBOX_CGROUP_CPU_LIMIT_CORES` | unset (`0`) | Operator-configurable Docker cgroup CPU limit, in **CPU cores** (unit suffix `_CORES`). Translated server-side to `NanoCPUs = cores * 1_000_000_000` (**§7.1**). Unset/`0` ⇒ built-in server default applies. |
| `SANDBOX_CGROUP_MEMORY_LIMIT_GB` | unset (`0`) | Operator-configurable Docker cgroup memory limit, in **gibibytes** (unit suffix `_GB`). Translated server-side to `Memory = gb * 1024^3` bytes; `MemorySwap` is set equal to `Memory` so swap stays disabled (**§7.1**). Unset/`0` ⇒ built-in server default applies. |
| `TRUSTED_REGISTRIES` | built-in safe default | Comma-separated allowlist of registry domains the sandbox may pull images from (**REPLACE semantics**, §7.9). Unset/empty/whitespace-only ⇒ falls back to the built-in default (`docker.io`, `index.docker.io`, `registry-1.docker.io`, `ghcr.io`). |

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
| `NetworkMode` | **`bridge` by default** (Docker's default bridged network); **`host` is forbidden at policy level**. |
| `tmpfs` mounts | **`/run`** tmpfs `rw,noexec,nosuid,nodev` (owned by the sandbox user; holds the `/run/sandbox` secret dir) and **`/tmp`** tmpfs `rw,nosuid,nodev` (writable, no `noexec` requested). **Platform note:** some daemons (Docker Desktop on macOS) force `noexec` on tmpfs, so `/tmp` may not be executable; runtime compilation (Go builds) then needs the writable bind-mounted `cwd` (`SANDBOX_CWD_MODE=rw`). |
| `AutoRemove` | `true` — container auto-removed on stop. |

Because the block is immutable and applied by the server, a caller cannot pass
a spec that disables these protections.

**Operator-configurable resource limits.** The `Memory`/`MemorySwap`/`NanoCPUs`
values (rows above) are seeded from operator environment variables — see §6.8:
`SANDBOX_CGROUP_CPU_LIMIT_CORES` and `SANDBOX_CGROUP_MEMORY_LIMIT_GB`. Both
default to **unset (`0`)**, in which case the built-in server defaults apply
(currently 1 CPU, 4 GiB, swap off, `PidsLimit` 512). The unit suffixes (`_CORES`,
`_GB`) are intentional so the configuration form is self-documenting:

- **CPU:** `SANDBOX_CGROUP_CPU_LIMIT_CORES` (uint, cores) → `NanoCPUs =
  cores * 1_000_000_000`.
- **Memory:** `SANDBOX_CGROUP_MEMORY_LIMIT_GB` (uint, gibibytes) →
  `Memory = gb * 1024^3` bytes; `MemorySwap` is set **equal** to `Memory` so
  swap remains disabled.

These set the operator-level baseline only. A caller can still **tighten or
override per-request** via `memory`/`nano_cpus` on `sandbox_create` on top of
this base, but can **never weaken** the immutable block itself.

### 7.2 Filesystem scope & mounts (S04)

- **`ALLOW_DIRS []string`** — the **only** host directories permitted to be
  mounted into a sandbox (e.g. the session `cwd`). Anything outside is
  forbidden (**N03**). Empty by default (deny).
- **`ALLOW_SYMLINKS bool`** — default `false`.
  - `false` (safe default): every host path is canonicalized with
    `filepath.EvalSymlinks`, then **open-then-verify** (open the fd, `fstat`,
    compare the resolved real path against the allowed roots); reject if it
    escapes `ALLOW_DIRS`. `O_NOFOLLOW` is used on the final component.
  - `true`: following symlinks is accepted (operator explicitly opts in).
  - **Hardlinks** cannot be prevented by a path check (a hardlink is another
    name for the same inode); this is mitigated at the OS level (Linux
    `fs.protected_hardlinks`, separate bind-mount for the allowed dir, immutable
    dirs) and is documented as an OS-level concern, not something the path check
    enforces.
- **Mount policy:** only **explicit `ALLOW_DIRS`** may be mounted; **read-only
  by default**; **minimize the number of paths**; **`/` and secret directories
  are forbidden**.
- **Minimize bind-mounts:** only the session `cwd` (and explicit `ALLOW_DIRS`)
  are mounted; file transfer is **host-side** (the model-host writes into the
  mounted cwd), and runtime writes happen **only into tmpfs** (not into
  bind-mounted host paths). This reduces the host surface a compromised sandbox
  could reach.
- **Session working-directory mount (1:1):** `sandbox_create` accepts the
  model-host's **current working directory** and mounts it at the **same
  absolute path** inside the container — host `/Users/myuser/Downloads/blah` →
  container `/Users/myuser/Downloads/blah` — with **no path rewriting**. The
  mounted cwd must be **within `ALLOW_DIRS`** (or explicitly authorized). It is
  **read-only by default** (the model-host writes into the mounted cwd on the
  host; the container reads/executes), with an **opt-in `rw`** per-sandbox flag
  (`SANDBOX_CWD_MODE=rw`) for cases where the container must write artifacts
  into the cwd during command execution. This is the one sanctioned bind-mount
  for the model-host use case so the model operates on the session's files with
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
- **Do not log the full `argv`** of `sandbox_exec` — redact paths/values while
  keeping the access-log shape (L08).
- **No secret leakage** in any output or log (**N02/L05**).
- **Streaming output is redacted too** (§5.1): the same redaction layer applies
  to the `notifications/progress` chunks emitted while a command runs, so no
  secret leaks through the live-streaming channel.

### 7.4 Strict input & output validation (S08/S09)

- Every tool validates arguments against `inputSchema`
  (`additionalProperties: false`, strict types, limits) **before** execution;
  rejects with `InvalidParams` (**S08/N22**).
- Output conforms to `outputSchema`; **ANSI/control escape sequences are
  filtered** in text output (**S09/N23**) — `sandbox_exec` output passes through
  ANSI sanitization. The same sanitization is applied to **streamed** output
  before it is emitted via `notifications/progress` (§5.1).

### 7.5 No shell / SQL / URL concatenation (S11 / N21)

- **Commands are executed as argument arrays — no shell** (`sh -c` concatenation
  is banned). `sandbox_exec` runs the command directly inside the container via
  the Docker SDK exec API; no user string is interpolated into a shell string.
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
| Backend abstraction | Ready — a future `gvisor`/`apple` backend implements the same `Backend` roles. |

### 7.8 Session identifiers & ownership

- `session_id` is generated with **`crypto/rand`** — **opaque**, unpredictable.
- Ownership uses **label-based ownership** in Docker (containers are labelled
  with the session id) so the server can identify, reap and destroy exactly its
  own containers.
- **OpenCode session id (`session_id`):** `sandbox_create` takes a **required**
  `session_id` parameter carrying the OpenCode client session id that created
  the sandbox. The backend records it as a dedicated container label
  (`dev.teran.sandbox.opencode_session`, set only when non-empty) so each
  sandbox container is traceable to the OpenCode session that created it. This
  is **distinct** from the internal sandbox session id (the opaque `crypto/rand`
  id above, returned by `sandbox_create` as `id` and never passed as a tool
  parameter). The required-ness is enforced at the tool input boundary; the
  domain-level `SandboxSpec.ClientSessionID` is optional so internal callers and
  e2e specs that build a spec directly can omit it.
- Each session has a **per-session mutex**; `destroy` is **idempotent** (X02).

### 7.9 Supply-chain (T7 / C05 / S05)

- **Trusted-registry allowlist is enforced in code** (`validateImageRef` in the
  docker backend, applied on every `sandbox_create` before any Docker
  side-effect): an image reference is accepted only if it parses as a valid
  reference and its registry is in the allowlist. Images from
  arbitrary/untrusted registries are **rejected**.
- **Configurable allowlist (`TRUSTED_REGISTRIES`):** the allowlist is no longer
  hardcoded — it is read from the `TRUSTED_REGISTRIES` environment variable
  (see §6.8) as a comma-separated list of registry domains. It uses **REPLACE
  semantics**: when set, the variable **entirely replaces** the allowlist; when
  unset, empty, or whitespace-only, it **falls back** to the built-in safe
  default — `DefaultTrustedRegistries` =
  `docker.io, index.docker.io, registry-1.docker.io, ghcr.io`.
- **Matching & validation:** each configured entry is matched **exactly** against
  the normalized registry domain of the image reference, taken from
  `reference.Domain(reference.ParseNormalizedNamed(...))`. Entries are
  **lowercased** and **deduplicated**. Entries are rejected when they contain a
  wildcard/glob (`* ? [`), a scheme (`://`/`//`), internal whitespace, or any
  character outside `[a-z0-9._:-]`; a colon is permitted **only** as an explicit
  port (e.g. `registry.example.com:5000`). Validation is **fail-closed** — a
  malformed or ambiguous value errors at startup; there is **no allow-all**
  escape hatch.
- **`registry-1.docker.io` is not normalized** to `docker.io` by the parser, so
  it must be listed as a **separate explicit entry** if it is to be trusted. The
  built-in default includes it for this reason.
- **Operator posture:** because the variable **replaces** the default, an
  operator who sets `TRUSTED_REGISTRIES` to enable a private registry must list
  any public registries they still want **explicitly** alongside it (e.g.
  `TRUSTED_REGISTRIES=registry.homelab.teran.dev, docker.io, ghcr.io`). There is
  no implicit union with the default.
- **Auto-pull of a trusted-but-missing image**: on `sandbox_create`, if the
  (already validated) image is not present in the local cache the backend pulls
  it from its trusted registry before creating the container. The pull only
  ever happens **after** `validateImageRef` has accepted the reference, so an
  untrusted image is never pulled. A create for an already-local image issues
  no pull. The pull stream is drained and discarded — image content is never
  logged (L05) and carries no secrets.
- **Digest-pinning is the recommended deployment posture**: images **should**
  be referenced by digest (`image@sha256:…`) rather than a floating tag, and
  this is the documented production guideline (C05/T7); the hard enforcement
  boundary is the trusted-registry allowlist. Digest-pinning remains the
  **strongest** posture even on a trusted registry — the allowlist scopes *where*
  images may come from, while a digest pins *exactly which* image is used.
- Dependencies are pinned to the **latest stable** versions (C05) and
  **verified**; installed packages at build/install time are **signed** where
  available.
- Security-scanner findings are **fixed, not suppressed** (S05/N02GO).

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
  explicit timeout/context; on failure the error is **returned to the model and
  logged** (no secrets) — **no silent retry/backoff**.
- **Concurrency (X05):** per-session mutex serializes reads/writes; no
  partial/corrupt state; verified with `go test -race`.

---

## 9. Logging

**Logging library:** `logrus` v1.10.2 (**L01GO**).

- **Channel by transport (L01):** Local / stdio → logs go to a **file**
  (`LOG_FILENAME`, `chmod 600`), **never stdout**. (HTTP mode would write to
  stdout — not applicable here.)
- **Mode-dependent enablement (L02):** in **stdio** mode (default), logging is
  **enabled only when `LOG_LEVEL` is set**; unset ⇒ disabled.
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
| `test` | Unit tests (`go test -race`, **C04GO**) + **coverage ≥ 95% gate** (**C01**). |
| `lint` | golangci-lint (`golangci-lint run ./...`, **C03GO**), gosec (**C05GO**), govulncheck (**C06GO**), go-arch-lint (**C07GO**). |
| `e2e` | `go test -tags e2e ./...` via the **go-docker-testsuite** harness (**C09GO**) — declared because e2e exists. |
| `container-image` | **NOT declared** — Local stdio-only server produces no image (**B03/R01/N31/N17**). |

The 4-stage pipeline shape (linters → build → tests → release artifacts) is
provider-agnostic; here it is realized in GitHub Actions:

- **linters** stage: `make lint` (golangci-lint, gofmt/gofumpt, gosec,
  govulncheck, go-arch-lint).
- **tests** stage: `make test` (race + coverage ≥ 95%), plus the **hard-gate**
  jobs: **gremlins** mutation testing `--threshold-efficacy=80
  --threshold-mcover=80` (C02/C08GO) and **gitleaks** secret scan over git
  history (C03/N28). These are dedicated hard-gate jobs, not part of the 5-target
  make interface.
- **build** stage: `make build` (goreleaser → single binary per platform,
  ldflags-embedded build metadata **B02**).
- **e2e** stage: **dedicated `e2e` CI job** running `make e2e` (C04/T02/C09GO).
  e2e tests are **build-tagged** (`//go:build e2e`) so they are **excluded from
  the default unit pass** (T02/N30).
- **release artifacts:** for a Local stdio-only server there is **no container
  image** to publish (B03/R01); goreleaser publishes binary release artifacts on
  tags (**B01**).

**Coverage gate (C01):** ≥ 95%, fails the build below.
**Mutation gate (C02/C08GO):** gremlins hard gate at 80/80.
**Secret scan (C03):** gitleaks over full git history; findings fixed, not
suppressed.

**CI never mutates repository state (N36):** CI is **read-only** over the
working tree — no auto-commits, no pushing commits/tags back, no writing to
tracked files. Write-side effects are **external publications only** (release
binaries on GitHub Release). For this Local server there are no registry image
publications.

**Go / dependency currency (C05/N33):** pinned to latest stable — **Go 1.27.1**,
**go-sdk v1.8.0**, **logrus v1.10.2**, **go-docker-testsuite latest** (see
`reference/versions.md`). **resty.dev/v3** is **not** a current dependency: the
server performs no outbound HTTP (L02GO applies only when genuine remote HTTP is
introduced), and resty would be added only for a future backend that requires it.

**TDD workflow (T01):** all fixes/features are done TDD-style — **@qa** writes
tests first (isolated context), **@developer** writes the implementation
(isolated context). e2e tests verify the full tool-handler → Docker-backend path
(T02/C09GO).

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

**MUST satisfied:** M01–M07, C01, C02, C04, C05, C01GO–C09GO, T01, T02, S01–S12,
R02, R05, R06, R07, B01, B02, B05, L01–L09,
L01GO–L04GO, A01, A02, D01–D04, X01, X02, X03, X05, C03.

**Explicitly not applicable (documented):**
- **O01–O04/N32** — Local stdio-only server: no HTTP, no observability
  endpoint.
- **R01/R03/R04/B03/B04/N14/N17/N18** — no container image for a Local server.
- **M03 OAuth2** — no upstream HTTP API to authorize against (justified).
- **X04** — stateful-but-in-memory; no persistence/migration.
- **S06/S07/S10/S13** — not applicable: public GitHub.com repo (S06); every
  tool is a closed-world `openWorldHint:false` tool (S07/S10); Go is not a
  high-risk TS/JS ecosystem (S13).

**MUST NOT respected:** N01–N13, N15–N36 as applicable (notably N31 — no
undeclared optional Makefile targets; N36 — CI read-only; N05GO — official
Docker SDK, not low-level `http.Client`).
