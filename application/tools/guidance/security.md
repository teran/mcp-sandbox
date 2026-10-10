# Security model

mcp-sandbox replaces the host shell with isolated, ephemeral Docker containers.
Security is the primary contract.

## Immutable Isolation

Every sandbox enforces an immutable `Isolation` block that neither a caller nor
a profile can weaken:

- read-only rootfs
- all capabilities dropped (minimal set re-added)
- `no-new-privileges`
- seccomp filtering
- non-root user
- bounded pids, memory, CPU and ulimits
- network from the operator profile
- tmpfs mounts for `/run` and `/tmp`
- `AutoRemove=true` — the container is removed when it stops

Cleanup is **explicit only**: a sandbox is destroyed by
`sandbox_destroy` / `sandbox_destroy_all`. There is no automatic garbage
collection — the idle reaper and orphan reclamation are future work.

## Secrets

Secrets are delivered as **tmpfs `0600` files** under `/run/sandbox` — never in
argv, image, or environment. Never place a secret in a command line or env:
commands are logged as-is and are not secret-value-redacted. All output and logs
redact known secret values before returning them to the caller.

## Human-in-the-loop (HITL)

Destructive tools (`sandbox_destroy`, `sandbox_destroy_all`) require human
confirmation. They permanently and irreversibly remove resources.

## Output hygiene

All tool output — including streamed progress chunks — is **secrets-redacted**
(S02) and **ANSI-sanitized** (S09): no known secret value and no control
sequence leaks through any output channel.

## Host access

Host filesystem access is gated by `ALLOW_DIRS` (empty by default), and path
resolution rejects anything escaping the allowed roots. There is no general
outbound HTTP; the only backend is the local Docker daemon over a unix socket.
