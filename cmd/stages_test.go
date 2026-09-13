package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWarnIfNoAgentsStaysQuietWhenAnyStageReportsOne(t *testing.T) {
	var buf bytes.Buffer
	warnIfNoAgents(&buf, []stageInfo{{ID: "1"}, {ID: "2", Agent: "pod-abc"}})
	assert.Empty(t, buf.String())
}

func TestWarnIfNoAgentsNamesBothCauses(t *testing.T) {
	var buf bytes.Buffer
	warnIfNoAgents(&buf, []stageInfo{{ID: "1"}, {ID: "2"}})
	out := buf.String()
	assert.Contains(t, out, "no node")
	assert.Contains(t, out, "Blue Ocean")
}
