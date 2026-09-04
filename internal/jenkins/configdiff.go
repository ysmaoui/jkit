package jenkins

import (
	"regexp"
	"strconv"
	"strings"
)

// Diff operations, matching the leading character of a unified diff line.
const (
	DiffContext = " "
	DiffDelete  = "-"
	DiffAdd     = "+"
)

// diffContextLines is the unified-diff default. config.xml nests deeply, so the
// surrounding lines are usually the only clue which element a change sits in.
const diffContextLines = 3

// maxDiffCells bounds the O(n*m) table. Beyond it the differing region is
// reported as one replacement instead: a coarse but correct diff costs less
// than allocating gigabytes for a config nobody reads line by line.
const maxDiffCells = 4_000_000

// ConfigRevisionDiff is the difference between two stored revisions of one
// job's config.xml. No hunks means the two revisions are byte-identical.
type ConfigRevisionDiff struct {
	Job string `json:"job"`
	// From and To are plugin timestamps, From always the older of the pair.
	From       string     `json:"from"`
	To         string     `json:"to"`
	Hunks      []DiffHunk `json:"hunks"`
	MaskedOnly bool       `json:"maskedOnly"`
}

// DiffHunk is one unified-diff hunk. Starts are 1-based line numbers.
type DiffHunk struct {
	OldStart int        `json:"oldStart"`
	OldLines int        `json:"oldLines"`
	NewStart int        `json:"newStart"`
	NewLines int        `json:"newLines"`
	Lines    []DiffLine `json:"lines"`
}

// DiffLine is one line of a hunk.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Header renders the @@ line.
func (h DiffHunk) Header() string {
	return "@@ -" + rangeSpec(h.OldStart, h.OldLines) + " +" + rangeSpec(h.NewStart, h.NewLines) + " @@"
}

func rangeSpec(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

// DiffConfigRevisions compares two stored config.xml revisions. The caller
// passes them in either order; older first is not enforced here.
func DiffConfigRevisions(job, from, to string, oldConfig, newConfig []byte) *ConfigRevisionDiff {
	hunks := UnifiedDiff(splitConfigLines(oldConfig), splitConfigLines(newConfig))
	return &ConfigRevisionDiff{
		Job:        job,
		From:       from,
		To:         to,
		Hunks:      hunks,
		MaskedOnly: maskedOnly(hunks),
	}
}

// splitConfigLines splits on newlines and drops the trailing empty element a
// file ending in a newline produces, so an identical pair yields no hunks.
func splitConfigLines(data []byte) []string {
	s := strings.TrimSuffix(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// UnifiedDiff returns the hunks turning old into new, with the usual three
// lines of context around each run of changes.
func UnifiedDiff(old, updated []string) []DiffHunk {
	return groupHunks(diffLines(old, updated), diffContextLines)
}

// diffLines returns the whole edit script, one entry per line of either side.
func diffLines(old, updated []string) []DiffLine {
	prefix := 0
	for prefix < len(old) && prefix < len(updated) && old[prefix] == updated[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(updated)-prefix &&
		old[len(old)-1-suffix] == updated[len(updated)-1-suffix] {
		suffix++
	}

	ops := make([]DiffLine, 0, len(old)+len(updated))
	for _, line := range old[:prefix] {
		ops = append(ops, DiffLine{Op: DiffContext, Text: line})
	}
	ops = append(ops, middleOps(old[prefix:len(old)-suffix], updated[prefix:len(updated)-suffix])...)
	for _, line := range old[len(old)-suffix:] {
		ops = append(ops, DiffLine{Op: DiffContext, Text: line})
	}
	return ops
}

func middleOps(old, updated []string) []DiffLine {
	n, m := len(old), len(updated)
	if n == 0 || m == 0 || n*m > maxDiffCells {
		ops := make([]DiffLine, 0, n+m)
		for _, line := range old {
			ops = append(ops, DiffLine{Op: DiffDelete, Text: line})
		}
		for _, line := range updated {
			ops = append(ops, DiffLine{Op: DiffAdd, Text: line})
		}
		return ops
	}

	// Longest common subsequence, filled from the end so the walk below can run
	// forwards and keep the edit script in file order.
	width := m + 1
	lcs := make([]int32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case old[i] == updated[j]:
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
				lcs[i*width+j] = lcs[(i+1)*width+j]
			default:
				lcs[i*width+j] = lcs[i*width+j+1]
			}
		}
	}

	ops := make([]DiffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case old[i] == updated[j]:
			ops = append(ops, DiffLine{Op: DiffContext, Text: old[i]})
			i++
			j++
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			ops = append(ops, DiffLine{Op: DiffDelete, Text: old[i]})
			i++
		default:
			ops = append(ops, DiffLine{Op: DiffAdd, Text: updated[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, DiffLine{Op: DiffDelete, Text: old[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, DiffLine{Op: DiffAdd, Text: updated[j]})
	}
	return ops
}

// groupHunks cuts the edit script into hunks, keeping context lines around each
// run of changes and merging runs that are close enough to share them.
func groupHunks(ops []DiffLine, context int) []DiffHunk {
	oldNo := make([]int, len(ops))
	newNo := make([]int, len(ops))
	oldLine, newLine := 0, 0
	changed := false
	for i, op := range ops {
		if op.Op != DiffAdd {
			oldLine++
		}
		if op.Op != DiffDelete {
			newLine++
		}
		oldNo[i], newNo[i] = oldLine, newLine
		if op.Op != DiffContext {
			changed = true
		}
	}
	if !changed {
		return nil
	}

	var hunks []DiffHunk
	for i := 0; i < len(ops); {
		if ops[i].Op == DiffContext {
			i++
			continue
		}
		start := max(0, i-context)
		end := i
		// Extend while the next change is near enough that the two runs would
		// otherwise print overlapping context.
		for end < len(ops) {
			next := end
			for next < len(ops) && ops[next].Op == DiffContext {
				next++
			}
			if next >= len(ops) || next-end > 2*context {
				break
			}
			end = next + 1
		}
		end = min(len(ops), end+context)
		hunks = append(hunks, newHunk(ops[start:end], oldNo[start:end], newNo[start:end]))
		i = end
	}
	return hunks
}

func newHunk(ops []DiffLine, oldNo, newNo []int) DiffHunk {
	h := DiffHunk{Lines: ops}
	for i, op := range ops {
		if op.Op != DiffAdd {
			if h.OldLines == 0 {
				h.OldStart = oldNo[i]
			}
			h.OldLines++
		}
		if op.Op != DiffDelete {
			if h.NewLines == 0 {
				h.NewStart = newNo[i]
			}
			h.NewLines++
		}
	}
	// A hunk with no line on one side is a pure insertion or deletion. The
	// unified format then numbers it by the line it follows, which is 0 when the
	// hunk starts the file; the running counters already hold exactly that.
	if h.OldLines == 0 {
		h.OldStart = oldNo[0]
	}
	if h.NewLines == 0 {
		h.NewStart = newNo[0]
	}
	return h
}

// hiddenValue matches a value the reader cannot see: the ******** mask Jenkins
// substitutes for a secret when the caller only holds Item.EXTENDED_READ, and a
// Secret ciphertext, which the controller re-encrypts with a fresh IV every
// time the job is saved.
var hiddenValue = regexp.MustCompile(`\*{4,}|\{[A-Za-z0-9+/=]{16,}\}`)

// maskedOnly reports whether the two revisions are identical once every hidden
// value is blanked out. Such a diff is not evidence that the configuration
// changed: an untouched job that stores a secret differs this way on every
// save. A line that has no counterpart is a real edit, so the two sides must
// line up one for one.
func maskedOnly(hunks []DiffHunk) bool {
	var removed, added []string
	hidden := false
	for _, h := range hunks {
		for _, line := range h.Lines {
			if line.Op == DiffContext {
				continue
			}
			blanked := hiddenValue.ReplaceAllString(line.Text, "#")
			hidden = hidden || blanked != line.Text
			if line.Op == DiffDelete {
				removed = append(removed, blanked)
			} else {
				added = append(added, blanked)
			}
		}
	}
	if !hidden || len(removed) != len(added) {
		return false
	}
	for i := range removed {
		if removed[i] != added[i] {
			return false
		}
	}
	return true
}
