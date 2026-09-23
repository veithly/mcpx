package observation

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const redactedValue = "[REDACTED]"

var sensitiveKeyParts = []string{
	"token", "secret", "password", "authorization", "cookie", "api_key", "apikey", "private_key", "client_secret",
}

var sensitiveTextPattern = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\b\s*:\s*bearer\s+|\bbearer\s+|["']?\b(?:token|secret|password|authorization|cookie|api[_-]?key|private[_-]?key|client[_-]?secret)\b["']?\s*[:=]\s*)(["']?)[^"'\s,;}]+`)
var sensitiveTextMarkerPattern = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\b\s*:\s*bearer\s*|\bbearer\s*|["']?\b(?:token|secret|password|authorization|cookie|api[_-]?key|private[_-]?key|client[_-]?secret)\b["']?\s*[:=]\s*["']?)$`)

var sensitiveTextPrefixes = []string{
	"token", "secret", "password", "authorization", "proxy-authorization", "cookie",
	"api_key", "apikey", "private_key", "client_secret", "bearer",
}

// TextStreamSanitizer redacts credentials that are split across output chunks.
// It emits a redaction marker as soon as a sensitive marker is complete, then
// consumes the value until a safe delimiter or the end of the stream.
type TextStreamSanitizer struct {
	pending   string
	redacting bool
}

// SanitizeChunk sanitizes one output chunk. final must be true for the final
// callback of a stream so incomplete state is discarded before the next task.
func (s *TextStreamSanitizer) SanitizeChunk(value string, final bool, maxBytes int) (string, bool) {
	if s == nil {
		return SanitizeText(value, maxBytes)
	}
	combined := s.pending + value
	s.pending = ""
	var clean strings.Builder
	if final && combined == "" {
		s.redacting = false
	}

	for len(combined) > 0 {
		if s.redacting {
			delimiter := sensitiveValueDelimiter(combined)
			if delimiter < 0 {
				if final {
					s.redacting = false
				}
				break
			}
			clean.WriteByte(combined[delimiter])
			combined = combined[delimiter+1:]
			s.redacting = false
			continue
		}

		match := sensitiveTextPattern.FindStringIndex(combined)
		if match != nil {
			clean.WriteString(combined[:match[0]])
			clean.WriteString(RedactText(combined[match[0]:match[1]]))
			combined = combined[match[1]:]
			continue
		}

		if start, ok := sensitiveTextMarker(combined); ok {
			clean.WriteString(combined[:start])
			clean.WriteString(combined[start:])
			clean.WriteString(redactedValue)
			combined = ""
			s.redacting = !final
			continue
		}

		if !final {
			if start := partialSensitiveTextPrefix(combined); start >= 0 {
				clean.WriteString(combined[:start])
				s.pending = combined[start:]
				break
			}
		}
		clean.WriteString(combined)
		break
	}

	return SanitizeText(clean.String(), maxBytes)
}

// Sanitize recursively removes values under sensitive keys and bounds the
// resulting JSON representation without breaking UTF-8.
func Sanitize(value any, maxBytes int) (any, bool) {
	if maxBytes <= 0 {
		maxBytes = MaxEventBytes
	}
	clean := sanitizeValue(value)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return redactedValue, true
	}
	if len(encoded) <= maxBytes {
		return clean, false
	}
	// JSON escaping can make a preview much larger than its source text.
	// Search the fitting prefix in logarithmic steps rather than encoding and
	// retrying every byte (millions of multi-megabyte marshals for '\\'-heavy output).
	text := string(encoded)
	var best map[string]any
	for low, high := 0, maxBytes; low <= high; {
		previewBytes := low + (high-low)/2
		preview := truncateUTF8(text, previewBytes)
		candidate := map[string]any{"truncated": true, "preview": preview}
		candidateBytes, marshalErr := json.Marshal(candidate)
		if marshalErr == nil && len(candidateBytes) <= maxBytes {
			best = candidate
			low = previewBytes + 1
		} else {
			high = previewBytes - 1
		}
	}
	if best != nil {
		return best, true
	}
	return map[string]any{"truncated": true}, true
}

// SanitizeText applies the same UTF-8-safe bound to plain command output.
func SanitizeText(value string, maxBytes int) (string, bool) {
	value = RedactText(value)
	if maxBytes <= 0 {
		maxBytes = MaxEventBytes
	}
	if len(value) <= maxBytes {
		return value, false
	}
	suffix := "\n… [truncated]"
	if maxBytes <= len(suffix) {
		return truncateUTF8(suffix, maxBytes), true
	}
	return truncateUTF8(value, maxBytes-len(suffix)) + suffix, true
}

// RedactText removes common inline credential forms from human-readable
// intent, summaries and command output. Structured JSON uses key-based
// redaction in Sanitize; this covers text fields that have no keys.
func RedactText(value string) string {
	return sensitiveTextPattern.ReplaceAllString(value, "$1$2[REDACTED]")
}

func sensitiveTextMarker(value string) (int, bool) {
	matches := sensitiveTextMarkerPattern.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return 0, false
	}
	return matches[len(matches)-1][0], true
}

func partialSensitiveTextPrefix(value string) int {
	lower := strings.ToLower(value)
	best := -1
	for _, prefix := range sensitiveTextPrefixes {
		maxLength := len(prefix)
		if len(lower) < maxLength {
			maxLength = len(lower)
		}
		for length := maxLength; length >= 3; length-- {
			if !strings.HasSuffix(lower, prefix[:length]) {
				continue
			}
			start := len(value) - length
			if start > 0 && isWordByte(value[start-1]) {
				break
			}
			if best < 0 || start < best {
				best = start
			}
			break
		}
	}
	return best
}

func isWordByte(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_'
}

func sensitiveValueDelimiter(value string) int {
	return strings.IndexAny(value, " \t\r\n,;}\"'")
}

// SanitizeIntent applies text redaction and the protocol intent limit.
func SanitizeIntent(value string) string {
	clean, _ := SanitizeText(strings.TrimSpace(value), MaxIntentBytes)
	return clean
}

// SanitizeJSON parses, sanitizes and bounds an already encoded JSON value.
func SanitizeJSON(value []byte, maxBytes int) ([]byte, bool) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		text, truncated := SanitizeText(string(value), maxBytes)
		encoded, _ := json.Marshal(text)
		return encoded, truncated
	}
	clean, truncated := Sanitize(decoded, maxBytes)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return []byte(`"[REDACTED]"`), true
	}
	return encoded, truncated
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				clean[key] = redactedValue
				continue
			}
			clean[key] = sanitizeValue(item)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, item := range typed {
			clean[i] = sanitizeValue(item)
		}
		return clean
	case string:
		return RedactText(typed)
	default:
		return value
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(key))
	for _, part := range sensitiveKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
