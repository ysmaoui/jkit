package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ysmaoui/jkit/internal/staplertest"
)

// TestConsoleLogFollowBodyCutMidCharacter covers Stapler 2029 to 2049 when
// the log grows during an answer: the body is cut after the reported size,
// here inside the two bytes of "é". The cut line is not written; the next
// poll sends it whole.
func TestConsoleLogFollowBodyCutMidCharacter(t *testing.T) {
	var mu sync.Mutex
	logs := []struct{ before, grown string }{
		{"a\n\xc3", "a\n\xc3\xa9b\n"},
		{"a\n\xc3\xa9b\n", "a\n\xc3\xa9b\n"},
	}
	i := 0
	open := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/json" {
			staplertest.WriteRoot(w, staplertest.Uncounted)
			return
		}
		l := logs[min(i, len(logs)-1)]
		i++
		staplertest.WriteProgressiveDuring(w, r, staplertest.Uncounted, l.before, l.grown, open)
	}))
	defer srv.Close()

	l := NewClient(srv.URL, "u", "t").ConsoleLog("app", 1)
	var out strings.Builder
	_, err := l.Read(context.Background(), &out)
	require.NoError(t, err)
	assert.Equal(t, "a\n", out.String())
	mu.Lock()
	open = false
	mu.Unlock()
	more, err := l.Read(context.Background(), &out)
	require.NoError(t, err)
	assert.False(t, more)
	assert.Equal(t, "a\néb\n", out.String())
}
