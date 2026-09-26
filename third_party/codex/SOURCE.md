# Codex source attribution for MCPX native tools

Upstream project: OpenAI Codex (`openai/codex`).
Pinned commit: `68e0c9f5d8fd9449e97a81e92e8fcb86795713b2`.
Reference checkout: `/tmp/mcpx-codex-cli-20260925` (development reference only;
not needed to build, test or run MCPX).
Copyright: 2025 OpenAI. License: Apache License 2.0.

The adjacent `LICENSE` and `NOTICE` are byte-for-byte copies of the upstream
root files at the pinned commit. The full upstream NOTICE is retained, including
its Ratatui attribution; this port does not copy the Codex/Ratatui terminal UI.
No upstream repository, executable, npm package, or generated Rust build is
vendored here. This directory contains attribution documents only.

## Ported logic and source files

This is an inventory of selected logic translated or adapted to Go, not a claim
of complete line-by-line equivalence. The source headers in each derived Go file
identify its origin. Native runtime acceptance and remaining limits are recorded in
[`docs/results/2026-09-26-native-toolchain-port.md`](../../docs/results/2026-09-26-native-toolchain-port.md).
The main implementation owners should keep this mapping synchronized with their
final source headers before closing acceptance.

| MCPX destination | Upstream source at the pinned commit | Ported logic / modifications |
| --- | --- | --- |
| `internal/unifiedexec/types.go`, `client.go` | `codex-rs/core/src/unified_exec/mod.rs`, `process.rs`, `process_manager.rs` in that directory | Request/result and process-session concepts adapted to MCPX's Go interfaces and ownership. |
| `internal/unifiedexec/exec.go` | `codex-rs/core/src/unified_exec/process_manager.rs` | Start, wait, stdin and process-handle orchestration translated to local Go execution. |
| `internal/unifiedexec/process.go` | `codex-rs/core/src/unified_exec/process.rs`; `codex-rs/utils/pty/src/pipe.rs`, `pty.rs`, `process.rs` in that directory | Process lifecycle and output collection adapted to Go concurrency and local OS processes. |
| `internal/unifiedexec/process_unix.go` | `codex-rs/utils/pty/src/pipe.rs`, `pty.rs`, `process_group.rs` in that directory | Unix pipes, PTY and process-group handling; platform operations are implemented in Go. |
| `internal/unifiedexec/process_windows.go` | `codex-rs/utils/pty/src/pipe.rs`, `codex-rs/utils/pty/src/win/job.rs` | Windows process handling reference; this inventory does not establish Windows runtime or PTY parity. |
| `internal/unifiedexec/output.go` | `codex-rs/core/src/unified_exec/head_tail_buffer.rs` | Bounded head/tail output retention adapted to Go; output tokens are estimated, not a model tokenizer guarantee. |
| `internal/unifiedexec/shell.go` | `codex-rs/core/src/shell.rs`, `codex-rs/shell-command/src/shell_detect.rs` | Shell selection and invocation semantics adapted to local Go process launch. |
| `internal/patch/parser.go` | `codex-rs/apply-patch/src/parser.rs`, `streaming_parser.rs` in that directory | Patch markers and Add/Update/Delete/Move grammar translated/adapted to Go. |
| `internal/patch/seek_sequence.go` | `codex-rs/apply-patch/src/seek_sequence.rs` | Ordered context matching and EOF matching translated to Go. |
| `internal/patch/file_update.go` | `codex-rs/apply-patch/src/file_update.rs` | Apply update chunks and derive new contents in Go. |
| `internal/patch/text_file.go` | `codex-rs/apply-patch/src/text_file.rs` | Text and line-ending handling translated to Go. |
| `internal/patch/apply.go` | `codex-rs/apply-patch/src/lib.rs` | Patch application and result summaries adapted to MCPX path/file policy and local filesystem operations. |

Additional mappings: group_darwin.go/group_other.go adapt utils/pty/src/process_group.rs;
internal/patch/testdata/scenarios copies apply-patch/tests/fixtures/scenarios inputs,
patches and expected outputs for mandatory native tests.

MCPX's Workspace path checks, file policy, local write helpers and authenticated
Remote Session routing are integration changes, not upstream sandbox code.

## Modifications and limits

- Rust/Tokio implementation concepts are translated or adapted to Go, local OS
  process primitives and MCPX's lifecycle. The `exec_command`, `write_stdin` and
  `apply_patch` public names remain; Binary/Version dependencies on an external
  Codex executor are removed. `process_sessions` remains MCPX runtime state.
- MCP transports patches in one `input` string. MCPX owns authentication,
  Workspace boundaries, command/file policy, console approval and receipts.
  These are not the complete upstream sandbox or approval orchestrator.
- Codex model-agent orchestration, OS sandbox integration, network proxy, full
  terminal UI and the external exec-server protocol are outside this port.
- Native programming cores must not wrap or fall back to external `codex`,
  `codex-exec-server` or `apply_patch` executables. Mandatory tests and the HTTP
  smoke run without them; traps under those names cause smoke failure if called.
- Only an explicitly selected differential test with an explicitly supplied
  baseline may invoke a real Codex executable. No default baseline discovery is
  allowed. The HTTP smoke has no differential mode and never invokes one.
- Platform support, patch edge cases and lifecycle behavior require native
  tests. Attribution, source similarity and earlier binary-delegation reports
  are not evidence of behavioral equivalence.

## Source header convention

Each translated/copied implementation file must retain a short attribution and
modification notice, for example (using its actual upstream path):

```go
// Derived from OpenAI Codex, codex-rs/apply-patch/src/parser.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2.
// Copyright 2025 OpenAI. Apache-2.0; see third_party/codex/{LICENSE,NOTICE}.
// Modified for MCPX: translated to Go and adapted to local Workspace policy.
```

This document does not replace per-file notices. Source owners maintain those
headers; this documentation work item does not edit backend Go files.
