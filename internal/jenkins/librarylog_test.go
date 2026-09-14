package jenkins

import (
	"os"
	"strings"
	"testing"
)

func scanLines(t *testing.T, lines ...string) *LibraryLogScanner {
	t.Helper()
	s := NewLibraryLogScanner()
	for _, l := range lines {
		s.Line(l)
	}
	return s
}

// The fixture is the head of a real console, raw: two libraries, both requested
// @master, both served from the library cache, every line carrying the
// pipeline's own "[2026-09-13T...Z] " prefix as logText/progressiveText serves
// it. It is the case ResolveLibrarySHAs refuses, because the branch name alone
// cannot say which checkout is whose.
func TestLibraryLogScannerReadsARealConsole(t *testing.T) {
	body, err := os.ReadFile("testdata/library_log_sample.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	s := NewLibraryLogScanner()
	for _, line := range strings.Split(string(body), "\n") {
		s.Line(line)
	}

	for _, tc := range []struct{ name, url, sha string }{
		{"e3-sdk-global-jenkins-shared-lib",
			"https://cariad.ghe.com/swf/tools-global-jenkins-shared-lib-src.git",
			"327b5c22a40ee9ef448b917b2b4a5b22e8d63239"},
		{"pipeline-generic-shared-lib-premium",
			"https://cariad.ghe.com/swf/tools-premium-jenkins-shared-lib-src.git",
			"f5d759ea6d051759908fedaf684e52bdfbdf0c2c"},
	} {
		got, ok := s.Evidence(tc.name, "master")
		if !ok {
			t.Fatalf("%s: no evidence", tc.name)
		}
		if got.RemoteURL != tc.url {
			t.Errorf("%s url = %q, want %q", tc.name, got.RemoteURL, tc.url)
		}
		if got.SHA1 != tc.sha {
			t.Errorf("%s sha = %q, want %q", tc.name, got.SHA1, tc.sha)
		}
		if got.Ref != "refs/heads/master" {
			t.Errorf("%s ref = %q", tc.name, got.Ref)
		}
		if got.Conflict {
			t.Errorf("%s reported a conflict it does not have", tc.name)
		}
	}
}

// A "Found match" outside any library block belongs to something else — a
// build's own SCM checkout, say — and must not be attributed to the library
// that happened to be loaded earlier.
func TestLibraryLogScannerIgnoresAMatchOutsideABlock(t *testing.T) {
	s := scanLines(t,
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+strings.Repeat("a", 40),
		"Found match: refs/heads/main revision "+strings.Repeat("b", 40),
	)
	got, ok := s.Evidence("lib-a", "main")
	if !ok {
		t.Fatal("no evidence")
	}
	if got.SHA1 != strings.Repeat("a", 40) {
		t.Errorf("sha = %q, want the one inside the block", got.SHA1)
	}
	if got.Conflict {
		t.Error("a match outside the block must not register as a conflict")
	}
}

// A block that never reaches "Found match" contributes nothing. It must not
// borrow the evidence of the block that follows it.
func TestLibraryLogScannerDoesNotBorrowTheNextBlock(t *testing.T) {
	s := scanLines(t,
		"Loading library lib-a@main",
		"Loading library lib-b@main",
		" > git ls-remote -- https://example.invalid/b.git # timeout=10",
		"Found match: refs/heads/main revision "+strings.Repeat("b", 40),
	)
	if _, ok := s.Evidence("lib-a", "main"); ok {
		t.Error("lib-a had no Found match and must stay unresolved")
	}
	got, ok := s.Evidence("lib-b", "main")
	if !ok || got.RemoteURL != "https://example.invalid/b.git" {
		t.Errorf("lib-b evidence = %+v, ok=%v", got, ok)
	}
}

// A library loaded twice at different commits is legal. Reporting the first
// would name code the rest of the build did not run.
func TestLibraryLogScannerFlagsTwoCommitsForOneLibrary(t *testing.T) {
	s := scanLines(t,
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+strings.Repeat("a", 40),
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+strings.Repeat("c", 40),
	)
	got, ok := s.Evidence("lib-a", "main")
	if !ok {
		t.Fatal("no evidence")
	}
	if !got.Conflict {
		t.Error("two different commits for one library must set Conflict")
	}
}

// Reloading a library at the same commit is not a conflict.
func TestLibraryLogScannerToleratesAnIdenticalReload(t *testing.T) {
	sha := strings.Repeat("a", 40)
	s := scanLines(t,
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+sha,
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+sha,
	)
	got, _ := s.Evidence("lib-a", "main")
	if got.Conflict {
		t.Error("the same commit twice is not a conflict")
	}
}

// Same library, two different versions, are two different things.
func TestLibraryLogScannerKeepsVersionsApart(t *testing.T) {
	s := scanLines(t,
		"Loading library lib-a@main",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/main revision "+strings.Repeat("a", 40),
		"Loading library lib-a@v2",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/tags/v2 revision "+strings.Repeat("d", 40),
	)
	main, _ := s.Evidence("lib-a", "main")
	v2, ok := s.Evidence("lib-a", "v2")
	if !ok {
		t.Fatal("no evidence for v2")
	}
	if main.Conflict || v2.Conflict {
		t.Error("different versions are different libraries, not a conflict")
	}
	if v2.Ref != "refs/tags/v2" {
		t.Errorf("tag ref = %q, want it recorded verbatim", v2.Ref)
	}
}

// A version containing '@' must not be truncated: the split is on the last
// separator, and a library name cannot contain '@'.
func TestLibraryLogScannerHandlesAtInTheVersion(t *testing.T) {
	s := scanLines(t,
		"Loading library lib-a@feature/user@example",
		" > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"Found match: refs/heads/feature/user@example revision "+strings.Repeat("a", 40),
	)
	if _, ok := s.Evidence("lib-a", "feature/user@example"); !ok {
		t.Error("a '@' in the ref moved into the library name")
	}
}

// Console lines arrive from progressiveText with the pipeline's own timestamp
// prefix. Anchored patterns must still match through it.
func TestLibraryLogScannerSeesThroughAConsoleTimestamp(t *testing.T) {
	s := scanLines(t,
		"[2026-09-13T19:00:07.813Z] Loading library lib-a@main",
		"[2026-09-13T19:00:08.278Z]  > git ls-remote -- https://example.invalid/a.git # timeout=10",
		"[2026-09-13T19:00:08.678Z] Found match: refs/heads/main revision "+strings.Repeat("a", 40),
	)
	got, ok := s.Evidence("lib-a", "main")
	if !ok {
		t.Fatal("timestamp prefix hid the whole block")
	}
	if got.SHA1 != strings.Repeat("a", 40) || got.RemoteURL != "https://example.invalid/a.git" {
		t.Errorf("evidence = %+v", got)
	}
}

// Console lines arrive CRLF-terminated from Jenkins. The captured fixture may
// be LF-normalised by git on checkout, so the carriage return is pinned here
// instead of relying on the fixture to carry it.
func TestLibraryLogScannerToleratesCarriageReturns(t *testing.T) {
	s := scanLines(t,
		"[2026-09-13T19:00:07.813Z] Loading library lib-a@main\r",
		"[2026-09-13T19:00:08.278Z]  > git ls-remote -- https://example.invalid/a.git # timeout=10\r",
		"[2026-09-13T19:00:08.678Z] Found match: refs/heads/main revision "+strings.Repeat("a", 40)+"\r",
	)
	if _, ok := s.Evidence("lib-a", "main"); !ok {
		t.Error("a trailing carriage return hid the block")
	}
}
