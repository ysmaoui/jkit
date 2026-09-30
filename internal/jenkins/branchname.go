package jenkins

import "strings"

// branch-api's NameEncoder escapes only these; Stapler URL-encodes the result
// again, so "feature/x#4" is the job "feature%2Fx%234" and reaches the wire as
// feature%252Fx%25234.
// https://github.com/jenkinsci/branch-api-plugin/blob/master/src/main/java/jenkins/branch/NameEncoder.java
var branchNameEscapes = map[rune]string{
	'#':  "%23",
	'%':  "%25",
	'/':  "%2F",
	'?':  "%3F",
	'[':  "%5B",
	']':  "%5D",
	'\\': "%5C",
}

// BranchJobName returns the name of the job multibranch creates for a branch.
func BranchJobName(branch string) string {
	switch branch {
	case "":
		return "%00"
	case ".":
		return "%2E"
	case "..":
		return "%2E."
	}
	var b strings.Builder
	for _, c := range branch {
		if esc, ok := branchNameEscapes[c]; ok {
			b.WriteString(esc)
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// IsBranchJobName reports whether name could be BranchJobName output: no
// character NameEncoder escapes appears raw, and every "%" starts one of its
// escapes. A raw branch name fails this unless it holds none of those
// characters, in which case it is its own job name anyway.
func IsBranchJobName(name string) bool {
	switch name {
	case "%00", "%2E", "%2E.":
		return true
	}
	for i := 0; i < len(name); i++ {
		c := rune(name[i])
		if c != '%' {
			if _, ok := branchNameEscapes[c]; ok {
				return false
			}
			continue
		}
		if _, ok := branchNameUnescape(name[i:]); !ok {
			return false
		}
		i += 2
	}
	return true
}

// DecodeBranchJobName reverses BranchJobName, turning a branch job's name back
// into the branch name indexing logs print. A "%" that starts no NameEncoder
// escape stays literal, as in NameEncoder.decode.
func DecodeBranchJobName(name string) string {
	switch name {
	case "%00":
		return ""
	case "%2E":
		return "."
	case "%2E.":
		return ".."
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if c, ok := branchNameUnescape(name[i:]); ok {
			b.WriteRune(c)
			i += 2
			continue
		}
		b.WriteByte(name[i])
	}
	return b.String()
}

// branchNameUnescape reports which character the NameEncoder escape at the
// start of s stands for.
func branchNameUnescape(s string) (rune, bool) {
	if len(s) < 3 || s[0] != '%' {
		return 0, false
	}
	for c, esc := range branchNameEscapes {
		if s[:3] == esc {
			return c, true
		}
	}
	return 0, false
}
