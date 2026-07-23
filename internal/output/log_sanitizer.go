package output

import (
	"regexp"
	"strings"
)

// secretKeyRe matches environment-variable names that conventionally hold
// secrets, so their values can be masked in output by default.
var secretKeyRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|credential|api[_-]?key|private[_-]?key|access[_-]?key|auth)`)

// IsSecretKey reports whether an env-var name looks like it holds a secret.
func IsSecretKey(key string) bool {
	return secretKeyRe.MatchString(key)
}

// MaskSecret masks a secret value, preserving empty values as-is.
func MaskSecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	return "••••••"
}

// jenkinsAnnotationRe matches Jenkins pipeline annotation markers.
// These are ANSI escape sequences containing base64-encoded pipeline metadata:
// \x1b[8mha:////...base64...\x1b[0m
var jenkinsAnnotationRe = regexp.MustCompile(`\x1b\[8mha:.*?\x1b\[0m`)

// SanitizeLog strips Jenkins pipeline annotation markers from console output.
func SanitizeLog(text string) string {
	return jenkinsAnnotationRe.ReplaceAllString(text, "")
}
