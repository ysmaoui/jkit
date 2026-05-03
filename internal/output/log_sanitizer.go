package output

import "regexp"

// jenkinsAnnotationRe matches Jenkins pipeline annotation markers.
// These are ANSI escape sequences containing base64-encoded pipeline metadata:
// \x1b[8mha:////...base64...\x1b[0m
var jenkinsAnnotationRe = regexp.MustCompile(`\x1b\[8mha:.*?\x1b\[0m`)

// SanitizeLog strips Jenkins pipeline annotation markers from console output.
func SanitizeLog(text string) string {
	return jenkinsAnnotationRe.ReplaceAllString(text, "")
}
