# Filesystem layout and mounts

Understanding where you can write inside a sandbox matters for commands that
produce artifacts or need scratch space.

## Layers

- **Root filesystem** — read-only overlay. You cannot modify the image rootfs.
- **`/tmp`** — a tmpfs mount that is writable. Note that some Docker daemons
  (notably Docker Desktop on macOS) force `noexec` on tmpfs, so `/tmp` may not
  be executable even though it is writable.
- **`/run`** — a tmpfs mount owned by the sandbox user, used for secret files.
- **`cwd`** — the session working directory, mounted 1:1 at the same absolute
  path (bind-mount; no separate virtiofs). It is read-only unless the profile
  sets `cwd.mode=rw`.

## Where to write

For scratch files, build outputs, or runtime compilation, write into the
writable bind-mounted `cwd` (the profile's `cwd.mode=rw`). Because `/tmp` may
be `noexec` on some platforms, do **not** rely on `/tmp` for running compiled
binaries.

## Go caches recipe

When compiling Go inside the sandbox, point the caches at the writable
bind-mount so builds succeed regardless of tmpfs `noexec`:

```
export GOCACHE="$PWD/.gocache"
export GOMODCACHE="$PWD/.gomodcache"
export GOTMPDIR="$PWD/.gotmp"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"
```

Set these in your command or a temp script before running `go build` / `go test`
so the toolchain writes to a path that is both writable and executable.
