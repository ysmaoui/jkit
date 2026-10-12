package export

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ysmaoui/jkit/internal/output"
)

// maxLineBytes bounds what a sanitizer holds while waiting for a line end. A
// longer line is sanitized in pieces split at rune starts, so a note or escape
// sequence that spans a piece boundary is left as text.
const maxLineBytes = 1 << 20

// encodeLog runs what fill writes through sanitizing, the head/tail cap and
// gzip into base64. The Log holds whatever arrived even when fill fails.
func encodeLog(maxBytes int64, fill func(io.Writer) error) (Log, error) {
	var out strings.Builder
	b64 := base64.NewEncoder(base64.StdEncoding, &out)
	gz := gzip.NewWriter(b64)
	ht := &headTail{w: gz, max: maxBytes}
	san := &sanitizer{w: ht}

	fillErr := fill(san)
	// The writers below only fail when the one beneath them does, and the
	// bottom one is a strings.Builder.
	_ = san.flush()
	_ = ht.close()
	_ = gz.Close()
	_ = b64.Close()

	l := Log{Bytes: ht.total, CutBytes: ht.cut}
	if ht.total > 0 {
		l.GzipBase64 = out.String()
	}
	return l, fillErr
}

// sanitizer drops console notes, escape sequences and control characters
// other than tab, line by line, and ends every line in LF. Notes are dropped
// whatever the source: plain progressiveText (before Jenkins 2.534) keeps them.
type sanitizer struct {
	w    io.Writer
	line []byte
}

func (s *sanitizer) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			s.line = append(s.line, p...)
			if len(s.line) >= maxLineBytes {
				if err := s.emit(runeSafeLen(s.line), false); err != nil {
					return 0, err
				}
			}
			break
		}
		s.line = append(s.line, p[:i]...)
		p = p[i+1:]
		if err := s.emit(len(s.line), true); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func (s *sanitizer) flush() error {
	if len(s.line) == 0 {
		return nil
	}
	return s.emit(len(s.line), false)
}

// emit writes the first n bytes of the pending line and keeps the rest.
func (s *sanitizer) emit(n int, eol bool) error {
	line := s.line[:n:n]
	switch {
	case !hasControl(line):
	case !hasControl(line[:n-1]) && line[n-1] == '\r':
		// CRLF from a Windows agent: the only control byte, so no regexps.
		line = line[:n-1]
	default:
		line = []byte(output.StripControl(output.SanitizeLog(string(line))))
	}
	if eol {
		line = append(line, '\n')
	}
	_, err := s.w.Write(line)
	s.line = append(s.line[:0], s.line[n:]...)
	return err
}

// runeSafeLen is len(b) less a UTF-8 sequence that b ends in the middle of,
// so a cut there does not split a character.
func runeSafeLen(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax+1; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}

// hasControl is the cheap test that lets most lines skip the regexps.
func hasControl(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return true
		}
	}
	return false
}

// headTail passes the first max/2 bytes through, keeps the last max-max/2 in
// a ring, and on close writes one marker line for what fell between, then
// the tail from its first whole line. Neither side of a cut splits a UTF-8
// character. max 0 passes everything through.
type headTail struct {
	w     io.Writer
	max   int64
	total int64
	cut   int64

	headLeft int64
	started  bool
	// held is the end of the head, not yet written: if a cut follows, a
	// character it starts may have to be dropped.
	held     []byte
	lastHead byte
	ring     []byte
	pos      int
	full     bool
}

func (h *headTail) Write(p []byte) (int, error) {
	n := len(p)
	h.total += int64(n)
	if n == 0 {
		return 0, nil
	}
	if h.max <= 0 {
		_, err := h.w.Write(p)
		return n, err
	}
	if !h.started {
		h.started = true
		h.headLeft = h.max / 2
	}
	if h.headLeft > 0 {
		k := int(min(h.headLeft, int64(len(p))))
		h.headLeft -= int64(k)
		h.held = append(h.held, p[:k]...)
		p = p[k:]
		if keep := utf8.UTFMax - 1; len(h.held) > keep {
			out := h.held[:len(h.held)-keep]
			if _, err := h.w.Write(out); err != nil {
				return 0, err
			}
			h.lastHead = out[len(out)-1]
			h.held = append(h.held[:0], h.held[len(out):]...)
		}
	}
	if len(p) == 0 {
		return n, nil
	}
	if h.ring == nil {
		h.ring = make([]byte, h.max-h.max/2)
	}
	if len(p) >= len(h.ring) {
		copy(h.ring, p[len(p)-len(h.ring):])
		h.pos, h.full = 0, true
		return n, nil
	}
	k := copy(h.ring[h.pos:], p)
	copy(h.ring, p[k:])
	if h.pos+len(p) >= len(h.ring) {
		h.full = true
	}
	h.pos = (h.pos + len(p)) % len(h.ring)
	return n, nil
}

func (h *headTail) close() error {
	var tail []byte
	switch {
	case h.ring == nil:
	case !h.full:
		tail = h.ring[:h.pos]
	default:
		tail = append(h.ring[h.pos:len(h.ring):len(h.ring)], h.ring[:h.pos]...)
		h.cut = h.total - (h.max / 2) - int64(len(tail))
	}
	if h.cut == 0 {
		if _, err := h.w.Write(h.held); err != nil {
			return err
		}
		_, err := h.w.Write(tail)
		return err
	}

	keep := runeSafeLen(h.held)
	h.cut += int64(len(h.held) - keep)
	if _, err := h.w.Write(h.held[:keep]); err != nil {
		return err
	}
	if keep > 0 {
		h.lastHead = h.held[keep-1]
	}
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
		h.cut += int64(i + 1)
	}
	for k := 0; k < utf8.UTFMax-1 && len(tail) > 0 && !utf8.RuneStart(tail[0]); k++ {
		tail = tail[1:]
		h.cut++
	}
	marker := fmt.Sprintf("[jkit: %d bytes cut]\n", h.cut)
	if h.lastHead != '\n' {
		marker = "\n" + marker
	}
	if _, err := io.WriteString(h.w, marker); err != nil {
		return err
	}
	_, err := h.w.Write(tail)
	return err
}
