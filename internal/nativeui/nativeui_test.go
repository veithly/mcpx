package nativeui

import (
	"strings"
	"testing"
)

func TestNativeDirectoryPickerUsesOSDialogNotFilesystemEnumeration(t *testing.T) {
	mac, err := DirectoryCommand("darwin")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(mac, " ")
	for _, required := range []string{"NSOpenPanel", "canChooseDirectories = true", "canChooseFiles = false", "allowsMultipleSelection = false", "JSON.stringify"} {
		if !strings.Contains(script, required) {
			t.Errorf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"System Events", "Finder", "readDir", "doShellScript"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("unexpected permission/enumeration dependency %s", forbidden)
		}
	}
	for _, platform := range []string{"windows", "linux"} {
		if _, err := DirectoryCommand(platform); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DirectoryCommand("unsupported"); err == nil {
		t.Fatal("unsupported platform silently accepted")
	}
}
