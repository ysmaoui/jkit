package jenkins

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// render turns hunks back into the textual unified diff the command prints, so
// a test asserts on what a reader sees rather than on the intermediate shape.
func render(hunks []DiffHunk) string {
	var b strings.Builder
	for _, h := range hunks {
		b.WriteString(h.Header() + "\n")
		for _, line := range h.Lines {
			b.WriteString(line.Op + line.Text + "\n")
		}
	}
	return b.String()
}

func lines(s string) []string { return splitConfigLines([]byte(s)) }

func TestDiffIdenticalRevisionsHaveNoHunks(t *testing.T) {
	config := "<flow-definition>\n  <disabled>false</disabled>\n</flow-definition>\n"
	d := DiffConfigRevisions("team/svc", "2026-07-24_13-06-30", "2026-08-27_14-58-13", []byte(config), []byte(config))
	assert.Empty(t, d.Hunks)
	assert.False(t, d.MaskedOnly, "no change at all is not a masked change")
}

// A trailing newline is not a difference: every config.xml has one, and a
// revision pair that only disagreed about it would report a phantom change.
func TestDiffIgnoresTrailingNewline(t *testing.T) {
	d := DiffConfigRevisions("team/svc", "a", "b", []byte("<x/>\n"), []byte("<x/>"))
	assert.Empty(t, d.Hunks)
}

func TestDiffOneChangedLine(t *testing.T) {
	before := lines("<flow-definition>\n  <keepDependencies>false</keepDependencies>\n  <numToKeep>10</numToKeep>\n  <disabled>false</disabled>\n</flow-definition>")
	after := lines("<flow-definition>\n  <keepDependencies>false</keepDependencies>\n  <numToKeep>50</numToKeep>\n  <disabled>false</disabled>\n</flow-definition>")

	hunks := UnifiedDiff(before, after)
	require.Len(t, hunks, 1)
	assert.Equal(t, "@@ -1,5 +1,5 @@", hunks[0].Header())
	assert.Equal(t, `@@ -1,5 +1,5 @@
 <flow-definition>
   <keepDependencies>false</keepDependencies>
-  <numToKeep>10</numToKeep>
+  <numToKeep>50</numToKeep>
   <disabled>false</disabled>
 </flow-definition>
`, render(hunks))
}

// Line numbers are the whole point of a hunk header: they say where in the file
// to look. An off-by-one here sends the reader to the wrong element.
func TestDiffHunkHeaderCountsFromTheChange(t *testing.T) {
	before := make([]string, 20)
	for i := range before {
		before[i] = "<line>" + strconv.Itoa(i) + "</line>"
	}
	after := append([]string(nil), before...)
	after[11] = "<line>changed</line>"

	hunks := UnifiedDiff(before, after)
	require.Len(t, hunks, 1)
	assert.Equal(t, "@@ -9,7 +9,7 @@", hunks[0].Header())
}

func TestDiffSeparatesDistantChangesAndMergesCloseOnes(t *testing.T) {
	before := make([]string, 40)
	for i := range before {
		before[i] = strconv.Itoa(i)
	}

	far := append([]string(nil), before...)
	far[2], far[30] = "x", "y"
	assert.Len(t, UnifiedDiff(before, far), 2, "changes 28 lines apart cannot share context")

	near := append([]string(nil), before...)
	near[2], near[6] = "x", "y"
	assert.Len(t, UnifiedDiff(before, near), 1, "changes 4 lines apart share their context")
}

// A pure insertion has no line on the old side; unified numbering points at the
// line it follows, and 0 when it is inserted before the first one.
func TestDiffPureInsertion(t *testing.T) {
	hunks := UnifiedDiff(nil, lines("<a/>\n<b/>"))
	require.Len(t, hunks, 1)
	assert.Equal(t, "@@ -0,0 +1,2 @@", hunks[0].Header())

	hunks = UnifiedDiff(lines("<a/>\n<b/>"), nil)
	require.Len(t, hunks, 1)
	assert.Equal(t, "@@ -1,2 +0,0 @@", hunks[0].Header())
}

// Two configs with nothing in common must still produce a diff rather than an
// allocation the size of their product.
func TestDiffFallsBackOnHugeInputs(t *testing.T) {
	before := make([]string, 2100)
	after := make([]string, 2100)
	for i := range before {
		before[i] = "old " + strconv.Itoa(i)
		after[i] = "new " + strconv.Itoa(i)
	}

	hunks := UnifiedDiff(before, after)
	require.Len(t, hunks, 1)
	assert.Equal(t, "@@ -1,2100 +1,2100 @@", hunks[0].Header())
	assert.Equal(t, DiffDelete, hunks[0].Lines[0].Op)
	assert.Equal(t, DiffAdd, hunks[0].Lines[len(hunks[0].Lines)-1].Op)
}

// Trap 2 of jk-2gc.6: with only Item.EXTENDED_READ the controller masks secrets
// on the way out and re-encrypts stored ones on every save, so a pair of
// revisions can differ without the job having been reconfigured.
func TestDiffFlagsAMaskedOnlyChange(t *testing.T) {
	tests := map[string]struct {
		before, after string
		want          bool
	}{
		"re-encrypted secret": {
			"<passphrase>{AQAAABAAAAAgKGFrZQ==}</passphrase>",
			"<passphrase>{AQAAABAAAAAgb3RoZXI=}</passphrase>",
			true,
		},
		"mask beside an untouched line": {
			"<numToKeep>10</numToKeep>\n<authToken>{AQAAABAAAAAgKGFrZQ==}</authToken>",
			"<numToKeep>10</numToKeep>\n<authToken>********</authToken>",
			true,
		},
		"a real change alongside a masked one": {
			"<authToken>{AQAAABAAAAAgKGFrZQ==}</authToken>\n<numToKeep>10</numToKeep>",
			"<authToken>********</authToken>\n<numToKeep>50</numToKeep>",
			false,
		},
		"a masked value that was deleted outright": {
			"<authToken>********</authToken>\n<numToKeep>10</numToKeep>",
			"<numToKeep>10</numToKeep>",
			false,
		},
		"a real change on its own": {
			"<numToKeep>10</numToKeep>",
			"<numToKeep>50</numToKeep>",
			false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			d := DiffConfigRevisions("team/svc", "a", "b", []byte(tt.before), []byte(tt.after))
			require.NotEmpty(t, d.Hunks)
			assert.Equal(t, tt.want, d.MaskedOnly)
		})
	}
}
