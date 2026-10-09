# FEEDBACK — Working with the mcp-sandbox (dogfooding)

Living notes on the friction of using the sandbox to run the repo's own quality
gates (`go build` / `go vet` / `go test -race` / coverage / lint) after host
`shell` access is restricted. Kept to the items **not yet covered** by the code,
docs, or current setup; resolved/obsolete points are dropped.

## 1. Execution-path discovery when host shell is restricted

When the host `shell` is restricted, check the MCP catalog for a dedicated
sandbox/execution server **before** treating the shell as the only option. The
manager's default toolset (core + task + memory) has no executor, so the sandbox
MCP is the sanctioned way to run arbitrary commands. This lesson generalizes to
any MCP host — ideally captured as a **general MCP skill** ("host shell blocked
→ run it in the sandbox MCP"), not repo-specific guidance.

## 2. Read-only rootfs + noexec /tmp — the biggest trap

The container filesystem layout is non-obvious:

- `/` is an **overlay mounted read-only** — Go's default build cache at
  `/.cache/go-build` fails with `read-only file system`.
- `/tmp` is **tmpfs rw but mounted `noexec`** — test binaries built into
  `/tmp/go-build...` fail to execute (`fork/exec ...: permission denied`).
- `/go` is part of the read-only overlay, so `mkdir /go/gocache` also fails.
- The only writable **and executable** path is the repo mount (`virtiofs rw`,
  no `noexec`).

Workaround that works: point `GOCACHE`/`GOTMPDIR`/`GOMODCACHE` at writable
locations (module cache **outside the repo tree**, build cache on a writable+
exec mount). Still manual and fragile — not covered by the base image.

## 3. `go tool cover -func` crashes with SIGBUS (mmap on virtiofs)

`go tool cover -func` can crash with a `SIGBUS ... cmd/link/internal/ld.(*OutBuf).Mmap`
fault while building the `cmd/covdata` helper, because `GOTMPDIR` points at the
virtiofs mount which does not reliably support that mmap pattern. Workaround:
compute coverage from `coverage.out` with `awk` instead of `go tool cover`.

Note: building a **custom base image** could fix the tooling/fs friction, but
that forces maintaining **latest versions everywhere — no version matrix** is
possible that way, so a custom image is not an obvious win.

## 4. Extra tooling is not present in the image

The `golang:1.27.2` image has no `golangci-lint`, `go-arch-lint`, `gosec`,
`govulncheck`, or `gitleaks`. Installing them via `go install` is hampered by the
same linker mmap quirk and by there being no clean writable+exec location besides
the repo mount (which pollutes the working tree — see `.gitignore`).

## 5. Committing from the sandbox

Previously flagged as unsafe (detached HEAD, no git identity). In practice git
**is allowed** and local git settings are usually present, so committing from the
sandbox can work; the only real blockers are git config/identity and being on a
detached HEAD — environment-specific, not fundamental. Prefer committing from the
host checkout where the config is guaranteed.

## 6. "Permission denied: shell" read as a dead end, not as a redirect to the sandbox

The host shell only allows a very small set of commands (even `cat`, `tail`,
`head`, `sed`, `grep`, `rg` are sandbox-only). Everything else is meant to run in
the sandbox, but an agent can read the denial as "can't run this at all" instead
of re-running it via `sandbox_exec`. The gap is agent-side discovery: make the
"host shell restricted → sandbox MCP" substitution an explicit, discoverable
invariant (ideally a general MCP skill, see §1).

## 7. cgo-link flakiness (`crosscall1`) under `go test -race` on arm64

Occasionally `go test -race` fails to **link** some test binaries on the
linux/arm64 golang image with `undefined reference to crosscall1`. `crosscall1`
is the cgo runtime bridge stub (`runtime/cgo/gcc_arm64.S`); under heavy parallel
`-race` linking the symbol intermittently fails to land in the `runtime/cgo`
archive — a **build-cache / parallel-link race**, not a code bug. Retry (warm
cache) or `go clean -cache` resolves it; stable on the host, flaky in the sandbox.

## 8. gosec must not scan the module cache

`gosec ./...` walks the whole package graph, so if the Go module cache lives
**inside the repo tree** it type-checks every dependency (huge, noisy). The module
cache must live **outside the repo tree** (currently handled: `GOMODCACHE` points
at the mounted `/go/pkg/mod`, outside the checkout).

## Recommendations / future work

- **Hot-reload of operator profiles** — currently a profile edit requires a full
  server-process restart (reconnecting the MCP client does not reload profiles,
  which are read once at startup). Planned as a future feature (SIGHUP/fsnotify).
- Default `GOCACHE` / `GOTMPDIR` to a writable+exec location so Go builds work
  out of the box without manual env pointing.
- Document the full "writable vs executable" mount matrix (ro overlay / noexec
  tmpfs / virtiofs rw) as a dedicated section.
- Make the "host shell restricted → sandbox" substitution explicit and
  discoverable, ideally as a general MCP skill (see §1/§6).
