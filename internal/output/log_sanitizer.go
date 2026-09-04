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

// ansiEscapeRe matches CSI sequences (\x1b[...) and OSC strings (\x1b]...BEL
// or ST). Dropping the escape byte alone would leave the parameters behind as
// literal text such as "[2K".
var ansiEscapeRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// StripControl removes escape sequences and C0 control characters from text
// that will be printed into a table cell. Tab is kept; everything else in that
// range can move the cursor or erase the row, and a change reason or job
// description is typed by whoever edited the job. Width-aware padding counts
// these as zero-width, so the layout looks correct while the line is destroyed.
func StripControl(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, ansiEscapeRe.ReplaceAllString(text, ""))
}
