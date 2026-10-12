//go:build unix

package cmd

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Jenkins aborts a build by sending SIGTERM to its step's processes.
func TestExportHTMLSIGTERMLeavesNoFile(t *testing.T) {
	h := &htmlRun{slowStep: 5 * time.Second}
	h.onStep = func() {
		p, err := os.FindProcess(os.Getpid())
		require.NoError(t, err)
		require.NoError(t, p.Signal(syscall.SIGTERM))
	}
	h.serve(t)

	_, _, dir, err := runHTMLExport(t, "export", "html", "my-app", "7")
	assert.Equal(t, 130, exitCode(t, err))
	assert.Empty(t, dirNames(t, dir))
}
