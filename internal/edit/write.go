package edit

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"mcpx/internal/file"
)

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mcpx-edit-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if mode == 0 {
		mode = 0o644
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		// KNOWN ISSUE (windows): remove-then-rename has a window where the
		// target path does not exist, so a crash in between destroys the
		// original file. A proper fix needs MoveFileEx with
		// MOVEFILE_REPLACE_EXISTING plus directory handle syncing; left as is
		// for this change set.
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
			if err2 := os.Rename(temporaryPath, path); err2 != nil {
				return err2
			}
			_ = syncDir(filepath.Dir(path))
			return nil
		}
		return err
	}
	// The rename is only durable once its directory entry is synced.
	_ = syncDir(filepath.Dir(path))
	return nil
}

// encodeWithFormat converts logical LF text back to the original line ending
// and BOM when possible. Content is assumed UTF-8 logical lines joined by \n.
func encodeWithFormat(logical string, format file.Format) []byte {
	body := logical
	switch format.LineEnding {
	case "CRLF":
		body = strings.ReplaceAll(body, "\n", "\r\n")
	case "CR":
		body = strings.ReplaceAll(body, "\n", "\r")
	}
	var out []byte
	switch format.Charset {
	case "utf-16le":
		out = encodeUTF16(body, binary.LittleEndian)
	case "utf-16be":
		out = encodeUTF16(body, binary.BigEndian)
	default:
		out = []byte(body)
	}
	switch format.BOM {
	case "utf-8":
		out = append([]byte{0xef, 0xbb, 0xbf}, out...)
	case "utf-16le":
		out = append([]byte{0xff, 0xfe}, out...)
	case "utf-16be":
		out = append([]byte{0xfe, 0xff}, out...)
	}
	return out
}

// encodeWithOriginalFormat retains a mixed line-ending sequence where the
// source used one. New or moved lines reuse the nearest available separator;
// uniform files use the faster format-only path above.
func encodeWithOriginalFormat(logical string, format file.Format, original []byte) []byte {
	if format.LineEnding != "mixed" {
		return encodeWithFormat(logical, format)
	}
	separatorSource := original
	switch format.Charset {
	case "utf-16le":
		separatorSource = []byte(decodeUTF16(original[2:], binary.LittleEndian))
	case "utf-16be":
		separatorSource = []byte(decodeUTF16(original[2:], binary.BigEndian))
	}
	separators := lineEndings(separatorSource)
	lines := strings.Split(logical, "\n")
	var builder strings.Builder
	for index, line := range lines {
		builder.WriteString(line)
		if index >= len(lines)-1 {
			continue
		}
		separator := "\n"
		if index < len(separators) {
			separator = separators[index]
		} else if len(separators) > 0 {
			separator = separators[len(separators)-1]
		}
		builder.WriteString(separator)
	}
	return encodeWithFormat(builder.String(), format)
}

func lineEndings(content []byte) []string {
	separators := make([]string, 0)
	for index := 0; index < len(content); index++ {
		switch content[index] {
		case '\r':
			if index+1 < len(content) && content[index+1] == '\n' {
				separators = append(separators, "\r\n")
				index++
			} else {
				separators = append(separators, "\r")
			}
		case '\n':
			separators = append(separators, "\n")
		}
	}
	return separators
}

func normalizeToLogical(content []byte) (logical string, format file.Format, err error) {
	text, format, err := file.DecodeText(content)
	if err != nil {
		return "", format, err
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text, format, nil
}

func encodeUTF16(text string, order binary.ByteOrder) []byte {
	units := utf16.Encode([]rune(text))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		order.PutUint16(encoded[index*2:], unit)
	}
	return encoded
}

func decodeUTF16(content []byte, order binary.ByteOrder) string {
	if len(content)%2 != 0 {
		content = content[:len(content)-1]
	}
	units := make([]uint16, len(content)/2)
	for index := range units {
		units[index] = order.Uint16(content[index*2:])
	}
	return string(utf16.Decode(units))
}

func boolPointer(value bool) *bool {
	return &value
}
