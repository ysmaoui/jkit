package output

import (
	"net/url"
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

// urlCredentialMask stands in for redacted userinfo. It is spliced in as text
// rather than set via url.User, which percent-encodes anything it is given.
const urlCredentialMask = "***"

// RedactURLCredentials replaces the userinfo of an http(s) URL with a mask,
// keeping scheme, host and path so the location stays readable. Jenkins jobs
// normally reference a credentialsId, but an SCM url may embed the secret
// directly as https://user:token@host/repo.git. scp-style git remotes
// (git@host:org/repo.git) carry no userinfo in the URL sense and are left
// alone; the "git" there is a conventional account name, not a secret.
func RedactURLCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	stripped := u.String()
	prefix := u.Scheme + "://"
	if !strings.HasPrefix(stripped, prefix) {
		return stripped
	}
	return prefix + urlCredentialMask + "@" + stripped[len(prefix):]
}
