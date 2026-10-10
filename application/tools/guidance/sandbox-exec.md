# Executing commands in a sandbox

`sandbox_exec` runs a command inside an existing sandbox via its configured
shell.

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

## Streaming and output handling

Long-running commands may stream live output through the progress channel.
Streamed chunks and the final `stdout`/`stderr` are **ANSI-sanitized** and
**secrets-redacted** before they reach you, so no control sequences or known
secret values leak through.
