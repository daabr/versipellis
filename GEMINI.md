# Gemini & Antigravity Instructions

Follow [`AGENTS.md`](./AGENTS.md) for architecture, coding practices, and concurrency invariants.

When reviewing code, PRs, or diffs, read and follow [`REVIEW.md`](./REVIEW.md) as well.

## Antigravity Environment Guidelines

- **Sandbox execution:** Commands run in Antigravity's standard sandbox (`BypassSandbox: false`) by default without loopback socket access.
  - For standard unit and race testing, exclude socket-dependent HTTP tests:

    ```shell
    go test -race $(go list ./pkg/... | grep -v /pkg/http)
    ```

  - Use `BypassSandbox: true` only when loopback network socket access is strictly necessary (e.g., running `pkg/http/sys_test.go`).
- **Command execution:**
  - Never execute `cd` commands; always set the command's working directory via `Cwd`.
  - Never run `sleep` in background commands; use Antigravity's `schedule` tool or non-blocking mechanisms.
- **Code modifications:**
  - Prefer surgical replacements (`replace_file_content`) over whole-file rewrites whenever possible.
  - After modifying Go code, always run `golangci-lint fmt` and `golangci-lint run`.
