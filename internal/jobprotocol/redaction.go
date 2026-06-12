package jobprotocol

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	windowsPathPattern = regexp.MustCompile(`[A-Za-z]:\\[^\s"',;]+`)
	unixPathPattern    = regexp.MustCompile(`/(?:Users|home|var|private|tmp|opt|srv|mnt|Volumes)/[^\s"',;]+`)
	secretPattern      = regexp.MustCompile(`(?i)(token|secret|password|credential|authorization|api[_-]?key)=([^\s"',;]+)`)
	sqlLiteralPattern  = regexp.MustCompile(`(?i)(select|update|insert|delete|pragma|attach)\s+[^;]{24,}`)
)

// RedactText removes host-local paths, SQL bodies and inline secrets from one text value.
// value is any diagnostic or payload string, and the function returns a bounded safe summary suitable for journal, readiness and responses.
func RedactText(value string) string {
	value = strings.TrimSpace(value)
	value = windowsPathPattern.ReplaceAllString(value, "[redacted-path]")
	value = unixPathPattern.ReplaceAllString(value, "[redacted-path]")
	value = secretPattern.ReplaceAllString(value, "$1=[redacted-secret]")
	value = sqlLiteralPattern.ReplaceAllStringFunc(value, func(match string) string {
		fields := strings.Fields(match)
		if len(fields) == 0 {
			return "[redacted-sql]"
		}
		return strings.ToUpper(fields[0]) + " [redacted-sql]"
	})
	if len(value) > 512 {
		return value[:512] + "...[truncated]"
	}
	return value
}

// RedactMap returns a sanitized shallow copy of map data for journal or response use.
// input contains decoded JSON payload data, and the function returns a copy with nested strings redacted and oversized arrays summarized.
func RedactMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = RedactValue(value)
	}
	return result
}

// RedactValue sanitizes one decoded JSON value for safe persistence or transport.
// value may be a scalar, map or slice, and the function returns a redacted copy that avoids host paths and unbounded data.
func RedactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactText(typed)
	case map[string]any:
		return RedactMap(typed)
	case []any:
		limit := len(typed)
		if limit > 20 {
			limit = 20
		}
		safe := make([]any, 0, limit)
		for i := 0; i < limit; i++ {
			safe = append(safe, RedactValue(typed[i]))
		}
		if len(typed) > limit {
			return map[string]any{"items": safe, "truncated": true, "originalCount": len(typed)}
		}
		return safe
	default:
		return typed
	}
}

// SafeError converts one error into a redacted stable summary string.
// err is the execution error to expose, and the function returns an empty string for nil or a sanitized message for failures.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	return RedactText(err.Error())
}

// PayloadSummary builds a small sanitized summary for an execution job payload.
// payload is the raw capability input and the function returns a redacted map with at most stable scalar-like fields.
func PayloadSummary(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	summary := make(map[string]any, len(payload))
	for key, value := range payload {
		switch typed := value.(type) {
		case string:
			if strings.EqualFold(key, "sql") || strings.EqualFold(key, "query") {
				summary[key] = "[redacted-sql]"
				continue
			}
			summary[key] = RedactText(typed)
		case float64, bool, int, int64, uint64:
			summary[key] = typed
		case []any:
			summary[key] = fmt.Sprintf("array[%d]", len(typed))
		case map[string]any:
			summary[key] = fmt.Sprintf("object[%d]", len(typed))
		default:
			summary[key] = fmt.Sprintf("%T", value)
		}
	}
	return summary
}
