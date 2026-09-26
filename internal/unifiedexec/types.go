// Package unifiedexec implements native command execution.
// Derived from OpenAI Codex, codex-rs/core/src/unified_exec/mod.rs
// at 68e0c9f5d8fd9449e97a81e92e8fcb86795713b2. Copyright OpenAI. Apache-2.0.
package unifiedexec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type Options struct {
	WorkDir string
	// Env is the complete child environment; nil
	// inherits os.Environ. Values are never logged by this package.
	Env []string
}

type ExecRequest struct {
	Cmd             string `json:"cmd"`
	WorkDir         string `json:"workdir,omitempty"`
	Shell           string `json:"shell,omitempty"`
	Login           *bool  `json:"login,omitempty"`
	TTY             bool   `json:"tty,omitempty"`
	YieldTimeMS     int    `json:"yield_time_ms,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

type WriteRequest struct {
	SessionID       int    `json:"session_id"`
	Chars           string `json:"chars,omitempty"`
	YieldTimeMS     int    `json:"yield_time_ms,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

type ExecResult struct {
	ChunkID         string  `json:"chunk_id"`
	WallTimeSeconds float64 `json:"wall_time_seconds"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	Output          string  `json:"output"`
	SessionID       *int    `json:"session_id,omitempty"`
	// OriginalTokenCount is a byte-based estimate (ceil(bytes/4)), never an
	// exact tokenizer count. Present only when output was truncated.
	OriginalTokenCount *int `json:"original_token_count,omitempty"`
}

// Reject unsupported tool arguments rather than silently discarding them.
func (r *ExecRequest) UnmarshalJSON(data []byte) error {
	type plain ExecRequest
	var value plain
	if err := decodeRequest(data, &value); err != nil {
		return err
	}
	*r = ExecRequest(value)
	return nil
}

func (r *WriteRequest) UnmarshalJSON(data []byte) error {
	type plain WriteRequest
	var value plain
	if err := decodeRequest(data, &value); err != nil {
		return err
	}
	*r = WriteRequest(value)
	return nil
}

func decodeRequest(data []byte, dst any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("unifiedexec: request must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unifiedexec: expected one request object")
	}
	return nil
}
