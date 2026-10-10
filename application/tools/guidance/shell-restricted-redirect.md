# Host shell restricted — route through the sandbox

The host shell is **restricted**. You (the model-host) cannot run commands
directly on the host machine. Any command you need to run must go through this
mcp-sandbox server.

## The workflow

1. **Create a sandbox** with `sandbox_create`. Pick an operator-authored
   profile by name (`profile_list` shows the available ones) and pass the
   session `cwd`. The profile determines the image, environment, mounts,
   network, resources, user and sudo policy.
2. **Run commands** inside the sandbox with `sandbox_exec`. Pass the command
   as a single scalar string; it is executed via the sandbox's fixed shell
   wrapper — never interpolate a user string into a shell string.
3. **Inspect** results with `sandbox_list`, `sandbox_info`, `sandbox_stats`.
4. **Clean up** with `sandbox_destroy` / `sandbox_destroy_all`, which require
   human confirmation.

## Why redirect instead of using the host shell

- The host shell is outside the sandbox's isolation boundary and is denied.
- The sandbox provides per-session, ephemeral, disposable containers.
- Commands run with a hardened, immutable `Isolation` block, so a mistake does
   not leak to the host.

Whenever you find yourself wanting to reach for the host shell, stop and use
this sandbox instead.
