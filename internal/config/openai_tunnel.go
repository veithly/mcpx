package config

import (
	"fmt"
	"os"
	"strings"
)

// OpenAITunnelConfig is process-wide. A complete ID/Key pair enables the
// outbound-only transport automatically; enabled: false explicitly disables it.
// Key is intentionally excluded from JSON status/config responses.
type OpenAITunnelConfig struct {
	ID      string `yaml:"id,omitempty" json:"id"`
	Key     string `yaml:"key,omitempty" json:"-"`
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

func (c OpenAITunnelConfig) IsEnabled() bool {
	if c.Enabled != nil {
		return *c.Enabled
	}
	return strings.TrimSpace(c.ID) != "" || strings.TrimSpace(c.Key) != ""
}

// String keeps credentials out of fmt/slog diagnostics, including nested Configs.
func (c OpenAITunnelConfig) String() string {
	return fmt.Sprintf("OpenAITunnel{enabled:%t,key_configured:%t}", c.IsEnabled(), c.Key != "")
}

// ResolveOpenAITunnel returns a runtime-only copy. Never persist this result:
// environment secrets must not be written back by unrelated config updates.
func ResolveOpenAITunnel(c OpenAITunnelConfig) (OpenAITunnelConfig, error) {
	if value, ok := os.LookupEnv("MCPX_OPENAI_TUNNEL_ID"); ok {
		c.ID = value
	}
	if value, ok := os.LookupEnv("MCPX_OPENAI_TUNNEL_KEY"); ok {
		c.Key = value
	}
	c.ID = strings.TrimSpace(c.ID)
	c.Key = strings.TrimSpace(c.Key)
	if !c.IsEnabled() {
		return c, nil
	}
	if c.ID == "" || c.Key == "" {
		return OpenAITunnelConfig{}, fmt.Errorf("openai_tunnel requires both id and key (or MCPX_OPENAI_TUNNEL_ID and MCPX_OPENAI_TUNNEL_KEY)")
	}
	if strings.ContainsAny(c.Key, "\r\n") {
		return OpenAITunnelConfig{}, fmt.Errorf("openai_tunnel.key must not contain line breaks")
	}
	return c, nil
}
