package patch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (w *workspace) write(ctx context.Context, t *target, data []byte, createParents bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := w.check(t); err != nil {
		return nil, err
	}
	changed := []string{t.physical}
	if createParents {
		var missing []string
		for parent := filepath.Dir(t.physical); parent != "."; parent = filepath.Dir(parent) {
			_, err := w.root.Stat(parent)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				return nil, err
			}
			missing = append(missing, parent)
		}
		if len(missing) > 0 {
			if err := w.root.MkdirAll(filepath.Dir(t.physical), 0755); err != nil {
				return nil, err
			}
			changed = append(changed, missing...)
		}
	}
	flags := os.O_RDWR
	if t.info == nil {
		flags |= os.O_CREATE | os.O_EXCL
	} else if !t.info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	// Existing files are opened WITHOUT O_TRUNC: check the opened identity and
	// bytes first. Keep the inode, ownership, ACL and mode of existing files.
	f, err := w.root.OpenFile(t.physical, flags, 0666)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if t.info != nil {
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !sameInfo(t.info, info) || multipleLinks(info) {
			return nil, fmt.Errorf("patch target changed before write")
		}
		before, err := io.ReadAll(f)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(before, t.contents) {
			return nil, fmt.Errorf("patch target contents changed before write")
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	if err := f.Truncate(int64(len(data))); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return changed, nil
}
