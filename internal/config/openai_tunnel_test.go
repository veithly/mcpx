package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearTunnelEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"MCPX_OPENAI_TUNNEL_ID", "MCPX_OPENAI_TUNNEL_KEY"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveOpenAITunnel(t *testing.T) {
	clearTunnelEnvironment(t)
	yes, no := true, false
	for _, tc := range []struct {
		name             string
		config           OpenAITunnelConfig
		enabled, invalid bool
	}{
		{"absent", OpenAITunnelConfig{}, false, false},
		{"pair auto-enables", OpenAITunnelConfig{ID: " tunnel_test ", Key: " test-key "}, true, false},
		{"missing key", OpenAITunnelConfig{ID: "tunnel_test"}, true, true},
		{"missing id", OpenAITunnelConfig{Key: "test-key"}, true, true},
		{"explicit enable needs pair", OpenAITunnelConfig{Enabled: &yes}, true, true},
		{"explicit disable", OpenAITunnelConfig{ID: "tunnel_test", Enabled: &no}, false, false},
		{"header injection", OpenAITunnelConfig{ID: "tunnel_test", Key: "secret\r\nInjected: value"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := ResolveOpenAITunnel(tc.config)
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected validation result: %v", err)
			}
			if err != nil {
				if tc.config.Key != "" && strings.Contains(err.Error(), tc.config.Key) {
					t.Fatal("validation error leaked key")
				}
				return
			}
			if resolved.IsEnabled() != tc.enabled {
				t.Fatal("unexpected enablement")
			}
			if resolved.ID != strings.TrimSpace(tc.config.ID) || resolved.Key != strings.TrimSpace(tc.config.Key) {
				t.Fatal("credentials not normalized")
			}
		})
	}
}

func TestOpenAITunnelEnvironmentDoesNotPersist(t *testing.T) {
	t.Setenv("MCPX_OPENAI_TUNNEL_ID", "tunnel_env")
	t.Setenv("MCPX_OPENAI_TUNNEL_KEY", "environment-secret")
	cfg := DefaultConfig()
	cfg.OpenAITunnel = OpenAITunnelConfig{ID: "tunnel_file", Key: "file-secret"}
	resolved, err := ResolveOpenAITunnel(cfg.OpenAITunnel)
	if err != nil || resolved.ID != "tunnel_env" || resolved.Key != "environment-secret" {
		t.Fatalf("environment resolution failed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteGlobal(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadGlobal(path)
	if err != nil || loaded.OpenAITunnel.ID != "tunnel_file" || loaded.OpenAITunnel.Key != "file-secret" {
		t.Fatalf("environment secret persisted or YAML failed: %v", err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("credential file must be owner-only")
	}
}

func TestOpenAITunnelProjectCannotOverrideAndJSONDoesNotLeak(t *testing.T) {
	global := DefaultConfig()
	global.OpenAITunnel = OpenAITunnelConfig{ID: "tunnel_global", Key: "private-key"}
	no := false
	project := Config{OpenAITunnel: OpenAITunnelConfig{ID: "tunnel_other", Key: "other-key", Enabled: &no}}
	merged := Merge(global, project)
	if merged.OpenAITunnel != global.OpenAITunnel {
		t.Fatal("project overrode process tunnel settings")
	}
	for _, value := range []any{global, global.OpenAITunnel} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-key") || strings.Contains(fmt.Sprintf("%+v", value), "private-key") {
			t.Fatal("key leaked into diagnostics")
		}
	}
}
