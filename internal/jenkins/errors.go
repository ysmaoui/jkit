package jenkins

import "fmt"

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
