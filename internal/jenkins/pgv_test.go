package jenkins

import "testing"

func TestMapPGVState(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"success", "SUCCESS"},
		{"failure", "FAILURE"},
		{"unstable", "UNSTABLE"},
		{"aborted", "ABORTED"},
		{"not_built", "NOT_BUILT"},
		{"skipped", "NOT_BUILT"},
		{"running", "IN_PROGRESS"},
		{"paused", "PAUSED_PENDING_INPUT"},
		{"queued", "QUEUED"},
		{"unknown", ""},
		{"", ""},
		{"bogus", ""},
	}
	for _, c := range cases {
		if got := MapPGVState(c.in); got != c.want {
			t.Errorf("MapPGVState(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
