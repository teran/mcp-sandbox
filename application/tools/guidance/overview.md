# mcp-sandbox overview

mcp-sandbox is an isolated Docker sandbox that lets a model-host run arbitrary
commands **without using the host shell**. The host shell is restricted; route
every command through this sandbox server instead.

> **Backends.** On macOS, commands can alternatively run under the experimental
> **`sandbox-exec` (Seatbelt)** backend instead of Docker (a `seatbelt` profile,
> no container/daemon/image). It is a policy-based, **strictly weaker**
> containment — the mechanics described in this guide (mounts, `cwd`, tmpfs,
> network, resources) are **Docker-centric** and differ for seatbelt sessions
> (no tmpfs, soft `setrlimit` only, file/network SBPL rules). See `SPEC.md`
> §7.12 and the `seatbelt` profile block.

## Tool grouping

Tools are grouped by read, write, and delete intent so you can predict their
effect before calling them.

- **Read** (read-only, never mutate): `sandbox_list`, `sandbox_info`,
  `sandbox_stats`, `profile_list`.
- **Write** (create / execute, non-destructive): `sandbox_create`,
  `sandbox_exec`.
- **Delete** (destructive, irreversible): `sandbox_destroy`,
  `sandbox_destroy_all`.

## Annotations

Every tool carries closed-world annotations describing how it behaves:

- `ReadOnlyHint` marks tools that never mutate state.
- `DestructiveHint` marks tools that permanently remove resources.
- `IdempotentHint` marks tools that are safe to call repeatedly.
- `OpenWorldHint` is always `false`: the tool set is a closed world.

## Human-in-the-loop (HITL)

Destructive tools (`sandbox_destroy`, `sandbox_destroy_all`) require **human
confirmation** before they take effect. Never call them without the user's
explicit, informed consent.

## Where to go next

- `sandbox://guidance/sandbox-create` — creating a sandbox from a profile.
- `sandbox://guidance/sandbox-exec` — running commands inside a sandbox.
- `sandbox://guidance/shell-restricted-redirect` — the host-shell redirect
  workflow.
- `sandbox://guidance/filesystem-mounts` — how the sandbox filesystem is laid
  out and where to write.
- `sandbox://guidance/security` — the security model that keeps this safe.
