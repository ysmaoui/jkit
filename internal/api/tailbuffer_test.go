package api

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTailBufferKeepsLastBytes writes random chunks, some longer than the
// buffer, and checks it holds exactly the last size bytes in that much room.
func TestTailBufferKeepsLastBytes(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, size := range []int{1, 7, 64} {
		buf := &tailBuffer{max: size}
		var all strings.Builder
		for range 500 {
			chunk := strings.Repeat(string(rune('a'+r.Intn(26))), r.Intn(2*size+2))
			_, _ = buf.Write([]byte(chunk))
			all.WriteString(chunk)
			want := all.String()[max(all.Len()-size, 0):]
			require.Equal(t, want, buf.String(), "size %d", size)
			require.Equal(t, all.Len() > size, buf.dropped())
			require.LessOrEqual(t, len(buf.ring), size)
		}
	}
}
