//go:build unix

package patch

import (
	"os"
	"syscall"
)

func multipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
