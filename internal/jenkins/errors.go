package jenkins

import (
	"fmt"
	"strings"
)

type AuthError struct {
	Host string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("not authenticated to %s — run 'jkit auth login'", e.Host)
}

type NotFoundError struct {
	Resource string
	Name     string
	Host     string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found on %s — run 'jkit list'", e.Resource, e.Name, e.Host)
}

// ContainerBuildError is returned when a build is requested on a job that is
// actually a container (folder or multibranch pipeline) and therefore has no
// builds of its own. It lists the available child jobs (branches) so the caller
// can retry with the correct target.
type ContainerBuildError struct {
	JobPath  string
	Kind     string // human label, e.g. "multibranch pipeline" or "folder"
	Children []string
	Host     string
}

func (e *ContainerBuildError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q is a %s, not a buildable job — only its branches have builds.\n", e.JobPath, e.Kind)
	if e.Kind == "folder" {
		fmt.Fprintf(&b, "Pick a job inside it, e.g.:  jkit log %s/<job> <build>\n", e.JobPath)
	} else {
		fmt.Fprintf(&b, "Retry with a branch, e.g.:  jkit log %s <build> --branch <name>\n", e.JobPath)
	}
	if len(e.Children) == 0 {
		b.WriteString("No child jobs found — run 'jkit list --folder " + e.JobPath + "'")
		return b.String()
	}
	label := "branches"
	if e.Kind == "folder" {
		label = "jobs"
	}
	fmt.Fprintf(&b, "Available %s (%d):", label, len(e.Children))
	for _, c := range e.Children {
		b.WriteString("\n  " + c)
	}
	return b.String()
}

type PermissionError struct {
	Resource string
	Host     string
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("access denied for %q on %s — check Jenkins permissions", e.Resource, e.Host)
}

type UnreachableError struct {
	Host  string
	Cause error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("cannot reach %s — check network or VPN: %v", e.Host, e.Cause)
}

func (e *UnreachableError) Unwrap() error {
	return e.Cause
}

type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

type ServerError struct {
	Host       string
	Path       string
	StatusCode int
	Body       string
}

func (e *ServerError) Error() string {
	hint := "retry later or check Jenkins server logs"
	if e.StatusCode == 502 || e.StatusCode == 503 || e.StatusCode == 504 {
		hint = "Jenkins may be restarting — retry in a moment"
	}
	msg := fmt.Sprintf("Jenkins error HTTP %d on %s (%s)", e.StatusCode, e.Path, hint)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}
