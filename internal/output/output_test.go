package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------- Formatter ---------------

func TestFormatterJSON(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, true, "")

	data := map[string]string{"name": "build1", "status": "SUCCESS"}
	err := f.Output(data, nil)
	require.NoError(t, err)

	want := "{\n  \"name\": \"build1\",\n  \"status\": \"SUCCESS\"\n}\n"
	assert.Equal(t, want, buf.String())
}

func TestFormatterTemplate(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "Name={{.Name}}")

	data := struct{ Name string }{Name: "job1"}
	err := f.Output(data, nil)
	require.NoError(t, err)

	assert.Equal(t, "Name=job1", buf.String())
}

func TestFormatterTemplateBadParse(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "{{.Bad")

	err := f.Output("ignored", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing template")
}

func TestFormatterTable(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "")

	type item struct {
		Name   string
		Status string
	}

	items := []any{
		item{Name: "a", Status: "SUCCESS"},
		item{Name: "long-name", Status: "FAIL"},
	}

	cols := []Column{
		{Header: "NAME", Field: func(v any) string { return v.(item).Name }},
		{Header: "STATUS", Field: func(v any) string { return v.(item).Status }},
	}

	err := f.Output(items, cols)
	require.NoError(t, err)

	out := buf.String()
	// Header present
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "STATUS")
	// Rows present
	assert.Contains(t, out, "a")
	assert.Contains(t, out, "long-name")
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "FAIL")
}

func TestFormatterTableANSIAlignment(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "")

	type item struct {
		Name   string
		Status string
	}

	// Simulate ANSI-colored values — ESC[32m...ESC[0m wraps "OK"
	colored := "\x1b[32mOK\x1b[0m"

	items := []any{
		item{Name: "short", Status: colored},
		item{Name: "longer-name", Status: "PENDING"},
	}

	cols := []Column{
		{Header: "NAME", Field: func(v any) string { return v.(item).Name }},
		{Header: "STATUS", Field: func(v any) string { return v.(item).Status }},
	}

	err := f.Output(items, cols)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3) // header + 2 rows

	// All lines should have the same visual width (column alignment)
	// The STATUS column starts at the same position in each line
	// Header: "NAME        STATUS"  — NAME col is 11 wide ("longer-name") + 2 gap
	// Row 1:  "short       \x1b[32mOK\x1b[0m"  — should pad "short" to 11
	// Row 2:  "longer-name PENDING" — no padding needed

	// Verify the colored row's NAME field is properly padded
	// "short" (5) should be padded to 11 = "short      " (6 spaces)
	assert.Contains(t, lines[1], "short      ")
}

func TestFormatterTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "")

	err := f.Output([]any{}, []Column{{Header: "H", Field: func(any) string { return "" }}})
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}

func TestFormatterNonSliceFallsBackToJSON(t *testing.T) {
	var buf bytes.Buffer
	f := NewFormatter(&buf, false, "")

	data := map[string]int{"count": 42}
	err := f.Output(data, nil)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `"count": 42`)
}

// --------------- Color ---------------

func TestColorStatusNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	for _, status := range []string{"SUCCESS", "FAILURE", "UNSTABLE", "ABORTED", "NOT_BUILT", "BUILDING"} {
		assert.Equal(t, status, ColorStatus(status), "NO_COLOR should return plain text for %s", status)
	}
}

func TestColorStatusValues(t *testing.T) {
	// Ensure NO_COLOR is unset so only TTY check applies.
	// Tests run in non-TTY, so noColor() returns true and plain text is returned.
	t.Setenv("NO_COLOR", "")

	statuses := []string{"SUCCESS", "FAILURE", "UNSTABLE", "ABORTED", "NOT_BUILT", "BUILDING", "UNKNOWN"}
	for _, s := range statuses {
		got := ColorStatus(s)
		assert.Equal(t, s, got, "non-TTY should return plain text for %s", s)
	}
}

// --------------- LogStreamer ---------------

func TestLogStreamerComplete(t *testing.T) {
	call := 0
	fetch := func(jobPath string, number int, start int64) (string, int64, bool, error) {
		call++
		switch call {
		case 1:
			return "chunk1\n", 10, true, nil
		case 2:
			return "chunk2\n", 20, false, nil
		default:
			t.Fatal("unexpected extra call")
			return "", 0, false, nil
		}
	}

	var buf bytes.Buffer
	s := NewLogStreamer(fetch, "/job/test", 1, &buf)
	s.pollInterval = time.Millisecond

	err := s.Stream(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "chunk1\nchunk2\n", buf.String())
	assert.Equal(t, 2, call)
}

func TestLogStreamerCancel(t *testing.T) {
	fetch := func(jobPath string, number int, start int64) (string, int64, bool, error) {
		return "text", 10, true, nil
	}

	var buf bytes.Buffer
	s := NewLogStreamer(fetch, "/job/test", 1, &buf)
	s.pollInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := s.Stream(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// --------------- SanitizeLog ---------------

func TestSanitizeLogStripsAnnotations(t *testing.T) {
	input := "normal line\n\x1b[8mha:////4BAhbGluZXM=\x1b[0m[Pipeline] stage\nnext line\n"
	got := SanitizeLog(input)
	assert.NotContains(t, got, "\x1b[8m")
	assert.NotContains(t, got, "ha:////")
	assert.Contains(t, got, "normal line")
	assert.Contains(t, got, "[Pipeline] stage")
	assert.Contains(t, got, "next line")
}

func TestSanitizeLogNoAnnotations(t *testing.T) {
	input := "clean log line 1\nclean log line 2\n"
	got := SanitizeLog(input)
	assert.Equal(t, input, got)
}

func TestSanitizeLogMultipleAnnotations(t *testing.T) {
	input := "\x1b[8mha:////abc\x1b[0mtext1\x1b[8mha:////def\x1b[0mtext2\n"
	got := SanitizeLog(input)
	assert.Equal(t, "text1text2\n", got)
	assert.NotContains(t, got, "\x1b[8m")
}

func TestSanitizeLogEmpty(t *testing.T) {
	assert.Equal(t, "", SanitizeLog(""))
}

func TestSanitizeLogOnlyAnnotation(t *testing.T) {
	input := "\x1b[8mha:////4BAhbGluZXM=\x1b[0m"
	got := SanitizeLog(input)
	assert.Equal(t, "", got)
}

func TestLogStreamerError(t *testing.T) {
	fetchErr := fmt.Errorf("connection refused")
	fetch := func(jobPath string, number int, start int64) (string, int64, bool, error) {
		return "", 0, false, fetchErr
	}

	var buf bytes.Buffer
	s := NewLogStreamer(fetch, "/job/test", 1, &buf)
	s.pollInterval = time.Millisecond

	err := s.Stream(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, fetchErr))
	assert.Contains(t, err.Error(), "streaming log")
}
