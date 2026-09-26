//go:build !unix

package patch

import "os"

// os.FileInfo does not expose link counts portably. Root containment still
// applies; multi-link rejection is supported on Unix only.
func multipleLinks(info os.FileInfo) bool { return false }
