//go:build windows

package edit

// syncDir is a no-op on windows: FILE_SYNC_UPDATE_INFORMATION based directory
// syncing is not wired up yet. See the known-issue note next to the
// remove-then-rename fallback in write.go.
func syncDir(dir string) error {
	return nil
}
