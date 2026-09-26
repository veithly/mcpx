# Native apply-patch

Apply(context.Context, Request) runs entirely inside MCPX. It never discovers
or invokes Codex, git apply, a shell, Python, a model or an API. Request has no
Binary field. The only external process call is in differential_test.go, gated
by an explicitly supplied MCPX_PATCH_DIFFERENTIAL_BINARY.

## Upstream evidence

The port is derived from OpenAI Codex commit
68e0c9f5d8fd9449e97a81e92e8fcb86795713b2, under Apache-2.0:

- codex-rs/apply-patch/src/parser.rs and streaming_parser.rs: complete parsing,
  incremental line handling, raw headers, context, EOF and lenient EOF heredocs.
- seek_sequence.rs: exact, trailing whitespace, both-side whitespace, then
  Unicode punctuation passes. A later exact match beats an earlier fuzzy match.
  In this revision EOF restricts matching to the final region; its introductory
  comment mentions a fallback which the implementation does not perform.
- file_update.rs: ordered chunks, stable replacement ordering, pure additions
  at EOF, trailing-empty-pattern retry and final-newline behavior.
- text_file.rs: explicit PreserveLineEndings mode and original context lines.
- lib.rs apply_hunks_to_files / print_summary: Add overwrites, Move writes its
  destination then removes its source, including an existing destination and
  move-to-self; success output groups A, M, D. Failure prints only its error;
  earlier successful writes remain. The operation is not a transaction.
- tests/fixtures/scenarios: copied input/patch/expected files; the upstream
  runner selects preserve mode explicitly. Local tests additionally run default
  mode on every fixture except the two preserve-specific line-ending fixtures.

Default UpdateMode is NormalizeToLF, the selected upstream default. This name
does not mean every CR is stripped: source is split on LF, replaced/context
segments use patch LF, and untouched source CR bytes survive. Tests assert
that mixed result. PreserveLineEndings must be requested explicitly; environment
variables never change production semantics.

### Environment header decision

Upstream parser.rs stores environment_id in ApplyPatchArgs, but lib.rs:369
apply_patch_with_options takes only source.hunks. standalone_executable.rs
passes the current directory into that function. Thus that standalone entry
ignores even a remote environment ID. MCPX deliberately rejects non-local
environment IDs before opening targets or writing anything. Missing ID or
the literal local means the explicitly selected Request workspace. There is
no implicit routing to another host or workspace. This is a safety contract
difference, tested separately from equivalence cases.

## Result and safety contract

Syntax and application errors return ExitCode 1 and Output; complete success
returns 0 and the upstream summary. Go errors report policy, containment,
unsupported environment, cancellation or detected changes. ExitCode -1 means
the operation did not complete normally; cancellation after a prior hunk can
still leave partial changes. Paths contains unique workspace-relative candidate
targets, including both move endpoints, never a completed-write receipt.

One complete parser produces the hunks used by both validation and execution.
All targets are inspected and passed to the read-only ValidatePath callback
before writes. WorkDir is workspace-relative or an absolute path inside the
workspace. Policy checks cover lexical aliases and resolved endpoints.

All filesystem access is through os.Root. Relative in-root symlinks are
supported; absolute or escaping symlinks are rejected. Existing identities,
permissions and bytes are checked before writes; a policy callback changing
them rejects the entire preflight. Successful mutations refresh only affected
target snapshots so repeated operations in one patch remain sequential.
Existing files are opened without truncation and verified again on the open
descriptor; their ownership, ACL and permissions are not replaced. Unix files
with multiple hard links are rejected. Special files and directory deletion
are rejected. New files use 0666 and parent directories 0755, subject to umask.

This is filesystem containment, not an OS sandbox or an atomic filesystem
transaction. It cannot exclude another process writing the same open inode
after the final comparison or racing an unlink inside the workspace. os.Root
does not isolate mount points; non-Unix hard-link rejection is not implemented.
Detected changes are handled conservatively; no retry or rollback runs.

## Focused checks

    GOMAXPROCS=2 go test ./internal/patch -count=1
    GOMAXPROCS=2 go test -race ./internal/patch -count=1

All required cases work with Codex unavailable. They include 112 header/policy
cases, copied upstream fixtures, incremental parsing, malformed input, Unicode,
CRLF, EOF/repeated contexts, overwrites/moves, partial failures, permissions,
path rejection, conflict checks, a large patch and 2,000 update chunks.

An explicit optional test compares byte-for-byte output, exit code and complete
file snapshots against the named external standalone baseline. It uses only
temporary fixtures and isolated HOME/CODEX_HOME:

    MCPX_PATCH_DIFFERENTIAL_BINARY=/opt/homebrew/bin/codex GOMAXPROCS=2 go test ./internal/patch -run '^TestOptionalDifferential$' -count=1

No external baseline is selected automatically. Root LICENSE/NOTICE aggregation
is owned by the parallel licensing work; every translated source retains its
derived-from path, revision, copyright and license annotation.
