package prompts

import "testing"

func TestDescriptions(t *testing.T) {
	m, err := Descriptions()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"exec_command", "write_stdin", "apply_patch", "session", "observe", "artifact"} {
		if m[name] == "" {
			t.Fatalf("missing description for %s", name)
		}
	}
	for _, name := range []string{"read", "edit", "execute"} {
		if _, exists := m[name]; exists {
			t.Fatalf("description exposes removed programming tool %s", name)
		}
	}
}
