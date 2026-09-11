//go:build unix

package edit

import "os"

// syncDir fsyncs a directory so a just-committed rename, create or removal
// survives a crash. Best effort: some filesystems refuse directory fsync, in
// which case durability is weakened but correctness of the applied batch is
// unaffected.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
