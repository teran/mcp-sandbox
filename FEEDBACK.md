# FEEDBACK — Working with the mcp-sandbox (dogfooding)

Living notes on the friction of using the sandbox to run the repo's own quality
gates (`go build` / `go vet` / `go test -race` / coverage / lint) after host
`shell` access is blocked. Kept to the items **not yet covered** by the code or
docs; resolved points (e.g. the trusted-registry allowlist now being
configurable via `TRUSTED_REGISTRIES`) are dropped.

## 1. Execution-path discovery when host shell is blocked

When the host `shell` is denied, check the MCP catalog for a dedicated
sandbox/execution server **before** treating the shell as the only option. The
manager's default toolset (core + task + memory) does not include an executor,
so the sandbox MCP is the sanctioned way to run builds/tests.

## 2. No client-side way to enumerate allowlisted registries/images

`sandbox_create` requires an explicit image and there is no tool that lists the
trusted registries (the `TRUSTED_REGISTRIES` allowlist). Discovering whether an
image reference is accepted is still trial-and-error. A read-only helper to
enumerate the effective allowlist would remove this guessing.

## 3. Read-only rootfs + noexec /tmp — the biggest trap

The container filesystem layout is non-obvious and costs the most time:

- `/` is an **overlay mounted read-only** — Go's default build cache at
  `/.cache/go-build` fails with `read-only file system`.
- `/tmp` is **tmpfs rw but mounted `noexec`** — test binaries built into
  `/tmp/go-build...` fail to execute (`fork/exec ...: permission denied`).
- `/go` is part of the read-only overlay, so `mkdir /go/gocache` also fails.
- The only writable **and executable** path is the repo mount itself
  (`virtiofs rw`, no `noexec`).

Workaround that works: `GOCACHE=/tmp/gocache` (cache is read-only for Go, noexec
is fine) + `GOTMPDIR=<repo>/.sandbox-build` (exec-capable) + `GOMODCACHE=/tmp/gomod`.
This is still **manual and fragile** — not covered by the base image.

## 4. `go tool cover -func` crashes with SIGBUS (mmap on virtiofs)

After tests pass, `go tool cover -func` can crash with a
`SIGBUS ... cmd/link/internal/ld.(*OutBuf).Mmap` fault while building the
`cmd/covdata` helper, because `GOTMPDIR` points at the virtiofs mount which does
not reliably support that mmap pattern. Workaround: compute coverage from
`coverage.out` with `awk` instead of `go tool cover`. Not covered.

## 5. Extra tooling is not present in the image

The `golang:1.27.2` image has no `golangci-lint`, `go-arch-lint`, `gosec`,
`govulncheck`, or `gitleaks`. Installing them via `go install` is hampered by
the same linker mmap quirk (building large binaries on the virtiofs mount) and
by there being no clean writable+exec location besides the repo mount (which
pollutes the working tree — see `.gitignore` for the cache dirs). Not covered.

## 6. Committing is not appropriate from the sandbox

The sandbox mounts the repo read-write, but git there sees a **detached HEAD**
(not `master`) with **no `user.name`/`user.email`** configured. Creating commits
from the sandbox is unsafe/wrong; the user commits on their checkout instead.
Not covered.

## 7. "Permission denied: shell" read as a dead end, not as a redirect to the sandbox

During a config-editing task the agent needed to syntax-check a JSONC file and
instinctively reached for the host `shell` (`node -e ...` to strip comments and
`JSON.parse`). That returned `Permission denied: shell` — precisely the block the
config now enforces. The agent then treated validation as "not possible" and fell
back to asserting correctness from the structure of the edits, **even though the
sandbox MCP was sitting right there in the catalog and its entire purpose is
arbitrary command execution**. The same `node` command could have run untouched
inside a sandbox (create with `cwd`, `sandbox_exec` with argv, destroy).

The gap is agent-side discovery, not capability:

- Nothing in the prompt/tool-discovery guidance wired the two facts together:
  "host `shell` is blocked" and "re-run the command via `tools.sandbox.sandbox_exec`".
  The link is left to agent intuition, and the default instinct is host shell.
- In this repo the sandbox is framed around the Go quality gates
  (`go build`/`go vet`/`go test`), so it did not register as the **general**
  executor for "any command" — here a one-off JSON parse.
- Getting a file into the sandbox requires remembering the full
  `sandbox_create(cwd)` → `sandbox_exec(argv)` → `sandbox_destroy` round-trip.
  With no shorthand it feels heavier than shell, so an agent avoids it.

What would have closed the loop:

- An explicit, discoverable invariant in the agent guidance: *"`Permission denied:
  shell` is not a dead end — run the same command through the sandbox MCP (create
  with `cwd`, exec as argv, destroy)."*
- Presenting the sandbox as the universal executor ("you can run anything in the
  sandbox" — node, go, awk, etc.), not just the repo's build/lint/test gates.
- A reminder that the repo is already mounted read-write at the working directory,
  so reading the file and returning its parsed output is a natural fit.

## Recommendations (still open)

- Default `GOCACHE` / `GOTMPDIR` to a writable+exec location in the base image so
  Go builds work out of the box without manual env pointing.
- Document the full "writable vs executable" mount matrix (ro overlay / noexec
  tmpfs / virtiofs rw) as a dedicated section — the tmpfs-noexec note exists in
  SPEC/AGENTS but the complete matrix does not.
- Add a read-only way to enumerate the effective `TRUSTED_REGISTRIES` allowlist
  (see §2).
- Make the "host shell blocked → sandbox" substitution explicit and discoverable
  (see §7): an invariant that `Permission denied: shell` means "run it in the
  sandbox", plus guidance framing the sandbox as the general executor for any
  command, not just the repo's quality gates.
