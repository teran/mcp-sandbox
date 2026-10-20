# Security Policy

This is a **local stdio-only** MCP server that provides a safe, isolated
**Docker sandbox** so a model-host client can run arbitrary commands without
using the host shell. There is **no HTTP listener and no container image**.
The server is developed and tested largely by AI agents with minimal human
review, so security is enforced automatically by CI (lint, coverage, mutation,
gosec, govulncheck, gitleaks). The security baseline is described in `SPEC.md`
(S01–S14) — the single source of truth for the security contracts this
repository must honour.

## Reporting a Vulnerability

Please **do not** open a public issue for a security vulnerability.

Report vulnerabilities privately to the maintainers using GitHub's private
vulnerability reporting at
<https://github.com/teran/mcp-sandbox/security/advisories/new>.

Please include:

- the affected version(s) and how you detected the issue;
- a minimal, reproducible description of the vulnerability;
- the potential impact and, if known, a suggested fix;
- the `ALLOW_DIRS` / configuration context in which it occurs (if relevant).

## What happens next

1. The maintainers acknowledge the report within 5 business days.
2. The issue is triaged: severity, affected versions, and a fix plan.
3. A fix is developed **test-first** and lands through the normal CI gates.
4. Once a fix is available, the vulnerability is disclosed and (if appropriate)
   a `v*` release is published.

We ask that you **do not** disclose the vulnerability publicly until a fix has
been released or the maintainers explicitly agree otherwise (responsible
disclosure).

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| 0.x     | ✅ |

Only the latest release line is supported. The project is in an early/draft
stage, so only the latest **0.x** line is patched. Older versions are not
patched; upgrade to a supported version.

## Security Baseline

This is a **local stdio-only** server with **no HTTP listener and no container
image**.

- **TLS** is never implemented inside the server (S01) — there is no HTTP
  listener at all.
- **Secrets** are never leaked into tool output or logs (annotation-based
  redaction, `secret:true`, S02). Secrets go into tmpfs `0600` files under
  `/run/sandbox`, never argv/image. Note that `sandbox_exec` records
  `command`/`summary` verbatim and they are intentionally **not** masked — a
  command line is not a channel for secrets.
- **Filesystem** access is scoped to `ALLOW_DIRS` (empty by default, with
  `ALLOW_SYMLINKS=false` by default); anything resolving outside the scope is
  rejected (S04).
- **Immutable `Isolation`** — read-only rootfs, `CapDrop:ALL`, no-new-privileges,
  non-root user, bounded pids/memory/cpu/ulimits, tmpfs mounts, `AutoRemove`
  — a caller can never weaken these.
- **Destructive tools** (`sandbox_destroy`, `sandbox_destroy_all`) require
  human confirmation (HITL, S12).
- **Open-world tools** treat their output as untrusted data and are isolated
  from `ALLOW_DIRS`/secrets.

See `SPEC.md` for the full domain-specific security audit (S14).
