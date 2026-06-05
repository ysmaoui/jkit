package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logServer serves Jenkins progressiveText for a fixed body, honoring the
// `start` offset and streaming the whole remainder in one response (as real
// Jenkins does). building controls the X-More-Data header.
func logServer(t *testing.T, body string, building bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/logText/progressiveText") {
			http.NotFound(w, r)
			return
		}
		start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		if start < 0 || start > int64(len(body)) {
			start = int64(len(body))
		}
		w.Header().Set("X-Text-Size", strconv.Itoa(len(body)))
		if building {
			w.Header().Set("X-More-Data", "true")
		}
		_, _ = w.Write([]byte(body[start:]))
	}))
}

// TestGetBuildLogPagesBeyondCap is the core regression: a completed build whose
// log exceeds the per-request cap (and has no X-More-Data) must page fully via
// Offset, not stop after the first chunk.
func TestGetBuildLogPagesBeyondCap(t *testing.T) {
	body := strings.Repeat("a", maxLogChunk) + "TAIL\n"
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	var got strings.Builder
	var offset int64
	iterations := 0
	for {
		chunk, err := client.GetBuildLog("test", 42, offset)
		require.NoError(t, err)
		got.WriteString(chunk.Text)
		require.Greater(t, chunk.Offset, offset, "offset must advance")
		offset = chunk.Offset
		iterations++
		if !chunk.HasMore {
			break
		}
		require.Less(t, iterations, 10, "should converge quickly")
	}

	assert.Equal(t, body, got.String(), "paging must reconstruct the whole log")
	assert.GreaterOrEqual(t, iterations, 2, "log larger than cap must take multiple chunks")
}

func TestGetBuildLogOffsetIsBytesRead(t *testing.T) {
	body := "hello world\n"
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	chunk, err := client.GetBuildLog("test", 42, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), chunk.Offset)
	assert.False(t, chunk.HasMore, "completed log fully read => no more")
}

func TestGetBuildLogBuildingHasMore(t *testing.T) {
	body := "partial\n"
	srv := logServer(t, body, true)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	chunk, err := client.GetBuildLog("test", 42, 0)
	require.NoError(t, err)
	assert.True(t, chunk.HasMore, "in-progress build reports more data")
}

func TestGetBuildLogSize(t *testing.T) {
	body := strings.Repeat("x", 1234)
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	size, err := client.GetBuildLogSize("test", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), size)
}

func TestGetBuildLogTailTrimsPartialLine(t *testing.T) {
	body := "line1\nline2\nline3\n"
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	// Window smaller than the log => starts mid-stream, partial first line dropped.
	text, err := client.GetBuildLogTail("test", 42, 8)
	require.NoError(t, err)
	assert.NotContains(t, text, "line1", "partial leading line must be trimmed")
	assert.Contains(t, text, "line3")
}

func TestGetBuildLogTailWholeWhenWindowExceedsSize(t *testing.T) {
	body := "line1\nline2\n"
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	text, err := client.GetBuildLogTail("test", 42, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, body, text, "window larger than log returns everything untrimmed")
}

func TestGetBuildLogTailPagesAcrossCap(t *testing.T) {
	// Tail window larger than the per-request cap must still read fully.
	body := "HEAD\n" + strings.Repeat("b", maxLogChunk) + "END\n"
	srv := logServer(t, body, false)
	defer srv.Close()
	client := NewClient(srv.URL, "u", "t")

	text, err := client.GetBuildLogTail("test", 42, int64(maxLogChunk)+100)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(text, "END\n"))
	assert.Greater(t, len(text), maxLogChunk, "tail window spanning chunks reads all of it")
}
