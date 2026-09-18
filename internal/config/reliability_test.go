package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPResultBudgetIsIndependentAndConfigurable(t *testing.T) {
	global := DefaultConfig()
	if MaxMCPResultBytes(global.Limits) != 4<<20 || MaxMCPResultBytes(LimitsConfig{}) != 4<<20 {
		t.Fatal("missing bounded MCP default")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("limits:\n  max_result_bytes: 4096\n  max_mcp_result_bytes: 8192\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if MaxResultBytes(loaded.Limits) != 4096 || MaxMCPResultBytes(loaded.Limits) != 8192 {
		t.Fatal("limits were not loaded independently")
	}
	project := Config{Limits: LimitsConfig{MaxMCPResultBytes: 16384}}
	merged := Merge(loaded, project)
	if MaxResultBytes(merged.Limits) != 4096 || MaxMCPResultBytes(merged.Limits) != 16384 {
		t.Fatal("project override changed the wrong budget")
	}
	if MaxMCPResultBytes(Merge(loaded, Config{}).Limits) != 8192 {
		t.Fatal("omitted project limit lost global budget")
	}
}

func TestWriteGlobalReplacesConfigAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("description: original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Description = "replacement"
	if err := WriteGlobal(path, cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("configuration was overwritten in place")
	}
	loaded, err := LoadGlobal(path)
	if err != nil || loaded.Description != "replacement" {
		t.Fatalf("replacement read: %v", err)
	}
}
