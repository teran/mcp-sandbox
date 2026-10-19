# Creating a sandbox

`sandbox_create` provisions a new, isolated, ephemeral Docker container from an
**operator-authored profile**.

## Inputs

- **profile** (**REQUIRED**) — the operator-authored profile name (see
  `profile_list`). This is the trusted source for the image, environment,
  mounts, network, resource limits, user and sudo policy.
- **cwd** (**REQUIRED**) — the session working directory, mounted 1:1 at the
  same absolute path inside the container (read-only unless the profile sets
  `cwd.mode=rw`).
- **session_id** (**REQUIRED**) — the OpenCode client session id that created
  this sandbox, recorded as a container label for traceability.
- **secrets** (OPTIONAL) — name/value pairs delivered as tmpfs `0600` files, so
  their values never appear in argv, image, or environment.

## What the profile owns

The profile determines image, env, mounts, network, resources, user and sudo.
A caller cannot override these through `sandbox_create`; they come from the
profile.

## Immutable Isolation

The `Isolation` block is server-enforced and **cannot be weakened** by a caller
or a profile: read-only rootfs, dropped capabilities, no-new-privileges,
seccomp, non-root user, bounded pids/memory/CPU/ulimits, tmpfs mounts for
`/run` and `/tmp`, and `AutoRemove=true`. You never need to (and cannot) request
these hardening options yourself.

## Secrets

Pass secrets as name/value pairs; each becomes a tmpfs `0600` file under
`/run/sandbox`. Refer to them by name from your commands. Never place a secret
in a command line or environment — it would be logged or visible.

## Notes

- Creating a duplicate session is surfaced as an error.
- The returned `id` is the sandbox session id; use it for `sandbox_exec`,
  `sandbox_info`, `sandbox_stats` and `sandbox_destroy`.
