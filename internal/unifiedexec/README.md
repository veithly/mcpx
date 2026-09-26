# Native unified execution

MCPX executes commands in its Go runtime with os/exec and github.com/creack/pty.
No Codex executable, exec-server RPC, model/API, external patch tool, executable
discovery or private application home is used. Options contains only WorkDir
and Env. Exec/Write JSON fields preserve the current MCPX contract.

## Source and adaptation

The implementation derives lifecycle, shell arguments, bounded yields, session
ownership and head/tail output semantics from OpenAI Codex commit
68e0c9f5d8fd9449e97a81e92e8fcb86795713b2:

- codex-rs/core/src/unified_exec/{mod,process,process_manager,head_tail_buffer}.rs
- codex-rs/core/src/shell.rs and codex-rs/shell-command/src/shell_detect.rs
- codex-rs/utils/pty/src/{pipe,pty,process_group}.rs and win/job.rs

These sources are Copyright OpenAI, Apache-2.0; Go adaptations carry source
comments. Repository LICENSE/NOTICE aggregation belongs to the separate
licensing change. This is a Go adaptation, not a claim of byte-for-byte parity.
Codex approvals, sandboxing, shell snapshots, remote drivers and model
orchestration are not implemented here; the MCPX server owns its policy.

## Environment and shell

Nil Env snapshots the current environment. A non-nil slice is the complete
child environment, including an explicitly empty slice. Values are not logged.
SHELL in that environment takes precedence. Explicit shell paths must exist;
bare names resolve through absolute entries in that environment's PATH.
Unsupported shells fail. Missing tools are never installed.

Without SHELL the fallback is zsh/bash/sh on macOS, bash/zsh/sh on other Unix,
and PowerShell/cmd on Windows. Unlike upstream's passwd lookup, MCPX does not
query account databases: daemon owners should supply SHELL explicitly.
Login defaults to true (-lc for sh/bash/zsh); false uses -c. PowerShell adds
-NoProfile only for login=false. PTY does not implicitly enable interactive -i.

On macOS launchd, set SHELL, HOME and PATH explicitly for the required user
context. Login shells may update PATH through normal login startup files;
non-login shells retain the supplied PATH, subject to shell startup semantics.
MCPX does not read credentials, scrape dotfiles, inject Homebrew paths or replay
shell snapshots. Tests use temporary HOME and explicit shell.

## Ownership, output and waiting

- Unix children start in a fresh session/process group. macOS/Linux PTYs are
  real controlling terminals from creack/pty, initially 80x24. Plain execution
  uses a merged stdout/stderr pipe and null stdin; input requires tty=true.
  PTY Ctrl-C uses the terminal's line discipline.
- Request context cancellation interrupts waiting, returning partial output
  with the session ID. It never kills or repeats a started command. Input may
  already be partially written when cancelled; errors report accepted bytes.
  Input is never replayed. Close owns termination.
- Input and output use independent gates. Long polls cannot block stdin or
  Close. Reads consume bytes once. Already collected handles are rejected.
  IDs are unique across clients in this MCPX process.
- Pending output is capped at 1 MiB per session, preserving head and tail.
  Response budgets estimate one token per four bytes (default 10,000, capped
  at 262,144); omitted bytes are reported, not deferred for a later poll.
  Incomplete UTF-8 runes are held between reads/polls. Invalid complete bytes
  and incomplete final bytes are replaced; truncation trims rune edges.
- Exec defaults to 10s, bounded to 250ms–30s. Nonempty Write is bounded to
  250ms–30s; empty polls default/minimum to 5s and cap at 300s. Input shares the
  call deadline. Context cancellation can end waits earlier.
- Close, normal root exit and signal exit kill remaining members of the
  managed Unix process group. Output drains after exit with a 250ms cap before
  descriptor closure. Normal descendants and inherited pipe descriptors are
  covered. Deliberate setsid/new-process-group escape is outside this guarantee;
  this is lifecycle management, not a security sandbox.
  On macOS, denied group signals retry against individually verified group
  members, following upstream's PTY teardown handling without cgo or external ps.
- Exited output remains until collected or Close. The 64-session cap rejects
  new starts without evicting unread output or killing active work. Final
  collection and Close remove sessions.
- Windows pipes start suspended, assign a job, then resume, so cleanup includes
  descendants. Native Windows PTY returns ErrUnsupported instead of substituting
  pipes. Cross-compilation is not Windows runtime verification. No cgo is needed.

## Focused verification

Run go test ./internal/unifiedexec -count=1 and
go test -race ./internal/unifiedexec -count=1. Native fixtures have no Codex on
PATH and cover stdin EOF, exit codes, strict fields, real PTY, Ctrl-C, concurrent
poll/input, split Unicode, cancellation without replay, bounded output, isolated
handles, cleanup and grandchildren surviving their parent's exit. Fixtures are
Go child processes and temporary files. No external differential baseline runs
implicitly.
