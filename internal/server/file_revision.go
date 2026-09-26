package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const compactFileRevisionBytes = 10 // 80 bits; model-facing only.

func compactFileRevision(fullSHA string) string {
	value := strings.ToLower(strings.TrimSpace(fullSHA))
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) < compactFileRevisionBytes*2 {
		return ""
	}
	raw, err := hex.DecodeString(value[:compactFileRevisionBytes*2])
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func sourceFileSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func compactRevisionPayload(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return value
	}
	rewriteRevisionFields(normalized)
	return normalized
}

func rewriteRevisionFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if full, ok := typed["sha256"].(string); ok {
			if rev := compactFileRevision(full); rev != "" {
				delete(typed, "sha256")
				typed["rev"] = rev
			}
		}
		for _, child := range typed {
			rewriteRevisionFields(child)
		}
	case []any:
		for _, child := range typed {
			rewriteRevisionFields(child)
		}
	}
}
