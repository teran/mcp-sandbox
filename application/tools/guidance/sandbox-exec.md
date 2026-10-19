# Executing commands in a sandbox

`sandbox_exec` runs a command inside an existing sandbox via its configured
shell.

## Parameters

Quick reference — full details in the sections below:

| Parameter     | Type              | Required | Notes                                                |
| ------------- | ----------------- | -------- | ---------------------------------------------------- |
| `id`          | string            | **REQUIRED** | Sandbox session id (from `sandbox_create`/`sandbox_list`). |
| `command`     | string            | **REQUIRED** | Scalar command run via the fixed shell wrapper.      |
| `env`         | object            | optional | Key/value overrides (see [Environment](#environment)). |
| `summary`     | string            | optional | Short human-readable description (see [Summary](#summary)). |
| `working_dir` | string            | optional | Working directory for the command.                   |
| `timeout`     | integer (seconds) | optional | Deadline; `0` = no deadline (see [Timeout](#timeout)). |
| `user`        | enum `"default"` \| `"root"` | optional | `root` requires the profile's sudo policy = `require` (see [User](#user)). |

## Passing the command

Pass the command as a single **scalar string**. It is executed through a fixed
wrapper of the form `[<shell>, "-c", command]`, where `<shell>` comes from the
profile (default `bash`). This means:

- You can write shell syntax: pipes, loops, `export VAR=$(...)` all work.
- No user string is interpolated into a shell string at the tool boundary — the
  command is a literal argument to `-c`.
- Never put secrets in the command line; use tmpfs `0600` secret files instead.

## Environment

`env` is a set of literal string key/value overrides applied to the command.
Values are **not** shell-evaluated — `env` is not a shell string, so `$HOME`
inside an env value is not expanded by the wrapper (the command body itself is
what the shell evaluates).

## Long-running commands and temp scripts

For complex or multi-line work, prefer writing a script file into the writable,
bind-mounted `cwd` and then executing it, rather than cramming everything into
one `-c` string. This keeps quoting manageable and lets you iterate.

## User

By default the command runs under the sandbox's non-root user. `user: root`
runs as scoped uid 0 and **requires** the profile's sudo policy to be `require`.

## Timeout

`timeout` (seconds) sets a deadline for the command. A value of `0` means no
deadline. When the deadline is hit, the command is cancelled.

## Summary

Fill `summary` with a short human-readable description of the command (one or
two words, e.g. "run unit tests"). It is recorded for traceability.

## Seatbelt (darwin) sessions

Under the experimental **`sandbox-exec` (Seatbelt)** backend (`seatbelt`
profile) some mechanics differ: there is **no container/tmpfs**, resource
limits are **soft `setrlimit`** only, `sandbox_stats` is **not supported**, and
the command runs under the **current host user**. The command must start under
whatever the operator's `seatbelt.sbpl` grants (typically `(allow default)` for
non-file operations plus a narrowed file scope). `user: root` is not applicable
(sudo is always `never`).

## Streaming and output handling

Long-running commands may stream live output through the progress channel.
Streamed chunks and the final `stdout`/`stderr` are **ANSI-sanitized** and
**secrets-redacted** before they reach you, so no control sequences or known
secret values leak through.
