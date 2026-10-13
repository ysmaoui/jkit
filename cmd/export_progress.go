package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/ysmaoui/jkit/internal/export"
)

// A build can have thousands of steps, so off a terminal a read-by-step export
// prints about this many step lines, however many steps there are.
const maxPlainProgressLines = 15

// exportProgress reports step log reads on w. On a terminal it keeps one
// status line, redrawn in place; elsewhere it prints the first and last step
// and an even spread between them, so a captured stderr stays small.
type exportProgress struct {
	w     io.Writer
	tty   bool
	width int
	dirty bool
}

// rewriteInPlace: --verbose writes HTTP lines to stderr from worker
// goroutines, which would glue onto a half-drawn status line. TERM=dumb takes
// no escape codes. NO_COLOR is about color only, so it does not matter here.
func rewriteInPlace(isTTY bool, termName string, verbose bool) bool {
	return isTTY && termName != "dumb" && !verbose
}

func newExportProgress(f *os.File, verbose bool) *exportProgress {
	p := &exportProgress{w: f}
	fd := int(f.Fd())
	if rewriteInPlace(term.IsTerminal(fd), os.Getenv("TERM"), verbose) {
		p.tty = true
		p.width = ttyWidth(fd)
	}
	return p
}

func (p *exportProgress) report(i, total int, n *export.Node, s *export.Step) {
	if s == nil {
		p.line("fetching console log", true)
		return
	}
	every := (total + maxPlainProgressLines - 1) / maxPlainProgressLines
	keep := i == 1 || i == total || i%every == 0
	p.line(fmt.Sprintf("fetching step %d/%d: %s", i, total, stepLabel(n, s)), keep)
}

func (p *exportProgress) line(text string, keep bool) {
	if !p.tty {
		if keep {
			_, _ = fmt.Fprintln(p.w, text)
		}
		return
	}
	// A line as wide as the terminal wraps, and \r then returns only to the
	// start of the last row. Width is in cells: CJK and emoji take two.
	if p.width > 1 {
		text = runewidth.Truncate(text, p.width-1, "…")
	}
	_, _ = fmt.Fprintf(p.w, "\r\x1b[K%s", text)
	p.dirty = true
}

// clear erases the status line; call it before any other write to the same
// stream so the text does not land beside a half-drawn status.
func (p *exportProgress) clear() {
	if p.dirty {
		_, _ = fmt.Fprint(p.w, "\r\x1b[K")
		p.dirty = false
	}
}

func (p *exportProgress) notice(msg string) {
	p.clear()
	_, _ = fmt.Fprintln(p.w, msg)
}

func ttyWidth(fd int) int {
	if w, _, err := term.GetSize(fd); err == nil && w > 0 {
		return w
	}
	return 80
}
