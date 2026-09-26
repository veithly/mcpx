package server

import (
	"encoding/json"
	"mcpx/internal/secrets"
	"strings"
	"testing"
)

func TestParitysecret_provideNormalNoEcho(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	id := operationTestSession(t, rt, "demo").ID
	other := operationTestSession(t, rt, "demo").ID
	dummy := "dummy-parity-value-not-a-real-credential"
	for _, pending := range []bool{false, true} {
		args := map[string]any{"remote_session_id": id, "purpose": "test dummy secret", "values": map[string]any{"password": dummy}}
		if pending {
			args["secret_id"] = rt.secrets.PutPending(secrets.PendingSecret{RemoteSessionID: id})
		}
		result := callRawToolResult(t, rt.toolHandlers["secret_provide"], args)
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || strings.Contains(string(raw), dummy) {
			t.Fatal("provide failed or response echoed dummy value")
		}
		if value, ok := rt.secrets.Get(id, "password"); !ok || value != dummy {
			t.Fatal("dummy was not cached")
		}
		if _, ok := rt.secrets.Get(other, "password"); ok {
			t.Fatal("cross-session cache leak")
		}
		t.Logf("pending=%v isError=%v data=%v no_echo=true session_isolation=true", pending, result.IsError, decodeToolResult(t, result)["data"])
	}
}

func TestParitysecret_provideInvalidRequests(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	id := operationTestSession(t, rt, "demo").ID
	for _, tc := range []struct {
		name    string
		args    map[string]any
		message string
	}{
		{"missing_session", map[string]any{"purpose": "test dummy secret", "values": map[string]any{"token": "dummy"}}, "remote_session"},
		{"missing_purpose", map[string]any{"remote_session_id": id, "values": map[string]any{"token": "dummy"}}, "purpose"},
		{"wrong_id", map[string]any{"remote_session_id": id, "purpose": "test dummy secret", "secret_id": "sec_nonexistent_dummy", "values": map[string]any{"token": "dummy"}}, "unknown secret_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := callRawToolResult(t, rt.toolHandlers["secret_provide"], tc.args)
			raw, _ := json.Marshal(decodeToolResult(t, result)["error"])
			t.Logf("isError=%v error=%s", result.IsError, raw)
			if !result.IsError || !strings.Contains(strings.ToLower(string(raw)), tc.message) {
				t.Errorf("expected rejection containing %q", tc.message)
			}
		})
	}
}

func TestParitysecret_provideWrongSessionRecoverable(t *testing.T) {
	rt := newWorkspaceRuntime(t, "demo")
	id := operationTestSession(t, rt, "demo").ID
	other := operationTestSession(t, rt, "demo").ID
	sid := rt.secrets.PutPending(secrets.PendingSecret{RemoteSessionID: id})
	args := map[string]any{"remote_session_id": other, "purpose": "test dummy secret", "secret_id": sid, "values": map[string]any{"password": "dummy"}}
	denied := callRawToolResult(t, rt.toolHandlers["secret_provide"], args)
	if !denied.IsError {
		t.Fatal("wrong session accepted")
	}
	args["remote_session_id"] = id
	recovered := callRawToolResult(t, rt.toolHandlers["secret_provide"], args)
	if recovered.IsError {
		t.Fatal("correct session could not recover")
	}
	t.Logf("wrong_session_isError=%v correct_session_retry_isError=%v", denied.IsError, recovered.IsError)
}

func TestParitysecret_provideEmptyValuesPreservesPending(t *testing.T) {
	for _, variant := range []string{"missing", "empty_object", "empty_value"} {
		t.Run(variant, func(t *testing.T) {
			rt := newWorkspaceRuntime(t, "demo")
			id := operationTestSession(t, rt, "demo").ID
			sid := rt.secrets.PutPending(secrets.PendingSecret{RemoteSessionID: id})
			args := map[string]any{"remote_session_id": id, "purpose": "test dummy secret", "secret_id": sid}
			if variant == "empty_object" {
				args["values"] = map[string]any{}
			}
			if variant == "empty_value" {
				args["values"] = map[string]any{"password": ""}
			}
			empty := callRawToolResult(t, rt.toolHandlers["secret_provide"], args)
			t.Logf("empty isError=%v data=%v", empty.IsError, decodeToolResult(t, empty)["data"])
			args["values"] = map[string]any{"password": "dummy"}
			retry := callRawToolResult(t, rt.toolHandlers["secret_provide"], args)
			raw, _ := json.Marshal(decodeToolResult(t, retry)["error"])
			t.Logf("corrected retry isError=%v error=%s", retry.IsError, raw)
			if !empty.IsError {
				t.Error("empty values must fail before consuming pending request")
			}
			if retry.IsError {
				t.Error("corrected retry must retain usable secret_id")
			}
		})
	}
}
